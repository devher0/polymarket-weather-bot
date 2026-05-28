// spread_gate.go — TASK-226: raw probability spread gate across data sources.
//
// Complements the existing ConsensusIndex (which uses stddev × direction signal).
// The spread gate asks a simpler question: do any two sources disagree by more
// than MaxSourceSpread percentage points? If so, skip the bet — we don't know
// enough to trade confidently.
//
// Example: openmeteo=0.80, nasa=0.40 → spread=0.40 → gate triggers at 0.35.
package aggregation

import (
	"math"
)

// SourceSpread summarises the dispersion of per-source probability estimates.
type SourceSpread struct {
	Min     float64 // lowest source probability
	Max     float64 // highest source probability
	Spread  float64 // Max - Min (the "raw spread")
	Mean    float64 // simple average
	StdDev  float64 // population standard deviation
	Sources int     // number of sources included
}

// ComputeSpread calculates SourceSpread from a slice of per-source probability
// estimates (each in [0,1]). Returns zero SourceSpread when probs is empty.
func ComputeSpread(probs []float64) SourceSpread {
	n := len(probs)
	if n == 0 {
		return SourceSpread{}
	}

	min := probs[0]
	max := probs[0]
	sum := 0.0
	for _, p := range probs {
		if p < min {
			min = p
		}
		if p > max {
			max = p
		}
		sum += p
	}
	mean := sum / float64(n)

	var variance float64
	for _, p := range probs {
		d := p - mean
		variance += d * d
	}
	variance /= float64(n)

	return SourceSpread{
		Min:     min,
		Max:     max,
		Spread:  max - min,
		Mean:    mean,
		StdDev:  math.Sqrt(variance),
		Sources: n,
	}
}

// Exceeds reports whether the spread surpasses maxSpread.
// Returns false when maxSpread <= 0 (gate disabled) or fewer than 2 sources.
func (s SourceSpread) Exceeds(maxSpread float64) bool {
	return maxSpread > 0 && s.Sources >= 2 && s.Spread > maxSpread
}

// Label returns a human-readable description of the spread magnitude.
//
//	< 0.10  → "tight"
//	0.10–0.20 → "moderate"
//	0.20–0.30 → "wide"
//	≥ 0.30  → "extreme"
func (s SourceSpread) Label() string {
	switch {
	case s.Sources == 0:
		return "n/a"
	case s.Spread < 0.10:
		return "tight"
	case s.Spread < 0.20:
		return "moderate"
	case s.Spread < 0.30:
		return "wide"
	default:
		return "extreme"
	}
}
