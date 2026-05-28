package collectors

import (
	"testing"
	"time"

	"github.com/devher0/polymarket-weather-bot/internal/weather"
)

func makeFusedAt(t time.Time) *FusedForecast {
	return &FusedForecast{
		Forecast:  weather.Forecast{},
		FetchedAt: t,
	}
}

func TestIsForecastStale_Fresh(t *testing.T) {
	ff := makeFusedAt(time.Now().Add(-30 * time.Minute))
	if IsForecastStale(ff, 3.0) {
		t.Error("forecast fetched 30m ago should not be stale with 3h limit")
	}
}

func TestIsForecastStale_ExactlyOnBoundary(t *testing.T) {
	// Fetched 1 second before the max-age boundary — must NOT be stale.
	// We subtract 1s of slack to avoid flakiness from execution time.
	maxH := 3.0
	ff := makeFusedAt(time.Now().Add(-time.Duration(maxH*float64(time.Hour)) + time.Second))
	if IsForecastStale(ff, maxH) {
		t.Error("forecast 1s before boundary should not be stale (uses strict >)")
	}
}

func TestIsForecastStale_Stale(t *testing.T) {
	ff := makeFusedAt(time.Now().Add(-4 * time.Hour))
	if !IsForecastStale(ff, 3.0) {
		t.Error("forecast fetched 4h ago should be stale with 3h limit")
	}
}

func TestIsForecastStale_DisabledWhenZero(t *testing.T) {
	ff := makeFusedAt(time.Now().Add(-100 * time.Hour))
	if IsForecastStale(ff, 0) {
		t.Error("maxAgeHours=0 should disable staleness checking")
	}
}

func TestIsForecastStale_NilForecast(t *testing.T) {
	if IsForecastStale(nil, 1.0) {
		t.Error("nil forecast should never be considered stale")
	}
}

func TestIsForecastStale_ZeroFetchedAt(t *testing.T) {
	ff := &FusedForecast{}
	if IsForecastStale(ff, 1.0) {
		t.Error("zero FetchedAt should not be considered stale")
	}
}

func TestForecastAge_ReturnsApproxAge(t *testing.T) {
	fetched := time.Now().Add(-2 * time.Hour)
	ff := makeFusedAt(fetched)
	age := ForecastAge(ff)
	if age < 115*time.Minute || age > 125*time.Minute {
		t.Errorf("expected ~2h age, got %v", age)
	}
}

func TestForecastAge_ZeroFetchedAt(t *testing.T) {
	ff := &FusedForecast{}
	if ForecastAge(ff) != 0 {
		t.Error("zero FetchedAt should return 0 age")
	}
}
