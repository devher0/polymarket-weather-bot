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
	City                    string
	Date                    string
	MaxTempC                float64
	MinTempC                float64
	PrecipitationMM         float64
	PrecipitationProbability float64 // 0–100
	WindSpeedKMH            float64
	WeatherCode             int
}

type City struct {
	Lat float64
	Lon float64
}

var Cities = map[string]City{
	"new_york": {40.71, -74.01},
	"london":   {51.51, -0.13},
	"tokyo":    {35.68, 139.69},
	"miami":    {25.77, -80.19},
	"paris":    {48.85, 2.35},
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
			"precipitation_probability_max,wind_speed_10m_max,weather_code"+
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
		out = append(out, Forecast{
			City:                    city,
			Date:                    date,
			MaxTempC:                m.Daily.Temperature2MMax[i],
			MinTempC:                m.Daily.Temperature2MMin[i],
			PrecipitationMM:         m.Daily.PrecipitationSum[i],
			PrecipitationProbability: m.Daily.PrecipitationProbabilityMax[i],
			WindSpeedKMH:            m.Daily.WindSpeed10MMax[i],
			WeatherCode:             m.Daily.WeatherCode[i],
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

func clamp(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
