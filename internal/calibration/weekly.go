// weekly.go — weekly P&L aggregation for performance analysis.
// TASK-166
package calibration

import (
	"sort"
	"time"
)

// WeeklyStats holds aggregated resolved-bet metrics for one ISO calendar week
// (Monday–Sunday, keyed by the Monday midnight UTC of that week).
type WeeklyStats struct {
	WeekStart  time.Time // Monday 00:00 UTC of the week
	Bets       int
	Wins       int
	PnLUSDC    float64
	BrierScore float64 // average Brier score for bets resolved that week; 0 if no data
}

// WinPct returns win percentage (0–100), or -1 when no bets.
func (w WeeklyStats) WinPct() float64 {
	if w.Bets == 0 {
		return -1
	}
	return float64(w.Wins) / float64(w.Bets) * 100
}

// weekStart returns the Monday midnight UTC for the week containing t.
func weekStart(t time.Time) time.Time {
	t = t.UTC().Truncate(24 * time.Hour)
	// time.Weekday: Sunday=0 Monday=1 … Saturday=6
	daysSinceMonday := int(t.Weekday()+6) % 7
	return t.AddDate(0, 0, -daysSinceMonday)
}

// WeeklyBreakdown returns per-ISO-week stats for the last nWeeks weeks, ordered
// oldest first. Weeks with no resolved bets still appear with zero values.
func WeeklyBreakdown(records []BetRecord, nWeeks int) []WeeklyStats {
	if nWeeks <= 0 {
		return nil
	}

	now := weekStart(time.Now().UTC())
	weeks := make([]WeeklyStats, nWeeks)
	for i := range weeks {
		weeks[i].WeekStart = now.AddDate(0, 0, -7*(nWeeks-1-i))
	}

	// Index from weekStart → slice index.
	weekIdx := make(map[time.Time]int, nWeeks)
	for i, w := range weeks {
		weekIdx[w.WeekStart] = i
	}

	type weekAcc struct {
		brierSum float64
		brierN   int
	}
	accs := make([]weekAcc, nWeeks)

	for _, r := range records {
		if r.Outcome == nil {
			continue
		}
		day := r.ResolvedAt
		if day.IsZero() {
			day = r.Timestamp
		}
		ws := weekStart(day)
		idx, ok := weekIdx[ws]
		if !ok {
			continue
		}

		var pnl float64
		if *r.Outcome {
			pnl = r.SizeUSDC/r.MarketPrice - r.SizeUSDC
			weeks[idx].Wins++
		} else {
			pnl = -r.SizeUSDC
		}
		weeks[idx].Bets++
		weeks[idx].PnLUSDC += pnl

		// Accumulate Brier score component: (p - outcome)^2
		outcome := 0.0
		if *r.Outcome {
			outcome = 1.0
		}
		brier := (r.OurProbability - outcome) * (r.OurProbability - outcome)
		accs[idx].brierSum += brier
		accs[idx].brierN++
	}

	for i := range weeks {
		if accs[i].brierN > 0 {
			weeks[i].BrierScore = accs[i].brierSum / float64(accs[i].brierN)
		}
	}

	return weeks
}

// BestWeek returns the WeeklyStats entry with the highest PnLUSDC.
// Returns the zero value when stats is empty.
func BestWeek(stats []WeeklyStats) WeeklyStats {
	if len(stats) == 0 {
		return WeeklyStats{}
	}
	sorted := make([]WeeklyStats, len(stats))
	copy(sorted, stats)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].PnLUSDC > sorted[j].PnLUSDC
	})
	return sorted[0]
}

// WorstWeek returns the WeeklyStats entry with the lowest PnLUSDC.
// Returns the zero value when stats is empty.
func WorstWeek(stats []WeeklyStats) WeeklyStats {
	if len(stats) == 0 {
		return WeeklyStats{}
	}
	sorted := make([]WeeklyStats, len(stats))
	copy(sorted, stats)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].PnLUSDC < sorted[j].PnLUSDC
	})
	return sorted[0]
}
