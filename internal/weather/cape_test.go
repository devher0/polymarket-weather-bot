// cape_test.go — unit tests for CAPEStormProbability (TASK-089).
package weather

import "testing"

func TestCAPEStormProbability_Zero(t *testing.T) {
	p := CAPEStormProbability(0)
	if p != 0 {
		t.Errorf("expected 0 for cape=0 (no data), got %.3f", p)
	}
}

func TestCAPEStormProbability_Negative(t *testing.T) {
	p := CAPEStormProbability(-100)
	if p != 0 {
		t.Errorf("expected 0 for negative cape, got %.3f", p)
	}
}

func TestCAPEStormProbability_Weak(t *testing.T) {
	// cape < 500 → low risk ~0.05
	p := CAPEStormProbability(200)
	if p < 0.04 || p > 0.07 {
		t.Errorf("expected ~0.05 for cape=200, got %.3f", p)
	}
}

func TestCAPEStormProbability_WeakBoundary(t *testing.T) {
	// Just below 500
	p := CAPEStormProbability(499)
	if p < 0.04 || p > 0.07 {
		t.Errorf("expected ~0.05 for cape=499, got %.3f", p)
	}
}

func TestCAPEStormProbability_Moderate(t *testing.T) {
	// cape 500–1500 → moderate, midpoint ~1000 should be ~0.15
	p := CAPEStormProbability(1000)
	if p < 0.10 || p > 0.20 {
		t.Errorf("expected 0.10–0.20 for cape=1000, got %.3f", p)
	}
}

func TestCAPEStormProbability_ModerateUpperBound(t *testing.T) {
	// Just below 1500 → near 0.25
	p := CAPEStormProbability(1499)
	if p < 0.22 || p > 0.28 {
		t.Errorf("expected near 0.25 for cape=1499, got %.3f", p)
	}
}

func TestCAPEStormProbability_High(t *testing.T) {
	// cape 1500–3000 → high; midpoint ~2250 should be ~0.425
	p := CAPEStormProbability(2250)
	if p < 0.35 || p > 0.55 {
		t.Errorf("expected 0.35–0.55 for cape=2250, got %.3f", p)
	}
}

func TestCAPEStormProbability_HighUpperBound(t *testing.T) {
	// Just below 3000 → near 0.60
	p := CAPEStormProbability(2999)
	if p < 0.55 || p > 0.65 {
		t.Errorf("expected near 0.60 for cape=2999, got %.3f", p)
	}
}

func TestCAPEStormProbability_VeryHigh(t *testing.T) {
	// cape > 3000 → very high → approaches 0.90
	p := CAPEStormProbability(4000)
	if p < 0.75 || p > 0.92 {
		t.Errorf("expected 0.75–0.90 for cape=4000, got %.3f", p)
	}
}

func TestCAPEStormProbability_Extreme(t *testing.T) {
	// cape >> 3000 → capped at ~0.90
	p := CAPEStormProbability(6000)
	if p > 0.91 {
		t.Errorf("probability should be capped near 0.90, got %.3f", p)
	}
	if p < 0.88 {
		t.Errorf("expected near 0.90 for extreme cape=6000, got %.3f", p)
	}
}

func TestCAPEStormProbability_Monotonic(t *testing.T) {
	// Probability should increase monotonically with CAPE.
	capes := []float64{1, 250, 500, 750, 1000, 1250, 1500, 2000, 2500, 3000, 4000, 5000}
	prev := -1.0
	for _, cape := range capes {
		p := CAPEStormProbability(cape)
		if p < prev {
			t.Errorf("CAPEStormProbability not monotonic: cape=%.0f p=%.3f < prev=%.3f", cape, p, prev)
		}
		prev = p
	}
}
