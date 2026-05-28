// horizon.go — forecast horizon confidence decay (TASK-134).
//
// 1-day forecasts are significantly more accurate than 5-day forecasts.
// HorizonDecayLinear provides a continuous, linear decay factor that
// reduces confidence as the forecast horizon grows.
package collectors

import (
	"math"
	"time"
)

// HorizonDecayLinear computes a continuous confidence decay factor based on
// the forecast horizon in hours. The factor ranges from 1.0 (same-day) down
// to 0.65 (>120 h out), clipped to [0.65, 1.0].
//
// Formula: max(0.65, 1.0 - horizonHours/400)
func HorizonDecayLinear(horizonHours float64) float64 {
	if horizonHours < 0 {
		horizonHours = 0
	}
	return math.Max(0.65, 1.0-horizonHours/400.0)
}

// HorizonDecay computes the decay factor given the target forecast date and
// the time the forecast was assembled. It converts the gap to hours and
// delegates to HorizonDecayLinear.
//
// Decay table (approximate):
//
//	 0–24 h  → 1.00
//	24–48 h  → ~0.94–1.00
//	48–72 h  → ~0.88–0.94
//	72–96 h  → ~0.82–0.88
//	96–120 h → ~0.76–0.82
//	>120 h   → 0.65 (floor)
func HorizonDecay(targetDate time.Time, forecastedAt time.Time) float64 {
	horizonHours := targetDate.Sub(forecastedAt).Hours()
	return HorizonDecayLinear(horizonHours)
}
