// platt.go — TASK-122: Platt scaling probability calibration.
//
// Fits a sigmoid calibration curve to resolved bet history so that a raw
// model probability of 0.70 translates to the empirically-observed win rate
// at that probability level.
//
//	calibrated = σ(A × raw_p + B)   where σ(x) = 1 / (1 + exp(-x))
//
// A and B are fitted by minimising log-loss via gradient descent on resolved bets.
// When fewer than MinCalibrationSamples bets have been resolved, Calibrate
// returns the raw probability unchanged (not enough data to fit reliably).
package calibration

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
)

const (
	// MinCalibrationSamples is the minimum number of resolved bets needed
	// before the calibrator departs from the identity mapping.
	MinCalibrationSamples = 20

	calibratorFile = "data/platt_calibrator.json"

	// SGD hyper-parameters.
	plattLR         = 0.05
	plattIterations = 500
	plattL2         = 1e-4 // L2 regularisation to prevent extreme A/B values
)

// PlattCalibrator holds the fitted sigmoid parameters for probability calibration.
type PlattCalibrator struct {
	A float64 `json:"a"` // slope  (1.0 = identity)
	B float64 `json:"b"` // intercept (0.0 = no shift)
	N int     `json:"n"` // number of training samples used for the last fit
}

// NewPlattCalibrator returns an identity calibrator (no transformation applied).
func NewPlattCalibrator() *PlattCalibrator {
	return &PlattCalibrator{A: 1.0, B: 0.0}
}

// sigmoid computes 1 / (1 + exp(-x)) with numerical clamping.
func sigmoid(x float64) float64 {
	if x > 35 {
		return 1.0
	}
	if x < -35 {
		return 0.0
	}
	return 1.0 / (1.0 + math.Exp(-x))
}

// Calibrate applies the fitted sigmoid to a raw probability value.
// Returns p unchanged when N < MinCalibrationSamples.
func (pc *PlattCalibrator) Calibrate(p float64) float64 {
	if pc.N < MinCalibrationSamples {
		return p
	}
	p = math.Max(1e-9, math.Min(1-1e-9, p))
	return sigmoid(pc.A*p + pc.B)
}

// IsActive reports whether the calibrator has been fitted on enough data.
func (pc *PlattCalibrator) IsActive() bool {
	return pc.N >= MinCalibrationSamples
}

// Fit trains the calibrator parameters A and B using gradient descent on
// binary cross-entropy (log-loss) over the supplied (prediction, outcome) pairs.
//
// predictions[i] ∈ (0, 1) — the raw model probability for the YES side
// outcomes[i]    ∈ {0, 1}  — 1 if the bet won, 0 if it lost
func (pc *PlattCalibrator) Fit(predictions, outcomes []float64) {
	n := len(predictions)
	if n != len(outcomes) || n == 0 {
		return
	}
	pc.N = n

	a, b := pc.A, pc.B

	for iter := 0; iter < plattIterations; iter++ {
		gradA, gradB := 0.0, 0.0
		for i, p := range predictions {
			p = math.Max(1e-9, math.Min(1-1e-9, p))
			y := outcomes[i]
			z := a*p + b
			pred := sigmoid(z)
			// Gradient of log-loss: ∂L/∂z = (pred - y) per sample.
			delta := (pred - y) / float64(n)
			gradA += delta * p
			gradB += delta
		}
		// L2 regularisation gradient.
		gradA += plattL2 * a
		gradB += plattL2 * b

		a -= plattLR * gradA
		b -= plattLR * gradB
	}

	pc.A = a
	pc.B = b
}

// FitFromHistory builds calibration data from resolved bet records and re-fits.
// Returns early without changing calibrator state if fewer than
// MinCalibrationSamples resolved records are available.
func (pc *PlattCalibrator) FitFromHistory(records []BetRecord) {
	var preds, outcomes []float64
	for _, r := range records {
		if r.Outcome == nil {
			continue
		}
		outcome := 0.0
		if *r.Outcome {
			outcome = 1.0
		}
		preds = append(preds, r.OurProbability)
		outcomes = append(outcomes, outcome)
	}
	if len(preds) < MinCalibrationSamples {
		pc.N = len(preds) // store count so IsActive() returns false
		return
	}
	pc.Fit(preds, outcomes)
}

// calibratorPath resolves the JSON file path relative to dataRoot.
func calibratorPath(dataRoot string) string {
	if dataRoot == "" || dataRoot == "." {
		return calibratorFile
	}
	return filepath.Join(dataRoot, calibratorFile)
}

// SaveCalibrator persists the calibrator parameters to disk as JSON.
func (pc *PlattCalibrator) SaveCalibrator(dataRoot string) error {
	path := calibratorPath(dataRoot)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.Marshal(pc)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

// LoadCalibrator reads calibrator parameters from disk.
// Returns a fresh identity calibrator when the file does not exist.
func LoadCalibrator(dataRoot string) *PlattCalibrator {
	pc := NewPlattCalibrator()
	data, err := os.ReadFile(calibratorPath(dataRoot))
	if err != nil {
		return pc
	}
	if err := json.Unmarshal(data, pc); err != nil {
		return NewPlattCalibrator()
	}
	return pc
}

// UpdateAndSave re-fits the calibrator from the full resolved history and
// saves to disk. Call once after each market resolution.
func UpdateAndSave(dataRoot string, records []BetRecord) (*PlattCalibrator, error) {
	pc := LoadCalibrator(dataRoot)
	pc.FitFromHistory(records)
	return pc, pc.SaveCalibrator(dataRoot)
}

// ReliabilityDiagram returns buckets for a reliability (calibration) diagram.
// It groups resolved bets by predicted probability in `nBuckets` equal-width
// bins and returns the mean prediction and actual win rate per bucket.
//
// Returns nil slices when fewer than MinCalibrationSamples records are available.
func ReliabilityDiagram(records []BetRecord, nBuckets int) (meanPred, actualRate []float64) {
	if nBuckets <= 0 {
		nBuckets = 10
	}
	type bucket struct {
		sumPred float64
		wins    int
		total   int
	}
	buckets := make([]bucket, nBuckets)

	resolved := 0
	for _, r := range records {
		if r.Outcome == nil {
			continue
		}
		resolved++
		idx := int(r.OurProbability * float64(nBuckets))
		if idx >= nBuckets {
			idx = nBuckets - 1
		}
		buckets[idx].sumPred += r.OurProbability
		buckets[idx].total++
		if *r.Outcome {
			buckets[idx].wins++
		}
	}
	if resolved < MinCalibrationSamples {
		return nil, nil
	}

	for _, b := range buckets {
		if b.total == 0 {
			continue
		}
		meanPred = append(meanPred, b.sumPred/float64(b.total))
		actualRate = append(actualRate, float64(b.wins)/float64(b.total))
	}
	return meanPred, actualRate
}
