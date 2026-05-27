// Package weather fetches forecasts from Open-Meteo (free, no API key).
package weather

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

type Forecast struct {
	City                     string
	Date                     string
	MaxTempC                 float64
	MinTempC                 float64
	PrecipitationMM          float64
	PrecipitationProbability float64 // 0–100
	WindSpeedKMH             float64
	WeatherCode              int
	UVIndexMax               float64 // TASK-083: daily maximum UV index (0–12+); 0 if not available
	HumidityPct              float64 // TASK-084: relative humidity 0–100; populated from NASA POWER (RH2M) when available
	ApparentMaxTempC         float64 // TASK-084: apparent ("feels like") max temp; heat index when humid, wind chill when cold+windy
	CapeJkg                  float64 // TASK-089: convective available potential energy (J/kg); 0 if unavailable
}

type City struct {
	Lat float64
	Lon float64
}

var Cities = map[string]City{
	"new_york":      {40.71, -74.01},
	"london":        {51.51, -0.13},
	"tokyo":         {35.68, 139.69},
	"miami":         {25.77, -80.19},
	"paris":         {48.85, 2.35},
	"chicago":       {41.88, -87.63},
	"los_angeles":   {34.05, -118.24},
	"san_francisco": {37.77, -122.42},
	"berlin":        {52.52, 13.41},
}

type openMeteoResp struct {
	Daily struct {
		Time                        []string  `json:"time"`
		Temperature2MMax            []float64 `json:"temperature_2m_max"`
		Temperature2MMin            []float64 `json:"temperature_2m_min"`
		PrecipitationSum            []float64 `json:"precipitation_sum"`
		PrecipitationProbabilityMax []float64 `json:"precipitation_probability_max"`
		WindSpeed10MMax             []float64 `json:"wind_speed_10m_max"`
		WeatherCode                 []int     `json:"weather_code"`
		UVIndexMax                  []float64 `json:"uv_index_max"` // TASK-083
	} `json:"daily"`
}

var httpClient = &http.Client{Timeout: 15 * time.Second}

// GetForecast returns daily forecasts for the given city (up to `days` days).
func GetForecast(city string, days int) ([]Forecast, error) {
	c, ok := Cities[city]
	if !ok {
		return nil, fmt.Errorf("unknown city: %s", city)
	}

	url := fmt.Sprintf(
		"https://api.open-meteo.com/v1/forecast?latitude=%.4f&longitude=%.4f"+
			"&daily=temperature_2m_max,temperature_2m_min,precipitation_sum,"+
			"precipitation_probability_max,wind_speed_10m_max,weather_code,uv_index_max"+
			"&forecast_days=%d&timezone=UTC",
		c.Lat, c.Lon, days,
	)

	resp, err := httpClient.Get(url)
	if err != nil {
		return nil, fmt.Errorf("open-meteo request: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	var m openMeteoResp
	if err := json.Unmarshal(body, &m); err != nil {
		return nil, fmt.Errorf("open-meteo parse: %w", err)
	}

	out := make([]Forecast, 0, len(m.Daily.Time))
	for i, date := range m.Daily.Time {
		var uvMax float64
		if i < len(m.Daily.UVIndexMax) {
			uvMax = m.Daily.UVIndexMax[i]
		}
		out = append(out, Forecast{
			City:                    city,
			Date:                    date,
			MaxTempC:                m.Daily.Temperature2MMax[i],
			MinTempC:                m.Daily.Temperature2MMin[i],
			PrecipitationMM:         m.Daily.PrecipitationSum[i],
			PrecipitationProbability: m.Daily.PrecipitationProbabilityMax[i],
			WindSpeedKMH:            m.Daily.WindSpeed10MMax[i],
			WeatherCode:             m.Daily.WeatherCode[i],
			UVIndexMax:              uvMax,
		})
	}
	return out, nil
}

// RainProbability returns 0–1 probability of meaningful rain (>2 mm).
func RainProbability(f Forecast) float64 {
	p := f.PrecipitationProbability / 100
	switch {
	case f.PrecipitationMM > 10:
		return clamp(p+0.10, 0, 0.97)
	case f.PrecipitationMM > 2:
		return p
	default:
		return clamp(p-0.15, 0.03, 1)
	}
}

// HeatProbability returns 0–1 probability of max temp exceeding threshold.
func HeatProbability(f Forecast, thresholdC float64) float64 {
	diff := f.MaxTempC - thresholdC
	switch {
	case diff >= 3:
		return 0.93
	case diff >= 0:
		return clamp(0.70+diff*0.077, 0, 0.93)
	case diff >= -5:
		return clamp(0.50+diff*0.09, 0.05, 0.70)
	default:
		return 0.03
	}
}

// SunnyProbability returns 0-1 probability of a clear/sunny day.
// Uses WMO weather codes (0=clear, 1=mainly clear, 2=partly cloudy, 3=overcast)
// combined with precipitation probability to produce a calibrated estimate.
func SunnyProbability(f Forecast) float64 {
	rainPenalty := f.PrecipitationProbability / 100 * 0.4 // max 0.4 penalty
	switch {
	case f.WeatherCode == 0: // clear sky
		return clamp(0.93-rainPenalty, 0.60, 0.97)
	case f.WeatherCode == 1: // mainly clear
		return clamp(0.80-rainPenalty, 0.45, 0.90)
	case f.WeatherCode == 2: // partly cloudy
		return clamp(0.55-rainPenalty, 0.20, 0.70)
	case f.WeatherCode == 3: // overcast
		return clamp(0.20-rainPenalty, 0.03, 0.35)
	case f.WeatherCode >= 51 && f.WeatherCode <= 67: // drizzle/rain codes
		return clamp(0.05-rainPenalty*0.5, 0.01, 0.10)
	case f.WeatherCode >= 71 && f.WeatherCode <= 77: // snow
		return clamp(0.03, 0.01, 0.08)
	case f.WeatherCode >= 80: // showers / thunderstorms
		return clamp(0.04-rainPenalty*0.5, 0.01, 0.08)
	default:
		return clamp(0.30-rainPenalty, 0.05, 0.50)
	}
}

// UVProbability returns 0–1 probability that the daily maximum UV index will
// meet or exceed `threshold`. UV index scale: 0-2 low, 3-5 moderate, 6-7 high,
// 8-10 very high, 11+ extreme. Common thresholds: 6 (high), 8 (very high), 11 (extreme).
//
// Returns 0.10 when UVIndexMax == 0 (UV data not available — base rate for overcast days).
// TASK-083.
func UVProbability(f Forecast, threshold float64) float64 {
	if f.UVIndexMax <= 0 {
		// UV data unavailable — return base rate rather than 0 to avoid false confidence.
		return 0.10
	}
	if threshold <= 0 {
		threshold = 8 // default: "very high UV"
	}
	diff := f.UVIndexMax - threshold
	switch {
	case diff >= 2:
		return 0.93
	case diff >= 0:
		// Smoothly transition 0.70 → 0.93 as diff goes 0 → 2
		return clamp(0.70+diff*0.115, 0, 0.93)
	case diff >= -3:
		// Transition 0.30 → 0.70 as diff goes -3 → 0
		return clamp(0.70+diff*0.133, 0.05, 0.70)
	default:
		return 0.05
	}
}

// CAPEStormProbability returns a 0–1 probability of convective storm activity
// based on CAPE (Convective Available Potential Energy) in J/kg.
//
// CAPE thresholds (TASK-089):
//
//	< 500  J/kg → 0.05 (weak/negligible convective potential)
//	500–1500     → 0.25 (moderate — showers possible)
//	1500–3000    → 0.60 (high — thunderstorm likely)
//	> 3000 J/kg  → 0.90 (very high — severe thunderstorm/tornado risk)
//
// Returns 0 when cape == 0 (data unavailable).
func CAPEStormProbability(cape float64) float64 {
	switch {
	case cape <= 0:
		return 0 // no data
	case cape < 500:
		return 0.05
	case cape < 1500:
		return 0.05 + (cape-500)/1000.0*0.20 // 0.05→0.25
	case cape < 3000:
		return 0.25 + (cape-1500)/1500.0*0.35 // 0.25→0.60
	default:
		extra := min((cape-3000)/2000.0, 1.0) // cap at 5000 J/kg
		return 0.60 + extra*0.30              // 0.60→0.90
	}
}

func clamp(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
