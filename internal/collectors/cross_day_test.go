package collectors

import (
	"os"
	"testing"
	"time"

	"github.com/devher0/polymarket-weather-bot/internal/weather"
)

// saveCachedFF is a test helper that writes a minimal FusedForecast to the
// disk cache so that LoadForecastCache can find it in subsequent calls.
func saveCachedFF(t *testing.T, city string, dayOffset int, maxTempC, precipMM, precipProb, windKMH float64, dataRoot string) {
	t.Helper()
	ff := &FusedForecast{
		Forecast: weather.Forecast{
			City:                     city,
			Date:                     time.Now().UTC().AddDate(0, 0, dayOffset).Format("2006-01-02"),
			MaxTempC:                 maxTempC,
			MinTempC:                 maxTempC - 8,
			PrecipitationMM:          precipMM,
			PrecipitationProbability: precipProb,
			WindSpeedKMH:             windKMH,
			WeatherCode:              0,
		},
		Confidence: 0.70,
		Sources:    []string{"openmeteo"},
		FetchedAt:  time.Now(),
	}
	if err := SaveForecastCache(city, dayOffset, ff, dataRoot); err != nil {
		t.Fatalf("saveCachedFF: %v", err)
	}
}

// TestCheckCrossDay_FullAgreement verifies that 3/3 days agreeing on rain
// yields the maximum boost (+0.08) and AgreementFraction == 1.0.
func TestCheckCrossDay_FullAgreement(t *testing.T) {
	dir := t.TempDir()
	city := "test_city"

	// All three days have high rain probability (> 0.5).
	for d := 0; d < 3; d++ {
		saveCachedFF(t, city, d, 18.0, 10.0, 80.0, 20.0, dir)
	}

	res := CheckCrossDay(city, "rain", 0, 0, dir)

	if res.DaysChecked < 2 {
		t.Fatalf("expected DaysChecked >= 2, got %d", res.DaysChecked)
	}
	if res.AgreementFraction != 1.0 {
		t.Errorf("expected AgreementFraction=1.0, got %.2f", res.AgreementFraction)
	}
	if res.ConfidenceBoost != 0.08 {
		t.Errorf("expected ConfidenceBoost=0.08, got %.2f", res.ConfidenceBoost)
	}
}

// TestCheckCrossDay_PartialAgreement verifies 2/3 days agreeing yields +0.04.
func TestCheckCrossDay_PartialAgreement(t *testing.T) {
	dir := t.TempDir()
	city := "test_city"

	// Day 0 and 1: rain. Day 2: no rain.
	saveCachedFF(t, city, 0, 18.0, 10.0, 80.0, 20.0, dir)
	saveCachedFF(t, city, 1, 18.0, 8.0, 70.0, 25.0, dir)
	saveCachedFF(t, city, 2, 30.0, 0.0, 10.0, 15.0, dir) // low precip → rain signal off

	res := CheckCrossDay(city, "rain", 0, 0, dir)

	if res.DaysChecked < 3 {
		t.Fatalf("expected DaysChecked=3, got %d", res.DaysChecked)
	}
	expected := 2.0 / 3.0
	if res.AgreementFraction < expected-0.01 || res.AgreementFraction > expected+0.01 {
		t.Errorf("expected AgreementFraction ≈ %.4f, got %.4f", expected, res.AgreementFraction)
	}
	if res.ConfidenceBoost != 0.04 {
		t.Errorf("expected ConfidenceBoost=0.04, got %.2f", res.ConfidenceBoost)
	}
}

// TestCheckCrossDay_NoAgreement verifies opposing signals yield zero boost.
func TestCheckCrossDay_NoAgreement(t *testing.T) {
	dir := t.TempDir()
	city := "test_city"

	// Day 0: rain. Day 1: no rain. (only 2 days → 1/2 = 0.5 < 0.67)
	saveCachedFF(t, city, 0, 18.0, 10.0, 80.0, 20.0, dir)
	saveCachedFF(t, city, 1, 30.0, 0.0, 10.0, 15.0, dir)

	res := CheckCrossDay(city, "rain", 0, 0, dir)

	if res.ConfidenceBoost != 0.00 {
		t.Errorf("expected zero boost, got %.2f", res.ConfidenceBoost)
	}
}

// TestCheckCrossDay_NoCache verifies graceful return when no cache exists.
func TestCheckCrossDay_NoCache(t *testing.T) {
	dir := t.TempDir()
	res := CheckCrossDay("ghost_city", "rain", 0, 0, dir)
	if res == nil {
		t.Fatal("expected non-nil result even for cache miss")
	}
	if res.DaysChecked != 0 {
		t.Errorf("expected DaysChecked=0 when no cache, got %d", res.DaysChecked)
	}
	if res.ConfidenceBoost != 0 {
		t.Errorf("expected zero boost for cache miss, got %.2f", res.ConfidenceBoost)
	}
}

// TestCheckCrossDay_OnlyTargetDay verifies that a single cached day yields no boost.
func TestCheckCrossDay_OnlyTargetDay(t *testing.T) {
	dir := t.TempDir()
	city := "lone_city"
	saveCachedFF(t, city, 0, 18.0, 10.0, 80.0, 20.0, dir)

	res := CheckCrossDay(city, "rain", 0, 0, dir)

	if res.DaysChecked != 1 {
		t.Errorf("expected DaysChecked=1, got %d", res.DaysChecked)
	}
	if res.ConfidenceBoost != 0.00 {
		t.Errorf("expected zero boost for single day, got %.2f", res.ConfidenceBoost)
	}
}

// TestCheckCrossDay_HeatSignal verifies the heat signal path (uses MaxTempC).
func TestCheckCrossDay_HeatSignal(t *testing.T) {
	dir := t.TempDir()
	city := "hot_city"
	// All three days very hot → heat signal fires consistently.
	for d := 0; d < 3; d++ {
		saveCachedFF(t, city, d, 42.0, 0.0, 5.0, 10.0, dir)
	}

	res := CheckCrossDay(city, "heat", 0, 38.0, dir)

	if res.AgreementFraction != 1.0 {
		t.Errorf("expected full agreement for hot days, got %.2f", res.AgreementFraction)
	}
	if res.ConfidenceBoost != 0.08 {
		t.Errorf("expected +0.08 boost, got %.2f", res.ConfidenceBoost)
	}
}

// TestApplyCrossDay_BoostApplied verifies that ApplyCrossDay updates Confidence
// and sets CrossDayScore.
func TestApplyCrossDay_BoostApplied(t *testing.T) {
	ff := &FusedForecast{Confidence: 0.60}
	res := &CrossDayResult{
		City:              "test_city",
		Signal:            "rain",
		DaysChecked:       3,
		DaysConsistent:    3,
		AgreementFraction: 1.0,
		ConfidenceBoost:   0.08,
	}
	ApplyCrossDay(ff, res)

	if ff.Confidence < 0.679 || ff.Confidence > 0.681 {
		t.Errorf("expected Confidence ≈ 0.68, got %.4f", ff.Confidence)
	}
	if ff.CrossDayScore != 1.0 {
		t.Errorf("expected CrossDayScore=1.0, got %.2f", ff.CrossDayScore)
	}
	found := false
	for _, s := range ff.Sources {
		if s == "cross_day" {
			found = true
		}
	}
	if !found {
		t.Error("expected 'cross_day' in Sources")
	}
}

// TestApplyCrossDay_CapAt097 verifies the confidence cap of 0.97.
func TestApplyCrossDay_CapAt097(t *testing.T) {
	ff := &FusedForecast{Confidence: 0.94}
	res := &CrossDayResult{AgreementFraction: 1.0, ConfidenceBoost: 0.08, City: "c", Signal: "rain"}
	ApplyCrossDay(ff, res)
	if ff.Confidence > 0.97 {
		t.Errorf("expected confidence capped at 0.97, got %.4f", ff.Confidence)
	}
}

// TestApplyCrossDay_Noop verifies that zero-boost results do not modify ff.
func TestApplyCrossDay_Noop(t *testing.T) {
	ff := &FusedForecast{Confidence: 0.55}
	res := &CrossDayResult{ConfidenceBoost: 0.00}
	ApplyCrossDay(ff, res)
	if ff.Confidence != 0.55 {
		t.Errorf("expected unchanged confidence, got %.2f", ff.Confidence)
	}
	if len(ff.Sources) != 0 {
		t.Errorf("expected no new sources appended, got %v", ff.Sources)
	}
}

// TestSignalProbFromForecast_AllSignals exercises each supported signal type.
func TestSignalProbFromForecast_AllSignals(t *testing.T) {
	f := weather.Forecast{
		MaxTempC:                 38.0,
		MinTempC:                 25.0,
		PrecipitationMM:          15.0,
		PrecipitationProbability: 80.0,
		WindSpeedKMH:             60.0,
		WeatherCode:              61, // moderate rain
		SnowfallCM:               0,
	}

	signals := []string{"heat", "cold", "rain", "sunny", "snow", "wind", "fog", "humid", "dry"}
	for _, sig := range signals {
		p, ok := signalProbFromForecast(f, sig, 35.0)
		if !ok {
			t.Errorf("signal %q returned ok=false", sig)
		}
		if p < 0 || p > 1 {
			t.Errorf("signal %q returned p=%.4f outside [0,1]", sig, p)
		}
	}

	// Unknown signal.
	_, ok := signalProbFromForecast(f, "unknown_xyz", 0)
	if ok {
		t.Error("expected ok=false for unknown signal")
	}
}

// TestCheckCrossDay_UnknownSignal verifies unknown signal returns empty result.
func TestCheckCrossDay_UnknownSignal(t *testing.T) {
	dir := t.TempDir()
	city := "test_city"
	saveCachedFF(t, city, 0, 18.0, 10.0, 80.0, 20.0, dir)

	res := CheckCrossDay(city, "unknown_signal", 0, 0, dir)
	if res.ConfidenceBoost != 0 {
		t.Errorf("expected zero boost for unknown signal, got %.2f", res.ConfidenceBoost)
	}
}

// TestCheckCrossDay_FilesCleanup verifies TempDir files are created correctly.
func TestCheckCrossDay_FilesCleanup(t *testing.T) {
	dir := t.TempDir()
	city := "cleanup_city"

	for d := 0; d < 3; d++ {
		saveCachedFF(t, city, d, 18.0, 10.0, 75.0, 20.0, dir)
	}
	// Files should exist.
	for d := 0; d < 3; d++ {
		path := forecastCachePath(dir, city, d)
		if _, err := os.Stat(path); err != nil {
			t.Errorf("expected cache file for day %d to exist: %v", d, err)
		}
	}
}
