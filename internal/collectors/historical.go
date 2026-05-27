// historical.go — Open-Meteo Historical Archive collector.
// Uses https://archive-api.open-meteo.com (free, no API key).
// Stores 90 days of data per city in data/historical/{city}.json.
package collectors

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/devher0/polymarket-weather-bot/internal/weather"
)

const (
	histBase    = "https://archive-api.open-meteo.com/v1/archive"
	histDays    = 90
	histDataDir = "data/historical"
)

// HistoricalRecord extends Forecast with the actual observed outcome.
type HistoricalRecord struct {
	weather.Forecast
	FetchedAt string `json:"fetched_at"`
}

type historicalFile struct {
	City      string             `json:"city"`
	FetchedAt string             `json:"fetched_at"`
	Records   []HistoricalRecord `json:"records"`
}

type histResp struct {
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

var histClient = &http.Client{Timeout: 30 * time.Second}

// CollectHistory downloads 90 days of historical weather data for all cities
// and saves them to data/historical/{city}.json.
// Designed for use with: go run ./cmd/bot --collect-history
func CollectHistory(dataRoot string) error {
	if dataRoot == "" {
		dataRoot = "."
	}
	dir := filepath.Join(dataRoot, histDataDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("historical: mkdir %s: %w", dir, err)
	}

	var lastErr error
	for city := range weather.Cities {
		if err := collectCityHistory(city, dir); err != nil {
			fmt.Fprintf(os.Stderr, "historical: %s: %v\n", city, err)
			lastErr = err
			continue
		}
	}
	return lastErr
}

// GetHistory loads historical records for a city from disk.
// Returns empty slice if no file exists yet.
func GetHistory(city, dataRoot string) ([]HistoricalRecord, error) {
	if dataRoot == "" {
		dataRoot = "."
	}
	path := filepath.Join(dataRoot, histDataDir, city+".json")

	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("historical: read %s: %w", path, err)
	}

	var f historicalFile
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("historical: parse %s: %w", path, err)
	}
	return f.Records, nil
}

func collectCityHistory(city, dir string) error {
	c, ok := weather.Cities[city]
	if !ok {
		return fmt.Errorf("unknown city %q", city)
	}

	end := time.Now().UTC().AddDate(0, 0, -1) // yesterday (archive lag)
	start := end.AddDate(0, 0, -(histDays - 1))

	url := fmt.Sprintf(
		"%s?latitude=%.4f&longitude=%.4f"+
			"&daily=temperature_2m_max,temperature_2m_min,precipitation_sum,"+
			"precipitation_probability_max,wind_speed_10m_max,weather_code"+
			"&start_date=%s&end_date=%s&timezone=UTC",
		histBase,
		c.Lat, c.Lon,
		start.Format("2006-01-02"),
		end.Format("2006-01-02"),
	)

	resp, err := histClient.Get(url)
	if err != nil {
		return fmt.Errorf("http get: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("status %d: %s", resp.StatusCode, string(body))
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read body: %w", err)
	}

	var r histResp
	if err := json.Unmarshal(body, &r); err != nil {
		return fmt.Errorf("parse: %w", err)
	}

	now := time.Now().UTC().Format(time.RFC3339)
	records := make([]HistoricalRecord, 0, len(r.Daily.Time))

	for i, date := range r.Daily.Time {
		fc := weather.Forecast{
			City: city,
			Date: date,
		}
		if i < len(r.Daily.Temperature2MMax) {
			fc.MaxTempC = r.Daily.Temperature2MMax[i]
		}
		if i < len(r.Daily.Temperature2MMin) {
			fc.MinTempC = r.Daily.Temperature2MMin[i]
		}
		if i < len(r.Daily.PrecipitationSum) {
			fc.PrecipitationMM = r.Daily.PrecipitationSum[i]
		}
		if i < len(r.Daily.PrecipitationProbabilityMax) {
			fc.PrecipitationProbability = r.Daily.PrecipitationProbabilityMax[i]
		}
		if i < len(r.Daily.WindSpeed10MMax) {
			fc.WindSpeedKMH = r.Daily.WindSpeed10MMax[i]
		}
		if i < len(r.Daily.WeatherCode) {
			fc.WeatherCode = r.Daily.WeatherCode[i]
		}
		records = append(records, HistoricalRecord{Forecast: fc, FetchedAt: now})
	}

	out := historicalFile{
		City:      city,
		FetchedAt: now,
		Records:   records,
	}

	data, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}

	path := filepath.Join(dir, city+".json")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}

	fmt.Printf("historical: saved %d records for %s → %s\n", len(records), city, path)
	return nil
}
