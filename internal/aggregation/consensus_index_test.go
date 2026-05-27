package aggregation

import (
	"math"
	"testing"
)

func TestConsensusIndexAllAgree(t *testing.T) {
	// All models say 0.80 probability — perfect agreement.
	models := []float64{0.80, 0.80, 0.80, 0.80}
	cr := ConsensusIndex(models, 0.50)
	if cr.StdDev > 0.001 {
		t.Errorf("StdDev = %.4f, want ~0 when all agree", cr.StdDev)
	}
	// direction_signal = |0.80 - 0.50| / 0.5 = 0.60; agreement = 1.0; consensus = 0.60
	if cr.Consensus < 0.55 || cr.Consensus > 0.65 {
		t.Errorf("Consensus = %.4f, want ~0.60 when all agree at 0.80 vs threshold 0.50", cr.Consensus)
	}
}

func TestConsensusIndexMaxDisagreement(t *testing.T) {
	// 50/50 split between 0.0 and 1.0 — maximum disagreement.
	models := []float64{0.0, 1.0, 0.0, 1.0}
	cr := ConsensusIndex(models, 0.50)
	// StdDev should be ~0.5, so agreement = max(0, 1-2*0.5) = 0.
	if cr.Consensus > 0.05 {
		t.Errorf("Consensus = %.4f, want ~0 for maximum disagreement", cr.Consensus)
	}
}

func TestSpreadScaleTightSpread(t *testing.T) {
	// Very tight spread: all sources agree → multiplier > 1.
	probs := []float64{0.70, 0.72, 0.68, 0.71}
	scale := SpreadScale(probs)
	if scale < 1.1 {
		t.Errorf("SpreadScale = %.2f, want > 1.1 for tight spread", scale)
	}
}

func TestSpreadScaleWideSpread(t *testing.T) {
	// Wide spread: sources strongly disagree → multiplier < 0.8.
	probs := []float64{0.20, 0.80, 0.15, 0.85}
	scale := SpreadScale(probs)
	if scale > 0.8 {
		t.Errorf("SpreadScale = %.2f, want < 0.8 for wide spread", scale)
	}
}

func TestHighConsensusKellyBoostAboveThreshold(t *testing.T) {
	cr := ConsensusResult{Consensus: 0.90}
	boost := HighConsensusKellyBoost(cr)
	if boost <= 1.0 || boost > 1.21 {
		t.Errorf("HighConsensusKellyBoost(0.90) = %.2f, want (1.0, 1.21]", boost)
	}
}

func TestHighConsensusKellyBoostBelowThreshold(t *testing.T) {
	cr := ConsensusResult{Consensus: 0.70}
	boost := HighConsensusKellyBoost(cr)
	if math.Abs(boost-1.0) > 0.001 {
		t.Errorf("HighConsensusKellyBoost(0.70) = %.2f, want 1.0", boost)
	}
}

func TestSkipOnLowConsensus(t *testing.T) {
	low := ConsensusResult{Consensus: 0.20}
	if !SkipOnLowConsensus(low, 0.30) {
		t.Error("should skip when consensus=0.20 < min=0.30")
	}
	high := ConsensusResult{Consensus: 0.50}
	if SkipOnLowConsensus(high, 0.30) {
		t.Error("should not skip when consensus=0.50 >= min=0.30")
	}
}
