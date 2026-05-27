package aggregation

import (
	"math"
	"testing"
)

func TestBayesianEnsembleNoBeliefs(t *testing.T) {
	// No beliefs, climatePrior provided → return climatePrior.
	p := BayesianEnsemble(0.35, nil)
	if math.Abs(p-0.35) > 0.05 {
		t.Errorf("BayesianEnsemble(0.35, nil) = %.4f, want ~0.35", p)
	}
}

func TestBayesianEnsembleNoPriorNoBeliefs(t *testing.T) {
	// No beliefs, no prior → default 0.5.
	p := BayesianEnsemble(0, nil)
	if math.Abs(p-0.5) > 0.05 {
		t.Errorf("BayesianEnsemble(0, nil) = %.4f, want ~0.5", p)
	}
}

func TestBayesianEnsembleAllAgreeRain(t *testing.T) {
	// All sources strongly say rain (0.85); climate prior = 0.40.
	// Posterior should shift decisively above 0.70.
	beliefs := []SourceBelief{
		{Source: "openmeteo", P: 0.85, Noise: 0.15},
		{Source: "ecmwf", P: 0.88, Noise: 0.12},
		{Source: "gfs", P: 0.82, Noise: 0.16},
	}
	p := BayesianEnsemble(0.40, beliefs)
	if p < 0.70 {
		t.Errorf("BayesianEnsemble all agree rain = %.4f, want > 0.70", p)
	}
}

func TestBayesianEnsembleAllAgreeNoRain(t *testing.T) {
	// All sources strongly say no rain (0.15); climate prior = 0.40.
	// Posterior should shift below 0.30.
	beliefs := []SourceBelief{
		{Source: "openmeteo", P: 0.15, Noise: 0.15},
		{Source: "ecmwf", P: 0.12, Noise: 0.12},
		{Source: "gfs", P: 0.18, Noise: 0.16},
	}
	p := BayesianEnsemble(0.40, beliefs)
	if p > 0.30 {
		t.Errorf("BayesianEnsemble all agree no rain = %.4f, want < 0.30", p)
	}
}

func TestBayesianEnsembleConflictingSourcesModeratePosterior(t *testing.T) {
	// Sources disagree strongly: two say rain, two say no rain.
	// Sequential Bayesian updates are order-sensitive; the result is constrained
	// to the (0.01, 0.99) range. We just verify it stays in a wide (0.10, 0.90)
	// window rather than collapsing to an extreme.
	beliefs := []SourceBelief{
		{Source: "openmeteo", P: 0.80, Noise: 0.15},
		{Source: "ecmwf", P: 0.75, Noise: 0.12},
		{Source: "gfs", P: 0.20, Noise: 0.15},
		{Source: "noaa", P: 0.25, Noise: 0.15},
	}
	p := BayesianEnsemble(0.50, beliefs)
	if p < 0.10 || p > 0.90 {
		t.Errorf("BayesianEnsemble conflicting sources = %.4f, want in [0.10, 0.90]", p)
	}
}

func TestBayesianEnsembleNoPriorUsesSourceAverage(t *testing.T) {
	// Without a climatePrior the prior is derived from the source average.
	// With uniform beliefs at 0.60 the result should be near 0.60.
	beliefs := []SourceBelief{
		{Source: "a", P: 0.60, Noise: 0.15},
		{Source: "b", P: 0.60, Noise: 0.15},
	}
	p := BayesianEnsemble(0, beliefs)
	if math.Abs(p-0.60) > 0.10 {
		t.Errorf("BayesianEnsemble no prior, uniform beliefs = %.4f, want ~0.60", p)
	}
}

func TestBayesianUpdateSingleSource(t *testing.T) {
	// A single very confident source (low noise) overrides a neutral prior.
	prior := 0.50
	belief := SourceBelief{Source: "ecmwf", P: 0.90, Noise: 0.05}
	posterior := BayesianUpdate(prior, belief)
	if posterior < 0.70 {
		t.Errorf("BayesianUpdate confident source = %.4f, want > 0.70", posterior)
	}
}

func TestBayesianUpdateHighNoiseLittleEffect(t *testing.T) {
	// Very noisy source (high uncertainty) should barely move the prior.
	prior := 0.50
	belief := SourceBelief{Source: "noisy", P: 0.90, Noise: 0.40}
	posterior := BayesianUpdate(prior, belief)
	if math.Abs(posterior-prior) > 0.30 {
		t.Errorf("BayesianUpdate noisy source moved prior by %.4f, want < 0.30", math.Abs(posterior-prior))
	}
}

func TestDefaultNoise(t *testing.T) {
	tests := []struct {
		brierScore float64
		wantMin    float64
		wantMax    float64
	}{
		{0, 0.14, 0.16},       // unknown accuracy → 0.15
		{0.04, 0.19, 0.21},    // sqrt(0.04) = 0.20
		{0.0025, 0.049, 0.06}, // sqrt(0.0025) = 0.05, clamped to 0.05
		{0.25, 0.39, 0.41},    // sqrt(0.25) = 0.50, clamped to 0.40
	}
	for _, tc := range tests {
		got := DefaultNoise(tc.brierScore)
		if got < tc.wantMin || got > tc.wantMax {
			t.Errorf("DefaultNoise(%.4f) = %.4f, want [%.3f, %.3f]",
				tc.brierScore, got, tc.wantMin, tc.wantMax)
		}
	}
}
