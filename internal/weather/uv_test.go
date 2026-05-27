// uv_test.go — unit tests for UVProbability (TASK-083).
package weather

import "testing"

func TestUVProbability_AboveThresholdHigh(t *testing.T) {
	f := Forecast{UVIndexMax: 11, City: "miami"}
	p := UVProbability(f, 8)
	if p < 0.90 {
		t.Errorf("expected p >= 0.90 for UV=11 threshold=8, got %.3f", p)
	}
}

func TestUVProbability_AtThreshold(t *testing.T) {
	f := Forecast{UVIndexMax: 8, City: "miami"}
	p := UVProbability(f, 8)
	if p < 0.65 || p > 0.80 {
		t.Errorf("expected p in [0.65, 0.80] for UV=8 threshold=8, got %.3f", p)
	}
}

func TestUVProbability_BelowThreshold(t *testing.T) {
	f := Forecast{UVIndexMax: 4, City: "london"}
	p := UVProbability(f, 8)
	if p > 0.35 {
		t.Errorf("expected p < 0.35 for UV=4 threshold=8, got %.3f", p)
	}
}

func TestUVProbability_FarBelowThreshold(t *testing.T) {
	f := Forecast{UVIndexMax: 1, City: "london"}
	p := UVProbability(f, 8)
	if p > 0.08 {
		t.Errorf("expected p < 0.08 for UV=1 threshold=8, got %.3f", p)
	}
}

func TestUVProbability_ZeroUVDataUnavailable(t *testing.T) {
	f := Forecast{UVIndexMax: 0}
	p := UVProbability(f, 8)
	if p != 0.10 {
		t.Errorf("expected 0.10 base rate when UVIndexMax=0, got %.3f", p)
	}
}

func TestUVProbability_ZeroThresholdUsesDefault(t *testing.T) {
	// threshold=0 → default 8
	f := Forecast{UVIndexMax: 10}
	p := UVProbability(f, 0)
	if p < 0.90 {
		t.Errorf("expected p >= 0.90 for UV=10 with default threshold=8, got %.3f", p)
	}
}

func TestUVProbability_ExtremeUV(t *testing.T) {
	f := Forecast{UVIndexMax: 14, City: "miami"}
	p := UVProbability(f, 8)
	if p > 0.97 {
		t.Errorf("probability should be clamped <= 0.97, got %.3f", p)
	}
	if p < 0.90 {
		t.Errorf("expected high probability for extreme UV, got %.3f", p)
	}
}

func TestUVProbability_Monotonic(t *testing.T) {
	// Probability should increase monotonically as UV index increases.
	threshold := 8.0
	prev := -1.0
	for uv := 1.0; uv <= 13.0; uv++ {
		f := Forecast{UVIndexMax: uv}
		p := UVProbability(f, threshold)
		if p < prev {
			t.Errorf("UVProbability not monotonic: UV=%.0f p=%.3f < UV=%.0f p=%.3f", uv, p, uv-1, prev)
		}
		prev = p
	}
}
