// aggregator.go — fuses weather data from all sources into a single FusedForecast.
// Sources and weights: OpenMeteo=0.35, NASA=0.30, NOAA=0.25, GOES=0.10
package collectors

import (
	"fmt"
	"math"
	"time"

	"github.com/devher0/polymarket-weather-bot/internal/weather"
)

// FusedForecast combines forecasts from multiple sources with confidence score.
type FusedForecast struct {
	weather.Forecast
	Confidence float64  // 0-1: how much sources agree (1 = full agreement)
	Sources    []string // which sources contributed
}

// sourceWeights defines the base weight for each data source.
var sourceWeights = map[string]float64{
	"openmeteo": 0.35,
	"nasa":      0.30,
	"noaa":      0.25,
	"goes":      0.10,
}

// sourceResult holds a forecast from one source along with its weight.
type sourceResult struct {
	name     string
	forecast weather.Forecast
	weight   float64
	// cloud cover override for GOES (no standard Forecast fields)
	cloudCover *float64
}

// Aggregate fetches forecasts from all available sources and fuses them.
// dataRoot is used for GOES cache. Pass "" to use current directory.
func Aggregate(city string, dataRoot string) (*FusedForecast, error) {
	results := make([]sourceResult, 0, 4)

	// --- OpenMeteo ---
	if fc, err := weather.GetForecast(city, 3); err == nil && len(fc) > 0 {
		results = append(results, sourceResult{
			name:     "openmeteo",
			forecast: fc[0],
			weight:   sourceWeights["openmeteo"],
		})
	}

	// --- NASA POWER ---
	if fc, err := NASAGetForecast(city, 3); err == nil && len(fc) > 0 {
		results = append(results, sourceResult{
			name:     "nasa",
			forecast: fc[0],
			weight:   sourceWeights["nasa"],
		})
	}

	// --- NOAA NWS (US only) ---
	if fc, err := NOAAGetForecast(city, 3); err == nil && len(fc) > 0 {
		results = append(results, sourceResult{
			name:     "noaa",
			forecast: fc[0],
			weight:   sourceWeights["noaa"],
		})
	}

	// --- GOES-19 (cloud cover supplement) ---
	if cover, err := GOESGetCloudCover(city, dataRoot); err == nil {
		results = append(results, sourceResult{
			name:        "goes",
			weight:      sourceWeights["goes"],
			cloudCover:  &cover,
		})
	}

	if len(results) == 0 {
		return nil, fmt.Errorf("aggregator: no data sources available for city %q", city)
	}

	return fuse(city, results), nil
}

// fuse computes weighted averages and confidence from source results.
func fuse(city string, results []sourceResult) *FusedForecast {
	// Normalise weights to available sources.
	totalWeight := 0.0
	for _, r := range results {
		totalWeight += r.weight
	}
	if totalWeight == 0 {
		totalWeight = 1
	}

	// Collect source names and weighted sums for each parameter.
	var (
		wMaxTemp  float64
		wMinTemp  float64
		wPrecip   float64
		wPrecipP  float64
		wWind     float64
		wCode     float64

		sourceNames []string
		precipProbs []float64 // for confidence calc
	)

	// Filter to sources that have actual forecast data (not just cloud cover).
	forecastSources := make([]sourceResult, 0, len(results))
	for _, r := range results {
		sourceNames = append(sourceNames, r.name)
		if r.cloudCover == nil {
			forecastSources = append(forecastSources, r)
		}
	}

	// Recalculate total weight for forecast sources only.
	fcWeight := 0.0
	for _, r := range forecastSources {
		fcWeight += r.weight
	}
	if fcWeight == 0 {
		fcWeight = 1
	}

	for _, r := range forecastSources {
		w := r.weight / fcWeight
		wMaxTemp += r.forecast.MaxTempC * w
		wMinTemp += r.forecast.MinTempC * w
		wPrecip += r.forecast.PrecipitationMM * w
		wPrecipP += r.forecast.PrecipitationProbability * w
		wWind += r.forecast.WindSpeedKMH * w
		wCode += float64(r.forecast.WeatherCode) * w
		precipProbs = append(precipProbs, r.forecast.PrecipitationProbability/100.0)
	}

	// Pick representative date from highest-weight source.
	bestDate := ""
	bestW := -1.0
	for _, r := range forecastSources {
		if r.weight > bestW {
			bestW = r.weight
			bestDate = r.forecast.Date
		}
	}
	if bestDate == "" {
		bestDate = "unknown"
	}

	// Confidence = 1 - stddev of precipitation probabilities across sources.
	confidence := 1.0
	if len(precipProbs) > 1 {
		sd := stddev(precipProbs)
		confidence = math.Max(0, 1-sd)
	}

	fused := &FusedForecast{
		Forecast: weather.Forecast{
			City:                     city,
			Date:                     bestDate,
			MaxTempC:                 wMaxTemp,
			MinTempC:                 wMinTemp,
			PrecipitationMM:          wPrecip,
			PrecipitationProbability: wPrecipP,
			WindSpeedKMH:             wWind,
			WeatherCode:              int(math.Round(wCode)),
		},
		Confidence: confidence,
		Sources:    sourceNames,
	}

	return fused
}

// AggregateForDay fetches a fused forecast for a specific day offset:
// dayOffset=0 → today, dayOffset=1 → tomorrow, up to dayOffset=6.
// GOES satellite data (cloud cover) is only available for today (dayOffset=0).
func AggregateForDay(city string, dayOffset int, dataRoot string) (*FusedForecast, error) {
	if dayOffset < 0 {
		dayOffset = 0
	}
	if dayOffset > 6 {
		dayOffset = 6
	}
	days := dayOffset + 1 // need at least dayOffset+1 forecast days

	results := make([]sourceResult, 0, 4)

	// --- OpenMeteo ---
	if fc, err := weather.GetForecast(city, days); err == nil {
		idx := dayOffset
		if idx >= len(fc) {
			idx = len(fc) - 1
		}
		if len(fc) > 0 {
			results = append(results, sourceResult{
				name:     "openmeteo",
				forecast: fc[idx],
				weight:   sourceWeights["openmeteo"],
			})
		}
	}

	// --- NASA POWER ---
	if fc, err := NASAGetForecast(city, days); err == nil {
		idx := dayOffset
		if idx >= len(fc) {
			idx = len(fc) - 1
		}
		if len(fc) > 0 {
			results = append(results, sourceResult{
				name:     "nasa",
				forecast: fc[idx],
				weight:   sourceWeights["nasa"],
			})
		}
	}

	// --- NOAA NWS (US only) ---
	if fc, err := NOAAGetForecast(city, days); err == nil {
		idx := dayOffset
		if idx >= len(fc) {
			idx = len(fc) - 1
		}
		if len(fc) > 0 {
			results = append(results, sourceResult{
				name:     "noaa",
				forecast: fc[idx],
				weight:   sourceWeights["noaa"],
			})
		}
	}

	// --- GOES-19 (cloud cover supplement, today only) ---
	if dayOffset == 0 {
		if cover, err := GOESGetCloudCover(city, dataRoot); err == nil {
			results = append(results, sourceResult{
				name:       "goes",
				weight:     sourceWeights["goes"],
				cloudCover: &cover,
			})
		}
	}

	if len(results) == 0 {
		return nil, fmt.Errorf("aggregator: no data sources available for city %q day+%d", city, dayOffset)
	}

	ff := fuse(city, results)
	// Embed the target date in the fused result so callers can inspect it.
	if ff.Forecast.Date == "" || ff.Forecast.Date == "unknown" {
		ff.Forecast.Date = time.Now().UTC().AddDate(0, 0, dayOffset).Format("2006-01-02")
	}
	return ff, nil
}

// AggregateAll fetches fused forecasts for all known cities.
// Errors per city are collected and returned as a combined error.
func AggregateAll(dataRoot string) (map[string]*FusedForecast, error) {
	out := make(map[string]*FusedForecast, len(weather.Cities))
	var errs []string

	for city := range weather.Cities {
		ff, err := Aggregate(city, dataRoot)
		if err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", city, err))
			continue
		}
		out[city] = ff
	}

	if len(errs) > 0 && len(out) == 0 {
		return nil, fmt.Errorf("aggregator: all cities failed: %v", errs)
	}
	return out, nil
}

// stddev computes population standard deviation of a slice.
func stddev(vals []float64) float64 {
	if len(vals) == 0 {
		return 0
	}
	mean := 0.0
	for _, v := range vals {
		mean += v
	}
	mean /= float64(len(vals))

	variance := 0.0
	for _, v := range vals {
		d := v - mean
		variance += d * d
	}
	variance /= float64(len(vals))
	return math.Sqrt(variance)
}
