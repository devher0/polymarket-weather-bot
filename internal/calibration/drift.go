// Package calibration — TASK-147: Calibration drift detector.
//
// Compares Brier score over a recent window against a base window and fires
// an alert when recent performance degrades beyond a configurable threshold.
//
// Example: BrierWindow(records, 14) vs BrierWindow(records, 28) with
// threshold=0.15 → alert if recent Brier is more than 15% worse than base.
package calibration

import (
	"fmt"
	"log"
	"sort"
	"time"
)

// BrierWindow computes the Brier score for resolved bets whose ResolvedAt falls
// within the last `days` calendar days. When ResolvedAt is zero, Timestamp is
// used as a fallback.
//
// Returns (score, count). count==0 means no resolved bets in the window.
func BrierWindow(records []BetRecord, days int) (score float64, count int) {
	cutoff := time.Now().UTC().AddDate(0, 0, -days)
	var sum float64
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
		o := 0.0
		if *r.Outcome {
			o = 1.0
		}
		diff := r.OurProbability - o
		sum += diff * diff
		count++
	}
	if count == 0 {
		return 0, 0
	}
	return sum / float64(count), count
}

// DriftAlert detects whether model performance has degraded in the recent window
// relative to a base window.
//
//   - recentDays: window for "current" performance (e.g. 14)
//   - baseDays: window for baseline (e.g. 30) — must be > recentDays
//   - threshold: fractional degradation that triggers alert (e.g. 0.15 = 15%)
//
// Returns (true, message) when recent Brier > baseBrier*(1+threshold) and both
// windows have at least 5 resolved bets.
// Returns (false, "") when there is insufficient data or no drift detected.
func DriftAlert(records []BetRecord, recentDays, baseDays int, threshold float64) (bool, string) {
	recentScore, recentCount := BrierWindow(records, recentDays)
	if recentCount < 5 {
		return false, "" // not enough recent data
	}

	// Base window: all bets in the base period (includes recent ones, but that's
	// intentional — base is the rolling average including recent).
	baseScore, baseCount := BrierWindow(records, baseDays)
	if baseCount < 5 {
		return false, "" // not enough base data
	}
	if baseScore == 0 {
		return false, "" // avoid division by zero / trivial case
	}

	ratio := recentScore / baseScore
	log.Printf("[calibration] drift check: recent(%.0fd)=%.4f base(%.0fd)=%.4f ratio=%.2f",
		float64(recentDays), recentScore, float64(baseDays), baseScore, ratio)

	if ratio <= 1+threshold {
		return false, ""
	}

	msg := fmt.Sprintf(
		"⚠️ Calibration drift detected: Brier %.4f (last %dd) vs %.4f (last %dd) — %.0f%% worse than baseline",
		recentScore, recentDays, baseScore, baseDays, (ratio-1)*100,
	)
	return true, msg
}

// DriftStatusLine returns a short human-readable drift summary.
// Format: "Drift: recent=0.1234 base=0.0987 (+25%)" or "Drift: OK (ratio=0.95)"
// Returns "" when there is insufficient data.
func DriftStatusLine(records []BetRecord) string {
	recentScore, recentCount := BrierWindow(records, 14)
	if recentCount < 5 {
		return ""
	}
	baseScore, baseCount := BrierWindow(records, 30)
	if baseCount < 5 {
		return ""
	}
	if baseScore == 0 {
		return ""
	}
	pct := (recentScore/baseScore - 1) * 100
	if pct > 0 {
		return fmt.Sprintf("Drift: recent=%.4f base=%.4f (+%.0f%% ⚠️)", recentScore, baseScore, pct)
	}
	return fmt.Sprintf("Drift: recent=%.4f base=%.4f (%.0f%% ✅)", recentScore, baseScore, pct)
}

// SortedResolved returns resolved records sorted by resolve time ascending.
// Exported for use in drift-related tests.
func SortedResolved(records []BetRecord) []BetRecord {
	var out []BetRecord
	for _, r := range records {
		if r.Outcome != nil {
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		ti := out[i].ResolvedAt
		if ti.IsZero() {
			ti = out[i].Timestamp
		}
		tj := out[j].ResolvedAt
		if tj.IsZero() {
			tj = out[j].Timestamp
		}
		return ti.Before(tj)
	})
	return out
}
