package calibration

import (
	"testing"
)

func TestWilsonCI_ZeroN(t *testing.T) {
	lo, hi := WilsonCI95(0, 0)
	if lo != 0.0 || hi != 1.0 {
		t.Errorf("n=0: expected (0,1) got (%.4f,%.4f)", lo, hi)
	}
}

func TestWilsonCI_AllWins_N1(t *testing.T) {
	lo, hi := WilsonCI95(1, 1)
	if lo < 0 || hi > 1 {
		t.Errorf("n=1 all wins: bounds out of [0,1]: (%.4f,%.4f)", lo, hi)
	}
	// With n=1 and p=1, Wilson lo should be considerably lower than 1.0
	if lo > 0.80 {
		t.Errorf("n=1 all wins: expected lo < 0.80 (Wilson shrinks toward center), got %.4f", lo)
	}
}

func TestWilsonCI_60pct_N10(t *testing.T) {
	lo, hi := WilsonCI95(6, 10)
	// p = 0.6, n = 10 → wide interval
	if lo >= 0.6 || hi <= 0.6 {
		t.Errorf("n=10, wins=6: expected 0.6 inside CI (%.4f,%.4f)", lo, hi)
	}
	// Interval should be roughly [0.26, 0.87] — check it's wide
	if hi-lo < 0.40 {
		t.Errorf("n=10: expected wide CI (>0.40), got width %.4f", hi-lo)
	}
}

func TestWilsonCI_60pct_N100(t *testing.T) {
	lo, hi := WilsonCI95(60, 100)
	if lo >= 0.6 || hi <= 0.6 {
		t.Errorf("n=100, wins=60: expected 0.6 inside CI (%.4f,%.4f)", lo, hi)
	}
	// Narrower than n=10 case (Wilson 95% CI for n=100, p=0.6 is ~0.19 wide)
	if hi-lo > 0.22 {
		t.Errorf("n=100: expected CI width < 0.22, got %.4f", hi-lo)
	}
	// Much narrower than n=10 case
	lo10, hi10 := WilsonCI95(6, 10)
	if hi-lo >= hi10-lo10 {
		t.Errorf("n=100 CI should be narrower than n=10 CI")
	}
}

func TestIsSignificantlyAbove50(t *testing.T) {
	// 6/10 = 60%: CI is wide, should NOT be significantly above 50%
	if IsSignificantlyAbove50(6, 10) {
		t.Error("6/10: should NOT be significantly above 50% (too few samples)")
	}
	// 60/100 = 60%: should be significantly above 50%
	if !IsSignificantlyAbove50(60, 100) {
		lo, _ := WilsonCI95(60, 100)
		t.Errorf("60/100: SHOULD be significantly above 50%% (lo=%.4f)", lo)
	}
	// 15/30 = 50%: exactly 50%, should NOT be significantly above
	if IsSignificantlyAbove50(15, 30) {
		t.Error("15/30 = 50%: should NOT be significantly above 50%")
	}
	// n<5: always false
	if IsSignificantlyAbove50(4, 4) {
		t.Error("n=4: should return false regardless of win rate")
	}
}

func TestFormatCI_SmallN(t *testing.T) {
	s := FormatCI(0, 1, 3)
	if s != "~" {
		t.Errorf("n=3: expected '~', got %q", s)
	}
}

func TestFormatCI_LargeN(t *testing.T) {
	lo, hi := WilsonCI95(60, 100)
	s := FormatCI(lo, hi, 100)
	// half-width ≈ (hi-lo)/2 * 100 — should produce something like "±9%"
	halfWidth := (hi - lo) / 2.0 * 100.0
	_ = halfWidth
	// Just check it starts with ± and ends with %
	if len(s) < 3 || []rune(s)[0] != '±' || s[len(s)-1] != '%' {
		t.Errorf("FormatCI: unexpected format %q", s)
	}
}

func TestWinRateWithCI_Zero(t *testing.T) {
	s := WinRateWithCI(0, 0)
	if s != "  — " {
		t.Errorf("n=0: expected '  — ', got %q", s)
	}
}

func TestWinRateWithCI_Normal(t *testing.T) {
	s := WinRateWithCI(60, 100)
	if len(s) == 0 {
		t.Error("expected non-empty string")
	}
	// Should contain a % and ±
	if s == "  — " {
		t.Error("unexpected missing data for n=100")
	}
}
