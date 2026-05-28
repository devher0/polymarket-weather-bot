// stability.go — TASK-184: in-process forecast stability tracker.
//
// Maintains a rolling window of probability estimates for each conditionID
// across evaluation cycles.  High variance (stddev > 0.15) indicates that the
// model is flip-flopping — data sources disagree or the cache is stale — and
// the confidence should be penalised before committing capital.
//
// Usage in EvaluateFused / aggregator:
//
//	GlobalStability.Track(conditionID, city, signal, ourP)
//	if GlobalStability.IsUnstable(conditionID) {
//	    ff.Confidence *= 0.80  // penalise flip-flopping markets
//	}
package collectors

import (
	"math"
	"sync"
	"time"
)

const (
	// stabilityWindow is the max number of observations kept per conditionID.
	stabilityWindow = 10
	// stabilityThreshold is the stddev above which a market is considered unstable.
	stabilityThreshold = 0.15
)

// stabilityObs is one recorded observation for a conditionID.
type stabilityObs struct {
	ourP      float64
	timestamp time.Time
}

// stabilityEntry holds the rolling window for one conditionID.
type stabilityEntry struct {
	city   string
	signal string
	obs    []stabilityObs // oldest first
}

// StabilityTracker maintains per-conditionID probability history.
// All methods are safe for concurrent use.
type StabilityTracker struct {
	mu      sync.RWMutex
	entries map[string]*stabilityEntry
}

// NewStabilityTracker allocates a new tracker.
func NewStabilityTracker() *StabilityTracker {
	return &StabilityTracker{entries: make(map[string]*stabilityEntry)}
}

// GlobalStability is the package-level tracker used by the bot's main loop.
var GlobalStability = NewStabilityTracker()

// Track records a new probability estimate for conditionID.
func (t *StabilityTracker) Track(conditionID, city, signal string, ourP float64) {
	if conditionID == "" {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()

	e, ok := t.entries[conditionID]
	if !ok {
		e = &stabilityEntry{city: city, signal: signal}
		t.entries[conditionID] = e
	}
	e.obs = append(e.obs, stabilityObs{ourP: ourP, timestamp: time.Now()})
	if len(e.obs) > stabilityWindow {
		e.obs = e.obs[len(e.obs)-stabilityWindow:]
	}
}

// Stability returns the standard deviation of the last N probability estimates
// for conditionID.  Returns 0.0 when fewer than 2 observations exist.
func (t *StabilityTracker) Stability(conditionID string) float64 {
	t.mu.RLock()
	e, ok := t.entries[conditionID]
	t.mu.RUnlock()
	if !ok || len(e.obs) < 2 {
		return 0.0
	}
	return obsStddev(e.obs)
}

// IsUnstable returns true when the probability stddev exceeds the threshold.
func (t *StabilityTracker) IsUnstable(conditionID string) bool {
	return t.Stability(conditionID) > stabilityThreshold
}

// Snapshot returns a copy of all tracked entries for display purposes.
// Each returned map value is [city, signal, n, mean, stddev, lastP].
type StabilitySnapshot struct {
	ConditionID string
	City        string
	Signal      string
	N           int
	Mean        float64
	Stddev      float64
	LastP       float64
	Unstable    bool
}

// Snapshot returns all tracked conditionIDs with their stability metrics.
func (t *StabilityTracker) Snapshot() []StabilitySnapshot {
	t.mu.RLock()
	defer t.mu.RUnlock()

	out := make([]StabilitySnapshot, 0, len(t.entries))
	for id, e := range t.entries {
		if len(e.obs) < 2 {
			continue
		}
		sd := obsStddev(e.obs)
		mean := 0.0
		for _, o := range e.obs {
			mean += o.ourP
		}
		mean /= float64(len(e.obs))
		out = append(out, StabilitySnapshot{
			ConditionID: id,
			City:        e.city,
			Signal:      e.signal,
			N:           len(e.obs),
			Mean:        mean,
			Stddev:      sd,
			LastP:       e.obs[len(e.obs)-1].ourP,
			Unstable:    sd > stabilityThreshold,
		})
	}
	return out
}

// obsStddev computes the population standard deviation of probability observations.
func obsStddev(obs []stabilityObs) float64 {
	if len(obs) < 2 {
		return 0
	}
	mean := 0.0
	for _, o := range obs {
		mean += o.ourP
	}
	mean /= float64(len(obs))
	variance := 0.0
	for _, o := range obs {
		d := o.ourP - mean
		variance += d * d
	}
	return math.Sqrt(variance / float64(len(obs)))
}
