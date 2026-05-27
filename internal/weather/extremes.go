// extremes.go — detects anomalous weather events in forecast data.
package weather

import "fmt"

// Extreme weather thresholds.
const (
	ExtremeHeatC    = 38.0 // MaxTempC above this → heat wave
	ExtremePrecipMM = 50.0 // PrecipitationMM above this → heavy rain
	ExtremeWindKMH  = 90.0 // WindSpeedKMH above this → storm
)

// IsExtreme reports whether f contains an extreme weather condition.
// Returns (true, tag) when extreme, (false, "") otherwise.
// tag is a human-readable label like "extreme: heat_wave (42.3°C)" which
// callers may append to FusedForecast.Sources for traceability.
func IsExtreme(f Forecast) (bool, string) {
	switch {
	case f.MaxTempC > ExtremeHeatC:
		return true, fmt.Sprintf("extreme: heat_wave (%.1f°C)", f.MaxTempC)
	case f.PrecipitationMM > ExtremePrecipMM:
		return true, fmt.Sprintf("extreme: heavy_rain (%.1fmm)", f.PrecipitationMM)
	case f.WindSpeedKMH > ExtremeWindKMH:
		return true, fmt.Sprintf("extreme: storm (%.1fkm/h)", f.WindSpeedKMH)
	}
	return false, ""
}

// ExtremeConfidenceFloor is the minimum confidence value applied when an
// extreme event is detected.  All weather models tend to agree on obvious
// extrema, so we can be more confident even with fewer sources.
const ExtremeConfidenceFloor = 0.75
