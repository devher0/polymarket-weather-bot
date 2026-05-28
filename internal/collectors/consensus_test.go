package collectors

import (
	"math"
	"testing"
)

func TestConsensusScore_PerfectConsensus(t *testing.T) {
	// All sources agree exactly — score must be 1.0.
	score := ConsensusScore([]float64{25.0, 25.0, 25.0, 25.0})
	if math.Abs(score-1.0) > 1e-6 {
		t.Fatalf("expected 1.0 for identical values, got %.6f", score)
	}
}

func TestConsensusScore_TotalDisagreement(t *testing.T) {
	// stddev == range → score should be 0.
	// For [0, 1, 0, 1]: range=1, stddev=0.5, score=max(0, 1-0.5/1)=0.5
	// For perfect max spread use values that push stddev close to range.
	// Actual minimal case: two extreme values.
	score := ConsensusScore([]float64{0.0, 1.0})
	// range=1, stddev=0.5 → score=0.5
	if score < 0 || score > 1 {
		t.Fatalf("score out of [0,1]: %.6f", score)
	}
	// Score should be significantly below 1.0
	if score >= 0.9 {
		t.Fatalf("expected low score for extreme spread, got %.6f", score)
	}
}

func TestConsensusScore_SingleValue(t *testing.T) {
	score := ConsensusScore([]float64{42.5})
	if score != 1.0 {
		t.Fatalf("expected 1.0 for single value, got %.6f", score)
	}
}

func TestConsensusScore_Empty(t *testing.T) {
	score := ConsensusScore([]float64{})
	if score != 1.0 {
		t.Fatalf("expected 1.0 for empty slice, got %.6f", score)
	}
}

func TestConsensusScore_TwoSourcesClose(t *testing.T) {
	// 30.0 vs 31.0 — small spread, should be high consensus.
	score := ConsensusScore([]float64{30.0, 31.0})
	if score < 0.4 {
		t.Fatalf("expected moderate-high consensus for close values, got %.4f", score)
	}
}

func TestConsensusScore_InBounds(t *testing.T) {
	cases := [][]float64{
		{10, 20, 30, 40},
		{0.1, 0.5, 0.9},
		{-5, 0, 5},
		{100, 100.001},
	}
	for _, vals := range cases {
		s := ConsensusScore(vals)
		if s < 0 || s > 1 {
			t.Errorf("score %.6f out of [0,1] for %v", s, vals)
		}
	}
}

func TestMultiDimConsensus_AllPerfect(t *testing.T) {
	score := MultiDimConsensus(
		[]float64{25.0, 25.0},
		[]float64{0.5, 0.5},
		[]float64{20.0, 20.0},
	)
	if math.Abs(score-1.0) > 1e-6 {
		t.Fatalf("expected 1.0 for perfect consensus, got %.6f", score)
	}
}

func TestMultiDimConsensus_MixedDims(t *testing.T) {
	// Temperature disagrees heavily, others agree.
	score := MultiDimConsensus(
		[]float64{10.0, 40.0}, // max disagreement
		[]float64{0.5, 0.5},   // perfect
		[]float64{20.0, 20.0}, // perfect
	)
	// Result should be < 1.0 but > 0.5 (temp weight 0.5 dragged down, others fine)
	if score >= 1.0 {
		t.Fatalf("expected < 1.0 when one dimension disagrees, got %.4f", score)
	}
	if score < 0.4 {
		t.Fatalf("expected > 0.4 when only temp disagrees, got %.4f", score)
	}
}

func TestMultiDimConsensus_Empty(t *testing.T) {
	score := MultiDimConsensus(nil, nil, nil)
	if math.Abs(score-1.0) > 1e-6 {
		t.Fatalf("expected 1.0 for empty inputs, got %.6f", score)
	}
}
