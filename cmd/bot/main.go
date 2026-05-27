// Polymarket Weather Bot
// Usage:
//   go run ./cmd/bot                   — dry run (no real orders)
//   go run ./cmd/bot --live            — real money mode
//   go run ./cmd/bot --loop 3600       — repeat every N seconds
//   go run ./cmd/bot --collect-history — download 90-day historical data
package main

import (
	"flag"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"time"

	"github.com/joho/godotenv"

	"github.com/devher0/polymarket-weather-bot/internal/calibration"
	"github.com/devher0/polymarket-weather-bot/internal/collectors"
	"github.com/devher0/polymarket-weather-bot/internal/markets"
	"github.com/devher0/polymarket-weather-bot/internal/notifier"
	"github.com/devher0/polymarket-weather-bot/internal/polymarket"
	"github.com/devher0/polymarket-weather-bot/internal/strategy"
	"github.com/devher0/polymarket-weather-bot/internal/weather"
)

func main() {
	live           := flag.Bool("live", false, "Disable dry-run (real money)")
	loop           := flag.Int("loop", 0, "Repeat interval in seconds (0 = run once)")
	collectHistory := flag.Bool("collect-history", false, "Download 90-day historical data and exit")
	testTelegram   := flag.Bool("test-telegram", false, "Send a test Telegram message and exit")
	flag.Parse()

	_ = godotenv.Load()

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
		if err := collectors.CollectHistory("."); err != nil {
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
	calibration.PrintBrierScore(".")

	maxBet  := envFloat("MAX_BET_USDC", 5.0)
	minEdge := envFloat("MIN_EDGE", 0.05)

	run := func() {
		slog.Info("=== cycle start", "time", time.Now().Format(time.RFC3339))

		// 1. Fetch fused forecasts from all sources (aggregator)
		fusedForecasts, err := collectors.AggregateAll(".")
		if err != nil {
			slog.Warn("aggregator failed, falling back to OpenMeteo only", "err", err)
		}

		// 2. Also keep plain OpenMeteo map for fallback Evaluate()
		legacyForecasts := make(map[string][]weather.Forecast)
		for city := range weather.Cities {
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

		// 3. Discover weather markets
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

		// 4. Evaluate and place bets
		placed := 0
		for _, m := range mkt {
			var d *strategy.Decision

			// Prefer fused forecast; fall back to legacy OpenMeteo
			if ff, ok := fusedForecasts[m.City]; ok {
				d = strategy.EvaluateFused(m, ff, 100.0, minEdge, maxBet)
			}
			if d == nil {
				d = strategy.Evaluate(m, legacyForecasts, 100.0, minEdge, maxBet)
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
					// Record bet for calibration tracking
					if err := calibration.SaveBet(d, "."); err != nil {
						slog.Warn("calibration save failed", "err", err)
					}
					// Notify via Telegram
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

	if *loop > 0 {
		t := time.Duration(*loop) * time.Second
		slog.Info("loop mode", "interval", t)

		lastDigest := time.Time{}
		for range time.Tick(t) {
			run()

			// Send daily digest at ~09:00 UTC
			now := time.Now().UTC()
			if now.Hour() == 9 && now.Sub(lastDigest) > 23*time.Hour {
				if err := notifier.DailyDigest("."); err != nil {
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

func envFloat(key string, def float64) float64 {
	if v := os.Getenv(key); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			return f
		}
	}
	return def
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
