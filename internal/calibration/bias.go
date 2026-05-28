// Package calibration — bias tracker for per-(city, signal) probability correction.
//
// After each resolved bet the resolver records (ourP, outcome) in
// data/bias/{city}_{signal}.json (rolling window of 30 entries).
//
// ComputeBias returns mean(ourP - outcome):
//   - positive → we systematically overestimate → subtract from ourP
//   - negative → we systematically underestimate → add to ourP
//   - zero     → insufficient data (< 5 samples)
//
// CorrectProbability applies the correction and clamps to [0.02, 0.98].
// The correction is applied in cmd/bot after Platt calibration.
package calibration

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"sort"
	"time"
)

const (
	biasMaxRecords = 30 // rolling window size per (city, signal)
	BiasMinSamples = 5  // minimum records before correction is active (exported for dashboard)
	biasSubDir     = "data/bias"
)

// BiasRecord is one calibration data point.
type BiasRecord struct {
	OurP    float64   `json:"our_p"`
	Outcome float64   `json:"outcome"` // 1.0 = won, 0.0 = lost
	TS      time.Time `json:"ts"`
}

// BiasSummaryRow is one row for the dashboard bias table.
type BiasSummaryRow struct {
	City        string
	Signal      string
	Bias        float64 // mean(ourP - outcome); positive = we overestimate
	N           int
	Calibration string // "over" | "under" | "ok"
}

func biasFilePath(city, signal, dataRoot string) string {
	return filepath.Join(dataRoot, biasSubDir, city+"_"+signal+".json")
}

// RecordBiasOutcome saves an outcome to the rolling bias history for (city, signal).
// Safe to call with empty city/signal — becomes a no-op.
func RecordBiasOutcome(city, signal string, ourP float64, won bool, dataRoot string) error {
	if city == "" || signal == "" {
		return nil
	}

	path := biasFilePath(city, signal, dataRoot)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}

	records, _ := loadBiasFile(path) // start fresh on read error

	outcome := 0.0
	if won {
		outcome = 1.0
	}
	records = append(records, BiasRecord{OurP: ourP, Outcome: outcome, TS: time.Now().UTC()})

	// Keep newest entries only.
	sort.Slice(records, func(i, j int) bool { return records[i].TS.After(records[j].TS) })
	if len(records) > biasMaxRecords {
		records = records[:biasMaxRecords]
	}

	data, err := json.MarshalIndent(records, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

func loadBiasFile(path string) ([]BiasRecord, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var records []BiasRecord
	if err := json.Unmarshal(data, &records); err != nil {
		return nil, err
	}
	return records, nil
}

// LoadBiasRecords returns stored bias records for (city, signal). Public for testing.
func LoadBiasRecords(city, signal, dataRoot string) ([]BiasRecord, error) {
	return loadBiasFile(biasFilePath(city, signal, dataRoot))
}

// ComputeBias returns mean(ourP - outcome) over the stored records.
// Returns 0 when fewer than BiasMinSamples records are available.
func ComputeBias(city, signal, dataRoot string) float64 {
	records, err := loadBiasFile(biasFilePath(city, signal, dataRoot))
	if err != nil || len(records) < BiasMinSamples {
		return 0
	}
	sum := 0.0
	for _, r := range records {
		sum += r.OurP - r.Outcome
	}
	return sum / float64(len(records))
}

// CorrectProbability applies bias correction to ourP, clamped to [0.02, 0.98].
// Returns (correctedP, bias). When insufficient data: (ourP, 0).
func CorrectProbability(city, signal string, ourP float64, dataRoot string) (float64, float64) {
	bias := ComputeBias(city, signal, dataRoot)
	if bias == 0 {
		return ourP, 0
	}
	corrected := math.Max(0.02, math.Min(0.98, ourP-bias))
	return corrected, bias
}

// LoadBiasSummary scans data/bias/ and returns a summary row per (city, signal) pair
// that has at least BiasMinSamples records.
func LoadBiasSummary(dataRoot string) []BiasSummaryRow {
	dir := filepath.Join(dataRoot, biasSubDir)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}

	var rows []BiasSummaryRow
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		records, err := loadBiasFile(path)
		if err != nil || len(records) < BiasMinSamples {
			continue
		}

		// Strip .json suffix and parse city+signal.
		name := entry.Name()[:len(entry.Name())-5]
		city, signal := splitCitySignal(name)

		sum := 0.0
		for _, r := range records {
			sum += r.OurP - r.Outcome
		}
		bias := sum / float64(len(records))

		cal := "ok"
		switch {
		case bias > 0.05:
			cal = "over"
		case bias < -0.05:
			cal = "under"
		}

		rows = append(rows, BiasSummaryRow{
			City:        city,
			Signal:      signal,
			Bias:        bias,
			N:           len(records),
			Calibration: cal,
		})
	}

	sort.Slice(rows, func(i, j int) bool {
		if rows[i].City != rows[j].City {
			return rows[i].City < rows[j].City
		}
		return rows[i].Signal < rows[j].Signal
	})
	return rows
}

// splitCitySignal splits "new_york_rain" into ("new_york", "rain") by matching
// known signal suffixes. Falls back to last-underscore split for unknown signals.
func splitCitySignal(name string) (city, signal string) {
	knownSignals := []string{"rain", "heat", "cold", "snow", "wind", "sunny", "fog", "humid", "dry", "uv"}
	for _, sig := range knownSignals {
		suffix := "_" + sig
		if len(name) > len(suffix) && name[len(name)-len(suffix):] == suffix {
			return name[:len(name)-len(suffix)], sig
		}
	}
	// Fallback: last underscore.
	for i := len(name) - 1; i >= 0; i-- {
		if name[i] == '_' {
			return name[:i], name[i+1:]
		}
	}
	return name, ""
}
