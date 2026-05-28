// rolling_winrate.go — rolling window win rate monitor.
//
// Brier score provides long-term accuracy tracking but reacts slowly to sharp
// deterioration. The rolling win rate over the last N resolved bets gives a
// faster early-warning signal when strategy performance degrades.
package calibration

import "fmt"

// ComputeRollingWinRate returns the win rate and actual sample size for the
// last `window` resolved bets. Only resolved bets (Outcome != nil) are counted.
//
// Returns (-1, 0) when there are fewer than 5 resolved bets — not enough data
// to produce a meaningful rate.
func ComputeRollingWinRate(records []BetRecord, window int) (winRate float64, sampleSize int) {
	// Collect resolved records in order (newest last).
	resolved := make([]BetRecord, 0, len(records))
	for _, r := range records {
		if r.Outcome != nil {
			resolved = append(resolved, r)
		}
	}
	if len(resolved) < 5 {
		return -1, 0
	}

	// Take the last `window` resolved bets.
	start := len(resolved) - window
	if start < 0 {
		start = 0
	}
	slice := resolved[start:]

	wins := 0
	for _, r := range slice {
		if *r.Outcome {
			wins++
		}
	}
	return float64(wins) / float64(len(slice)), len(slice)
}

// WinRateAlert checks whether the rolling win rate has fallen below threshold.
// Returns (true, message) when an alert should be fired, (false, "") otherwise.
//
// window is the rolling window size (e.g. 20).
// threshold is the minimum acceptable win rate (e.g. 0.35 for 35%).
func WinRateAlert(records []BetRecord, window int, threshold float64) (bool, string) {
	rate, n := ComputeRollingWinRate(records, window)
	if rate < 0 {
		return false, "" // insufficient data
	}
	if rate >= threshold {
		return false, "" // within acceptable range
	}
	msg := fmt.Sprintf("rolling win rate too low: %.0f%% over last %d bets (threshold %.0f%%)",
		rate*100, n, threshold*100)
	return true, msg
}
