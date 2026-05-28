// bankroll_kelly.go — TASK-152: dynamic Kelly scale based on bankroll growth.
//
// When the bankroll grows significantly above its initial value, we can afford
// slightly larger Kelly fractions. When the bankroll is in drawdown, we
// automatically reduce Kelly to protect capital. This complements the existing
// drawdown multiplier (drawdown.go) by adjusting the MaxKellyFraction cap.
//
// Scale table:
//
//	ratio < 0.70            → scale = 0.70 (capital protection)
//	ratio 0.70 – 1.00       → linear interpolation 0.70 → 1.00
//	ratio 1.00 – 2.00       → linear interpolation 1.00 → 1.20 (moderate growth)
//	ratio > 2.00            → scale = 1.20 (hard ceiling, no casino behaviour)
package calibration

import (
	"log/slog"
	"math"
)

const (
	kellyScaleFloor      = 0.70 // floor multiplier at severe drawdown
	kellyScaleCeiling    = 1.20 // ceiling multiplier at 2x bankroll
	kellyScaleMidRatio   = 2.00 // bankroll ratio where ceiling is reached
	kellyScaleNeutral    = 1.00 // multiplier when bankroll == initial
	kellyScaleDrawdown   = 0.70 // bankroll ratio below which floor kicks in
)

// BankrollKellyScale returns a multiplicative factor for MaxKellyFraction
// based on the ratio of current to initial bankroll.
//
//   - scale < 1 means reduce Kelly (drawdown protection)
//   - scale = 1 means no change
//   - scale > 1 means allow slightly larger Kelly (compounding growth)
//
// Returns 1.0 when initial is zero or negative (no scaling reference available).
func BankrollKellyScale(current, initial float64) float64 {
	if initial <= 0 || current <= 0 {
		return 1.0
	}

	ratio := current / initial

	var scale float64
	switch {
	case ratio <= kellyScaleDrawdown:
		scale = kellyScaleFloor
	case ratio <= 1.0:
		// Linear from floor at kellyScaleDrawdown → neutral at 1.0
		t := (ratio - kellyScaleDrawdown) / (1.0 - kellyScaleDrawdown)
		scale = kellyScaleFloor + t*(kellyScaleNeutral-kellyScaleFloor)
	case ratio <= kellyScaleMidRatio:
		// Linear from neutral at 1.0 → ceiling at kellyScaleMidRatio
		t := (ratio - 1.0) / (kellyScaleMidRatio - 1.0)
		scale = kellyScaleNeutral + t*(kellyScaleCeiling-kellyScaleNeutral)
	default:
		scale = kellyScaleCeiling
	}

	// Round to 4 decimal places to avoid float noise in logs.
	scale = math.Round(scale*10000) / 10000

	slog.Debug("bankroll kelly scale",
		"current", math.Round(current*100)/100,
		"initial", math.Round(initial*100)/100,
		"ratio", math.Round(ratio*100)/100,
		"scale", scale,
	)

	return scale
}
