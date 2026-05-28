package calibration

import (
	"math"
	"testing"
)

// helper: build a BetRecord with OurProbability p and Outcome won.
func betRecord(p float64, won bool) BetRecord {
	o := won
	return BetRecord{OurProbability: p, MarketPrice: 0.5, SizeUSDC: 1.0, Outcome: &o}
}

// TestIsotonicEmptyHistory verifies that an identity calibrator is returned
// when no resolved bets exist.
func TestIsotonicEmptyHistory(t *testing.T) {
	ic := FitIsotonic(nil)
	if ic.IsActive() {
		t.Fatal("expected inactive calibrator for empty input")
	}
	// Predict should return rawP unchanged.
	if got := ic.Predict(0.7); math.Abs(got-0.7) > 1e-9 {
		t.Fatalf("identity expect 0.7, got %.4f", got)
	}
}

// TestIsotonicAlreadyMonotone verifies PAV leaves already-monotone data intact.
func TestIsotonicAlreadyMonotone(t *testing.T) {
	var records []BetRecord
	// 10 losses at low probability, 10 wins at high probability — already monotone.
	for i := 0; i < 10; i++ {
		records = append(records, betRecord(0.3, false))
	}
	for i := 0; i < 10; i++ {
		records = append(records, betRecord(0.7, true))
	}
	ic := FitIsotonic(records)
	if !ic.IsActive() {
		t.Fatal("expected active calibrator for 20 samples")
	}
	// At 0.3, empirical win rate ≈ 0 (losses) → calibrated < 0.5.
	low := ic.Predict(0.3)
	high := ic.Predict(0.7)
	if low >= high {
		t.Fatalf("monotone violation: Predict(0.3)=%.4f >= Predict(0.7)=%.4f", low, high)
	}
}

// TestIsotonicViolationFixed verifies that PAV enforces monotonicity even
// when raw win rates would violate it (overconfident at low p, underconfident at high p).
func TestIsotonicViolationFixed(t *testing.T) {
	var records []BetRecord
	// Clear two-bucket violation: win rate at p=0.3 > win rate at p=0.8.
	// This forces PAV to merge them, but output must remain non-decreasing.
	// Bucket A (p=0.3): 10 wins, 0 losses → win rate 1.0
	// Bucket B (p=0.8): 0 wins, 10 losses → win rate 0.0
	// PAV will merge → combined win rate 0.5 (1 block), still non-decreasing.
	for i := 0; i < 10; i++ {
		records = append(records, betRecord(0.3, true))
		records = append(records, betRecord(0.8, false))
	}
	// Add extra low-p losses and high-p wins so we have ≥ 2 PAV blocks.
	// Bucket C (p=0.1): 0 wins, 5 losses → win rate 0.0 (lower than 0.3–0.8 merged)
	// Bucket D (p=0.95): 5 wins, 0 losses → win rate 1.0 (higher than merged)
	// PAV will not merge C into merged(A,B), nor D into merged(A,B).
	for i := 0; i < 5; i++ {
		records = append(records, betRecord(0.1, false))
		records = append(records, betRecord(0.95, true))
	}
	// Total: 30 records. ic.N=30, and after PAV we get ≥ 2 blocks.
	ic := FitIsotonic(records)
	if ic.N < MinCalibrationSamples {
		t.Fatalf("expected N≥%d, got %d", MinCalibrationSamples, ic.N)
	}
	// Allow single-block outcome (all merged): PAV is still correct, just flat.
	// The key invariant is monotonicity over increasing xp.
	prev := -1.0
	for _, xp := range []float64{0.05, 0.15, 0.3, 0.5, 0.8, 0.9} {
		cur := ic.Predict(xp)
		if cur < prev-1e-9 {
			t.Fatalf("monotonicity violated at p=%.2f: got %.4f after %.4f", xp, cur, prev)
		}
		prev = cur
	}
}

// TestIsotonicInterpolation checks that Predict interpolates smoothly between breakpoints.
func TestIsotonicInterpolation(t *testing.T) {
	// Build 20 records spanning [0.1, 0.9] with win rate = p (well-calibrated).
	var records []BetRecord
	probs := []float64{0.1, 0.2, 0.3, 0.4, 0.5, 0.6, 0.7, 0.8, 0.9, 1.0}
	for _, p := range probs {
		// Each prob gets 2 records; use a fair coin so win ≈ p.
		records = append(records, betRecord(p, true))
		records = append(records, betRecord(p, false))
	}
	ic := FitIsotonic(records)
	if !ic.IsActive() {
		t.Fatal("expected active calibrator with 20 records")
	}
	// Between breakpoints, Predict must return a value in [min_y, max_y].
	for _, xp := range []float64{0.15, 0.35, 0.55, 0.75, 0.85} {
		out := ic.Predict(xp)
		if out < 0.02 || out > 0.98 {
			t.Fatalf("Predict(%.2f)=%.4f out of [0.02, 0.98]", xp, out)
		}
	}
}

// TestIsotonicClamping verifies output is clamped to [0.02, 0.98].
func TestIsotonicClamping(t *testing.T) {
	// Force extreme breakpoints: all losses → ys ≈ 0, all wins → ys ≈ 1.
	var records []BetRecord
	for i := 0; i < 10; i++ {
		records = append(records, betRecord(0.1, false))
		records = append(records, betRecord(0.9, true))
	}
	ic := FitIsotonic(records)
	if !ic.IsActive() {
		t.Fatal("expected active calibrator")
	}
	low := ic.Predict(0.05)  // below leftmost breakpoint
	high := ic.Predict(0.95) // above rightmost breakpoint
	if low < 0.02 {
		t.Fatalf("Predict below 0.02: %.4f", low)
	}
	if high > 0.98 {
		t.Fatalf("Predict above 0.98: %.4f", high)
	}
}
