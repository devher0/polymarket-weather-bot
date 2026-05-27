// source_accuracy.go — per-source, per-city, per-signal Brier score tracking.
//
// TASK-103: Historical basis — track how accurate each weather source is
// broken down by city and signal type. Use city+signal-specific weights
// when aggregating forecasts.
//
// Key insight:
//   - NOAA excels for US cities + heat signal   → weight 0.40
//   - ECMWF excels for European cities + rain    → weight 0.45
//   - Generic sources get moderate default weights
//
// Usage:
//
//	reg := NewSourceAccuracyRegistry()
//	reg.Record("openmeteo", "new_york", "rain", predicted, actual)
//	w := reg.Weight("openmeteo", "new_york", "rain")
//	metrics := reg.PrometheusLines()
package aggregation

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"
)

// AccuracyKey uniquely identifies a (source, city, signal) combination.
type AccuracyKey struct {
	Source string
	City   string
	Signal string
}

// AccuracyStats accumulates Brier score statistics for one AccuracyKey.
// Brier score = mean((p - outcome)²), lower is better (0 = perfect, 0.25 = random).
type AccuracyStats struct {
	// N is the number of resolved observations.
	N int
	// SumSquaredError is the running sum of (prediction - outcome)² values.
	SumSquaredError float64
}

// BrierScore returns the mean Brier score (NaN when N==0).
func (a AccuracyStats) BrierScore() float64 {
	if a.N == 0 {
		return math.NaN()
	}
	return a.SumSquaredError / float64(a.N)
}

// Quality returns a human-readable quality label for the Brier score.
func (a AccuracyStats) Quality() string {
	bs := a.BrierScore()
	switch {
	case math.IsNaN(bs):
		return "unknown"
	case bs < 0.10:
		return "excellent"
	case bs < 0.15:
		return "good"
	case bs < 0.20:
		return "moderate"
	default:
		return "poor"
	}
}

// SourceAccuracyRegistry stores Brier score statistics keyed by
// (source, city, signal) tuples. All methods are safe for concurrent use.
type SourceAccuracyRegistry struct {
	mu   sync.RWMutex
	data map[AccuracyKey]*AccuracyStats
}

// NewSourceAccuracyRegistry creates an empty registry.
func NewSourceAccuracyRegistry() *SourceAccuracyRegistry {
	return &SourceAccuracyRegistry{
		data: make(map[AccuracyKey]*AccuracyStats),
	}
}

// Record adds one resolved observation to the registry.
//
//   - predicted: the source's probability estimate at forecast time (0–1)
//   - outcome:   1.0 if the event occurred, 0.0 if it did not
func (r *SourceAccuracyRegistry) Record(source, city, signal string, predicted, outcome float64) {
	key := AccuracyKey{
		Source: strings.ToLower(source),
		City:   strings.ToLower(city),
		Signal: strings.ToLower(signal),
	}
	err := (predicted - outcome) * (predicted - outcome)

	r.mu.Lock()
	s, ok := r.data[key]
	if !ok {
		s = &AccuracyStats{}
		r.data[key] = s
	}
	s.N++
	s.SumSquaredError += err
	r.mu.Unlock()
}

// Stats returns the current accuracy statistics for (source, city, signal).
// Returns zero stats (N=0) when no data has been recorded for the key.
func (r *SourceAccuracyRegistry) Stats(source, city, signal string) AccuracyStats {
	key := AccuracyKey{
		Source: strings.ToLower(source),
		City:   strings.ToLower(city),
		Signal: strings.ToLower(signal),
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	if s, ok := r.data[key]; ok {
		return *s
	}
	return AccuracyStats{}
}

// Weight returns an aggregation weight for a source based on its historical
// Brier score for a given city+signal combination.
//
// Domain rules (encoded domain knowledge):
//   - NOAA + US city + heat → baseline weight 0.40
//   - ECMWF + European city + rain → baseline weight 0.45
//   - OpenMeteo is a solid global default → baseline weight 0.30
//   - NASA is useful globally → baseline weight 0.28
//
// When N >= 10 the empirical Brier score adjusts the baseline:
//
//	weight = baseline × (1 − brierScore/0.25)
//
// Lower Brier score → higher weight. Sources with Brier > 0.25 get near-zero weight.
func (r *SourceAccuracyRegistry) Weight(source, city, signal string) float64 {
	src := strings.ToLower(source)
	cty := strings.ToLower(city)
	sig := strings.ToLower(signal)

	// --- Domain-knowledge baseline weights ---
	baseline := domainBaseline(src, cty, sig)

	// --- Empirical adjustment ---
	stats := r.Stats(src, cty, sig)
	if stats.N < 10 {
		// Not enough data — return the domain baseline.
		return baseline
	}
	bs := stats.BrierScore()
	// Clamp bs to [0, 0.25] to avoid negative or extreme weights.
	bs = math.Max(0, math.Min(0.25, bs))
	adjusted := baseline * (1.0 - bs/0.25)
	if adjusted < 0.01 {
		return 0.01 // minimum non-zero weight
	}
	return adjusted
}

// domainBaseline returns the a-priori weight for a (source, city, signal)
// combination based on known model strengths.
func domainBaseline(source, city, signal string) float64 {
	// NOAA is strongest for US cities and heat/temperature signals.
	if source == "noaa" && isUSCity(city) && (signal == "heat" || signal == "temperature") {
		return 0.40
	}
	// ECMWF is strongest for European cities and rain/precipitation.
	if source == "ecmwf" && isEuropeanCity(city) && (signal == "rain" || signal == "precipitation") {
		return 0.45
	}
	// ECMWF is generally good globally.
	if source == "ecmwf" {
		return 0.35
	}
	// OpenMeteo is a good global default.
	if source == "openmeteo" {
		return 0.30
	}
	// NASA is useful globally for precipitation.
	if source == "nasa" {
		return 0.28
	}
	// NOAA default (for non-heat or non-US).
	if source == "noaa" {
		return 0.25
	}
	// GFS and other NWP models.
	if source == "gfs" {
		return 0.28
	}
	// Unknown source — conservative default.
	return 0.20
}

// isUSCity returns true for known US cities (lower-case, underscore-normalised).
func isUSCity(city string) bool {
	usCities := map[string]bool{
		"new_york": true, "newyork": true, "new york": true,
		"miami": true, "chicago": true, "los_angeles": true, "losangeles": true,
		"houston": true, "dallas": true, "seattle": true, "phoenix": true,
		"denver": true, "atlanta": true, "boston": true, "san_francisco": true,
		"sanfrancisco": true, "washington": true, "dc": true,
	}
	return usCities[city]
}

// isEuropeanCity returns true for known European cities.
func isEuropeanCity(city string) bool {
	euCities := map[string]bool{
		"london": true, "paris": true, "berlin": true, "madrid": true,
		"rome": true, "amsterdam": true, "brussels": true, "vienna": true,
		"zurich": true, "stockholm": true, "oslo": true, "copenhagen": true,
		"helsinki": true, "warsaw": true, "prague": true, "budapest": true,
		"lisbon": true, "athens": true, "munich": true,
	}
	return euCities[city]
}

// WeightedBeliefs converts registry weights into SourceBelief slices for use
// with BayesianEnsemble. Each entry in sources is (source, city, signal) key
// and a predicted probability.
func (r *SourceAccuracyRegistry) WeightedBeliefs(city, signal string, predictions map[string]float64) []SourceBelief {
	beliefs := make([]SourceBelief, 0, len(predictions))
	for src, p := range predictions {
		w := r.Weight(src, city, signal)
		noise := weightToNoise(w)
		beliefs = append(beliefs, SourceBelief{
			Source: src,
			P:      p,
			Noise:  noise,
		})
	}
	// Stable sort by source name for deterministic ordering.
	sort.Slice(beliefs, func(i, j int) bool {
		return beliefs[i].Source < beliefs[j].Source
	})
	return beliefs
}

// weightToNoise maps an aggregation weight to a Bayesian noise level.
// Higher weight (more trusted source) → lower noise.
//
//	weight 0.45 → noise 0.10 (very trusted)
//	weight 0.30 → noise 0.15 (moderate trust)
//	weight 0.20 → noise 0.22 (lower trust)
func weightToNoise(weight float64) float64 {
	if weight <= 0 {
		return 0.30
	}
	// Linear interpolation: noise = 0.30 − 0.44 × weight, clamped to [0.05, 0.30].
	noise := 0.30 - 0.44*weight
	return math.Max(0.05, math.Min(0.30, noise))
}

// Summary returns a human-readable multi-line summary of all tracked accuracies.
func (r *SourceAccuracyRegistry) Summary() string {
	r.mu.RLock()
	keys := make([]AccuracyKey, 0, len(r.data))
	for k := range r.data {
		keys = append(keys, k)
	}
	r.mu.RUnlock()

	sort.Slice(keys, func(i, j int) bool {
		if keys[i].Source != keys[j].Source {
			return keys[i].Source < keys[j].Source
		}
		if keys[i].City != keys[j].City {
			return keys[i].City < keys[j].City
		}
		return keys[i].Signal < keys[j].Signal
	})

	var sb strings.Builder
	for _, k := range keys {
		stats := r.Stats(k.Source, k.City, k.Signal)
		bs := stats.BrierScore()
		if math.IsNaN(bs) {
			continue
		}
		fmt.Fprintf(&sb, "%-12s / %-15s / %-12s → Brier: %.2f, N=%-3d (%s)\n",
			k.Source, k.City, k.Signal, bs, stats.N, stats.Quality())
	}
	return sb.String()
}

// PrometheusLines returns Prometheus exposition format lines for all tracked
// source accuracy metrics. Safe to append to an existing /metrics response.
func (r *SourceAccuracyRegistry) PrometheusLines() string {
	r.mu.RLock()
	keys := make([]AccuracyKey, 0, len(r.data))
	stats := make(map[AccuracyKey]AccuracyStats, len(r.data))
	for k, s := range r.data {
		keys = append(keys, k)
		stats[k] = *s
	}
	r.mu.RUnlock()

	sort.Slice(keys, func(i, j int) bool {
		ki, kj := keys[i], keys[j]
		if ki.Source != kj.Source {
			return ki.Source < kj.Source
		}
		if ki.City != kj.City {
			return ki.City < kj.City
		}
		return ki.Signal < kj.Signal
	})

	var sb strings.Builder
	sb.WriteString("# HELP source_brier_score Brier score per weather source, city, and signal (lower=better)\n")
	sb.WriteString("# TYPE source_brier_score gauge\n")
	for _, k := range keys {
		s := stats[k]
		if s.N == 0 {
			continue
		}
		fmt.Fprintf(&sb,
			"source_brier_score{source=%q,city=%q,signal=%q} %g\n",
			k.Source, k.City, k.Signal, s.BrierScore())
	}

	sb.WriteString("# HELP source_observation_count Number of resolved observations per source/city/signal\n")
	sb.WriteString("# TYPE source_observation_count counter\n")
	for _, k := range keys {
		s := stats[k]
		if s.N == 0 {
			continue
		}
		fmt.Fprintf(&sb,
			"source_observation_count{source=%q,city=%q,signal=%q} %d\n",
			k.Source, k.City, k.Signal, s.N)
	}

	sb.WriteString("# HELP source_weight Aggregation weight per source/city/signal (higher=more trusted)\n")
	sb.WriteString("# TYPE source_weight gauge\n")
	for _, k := range keys {
		w := r.Weight(k.Source, k.City, k.Signal)
		fmt.Fprintf(&sb,
			"source_weight{source=%q,city=%q,signal=%q} %g\n",
			k.Source, k.City, k.Signal, w)
	}

	return sb.String()
}
