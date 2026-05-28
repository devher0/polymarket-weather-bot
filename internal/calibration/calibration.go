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
	"sort"
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
	City           string // e.g. "new_york" (cols 8; empty for legacy records)
	Signal         string // e.g. "rain", "heat" (col 9; empty for legacy records)
}

// BreakdownStats holds per-city or per-signal performance stats.
type BreakdownStats struct {
	Count    int
	BrierSum float64
	Wins     int
}

// BrierAvg returns the mean Brier score for this breakdown bucket, or 0 if Count==0.
func (b BreakdownStats) BrierAvg() float64 {
	if b.Count == 0 {
		return 0
	}
	return b.BrierSum / float64(b.Count)
}

// WinRate returns win percentage (0–100) for this bucket.
func (b BreakdownStats) WinRate() float64 {
	if b.Count == 0 {
		return 0
	}
	return float64(b.Wins) / float64(b.Count) * 100
}

// csvHeader defines the CSV column order.
// Columns 8-9 (city, signal) were added in TASK-035; legacy rows may be absent.
var csvHeader = []string{
	"condition_id", "timestamp", "side",
	"our_probability", "market_price", "size_usdc",
	"outcome", "resolved_at",
	"city", "signal",
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
		"",             // outcome: unresolved
		"",             // resolved_at: empty
		d.Market.City,  // col 8: city (TASK-035)
		d.Market.Signal, // col 9: signal (TASK-035)
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

	// cols 8-9: city, signal — added in TASK-035; absent in legacy rows
	if len(row) >= 9 {
		rec.City = row[8]
	}
	if len(row) >= 10 {
		rec.Signal = row[9]
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

// LoadOpenPositions returns a set of conditionIDs that currently have at least
// one unresolved bet in the history CSV. Used by the bot to skip markets where
// a position already exists (anti-double-bet).
func LoadOpenPositions(dataRoot string) (map[string]bool, error) {
	records, err := LoadHistory(dataRoot)
	if err != nil {
		return nil, err
	}
	open := make(map[string]bool)
	for _, r := range records {
		if r.Outcome == nil { // unresolved = still open
			open[r.ConditionID] = true
		}
	}
	return open, nil
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

// WeightedBrierScore computes a bet-size-weighted Brier score.
// Each resolved bet contributes proportionally to its size relative to the
// mean resolved-bet size. Larger bets count more than smaller bets.
// Returns (0, 0, nil) if there are no resolved bets.
func WeightedBrierScore(records []BetRecord) (score float64, count int, err error) {
	var resolved []BetRecord
	var totalSize float64
	for _, r := range records {
		if r.Outcome != nil {
			resolved = append(resolved, r)
			totalSize += r.SizeUSDC
		}
	}
	count = len(resolved)
	if count == 0 {
		return 0, 0, nil
	}
	meanSize := totalSize / float64(count)
	if meanSize == 0 {
		return 0, 0, nil
	}
	var weightedSum, weightSum float64
	for _, r := range resolved {
		w := r.SizeUSDC / meanSize
		o := 0.0
		if *r.Outcome {
			o = 1.0
		}
		diff := r.OurProbability - o
		weightedSum += w * diff * diff
		weightSum += w
	}
	if weightSum == 0 {
		return 0, 0, nil
	}
	score = weightedSum / weightSum
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
	// Also print size-weighted Brier for capital-weighted accuracy view.
	if wScore, wCount, _ := WeightedBrierScore(records); wCount > 0 {
		fmt.Printf("[calibration] Weighted Brier: %.4f (size-weighted, %d bets)\n", wScore, wCount)
	}

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

	// Per-city breakdown (top-5 by count, then by Brier asc)
	cityMap := CityBreakdown(records)
	if len(cityMap) > 0 {
		fmt.Println("[calibration] Top cities by Brier score:")
		printBreakdownTop(cityMap, 5)
	}

	// Per-signal breakdown
	sigMap := SignalBreakdown(records)
	if len(sigMap) > 0 {
		fmt.Println("[calibration] Signals by Brier score:")
		printBreakdownTop(sigMap, 6)
	}
}

// breakdownEntry is a key-value pair used for sorting breakdown maps.
type breakdownEntry struct {
	Key   string
	Stats BreakdownStats
}

// printBreakdownTop prints at most n breakdown entries sorted by Brier score ascending
// (lower = better calibration). Filters out buckets with 0 resolved bets.
func printBreakdownTop(m map[string]BreakdownStats, n int) {
	var items []breakdownEntry
	for k, v := range m {
		if v.Count > 0 {
			items = append(items, breakdownEntry{k, v})
		}
	}
	// Insertion sort: Brier ascending (best first), tie-break by count descending.
	for i := 1; i < len(items); i++ {
		for j := i; j > 0; j-- {
			ai := items[j-1].Stats.BrierAvg()
			aj := items[j].Stats.BrierAvg()
			if ai > aj || (ai == aj && items[j-1].Stats.Count < items[j].Stats.Count) {
				items[j-1], items[j] = items[j], items[j-1]
			}
		}
	}
	if len(items) > n {
		items = items[:n]
	}
	for _, item := range items {
		fmt.Printf("  %-20s  brier=%.4f  wr=%.0f%%  n=%d\n",
			item.Key,
			item.Stats.BrierAvg(),
			item.Stats.WinRate(),
			item.Stats.Count,
		)
	}
}

// BankrollMultiplier returns a multiplier for the effective bankroll based on
// the current Brier score. Well-calibrated models get more capital; poor ones
// are scaled back to limit losses during bad streaks.
//
// Mapping (TASK-033):
//
//	score < 0.10  → multiplier 1.5   (excellent — scale up)
//	score > 0.22  → multiplier 0.5   (near-random — scale down)
//	0.10–0.22     → linear interpolation from 1.5 → 0.5
//
// When score is 0 (no data yet) the neutral multiplier 1.0 is returned.
// Result is clamped to [0.25, 2.0].
func BankrollMultiplier(brierScore float64) float64 {
	if brierScore <= 0 {
		return 1.0 // no data — neutral
	}
	const (
		excellentThresh = 0.10
		randomThresh    = 0.22
		highMult        = 1.5
		lowMult         = 0.5
		minMult         = 0.25
		maxMult         = 2.0
	)
	var m float64
	switch {
	case brierScore <= excellentThresh:
		m = highMult
	case brierScore >= randomThresh:
		m = lowMult
	default:
		// Linear interpolation between (excellentThresh, highMult) and (randomThresh, lowMult).
		t := (brierScore - excellentThresh) / (randomThresh - excellentThresh)
		m = highMult + t*(lowMult-highMult)
	}
	// Clamp.
	if m < minMult {
		m = minMult
	}
	if m > maxMult {
		m = maxMult
	}
	return m
}

// CityBreakdown returns per-city Brier stats over all resolved bets.
// Records with an empty City field are bucketed under "(unknown)".
func CityBreakdown(records []BetRecord) map[string]BreakdownStats {
	m := make(map[string]BreakdownStats)
	for _, r := range records {
		if r.Outcome == nil {
			continue
		}
		key := r.City
		if key == "" {
			key = "(unknown)"
		}
		o := 0.0
		if *r.Outcome {
			o = 1.0
		}
		diff := r.OurProbability - o
		s := m[key]
		s.Count++
		s.BrierSum += diff * diff
		if *r.Outcome {
			s.Wins++
		}
		m[key] = s
	}
	return m
}

// SignalBreakdown returns per-signal Brier stats over all resolved bets.
// Records with an empty Signal field are bucketed under "(unknown)".
func SignalBreakdown(records []BetRecord) map[string]BreakdownStats {
	m := make(map[string]BreakdownStats)
	for _, r := range records {
		if r.Outcome == nil {
			continue
		}
		key := r.Signal
		if key == "" {
			key = "(unknown)"
		}
		o := 0.0
		if *r.Outcome {
			o = 1.0
		}
		diff := r.OurProbability - o
		s := m[key]
		s.Count++
		s.BrierSum += diff * diff
		if *r.Outcome {
			s.Wins++
		}
		m[key] = s
	}
	return m
}

// SignalBreakdownForPeriod returns per-signal Brier stats for resolved bets
// placed within the last `days` calendar days. Useful for trending win rate
// over different time windows (7d / 14d / 30d). See TASK-168.
func SignalBreakdownForPeriod(records []BetRecord, days int) map[string]BreakdownStats {
	cutoff := time.Now().UTC().AddDate(0, 0, -days)
	var filtered []BetRecord
	for _, r := range records {
		if !r.Timestamp.IsZero() && r.Timestamp.After(cutoff) {
			filtered = append(filtered, r)
		}
	}
	return SignalBreakdown(filtered)
}

// WeakSignalAlert scans a SignalBreakdown map and returns a slice of
// human-readable warning strings for any signal type whose win rate is below
// winRateThreshold (default 40%) and has at least minSamples resolved bets.
//
// Callers typically pass the result of SignalBreakdown(records).
// Example warning: "rain: win_rate=32.0% (n=15) — consider raising min_edge"
func WeakSignalAlert(breakdown map[string]BreakdownStats, minSamples int, winRateThreshold float64) []string {
	if winRateThreshold <= 0 {
		winRateThreshold = 40.0
	}
	if minSamples <= 0 {
		minSamples = 10
	}
	var alerts []string
	for sig, bs := range breakdown {
		if sig == "(unknown)" || bs.Count < minSamples {
			continue
		}
		wr := bs.WinRate()
		if wr < winRateThreshold {
			alerts = append(alerts, fmt.Sprintf(
				"%s: win_rate=%.1f%% (n=%d) — consider raising min_edge",
				sig, wr, bs.Count,
			))
		}
	}
	// Sort for deterministic output
	sort.Strings(alerts)
	return alerts
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
