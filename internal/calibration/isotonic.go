// isotonic.go — TASK-181: Isotonic regression probability calibration.
//
// Isotonic regression fits a non-parametric, monotonically non-decreasing
// mapping from raw model probabilities to empirical win rates.  Unlike Platt
// scaling it makes no assumption about the functional form of the
// miscalibration curve, which makes it more flexible when the bias is not
// sigmoid-shaped.
//
// Algorithm: Pool Adjacent Violators (PAV), O(n log n).
//
// Usage:
//
//	iso := FitIsotonic(records)
//	calibrated := iso.Predict(rawP)
//
// When fewer than MinCalibrationSamples resolved records are available,
// FitIsotonic returns an identity calibrator (Predict returns the input
// unchanged).
package calibration

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"sort"
)

const isotonicFile = "data/isotonic_calibrator.json"

// isotonicPoint is a (x, y) training pair used by PAV.
type isotonicPoint struct {
	x float64 // raw probability
	y float64 // outcome (0 or 1)
	w float64 // weight (number of merged points)
}

// IsotonicCalibrator holds fitted piecewise-linear breakpoints.
// xs and ys are sorted by xs[i] ascending.
type IsotonicCalibrator struct {
	Xs []float64 `json:"xs"` // raw probability breakpoints
	Ys []float64 `json:"ys"` // calibrated probability at each breakpoint
	N  int       `json:"n"`  // training sample count
}

// NewIsotonicCalibrator returns an identity calibrator with no breakpoints.
func NewIsotonicCalibrator() *IsotonicCalibrator {
	return &IsotonicCalibrator{}
}

// IsActive reports whether the calibrator was fitted on enough data.
func (ic *IsotonicCalibrator) IsActive() bool {
	return ic.N >= MinCalibrationSamples && len(ic.Xs) >= 2
}

// Predict applies the fitted piecewise-linear mapping to rawP.
// Returns rawP unchanged if the calibrator is not active.
// Output is clamped to [0.02, 0.98].
func (ic *IsotonicCalibrator) Predict(rawP float64) float64 {
	if !ic.IsActive() {
		return rawP
	}
	rawP = math.Max(0, math.Min(1, rawP))

	// Extrapolate below the leftmost breakpoint.
	if rawP <= ic.Xs[0] {
		return math.Max(0.02, math.Min(0.98, ic.Ys[0]))
	}
	// Extrapolate above the rightmost breakpoint.
	last := len(ic.Xs) - 1
	if rawP >= ic.Xs[last] {
		return math.Max(0.02, math.Min(0.98, ic.Ys[last]))
	}

	// Binary search for the bracketing interval [xs[i], xs[i+1]].
	idx := sort.SearchFloat64s(ic.Xs, rawP)
	if idx == 0 {
		idx = 1
	}
	x0, x1 := ic.Xs[idx-1], ic.Xs[idx]
	y0, y1 := ic.Ys[idx-1], ic.Ys[idx]

	// Linear interpolation within segment.
	t := (rawP - x0) / (x1 - x0)
	out := y0 + t*(y1-y0)
	return math.Max(0.02, math.Min(0.98, out))
}

// FitIsotonic trains an IsotonicCalibrator from resolved bet records using the
// Pool Adjacent Violators algorithm.
//
// Returns an identity calibrator when fewer than MinCalibrationSamples
// resolved records are present.
func FitIsotonic(records []BetRecord) *IsotonicCalibrator {
	ic := NewIsotonicCalibrator()

	// Collect (rawP, outcome) pairs from resolved records.
	var pts []isotonicPoint
	for _, r := range records {
		if r.Outcome == nil {
			continue
		}
		y := 0.0
		if *r.Outcome {
			y = 1.0
		}
		pts = append(pts, isotonicPoint{x: r.OurProbability, y: y, w: 1})
	}

	ic.N = len(pts)
	if ic.N < MinCalibrationSamples {
		return ic
	}

	// Sort by raw probability ascending.
	sort.Slice(pts, func(i, j int) bool { return pts[i].x < pts[j].x })

	// Pool Adjacent Violators: enforce non-decreasing y values.
	// Merge adjacent blocks whose weighted means violate monotonicity.
	blocks := make([]isotonicPoint, 0, len(pts))
	for _, p := range pts {
		// Start a new block for this point.
		b := isotonicPoint{x: p.x * p.w, y: p.y * p.w, w: p.w}
		for len(blocks) > 0 {
			prev := blocks[len(blocks)-1]
			prevMeanY := prev.y / prev.w
			curMeanY := b.y / b.w
			if prevMeanY <= curMeanY {
				break // monotonicity satisfied
			}
			// Merge current block into previous (pool).
			b.x += prev.x
			b.y += prev.y
			b.w += prev.w
			blocks = blocks[:len(blocks)-1]
		}
		blocks = append(blocks, b)
	}

	// Convert blocks to breakpoints: use weighted-mean x and mean y per block.
	ic.Xs = make([]float64, len(blocks))
	ic.Ys = make([]float64, len(blocks))
	for i, b := range blocks {
		ic.Xs[i] = b.x / b.w // weighted mean of raw probabilities in block
		ic.Ys[i] = b.y / b.w // mean outcome (= empirical win rate) in block
	}

	return ic
}

// isotonicPath resolves the JSON file path relative to dataRoot.
func isotonicPath(dataRoot string) string {
	if dataRoot == "" || dataRoot == "." {
		return isotonicFile
	}
	return filepath.Join(dataRoot, isotonicFile)
}

// SaveIsotonic persists the calibrator to disk as JSON.
func (ic *IsotonicCalibrator) SaveIsotonic(dataRoot string) error {
	path := isotonicPath(dataRoot)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.Marshal(ic)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

// LoadIsotonic reads the calibrator from disk.
// Returns a fresh identity calibrator when the file does not exist.
func LoadIsotonic(dataRoot string) *IsotonicCalibrator {
	ic := NewIsotonicCalibrator()
	data, err := os.ReadFile(isotonicPath(dataRoot))
	if err != nil {
		return ic
	}
	if err := json.Unmarshal(data, ic); err != nil {
		return NewIsotonicCalibrator()
	}
	return ic
}

// UpdateAndSaveIsotonic re-fits the isotonic calibrator from the full resolved
// history and saves to disk.
func UpdateAndSaveIsotonic(dataRoot string, records []BetRecord) (*IsotonicCalibrator, error) {
	ic := FitIsotonic(records)
	return ic, ic.SaveIsotonic(dataRoot)
}
