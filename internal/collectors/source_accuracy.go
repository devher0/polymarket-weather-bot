// source_accuracy.go — per-source accuracy tracker (TASK-032).
//
// After a market resolves, we compare each source's probability prediction
// against the actual outcome to compute a running Brier score contribution
// per source. These running statistics drive DynamicWeights(), which replaces
// the static source weights in the aggregator.
//
// Data layout on disk:
//
//	data/source_accuracy.json             — cumulative AccuracyStats per source
//	data/source_predictions/{cid}.json    — per-source probabilities recorded at bet time
//
// Minimum weight floor: 0.05 — a source that performs poorly still contributes
// a little, preventing permanent exclusion when data is sparse (< 10 bets).
package collectors

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"sync"
)

// AccuracyStats accumulates Brier score contributions for a single source.
type AccuracyStats struct {
	Count    int     `json:"count"`
	BrierSum float64 `json:"brier_sum"`
}

// BrierScore returns the mean Brier score for this source (lower = better).
// Returns 0.25 (random baseline) when count is 0.
func (a AccuracyStats) BrierScore() float64 {
	if a.Count == 0 {
		return 0.25 // no data — assume random baseline
	}
	return a.BrierSum / float64(a.Count)
}

// sourcePredictions holds per-source probability estimates for a single bet.
// Saved at bet placement so they can be scored against the actual outcome later.
type sourcePredictions struct {
	Probs map[string]float64 `json:"probs"` // source → P(signal is YES), [0,1]
}

const (
	accuracyFileName    = "data/source_accuracy.json"
	predictionsDir      = "data/source_predictions"
	minSourceWeight     = 0.05 // floor to prevent total exclusion
	minDataForDynamic   = 10   // minimum resolved bets before adjusting weights
)

var accuracyMu sync.Mutex

// ---- Persistence helpers ------------------------------------------------

func accuracyPath(dataRoot string) string {
	return filepath.Join(dataRoot, accuracyFileName)
}

func predictionPath(dataRoot, conditionID string) string {
	return filepath.Join(dataRoot, predictionsDir, conditionID+".json")
}

// LoadSourceAccuracy reads cumulative stats from disk.
// Returns default stats (empty map) if the file does not yet exist.
func LoadSourceAccuracy(dataRoot string) map[string]AccuracyStats {
	accuracyMu.Lock()
	defer accuracyMu.Unlock()
	return loadSourceAccuracyLocked(dataRoot)
}

func loadSourceAccuracyLocked(dataRoot string) map[string]AccuracyStats {
	path := accuracyPath(dataRoot)
	data, err := os.ReadFile(path)
	if err != nil {
		return make(map[string]AccuracyStats)
	}
	var out map[string]AccuracyStats
	if err := json.Unmarshal(data, &out); err != nil {
		return make(map[string]AccuracyStats)
	}
	return out
}

// SaveSourceAccuracy writes the accuracy map to disk atomically.
func SaveSourceAccuracy(accuracy map[string]AccuracyStats, dataRoot string) error {
	accuracyMu.Lock()
	defer accuracyMu.Unlock()
	return saveSourceAccuracyLocked(accuracy, dataRoot)
}

func saveSourceAccuracyLocked(accuracy map[string]AccuracyStats, dataRoot string) error {
	path := accuracyPath(dataRoot)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("source_accuracy: mkdir: %w", err)
	}
	b, err := json.MarshalIndent(accuracy, "", "  ")
	if err != nil {
		return fmt.Errorf("source_accuracy: marshal: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return fmt.Errorf("source_accuracy: write tmp: %w", err)
	}
	return os.Rename(tmp, path)
}

// ---- Prediction recording -----------------------------------------------

// RecordSourcePredictions saves per-source probability estimates for a bet.
// probs maps source name (e.g. "openmeteo") → probability in [0, 1].
// Called from strategy.EvaluateFused after deciding to place a bet.
func RecordSourcePredictions(conditionID string, probs map[string]float64, dataRoot string) error {
	if len(probs) == 0 {
		return nil
	}
	dir := filepath.Join(dataRoot, predictionsDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("source_accuracy: mkdir predictions: %w", err)
	}
	sp := sourcePredictions{Probs: probs}
	b, err := json.MarshalIndent(sp, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(predictionPath(dataRoot, conditionID), b, 0o644)
}

// LoadSourcePredictions reads previously recorded per-source probs for a bet.
// Returns nil (no error) when the file is missing — predictions were not recorded.
func LoadSourcePredictions(conditionID, dataRoot string) (map[string]float64, error) {
	data, err := os.ReadFile(predictionPath(dataRoot, conditionID))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var sp sourcePredictions
	if err := json.Unmarshal(data, &sp); err != nil {
		return nil, err
	}
	return sp.Probs, nil
}

// ---- Accuracy update on resolve -----------------------------------------

// UpdateSourceAccuracyOnResolve reads the per-source predictions for a
// resolved market, computes the Brier contribution for each source, and
// updates source_accuracy.json.
//
// outcome: true = YES won, false = NO won.
func UpdateSourceAccuracyOnResolve(conditionID string, outcome bool, dataRoot string) error {
	probs, err := LoadSourcePredictions(conditionID, dataRoot)
	if err != nil {
		return fmt.Errorf("source_accuracy: load predictions for %s: %w", conditionID, err)
	}
	if len(probs) == 0 {
		// No predictions were recorded for this bet (pre-TASK-032 bets).
		return nil
	}

	o := 0.0
	if outcome {
		o = 1.0
	}

	accuracyMu.Lock()
	defer accuracyMu.Unlock()

	accuracy := loadSourceAccuracyLocked(dataRoot)

	for source, p := range probs {
		diff := p - o
		brier := diff * diff
		st := accuracy[source]
		st.Count++
		st.BrierSum += brier
		accuracy[source] = st
	}

	if err := saveSourceAccuracyLocked(accuracy, dataRoot); err != nil {
		return err
	}

	// Log the current state so operators can see accuracy evolving.
	parts := make([]string, 0, len(accuracy))
	for src, st := range accuracy {
		parts = append(parts, fmt.Sprintf("%s=%.4f(n=%d)", src, st.BrierScore(), st.Count))
	}
	slog.Info("source accuracy updated", "conditionID", conditionID,
		"outcome", outcome, "sources", fmt.Sprintf("[%s]", joinStrings(parts, ", ")))

	// Clean up prediction sidecar (no longer needed).
	_ = os.Remove(predictionPath(dataRoot, conditionID))
	return nil
}

// ---- Dynamic weights ----------------------------------------------------

// DynamicWeights converts AccuracyStats into normalised weights for the
// aggregator, replacing the static source weights when enough data exists.
//
// Algorithm:
//  1. Convert each source's Brier score to "skill" = 1 / brier (lower brier = higher skill).
//  2. Sources with fewer than minDataForDynamic resolved bets keep their static baseline.
//  3. Normalise so all weights sum to 1.0.
//  4. Clamp each weight to [minSourceWeight, 1 - (numSources-1)*minSourceWeight].
//
// Always returns all four keys (openmeteo, nasa, noaa, goes).
func DynamicWeights(accuracy map[string]AccuracyStats) map[string]float64 {
	static := map[string]float64{
		"openmeteo": 0.35,
		"nasa":      0.30,
		"noaa":      0.25,
		"goes":      0.10,
	}

	if len(accuracy) == 0 {
		return static
	}

	skill := make(map[string]float64, len(static))
	anyDynamic := false

	for src, baseW := range static {
		st, ok := accuracy[src]
		if !ok || st.Count < minDataForDynamic {
			// Not enough data — use static baseline expressed as "skill" units.
			// 1/0.25 = 4 is the random-baseline skill; we scale static weight
			// to the same units so mixing static and dynamic sources is coherent.
			skill[src] = baseW / 0.25 // normalise relative to random baseline
		} else {
			bs := st.BrierScore()
			if bs <= 0 {
				bs = 0.001 // perfect score — assign very high skill
			}
			skill[src] = 1.0 / bs
			anyDynamic = true
		}
	}

	if !anyDynamic {
		return static // all sources below threshold — keep static
	}

	// Normalise skill scores → weights.
	total := 0.0
	for _, s := range skill {
		total += s
	}
	if total == 0 {
		return static
	}

	numSources := float64(len(static))
	maxW := 1.0 - (numSources-1)*minSourceWeight

	weights := make(map[string]float64, len(static))
	for src, s := range skill {
		w := s / total
		w = math.Max(minSourceWeight, math.Min(maxW, w))
		weights[src] = w
	}

	// Re-normalise after clamping.
	sum := 0.0
	for _, w := range weights {
		sum += w
	}
	if sum > 0 {
		for src := range weights {
			weights[src] /= sum
		}
	}

	return weights
}

// LogDynamicWeights emits an info-level log line with current dynamic weights.
func LogDynamicWeights(weights map[string]float64) {
	parts := make([]string, 0, len(weights))
	for src, w := range weights {
		parts = append(parts, fmt.Sprintf("%s=%.2f", src, w))
	}
	slog.Info("dynamic weights", "weights", joinStrings(parts, " "))
}

// joinStrings joins a slice of strings with a separator.
func joinStrings(parts []string, sep string) string {
	result := ""
	for i, p := range parts {
		if i > 0 {
			result += sep
		}
		result += p
	}
	return result
}
