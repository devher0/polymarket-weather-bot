package aggregation

import (
	"math"
	"testing"
)

func TestComputeSpreadEmpty(t *testing.T) {
	s := ComputeSpread(nil)
	if s.Sources != 0 || s.Spread != 0 || s.Mean != 0 {
		t.Errorf("empty probs: got %+v, want zero SourceSpread", s)
	}
	if s.Exceeds(0.30) {
		t.Error("empty spread should not exceed any threshold")
	}
}

func TestComputeSpreadAllSame(t *testing.T) {
	probs := []float64{0.65, 0.65, 0.65}
	s := ComputeSpread(probs)
	if s.Spread != 0 {
		t.Errorf("all-same: Spread = %.4f, want 0", s.Spread)
	}
	if math.Abs(s.Mean-0.65) > 1e-9 {
		t.Errorf("all-same: Mean = %.4f, want 0.65", s.Mean)
	}
	if s.StdDev != 0 {
		t.Errorf("all-same: StdDev = %.4f, want 0", s.StdDev)
	}
	if s.Label() != "tight" {
		t.Errorf("all-same: Label = %q, want tight", s.Label())
	}
}

func TestComputeSpreadTwoSources(t *testing.T) {
	probs := []float64{0.30, 0.70}
	s := ComputeSpread(probs)
	if math.Abs(s.Spread-0.40) > 1e-9 {
		t.Errorf("Spread = %.4f, want 0.40", s.Spread)
	}
	if math.Abs(s.Mean-0.50) > 1e-9 {
		t.Errorf("Mean = %.4f, want 0.50", s.Mean)
	}
	if s.Sources != 2 {
		t.Errorf("Sources = %d, want 2", s.Sources)
	}
	if s.Label() != "extreme" {
		t.Errorf("Label = %q, want extreme for spread=0.40", s.Label())
	}
}

func TestComputeSpreadWide(t *testing.T) {
	probs := []float64{0.20, 0.50, 0.70, 0.80}
	s := ComputeSpread(probs)
	// min=0.20 max=0.80 spread=0.60
	if math.Abs(s.Spread-0.60) > 1e-9 {
		t.Errorf("Spread = %.4f, want 0.60", s.Spread)
	}
	if !s.Exceeds(0.35) {
		t.Error("should exceed threshold 0.35 for spread=0.60")
	}
	if s.Exceeds(0) {
		t.Error("disabled gate (maxSpread=0) should never trigger")
	}
}

func TestExceedsDisabledGate(t *testing.T) {
	probs := []float64{0.10, 0.90}
	s := ComputeSpread(probs)
	// maxSpread=0 → disabled
	if s.Exceeds(0) {
		t.Error("Exceeds(0) should be false when gate is disabled")
	}
	// maxSpread=-1 → also disabled
	if s.Exceeds(-1) {
		t.Error("Exceeds(-1) should be false when gate is disabled")
	}
}

func TestLabelBoundaries(t *testing.T) {
	cases := []struct {
		probs []float64
		want  string
	}{
		{[]float64{0.50, 0.55}, "tight"},      // spread=0.05
		{[]float64{0.40, 0.55}, "moderate"},   // spread=0.15
		{[]float64{0.30, 0.55}, "wide"},        // spread=0.25
		{[]float64{0.10, 0.55}, "extreme"},     // spread=0.45
	}
	for _, tc := range cases {
		s := ComputeSpread(tc.probs)
		if s.Label() != tc.want {
			t.Errorf("probs=%v spread=%.2f: Label=%q, want %q", tc.probs, s.Spread, s.Label(), tc.want)
		}
	}
}
