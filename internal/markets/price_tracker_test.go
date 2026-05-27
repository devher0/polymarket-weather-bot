// price_tracker_test.go — unit tests for DetectMomentum (TASK-060).
package markets

import (
	"testing"
	"time"
)

// makePP creates a PricePoint with the given yes_price at a synthetic timestamp.
func makePP(yesPrice float64, offset int) PricePoint {
	return PricePoint{
		Timestamp: time.Date(2026, 5, 27, 12, offset, 0, 0, time.UTC),
		YesPrice:  yesPrice,
		NoPrice:   1.0 - yesPrice,
	}
}

func TestDetectMomentum_InsufficientHistory(t *testing.T) {
	// Fewer than momentumMinPoints (4) snapshots → always neutral.
	history := []PricePoint{makePP(0.50, 0), makePP(0.55, 1), makePP(0.60, 2)}
	dir, strength := DetectMomentum("YES", history)
	if dir != MomentumNeutral {
		t.Errorf("expected Neutral, got %v", dir)
	}
	if strength != 0 {
		t.Errorf("expected strength 0, got %.3f", strength)
	}
}

func TestDetectMomentum_FavorableYES(t *testing.T) {
	// 4 consecutive rises in YES price.
	history := []PricePoint{
		makePP(0.40, 0),
		makePP(0.45, 1),
		makePP(0.50, 2),
		makePP(0.55, 3),
		makePP(0.60, 4),
	}
	dir, strength := DetectMomentum("YES", history)
	if dir != MomentumFavorable {
		t.Errorf("expected Favorable, got %v", dir)
	}
	if strength <= 0 {
		t.Errorf("expected positive strength, got %.3f", strength)
	}
}

func TestDetectMomentum_FavorableNO(t *testing.T) {
	// For NO side, favorable means NO price is rising (i.e. YES is falling).
	history := []PricePoint{
		makePP(0.65, 0),
		makePP(0.60, 1),
		makePP(0.55, 2),
		makePP(0.50, 3),
		makePP(0.45, 4),
	}
	dir, strength := DetectMomentum("NO", history)
	if dir != MomentumFavorable {
		t.Errorf("expected Favorable for NO side, got %v", dir)
	}
	if strength <= 0 {
		t.Errorf("expected positive strength, got %.3f", strength)
	}
}

func TestDetectMomentum_AdverseYES(t *testing.T) {
	// YES price falling consecutively.
	history := []PricePoint{
		makePP(0.70, 0),
		makePP(0.65, 1),
		makePP(0.60, 2),
		makePP(0.55, 3),
		makePP(0.50, 4),
	}
	dir, strength := DetectMomentum("YES", history)
	if dir != MomentumAdverse {
		t.Errorf("expected Adverse, got %v", dir)
	}
	if strength <= 0 {
		t.Errorf("expected positive strength, got %.3f", strength)
	}
}

func TestDetectMomentum_Neutral_MixedMoves(t *testing.T) {
	// Zigzag — no sustained run.
	history := []PricePoint{
		makePP(0.50, 0),
		makePP(0.55, 1),
		makePP(0.50, 2),
		makePP(0.55, 3),
		makePP(0.50, 4),
	}
	dir, strength := DetectMomentum("YES", history)
	if dir != MomentumNeutral {
		t.Errorf("expected Neutral for zigzag, got %v (strength=%.3f)", dir, strength)
	}
}

func TestDetectMomentum_RunBelowThreshold(t *testing.T) {
	// Only 2 consecutive rises — below momentumRunRequired (3).
	history := []PricePoint{
		makePP(0.50, 0),
		makePP(0.48, 1), // one dip
		makePP(0.52, 2),
		makePP(0.56, 3),
	}
	dir, _ := DetectMomentum("YES", history)
	if dir != MomentumNeutral {
		t.Errorf("expected Neutral for run<3, got %v", dir)
	}
}

func TestDetectMomentum_StrengthCapped(t *testing.T) {
	// Long run → strength should be capped at 1.0.
	history := []PricePoint{
		makePP(0.20, 0),
		makePP(0.30, 1),
		makePP(0.40, 2),
		makePP(0.50, 3),
		makePP(0.60, 4),
		makePP(0.70, 5),
		makePP(0.80, 6),
	}
	_, strength := DetectMomentum("YES", history)
	if strength > 1.0 {
		t.Errorf("strength must be ≤1.0, got %.3f", strength)
	}
}

func TestDetectMomentum_FlatPrices(t *testing.T) {
	// Flat prices (delta==0 for all) — no up or down run → neutral.
	history := []PricePoint{
		makePP(0.50, 0),
		makePP(0.50, 1),
		makePP(0.50, 2),
		makePP(0.50, 3),
		makePP(0.50, 4),
	}
	dir, strength := DetectMomentum("YES", history)
	if dir != MomentumNeutral {
		t.Errorf("expected Neutral for flat prices, got %v (strength=%.3f)", dir, strength)
	}
	if strength != 0 {
		t.Errorf("expected strength 0, got %.3f", strength)
	}
}
