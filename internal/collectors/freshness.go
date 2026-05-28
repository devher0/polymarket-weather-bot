// freshness.go — TASK-155: helpers to check whether a FusedForecast is too old
// to use for betting decisions.
//
// ForecastAge and IsForecastStale are thin wrappers around FusedForecast.FetchedAt.
// They exist as named functions so callers don't sprinkle time.Since arithmetic
// throughout main.go, and so they can be tested in isolation.
package collectors

import "time"

// ForecastAge returns how long ago the forecast was assembled.
// Returns 0 when FetchedAt is the zero value (forecast has no timestamp).
func ForecastAge(ff *FusedForecast) time.Duration {
	if ff == nil || ff.FetchedAt.IsZero() {
		return 0
	}
	return time.Since(ff.FetchedAt)
}

// IsForecastStale reports whether the forecast is older than maxAgeHours.
// Returns false when maxAgeHours ≤ 0 (staleness checking disabled) or when
// FetchedAt is the zero value.
func IsForecastStale(ff *FusedForecast, maxAgeHours float64) bool {
	if maxAgeHours <= 0 || ff == nil || ff.FetchedAt.IsZero() {
		return false
	}
	return time.Since(ff.FetchedAt) > time.Duration(maxAgeHours*float64(time.Hour))
}
