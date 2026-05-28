// forecast_command_test.go — TASK-138: unit tests for handleForecast.
package notifier

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/devher0/polymarket-weather-bot/internal/collectors"
	"github.com/devher0/polymarket-weather-bot/internal/weather"
)

func TestForecastMsg_InvalidCity(t *testing.T) {
	bcfg := BotConfig{DataRoot: t.TempDir()}
	msg := handleForecast(bcfg, "atlantis")
	if !strings.Contains(msg, "Unknown city") {
		t.Fatalf("expected unknown city error, got: %s", msg)
	}
}

func TestForecastMsg_OneCity_FromCache(t *testing.T) {
	dataRoot := t.TempDir()
	// Write a forecast cache entry for "new_york".
	ff := &collectors.FusedForecast{
		Forecast: weather.Forecast{
			City:                     "new_york",
			Date:                     time.Now().Format("2006-01-02"),
			MaxTempC:                 28.5,
			MinTempC:                 18.0,
			PrecipitationMM:          2.3,
			PrecipitationProbability: 35,
			WindSpeedKMH:             22,
		},
		Confidence: 0.72,
		Sources:    []string{"openmeteo", "nasa"},
		FetchedAt:  time.Now().Add(-30 * time.Minute),
	}
	if err := collectors.SaveForecastCache("new_york", 0, ff, dataRoot); err != nil {
		t.Fatalf("SaveForecastCache: %v", err)
	}

	bcfg := BotConfig{DataRoot: dataRoot}
	msg := handleForecast(bcfg, "new_york")

	if !strings.Contains(msg, "new_york") {
		t.Errorf("expected city name in response")
	}
	if !strings.Contains(msg, "28.5") {
		t.Errorf("expected MaxTemp in response")
	}
	if !strings.Contains(msg, "72%") {
		t.Errorf("expected confidence in response, got: %s", msg)
	}
}

func TestForecastMsg_AllCities_NoCacheReturnsNoData(t *testing.T) {
	// Use an empty dir so no cache exists and OpenMeteo won't be called in tests.
	// We expect the function to handle gracefully (may show "n/a" rows or error).
	dataRoot := filepath.Join(t.TempDir(), "nodata")
	_ = os.MkdirAll(dataRoot, 0o755)

	bcfg := BotConfig{DataRoot: dataRoot}
	msg := handleForecast(bcfg, "") // empty city = summary of default cities

	// Must not panic and must return something.
	if msg == "" {
		t.Fatal("expected non-empty response for all-cities summary")
	}
}

func TestFormatAge(t *testing.T) {
	cases := []struct {
		d    time.Duration
		want string
	}{
		{30 * time.Second, "just now"},
		{5 * time.Minute, "5m"},
		{90 * time.Minute, "1.5h"},
	}
	for _, tc := range cases {
		got := formatAge(tc.d)
		if got != tc.want {
			t.Errorf("formatAge(%v) = %q, want %q", tc.d, got, tc.want)
		}
	}
}

func TestForecastAlertEmoji(t *testing.T) {
	if forecastAlertEmoji(3) != "🔴" {
		t.Error("level 3 should be red")
	}
	if forecastAlertEmoji(2) != "🟡" {
		t.Error("level 2 should be yellow")
	}
	if forecastAlertEmoji(1) != "🔵" {
		t.Error("level 1 should be blue")
	}
}
