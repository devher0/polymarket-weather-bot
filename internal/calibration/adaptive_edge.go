// adaptive_edge.go — adjusts min_edge based on recent calibration quality.
//
// If the bot has been well-calibrated in recent bets (low rolling Brier score),
// we can afford to enter positions with a slightly smaller edge. If performance
// has been poor, we tighten the threshold to require more conviction before betting.
//
// Rolling window: last 20 resolved bets.
// Brier < 0.10 (excellent) → factor 0.90 (relax edge by 10%)
// Brier > 0.22 (near-random) → factor 1.20 (tighten edge by 20%)
// Between → linear interpolation
// Result is clamped to [base × 0.75, base × 1.50].
package calibration

import (
	"log/slog"
	"math"
)

const (
	adaptiveWindowSize    = 20
	adaptiveBrierLow      = 0.10 // excellent calibration → relax
	adaptiveBrierHigh     = 0.22 // near-random → tighten
	adaptiveFactorLow     = 0.90 // 10% relaxation
	adaptiveFactorHigh    = 1.20 // 20% tightening
	adaptiveFloorFactor   = 0.75
	adaptiveCeilingFactor = 1.50
	adaptiveMinSamples    = 5 // need at least this many resolved bets to adjust
)

// AdaptiveMinEdge adjusts the base minimum edge threshold based on recent
// calibration quality (rolling Brier score over the last 20 resolved bets).
//
// Returns baseMinEdge unchanged if fewer than adaptiveMinSamples resolved bets
// exist — there is not enough signal to deviate from the configured default.
func AdaptiveMinEdge(records []BetRecord, baseMinEdge float64) float64 {
	// Collect resolved records only.
	resolved := make([]BetRecord, 0, len(records))
	for _, r := range records {
		if r.Outcome != nil {
			resolved = append(resolved, r)
		}
	}
	if len(resolved) < adaptiveMinSamples {
		return baseMinEdge // insufficient history
	}
	// Keep only the most recent window.
	if len(resolved) > adaptiveWindowSize {
		resolved = resolved[len(resolved)-adaptiveWindowSize:]
	}

	// Rolling Brier score over the window.
	var brierSum float64
	for _, r := range resolved {
		outcome := 0.0
		if *r.Outcome {
			outcome = 1.0
		}
		diff := r.OurProbability - outcome
		brierSum += diff * diff
	}
	rollingBrier := brierSum / float64(len(resolved))

	// Map Brier → scale factor via linear interpolation between the two extremes.
	var factor float64
	switch {
	case rollingBrier <= adaptiveBrierLow:
		factor = adaptiveFactorLow
	case rollingBrier >= adaptiveBrierHigh:
		factor = adaptiveFactorHigh
	default:
		t := (rollingBrier - adaptiveBrierLow) / (adaptiveBrierHigh - adaptiveBrierLow)
		factor = adaptiveFactorLow + t*(adaptiveFactorHigh-adaptiveFactorLow)
	}

	adjusted := baseMinEdge * factor

	// Clamp to [floor, ceiling] of base.
	floor := baseMinEdge * adaptiveFloorFactor
	ceiling := baseMinEdge * adaptiveCeilingFactor
	adjusted = math.Max(floor, math.Min(ceiling, adjusted))

	slog.Debug("adaptive min_edge",
		"base", baseMinEdge,
		"rolling_brier", math.Round(rollingBrier*10000)/10000,
		"factor", math.Round(factor*1000)/1000,
		"adjusted", math.Round(adjusted*10000)/10000,
		"window_n", len(resolved),
	)

	return adjusted
}
