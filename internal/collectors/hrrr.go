// hrrr.go — NOAA HRRR (High-Resolution Rapid Refresh) via Open-Meteo.
//
// TASK-086: HRRR is a 3 km convection-allowing model updated every hour by
// NOAA. It is only available for North America (US cities). We fetch it
// through the Open-Meteo HRRR endpoint, which exposes the same daily/hourly
// JSON schema as other Open-Meteo models.
//
// Parameters: temperature_2m_max, temperature_2m_min, precipitation_sum,
// precipitation_probability_max, wind_speed_10m_max, weather_code, and
// cape (convective available potential energy — a key signal for storm/wind
// markets).
//
// Cache TTL: 60 minutes (HRRR updates hourly so caching longer is wasteful).
package collectors

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/devher0/polymarket-weather-bot/internal/weather"
)

// hrrrCacheEntry holds a cached HRRR result per city+days key.
type hrrrCacheEntry struct {
	forecasts []weather.Forecast
	fetchedAt time.Time
}

var (
	hrrrCache  sync.Map
	hrrrTTL    = 60 * time.Minute
	hrrrClient = &http.Client{Timeout: 15 * time.Second}
)

// hrrrResp is the JSON envelope returned by the Open-Meteo HRRR endpoint.
type hrrrResp struct {
	Daily struct {
		Time            []string  `json:"time"`
		TempMax         []float64 `json:"temperature_2m_max"`
		TempMin         []float64 `json:"temperature_2m_min"`
		PrecipSum       []float64 `json:"precipitation_sum"`
		PrecipProbMax   []float64 `json:"precipitation_probability_max"`
		WindSpeedMax    []float64 `json:"wind_speed_10m_max"`
		WeatherCode     []int     `json:"weather_code"`
		CapeMax         []float64 `json:"cape_max"`
	} `json:"daily"`
}

// HRRRGetForecast fetches daily forecasts for a US city from the NOAA HRRR
// model via Open-Meteo. Returns an error for non-US cities; results are cached
// for up to 60 minutes.
func HRRRGetForecast(city string, days int) ([]weather.Forecast, error) {
	// HRRR covers North America only.
	if !usCities[city] {
		return nil, fmt.Errorf("hrrr: city %q is outside HRRR coverage area", city)
	}

	cacheKey := fmt.Sprintf("%s_%d", city, days)
	if v, ok := hrrrCache.Load(cacheKey); ok {
		entry := v.(hrrrCacheEntry)
		if time.Since(entry.fetchedAt) < hrrrTTL {
			return entry.forecasts, nil
		}
	}

	c, ok := weather.Cities[city]
	if !ok {
		return nil, fmt.Errorf("hrrr: unknown city %q", city)
	}

	if days < 1 {
		days = 1
	}
	// HRRR via Open-Meteo supports up to 3 days of daily forecast.
	if days > 3 {
		days = 3
	}

	url := fmt.Sprintf(
		"https://api.open-meteo.com/v1/forecast"+
			"?latitude=%.4f&longitude=%.4f"+
			"&daily=temperature_2m_max,temperature_2m_min,precipitation_sum,"+
			"precipitation_probability_max,wind_speed_10m_max,weather_code,cape_max"+
			"&models=best_match"+
			"&forecast_days=%d&timezone=UTC",
		c.Lat, c.Lon, days,
	)

	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("hrrr: build request: %w", err)
	}
	req.Header.Set("User-Agent", "polymarket-weather-bot/1.0")

	resp, err := hrrrClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("hrrr: http: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("hrrr: status %d: %s", resp.StatusCode, string(body))
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("hrrr: read body: %w", err)
	}

	var r hrrrResp
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, fmt.Errorf("hrrr: parse: %w", err)
	}

	if len(r.Daily.Time) == 0 || len(r.Daily.TempMax) == 0 {
		return nil, fmt.Errorf("hrrr: empty daily arrays in response for %s", city)
	}

	out := make([]weather.Forecast, 0, len(r.Daily.Time))
	for i, dateStr := range r.Daily.Time {
		if i >= len(r.Daily.TempMax) || i >= len(r.Daily.TempMin) {
			break
		}

		var precipMM, precipProb, windKMH float64
		var wcode int
		// cape is convective available potential energy in J/kg — captured
		// in WeatherCode adjustment for storm markets (see hrrrWeatherCode).
		var capeMax float64

		if i < len(r.Daily.PrecipSum) {
			precipMM = r.Daily.PrecipSum[i]
		}
		if i < len(r.Daily.PrecipProbMax) {
			precipProb = r.Daily.PrecipProbMax[i]
		}
		if i < len(r.Daily.WindSpeedMax) {
			windKMH = r.Daily.WindSpeedMax[i]
		}
		if i < len(r.Daily.WeatherCode) {
			wcode = r.Daily.WeatherCode[i]
		}
		if i < len(r.Daily.CapeMax) {
			capeMax = r.Daily.CapeMax[i]
		}

		// Elevate weather code for high CAPE (convective storms likely).
		if capeMax > 0 {
			wcode = hrrrWeatherCode(wcode, capeMax)
		}

		out = append(out, weather.Forecast{
			City:                     city,
			Date:                     dateStr,
			MaxTempC:                 r.Daily.TempMax[i],
			MinTempC:                 r.Daily.TempMin[i],
			PrecipitationMM:          precipMM,
			PrecipitationProbability: precipProb,
			WindSpeedKMH:             windKMH,
			WeatherCode:              wcode,
		})
	}

	if len(out) == 0 {
		return nil, fmt.Errorf("hrrr: no usable data for %s", city)
	}

	hrrrCache.Store(cacheKey, hrrrCacheEntry{forecasts: out, fetchedAt: time.Now()})
	return out, nil
}

// hrrrWeatherCode refines an existing WMO weather code using CAPE (J/kg).
// CAPE > 1000 J/kg indicates significant convective potential (thunderstorm
// risk); > 2500 J/kg indicates severe thunderstorm potential.
func hrrrWeatherCode(base int, capeJkg float64) int {
	switch {
	case capeJkg > 2500 && base < 95:
		return 95 // thunderstorm (severe)
	case capeJkg > 1000 && base < 80:
		return 80 // showers with convection
	default:
		return base
	}
}
