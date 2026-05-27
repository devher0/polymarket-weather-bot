// Package metrics exposes a Prometheus-style text/plain metrics endpoint over
// HTTP using only the standard library (no external Prometheus client needed).
//
// Exported counters and gauges:
//
//	bets_placed_total    — cumulative bets placed (from bets_history.csv)
//	bets_won_total       — cumulative bets won (resolved, outcome=true)
//	brier_score          — current Brier score (0 = perfect)
//	edge_avg             — average (ourP − marketPrice) on won bets
//	bankroll_usdc        — sum of SizeUSDC for unresolved bets (money at risk)
//
// Additionally exposes:
//
//	GET /healthz  — JSON health check for Docker/k8s liveness probes (TASK-051)
//	GET /metrics  — Prometheus exposition format
//
// Usage:
//
//	metrics.Start(":9090", ".")      // start HTTP server in background
//	metrics.UpdateCycle(betsPlaced)  // call after each bot cycle
package metrics

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/devher0/polymarket-weather-bot/internal/calibration"
)

// ── Runtime state (updated by main.go via UpdateCycle) ────────────────────

// botState holds live runtime counters for the /healthz endpoint.
// All fields are updated atomically so the HTTP handler can read them
// without taking a lock.
type botState struct {
	startTime    time.Time
	lastCycleAt  atomic.Int64 // Unix timestamp (0 = never)
	cycleCount   atomic.Int64
	betsPlaced   atomic.Int64
	loopSec      atomic.Int64 // base loop interval for "degraded" detection
}

// global singleton — initialised when the package is first imported.
var state = &botState{startTime: time.Now()}

// UpdateCycle records that one bot cycle just completed and N bets were placed.
// It is safe to call from any goroutine.
func UpdateCycle(betsPlacedThisCycle int) {
	state.lastCycleAt.Store(time.Now().Unix())
	state.cycleCount.Add(1)
	state.betsPlaced.Add(int64(betsPlacedThisCycle))
}

// SetLoopSec stores the configured base loop interval so /healthz can compute
// the "degraded" threshold (last_cycle_at > 2×loop_sec ago).
func SetLoopSec(sec int) {
	state.loopSec.Store(int64(sec))
}

// ── snapshot holds computed metric values for one scrape ─────────────────

type snapshot struct {
	BetsPlaced   int
	BetsWon      int
	OpenPositions int
	BrierScore   float64
	EdgeAvg      float64
	Bankroll     float64
}

// collect computes current metrics from the bets_history CSV.
func collect(dataRoot string) snapshot {
	records, err := calibration.LoadHistory(dataRoot)
	if err != nil {
		slog.Warn("metrics: failed to load history", "err", err)
		return snapshot{}
	}

	var placed, won, open int
	var bankroll float64
	var edgeSum float64
	var edgeCount int

	for _, r := range records {
		placed++
		if r.Outcome == nil {
			// Unresolved = money still at risk.
			bankroll += r.SizeUSDC
			open++
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
		BetsPlaced:    placed,
		BetsWon:       won,
		OpenPositions: open,
		BrierScore:    score,
		EdgeAvg:       edgeAvg,
		Bankroll:      bankroll,
	}
}

// ── /metrics handler ──────────────────────────────────────────────────────

// metricsHandler responds with Prometheus exposition format (text/plain; version=0.0.4).
func metricsHandler(dataRoot string) http.HandlerFunc {
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

// ── /healthz handler (TASK-051) ───────────────────────────────────────────

// healthzPayload is the JSON body returned by GET /healthz.
type healthzPayload struct {
	Status         string  `json:"status"`           // "ok" | "degraded"
	UptimeSec      int64   `json:"uptime_s"`
	LastCycleAt    string  `json:"last_cycle_at"`    // RFC3339 or "never"
	Cycles         int64   `json:"cycles"`
	BetsPlaced     int64   `json:"bets_placed"`
	OpenPositions  int     `json:"open_positions"`
	BankrollUSDC   float64 `json:"bankroll_usdc"`
}

// healthzHandler returns a JSON summary of the bot's runtime health.
// status = "degraded" when no cycle has completed within 2×loopSec.
func healthzHandler(dataRoot string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		now := time.Now()
		uptime := int64(now.Sub(state.startTime).Seconds())

		lastTS := state.lastCycleAt.Load()
		var lastStr string
		var statusOK = true

		if lastTS == 0 {
			lastStr = "never"
			// Only flag degraded if loop is configured and enough time has passed.
			ls := state.loopSec.Load()
			if ls > 0 && uptime > 2*ls {
				statusOK = false
			}
		} else {
			lastTime := time.Unix(lastTS, 0)
			lastStr = lastTime.UTC().Format(time.RFC3339)
			// Degraded when last cycle is older than 2×loopSec.
			ls := state.loopSec.Load()
			if ls > 0 && now.Sub(lastTime) > time.Duration(2*ls)*time.Second {
				statusOK = false
			}
		}

		// Load open positions / bankroll from CSV (lightweight).
		snap := collect(dataRoot)

		status := "ok"
		httpCode := http.StatusOK
		if !statusOK {
			status = "degraded"
			httpCode = http.StatusServiceUnavailable
		}

		payload := healthzPayload{
			Status:        status,
			UptimeSec:     uptime,
			LastCycleAt:   lastStr,
			Cycles:        state.cycleCount.Load(),
			BetsPlaced:    state.betsPlaced.Load(),
			OpenPositions: snap.OpenPositions,
			BankrollUSDC:  snap.Bankroll,
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(httpCode)
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		_ = enc.Encode(payload)
	}
}

// ── Start ─────────────────────────────────────────────────────────────────

// Start launches the metrics HTTP server on addr (e.g. ":9090") in a
// background goroutine.  dataRoot is the repo root ("." when running normally).
// The returned *http.Server can be shut down gracefully via srv.Shutdown(ctx).
func Start(addr, dataRoot string) *http.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("/metrics", metricsHandler(dataRoot))
	mux.HandleFunc("/healthz", healthzHandler(dataRoot))
	// Keep legacy /health for backwards compat.
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
		slog.Info("metrics server started", "addr", addr, "healthz", addr+"/healthz")
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Warn("metrics server error", "err", err)
		}
	}()

	return srv
}
