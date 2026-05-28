// momentum_cache.go — rolling forecast momentum tracker. (TASK-230)
//
// Saves the last 3 FusedForecast snapshots per city and computes whether
// temperature and precipitation are trending up, down, or stable between
// bot cycles. Used by the /momentum Telegram command.
package collectors

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const (
	momentumMaxPoints = 3
	// Thresholds below which the change is considered noise.
	momentumTempDeltaC    = 0.5  // °C
	momentumPrecipDeltaPP = 5.0  // percentage points for precipProb
)

// MomentumPoint is one snapshot saved per forecast cycle.
type MomentumPoint struct {
	Timestamp  time.Time `json:"timestamp"`
	TempC      float64   `json:"temp_c"`       // MaxTempC from FusedForecast
	PrecipMM   float64   `json:"precip_mm"`    // PrecipitationMM
	PrecipProb float64   `json:"precip_prob"`  // PrecipitationProbability (0-100)
}

var momentumMu sync.Mutex

func momentumPath(city, dataRoot string) string {
	if dataRoot == "" {
		dataRoot = "."
	}
	return filepath.Join(dataRoot, "data", "momentum", city+".json")
}

// SaveMomentum records a forecast snapshot into the rolling buffer for the city.
// Only the last momentumMaxPoints snapshots are kept.
func SaveMomentum(city string, ff *FusedForecast, dataRoot string) {
	if ff == nil {
		return
	}
	momentumMu.Lock()
	defer momentumMu.Unlock()

	path := momentumPath(city, dataRoot)
	_ = os.MkdirAll(filepath.Dir(path), 0o755)

	var points []MomentumPoint
	if data, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(data, &points)
	}

	points = append(points, MomentumPoint{
		Timestamp:  time.Now().UTC(),
		TempC:      ff.MaxTempC,
		PrecipMM:   ff.PrecipitationMM,
		PrecipProb: ff.PrecipitationProbability,
	})

	if len(points) > momentumMaxPoints {
		points = points[len(points)-momentumMaxPoints:]
	}

	data, err := json.Marshal(points)
	if err == nil {
		_ = os.WriteFile(path, data, 0o644)
	}
}

// MomentumResult holds computed trend directions for a city.
type MomentumResult struct {
	City      string
	TempDir   string  // "rising" / "falling" / "stable"
	PrecipDir string  // "rising" / "falling" / "stable"
	TempDelta float64 // total change across the window (°C)
	PrecipDelta float64 // total change across the window (pp)
	Points    int     // how many snapshots exist
	OldestAt  time.Time
}

// GetMomentum loads stored snapshots and returns trend direction.
// ok=false when there are fewer than 2 points (not enough history).
func GetMomentum(city, dataRoot string) (MomentumResult, bool) {
	momentumMu.Lock()
	defer momentumMu.Unlock()

	path := momentumPath(city, dataRoot)
	data, err := os.ReadFile(path)
	if err != nil {
		return MomentumResult{City: city}, false
	}

	var points []MomentumPoint
	if err := json.Unmarshal(data, &points); err != nil || len(points) < 2 {
		return MomentumResult{City: city, Points: len(points)}, false
	}

	first := points[0]
	last := points[len(points)-1]

	tempDelta := last.TempC - first.TempC
	precipDelta := last.PrecipProb - first.PrecipProb

	dir := func(delta, threshold float64) string {
		if math.Abs(delta) < threshold {
			return "stable"
		}
		if delta > 0 {
			return "rising"
		}
		return "falling"
	}

	return MomentumResult{
		City:        city,
		TempDir:     dir(tempDelta, momentumTempDeltaC),
		PrecipDir:   dir(precipDelta, momentumPrecipDeltaPP),
		TempDelta:   tempDelta,
		PrecipDelta: precipDelta,
		Points:      len(points),
		OldestAt:    first.Timestamp,
	}, true
}
