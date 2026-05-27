// deadheat.go — dead-heat zone detection and probability adjustment (TASK-129).
//
// Some markets are phrased as "exactly X°C" or "between X and Y mm", meaning
// if our forecast is very close to the boundary value, the event is essentially
// a coin flip.  Betting when we're inside the "dead-heat zone" drains bankroll
// even when our model says we have a tiny edge — that edge is likely noise.
//
// Strategy: shrink ourP towards 0.5 proportionally to how close we are to the
// threshold, using ensemble uncertainty (σ) as the zone half-width.
package strategy

import (
	"fmt"
	"math"

	"github.com/devher0/polymarket-weather-bot/internal/collectors"
	"github.com/devher0/polymarket-weather-bot/internal/markets"
)

// defaultDeadHeatSigma is used when EnsembleUncertainty is unavailable (°C).
const defaultDeadHeatSigma = 2.0

// IsNearBoundary reports whether the forecast temperature is within ±σ of
// the market's temperature threshold.  Only meaningful for heat/cold signals
// with a parsed threshold; returns false for all other signals or when σ ≤ 0.
func IsNearBoundary(ff *collectors.FusedForecast, m markets.Market) bool {
	if ff == nil || m.ThresholdC == 0 {
		return false
	}
	if m.Signal != "heat" && m.Signal != "cold" {
		return false
	}
	sigma := ff.EnsembleUncertainty
	if sigma <= 0 {
		sigma = defaultDeadHeatSigma
	}
	distance := math.Abs(ff.Forecast.MaxTempC - m.ThresholdC)
	return distance < sigma
}

// DeadHeatAdjust squeezes p towards 0.5 based on how close the forecast is
// to the threshold boundary relative to sigma.
//
//   - When distanceToThreshold == 0 (exactly at threshold) → p is pulled fully
//     to 0.5 regardless of its original value.
//   - When distanceToThreshold >= sigma → no adjustment (p returned unchanged).
//   - In between: linear interpolation of the pull strength.
//
// This prevents confident bets on coin-flip outcomes caused by model precision
// rather than genuine knowledge.
func DeadHeatAdjust(p, distanceToThreshold, sigma float64) float64 {
	if sigma <= 0 || distanceToThreshold >= sigma {
		return p
	}
	// pull = fraction of the way from p to 0.5 to apply.
	// pull = 1 at distance=0, pull = 0 at distance=sigma.
	pull := 1.0 - distanceToThreshold/sigma
	adjusted := p + pull*(0.5-p)
	return adjusted
}

// applyDeadHeat checks whether the forecast is near the market's temperature
// boundary and, if so, adjusts ourP towards 0.5.  It also appends a note to
// sourceNote so the reason field in the Decision reflects the adjustment.
// Returns the (possibly adjusted) probability and updated sourceNote.
func applyDeadHeat(
	ourP float64,
	ff *collectors.FusedForecast,
	m markets.Market,
	sourceNote string,
) (float64, string) {
	if !IsNearBoundary(ff, m) {
		return ourP, sourceNote
	}

	sigma := ff.EnsembleUncertainty
	if sigma <= 0 {
		sigma = defaultDeadHeatSigma
	}
	distance := math.Abs(ff.Forecast.MaxTempC - m.ThresholdC)
	adjusted := DeadHeatAdjust(ourP, distance, sigma)

	sourceNote += fmt.Sprintf(
		" deadheat(temp=%.1f°C thresh=%.1f°C dist=%.1f σ=%.1f: %.2f→%.2f)",
		ff.Forecast.MaxTempC, m.ThresholdC, distance, sigma, ourP, adjusted,
	)
	return adjusted, sourceNote
}
