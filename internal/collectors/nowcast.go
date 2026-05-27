// nowcast.go — 2–6 hour precipitation/temperature nowcast via Open-Meteo minutely_15.
// TASK-106: Daily forecasts lack precision for markets expiring today.
// minutely_15 provides 15-minute slots up to 48 hours ahead.
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

// NowcastSummary holds aggregated nowcast data for the next N minutes.
type NowcastSummary struct {
	City            string
	WindowMinutes   int
	RainProbability float64 // 0-1: fraction of 15-min slots with precipitation > 0.1 mm
	AvgTempC        float64
	MaxWindKMH      float64
	PrecipMM        float64 // total precipitation in the window
	FetchedAt       time.Time
}

var nowcastCache struct {
	mu      sync.Mutex
	entries map[string]nowcastCacheEntry
}

type nowcastCacheEntry struct {
	summary   NowcastSummary
	expiresAt time.Time
}

func init() {
	nowcastCache.entries = make(map[string]nowcastCacheEntry)
}

var nowcastHTTPClient = &http.Client{Timeout: 15 * time.Second}

type minutely15Resp struct {
	Minutely15 struct {
		Time          []string  `json:"time"`
		Temperature2M []float64 `json:"temperature_2m"`
		Precipitation []float64 `json:"precipitation"`
		WindSpeed10M  []float64 `json:"wind_speed_10m"`
	} `json:"minutely_15"`
}

// GetNowcast fetches Open-Meteo minutely_15 data and aggregates the next
// windowMinutes minutes of precipitation, temperature, and wind.
// Results are cached for 10 minutes.
func GetNowcast(city string, windowMinutes int) (NowcastSummary, error) {
	cacheKey := fmt.Sprintf("%s:%d", city, windowMinutes)

	nowcastCache.mu.Lock()
	if e, ok := nowcastCache.entries[cacheKey]; ok && time.Now().Before(e.expiresAt) {
		nowcastCache.mu.Unlock()
		return e.summary, nil
	}
	nowcastCache.mu.Unlock()

	c, ok := weather.Cities[city]
	if !ok {
		return NowcastSummary{}, fmt.Errorf("nowcast: unknown city %s", city)
	}

	url := fmt.Sprintf(
		"https://api.open-meteo.com/v1/forecast?latitude=%.4f&longitude=%.4f"+
			"&minutely_15=temperature_2m,precipitation,wind_speed_10m"+
			"&forecast_minutely_15=48&timezone=UTC",
		c.Lat, c.Lon,
	)

	resp, err := nowcastHTTPClient.Get(url)
	if err != nil {
		return NowcastSummary{}, fmt.Errorf("nowcast fetch %s: %w", city, err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	var m minutely15Resp
	if err := json.Unmarshal(body, &m); err != nil {
		return NowcastSummary{}, fmt.Errorf("nowcast parse %s: %w", city, err)
	}

	summary := buildNowcastSummary(city, windowMinutes, m)
	summary.FetchedAt = time.Now()

	nowcastCache.mu.Lock()
	nowcastCache.entries[cacheKey] = nowcastCacheEntry{
		summary:   summary,
		expiresAt: time.Now().Add(10 * time.Minute),
	}
	nowcastCache.mu.Unlock()

	return summary, nil
}

func buildNowcastSummary(city string, windowMinutes int, m minutely15Resp) NowcastSummary {
	now := time.Now().UTC()
	windowEnd := now.Add(time.Duration(windowMinutes) * time.Minute)

	var sumTemp, sumPrecip, maxWind float64
	var count, rainSlots int

	for i, ts := range m.Minutely15.Time {
		t, err := time.Parse("2006-01-02T15:04", ts)
		if err != nil {
			continue
		}
		if t.Before(now) || t.After(windowEnd) {
			continue
		}

		temp := safeIdx(m.Minutely15.Temperature2M, i)
		precip := safeIdx(m.Minutely15.Precipitation, i)
		wind := safeIdx(m.Minutely15.WindSpeed10M, i)

		sumTemp += temp
		sumPrecip += precip
		if wind > maxWind {
			maxWind = wind
		}
		if precip > 0.1 {
			rainSlots++
		}
		count++
	}

	s := NowcastSummary{
		City:          city,
		WindowMinutes: windowMinutes,
		MaxWindKMH:    maxWind,
		PrecipMM:      sumPrecip,
	}
	if count > 0 {
		s.AvgTempC = sumTemp / float64(count)
		s.RainProbability = float64(rainSlots) / float64(count)
	}
	return s
}

// NowcastRainProbability returns 0–1 probability of meaningful rain in the
// next `minutes` minutes for `city`. Falls back to 0.10 on fetch error.
func NowcastRainProbability(city string, minutes int) float64 {
	s, err := GetNowcast(city, minutes)
	if err != nil {
		return 0.10
	}
	// Blend slot frequency with accumulated precip signal.
	precipBoost := 0.0
	if s.PrecipMM > 2.0 {
		precipBoost = 0.15
	} else if s.PrecipMM > 0.5 {
		precipBoost = 0.08
	}
	p := s.RainProbability + precipBoost
	if p < 0.01 {
		return 0.01
	}
	if p > 0.99 {
		return 0.99
	}
	return p
}
