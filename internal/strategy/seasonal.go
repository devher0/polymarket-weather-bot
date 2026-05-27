// seasonal.go — TASK-115: analyse historical bet records for temporal patterns.
//
// Win rates are broken down by:
//   - Weekday (Mon–Sun)
//   - Time-of-day bucket (morning / afternoon / evening / night)
//   - Calendar season (spring / summer / autumn / winter)
//
// When a win rate falls below 40% for a bucket with ≥ minSamples resolved bets
// the bot logs a warning and reduces MaxBet by 30% for the current cycle.
// Patterns are saved to data/seasonal_patterns.json after every evaluation run.
package strategy

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"time"
)

// seasonalWinRateThreshold is the minimum acceptable win rate (fraction, not %).
// Buckets below this trigger a slog.Warn and a MaxBet reduction.
const seasonalWinRateThreshold = 0.40

// minSeasonalSamples is the minimum resolved bets needed before a bucket
// triggers the warning / bet reduction.
const minSeasonalSamples = 10

// MaxBetSeasonalFactor caps the MaxBet reduction multiplier.
const maxBetSeasonalReduction = 0.70

// SeasonalBucket holds aggregate stats for one temporal slice.
type SeasonalBucket struct {
	Resolved int     `json:"resolved"` // total resolved bets in this bucket
	Wins     int     `json:"wins"`
	WinRate  float64 `json:"win_rate"` // fraction 0-1; 0 when Resolved == 0
}

// SeasonalPatterns stores win-rate stats for all temporal buckets.
type SeasonalPatterns struct {
	UpdatedAt  string                      `json:"updated_at"`
	ByWeekday  map[string]SeasonalBucket   `json:"by_weekday"`  // "Monday" … "Sunday"
	ByTimeSlot map[string]SeasonalBucket   `json:"by_time_slot"` // "morning" | "afternoon" | "evening" | "night"
	BySeason   map[string]SeasonalBucket   `json:"by_season"`   // "spring" | "summer" | "autumn" | "winter"
}

// weekdayKey returns the canonical weekday key for a timestamp.
func weekdayKey(t time.Time) string { return t.UTC().Weekday().String() }

// timeSlotKey categorises a UTC hour into a named slot.
func timeSlotKey(t time.Time) string {
	h := t.UTC().Hour()
	switch {
	case h >= 6 && h < 12:
		return "morning"
	case h >= 12 && h < 18:
		return "afternoon"
	case h >= 18 && h < 22:
		return "evening"
	default:
		return "night"
	}
}

// seasonKey returns the meteorological season for the UTC month.
func seasonKey(t time.Time) string {
	switch t.UTC().Month() {
	case 3, 4, 5:
		return "spring"
	case 6, 7, 8:
		return "summer"
	case 9, 10, 11:
		return "autumn"
	default: // 12, 1, 2
		return "winter"
	}
}

// SeasonalRecord is a minimal bet record used for temporal pattern analysis.
// Callers convert from calibration.BetRecord to avoid an import cycle.
type SeasonalRecord struct {
	Timestamp time.Time
	Outcome   *bool // nil = unresolved; true = won; false = lost
}

// ComputeSeasonalPatterns calculates win-rate breakdowns for all resolved records.
func ComputeSeasonalPatterns(records []SeasonalRecord) SeasonalPatterns {
	type acc struct{ resolved, wins int }
	weekday := map[string]*acc{}
	timeSlot := map[string]*acc{}
	season := map[string]*acc{}

	for _, r := range records {
		if r.Outcome == nil {
			continue // skip unresolved
		}
		wk := weekdayKey(r.Timestamp)
		ts := timeSlotKey(r.Timestamp)
		sn := seasonKey(r.Timestamp)

		if weekday[wk] == nil {
			weekday[wk] = &acc{}
		}
		if timeSlot[ts] == nil {
			timeSlot[ts] = &acc{}
		}
		if season[sn] == nil {
			season[sn] = &acc{}
		}

		won := 0
		if *r.Outcome {
			won = 1
		}
		weekday[wk].resolved++
		weekday[wk].wins += won
		timeSlot[ts].resolved++
		timeSlot[ts].wins += won
		season[sn].resolved++
		season[sn].wins += won
	}

	toMap := func(src map[string]*acc) map[string]SeasonalBucket {
		out := make(map[string]SeasonalBucket, len(src))
		for k, v := range src {
			wr := 0.0
			if v.resolved > 0 {
				wr = float64(v.wins) / float64(v.resolved)
			}
			out[k] = SeasonalBucket{Resolved: v.resolved, Wins: v.wins, WinRate: wr}
		}
		return out
	}

	return SeasonalPatterns{
		UpdatedAt:  time.Now().UTC().Format(time.RFC3339),
		ByWeekday:  toMap(weekday),
		ByTimeSlot: toMap(timeSlot),
		BySeason:   toMap(season),
	}
}

// SaveSeasonalPatterns writes patterns to data/seasonal_patterns.json.
// Errors are silently swallowed; the file is advisory only.
func SaveSeasonalPatterns(p SeasonalPatterns, dataRoot string) {
	if dataRoot == "" {
		dataRoot = "."
	}
	path := filepath.Join(dataRoot, "seasonal_patterns.json")
	b, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return
	}
	_ = os.WriteFile(path, b, 0o644)
}

// LoadSeasonalPatterns reads data/seasonal_patterns.json. Returns an empty
// SeasonalPatterns if the file does not exist or cannot be parsed.
func LoadSeasonalPatterns(dataRoot string) SeasonalPatterns {
	if dataRoot == "" {
		dataRoot = "."
	}
	b, err := os.ReadFile(filepath.Join(dataRoot, "seasonal_patterns.json"))
	if err != nil {
		return SeasonalPatterns{}
	}
	var p SeasonalPatterns
	if json.Unmarshal(b, &p) != nil {
		return SeasonalPatterns{}
	}
	return p
}

// WeakBuckets returns the names of buckets that have ≥ minSamples resolved
// bets but a win rate below seasonalWinRateThreshold.
// Each entry is formatted as "category:key (rate%/N bets)".
func WeakBuckets(p SeasonalPatterns) []string {
	var weak []string
	check := func(category string, m map[string]SeasonalBucket) {
		for k, b := range m {
			if b.Resolved >= minSeasonalSamples && b.WinRate < seasonalWinRateThreshold {
				weak = append(weak, fmt.Sprintf("%s:%s (%.0f%%/%d bets)",
					category, k, b.WinRate*100, b.Resolved))
			}
		}
	}
	check("weekday", p.ByWeekday)
	check("time_slot", p.ByTimeSlot)
	check("season", p.BySeason)
	return weak
}

// MaxBetMultiplier returns the MaxBet scaling factor for the current moment.
// Returns maxBetSeasonalReduction (0.70) if the current weekday, time slot, or
// season is a weak bucket; otherwise 1.0.
func MaxBetMultiplier(p SeasonalPatterns) float64 {
	now := time.Now().UTC()
	wk := weekdayKey(now)
	ts := timeSlotKey(now)
	sn := seasonKey(now)

	isWeak := func(b SeasonalBucket) bool {
		return b.Resolved >= minSeasonalSamples && b.WinRate < seasonalWinRateThreshold
	}

	if (p.ByWeekday != nil && isWeak(p.ByWeekday[wk])) ||
		(p.ByTimeSlot != nil && isWeak(p.ByTimeSlot[ts])) ||
		(p.BySeason != nil && isWeak(p.BySeason[sn])) {
		return maxBetSeasonalReduction
	}
	return 1.0
}

// LogSeasonalWarnings logs weak temporal buckets at Warn level and returns
// the MaxBet multiplier for the current time window.
func LogSeasonalWarnings(p SeasonalPatterns) float64 {
	weak := WeakBuckets(p)
	for _, w := range weak {
		slog.Warn("seasonal pattern warning", "bucket", w,
			"action", fmt.Sprintf("reducing max_bet to %.0f%%", maxBetSeasonalReduction*100))
	}
	mult := MaxBetMultiplier(p)
	if mult < 1.0 {
		now := time.Now().UTC()
		slog.Info("seasonal max_bet reduction active",
			"weekday", weekdayKey(now),
			"time_slot", timeSlotKey(now),
			"season", seasonKey(now),
			"multiplier", fmt.Sprintf("%.2f", mult),
		)
	}
	return mult
}

// UpdateAndSave recomputes patterns from records and persists them.
// Returns the freshly computed SeasonalPatterns.
func UpdateAndSave(records []SeasonalRecord, dataRoot string) SeasonalPatterns {
	p := ComputeSeasonalPatterns(records)
	SaveSeasonalPatterns(p, dataRoot)
	return p
}

// RoundedWinRate returns win rate rounded to 1 decimal for display.
func RoundedWinRate(b SeasonalBucket) string {
	if b.Resolved == 0 {
		return "n/a"
	}
	return fmt.Sprintf("%.1f%%", math.Round(b.WinRate*1000)/10)
}
