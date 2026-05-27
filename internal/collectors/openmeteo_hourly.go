// openmeteo_hourly.go — fetches hourly weather data for intraday accuracy.
//
// TASK-076: For same-day (dayOffset=0) and tomorrow (dayOffset=1) markets the
// daily aggregated max/precipitation values can be imprecise. Hourly data tells
// us *when* during the day it rains and what the actual peak temperature is,
// reducing the coarseness of daily rollups.
//
// The API is the same Open-Meteo endpoint used for daily data but with the
// &hourly= parameter instead of &daily=. No API key required.
package collectors

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"time"

	"github.com/devher0/polymarket-weather-bot/internal/weather"
)

// HourlyPoint holds weather data for a single hour.
type HourlyPoint struct {
	Time        time.Time
	TempC       float64
	PrecipMM    float64
	PrecipProb  float64 // 0–100 %
	WindKMH     float64
	CloudCover  float64 // 0–100 %
	WeatherCode int
}

// openMeteoHourlyResp is the JSON envelope returned by the Open-Meteo hourly API.
type openMeteoHourlyResp struct {
	Hourly struct {
		Time          []string  `json:"time"`
		Temperature2M []float64 `json:"temperature_2m"`
		Precipitation []float64 `json:"precipitation"`
		PrecipProb    []float64 `json:"precipitation_probability"`
		WindSpeed10M  []float64 `json:"wind_speed_10m"`
		CloudCover    []float64 `json:"cloud_cover"`
		WeatherCode   []int     `json:"weather_code"`
	} `json:"hourly"`
}

// hourlyHTTPClient is used exclusively for the hourly endpoint.
var hourlyHTTPClient = &http.Client{Timeout: 10 * time.Second}

// FetchHourlyForecast downloads up to `days` days (1–3) of hourly data for the
// given city from Open-Meteo. Returns points sorted by time (UTC).
func FetchHourlyForecast(city string, days int) ([]HourlyPoint, error) {
	c, ok := weather.Cities[city]
	if !ok {
		return nil, fmt.Errorf("openmeteo-hourly: unknown city %q", city)
	}
	if days < 1 {
		days = 1
	}
	if days > 3 {
		days = 3
	}

	url := fmt.Sprintf(
		"https://api.open-meteo.com/v1/forecast"+
			"?latitude=%.4f&longitude=%.4f"+
			"&hourly=temperature_2m,precipitation,precipitation_probability,"+
			"wind_speed_10m,cloud_cover,weather_code"+
			"&forecast_days=%d&timezone=UTC",
		c.Lat, c.Lon, days,
	)

	resp, err := hourlyHTTPClient.Get(url)
	if err != nil {
		return nil, fmt.Errorf("openmeteo-hourly: request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("openmeteo-hourly: HTTP %d for %s", resp.StatusCode, city)
	}

	body, _ := io.ReadAll(resp.Body)
	var m openMeteoHourlyResp
	if err := json.Unmarshal(body, &m); err != nil {
		return nil, fmt.Errorf("openmeteo-hourly: parse: %w", err)
	}

	n := len(m.Hourly.Time)
	if n == 0 {
		return nil, fmt.Errorf("openmeteo-hourly: empty response for %s", city)
	}

	safeFloat := func(sl []float64, i int) float64 {
		if i < len(sl) {
			return sl[i]
		}
		return 0
	}
	safeInt := func(sl []int, i int) int {
		if i < len(sl) {
			return sl[i]
		}
		return 0
	}

	points := make([]HourlyPoint, 0, n)
	for i := 0; i < n; i++ {
		t, err := time.Parse("2006-01-02T15:04", m.Hourly.Time[i])
		if err != nil {
			continue
		}
		points = append(points, HourlyPoint{
			Time:        t,
			TempC:       safeFloat(m.Hourly.Temperature2M, i),
			PrecipMM:    safeFloat(m.Hourly.Precipitation, i),
			PrecipProb:  safeFloat(m.Hourly.PrecipProb, i),
			WindKMH:     safeFloat(m.Hourly.WindSpeed10M, i),
			CloudCover:  safeFloat(m.Hourly.CloudCover, i),
			WeatherCode: safeInt(m.Hourly.WeatherCode, i),
		})
	}

	return points, nil
}

// FilterHourlyByDate returns only the HourlyPoints whose date matches targetDate
// (format "2006-01-02" UTC).
func FilterHourlyByDate(points []HourlyPoint, targetDate string) []HourlyPoint {
	out := make([]HourlyPoint, 0, 24)
	for _, p := range points {
		if p.Time.UTC().Format("2006-01-02") == targetDate {
			out = append(out, p)
		}
	}
	return out
}

// hourlyRainProbability computes the probability that it rains *at some point*
// during the given set of hours.
//
// The dominant signal is the per-hour precipitation-probability maximum
// (the API provides the probability that precipitation falls in a given hour).
// We boost that signal when hourly totals confirm real accumulation.
func hourlyRainProbability(points []HourlyPoint) float64 {
	if len(points) == 0 {
		return 0
	}
	maxProb := 0.0
	totalPrecip := 0.0
	for _, p := range points {
		if p.PrecipProb > maxProb {
			maxProb = p.PrecipProb
		}
		totalPrecip += p.PrecipMM
	}
	prob := maxProb / 100.0

	switch {
	case totalPrecip >= 5.0:
		prob = math.Min(1.0, prob+0.15)
	case totalPrecip >= 1.5:
		prob = math.Min(1.0, prob+0.05)
	}
	return prob
}

// hourlyMaxTemp returns the peak temperature across all hourly points.
func hourlyMaxTemp(points []HourlyPoint) float64 {
	max := math.Inf(-1)
	for _, p := range points {
		if p.TempC > max {
			max = p.TempC
		}
	}
	if math.IsInf(max, -1) {
		return 0
	}
	return max
}

// hourlyMinTemp returns the trough temperature across all hourly points.
func hourlyMinTemp(points []HourlyPoint) float64 {
	min := math.Inf(1)
	for _, p := range points {
		if p.TempC < min {
			min = p.TempC
		}
	}
	if math.IsInf(min, 1) {
		return 0
	}
	return min
}

// hourlyMaxWind returns the peak wind speed across all hourly points.
func hourlyMaxWind(points []HourlyPoint) float64 {
	max := 0.0
	for _, p := range points {
		if p.WindKMH > max {
			max = p.WindKMH
		}
	}
	return max
}

// hourlyTotalPrecip returns the total precipitation in mm.
func hourlyTotalPrecip(points []HourlyPoint) float64 {
	total := 0.0
	for _, p := range points {
		total += p.PrecipMM
	}
	return total
}

// RefineWithHourly overwrites key fields in a FusedForecast with higher-accuracy
// intraday values derived from hourly data.
//
// Daily rollups (max/min temperature, precipitation probability, wind) are
// replaced with values computed directly from the hourly time series. This is
// particularly important for:
//   - Rain probability on same-day markets: hourly gives the "at-some-point" probability.
//   - Peak temperature: hourly captures the true diurnal maximum vs a smoothed daily value.
//   - Wind gusts: hourly max is more relevant to "will it be windy?" questions.
//
// Side effects:
//   - "hourly" is appended to ff.Sources.
//   - ff.Confidence is boosted by +0.05 (richer granularity = more reliable signal).
func RefineWithHourly(ff *FusedForecast, points []HourlyPoint) {
	if ff == nil || len(points) == 0 {
		return
	}

	newMaxTemp := hourlyMaxTemp(points)
	newMinTemp := hourlyMinTemp(points)
	newRainP := hourlyRainProbability(points)
	newPrecip := hourlyTotalPrecip(points)
	newWind := hourlyMaxWind(points)

	slog.Info("hourly refinement",
		"city", ff.City,
		"max_temp", fmt.Sprintf("%.1f→%.1f°C", ff.MaxTempC, newMaxTemp),
		"rain_prob", fmt.Sprintf("%.0f→%.0f%%", ff.PrecipitationProbability, newRainP*100),
		"precip", fmt.Sprintf("%.1f→%.1fmm", ff.PrecipitationMM, newPrecip),
		"wind", fmt.Sprintf("%.1f→%.1fkm/h", ff.WindSpeedKMH, newWind),
		"hourly_pts", len(points),
	)

	ff.MaxTempC = newMaxTemp
	ff.MinTempC = newMinTemp
	ff.PrecipitationProbability = newRainP * 100
	ff.PrecipitationMM = newPrecip
	ff.WindSpeedKMH = newWind
	ff.Sources = append(ff.Sources, "hourly")
	ff.Confidence = math.Min(1.0, ff.Confidence+0.05)
}
