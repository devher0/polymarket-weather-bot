// Package aggregation provides advanced multi-model consensus analytics.
//
// TASK-102: Professional weather traders and energy firms use consensus metrics
// to filter out noise: when all major models agree on a forecast (ECMWF, GFS,
// HRRR, OpenMeteo), the signal is much more reliable than when models diverge.
// This package implements a ConsensusIndex that mirrors that professional
// workflow.
package aggregation

import (
	"math"
)

// ConsensusResult holds the output of a multi-model consensus analysis.
type ConsensusResult struct {
	// Consensus is 0–1: how much the models agree (1 = all agree, 0 = 50/50 split).
	Consensus float64
	// Direction is the average probability across all models.
	Direction float64
	// StdDev is the standard deviation of probabilities (raw spread measure).
	StdDev float64
	// Count is the number of models contributing.
	Count int
}

// ConsensusIndex computes a consensus score from a set of per-model probability
// estimates (each in [0,1]) and a market threshold (also in [0,1]).
//
// threshold is the market price (the "consensus baseline" — what the market
// believes). When all models are above threshold, ConsensusIndex = 1.0 and
// Direction > threshold. When models straddle the threshold, ConsensusIndex → 0.
//
// Formula:
//
//	mean_p   = average(models)
//	stddev_p = stddev(models)
//	agreement = 1 - 2*stddev_p  (1 when all agree, 0 when stddev=0.5)
//	direction_signal = |mean_p - threshold| / (0.5)  (0 = on threshold, 1 = far away)
//	ConsensusIndex = agreement × direction_signal
func ConsensusIndex(models []float64, threshold float64) ConsensusResult {
	if len(models) == 0 {
		return ConsensusResult{}
	}

	// Compute mean.
	sum := 0.0
	for _, p := range models {
		sum += p
	}
	mean := sum / float64(len(models))

	// Compute stddev.
	var variance float64
	for _, p := range models {
		d := p - mean
		variance += d * d
	}
	variance /= float64(len(models))
	sd := math.Sqrt(variance)

	// Agreement: high when sd is small.
	agreement := math.Max(0, 1-2*sd)

	// Directional signal: how far the consensus is from the threshold.
	dirSignal := math.Min(1, math.Abs(mean-threshold)/0.5)

	consensus := agreement * dirSignal

	return ConsensusResult{
		Consensus: consensus,
		Direction: mean,
		StdDev:    sd,
		Count:     len(models),
	}
}

// SpreadScale returns a Kelly fraction multiplier based on source spread.
// The multiplier adjusts bet size according to how much the weather models agree.
//
// TASK-105: Small spread (all sources agree) → bet more confidently.
// Large spread (sources disagree) → reduce bet size.
//
//	spread < 0.10 → 1.30 (strong agreement)
//	0.10–0.20     → 1.00 (neutral)
//	0.20–0.35     → 0.75
//	> 0.35        → 0.50 (strong disagreement)
func SpreadScale(sourceProbs []float64) float64 {
	if len(sourceProbs) < 2 {
		return 1.0 // not enough sources to measure spread
	}
	cr := ConsensusIndex(sourceProbs, 0.5) // 0.5 = neutral threshold
	sd := cr.StdDev
	switch {
	case sd < 0.10:
		return 1.30
	case sd < 0.20:
		return 1.0 + (0.20-sd)/0.10*0.30 // 1.0→1.30 linearly
	case sd < 0.35:
		return 0.75 + (0.35-sd)/0.15*0.25 // 0.75→1.0 linearly
	default:
		return 0.50
	}
}

// SkipOnLowConsensus returns true when the consensus is too low to bet.
// This mirrors the professional rule: when models can't agree, skip the market.
//
// TASK-102: ConsensusIndex < 0.30 → skip bet regardless of edge.
func SkipOnLowConsensus(cr ConsensusResult, minConsensus float64) bool {
	if minConsensus <= 0 {
		minConsensus = 0.30
	}
	return cr.Consensus < minConsensus
}

// HighConsensusKellyBoost returns an additional Kelly multiplier for very
// high-consensus situations where we should bet more aggressively.
//
// TASK-102: ConsensusIndex > 0.80 → increase Kelly by 20%.
func HighConsensusKellyBoost(cr ConsensusResult) float64 {
	if cr.Consensus > 0.80 {
		// Scale from 1.0 at 0.80 to 1.20 at 1.0.
		return 1.0 + (cr.Consensus-0.80)/0.20*0.20
	}
	return 1.0
}
