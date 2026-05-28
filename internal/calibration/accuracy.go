package calibration

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"sync"
	"time"
)

// CityAccuracyRecord stores accuracy data for a single city.
type CityAccuracyRecord struct {
	Predictions []float64 `json:"predictions"` // our predicted probabilities
	Outcomes    []bool    `json:"outcomes"`    // true = bet won, false = lost
	Timestamp   []int64   `json:"timestamp"`   // Unix timestamps
}

// CityStats holds aggregated accuracy metrics for a city.
type CityStats struct {
	City       string
	BrierScore float64
	Count      int
	Status     string // "excellent", "good", "fair", "poor"
}

var (
	// In-memory cache of city accuracy; keyed by city name.
	// Protected by cityAccuracyMu.
	cityAccuracyCache sync.Map // map[string]CityAccuracyRecord
	cityAccuracyMu    sync.Mutex
)

// RecordCityAccuracy appends a (prediction, outcome) pair for a city.
// Called after a market is resolved.
func RecordCityAccuracy(city string, ourP float64, outcome bool, dataRoot string) error {
	if city == "" {
		return fmt.Errorf("city name empty")
	}

	// Clamp probability to [0.01, 0.99] for numeric stability.
	ourP = math.Max(0.01, math.Min(0.99, ourP))

	now := time.Now().UTC().Unix()

	// Load current records from disk (if any).
	path := accuracyPath(city, dataRoot)
	rec := CityAccuracyRecord{}
	if data, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(data, &rec)
	}

	// Append new data.
	rec.Predictions = append(rec.Predictions, ourP)
	rec.Outcomes = append(rec.Outcomes, outcome)
	rec.Timestamp = append(rec.Timestamp, now)

	// Keep only last 100 records (rolling window for storage efficiency).
	const maxRecords = 100
	if len(rec.Predictions) > maxRecords {
		excess := len(rec.Predictions) - maxRecords
		rec.Predictions = rec.Predictions[excess:]
		rec.Outcomes = rec.Outcomes[excess:]
		rec.Timestamp = rec.Timestamp[excess:]
	}

	// Save to disk.
	if err := os.MkdirAll(accuracyDir(dataRoot), 0o755); err != nil {
		return err
	}
	data, _ := json.MarshalIndent(rec, "", "  ")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return err
	}

	return nil
}

// CityAccuracy computes Brier score for a city.
func CityAccuracy(city, dataRoot string) (brierScore float64, count int, err error) {
	path := accuracyPath(city, dataRoot)
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, 0, err
	}

	var rec CityAccuracyRecord
	if err := json.Unmarshal(data, &rec); err != nil {
		return 0, 0, err
	}

	if len(rec.Predictions) != len(rec.Outcomes) {
		return 0, 0, fmt.Errorf("mismatch: %d predictions vs %d outcomes", len(rec.Predictions), len(rec.Outcomes))
	}

	count = len(rec.Predictions)
	if count == 0 {
		return 0, 0, nil
	}

	sumSquaredError := 0.0
	for i, pred := range rec.Predictions {
		outcome := 0.0
		if rec.Outcomes[i] {
			outcome = 1.0
		}
		diff := pred - outcome
		sumSquaredError += diff * diff
	}

	brierScore = sumSquaredError / float64(count)
	return brierScore, count, nil
}

// LoadCityAccuracies loads aggregated accuracy stats for all cities.
// Returns a map keyed by city name, with Brier scores and counts.
func LoadCityAccuracies(dataRoot string) map[string]CityStats {
	dir := accuracyDir(dataRoot)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return map[string]CityStats{}
	}

	result := make(map[string]CityStats)
	for _, e := range entries {
		if e.IsDir() || len(e.Name()) < 6 {
			continue // Skip directories and invalid names (must be at least "X.json")
		}

		// Extract city name from filename: "{city}.json".
		if e.Name()[len(e.Name())-5:] != ".json" {
			continue
		}
		city := e.Name()[:len(e.Name())-5]

		brier, count, err := CityAccuracy(city, dataRoot)
		if err != nil || count == 0 {
			continue
		}

		// Classify status based on Brier score.
		status := "poor"
		switch {
		case brier < 0.10:
			status = "excellent"
		case brier < 0.15:
			status = "good"
		case brier < 0.20:
			status = "fair"
		}

		result[city] = CityStats{
			City:       city,
			BrierScore: brier,
			Count:      count,
			Status:     status,
		}
	}

	return result
}

// accuracyPath returns the file path for a city's accuracy data.
func accuracyPath(city, dataRoot string) string {
	dir := accuracyDir(dataRoot)
	return dir + "/" + city + ".json"
}

// accuracyDir returns the accuracy data directory.
func accuracyDir(dataRoot string) string {
	if dataRoot == "" || dataRoot == "." {
		return "data/city_accuracy"
	}
	return dataRoot + "/data/city_accuracy"
}
