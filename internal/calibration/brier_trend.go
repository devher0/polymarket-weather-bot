// brier_trend.go — TASK-178: Brier score trend alert.
//
// Detects a sustained, statistically robust worsening trend in weekly Brier
// scores using ordinary-least-squares linear regression. Unlike the point-in-
// time drift detector (TASK-147), this catches gradual deterioration over
// multiple weeks that may not trigger the 14/30-day window comparison.
package calibration

import (
	"fmt"
	"math"
	"time"
)

// weeklyBrierPoints groups resolved bets by ISO week and returns
// (weekIndex, brierScore) pairs sorted chronologically.
// Only weeks with at least minBetsPerWeek resolved bets are included.
//
// Returns nil when there are fewer than minWeeks qualifying weeks.
func weeklyBrierPoints(records []BetRecord, nWeeks, minBetsPerWeek int) (xs, ys []float64) {
	type weekBucket struct {
		sum   float64
		count int
	}
	buckets := make(map[string]*weekBucket)
	weekOrder := make([]string, 0)

	// Add a 7-day buffer so the window captures complete ISO weeks regardless
	// of which day of the week the function is called.
	cutoff := time.Now().UTC().AddDate(0, 0, -(nWeeks*7 + 7))

	for _, r := range records {
		if r.Outcome == nil {
			continue
		}
		ts := r.ResolvedAt
		if ts.IsZero() {
			ts = r.Timestamp
		}
		if ts.Before(cutoff) {
			continue
		}
		// ISO week key: "YYYY-WW"
		y, w := ts.UTC().ISOWeek()
		key := fmt.Sprintf("%04d-%02d", y, w)
		b, exists := buckets[key]
		if !exists {
			b = &weekBucket{}
			buckets[key] = b
			weekOrder = append(weekOrder, key)
		}
		o := 0.0
		if *r.Outcome {
			o = 1.0
		}
		diff := r.OurProbability - o
		b.sum += diff * diff
		b.count++
	}

	// Stable ordering: keys are YYYY-WW which sort lexicographically == chronologically.
	for i := 0; i < len(weekOrder)-1; i++ {
		for j := i + 1; j < len(weekOrder); j++ {
			if weekOrder[i] > weekOrder[j] {
				weekOrder[i], weekOrder[j] = weekOrder[j], weekOrder[i]
			}
		}
	}

	for _, key := range weekOrder {
		b := buckets[key]
		if b.count < minBetsPerWeek {
			continue
		}
		idx := float64(len(xs))
		xs = append(xs, idx)
		ys = append(ys, b.sum/float64(b.count))
	}
	return xs, ys
}

// linReg performs ordinary-least-squares linear regression on (xs, ys).
// Returns (slope, r2). Returns (0, 0) for fewer than 2 points.
func linReg(xs, ys []float64) (slope, r2 float64) {
	n := len(xs)
	if n < 2 {
		return 0, 0
	}

	var sumX, sumY, sumXY, sumX2 float64
	for i := range xs {
		sumX += xs[i]
		sumY += ys[i]
		sumXY += xs[i] * ys[i]
		sumX2 += xs[i] * xs[i]
	}
	fn := float64(n)
	denom := fn*sumX2 - sumX*sumX
	if denom == 0 {
		return 0, 0
	}
	slope = (fn*sumXY - sumX*sumY) / denom
	intercept := (sumY - slope*sumX) / fn

	// Compute R².
	meanY := sumY / fn
	var ssTot, ssRes float64
	for i := range xs {
		predicted := slope*xs[i] + intercept
		diff := ys[i] - predicted
		ssRes += diff * diff
		diff2 := ys[i] - meanY
		ssTot += diff2 * diff2
	}
	if ssTot == 0 {
		return slope, 1.0
	}
	r2 = 1.0 - ssRes/ssTot
	if r2 < 0 {
		r2 = 0
	}
	return slope, r2
}

// BrierTrend computes the linear slope and R² of weekly Brier scores over
// the last `weeks` calendar weeks.
//
// Only weeks with at least 5 resolved bets are included. Returns (0, 0) when
// there are fewer than 3 qualifying weeks (insufficient data for trend).
func BrierTrend(records []BetRecord, weeks int) (slope, r2 float64) {
	const minBetsPerWeek = 5
	xs, ys := weeklyBrierPoints(records, weeks, minBetsPerWeek)
	if len(xs) < 3 {
		return 0, 0
	}
	return linReg(xs, ys)
}

// BrierTrendAlert checks whether the Brier score trend represents a
// statistically robust worsening.
//
// Fires when:
//   - slope > slopeThreshold (default 0.015 per week) AND
//   - r2 > r2Threshold (default 0.7) AND
//   - at least 3 qualifying weeks exist
//
// Returns (true, message) on alert; (false, "") otherwise.
func BrierTrendAlert(records []BetRecord) (bool, string) {
	const (
		weeks          = 3
		slopeThreshold = 0.015
		r2Threshold    = 0.70
	)

	slope, r2 := BrierTrend(records, weeks)
	if slope == 0 && r2 == 0 {
		return false, "" // insufficient data
	}
	if !(slope > slopeThreshold && r2 > r2Threshold) {
		return false, ""
	}

	msg := fmt.Sprintf(
		"📉 Calibration trend: Brier worsening +%.3f/week (R²=%.2f) over last %d weeks",
		slope, r2, weeks,
	)
	return true, msg
}

// BrierTrendLine returns a human-readable one-liner for dashboards/logs.
// Returns "" when there is insufficient data.
func BrierTrendLine(records []BetRecord) string {
	slope, r2 := BrierTrend(records, 3)
	if slope == 0 && math.Abs(r2) < 1e-9 {
		return ""
	}
	dir := "improving"
	if slope > 0 {
		dir = "worsening"
	}
	return fmt.Sprintf("Brier trend: %+.4f/week (%s, R²=%.2f)", slope, dir, r2)
}
