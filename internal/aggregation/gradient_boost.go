// gradient_boost.go — lightweight gradient-boosted decision trees in pure Go.
//
// TASK-101: XGBoost-style calibration without external dependencies.
//
// Features (index → name):
//
//	0: openmeteo_p      — OpenMeteo rain probability (0–1)
//	1: nasa_p           — NASA precipitation probability (0–1)
//	2: noaa_p           — NOAA probability (0–1)
//	3: goes_cloud       — GOES cloud-cover fraction (0–1)
//	4: cape             — CAPE (J/kg), normalised ÷ 3000
//	5: pressure_trend   — 3h pressure change (hPa), normalised ÷ 10
//	6: month            — calendar month (1–12), normalised ÷ 12
//	7: city_id          — integer city ID, normalised ÷ 50
//
// Training data comes from data/bets_history.csv (resolved records only).
// Model is serialised to data/model.json and reloaded on startup.
// Re-training is triggered automatically after 7 days or 50+ new resolved bets.
package aggregation

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// FeatureVec holds the eight features used by the gradient-boost model.
type FeatureVec struct {
	OpenMeteoP    float64 // source: open-meteo precipitation probability
	NASAP         float64 // source: NASA precipitation probability
	NOAAP         float64 // source: NOAA precipitation probability
	GOESCloud     float64 // GOES satellite cloud fraction
	CAPE          float64 // convective available potential energy (J/kg)
	PressureTrend float64 // 3-hour sea-level pressure change (hPa)
	Month         float64 // calendar month 1–12
	CityID        float64 // numeric city identifier
}

// toSlice converts a FeatureVec to a normalised float64 slice.
func (f FeatureVec) toSlice() []float64 {
	cape := f.CAPE / 3000.0
	if cape > 1 {
		cape = 1
	}
	pt := f.PressureTrend / 10.0
	return []float64{
		f.OpenMeteoP,
		f.NASAP,
		f.NOAAP,
		f.GOESCloud,
		cape,
		pt,
		f.Month / 12.0,
		f.CityID / 50.0,
	}
}

// TrainingSample is one resolved bet used for training.
type TrainingSample struct {
	Features FeatureVec
	Outcome  float64 // 1.0 = YES resolved, 0.0 = NO resolved
}

// splitNode is an internal decision-tree split.
type splitNode struct {
	FeatureIdx int     // which feature to split on
	Threshold  float64 // split value
	Left       float64 // prediction for feature < threshold
	Right      float64 // prediction for feature >= threshold
	LeafOnly   bool    // true for stumps/leaves with no further splits
}

// tree is a depth-2 decision tree (stump with one level of children).
type tree struct {
	Root splitNode
	LL   float64 // left-left leaf value
	LR   float64 // left-right leaf value
	RL   float64 // right-left leaf value
	RR   float64 // right-right leaf value

	// For depth-1 stumps (when data is sparse).
	Depth1 bool
}

// predict returns the tree's raw (log-odds additive) output for a sample.
func (t *tree) predict(x []float64) float64 {
	if t.Depth1 {
		if x[t.Root.FeatureIdx] < t.Root.Threshold {
			return t.Root.Left
		}
		return t.Root.Right
	}
	if x[t.Root.FeatureIdx] < t.Root.Threshold {
		// left subtree
		split := t.Root.Left // feature index encoded in Left as int part? No — use separate fields.
		_ = split
		// Use the LL/LR split (same feature for simplicity — depth-2 CART).
		if x[t.Root.FeatureIdx] < t.Root.Threshold-0.25 {
			return t.LL
		}
		return t.LR
	}
	// right subtree
	if x[t.Root.FeatureIdx] < t.Root.Threshold+0.25 {
		return t.RL
	}
	return t.RR
}

// GBModel is a gradient-boosted ensemble of decision trees.
type GBModel struct {
	Trees        []tree    `json:"trees"`
	BasePred     float64   `json:"base_pred"`   // initial log-odds prediction
	LearningRate float64   `json:"learning_rate"`
	TrainedAt    time.Time `json:"trained_at"`
	NSamples     int       `json:"n_samples"`
}

// Predict returns the calibrated probability (0–1) for the given features.
func (m *GBModel) Predict(f FeatureVec) float64 {
	x := f.toSlice()
	score := m.BasePred
	for i := range m.Trees {
		score += m.LearningRate * m.Trees[i].predict(x)
	}
	return sigmoid(score)
}

// sigmoid maps log-odds to probability.
func sigmoid(x float64) float64 {
	return 1.0 / (1.0 + math.Exp(-x))
}

// logit is the inverse of sigmoid.
func logit(p float64) float64 {
	p = math.Max(1e-7, math.Min(1-1e-7, p))
	return math.Log(p / (1 - p))
}

// Train fits a new GBModel on the provided samples using gradient boosting
// with binary cross-entropy loss. numTrees controls the number of boosting
// rounds (50–100 recommended).
func Train(samples []TrainingSample, numTrees int, learningRate float64) *GBModel {
	if len(samples) == 0 {
		return &GBModel{BasePred: 0, LearningRate: learningRate, TrainedAt: time.Now()}
	}
	if numTrees <= 0 {
		numTrees = 75
	}
	if learningRate <= 0 {
		learningRate = 0.10
	}

	n := len(samples)
	// Compute base prediction: log-odds of the mean outcome.
	sumY := 0.0
	for _, s := range samples {
		sumY += s.Outcome
	}
	meanY := sumY / float64(n)
	basePred := logit(meanY)

	// F[i] = current ensemble prediction (in log-odds space).
	F := make([]float64, n)
	for i := range F {
		F[i] = basePred
	}

	xs := make([][]float64, n)
	ys := make([]float64, n)
	for i, s := range samples {
		xs[i] = s.Features.toSlice()
		ys[i] = s.Outcome
	}

	trees := make([]tree, 0, numTrees)

	for round := 0; round < numTrees; round++ {
		// Compute negative gradient (pseudo-residuals) for log-loss.
		// residual_i = y_i - sigmoid(F_i)
		residuals := make([]float64, n)
		for i := range residuals {
			residuals[i] = ys[i] - sigmoid(F[i])
		}

		t := fitStump(xs, residuals)
		trees = append(trees, t)

		// Update F.
		for i := range F {
			F[i] += learningRate * t.predict(xs[i])
		}
	}

	return &GBModel{
		Trees:        trees,
		BasePred:     basePred,
		LearningRate: learningRate,
		TrainedAt:    time.Now(),
		NSamples:     n,
	}
}

// fitStump builds a depth-1 decision stump that best splits the residuals.
// It scans all 8 features and all unique thresholds to find the split that
// minimises the squared-residual loss (like XGBoost's exact greedy algorithm).
func fitStump(xs [][]float64, residuals []float64) tree {
	bestGain := math.Inf(-1)
	bestFeat := 0
	bestThresh := 0.5
	bestLeft := 0.0
	bestRight := 0.0

	nFeatures := len(xs[0])
	n := len(xs)

	for f := 0; f < nFeatures; f++ {
		// Collect unique thresholds (midpoints between sorted unique values).
		vals := make([]float64, n)
		for i, x := range xs {
			vals[i] = x[f]
		}
		thresholds := midpoints(vals)

		for _, thresh := range thresholds {
			var leftRes, rightRes []float64
			for i, x := range xs {
				if x[f] < thresh {
					leftRes = append(leftRes, residuals[i])
				} else {
					rightRes = append(rightRes, residuals[i])
				}
			}
			if len(leftRes) == 0 || len(rightRes) == 0 {
				continue
			}
			lMean := mean(leftRes)
			rMean := mean(rightRes)
			// Gain = sum of squared means weighted by partition size.
			gain := float64(len(leftRes))*lMean*lMean + float64(len(rightRes))*rMean*rMean
			if gain > bestGain {
				bestGain = gain
				bestFeat = f
				bestThresh = thresh
				bestLeft = lMean
				bestRight = rMean
			}
		}
	}

	return tree{
		Root: splitNode{
			FeatureIdx: bestFeat,
			Threshold:  bestThresh,
			Left:       bestLeft,
			Right:      bestRight,
		},
		Depth1: true,
	}
}

// midpoints returns candidate split thresholds (midpoints of sorted unique values).
// Capped at 20 thresholds to keep training fast.
func midpoints(vals []float64) []float64 {
	seen := map[float64]bool{}
	for _, v := range vals {
		seen[v] = true
	}
	unique := make([]float64, 0, len(seen))
	for v := range seen {
		unique = append(unique, v)
	}
	// Sort ascending.
	for i := 0; i < len(unique); i++ {
		for j := i + 1; j < len(unique); j++ {
			if unique[j] < unique[i] {
				unique[i], unique[j] = unique[j], unique[i]
			}
		}
	}
	mids := make([]float64, 0, len(unique)-1)
	for i := 0; i < len(unique)-1; i++ {
		mids = append(mids, (unique[i]+unique[i+1])/2)
	}
	// Cap at 20 thresholds evenly spaced.
	if len(mids) > 20 {
		step := len(mids) / 20
		capped := make([]float64, 0, 20)
		for i := 0; i < len(mids); i += step {
			capped = append(capped, mids[i])
		}
		return capped
	}
	return mids
}

func mean(xs []float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	s := 0.0
	for _, v := range xs {
		s += v
	}
	return s / float64(len(xs))
}

// ─── Persistence ──────────────────────────────────────────────────────────────

const modelFile = "data/model.json"

var (
	modelMu    sync.RWMutex
	cachedModel *GBModel
)

// LoadModel reads the persisted model from dataRoot/data/model.json.
// Returns nil (not an error) when no model file exists yet.
func LoadModel(dataRoot string) (*GBModel, error) {
	path := filepath.Join(dataRoot, modelFile)
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("gradient_boost: read model: %w", err)
	}
	var m GBModel
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("gradient_boost: parse model: %w", err)
	}
	modelMu.Lock()
	cachedModel = &m
	modelMu.Unlock()
	return &m, nil
}

// SaveModel persists the model to dataRoot/data/model.json.
func SaveModel(m *GBModel, dataRoot string) error {
	path := filepath.Join(dataRoot, modelFile)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("gradient_boost: mkdir: %w", err)
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("gradient_boost: marshal: %w", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("gradient_boost: write model: %w", err)
	}
	modelMu.Lock()
	cachedModel = m
	modelMu.Unlock()
	return nil
}

// NeedsRetraining returns true when the model is nil, older than 7 days,
// or was trained on fewer than minSamples resolved bets.
func NeedsRetraining(m *GBModel, resolvedCount int, minSamples int) bool {
	if m == nil {
		return true
	}
	if time.Since(m.TrainedAt) > 7*24*time.Hour {
		return true
	}
	if resolvedCount >= minSamples && resolvedCount > m.NSamples {
		return true
	}
	return false
}

// CachedModel returns the in-memory model (may be nil before first load/train).
func CachedModel() *GBModel {
	modelMu.RLock()
	defer modelMu.RUnlock()
	return cachedModel
}
