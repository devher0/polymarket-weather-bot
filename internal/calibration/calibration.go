// Package calibration tracks bet history and computes probabilistic accuracy metrics.
//
// History is stored in data/bets_history.csv with columns:
//   condition_id, timestamp, side, our_probability, market_price, size_usdc, outcome, resolved_at
//
// outcome is "true", "false", or "" (unresolved).
package calibration

import (
	"encoding/csv"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/devher0/polymarket-weather-bot/internal/strategy"
)

const csvFileName = "data/bets_history.csv"

var (
	mu      sync.Mutex
	csvPath string
)

// BetRecord represents one saved bet with optional resolved outcome.
type BetRecord struct {
	ConditionID    string
	Timestamp      time.Time
	Side           string  // "YES" or "NO"
	OurProbability float64 // 0-1, our estimated probability for the side
	MarketPrice    float64 // 0-1
	SizeUSDC       float64
	Outcome        *bool  // nil = unresolved; true = won; false = lost
	ResolvedAt     time.Time
}

// csvHeader defines the CSV column order.
var csvHeader = []string{
	"condition_id", "timestamp", "side",
	"our_probability", "market_price", "size_usdc",
	"outcome", "resolved_at",
}

// initPath resolves the CSV file path relative to dataRoot.
// dataRoot is typically "." or the repo root when running tests.
func initPath(dataRoot string) string {
	mu.Lock()
	defer mu.Unlock()
	if dataRoot == "" {
		dataRoot = "."
	}
	csvPath = filepath.Join(dataRoot, csvFileName)
	return csvPath
}

// ensureFile creates the CSV file with a header row if it doesn't exist.
func ensureFile(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("calibration: mkdir: %w", err)
	}

	if _, err := os.Stat(path); os.IsNotExist(err) {
		f, err := os.Create(path)
		if err != nil {
			return fmt.Errorf("calibration: create csv: %w", err)
		}
		defer f.Close()
		w := csv.NewWriter(f)
		if err := w.Write(csvHeader); err != nil {
			return fmt.Errorf("calibration: write header: %w", err)
		}
		w.Flush()
		return w.Error()
	}
	return nil
}

// SaveBet appends a new bet record to the history CSV.
// dataRoot is the repo root (pass "." when running from project root).
func SaveBet(d *strategy.Decision, dataRoot string) error {
	if d == nil {
		return fmt.Errorf("calibration: nil decision")
	}
	path := initPath(dataRoot)

	mu.Lock()
	defer mu.Unlock()

	if err := ensureFile(path); err != nil {
		return err
	}

	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("calibration: open csv: %w", err)
	}
	defer f.Close()

	w := csv.NewWriter(f)
	row := []string{
		d.Market.ConditionID,
		time.Now().UTC().Format(time.RFC3339),
		d.Side,
		strconv.FormatFloat(d.OurProbability, 'f', 6, 64),
		strconv.FormatFloat(d.MarketPrice, 'f', 6, 64),
		strconv.FormatFloat(d.SizeUSDC, 'f', 2, 64),
		"",  // outcome: unresolved
		"",  // resolved_at: empty
	}
	if err := w.Write(row); err != nil {
		return fmt.Errorf("calibration: write row: %w", err)
	}
	w.Flush()
	return w.Error()
}

// LoadHistory reads all records from the history CSV.
// Returns an empty slice (not an error) if the file doesn't exist yet.
func LoadHistory(dataRoot string) ([]BetRecord, error) {
	path := initPath(dataRoot)

	mu.Lock()
	defer mu.Unlock()

	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("calibration: open csv: %w", err)
	}
	defer f.Close()

	r := csv.NewReader(f)
	rows, err := r.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("calibration: read csv: %w", err)
	}

	var records []BetRecord
	for i, row := range rows {
		if i == 0 {
			continue // skip header
		}
		if len(row) < 8 {
			continue
		}
		rec, err := parseRow(row)
		if err != nil {
			// Skip malformed rows rather than failing entirely
			continue
		}
		records = append(records, rec)
	}
	return records, nil
}

func parseRow(row []string) (BetRecord, error) {
	var rec BetRecord
	rec.ConditionID = row[0]

	ts, err := time.Parse(time.RFC3339, row[1])
	if err != nil {
		return rec, fmt.Errorf("parse timestamp: %w", err)
	}
	rec.Timestamp = ts

	rec.Side = row[2]

	ourP, err := strconv.ParseFloat(row[3], 64)
	if err != nil {
		return rec, fmt.Errorf("parse our_probability: %w", err)
	}
	rec.OurProbability = ourP

	mktP, err := strconv.ParseFloat(row[4], 64)
	if err != nil {
		return rec, fmt.Errorf("parse market_price: %w", err)
	}
	rec.MarketPrice = mktP

	size, err := strconv.ParseFloat(row[5], 64)
	if err != nil {
		return rec, fmt.Errorf("parse size_usdc: %w", err)
	}
	rec.SizeUSDC = size

	// outcome column: "true", "false", or ""
	switch strings.ToLower(strings.TrimSpace(row[6])) {
	case "true":
		v := true
		rec.Outcome = &v
	case "false":
		v := false
		rec.Outcome = &v
	default:
		rec.Outcome = nil
	}

	// resolved_at: may be empty
	if row[7] != "" {
		t, err := time.Parse(time.RFC3339, row[7])
		if err == nil {
			rec.ResolvedAt = t
		}
	}

	return rec, nil
}

// UpdateOutcome sets the resolved outcome for a specific conditionID.
// It rewrites the entire CSV in place.
func UpdateOutcome(conditionID string, outcome bool, dataRoot string) error {
	path := initPath(dataRoot)

	mu.Lock()
	defer mu.Unlock()

	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("calibration: open csv: %w", err)
	}
	rows, err := csv.NewReader(f).ReadAll()
	f.Close()
	if err != nil {
		return fmt.Errorf("calibration: read csv: %w", err)
	}

	now := time.Now().UTC().Format(time.RFC3339)
	outcomeStr := "false"
	if outcome {
		outcomeStr = "true"
	}

	updated := false
	for i, row := range rows {
		if i == 0 {
			continue // header
		}
		if len(row) >= 8 && row[0] == conditionID && row[6] == "" {
			row[6] = outcomeStr
			row[7] = now
			rows[i] = row
			updated = true
		}
	}

	if !updated {
		return fmt.Errorf("calibration: condition %q not found or already resolved", conditionID)
	}

	out, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("calibration: rewrite csv: %w", err)
	}
	defer out.Close()

	w := csv.NewWriter(out)
	if err := w.WriteAll(rows); err != nil {
		return fmt.Errorf("calibration: write updated csv: %w", err)
	}
	w.Flush()
	return w.Error()
}

// BrierScore computes the mean Brier score over all resolved records.
//
// Brier score = (1/N) * Σ (forecast_probability - outcome)²
// Lower is better. 0 = perfect, 1 = maximally wrong, 0.25 = random.
//
// Returns (score, count, error). Count is the number of resolved bets used.
func BrierScore(records []BetRecord) (score float64, count int, err error) {
	var sum float64
	for _, r := range records {
		if r.Outcome == nil {
			continue
		}
		o := 0.0
		if *r.Outcome {
			o = 1.0
		}
		diff := r.OurProbability - o
		sum += diff * diff
		count++
	}
	if count == 0 {
		return 0, 0, nil
	}
	score = sum / float64(count)
	return score, count, nil
}

// PrintBrierScore loads history and logs the current Brier score to stdout.
// Intended to be called at bot startup.
func PrintBrierScore(dataRoot string) {
	records, err := LoadHistory(dataRoot)
	if err != nil {
		fmt.Printf("[calibration] failed to load history: %v\n", err)
		return
	}
	score, count, _ := BrierScore(records)
	if count == 0 {
		fmt.Println("[calibration] no resolved bets yet — Brier score unavailable")
		return
	}
	// Skill level interpretation:
	// < 0.10 → excellent, 0.10-0.20 → good, 0.20-0.25 → near random, > 0.25 → worse than random
	quality := brierQuality(score)
	fmt.Printf("[calibration] Brier score: %.4f (%s) over %d resolved bets\n",
		score, quality, count)

	// Also print win rate
	wins := 0
	for _, r := range records {
		if r.Outcome != nil && *r.Outcome {
			wins++
		}
	}
	if count > 0 {
		fmt.Printf("[calibration] Win rate: %.1f%% (%d/%d resolved)\n",
			float64(wins)/float64(count)*100, wins, count)
	}

	// Edge calibration: average (ourP - marketPrice) for winning bets
	edgeSum := 0.0
	edgeCount := 0
	for _, r := range records {
		if r.Outcome != nil && *r.Outcome {
			edgeSum += r.OurProbability - r.MarketPrice
			edgeCount++
		}
	}
	if edgeCount > 0 {
		fmt.Printf("[calibration] Avg edge on wins: %+.4f\n", edgeSum/float64(edgeCount))
	}
}

// brierQuality returns a human-readable quality label for a Brier score.
func brierQuality(score float64) string {
	switch {
	case score < 0.05:
		return "excellent"
	case score < 0.10:
		return "very good"
	case score < 0.15:
		return "good"
	case score < 0.20:
		return "fair"
	case score < 0.25:
		return "near random"
	default:
		return "worse than random"
	}
}
