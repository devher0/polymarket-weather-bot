//go:build integration

// Package collectors_integration_test provides smoke tests that make real HTTP
// requests to upstream weather APIs. These tests are NOT run as part of the
// normal test suite (go test ./...) — they require the `integration` build tag:
//
//	go test -tags=integration -timeout=60s ./internal/collectors/
//
// The tests verify that each upstream API is alive and returns plausible data.
// They are intentionally minimal: a single request, a non-nil / non-empty check,
// and a couple of sanity assertions on the response values.
package collectors

import (
	"testing"
)

// TestSmokeOpenMeteo verifies that the Open-Meteo API returns at least one
// forecast for Berlin (a well-supported mid-latitude city).
func TestSmokeOpenMeteo(t *testing.T) {
	t.Log("🌐 [smoke] Open-Meteo — fetching forecast for berlin")

	// The primary OpenMeteo path is weather.GetForecast; but it lives in the
	// weather package. We exercise it indirectly via the aggregator's goroutine
	// for OpenMeteo by calling Aggregate for a known city and checking the result.
	ff, err := Aggregate("berlin", ".")
	if err != nil {
		t.Fatalf("Aggregate(berlin): unexpected error: %v", err)
	}
	if ff == nil {
		t.Fatal("Aggregate(berlin): returned nil FusedForecast")
	}
	if ff.Forecast.MaxTempC == 0 && ff.Forecast.MinTempC == 0 {
		t.Error("Aggregate(berlin): both MaxTempC and MinTempC are 0 — likely bad data")
	}
	if ff.Forecast.Date == "" || ff.Forecast.Date == "unknown" {
		t.Errorf("Aggregate(berlin): forecast date is empty/unknown: %q", ff.Forecast.Date)
	}
	t.Logf("  date=%s  maxT=%.1f°C  minT=%.1f°C  precip=%.1fmm  conf=%.2f  sources=%v",
		ff.Forecast.Date,
		ff.Forecast.MaxTempC,
		ff.Forecast.MinTempC,
		ff.Forecast.PrecipitationMM,
		ff.Confidence,
		ff.Sources,
	)
}

// TestSmokeNASAPower verifies that the NASA POWER API returns a non-nil forecast
// for a single city. NASA POWER is global, so tokyo is a good non-US test.
func TestSmokeNASAPower(t *testing.T) {
	t.Log("🌐 [smoke] NASA POWER — fetching forecast for tokyo")

	forecasts, err := NASAGetForecast("tokyo", 3)
	if err != nil {
		t.Fatalf("NASAGetForecast(tokyo, 3): unexpected error: %v", err)
	}
	if len(forecasts) == 0 {
		t.Fatal("NASAGetForecast(tokyo, 3): returned empty forecast slice")
	}

	// Check the first day for plausible values.
	f := forecasts[0]
	if f.City == "" {
		t.Error("NASA forecast: City field is empty")
	}
	if f.Date == "" {
		t.Error("NASA forecast: Date field is empty")
	}
	// Tokyo max temperature should be within a broadly sane range (-10°C to +50°C).
	if f.MaxTempC < -10 || f.MaxTempC > 50 {
		t.Errorf("NASA forecast: suspicious MaxTempC=%.1f for tokyo", f.MaxTempC)
	}
	t.Logf("  days=%d  [0] date=%s  maxT=%.1f°C  precip=%.1fmm  wind=%.0f km/h",
		len(forecasts),
		f.Date,
		f.MaxTempC,
		f.PrecipitationMM,
		f.WindSpeedKMH,
	)
}

// TestSmokeNOAANWS verifies the NWS API for new_york (the only mandatory US city).
// The NWS chain: /points/{lat,lon} → /gridpoints/{office}/{x,y}/forecast.
func TestSmokeNOAANWS(t *testing.T) {
	t.Log("🌐 [smoke] NOAA NWS — fetching forecast for new_york")

	forecasts, err := NOAAGetForecast("new_york", 3)
	if err != nil {
		t.Fatalf("NOAAGetForecast(new_york, 3): unexpected error: %v", err)
	}
	if len(forecasts) == 0 {
		t.Fatal("NOAAGetForecast(new_york, 3): returned empty forecast slice")
	}

	// At least 1 forecast period must come back.
	f := forecasts[0]
	if f.City == "" {
		t.Error("NOAA forecast: City field is empty")
	}
	// NYC temperatures: plausible range -20°C to 45°C.
	if f.MaxTempC < -20 || f.MaxTempC > 45 {
		t.Errorf("NOAA forecast: suspicious MaxTempC=%.1f for new_york", f.MaxTempC)
	}
	t.Logf("  periods=%d  [0] date=%s  maxT=%.1f°C  precipP=%.0f%%",
		len(forecasts),
		f.Date,
		f.MaxTempC,
		f.PrecipitationProbability,
	)
}

// TestSmokeNOAANWSNonUS ensures that NOAAGetForecast correctly rejects a non-US
// city rather than silently returning empty/wrong data.
func TestSmokeNOAANWSNonUS(t *testing.T) {
	t.Log("🌐 [smoke] NOAA NWS — verifying rejection of non-US city (berlin)")

	_, err := NOAAGetForecast("berlin", 1)
	if err == nil {
		t.Fatal("NOAAGetForecast(berlin, 1): expected an error for non-US city, got nil")
	}
	t.Logf("  got expected error: %v", err)
}

// TestSmokeEnsemble verifies that the Open-Meteo Ensemble endpoint is reachable
// and returns a valid EnsembleResult for paris.
func TestSmokeEnsemble(t *testing.T) {
	t.Log("🌐 [smoke] Open-Meteo Ensemble — fetching for paris (day 0)")

	er, err := GetEnsembleForecast("paris", 0)
	if err != nil {
		t.Fatalf("GetEnsembleForecast(paris, 0): unexpected error: %v", err)
	}
	if er == nil {
		t.Fatal("GetEnsembleForecast(paris, 0): returned nil EnsembleResult")
	}
	if er.MemberCount == 0 {
		t.Error("EnsembleResult: MemberCount=0, no ensemble members returned")
	}
	if er.TempStdDev < 0 {
		t.Errorf("EnsembleResult: negative TempStdDev=%.2f", er.TempStdDev)
	}
	t.Logf("  members=%d  tempStdDev=%.2f°C  precipStdDev=%.2fmm  confidence=%.2f",
		er.MemberCount,
		er.TempStdDev,
		er.PrecipStdDev,
		EnsembleToConfidence(er.TempStdDev),
	)
}

// TestSmokeAggregateAll verifies that AggregateAll returns at least 3 cities
// (not all sources are global, but OpenMeteo should always succeed).
func TestSmokeAggregateAll(t *testing.T) {
	t.Log("🌐 [smoke] AggregateAll — fetching all cities concurrently")

	results, err := AggregateAll(".")
	if err != nil && len(results) == 0 {
		t.Fatalf("AggregateAll: total failure: %v", err)
	}
	if len(results) < 3 {
		t.Errorf("AggregateAll: only %d cities succeeded (expected ≥3)", len(results))
	}
	t.Logf("  cities succeeded: %d", len(results))
	for city, ff := range results {
		t.Logf("    %-20s  maxT=%.1f°C  conf=%.2f  sources=%v",
			city, ff.Forecast.MaxTempC, ff.Confidence, ff.Sources)
	}
}
