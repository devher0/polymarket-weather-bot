// ecmwf_aifs.go — ECMWF AIFS (AI Forecasting System) via Open-Meteo.
//
// TASK-091: Since October 2025 ECMWF has opened full public access to their
// IFS and AIFS (AI-based) model output. AIFS is ECMWF's machine-learning
// forecast system that outperforms classical NWP by 5–15% on standard metrics
// and is especially accurate for tropical cyclones and mid-latitude extremes.
//
// We fetch AIFS data through Open-Meteo's ECMWF model endpoint (model=ecmwf_aifs025),
// which exposes the same daily JSON schema used for other models — no GRIB2
// parsing required, no API key needed.
//
// Added as a 6th aggregator source with weight 0.25 (highest individual weight).
// Weights are re-normalised; static weights of other sources are reduced
// proportionally (see aggregator.go staticSourceWeights).
//
// Graceful fallback: ECMWF availability is not guaranteed; errors produce a
// warning log and the source is simply omitted from the fusion.
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

// ecmwfCacheEntry holds cached ECMWF AIFS forecasts per city+days key.
type ecmwfCacheEntry struct {
	forecasts []weather.Forecast
	fetchedAt time.Time
}

var (
	ecmwfCache  sync.Map
	ecmwfTTL    = 6 * time.Hour // ECMWF runs twice daily; 6h cache is safe
	ecmwfClient = &http.Client{Timeout: 20 * time.Second}
)

// ecmwfResp is the JSON envelope from the Open-Meteo ECMWF endpoint.
type ecmwfResp struct {
	Daily struct {
		Time            []string  `json:"time"`
		TempMax         []float64 `json:"temperature_2m_max"`
		TempMin         []float64 `json:"temperature_2m_min"`
		PrecipSum       []float64 `json:"precipitation_sum"`
		PrecipProbMax   []float64 `json:"precipitation_probability_max"`
		WindSpeedMax    []float64 `json:"wind_speed_10m_max"`
		WeatherCode     []int     `json:"weather_code"`
	} `json:"daily"`
}

// ECMWFGetForecast fetches daily forecasts for a city from the ECMWF AIFS model
// via Open-Meteo. Results are cached for ecmwfTTL.
func ECMWFGetForecast(city string, days int) ([]weather.Forecast, error) {
	cacheKey := fmt.Sprintf("%s_%d", city, days)
	if v, ok := ecmwfCache.Load(cacheKey); ok {
		entry := v.(ecmwfCacheEntry)
		if time.Since(entry.fetchedAt) < ecmwfTTL {
			return entry.forecasts, nil
		}
	}

	c, ok := weather.Cities[city]
	if !ok {
		return nil, fmt.Errorf("ecmwf: unknown city %q", city)
	}
	if days < 1 {
		days = 1
	}
	// ECMWF AIFS via Open-Meteo supports up to 10-day forecasts.
	if days > 10 {
		days = 10
	}

	// Try AIFS 0.25° model first; fall back to IFS 0.4° if unavailable.
	forecasts, err := ecmwfFetch(c.Lat, c.Lon, city, days, "ecmwf_aifs025")
	if err != nil || len(forecasts) == 0 {
		// Try IFS as fallback.
		forecasts, err = ecmwfFetch(c.Lat, c.Lon, city, days, "ecmwf_ifs025")
		if err != nil || len(forecasts) == 0 {
			return nil, fmt.Errorf("ecmwf: both aifs and ifs failed: %w", err)
		}
	}

	ecmwfCache.Store(cacheKey, ecmwfCacheEntry{forecasts: forecasts, fetchedAt: time.Now()})
	return forecasts, nil
}

// ecmwfFetch performs the actual HTTP request for a specific ECMWF model variant.
func ecmwfFetch(lat, lon float64, city string, days int, model string) ([]weather.Forecast, error) {
	url := fmt.Sprintf(
		"https://api.open-meteo.com/v1/forecast"+
			"?latitude=%.4f&longitude=%.4f"+
			"&daily=temperature_2m_max,temperature_2m_min,precipitation_sum,"+
			"precipitation_probability_max,wind_speed_10m_max,weather_code"+
			"&models=%s"+
			"&forecast_days=%d&timezone=UTC",
		lat, lon, model, days,
	)

	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("User-Agent", "polymarket-weather-bot/1.0")

	resp, err := ecmwfClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("http: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("status %d: %s", resp.StatusCode, string(body))
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}

	var r ecmwfResp
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, fmt.Errorf("parse: %w", err)
	}

	if len(r.Daily.Time) == 0 || len(r.Daily.TempMax) == 0 {
		return nil, fmt.Errorf("empty daily arrays in %s response", model)
	}

	out := make([]weather.Forecast, 0, len(r.Daily.Time))
	for i, dateStr := range r.Daily.Time {
		if i >= len(r.Daily.TempMax) {
			break
		}
		var (
			minT, precipMM, precipProb, windKMH float64
			wcode                               int
		)
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
		return nil, fmt.Errorf("no usable data for %s from %s", city, model)
	}
	return out, nil
}
