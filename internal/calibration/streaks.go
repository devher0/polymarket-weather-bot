// Package calibration — TASK-139: Win/loss streak detector.
//
// ComputeStreak scans resolved bets from most recent to oldest and returns
// the length and direction of the current streak.
//
// StreakAlert returns true and a human-readable message when a losing streak
// reaches or exceeds alertLen (default 4).
package calibration

import (
	"fmt"
	"sort"
)

// ComputeStreak returns the current streak from the most recent resolved bets.
//
//   - current is the number of consecutive identical outcomes at the tail of
//     the resolved history.
//   - kind is "wins" or "losses".
//   - Returns (0, "") when there are no resolved bets.
func ComputeStreak(records []BetRecord) (current int, kind string) {
	// Collect only resolved bets.
	var resolved []BetRecord
	for _, r := range records {
		if r.Outcome != nil {
			resolved = append(resolved, r)
		}
	}
	if len(resolved) == 0 {
		return 0, ""
	}

	// Sort by ResolvedAt ascending; fall back to Timestamp when ResolvedAt is zero.
	sort.Slice(resolved, func(i, j int) bool {
		ti := resolved[i].ResolvedAt
		if ti.IsZero() {
			ti = resolved[i].Timestamp
		}
		tj := resolved[j].ResolvedAt
		if tj.IsZero() {
			tj = resolved[j].Timestamp
		}
		return ti.Before(tj)
	})

	// Walk backwards from the newest resolved bet.
	last := *resolved[len(resolved)-1].Outcome
	current = 1
	for i := len(resolved) - 2; i >= 0; i-- {
		if *resolved[i].Outcome == last {
			current++
		} else {
			break
		}
	}

	if last {
		kind = "wins"
	} else {
		kind = "losses"
	}
	return current, kind
}

// StreakAlert checks whether the current losing streak has reached alertLen.
//
// alertLen ≤ 0 defaults to 4.
// Returns (false, "") for win streaks or streaks below the threshold.
func StreakAlert(records []BetRecord, alertLen int) (bool, string) {
	if alertLen <= 0 {
		alertLen = 4
	}
	n, kind := ComputeStreak(records)
	if kind != "losses" || n < alertLen {
		return false, ""
	}
	msg := fmt.Sprintf(
		"🚨 Loss streak: %d consecutive losses — consider pausing",
		n,
	)
	return true, msg
}

// StreakStatusLine returns a short human-readable streak summary for display
// in /status responses, e.g. "+3 wins" or "-2 losses".
// Returns "" when there are no resolved bets.
func StreakStatusLine(records []BetRecord) string {
	n, kind := ComputeStreak(records)
	if n == 0 || kind == "" {
		return ""
	}
	if kind == "wins" {
		return fmt.Sprintf("+%d wins", n)
	}
	return fmt.Sprintf("-%d losses", n)
}
