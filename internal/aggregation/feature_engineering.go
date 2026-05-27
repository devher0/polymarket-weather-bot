// feature_engineering.go — TASK-116: extended feature set for gradient boosting.
//
// Extends the base 8 features (FeatureVec) to ~25 by adding:
//   - Interaction terms: openmeteo_p × nasa_p, source agreement
//   - Temporal lag features: yesterday_rain_prob, 3-day rain trend
//   - Rolling aggregates: mean_7d_precip, max_7d_temp, std_7d_wind
//   - City one-hot embedding (15 cities → 15 bits)
//   - Signal one-hot embedding (rain/heat/cold/snow/wind/sunny/uv → 7 bits)
//
// Total: 8 base + 3 interaction + 3 lag + 3 rolling + 15 city + 7 signal = 39 features
// (indices beyond 39 are padded with 0 for forward compatibility)
//
// Feature importance is written to data/feature_importance.json after each
// training run.
package aggregation

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"sort"
)

// cityIndex maps city names to integer IDs used for one-hot encoding.
// TASK-116: 15 cities (indices 0–14); unknown → all zeros.
var cityIndex = map[string]int{
	"new_york":      0,
	"miami":         1,
	"chicago":       2,
	"los_angeles":   3,
	"san_francisco": 4,
	"london":        5,
	"paris":         6,
	"berlin":        7,
	"dubai":         8,
	"sydney":        9,
	"singapore":     10,
	"toronto":       11,
	"moscow":        12,
	"tokyo":         13,
	"rio":           14,
}

// signalIndex maps signal names to embedding positions.
// TASK-116: 7 signals (indices 0–6).
var signalIndex = map[string]int{
	"rain":  0,
	"heat":  1,
	"cold":  2,
	"snow":  3,
	"wind":  4,
	"sunny": 5,
	"uv":    6,
}

const (
	numCities  = 15
	numSignals = 7
)

// FeatureVecExt is the extended feature vector with ~39 dimensions.
// It embeds the base FeatureVec and adds interaction, lag, rolling,
// and embedding features.
type FeatureVecExt struct {
	Base FeatureVec

	// Interaction terms (indices 8-10)
	OpenMeteoXNASA float64 // openmeteo_p × nasa_p (source agreement)
	SourceAgreement float64 // fraction of sources within ±0.10 of OpenMeteo prediction
	TempRank       float64 // (maxTemp - historical_mean) / historical_std (normalised)

	// Lag features (indices 11-13)
	YesterdayRainProb float64 // previous day's rain probability (0–1)
	RainTrend3d       float64 // (today - 3-day-ago rain prob) / 3; positive = increasing
	TempTrend3d       float64 // (today - 3-day-ago maxTemp) / 3°C per day

	// Rolling aggregates (indices 14-16)
	Mean7dPrecip float64 // mean precipitation (mm) over last 7 days
	Max7dTemp    float64 // max temperature (°C) over last 7 days, normalised ÷ 50
	Std7dWind    float64 // std-dev of wind speed (km/h) over last 7 days, normalised ÷ 30

	// City one-hot (indices 17-31)
	CityOH [numCities]float64

	// Signal one-hot (indices 32-38)
	SignalOH [numSignals]float64
}

// SetCity sets the one-hot city embedding by name. Unknown cities produce all zeros.
func (f *FeatureVecExt) SetCity(city string) {
	if idx, ok := cityIndex[city]; ok {
		f.CityOH[idx] = 1.0
	}
}

// SetSignal sets the one-hot signal embedding by name. Unknown signals → all zeros.
func (f *FeatureVecExt) SetSignal(signal string) {
	if idx, ok := signalIndex[signal]; ok {
		f.SignalOH[idx] = 1.0
	}
}

// ToSlice serialises the extended feature vector to a flat normalised float64 slice.
// The length is always baseFeatures + 3 + 3 + 3 + numCities + numSignals = 39.
func (f FeatureVecExt) ToSlice() []float64 {
	base := f.Base.toSlice() // 8 base features

	ext := []float64{
		// Interaction (8-10)
		f.OpenMeteoXNASA,
		f.SourceAgreement,
		clamp01(f.TempRank),
		// Lag (11-13)
		f.YesterdayRainProb,
		clamp11(f.RainTrend3d),
		clamp11(f.TempTrend3d),
		// Rolling (14-16)
		f.Mean7dPrecip / 30.0, // normalise: 30mm = 1.0
		f.Max7dTemp / 50.0,
		f.Std7dWind / 30.0,
	}

	city := f.CityOH[:]
	signal := f.SignalOH[:]

	out := make([]float64, 0, len(base)+len(ext)+numCities+numSignals)
	out = append(out, base...)
	out = append(out, ext...)
	out = append(out, city...)
	out = append(out, signal...)
	return out
}

// FeatureNames returns the ordered list of feature names matching ToSlice().
// Useful for feature importance reporting.
func FeatureNames() []string {
	names := []string{
		// Base (0-7)
		"openmeteo_p", "nasa_p", "noaa_p", "goes_cloud",
		"cape_norm", "pressure_trend_norm", "month_norm", "city_id_norm",
		// Interaction (8-10)
		"openmeteo_x_nasa", "source_agreement", "temp_rank",
		// Lag (11-13)
		"yesterday_rain_prob", "rain_trend_3d", "temp_trend_3d",
		// Rolling (14-16)
		"mean_7d_precip_norm", "max_7d_temp_norm", "std_7d_wind_norm",
	}
	// City one-hot (17-31)
	cities := []string{
		"city_new_york", "city_miami", "city_chicago", "city_los_angeles",
		"city_san_francisco", "city_london", "city_paris", "city_berlin",
		"city_dubai", "city_sydney", "city_singapore", "city_toronto",
		"city_moscow", "city_tokyo", "city_rio",
	}
	// Signal one-hot (32-38)
	signals := []string{
		"sig_rain", "sig_heat", "sig_cold", "sig_snow",
		"sig_wind", "sig_sunny", "sig_uv",
	}
	names = append(names, cities...)
	names = append(names, signals...)
	return names
}

// BuildExtended constructs a FeatureVecExt from a base FeatureVec plus
// optional derived values. Callers compute lag/rolling features from
// historical data; pass 0 for fields not available.
func BuildExtended(base FeatureVec, city, signal string, opts ExtendedOpts) FeatureVecExt {
	f := FeatureVecExt{Base: base}
	f.SetCity(city)
	f.SetSignal(signal)

	// Interaction: product of two most reliable source probabilities.
	f.OpenMeteoXNASA = base.OpenMeteoP * base.NASAP

	// Source agreement: fraction of the 4 sources within ±0.10 of OpenMeteo.
	probs := []float64{base.NASAP, base.NOAAP, base.GOESCloud}
	agree := 0.0
	for _, p := range probs {
		if math.Abs(p-base.OpenMeteoP) <= 0.10 {
			agree++
		}
	}
	f.SourceAgreement = agree / float64(len(probs))

	f.TempRank = opts.TempRank
	f.YesterdayRainProb = opts.YesterdayRainProb
	f.RainTrend3d = opts.RainTrend3d
	f.TempTrend3d = opts.TempTrend3d
	f.Mean7dPrecip = opts.Mean7dPrecip
	f.Max7dTemp = opts.Max7dTemp
	f.Std7dWind = opts.Std7dWind

	return f
}

// ExtendedOpts holds optional derived features that callers provide.
// Fields that cannot be computed (missing historical data) should be left at zero.
type ExtendedOpts struct {
	TempRank          float64 // (maxTemp - rolling_mean) / (2 × rolling_std); clipped to [-1, 1]
	YesterdayRainProb float64 // previous day's precipitation probability (0–1)
	RainTrend3d       float64 // rate-of-change in rain probability over last 3 days
	TempTrend3d       float64 // rate-of-change in max temperature over last 3 days (°C/day)
	Mean7dPrecip      float64 // 7-day rolling mean precipitation (mm)
	Max7dTemp         float64 // 7-day rolling max temperature (°C)
	Std7dWind         float64 // 7-day rolling std-dev of wind speed (km/h)
}

// ─── Feature Importance ──────────────────────────────────────────────────────

// FeatureImportance records the importance of each feature after training.
type FeatureImportance struct {
	Name  string  `json:"name"`
	Score float64 `json:"score"` // higher = more frequently chosen as split feature
}

// ComputeFeatureImportance tallies how many times each feature index was used
// as the best split in a GBModel trained on extended features.
// Returns a list sorted by Score descending.
func ComputeFeatureImportance(m *GBModel) []FeatureImportance {
	if m == nil {
		return nil
	}
	names := FeatureNames()
	counts := make([]float64, len(names))

	for _, t := range m.Trees {
		idx := t.Root.FeatureIdx
		if idx >= 0 && idx < len(counts) {
			counts[idx]++
		}
	}

	total := float64(len(m.Trees))
	if total == 0 {
		total = 1
	}

	result := make([]FeatureImportance, 0, len(names))
	for i, name := range names {
		result = append(result, FeatureImportance{
			Name:  name,
			Score: counts[i] / total,
		})
	}

	sort.Slice(result, func(i, j int) bool {
		return result[i].Score > result[j].Score
	})
	return result
}

// SaveFeatureImportance writes feature importance to data/feature_importance.json.
func SaveFeatureImportance(imp []FeatureImportance, dataRoot string) error {
	if dataRoot == "" {
		dataRoot = "."
	}
	if err := os.MkdirAll(filepath.Join(dataRoot, "data"), 0o755); err != nil {
		return err
	}
	path := filepath.Join(dataRoot, "data", "feature_importance.json")
	b, err := json.MarshalIndent(imp, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o644)
}

// LoadFeatureImportance reads data/feature_importance.json. Returns nil when absent.
func LoadFeatureImportance(dataRoot string) []FeatureImportance {
	if dataRoot == "" {
		dataRoot = "."
	}
	b, err := os.ReadFile(filepath.Join(dataRoot, "data", "feature_importance.json"))
	if err != nil {
		return nil
	}
	var imp []FeatureImportance
	if json.Unmarshal(b, &imp) != nil {
		return nil
	}
	return imp
}

// clamp01 clips v to [0, 1].
func clamp01(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

// clamp11 clips v to [-1, 1].
func clamp11(v float64) float64 {
	if v < -1 {
		return -1
	}
	if v > 1 {
		return 1
	}
	return v
}
