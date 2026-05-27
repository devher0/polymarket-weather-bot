// Polymarket Weather Bot
// Usage:
//   go run ./cmd/bot                         — dry run (no real orders)
//   go run ./cmd/bot --live                  — real money mode
//   go run ./cmd/bot --loop 3600             — repeat every N seconds
//   go run ./cmd/bot --collect-history       — download 90-day historical data
//   go run ./cmd/bot --config path/to/config.yaml  — use a specific config file
package main

import (
	"flag"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/joho/godotenv"

	"github.com/devher0/polymarket-weather-bot/config"
	"github.com/devher0/polymarket-weather-bot/internal/calibration"
	"github.com/devher0/polymarket-weather-bot/internal/collectors"
	"github.com/devher0/polymarket-weather-bot/internal/markets"
	"github.com/devher0/polymarket-weather-bot/internal/metrics"
	"github.com/devher0/polymarket-weather-bot/internal/notifier"
	"github.com/devher0/polymarket-weather-bot/internal/polymarket"
	"github.com/devher0/polymarket-weather-bot/internal/strategy"
	"github.com/devher0/polymarket-weather-bot/internal/weather"
)

func main() {
	live           := flag.Bool("live", false, "Disable dry-run (real money)")
	loopFlag       := flag.Int("loop", 0, "Repeat interval in seconds (0 = run once; overrides config)")
	collectHistory := flag.Bool("collect-history", false, "Download 90-day historical data and exit")
	testTelegram   := flag.Bool("test-telegram", false, "Send a test Telegram message and exit")
	metricsPortFlag := flag.Int("metrics-port", -1, "Prometheus /metrics port (0=disabled; overrides config)")
	configFile     := flag.String("config", "", "Path to config.yaml (default: config/config.yaml)")
	flag.Parse()

	// Load .env first so ENV vars are available for config overlay.
	_ = godotenv.Load()

	// Load configuration: yaml file + ENV overlay.
	cfgPath := *configFile
	if cfgPath == "" {
		cfgPath = "config/config.yaml"
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		slog.Warn("config load failed, using defaults", "err", err)
		cfg, _ = config.Load("") // pure defaults + ENV
	}

	// CLI flags win over config file for loop and metrics-port.
	if *loopFlag > 0 {
		cfg.LoopSec = *loopFlag
	}
	if *metricsPortFlag >= 0 {
		cfg.MetricsPort = *metricsPortFlag
	}

	// Start the Prometheus metrics server unless disabled.
	if cfg.MetricsPort > 0 {
		metrics.Start(fmt.Sprintf(":%d", cfg.MetricsPort), cfg.DataRoot)
	}

	slog.Info("config loaded",
		"cities", cfg.Cities,
		"min_edge", cfg.MinEdge,
		"max_bet", cfg.MaxBet,
		"loop_sec", cfg.LoopSec,
		"metrics_port", cfg.MetricsPort,
		"data_root", cfg.DataRoot,
	)

	// --- Telegram test mode ---
	if *testTelegram {
		if err := notifier.SendTestMessage(); err != nil {
			slog.Error("Telegram test failed", "err", err)
			os.Exit(1)
		}
		slog.Info("Telegram test message sent successfully")
		return
	}

	// --- Historical collection mode ---
	if *collectHistory {
		slog.Info("collecting 90-day historical data for all cities...")
		if err := collectors.CollectHistory(cfg.DataRoot); err != nil {
			slog.Error("history collection failed", "err", err)
			os.Exit(1)
		}
		slog.Info("historical data collection complete")
		return
	}

	dryRun := !*live
	if dryRun {
		slog.Info("DRY RUN mode — no real orders will be placed")
	} else {
		slog.Warn("LIVE MODE — real money!")
	}

	// Print Brier score from past bets at startup
	calibration.PrintBrierScore(cfg.DataRoot)

	run := func() {
		slog.Info("=== cycle start", "time", time.Now().Format(time.RFC3339))

		// 0. Load open positions to avoid double-betting the same conditionID.
		openPositions, err := calibration.LoadOpenPositions(cfg.DataRoot)
		if err != nil {
			slog.Warn("failed to load open positions, proceeding without dedup", "err", err)
			openPositions = make(map[string]bool)
		}
		slog.Info("open positions loaded", "count", len(openPositions))

		// 1. Fetch fused forecasts from all sources (aggregator)
		fusedForecasts, err := collectors.AggregateAll(cfg.DataRoot)
		if err != nil {
			slog.Warn("aggregator failed, falling back to OpenMeteo only", "err", err)
		}

		// 2. Build active-city set from config.
		activeCity := make(map[string]bool, len(cfg.Cities))
		for _, c := range cfg.Cities {
			activeCity[c] = true
		}

		// 3. Fetch plain OpenMeteo forecasts for fallback Evaluate().
		legacyForecasts := make(map[string][]weather.Forecast)
		for city := range weather.Cities {
			if !activeCity[city] {
				continue
			}
			fc, err := weather.GetForecast(city, 3)
			if err != nil {
				slog.Warn("forecast failed", "city", city, "err", err)
				continue
			}
			legacyForecasts[city] = fc
			f := fc[0]

			confidence := 0.0
			if ff, ok := fusedForecasts[city]; ok {
				confidence = ff.Confidence
			}

			slog.Info("forecast",
				"city", city,
				"max_c", fmt.Sprintf("%.1f", f.MaxTempC),
				"precip_mm", fmt.Sprintf("%.1f", f.PrecipitationMM),
				"rain_p", fmt.Sprintf("%.2f", weather.RainProbability(f)),
				"confidence", fmt.Sprintf("%.2f", confidence),
			)
		}

		// 4. Discover weather markets
		mkt, err := markets.GetWeatherMarkets()
		if err != nil {
			slog.Error("markets fetch failed", "err", err)
			return
		}
		slog.Info("weather markets found", "count", len(mkt))

		if len(mkt) == 0 {
			slog.Warn("no weather markets found on Polymarket right now")
			return
		}

		// 5. Evaluate and place bets
		placed := 0
		for _, m := range mkt {
			// Skip cities not in active set (if city is recognisable).
			if m.City != "" && !activeCity[m.City] {
				continue
			}

			// Skip markets where we already have an open position.
			if openPositions[m.ConditionID] {
				slog.Info("skipped: already have position on", "conditionID", m.ConditionID,
					"question", truncate(m.Question, 60))
				continue
			}

			var d *strategy.Decision

			// Select forecast for the day the market expires.
			dayOffset := m.DaysUntilExpiry()

			var ff *collectors.FusedForecast
			if dayOffset > 0 && m.City != "" {
				dayFF, err := collectors.AggregateForDay(m.City, dayOffset, cfg.DataRoot)
				if err == nil {
					ff = dayFF
				}
			}
			if ff == nil {
				if v, ok := fusedForecasts[m.City]; ok {
					ff = v
				}
			}

			if ff != nil {
				d = strategy.EvaluateFused(m, ff, 100.0, cfg.MinEdge, cfg.MaxBet)
			}
			if d == nil {
				d = strategy.Evaluate(m, legacyForecasts, 100.0, cfg.MinEdge, cfg.MaxBet)
			}
			if d == nil {
				continue
			}

			prefix := ""
			if dryRun {
				prefix = "[DRY RUN] "
			}
			slog.Info(prefix+"bet",
				"side", d.Side,
				"size", fmt.Sprintf("$%.2f", d.SizeUSDC),
				"question", truncate(d.Market.Question, 60),
				"reason", d.Reason,
			)

			if !dryRun {
				if err := placeBet(d); err != nil {
					slog.Error("order failed", "err", err)
					_ = notifier.NotifyError("placeBet", err)
				} else {
					if err := calibration.SaveBet(d, cfg.DataRoot); err != nil {
						slog.Warn("calibration save failed", "err", err)
					}
					if err := notifier.NotifyBet(d); err != nil {
						slog.Warn("telegram notify failed", "err", err)
					}
					placed++
				}
			} else {
				placed++
			}
		}

		if placed == 0 {
			slog.Info("no bets placed (no sufficient edge)")
		} else {
			slog.Info("cycle done", "bets_placed", placed)
		}
	}

	run()

	if cfg.LoopSec > 0 {
		t := time.Duration(cfg.LoopSec) * time.Second
		slog.Info("loop mode", "interval", t)

		// Start the auto-resolver goroutine: checks resolved markets every hour.
		calibration.StartResolver(cfg.DataRoot)

		lastDigest := time.Time{}
		for range time.Tick(t) {
			run()

			// Send daily digest at ~09:00 UTC
			now := time.Now().UTC()
			if now.Hour() == 9 && now.Sub(lastDigest) > 23*time.Hour {
				if err := notifier.DailyDigest(cfg.DataRoot); err != nil {
					slog.Warn("daily digest failed", "err", err)
				} else {
					lastDigest = now
				}
			}
		}
	}
}

// placeBet submits an order to Polymarket CLOB using EIP-712 signing.
func placeBet(d *strategy.Decision) error {
	orderID, err := polymarket.PlaceBet(d)
	if err != nil {
		return fmt.Errorf("placeBet: %w", err)
	}
	slog.Info("order submitted", "orderID", orderID, "token", d.TokenID, "side", d.Side)
	return nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
