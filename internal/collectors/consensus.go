// consensus.go — TASK-130: source consensus spread indicator.
//
// Measures the level of agreement across weather data sources for each
// forecast dimension (temperature, precipitation probability, wind speed).
// When sources disagree significantly, the fused forecast is less reliable,
// so we scale down confidence proportionally.
//
// Formula:
//
//	ConsensusScore(values) = 1 - clamp(stddev(values) / range(values), 0, 1)
//
// The final multi-dimensional score is a weighted average across dimensions:
//
//	MultiDimConsensus = 0.5*tempConsensus + 0.3*precipConsensus + 0.2*windConsensus
//
// The square-root dampening applied to confidence prevents over-penalisation
// when sources disagree moderately (e.g., high-uncertainty days with legitimate
// model spread should not be blocked outright).
package collectors

import (
	"math"
)

// ConsensusScore computes a 0-1 agreement score for a slice of values from
// different data sources.
//
//   - 1.0 → all sources return identical values (perfect consensus)
//   - 0.0 → maximum possible spread (stddev == range)
//   - Returns 1.0 when len(values) <= 1 (single source, nothing to disagree with)
func ConsensusScore(values []float64) float64 {
	if len(values) <= 1 {
		return 1.0
	}

	minV, maxV := values[0], values[0]
	for _, v := range values[1:] {
		if v < minV {
			minV = v
		}
		if v > maxV {
			maxV = v
		}
	}

	valueRange := maxV - minV
	if valueRange < 1e-9 {
		// All values are identical — perfect consensus.
		return 1.0
	}

	sd := stddev(values)
	// Normalise stddev by range to get a scale-independent spread measure.
	normalised := sd / valueRange
	return math.Max(0, 1-math.Min(1, normalised))
}

// MultiDimConsensus computes a weighted average consensus score across the
// three primary forecast dimensions.
//
// Weights: temperature 0.50, precipitation probability 0.30, wind speed 0.20.
// Each dimension is normalised to [0,1] first via ConsensusScore, so different
// physical scales (°C vs km/h vs %) are treated fairly.
//
// Returns 1.0 when all slices are empty or single-element.
func MultiDimConsensus(temps, precips, winds []float64) float64 {
	tc := ConsensusScore(temps)
	pc := ConsensusScore(precips)
	wc := ConsensusScore(winds)

	return 0.50*tc + 0.30*pc + 0.20*wc
}
