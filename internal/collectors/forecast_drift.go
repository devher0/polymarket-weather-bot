// forecast_drift.go — tracks how much forecasts change between fetch cycles.
//
// Meteorological insight: a forecast that keeps shifting is less reliable than
// a stable one. We accumulate a rolling history of per-fetch shifts and derive
// a DriftFactor ∈ [0.70, 1.00] that scales down confidence for unstable cities.
//
// TASK-125
package collectors

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"time"
)

// driftHistoryMax is the maximum number of drift records stored per city+dayOffset.
const driftHistoryMax = 10

// DriftRecord captures the magnitude of a single forecast shift.
type DriftRecord struct {
	Timestamp        time.Time `json:"timestamp"`
	AbsDeltaTempC    float64   `json:"abs_delta_temp_c"`
	AbsDeltaPrecipPt float64   `json:"abs_delta_precip_pt"` // percentage points (0-100)
}

// driftHistoryFile returns the path for city+dayOffset drift history.
func driftHistoryFile(city string, dayOffset int, dataRoot string) string {
	if dataRoot == "" {
		dataRoot = "."
	}
	dir := filepath.Join(dataRoot, "data", "drift")
	return filepath.Join(dir, fmt.Sprintf("%s_d%d.json", city, dayOffset))
}

// loadDriftHistory reads stored drift records for city+dayOffset.
// Returns an empty slice (not an error) when no file exists yet.
func loadDriftHistory(city string, dayOffset int, dataRoot string) []DriftRecord {
	data, err := os.ReadFile(driftHistoryFile(city, dayOffset, dataRoot))
	if err != nil {
		return nil
	}
	var records []DriftRecord
	if err := json.Unmarshal(data, &records); err != nil {
		slog.Warn("forecast_drift: corrupt history file, resetting",
			"city", city, "day_offset", dayOffset, "err", err)
		return nil
	}
	return records
}

// saveDriftHistory writes records to disk, capping at driftHistoryMax.
func saveDriftHistory(city string, dayOffset int, dataRoot string, records []DriftRecord) {
	path := driftHistoryFile(city, dayOffset, dataRoot)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		slog.Warn("forecast_drift: mkdir failed", "err", err)
		return
	}
	data, err := json.MarshalIndent(records, "", "  ")
	if err != nil {
		slog.Warn("forecast_drift: marshal failed", "err", err)
		return
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		slog.Warn("forecast_drift: write failed", "path", path, "err", err)
	}
}

// RecordDrift appends a new drift observation derived from a ForecastShift.
// Older records beyond driftHistoryMax are evicted (FIFO).
// No-op when shift is nil.
func RecordDrift(city string, dayOffset int, shift *ForecastShift, dataRoot string) {
	if shift == nil {
		return
	}
	rec := DriftRecord{
		Timestamp:        time.Now().UTC(),
		AbsDeltaTempC:    math.Abs(shift.DeltaMaxTempC),
		AbsDeltaPrecipPt: math.Abs(shift.DeltaPrecipP),
	}
	records := loadDriftHistory(city, dayOffset, dataRoot)
	records = append(records, rec)
	if len(records) > driftHistoryMax {
		records = records[len(records)-driftHistoryMax:]
	}
	saveDriftHistory(city, dayOffset, dataRoot, records)
	slog.Debug("forecast_drift: recorded",
		"city", city,
		"day_offset", dayOffset,
		"abs_delta_temp", fmt.Sprintf("%.1f", rec.AbsDeltaTempC),
		"abs_delta_precip_pt", fmt.Sprintf("%.1f", rec.AbsDeltaPrecipPt),
	)
}

// ComputeDriftFactor converts a slice of drift records into a confidence
// multiplier ∈ [0.70, 1.00].
//
// Algorithm:
//  1. For each record compute a normalised instability score:
//     instability_i = clamp(|ΔTemp|/10 + |ΔPrecip%|/40, 0, 1)
//     (|ΔTemp|=10°C or |ΔPrecip|=40 pp each contribute a full 1.0 instability)
//  2. Apply exponential weighting: most-recent record gets weight 1.0, each
//     older record loses 20% (weight = 0.8^age_index).
//  3. DriftFactor = clamp(1.0 − 0.30 × weighted_avg_instability, 0.70, 1.00)
//
// Returns 1.00 when records is empty (no history → assume stable).
func ComputeDriftFactor(records []DriftRecord) float64 {
	if len(records) == 0 {
		return 1.0
	}

	const (
		tempNorm   = 10.0 // °C shift at which temperature contributes instability=1
		precipNorm = 40.0 // percentage-point shift at which precip contributes 1
		decay      = 0.80 // weight decay per step going back in history
		maxPenalty = 0.30 // maximum confidence reduction (1.00 → 0.70)
		minFactor  = 0.70
	)

	var weightedSum, totalWeight float64
	n := len(records)
	for i, r := range records {
		// index 0 is oldest; index n-1 is newest (most weight)
		age := n - 1 - i
		w := math.Pow(decay, float64(age))

		instability := r.AbsDeltaTempC/tempNorm + r.AbsDeltaPrecipPt/precipNorm
		if instability > 1.0 {
			instability = 1.0
		}

		weightedSum += instability * w
		totalWeight += w
	}

	avgInstability := weightedSum / totalWeight
	factor := 1.0 - maxPenalty*avgInstability
	if factor < minFactor {
		factor = minFactor
	}
	return factor
}

// DriftFactor loads the drift history for city+dayOffset and returns the
// confidence multiplier. Returns 1.00 on first call (no history yet).
func DriftFactor(city string, dayOffset int, dataRoot string) float64 {
	records := loadDriftHistory(city, dayOffset, dataRoot)
	f := ComputeDriftFactor(records)
	if f < 1.0 {
		slog.Debug("forecast_drift: applying factor",
			"city", city,
			"day_offset", dayOffset,
			"factor", fmt.Sprintf("%.3f", f),
			"history_len", len(records),
		)
	}
	return f
}

// LoadDriftSummary returns the last drift record and current factor for
// city+dayOffset; used by the dashboard drift sub-command.
// Returns (zero DriftRecord, 1.0) when no history exists.
func LoadDriftSummary(city string, dayOffset int, dataRoot string) (DriftRecord, float64) {
	records := loadDriftHistory(city, dayOffset, dataRoot)
	factor := ComputeDriftFactor(records)
	if len(records) == 0 {
		return DriftRecord{}, factor
	}
	return records[len(records)-1], factor
}
