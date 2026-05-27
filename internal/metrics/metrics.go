// Package metrics exposes a Prometheus-style text/plain metrics endpoint over
// HTTP using only the standard library (no external Prometheus client needed).
//
// Exported counters and gauges:
//
//   bets_placed_total    — cumulative bets placed (from bets_history.csv)
//   bets_won_total       — cumulative bets won (resolved, outcome=true)
//   brier_score          — current Brier score (0 = perfect)
//   edge_avg             — average (ourP − marketPrice) on won bets
//   bankroll_usdc        — sum of SizeUSDC for unresolved bets (money at risk)
//
// Usage:
//
//   metrics.Start(":9090", ".")   // start HTTP server in background
package metrics

import (
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/devher0/polymarket-weather-bot/internal/calibration"
)

// snapshot holds computed metric values for one scrape.
type snapshot struct {
	BetsPlaced  int
	BetsWon     int
	BrierScore  float64
	EdgeAvg     float64
	Bankroll    float64
}

// collect computes current metrics from the bets_history CSV.
func collect(dataRoot string) snapshot {
	records, err := calibration.LoadHistory(dataRoot)
	if err != nil {
		slog.Warn("metrics: failed to load history", "err", err)
		return snapshot{}
	}

	var placed, won int
	var bankroll float64
	var edgeSum float64
	var edgeCount int

	for _, r := range records {
		placed++
		if r.Outcome == nil {
			// Unresolved = money still at risk.
			bankroll += r.SizeUSDC
		} else if *r.Outcome {
			won++
			edgeSum += r.OurProbability - r.MarketPrice
			edgeCount++
		}
	}

	score, _, _ := calibration.BrierScore(records)

	edgeAvg := 0.0
	if edgeCount > 0 {
		edgeAvg = edgeSum / float64(edgeCount)
	}

	return snapshot{
		BetsPlaced: placed,
		BetsWon:    won,
		BrierScore: score,
		EdgeAvg:    edgeAvg,
		Bankroll:   bankroll,
	}
}

// handler responds with Prometheus exposition format (text/plain; version=0.0.4).
func handler(dataRoot string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		s := collect(dataRoot)

		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		w.WriteHeader(http.StatusOK)

		lines := []string{
			"# HELP bets_placed_total Total number of bets placed (from bets_history.csv)",
			"# TYPE bets_placed_total counter",
			fmt.Sprintf("bets_placed_total %d", s.BetsPlaced),

			"# HELP bets_won_total Total number of resolved bets that were won",
			"# TYPE bets_won_total counter",
			fmt.Sprintf("bets_won_total %d", s.BetsWon),

			"# HELP brier_score Current Brier score over resolved bets (lower=better, 0=perfect, 0.25=random)",
			"# TYPE brier_score gauge",
			fmt.Sprintf("brier_score %g", s.BrierScore),

			"# HELP edge_avg Average edge (ourProbability - marketPrice) on won bets",
			"# TYPE edge_avg gauge",
			fmt.Sprintf("edge_avg %g", s.EdgeAvg),

			"# HELP bankroll_usdc USDC currently at risk in unresolved bets",
			"# TYPE bankroll_usdc gauge",
			fmt.Sprintf("bankroll_usdc %g", s.Bankroll),
		}

		for _, l := range lines {
			fmt.Fprintln(w, l)
		}
	}
}

// Start launches the /metrics HTTP server on addr (e.g. ":9090") in a
// background goroutine.  dataRoot is the repo root ("." when running normally).
func Start(addr, dataRoot string) {
	mux := http.NewServeMux()
	mux.HandleFunc("/metrics", handler(dataRoot))
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprintln(w, "ok")
	})

	srv := &http.Server{
		Addr:         addr,
		Handler:      mux,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
	}

	go func() {
		slog.Info("metrics server started", "addr", addr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Warn("metrics server error", "err", err)
		}
	}()
}
