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
	"github.com/devher0/polymarket-weather-bot/internal/collectors"
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

// ── /healthz handler (TASK-051, extended by TASK-108) ────────────────────

// sourceStatus is one entry in the healthz "sources" map.
type sourceStatus struct {
	OK          bool   `json:"ok"`           // true when last successful fetch < 3 s ago via ping
	LastSuccess string `json:"last_success"` // RFC3339 or "never"
	ConsecFails int    `json:"consec_fails"`
}

// healthzPayload is the JSON body returned by GET /healthz.
// TASK-108: expanded with per-source status, brier_score, last_bet_at.
type healthzPayload struct {
	Status        string                  `json:"status"`         // "ok" | "degraded"
	UptimeSec     int64                   `json:"uptime_s"`
	LastCycleAt   string                  `json:"last_cycle_at"`  // RFC3339 or "never"
	LastBetAt     string                  `json:"last_bet_at"`    // RFC3339 or "never"
	Cycles        int64                   `json:"cycles"`
	BetsPlaced    int64                   `json:"bets_placed"`
	OpenPositions int                     `json:"open_positions"`
	BankrollUSDC  float64                 `json:"bankroll_usdc"`
	BrierScore    float64                 `json:"brier_score"`    // 0=perfect; -1 if no resolved bets
	Sources       map[string]sourceStatus `json:"sources"`
}

// pingURL does a HEAD/GET to the given URL with a 3-second timeout.
func pingURL(rawURL string) bool {
	c := &http.Client{Timeout: 3 * time.Second}
	resp, err := c.Get(rawURL)
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode < 500
}

// sourceProbes maps source names to a lightweight probe URL.
// A successful response (HTTP < 500) within 3 s → source is reachable.
var sourceProbes = map[string]string{
	"openmeteo": "https://api.open-meteo.com/v1/forecast?latitude=52.52&longitude=13.41&daily=temperature_2m_max&forecast_days=1&timezone=UTC",
	"nasa":      "https://power.larc.nasa.gov/api/temporal/daily/point?parameters=T2M&community=RE&longitude=13.41&latitude=52.52&start=20240101&end=20240101&format=JSON",
	"noaa":      "https://api.weather.gov/points/40.71,-74.01",
	"goes":      "https://noaa-goes19.s3.amazonaws.com/",
}

// buildSourceStatuses reads the persisted SourceHealth records and returns a
// status map.  It does NOT issue live probes during the HTTP request so as to
// stay fast; the persistent health records (written by collectors on each cycle)
// are used instead.  A source is "ok" when its last successful fetch was < 1 h ago.
func buildSourceStatuses(dataRoot string) map[string]sourceStatus {
	health := collectors.LoadSourceHealth(dataRoot)
	out := make(map[string]sourceStatus, len(sourceProbes))
	now := time.Now()
	for name := range sourceProbes {
		h, found := health[name]
		ss := sourceStatus{ConsecFails: h.ConsecFails}
		if found && !h.LastSuccess.IsZero() {
			ss.LastSuccess = h.LastSuccess.UTC().Format(time.RFC3339)
			ss.OK = now.Sub(h.LastSuccess) < time.Hour
		} else {
			ss.LastSuccess = "never"
		}
		out[name] = ss
	}
	return out
}

// lastBetTime scans bets_history.csv for the most recent bet timestamp.
func lastBetTime(dataRoot string) string {
	records, err := calibration.LoadHistory(dataRoot)
	if err != nil || len(records) == 0 {
		return "never"
	}
	var latest time.Time
	for _, r := range records {
		if r.Timestamp.After(latest) {
			latest = r.Timestamp
		}
	}
	if latest.IsZero() {
		return "never"
	}
	return latest.UTC().Format(time.RFC3339)
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
			ls := state.loopSec.Load()
			if ls > 0 && uptime > 2*ls {
				statusOK = false
			}
		} else {
			lastTime := time.Unix(lastTS, 0)
			lastStr = lastTime.UTC().Format(time.RFC3339)
			ls := state.loopSec.Load()
			if ls > 0 && now.Sub(lastTime) > time.Duration(2*ls)*time.Second {
				statusOK = false
			}
		}

		snap := collect(dataRoot)

		// Brier score: -1 when no resolved bets exist.
		brierVal := -1.0
		if records, err := calibration.LoadHistory(dataRoot); err == nil {
			if score, count, err := calibration.BrierScore(records); err == nil && count > 0 {
				brierVal = score
			}
		}

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
			LastBetAt:     lastBetTime(dataRoot),
			Cycles:        state.cycleCount.Load(),
			BetsPlaced:    state.betsPlaced.Load(),
			OpenPositions: snap.OpenPositions,
			BankrollUSDC:  snap.Bankroll,
			BrierScore:    brierVal,
			Sources:       buildSourceStatuses(dataRoot),
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
