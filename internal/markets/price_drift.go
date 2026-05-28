// price_drift.go — TASK-234: post-bet price drift tracker.
//
// After each bet is placed, the bot records the entry price for that
// condition ID. On subsequent bot cycles, the current price is compared
// against the entry price to compute drift in percentage points (pp).
//
// Positive drift means the market moved in our favour (e.g. we bet YES at
// 0.45 and the price rose to 0.52 → drift = +7pp). Negative drift means
// the market moved against us.
//
// Over many bets, the aggregate drift distribution tells us whether our
// edge signal is "predictive" (prices tend to move our way after entry)
// or whether we are buying into adverse flow.
//
// Persistence: data/price_drift.json
package markets

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

const priceDriftFile = "data/price_drift.json"

// DriftRecord represents the price drift for one bet position.
type DriftRecord struct {
	CondID       string    `json:"cond_id"`
	Side         string    `json:"side"`           // "YES" or "NO"
	EntryPrice   float64   `json:"entry_price"`    // price at bet placement (0-1)
	CurrentPrice float64   `json:"current_price"`  // most-recently observed price (0-1); 0 = not yet updated
	DriftPP      float64   `json:"drift_pp"`       // (current - entry) × 100, positive = favourable
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// DriftSummary holds aggregate statistics across all drift records.
type DriftSummary struct {
	Count    int
	Positive int     // records where DriftPP > 0
	Negative int     // records where DriftPP < 0
	AvgDrift float64 // mean DriftPP across all records with CurrentPrice > 0
}

func priceDriftPath(dataRoot string) string {
	if dataRoot == "" {
		dataRoot = "."
	}
	return filepath.Join(dataRoot, priceDriftFile)
}

func loadDriftRecords(dataRoot string) ([]DriftRecord, error) {
	data, err := os.ReadFile(priceDriftPath(dataRoot))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("price_drift: read: %w", err)
	}
	var records []DriftRecord
	if err := json.Unmarshal(data, &records); err != nil {
		return nil, fmt.Errorf("price_drift: parse: %w", err)
	}
	return records, nil
}

func saveDriftRecords(records []DriftRecord, dataRoot string) error {
	path := priceDriftPath(dataRoot)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("price_drift: mkdir: %w", err)
	}
	data, err := json.MarshalIndent(records, "", "  ")
	if err != nil {
		return fmt.Errorf("price_drift: marshal: %w", err)
	}
	return os.WriteFile(path, data, 0o644)
}

// SaveDriftEntry records the entry price for a newly placed bet.
// If a record for condID already exists it is not overwritten (entry price is fixed at placement).
func SaveDriftEntry(condID, side string, entryPrice float64, dataRoot string) error {
	records, err := loadDriftRecords(dataRoot)
	if err != nil {
		return err
	}
	// Check for existing entry.
	for _, r := range records {
		if r.CondID == condID {
			return nil // already recorded
		}
	}
	now := time.Now().UTC()
	records = append(records, DriftRecord{
		CondID:     condID,
		Side:       side,
		EntryPrice: entryPrice,
		CreatedAt:  now,
		UpdatedAt:  now,
	})
	return saveDriftRecords(records, dataRoot)
}

// UpdateDrift refreshes the current price for condID and recomputes DriftPP.
// No-op if condID has no entry record.
func UpdateDrift(condID string, currentPrice float64, dataRoot string) error {
	records, err := loadDriftRecords(dataRoot)
	if err != nil {
		return err
	}
	updated := false
	for i := range records {
		if records[i].CondID != condID {
			continue
		}
		records[i].CurrentPrice = currentPrice
		records[i].DriftPP = (currentPrice - records[i].EntryPrice) * 100
		records[i].UpdatedAt = time.Now().UTC()
		updated = true
		break
	}
	if !updated {
		return nil // unknown condID, nothing to do
	}
	return saveDriftRecords(records, dataRoot)
}

// LoadDriftSummary returns aggregate drift statistics.
// ok is false when there are no records with an observed current price.
func LoadDriftSummary(dataRoot string) (summary DriftSummary, ok bool) {
	records, err := loadDriftRecords(dataRoot)
	if err != nil || len(records) == 0 {
		return DriftSummary{}, false
	}

	var driftSum float64
	updatedCount := 0
	for _, r := range records {
		summary.Count++
		if r.CurrentPrice == 0 {
			continue // not yet updated
		}
		updatedCount++
		driftSum += r.DriftPP
		if r.DriftPP > 0 {
			summary.Positive++
		} else if r.DriftPP < 0 {
			summary.Negative++
		}
	}
	if updatedCount == 0 {
		return summary, false
	}
	summary.AvgDrift = driftSum / float64(updatedCount)
	return summary, true
}
