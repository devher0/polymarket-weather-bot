// Package collectors provides weather data from multiple sources.
// nasa_power.go — NASA POWER API (free, no API key, global coverage).
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

const nasaPowerBase = "https://power.larc.nasa.gov/api/temporal/daily/point"

// nasaCacheEntry holds a cached result with expiry.
type nasaCacheEntry struct {
	forecasts []weather.Forecast
	fetchedAt time.Time
}

var (
	nasaCache  sync.Map // key: "city_days" → nasaCacheEntry
	nasaTTL    = 6 * time.Hour
	nasaClient = &http.Client{Timeout: 30 * time.Second}
)

// nasaPowerResp represents the NASA POWER JSON response structure.
type nasaPowerResp struct {
	Properties struct {
		Parameter map[string]map[string]float64 `json:"parameter"`
	} `json:"properties"`
}

// NASAGetForecast fetches next-`days` days of forecast from NASA POWER API.
// Results are cached for 6 hours per city.
func NASAGetForecast(city string, days int) ([]weather.Forecast, error) {
	cacheKey := fmt.Sprintf("%s_%d", city, days)

	// Check cache
	if v, ok := nasaCache.Load(cacheKey); ok {
		entry := v.(nasaCacheEntry)
		if time.Since(entry.fetchedAt) < nasaTTL {
			return entry.forecasts, nil
		}
	}

	c, ok := weather.Cities[city]
	if !ok {
		return nil, fmt.Errorf("nasa_power: unknown city %q", city)
	}

	today := time.Now().UTC()
	end := today.AddDate(0, 0, days-1)

	startStr := today.Format("20060102")
	endStr := end.Format("20060102")

	url := fmt.Sprintf(
		"%s?parameters=T2M,T2M_MAX,T2M_MIN,PRECTOTCORR,WS2M,RH2M&community=RE"+
			"&longitude=%.4f&latitude=%.4f&start=%s&end=%s&format=JSON",
		nasaPowerBase,
		c.Lon, c.Lat,
		startStr, endStr,
	)

	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("nasa_power: build request: %w", err)
	}
	req.Header.Set("User-Agent", "polymarket-weather-bot/1.0")

	resp, err := nasaClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("nasa_power: http: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("nasa_power: status %d: %s", resp.StatusCode, string(body))
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("nasa_power: read body: %w", err)
	}

	var r nasaPowerResp
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, fmt.Errorf("nasa_power: parse: %w", err)
	}

	t2mMax := r.Properties.Parameter["T2M_MAX"]
	t2mMin := r.Properties.Parameter["T2M_MIN"]
	precip := r.Properties.Parameter["PRECTOTCORR"]
	wind := r.Properties.Parameter["WS2M"]
	rh := r.Properties.Parameter["RH2M"]

	if t2mMax == nil || t2mMin == nil {
		return nil, fmt.Errorf("nasa_power: missing T2M_MAX/T2M_MIN parameters in response")
	}

	out := make([]weather.Forecast, 0, days)
	for i := 0; i < days; i++ {
		date := today.AddDate(0, 0, i)
		key := date.Format("20060102")

		maxT, okMax := t2mMax[key]
		minT, okMin := t2mMin[key]
		if !okMax || !okMin {
			continue
		}

		var precipMM float64
		if precip != nil {
			precipMM = precip[key]
			if precipMM < 0 {
				precipMM = 0 // NASA uses -999 for missing
			}
		}

		var windMS float64
		if wind != nil {
			windMS = wind[key]
			if windMS < 0 {
				windMS = 0
			}
		}

		var rhPct float64
		if rh != nil {
			rhPct = rh[key]
			if rhPct < 0 {
				rhPct = 0
			}
		}

		// NASA POWER wind is in m/s; convert to km/h
		windKMH := windMS * 3.6

		// Estimate precipitation probability from RH + precip amount
		precipProb := estimatePrecipProb(precipMM, rhPct)

		out = append(out, weather.Forecast{
			City:                     city,
			Date:                     date.Format("2006-01-02"),
			MaxTempC:                 maxT,
			MinTempC:                 minT,
			PrecipitationMM:          precipMM,
			PrecipitationProbability: precipProb,
			WindSpeedKMH:             windKMH,
			WeatherCode:              nasaWeatherCode(precipMM, maxT, windKMH),
			HumidityPct:              rhPct, // TASK-084: RH2M from NASA POWER
		})
	}

	if len(out) == 0 {
		return nil, fmt.Errorf("nasa_power: no data returned for %s [%s-%s]", city, startStr, endStr)
	}

	// Store in cache
	nasaCache.Store(cacheKey, nasaCacheEntry{forecasts: out, fetchedAt: time.Now()})
	return out, nil
}

// estimatePrecipProb estimates precipitation probability (0-100) from
// precipitation amount and relative humidity, since NASA POWER doesn't
// provide direct probability.
func estimatePrecipProb(precipMM, rhPct float64) float64 {
	switch {
	case precipMM > 10:
		return 90
	case precipMM > 5:
		return 80
	case precipMM > 2:
		return 70
	case precipMM > 0.5:
		return 55
	case precipMM > 0:
		return 40
	case rhPct > 90:
		return 30
	case rhPct > 80:
		return 15
	default:
		return 5
	}
}

// nasaWeatherCode converts NASA POWER parameters to an approximate WMO weather code.
func nasaWeatherCode(precipMM, maxTempC, windKMH float64) int {
	switch {
	case maxTempC < 0 && precipMM > 1:
		return 73 // moderate snowfall
	case precipMM > 10:
		return 63 // moderate rain
	case precipMM > 2:
		return 51 // light drizzle/rain
	case precipMM > 0:
		return 45 // fog/light precip
	case windKMH > 60:
		return 65 // strong wind proxy
	default:
		return 0 // clear
	}
}
