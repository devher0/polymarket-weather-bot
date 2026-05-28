// source_health.go — per-source availability tracking (TASK-081).
//
// Records each data source fetch attempt (success or failure) and persists
// a health summary to data/source_health.json so the dashboard can display
// real-time availability stats.
//
// TASK-205: circuit breaker — sources with ≥TripThreshold consecutive failures
// are "tripped" and skipped by the aggregator until TripUntil elapses.
package collectors

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// TripThreshold is the number of consecutive failures before a source is tripped.
const TripThreshold = 3

// TripDuration is how long a tripped source is skipped before being retried.
const TripDuration = 15 * time.Minute

// SourceHealth holds aggregate statistics for one data source.
type SourceHealth struct {
	// LastSuccess is the timestamp of the most recent successful fetch (zero if never).
	LastSuccess time.Time `json:"last_success"`
	// LastError is the timestamp of the most recent failed fetch (zero if never errored).
	LastError time.Time `json:"last_error"`
	// LastErrorMsg is the string representation of the most recent error.
	LastErrorMsg string `json:"last_error_msg,omitempty"`
	// ConsecFails is the number of consecutive failed fetches since the last success.
	ConsecFails int `json:"consec_fails"`
	// TotalCalls is the total number of fetch attempts ever recorded.
	TotalCalls int64 `json:"total_calls"`
	// TotalSuccess is the total number of successful fetches ever recorded.
	TotalSuccess int64 `json:"total_success"`
	// TripUntil is the time until which this source is circuit-broken (zero = not tripped).
	TripUntil time.Time `json:"trip_until,omitempty"`
}

// UpRatePct returns the overall success percentage (0-100).
func (h SourceHealth) UpRatePct() float64 {
	if h.TotalCalls == 0 {
		return 0
	}
	return float64(h.TotalSuccess) / float64(h.TotalCalls) * 100
}

// Status returns a human-readable status string based on recency.
func (h SourceHealth) Status(now time.Time) string {
	if h.LastSuccess.IsZero() {
		return "unknown"
	}
	age := now.Sub(h.LastSuccess)
	switch {
	case age < time.Hour:
		return "ok"
	case age < 6*time.Hour:
		return "degraded"
	default:
		return "down"
	}
}

// healthFile is the name of the JSON file stored under dataRoot/data/.
const healthFile = "source_health.json"

var (
	healthMu    sync.Mutex
	healthCache map[string]*SourceHealth // in-memory, lazily loaded
)

// LoadSourceHealth reads the health file from disk and returns the map.
// Returns an empty map (not nil) if the file does not exist.
func LoadSourceHealth(dataRoot string) map[string]SourceHealth {
	healthMu.Lock()
	defer healthMu.Unlock()

	if dataRoot == "" {
		dataRoot = "."
	}
	path := filepath.Join(dataRoot, "data", healthFile)
	data, err := os.ReadFile(path)
	if err != nil {
		return map[string]SourceHealth{}
	}
	var m map[string]SourceHealth
	if err := json.Unmarshal(data, &m); err != nil {
		return map[string]SourceHealth{}
	}
	return m
}

// saveHealth persists the in-memory cache to disk. Caller must hold healthMu.
func saveHealth(dataRoot string) {
	if dataRoot == "" {
		dataRoot = "."
	}
	dir := filepath.Join(dataRoot, "data")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return
	}
	data, err := json.MarshalIndent(healthCache, "", "  ")
	if err != nil {
		return
	}
	path := filepath.Join(dir, healthFile)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		slog.Debug("source_health: write failed", "err", err)
	}
}

// loadCache reads the file into healthCache if not already loaded.
// Caller must hold healthMu.
func loadCache(dataRoot string) {
	if healthCache != nil {
		return
	}
	healthCache = make(map[string]*SourceHealth)
	if dataRoot == "" {
		dataRoot = "."
	}
	path := filepath.Join(dataRoot, "data", healthFile)
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var m map[string]SourceHealth
	if err := json.Unmarshal(data, &m); err != nil {
		return
	}
	for k, v := range m {
		vCopy := v
		healthCache[k] = &vCopy
	}
}

// IsTripped reports whether the given source is currently circuit-broken.
// Returns false for unknown sources or when no dataRoot is available.
func IsTripped(source, dataRoot string) bool {
	healthMu.Lock()
	defer healthMu.Unlock()
	loadCache(dataRoot)
	h, ok := healthCache[source]
	if !ok {
		return false
	}
	return !h.TripUntil.IsZero() && time.Now().UTC().Before(h.TripUntil)
}

// RecordSourceCall updates health stats for the given source name.
// Pass err=nil for a successful fetch, non-nil for a failure.
// dataRoot is the bot's data directory (may be empty → ".").
// TASK-205: sets TripUntil when ConsecFails reaches TripThreshold.
func RecordSourceCall(source string, err error, dataRoot string) {
	healthMu.Lock()
	defer healthMu.Unlock()
	loadCache(dataRoot)

	h, ok := healthCache[source]
	if !ok {
		h = &SourceHealth{}
		healthCache[source] = h
	}

	now := time.Now().UTC()
	h.TotalCalls++
	if err == nil {
		h.LastSuccess = now
		h.ConsecFails = 0
		h.TotalSuccess++
		h.TripUntil = time.Time{} // clear any active trip on recovery
	} else {
		h.LastError = now
		h.LastErrorMsg = fmt.Sprintf("%.200s", err.Error())
		h.ConsecFails++
		if h.ConsecFails >= TripThreshold {
			h.TripUntil = now.Add(TripDuration)
			slog.Warn("source circuit-broken", "source", source, "consec_fails", h.ConsecFails, "trip_until", h.TripUntil.Format(time.RFC3339))
		}
	}

	saveHealth(dataRoot)
}

// HealthSummaryLine returns a short log-friendly line for a source.
func HealthSummaryLine(source string, h SourceHealth) string {
	status := h.Status(time.Now().UTC())
	age := "-"
	if !h.LastSuccess.IsZero() {
		a := time.Since(h.LastSuccess)
		if a < time.Minute {
			age = fmt.Sprintf("%ds ago", int(a.Seconds()))
		} else if a < time.Hour {
			age = fmt.Sprintf("%dm ago", int(a.Minutes()))
		} else {
			age = fmt.Sprintf("%.1fh ago", a.Hours())
		}
	}
	return fmt.Sprintf("%-12s %-8s last_ok=%s up=%.0f%% consec_fails=%d",
		source, status, age, h.UpRatePct(), h.ConsecFails)
}
