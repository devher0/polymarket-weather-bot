package aggregation

import (
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// makeSamples generates synthetic training samples where outcome == 1 when
// openmeteo_p + nasa_p > 1.0 (i.e. both sources strongly predict rain).
func makeSamples(n int) []TrainingSample {
	samples := make([]TrainingSample, n)
	for i := range samples {
		t := float64(i) / float64(n)
		p := t * 0.9
		outcome := 0.0
		if p > 0.5 {
			outcome = 1.0
		}
		samples[i] = TrainingSample{
			Features: FeatureVec{
				OpenMeteoP:    p,
				NASAP:         p * 0.95,
				NOAAP:         p * 0.90,
				GOESCloud:     p * 0.80,
				CAPE:          p * 1500,
				PressureTrend: -p * 3,
				Month:         6,
				CityID:        1,
			},
			Outcome: outcome,
		}
	}
	return samples
}

func TestTrain_Basic(t *testing.T) {
	samples := makeSamples(100)
	m := Train(samples, 50, 0.1)
	if m == nil {
		t.Fatal("Train returned nil")
	}
	if len(m.Trees) != 50 {
		t.Fatalf("expected 50 trees, got %d", len(m.Trees))
	}
	if m.NSamples != 100 {
		t.Fatalf("expected NSamples=100, got %d", m.NSamples)
	}
}

func TestPredict_MonotoneLowToHigh(t *testing.T) {
	samples := makeSamples(120)
	m := Train(samples, 60, 0.1)

	low := m.Predict(FeatureVec{OpenMeteoP: 0.05, NASAP: 0.05, NOAAP: 0.05,
		GOESCloud: 0.1, Month: 6, CityID: 1})
	high := m.Predict(FeatureVec{OpenMeteoP: 0.90, NASAP: 0.88, NOAAP: 0.85,
		GOESCloud: 0.9, Month: 6, CityID: 1})

	if low >= high {
		t.Fatalf("expected low p (%.4f) < high p (%.4f)", low, high)
	}
}

func TestPredict_InRange(t *testing.T) {
	samples := makeSamples(80)
	m := Train(samples, 40, 0.1)

	for _, fv := range []FeatureVec{
		{OpenMeteoP: 0.3, NASAP: 0.3, NOAAP: 0.3, GOESCloud: 0.4, Month: 3, CityID: 2},
		{OpenMeteoP: 0.7, NASAP: 0.75, NOAAP: 0.65, GOESCloud: 0.8, Month: 8, CityID: 5},
	} {
		p := m.Predict(fv)
		if p < 0 || p > 1 || math.IsNaN(p) {
			t.Fatalf("predict out of range: %v → %.6f", fv, p)
		}
	}
}

func TestSaveLoadModel(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "data"), 0o755); err != nil {
		t.Fatal(err)
	}

	samples := makeSamples(60)
	m := Train(samples, 30, 0.1)

	if err := SaveModel(m, dir); err != nil {
		t.Fatalf("SaveModel: %v", err)
	}

	loaded, err := LoadModel(dir)
	if err != nil {
		t.Fatalf("LoadModel: %v", err)
	}
	if loaded == nil {
		t.Fatal("LoadModel returned nil")
	}
	if len(loaded.Trees) != len(m.Trees) {
		t.Fatalf("tree count mismatch: want %d got %d", len(m.Trees), len(loaded.Trees))
	}

	// Predictions should be identical after round-trip.
	fv := FeatureVec{OpenMeteoP: 0.6, NASAP: 0.55, NOAAP: 0.58, GOESCloud: 0.7, Month: 7, CityID: 3}
	orig := m.Predict(fv)
	relo := loaded.Predict(fv)
	if math.Abs(orig-relo) > 1e-9 {
		t.Fatalf("prediction mismatch after load: %.9f vs %.9f", orig, relo)
	}
}

func TestNeedsRetraining(t *testing.T) {
	if !NeedsRetraining(nil, 0, 50) {
		t.Error("nil model should need retraining")
	}

	old := &GBModel{TrainedAt: time.Now().Add(-8 * 24 * time.Hour), NSamples: 60}
	if !NeedsRetraining(old, 60, 50) {
		t.Error("stale model should need retraining")
	}

	// fresh model with same sample count — no retrain needed
	fresh := &GBModel{TrainedAt: time.Now(), NSamples: 60}
	if NeedsRetraining(fresh, 60, 50) {
		t.Error("fresh model with same resolved count should not need retraining")
	}
}

func TestNeedsRetraining_ManyNewSamples(t *testing.T) {
	fresh := &GBModel{TrainedAt: time.Now(), NSamples: 60}
	// 200 resolved >= 50 (minSamples) and 200 > 60 (m.NSamples) → needs retrain
	if !NeedsRetraining(fresh, 200, 50) {
		t.Error("model should retrain when many new resolved samples available")
	}
}

func TestLoadModel_Missing(t *testing.T) {
	m, err := LoadModel(t.TempDir())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if m != nil {
		t.Fatal("expected nil model when file missing")
	}
}
