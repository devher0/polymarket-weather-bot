// signal_trend.go — TASK-228: 7-day rolling win rate trend per signal.
package calibration

import (
	"fmt"
	"time"
)

// SignalTrend7d returns the win rate delta (current 7d − previous 7d) for a
// specific signal. Returns (delta, true) when both windows have ≥ minBets
// resolved bets, otherwise (0, false).
func SignalTrend7d(records []BetRecord, signal string, minBets int) (delta float64, ok bool) {
	if minBets <= 0 {
		minBets = 3
	}
	now := time.Now().UTC()
	cut7 := now.AddDate(0, 0, -7)
	cut14 := now.AddDate(0, 0, -14)

	var winsRecent, totalRecent int
	var winsPrev, totalPrev int

	for _, r := range records {
		if r.Signal != signal || r.Outcome == nil {
			continue
		}
		ts := r.Timestamp.UTC()
		win := *r.Outcome
		switch {
		case ts.After(cut7):
			totalRecent++
			if win {
				winsRecent++
			}
		case ts.After(cut14):
			totalPrev++
			if win {
				winsPrev++
			}
		}
	}

	if totalRecent < minBets || totalPrev < minBets {
		return 0, false
	}
	recent := float64(winsRecent) / float64(totalRecent) * 100
	prev := float64(winsPrev) / float64(totalPrev) * 100
	return recent - prev, true
}

// FormatTrend formats a SignalTrend7d delta into a short label with emoji.
// Returns "N/A" when ok is false.
func FormatTrend(delta float64, ok bool) string {
	if !ok {
		return "N/A"
	}
	if delta >= 1 {
		return fmt.Sprintf("+%.0f%% 📈", delta)
	}
	if delta <= -1 {
		return fmt.Sprintf("%.0f%% 📉", delta)
	}
	return "~"
}
