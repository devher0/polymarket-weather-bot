// streak.go — detect consecutive win/loss runs and return a Kelly scaling factor.
// Used by cmd/bot to reduce bet sizes during losing streaks.
package calibration

// StreakResult describes the current run of consecutive same-outcome bets at
// the tail of the resolved bet history.
type StreakResult struct {
	Count int  // number of consecutive same-outcome resolved bets (≥1 when non-empty)
	IsWin bool // true when the run is wins; false when losses
}

// CurrentStreak returns the consecutive run of wins or losses at the tail of
// the resolved subset of records. Records may be in any order; the function
// sorts by resolved time (most-recent last) before scanning.
// Returns a zero StreakResult when there are no resolved bets.
func CurrentStreak(records []BetRecord) StreakResult {
	// Collect resolved bets (Outcome != nil) sorted by resolved time ascending.
	resolved := make([]BetRecord, 0, len(records))
	for _, r := range records {
		if r.Outcome != nil {
			resolved = append(resolved, r)
		}
	}
	if len(resolved) == 0 {
		return StreakResult{}
	}

	// Sort by resolved time (earliest first) so we scan from oldest to newest.
	for i := 1; i < len(resolved); i++ {
		for j := i; j > 0 && resolved[j].ResolvedAt.Before(resolved[j-1].ResolvedAt); j-- {
			resolved[j], resolved[j-1] = resolved[j-1], resolved[j]
		}
	}

	// Walk backwards from the most recent resolved bet.
	last := resolved[len(resolved)-1]
	streak := 1
	isWin := *last.Outcome

	for i := len(resolved) - 2; i >= 0; i-- {
		if *resolved[i].Outcome == isWin {
			streak++
		} else {
			break
		}
	}

	return StreakResult{Count: streak, IsWin: isWin}
}

// StreakKellyFactor returns a multiplier for the Kelly fraction based on the
// current streak. Losing streaks reduce bet sizes to limit drawdown during
// periods of systematic underperformance:
//
//	3+ consecutive losses → 0.70 (−30%)
//	2 consecutive losses  → 0.85 (−15%)
//	1 loss / wins / empty → 1.00 (no change)
//
// We never boost on winning streaks because bets are already capped at
// half-Kelly, and avoiding overconfidence is more valuable than chasing.
func StreakKellyFactor(s StreakResult) float64 {
	if s.Count == 0 || s.IsWin {
		return 1.0
	}
	switch {
	case s.Count >= 3:
		return 0.70
	case s.Count == 2:
		return 0.85
	default:
		return 1.0
	}
}
