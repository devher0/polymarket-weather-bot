// gfs.go — NOAA GFS (Global Forecast System) via Open-Meteo.
//
// TASK-092: GFS is NOAA's flagship global forecast model used by all
// professional weather traders. Key differentiator: 16-day forecast horizon
// (unique — longer than any other source). GFS_seamless on Open-Meteo blends
// GFS with high-resolution HRRR for the US, providing the best of both.
//
// We add GFS as a 7th source (US-and-global) and expose a Forecast16Days slice
// for markets with expiry > 7 days out.
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

// gfsCacheEntry holds a cached GFS result.
type gfsCacheEntry struct {
	forecasts []weather.Forecast
	fetchedAt time.Time
}

var (
	gfsCache  sync.Map
	gfsTTL    = 6 * time.Hour
	gfsClient = &http.Client{Timeout: 20 * time.Second}
)

// gfsResp is the JSON envelope from the Open-Meteo GFS endpoint.
type gfsResp struct {
	Daily struct {
		Time          []string  `json:"time"`
		TempMax       []float64 `json:"temperature_2m_max"`
		TempMin       []float64 `json:"temperature_2m_min"`
		PrecipSum     []float64 `json:"precipitation_sum"`
		PrecipProbMax []float64 `json:"precipitation_probability_max"`
		WindSpeedMax  []float64 `json:"wind_speed_10m_max"`
		WeatherCode   []int     `json:"weather_code"`
	} `json:"daily"`
}

// GFSGetForecast fetches daily forecasts from GFS_seamless via Open-Meteo.
// Supports up to 16 days. Results are cached for gfsTTL.
func GFSGetForecast(city string, days int) ([]weather.Forecast, error) {
	if days < 1 {
		days = 1
	}
	if days > 16 {
		days = 16
	}

	cacheKey := fmt.Sprintf("%s_%d", city, days)
	if v, ok := gfsCache.Load(cacheKey); ok {
		entry := v.(gfsCacheEntry)
		if time.Since(entry.fetchedAt) < gfsTTL {
			return entry.forecasts, nil
		}
	}

	c, ok := weather.Cities[city]
	if !ok {
		return nil, fmt.Errorf("gfs: unknown city %q", city)
	}

	url := fmt.Sprintf(
		"https://api.open-meteo.com/v1/forecast"+
			"?latitude=%.4f&longitude=%.4f"+
			"&daily=temperature_2m_max,temperature_2m_min,precipitation_sum,"+
			"precipitation_probability_max,wind_speed_10m_max,weather_code"+
			"&models=gfs_seamless"+
			"&forecast_days=%d&timezone=UTC",
		c.Lat, c.Lon, days,
	)

	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("gfs: build request: %w", err)
	}
	req.Header.Set("User-Agent", "polymarket-weather-bot/1.0")

	resp, err := gfsClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("gfs: http: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("gfs: status %d: %s", resp.StatusCode, string(body))
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("gfs: read body: %w", err)
	}

	var r gfsResp
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, fmt.Errorf("gfs: parse: %w", err)
	}

	if len(r.Daily.Time) == 0 {
		return nil, fmt.Errorf("gfs: empty response for %s", city)
	}

	out := make([]weather.Forecast, 0, len(r.Daily.Time))
	for i, dateStr := range r.Daily.Time {
		if i >= len(r.Daily.TempMax) {
			break
		}
		var minT, precipMM, precipProb, windKMH float64
		var wcode int
		if i < len(r.Daily.TempMin) {
			minT = r.Daily.TempMin[i]
		}
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
		out = append(out, weather.Forecast{
			City:                     city,
			Date:                     dateStr,
			MaxTempC:                 r.Daily.TempMax[i],
			MinTempC:                 minT,
			PrecipitationMM:          precipMM,
			PrecipitationProbability: precipProb,
			WindSpeedKMH:             windKMH,
			WeatherCode:              wcode,
		})
	}

	if len(out) == 0 {
		return nil, fmt.Errorf("gfs: no usable data for %s", city)
	}

	gfsCache.Store(cacheKey, gfsCacheEntry{forecasts: out, fetchedAt: time.Now()})
	return out, nil
}

// GFSGet16DayForecast fetches the full 16-day GFS forecast for long-horizon markets.
// This is separate from GFSGetForecast to avoid polluting the standard 7-day cache.
func GFSGet16DayForecast(city string) ([]weather.Forecast, error) {
	return GFSGetForecast(city, 16)
}
