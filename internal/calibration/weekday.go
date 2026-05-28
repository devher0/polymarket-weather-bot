// weekday.go — TASK-233: P&L breakdown by day of week.
//
// Prediction markets have day-of-week patterns: liquidity concentrations,
// weekend market-maker absence, and settlement cycles differ between
// Monday–Friday and weekends. This file aggregates resolved bet outcomes
// by the UTC weekday on which the bet was placed.
//
// Returns [7]WeekdayStats indexed by time.Weekday (Sunday=0 … Saturday=6).
package calibration

import "time"

// WeekdayStats holds aggregated resolved-bet metrics for one weekday (UTC).
type WeekdayStats struct {
	Day         time.Weekday
	Bets        int
	Wins        int
	PnL         float64
	TotalRisked float64
}

// WinPct returns win percentage (0–100), or -1 when no bets.
func (w WeekdayStats) WinPct() float64 {
	if w.Bets == 0 {
		return -1
	}
	return float64(w.Wins) / float64(w.Bets) * 100
}

// ROIPct returns ROI as a percentage, or 0 when TotalRisked == 0.
func (w WeekdayStats) ROIPct() float64 {
	if w.TotalRisked == 0 {
		return 0
	}
	return w.PnL / w.TotalRisked * 100
}

// WeekdayBreakdown aggregates resolved bets by UTC weekday.
// Returns a [7]WeekdayStats slice indexed by time.Weekday (Sunday=0 … Saturday=6).
// Unresolved bets are silently skipped.
func WeekdayBreakdown(records []BetRecord) [7]WeekdayStats {
	var stats [7]WeekdayStats
	for i := range stats {
		stats[i].Day = time.Weekday(i)
	}

	for _, r := range records {
		if r.Outcome == nil {
			continue
		}
		d := r.Timestamp.UTC().Weekday()
		stats[d].Bets++
		stats[d].TotalRisked += r.SizeUSDC
		if *r.Outcome {
			stats[d].Wins++
			if r.MarketPrice > 0 {
				stats[d].PnL += r.SizeUSDC/r.MarketPrice - r.SizeUSDC
			}
		} else {
			stats[d].PnL -= r.SizeUSDC
		}
	}
	return stats
}

// BestWeekday returns the index of the weekday with the highest ROI
// among days with at least minBets resolved bets. Returns -1 if none qualify.
func BestWeekday(stats [7]WeekdayStats, minBets int) int {
	best := -1
	for i, s := range stats {
		if s.Bets < minBets {
			continue
		}
		if best == -1 || s.ROIPct() > stats[best].ROIPct() {
			best = i
		}
	}
	return best
}

// WorstWeekday returns the index of the weekday with the lowest ROI
// among days with at least minBets resolved bets. Returns -1 if none qualify.
func WorstWeekday(stats [7]WeekdayStats, minBets int) int {
	worst := -1
	for i, s := range stats {
		if s.Bets < minBets {
			continue
		}
		if worst == -1 || s.ROIPct() < stats[worst].ROIPct() {
			worst = i
		}
	}
	return worst
}
