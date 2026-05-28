// timing.go — TASK-133: time-of-day win rate tracker.
//
// Prediction markets have time-based patterns: liquidity, market-maker
// behaviour, and order flow vary significantly by UTC hour. This tracker
// records wins and losses per UTC hour and derives a timing multiplier
// (0.5–1.2) for bet sizing. Hours with historically poor results get a
// reduced size; hours with strong results get a modest boost.
//
// Persistence: data/hourly_winrate.json
// (updated after each resolved bet via UpdateHourlyStats)
package calibration

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"time"
)

const hourlyWinRateFile = "data/hourly_winrate.json"

// HourBucket holds win/loss counts for one UTC hour (0–23).
type HourBucket struct {
	Hour   int `json:"hour"`   // 0–23 UTC
	Wins   int `json:"wins"`   // resolved bets we won
	Losses int `json:"losses"` // resolved bets we lost
}

// WinRate returns the win fraction for this bucket, or -1 if no data.
func (b HourBucket) WinRate() float64 {
	total := b.Wins + b.Losses
	if total == 0 {
		return -1
	}
	return float64(b.Wins) / float64(total)
}

// Total returns the total number of resolved bets in this bucket.
func (b HourBucket) Total() int { return b.Wins + b.Losses }

type hourlyStore struct {
	Buckets [24]HourBucket `json:"buckets"`
}

func hourlyWinRatePath(dataRoot string) string {
	if dataRoot == "" {
		dataRoot = "."
	}
	return filepath.Join(dataRoot, hourlyWinRateFile)
}

// LoadHourlyStats reads the persisted hourly win-rate store.
// Returns zero-valued buckets if the file does not exist.
func LoadHourlyStats(dataRoot string) ([24]HourBucket, error) {
	var store hourlyStore
	for i := range store.Buckets {
		store.Buckets[i].Hour = i
	}

	data, err := os.ReadFile(hourlyWinRatePath(dataRoot))
	if os.IsNotExist(err) {
		return store.Buckets, nil
	}
	if err != nil {
		return store.Buckets, fmt.Errorf("timing: read hourly stats: %w", err)
	}
	if err := json.Unmarshal(data, &store); err != nil {
		return store.Buckets, fmt.Errorf("timing: parse hourly stats: %w", err)
	}
	// Ensure Hour field is always set correctly (index = hour).
	for i := range store.Buckets {
		store.Buckets[i].Hour = i
	}
	return store.Buckets, nil
}

func saveHourlyStats(buckets [24]HourBucket, dataRoot string) error {
	store := hourlyStore{Buckets: buckets}
	data, err := json.MarshalIndent(store, "", "  ")
	if err != nil {
		return fmt.Errorf("timing: marshal: %w", err)
	}
	path := hourlyWinRatePath(dataRoot)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("timing: mkdir: %w", err)
	}
	return os.WriteFile(path, data, 0o644)
}

// UpdateHourlyStats increments the win or loss counter for the UTC hour in
// which the bet was placed (using rec.Timestamp). Only resolved bets
// (Outcome != nil) are counted. Unresolved bets are silently ignored.
func UpdateHourlyStats(rec BetRecord, dataRoot string) error {
	if rec.Outcome == nil {
		return nil
	}
	hour := rec.Timestamp.UTC().Hour()

	buckets, err := LoadHourlyStats(dataRoot)
	if err != nil {
		// Non-fatal: start fresh rather than crash.
		for i := range buckets {
			buckets[i].Hour = i
		}
	}
	if *rec.Outcome {
		buckets[hour].Wins++
	} else {
		buckets[hour].Losses++
	}
	return saveHourlyStats(buckets, dataRoot)
}

// RebuildHourlyStats recomputes all hourly buckets from scratch using the
// full bet history. Useful for backfill after enabling this feature.
func RebuildHourlyStats(records []BetRecord, dataRoot string) error {
	var buckets [24]HourBucket
	for i := range buckets {
		buckets[i].Hour = i
	}
	for _, r := range records {
		if r.Outcome == nil {
			continue
		}
		h := r.Timestamp.UTC().Hour()
		if *r.Outcome {
			buckets[h].Wins++
		} else {
			buckets[h].Losses++
		}
	}
	return saveHourlyStats(buckets, dataRoot)
}

// TimingMultiplier returns a bet-size multiplier for the given UTC hour based
// on historical win rates. The multiplier is bounded to [0.5, 1.2].
//
// Algorithm:
//  1. Compute global win rate across all hours that have ≥ minSamples data.
//  2. For the target hour: if < minSamples → return 1.0 (neutral).
//  3. Ratio = hourWinRate / globalWinRate.
//  4. Multiplier = 1.0 + clamp(ratio-1, -0.5, 0.2) → range [0.5, 1.2].
//
// minSamples = 5 (enough to be statistically meaningful without excessive data).
func TimingMultiplier(buckets [24]HourBucket, hour int) float64 {
	const minSamples = 5

	if hour < 0 || hour > 23 {
		return 1.0
	}

	// Global win rate across all hours with enough data.
	totalWins, totalBets := 0, 0
	for _, b := range buckets {
		if b.Total() >= minSamples {
			totalWins += b.Wins
			totalBets += b.Total()
		}
	}
	if totalBets < minSamples {
		return 1.0 // not enough global data
	}
	globalWR := float64(totalWins) / float64(totalBets)

	target := buckets[hour]
	if target.Total() < minSamples {
		return 1.0 // not enough data for this specific hour
	}
	hourWR := target.WinRate()

	if globalWR == 0 {
		return 1.0
	}
	ratio := hourWR / globalWR
	// Clamp deviation: max +20% bonus, max -50% penalty.
	delta := math.Max(-0.5, math.Min(0.2, ratio-1.0))
	return 1.0 + delta
}

// TimingMultiplierNow returns the timing multiplier for the current UTC hour.
func TimingMultiplierNow(dataRoot string) float64 {
	buckets, err := LoadHourlyStats(dataRoot)
	if err != nil {
		return 1.0
	}
	return TimingMultiplier(buckets, time.Now().UTC().Hour())
}

// HourlyTable returns a formatted summary of win rates per hour for display.
// Format: hour, wins, losses, total, win_rate, multiplier.
func HourlyTable(buckets [24]HourBucket) []HourlyRow {
	rows := make([]HourlyRow, 24)
	for i, b := range buckets {
		wr := b.WinRate()
		m := TimingMultiplier(buckets, i)
		rows[i] = HourlyRow{
			Hour:       i,
			Wins:       b.Wins,
			Losses:     b.Losses,
			WinRate:    wr,
			Multiplier: m,
		}
	}
	return rows
}

// HourlyRow is one display row in the hourly timing table.
type HourlyRow struct {
	Hour       int
	Wins       int
	Losses     int
	WinRate    float64 // -1 = no data
	Multiplier float64
}
