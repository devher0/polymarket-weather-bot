package aggregation

import (
	"math"
	"strings"
	"testing"
)

func TestAccuracyStatsEmpty(t *testing.T) {
	var s AccuracyStats
	if !math.IsNaN(s.BrierScore()) {
		t.Errorf("empty stats: BrierScore should be NaN, got %g", s.BrierScore())
	}
	if s.Quality() != "unknown" {
		t.Errorf("empty stats: Quality should be 'unknown', got %q", s.Quality())
	}
}

func TestAccuracyStatsBrierScore(t *testing.T) {
	s := AccuracyStats{N: 4, SumSquaredError: 0.20}
	got := s.BrierScore()
	want := 0.05
	if math.Abs(got-want) > 1e-9 {
		t.Errorf("BrierScore() = %g, want %g", got, want)
	}
	if s.Quality() != "excellent" {
		t.Errorf("Quality for BS=0.05 should be 'excellent', got %q", s.Quality())
	}
}

func TestRegistryRecord(t *testing.T) {
	r := NewSourceAccuracyRegistry()
	// Perfect predictions.
	r.Record("openmeteo", "new_york", "rain", 1.0, 1.0)
	r.Record("openmeteo", "new_york", "rain", 0.0, 0.0)

	s := r.Stats("openmeteo", "new_york", "rain")
	if s.N != 2 {
		t.Errorf("N = %d, want 2", s.N)
	}
	if s.BrierScore() != 0.0 {
		t.Errorf("Brier perfect = %g, want 0.0", s.BrierScore())
	}
}

func TestRegistryStatsCaseInsensitive(t *testing.T) {
	r := NewSourceAccuracyRegistry()
	r.Record("OpenMeteo", "New_York", "Rain", 0.7, 1.0)
	s := r.Stats("openmeteo", "new_york", "rain")
	if s.N != 1 {
		t.Errorf("case normalisation failed: N = %d, want 1", s.N)
	}
}

func TestRegistryStatsUnknownKey(t *testing.T) {
	r := NewSourceAccuracyRegistry()
	s := r.Stats("nonexistent", "nowhere", "nothing")
	if s.N != 0 {
		t.Errorf("unknown key: N = %d, want 0", s.N)
	}
}

func TestDomainBaseline_NOAAUSHeat(t *testing.T) {
	w := domainBaseline("noaa", "miami", "heat")
	if w != 0.40 {
		t.Errorf("NOAA/US/heat baseline = %g, want 0.40", w)
	}
}

func TestDomainBaseline_ECMWFEuropeRain(t *testing.T) {
	w := domainBaseline("ecmwf", "london", "rain")
	if w != 0.45 {
		t.Errorf("ECMWF/Europe/rain baseline = %g, want 0.45", w)
	}
}

func TestDomainBaseline_OpenMeteo(t *testing.T) {
	w := domainBaseline("openmeteo", "tokyo", "rain")
	if w != 0.30 {
		t.Errorf("OpenMeteo baseline = %g, want 0.30", w)
	}
}

func TestWeightNoData(t *testing.T) {
	r := NewSourceAccuracyRegistry()
	// No data → returns domain baseline.
	w := r.Weight("ecmwf", "london", "rain")
	if w != 0.45 {
		t.Errorf("weight with no data = %g, want domain baseline 0.45", w)
	}
}

func TestWeightWithData(t *testing.T) {
	r := NewSourceAccuracyRegistry()
	// Record 20 observations: 10 perfect (error=0) and 10 worst-case (error=1).
	// Mean Brier = 0.50 → clamped to 0.25 → weight ≈ baseline × 0.0
	for i := 0; i < 20; i++ {
		if i%2 == 0 {
			r.Record("openmeteo", "paris", "rain", 1.0, 1.0) // error = 0
		} else {
			r.Record("openmeteo", "paris", "rain", 0.0, 1.0) // error = 1
		}
	}
	w := r.Weight("openmeteo", "paris", "rain")
	// With mean Brier ≈ 0.50 (clamped to 0.25), weight should be near 0.01.
	if w > 0.05 {
		t.Errorf("high-Brier weight = %g, want <= 0.05", w)
	}
}

func TestWeightExcellentSource(t *testing.T) {
	r := NewSourceAccuracyRegistry()
	// Record 20 near-perfect observations.
	for i := 0; i < 20; i++ {
		r.Record("ecmwf", "london", "rain", 0.95, 1.0) // error = 0.0025 each
	}
	w := r.Weight("ecmwf", "london", "rain")
	// Excellent Brier → weight close to 0.45 (barely reduced).
	if w < 0.40 {
		t.Errorf("excellent source weight = %g, want >= 0.40", w)
	}
}

func TestWeightToNoise(t *testing.T) {
	tests := []struct {
		weight  float64
		wantMin float64
		wantMax float64
	}{
		{0.45, 0.05, 0.15}, // very trusted → low noise
		{0.30, 0.10, 0.20}, // moderate trust
		{0.20, 0.18, 0.25}, // lower trust → higher noise
		{0.00, 0.29, 0.31}, // zero weight → max noise
	}
	for _, tc := range tests {
		n := weightToNoise(tc.weight)
		if n < tc.wantMin || n > tc.wantMax {
			t.Errorf("weightToNoise(%g) = %g, want [%g, %g]", tc.weight, n, tc.wantMin, tc.wantMax)
		}
	}
}

func TestWeightedBeliefs(t *testing.T) {
	r := NewSourceAccuracyRegistry()
	preds := map[string]float64{
		"ecmwf":     0.80,
		"openmeteo": 0.75,
		"noaa":      0.70,
	}
	beliefs := r.WeightedBeliefs("london", "rain", preds)
	if len(beliefs) != 3 {
		t.Fatalf("WeightedBeliefs: got %d beliefs, want 3", len(beliefs))
	}
	// Sorted by source name: ecmwf < noaa < openmeteo.
	if beliefs[0].Source != "ecmwf" {
		t.Errorf("beliefs[0].Source = %q, want 'ecmwf'", beliefs[0].Source)
	}
	// All probabilities should be preserved.
	for _, b := range beliefs {
		if b.P < 0.60 || b.P > 0.90 {
			t.Errorf("belief %q: P = %g out of expected range", b.Source, b.P)
		}
		if b.Noise <= 0 {
			t.Errorf("belief %q: Noise = %g, want > 0", b.Source, b.Noise)
		}
	}
}

func TestSummary(t *testing.T) {
	r := NewSourceAccuracyRegistry()
	r.Record("openmeteo", "new_york", "rain", 0.8, 1.0)
	r.Record("openmeteo", "new_york", "rain", 0.7, 1.0)
	summary := r.Summary()
	if !strings.Contains(summary, "openmeteo") {
		t.Errorf("Summary missing 'openmeteo': %s", summary)
	}
	if !strings.Contains(summary, "new_york") {
		t.Errorf("Summary missing 'new_york': %s", summary)
	}
}

func TestPrometheusLines(t *testing.T) {
	r := NewSourceAccuracyRegistry()
	r.Record("noaa", "miami", "heat", 0.9, 1.0)
	r.Record("noaa", "miami", "heat", 0.8, 1.0)
	lines := r.PrometheusLines()
	if !strings.Contains(lines, "source_brier_score") {
		t.Errorf("PrometheusLines missing source_brier_score metric")
	}
	if !strings.Contains(lines, "source_observation_count") {
		t.Errorf("PrometheusLines missing source_observation_count metric")
	}
	if !strings.Contains(lines, "source_weight") {
		t.Errorf("PrometheusLines missing source_weight metric")
	}
	if !strings.Contains(lines, `"noaa"`) {
		t.Errorf("PrometheusLines missing noaa label")
	}
	if !strings.Contains(lines, `"miami"`) {
		t.Errorf("PrometheusLines missing miami label")
	}
}
