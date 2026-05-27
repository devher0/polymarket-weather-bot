// Polymarket Weather Bot
// Usage:
//   go run ./cmd/bot              — dry run (no real orders)
//   go run ./cmd/bot --live       — real money mode
//   go run ./cmd/bot --loop 3600  — repeat every N seconds
package main

import (
	"flag"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"time"

	"github.com/joho/godotenv"

	"github.com/devher0/polymarket-weather-bot/internal/markets"
	"github.com/devher0/polymarket-weather-bot/internal/strategy"
	"github.com/devher0/polymarket-weather-bot/internal/weather"
)

func main() {
	live := flag.Bool("live", false, "Disable dry-run (real money)")
	loop := flag.Int("loop", 0, "Repeat interval in seconds (0 = run once)")
	flag.Parse()

	_ = godotenv.Load()

	dryRun := !*live
	if dryRun {
		slog.Info("DRY RUN mode — no real orders will be placed")
	} else {
		slog.Warn("LIVE MODE — real money!")
	}

	maxBet := envFloat("MAX_BET_USDC", 5.0)
	minEdge := envFloat("MIN_EDGE", 0.05)

	run := func() {
		slog.Info("=== cycle start", "time", time.Now().Format(time.RFC3339))

		// 1. Fetch weather forecasts for all cities
		forecasts := make(map[string][]weather.Forecast)
		for city := range weather.Cities {
			fc, err := weather.GetForecast(city, 3)
			if err != nil {
				slog.Warn("forecast failed", "city", city, "err", err)
				continue
			}
			forecasts[city] = fc
			f := fc[0]
			slog.Info("forecast",
				"city", city,
				"max_c", fmt.Sprintf("%.1f", f.MaxTempC),
				"precip_mm", fmt.Sprintf("%.1f", f.PrecipitationMM),
				"rain_p", fmt.Sprintf("%.2f", weather.RainProbability(f)),
			)
		}

		// 2. Discover weather markets
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

		// 3. Evaluate and place bets
		placed := 0
		for _, m := range mkt {
			d := strategy.Evaluate(m, forecasts, 100.0, minEdge, maxBet)
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
				} else {
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
		for range time.Tick(t) {
			run()
		}
	}
}

// placeBet submits an order to Polymarket CLOB.
// TODO: implement via py-clob-client or direct CLOB REST + EIP-712 signing.
func placeBet(d *strategy.Decision) error {
	// Polymarket CLOB requires:
	//   1. EIP-712 signed order (ethers / go-ethereum)
	//   2. POST /order with L1/L2 auth headers
	// See TASKS.md TASK-013 for full implementation.
	slog.Warn("placeBet not yet implemented — see TASK-013", "token", d.TokenID)
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
