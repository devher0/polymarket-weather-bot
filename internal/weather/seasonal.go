// Package weather — seasonal.go provides monthly climatological baselines for
// Bayesian adjustment of short-term forecast probabilities.
//
// For each city we store, per calendar month (1–12):
//   - AvgMaxTempC  — historical mean daily maximum temperature (°C)
//   - RainProb     — fraction of days with >1 mm precipitation (0–1)
//   - SunProb      — fraction of clear/mainly-clear days (0–1)
//
// AdjustForSeason blends a raw model probability with the climatological prior,
// weighting by forecast horizon:
//   - Day 0-1 → alpha=0.80 (trust the NWP model more)
//   - Day 2-3 → alpha=0.65
//   - Day 4-5 → alpha=0.50
//   - Day 6   → alpha=0.40 (climatology matters more for far-out forecasts)
package weather

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"time"
)

// MonthlyClimate holds climatological normals for one city in one month.
type MonthlyClimate struct {
	AvgMaxTempC float64 // °C
	RainProb    float64 // 0–1 fraction of rainy days
	SunProb     float64 // 0–1 fraction of sunny days
}

// climatology is indexed [city][month-1] (month 1=January … 12=December).
var climatology = map[string][12]MonthlyClimate{
	"new_york": {
		{4, 0.27, 0.43}, {6, 0.27, 0.46}, {11, 0.32, 0.51}, {17, 0.34, 0.54},
		{22, 0.37, 0.58}, {27, 0.36, 0.64}, {30, 0.37, 0.62}, {29, 0.38, 0.63},
		{25, 0.32, 0.63}, {19, 0.31, 0.59}, {13, 0.32, 0.47}, {7, 0.30, 0.43},
	},
	"london": {
		{8, 0.52, 0.28}, {9, 0.48, 0.33}, {12, 0.47, 0.40}, {15, 0.44, 0.47},
		{18, 0.43, 0.53}, {21, 0.42, 0.57}, {24, 0.43, 0.58}, {24, 0.44, 0.57},
		{20, 0.47, 0.52}, {16, 0.50, 0.43}, {11, 0.53, 0.31}, {9, 0.54, 0.26},
	},
	"tokyo": {
		{10, 0.15, 0.60}, {11, 0.17, 0.55}, {14, 0.27, 0.50}, {19, 0.35, 0.48},
		{23, 0.38, 0.48}, {26, 0.52, 0.33}, {30, 0.47, 0.37}, {31, 0.40, 0.42},
		{27, 0.48, 0.38}, {22, 0.33, 0.52}, {17, 0.23, 0.60}, {12, 0.17, 0.62},
	},
	"miami": {
		{24, 0.20, 0.68}, {25, 0.20, 0.70}, {27, 0.23, 0.72}, {29, 0.27, 0.70},
		{31, 0.43, 0.62}, {32, 0.63, 0.52}, {33, 0.63, 0.50}, {33, 0.63, 0.50},
		{32, 0.60, 0.52}, {30, 0.47, 0.58}, {27, 0.30, 0.65}, {25, 0.23, 0.68},
	},
	"paris": {
		{7, 0.40, 0.32}, {9, 0.37, 0.38}, {13, 0.40, 0.45}, {17, 0.40, 0.50},
		{21, 0.43, 0.57}, {24, 0.40, 0.62}, {26, 0.37, 0.67}, {26, 0.37, 0.65},
		{22, 0.37, 0.58}, {16, 0.43, 0.47}, {11, 0.43, 0.35}, {8, 0.43, 0.28},
	},
	"chicago": {
		{-2, 0.37, 0.42}, {0, 0.37, 0.45}, {7, 0.40, 0.48}, {14, 0.43, 0.53},
		{20, 0.43, 0.58}, {26, 0.43, 0.65}, {29, 0.40, 0.68}, {28, 0.40, 0.67},
		{23, 0.37, 0.62}, {16, 0.40, 0.55}, {8, 0.40, 0.43}, {1, 0.40, 0.38},
	},
	"los_angeles": {
		{19, 0.27, 0.68}, {20, 0.27, 0.67}, {21, 0.20, 0.72}, {23, 0.10, 0.78},
		{24, 0.03, 0.82}, {26, 0.03, 0.80}, {29, 0.03, 0.87}, {30, 0.03, 0.87},
		{29, 0.07, 0.83}, {26, 0.10, 0.80}, {23, 0.17, 0.72}, {20, 0.27, 0.65},
	},
	"san_francisco": {
		{13, 0.40, 0.43}, {15, 0.37, 0.45}, {16, 0.33, 0.52}, {17, 0.20, 0.60},
		{18, 0.10, 0.65}, {19, 0.03, 0.62}, {20, 0.03, 0.60}, {20, 0.03, 0.65},
		{21, 0.07, 0.72}, {19, 0.17, 0.68}, {16, 0.33, 0.52}, {14, 0.43, 0.42},
	},
	"berlin": {
		{3, 0.40, 0.28}, {4, 0.37, 0.33}, {9, 0.37, 0.43}, {14, 0.37, 0.53},
		{19, 0.43, 0.58}, {23, 0.43, 0.62}, {25, 0.43, 0.63}, {25, 0.43, 0.62},
		{20, 0.40, 0.53}, {14, 0.40, 0.40}, {8, 0.43, 0.28}, {4, 0.43, 0.23},
	},
}

// GetSeasonal returns climatological normals for the given city and month (1-12).
// Returns zero value and false when city or month is unknown.
func GetSeasonal(city string, month time.Month) (MonthlyClimate, bool) {
	months, ok := climatology[city]
	if !ok {
		return MonthlyClimate{}, false
	}
	if month < 1 || month > 12 {
		return MonthlyClimate{}, false
	}
	return months[int(month)-1], true
}

// forecastAlpha returns the weight given to the raw model probability vs the
// climatological prior, based on how many days ahead the forecast date is.
// Closer forecasts get higher alpha (more model weight).
func forecastAlpha(forecastDate string) float64 {
	t, err := time.Parse("2006-01-02", forecastDate)
	if err != nil {
		// Try RFC3339
		t, err = time.Parse(time.RFC3339, forecastDate)
		if err != nil {
			return 0.70 // safe default
		}
	}
	days := int(math.Round(time.Until(t).Hours() / 24))
	switch {
	case days <= 1:
		return 0.80
	case days <= 3:
		return 0.65
	case days <= 5:
		return 0.50
	default:
		return 0.40
	}
}

// priorForSignal returns the climatological base rate probability for a given
// weather signal (rain|heat|cold|snow|wind|sunny) in the given city + month.
// Returns -1 if the city/month is not found or the signal has no seasonal prior.
func priorForSignal(city string, month time.Month, signal string, thresholdC float64) float64 {
	mc, ok := GetSeasonal(city, month)
	if !ok {
		return -1
	}
	switch signal {
	case "rain":
		return mc.RainProb
	case "sunny":
		return mc.SunProb
	case "heat":
		// Probability that AvgMaxTemp is near/above threshold is approximated
		// by a soft step function centred on the historical mean.
		diff := mc.AvgMaxTempC - thresholdC
		return clampedSigmoid(diff, 3.0)
	case "cold":
		// cold = probability max stays *below* threshold
		diff := thresholdC - mc.AvgMaxTempC
		return clampedSigmoid(diff, 3.0)
	case "snow":
		// Snow is roughly cold + rain; seasonal table gives an approximation.
		coldP := 1 - clampedSigmoid(mc.AvgMaxTempC-2.0, 3.0)
		return coldP * mc.RainProb * 0.8
	case "wind":
		// Wind has no direct climatology entry; return -1 to skip adjustment.
		return -1
	case "fog":
		// Fog is more likely in high-rain months when humidity is elevated.
		// Approximate: fog_prob ≈ rain_prob × 0.30 (rough empirical ratio).
		return clamp(mc.RainProb*0.30, 0.03, 0.40)
	case "humid":
		// High humidity correlates strongly with rain probability and warm temps.
		return clamp(mc.RainProb*0.80+0.10, 0.10, 0.90)
	case "dry":
		// Dry = complement of rain on most days.
		return clamp(1-mc.RainProb, 0.05, 0.95)
	}
	return -1
}

// clampedSigmoid returns a probability 0-1 based on a difference in degrees
// and scale.  At diff=0 it returns 0.5; saturates towards 0.05/0.95 far away.
func clampedSigmoid(diff, scale float64) float64 {
	p := 1.0 / (1.0 + math.Exp(-diff/scale))
	return clamp(p, 0.05, 0.95)
}

// AdjustForSeason applies a Bayesian blend of the raw model probability with
// the monthly climatological prior.  Reduces overconfidence on distant
// forecasts, and corrects systematic biases in models that under/overestimate
// seasonal baselines.
//
// Parameters:
//   - city        : e.g. "miami", "london"
//   - forecastDate: "2026-06-15" or RFC3339
//   - rawP        : 0–1 probability from weather model
//   - signal      : "rain"|"heat"|"cold"|"snow"|"wind"|"sunny"
//   - thresholdC  : temp threshold in °C (used for heat/cold signals; 0 ok for others)
//
// Returns rawP unchanged when no seasonal data is available for this city/signal.
func AdjustForSeason(city, forecastDate string, rawP float64, signal string, thresholdC float64) float64 {
	t, err := time.Parse("2006-01-02", forecastDate)
	if err != nil {
		t, err = time.Parse(time.RFC3339, forecastDate)
		if err != nil {
			return rawP
		}
	}
	prior := priorForSignal(city, t.Month(), signal, thresholdC)
	if prior < 0 {
		return rawP // no seasonal data for this signal
	}
	alpha := forecastAlpha(forecastDate)
	adjusted := alpha*rawP + (1-alpha)*prior
	return clamp(adjusted, 0.02, 0.97)
}

// SeasonalSummary returns a human-readable description of seasonal priors for
// a city and month. Useful for logging and debugging.
func SeasonalSummary(city string, month time.Month) string {
	mc, ok := GetSeasonal(city, month)
	if !ok {
		return fmt.Sprintf("no seasonal data for %s", city)
	}
	return fmt.Sprintf("%s %s: maxTemp=%.0f°C rain=%.0f%% sun=%.0f%%",
		city, month, mc.AvgMaxTempC, mc.RainProb*100, mc.SunProb*100)
}

// ── TASK-064: Per-city climate anomaly score ───────────────────────────────

// historicalFileShape is a minimal struct for deserialising the historical
// JSON files written by collectors.CollectHistory (data/historical/{city}.json).
// We only need the MaxTempC fields for anomaly computation.
// NOTE: Forecast struct has no json tags, so Go serialises field names as-is
// (PascalCase). HistoricalRecord embeds Forecast so the records JSON looks like
// {"MaxTempC": 32.1, "Date": "2026-05-01", ...}.
type historicalFileShape struct {
	Records []struct {
		MaxTempC float64 `json:"MaxTempC"`
		Date     string  `json:"Date"`
	} `json:"records"`
}

// ClimateAnomalyScore returns a 0–1 score reflecting how extreme the given
// maxTemp is compared to the recent historical baseline for this city.
//
// Algorithm:
//  1. Load the last 30 days of observed MaxTempC from data/historical/{city}.json
//  2. Compute rolling mean (mu) and population standard deviation (sigma)
//  3. score = clamp((maxTemp - mu) / (2 * sigma), 0, 1)
//     score ≈ 0 → near average; score ≈ 1 → 2+ standard deviations above norm
//
// Returns 0 when historical data is unavailable (file missing, fewer than 7 records).
func ClimateAnomalyScore(city string, maxTemp float64, dataRoot string) float64 {
	if dataRoot == "" {
		dataRoot = "."
	}
	path := filepath.Join(dataRoot, "data", "historical", city+".json")

	data, err := os.ReadFile(path)
	if err != nil {
		return 0
	}

	var hf historicalFileShape
	if err := json.Unmarshal(data, &hf); err != nil {
		return 0
	}

	// Collect at most the last 30 records.
	recs := hf.Records
	if len(recs) == 0 {
		return 0
	}
	const window = 30
	const minRecs = 7
	if len(recs) > window {
		recs = recs[len(recs)-window:]
	}
	if len(recs) < minRecs {
		return 0
	}

	// Compute mean and population standard deviation.
	sum := 0.0
	for _, r := range recs {
		sum += r.MaxTempC
	}
	mu := sum / float64(len(recs))

	varSum := 0.0
	for _, r := range recs {
		d := r.MaxTempC - mu
		varSum += d * d
	}
	sigma := math.Sqrt(varSum / float64(len(recs)))
	if sigma <= 0 {
		return 0
	}

	// Normalise: score approaches 1 at 2 standard deviations above the mean.
	score := (maxTemp - mu) / (2 * sigma)
	if score < 0 {
		score = 0
	}
	if score > 1 {
		score = 1
	}
	return score
}
