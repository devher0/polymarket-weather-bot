// forecast_cache.go — disk-based cache for FusedForecast objects.
//
// In loop mode the bot runs every N seconds but weather forecasts change
// at most every few hours. This cache layer saves a fresh FusedForecast to
// data/forecasts/{city}_d{dayOffset}.json and reloads it on subsequent cycles
// without hitting any upstream APIs, cutting per-cycle API calls by ~95%.
//
// TASK-041
package collectors

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/devher0/polymarket-weather-bot/internal/weather"
)

// forecastCacheTTL is the default maximum age for a cached forecast.
// Callers may pass a shorter or longer duration to LoadForecastCache.
const forecastCacheTTL = 2 * time.Hour

// forecastCacheDir returns the directory where forecast JSON files are stored.
func forecastCacheDir(dataRoot string) string {
	if dataRoot == "" {
		dataRoot = "."
	}
	return filepath.Join(dataRoot, "data", "forecasts")
}

// forecastCachePath returns the full path for a city + day-offset cache file.
func forecastCachePath(dataRoot, city string, dayOffset int) string {
	filename := fmt.Sprintf("%s_d%d.json", city, dayOffset)
	return filepath.Join(forecastCacheDir(dataRoot), filename)
}

// cachedForecast is the on-disk envelope: wraps FusedForecast with a top-level
// SavedAt timestamp so we can check freshness without parsing the full struct.
type cachedForecast struct {
	SavedAt    time.Time          `json:"saved_at"`
	City       string             `json:"city"`
	DayOffset  int                `json:"day_offset"`
	Confidence float64            `json:"confidence"`
	Sources    []string           `json:"sources"`

	// Embedded weather.Forecast fields (flattened for readability in JSON).
	Date                     string  `json:"date"`
	MaxTempC                 float64 `json:"max_temp_c"`
	MinTempC                 float64 `json:"min_temp_c"`
	PrecipitationMM          float64 `json:"precipitation_mm"`
	PrecipitationProbability float64 `json:"precipitation_probability"`
	WindSpeedKMH             float64 `json:"wind_speed_kmh"`
	WeatherCode              int     `json:"weather_code"`

	EnsembleUncertainty float64                    `json:"ensemble_uncertainty"`
	FetchedAt           time.Time                  `json:"fetched_at"`
	PerSourceForecasts  map[string]weather.Forecast `json:"per_source_forecasts,omitempty"`
}

// SaveForecastCache persists ff to disk under dataRoot/data/forecasts/.
// Overwrites any existing cache file for this city+dayOffset combination.
// Errors are logged but never fatal — caching is best-effort.
func SaveForecastCache(city string, dayOffset int, ff *FusedForecast, dataRoot string) error {
	if ff == nil {
		return nil
	}

	dir := forecastCacheDir(dataRoot)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("forecast cache: mkdir %s: %w", dir, err)
	}

	c := cachedForecast{
		SavedAt:                  time.Now(),
		City:                     city,
		DayOffset:                dayOffset,
		Confidence:               ff.Confidence,
		Sources:                  ff.Sources,
		Date:                     ff.Forecast.Date,
		MaxTempC:                 ff.Forecast.MaxTempC,
		MinTempC:                 ff.Forecast.MinTempC,
		PrecipitationMM:          ff.Forecast.PrecipitationMM,
		PrecipitationProbability: ff.Forecast.PrecipitationProbability,
		WindSpeedKMH:             ff.Forecast.WindSpeedKMH,
		WeatherCode:              ff.Forecast.WeatherCode,
		EnsembleUncertainty:      ff.EnsembleUncertainty,
		FetchedAt:                ff.FetchedAt,
		PerSourceForecasts:       ff.PerSourceForecasts,
	}

	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return fmt.Errorf("forecast cache: marshal %s d%d: %w", city, dayOffset, err)
	}

	path := forecastCachePath(dataRoot, city, dayOffset)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("forecast cache: write %s: %w", path, err)
	}

	slog.Debug("forecast cache saved", "city", city, "day_offset", dayOffset, "path", path)
	return nil
}

// LoadForecastCache loads a cached forecast for city+dayOffset if it exists
// and its SavedAt age is within maxAge. Pass 0 to use the default TTL (2h).
//
// Returns (ff, true) on a fresh cache hit, or (nil, false) on miss or expiry.
func LoadForecastCache(city string, dayOffset int, dataRoot string, maxAge time.Duration) (*FusedForecast, bool) {
	if maxAge <= 0 {
		maxAge = forecastCacheTTL
	}

	path := forecastCachePath(dataRoot, city, dayOffset)
	data, err := os.ReadFile(path)
	if err != nil {
		// File doesn't exist — normal cache miss.
		return nil, false
	}

	var c cachedForecast
	if err := json.Unmarshal(data, &c); err != nil {
		slog.Warn("forecast cache: corrupt file, ignoring", "path", path, "err", err)
		return nil, false
	}

	age := time.Since(c.SavedAt)
	if age > maxAge {
		slog.Debug("forecast cache expired",
			"city", city, "day_offset", dayOffset,
			"age", age.Round(time.Minute).String(),
			"max_age", maxAge.String(),
		)
		return nil, false
	}

	ff := &FusedForecast{
		Forecast: weather.Forecast{
			City:                     c.City,
			Date:                     c.Date,
			MaxTempC:                 c.MaxTempC,
			MinTempC:                 c.MinTempC,
			PrecipitationMM:          c.PrecipitationMM,
			PrecipitationProbability: c.PrecipitationProbability,
			WindSpeedKMH:             c.WindSpeedKMH,
			WeatherCode:              c.WeatherCode,
		},
		Confidence:          c.Confidence,
		Sources:             c.Sources,
		EnsembleUncertainty: c.EnsembleUncertainty,
		FetchedAt:           c.FetchedAt,
		PerSourceForecasts:  c.PerSourceForecasts,
	}

	slog.Info("forecast cache hit",
		"city", city,
		"day_offset", dayOffset,
		"age", age.Round(time.Minute).String(),
		"confidence", fmt.Sprintf("%.2f", ff.Confidence),
	)
	return ff, true
}

// ForecastShift describes the change between two FusedForecast snapshots.
type ForecastShift struct {
	City          string
	DeltaMaxTempC float64
	DeltaPrecipP  float64
	Significant   bool // true if |ΔMaxTemp| > 5°C or |ΔPrecipProb| > 20%
}

// DetectForecastShift compares a newly-fetched forecast against the previous
// cached version for the same city+dayOffset.
//
// Returns a ForecastShift describing what changed, or nil if no prior cache
// existed or the cache had already been replaced.
//
// Must be called BEFORE SaveForecastCache so the old version is still on disk.
func DetectForecastShift(city string, dayOffset int, newFF *FusedForecast, dataRoot string) *ForecastShift {
	if newFF == nil {
		return nil
	}
	old, ok := LoadForecastCache(city, dayOffset, dataRoot, forecastCacheTTL*12) // long TTL: just need the previous version
	if !ok {
		return nil
	}

	dTemp := newFF.Forecast.MaxTempC - old.Forecast.MaxTempC
	dPrecip := newFF.Forecast.PrecipitationProbability - old.Forecast.PrecipitationProbability

	const tempThreshold = 5.0
	const precipThreshold = 20.0

	sig := (dTemp > tempThreshold || dTemp < -tempThreshold) ||
		(dPrecip > precipThreshold || dPrecip < -precipThreshold)

	return &ForecastShift{
		City:          city,
		DeltaMaxTempC: dTemp,
		DeltaPrecipP:  dPrecip,
		Significant:   sig,
	}
}

// ForecastCacheStats returns a quick summary of cached files in dataRoot.
// Used in dashboard / diagnostics.
func ForecastCacheStats(dataRoot string) map[string]time.Duration {
	dir := forecastCacheDir(dataRoot)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}

	ages := make(map[string]time.Duration, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		var c cachedForecast
		if err := json.Unmarshal(data, &c); err != nil {
			continue
		}
		key := fmt.Sprintf("%s_d%d", c.City, c.DayOffset)
		ages[key] = time.Since(c.SavedAt).Round(time.Second)
	}
	return ages
}
