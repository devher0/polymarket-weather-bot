package collectors

import (
	"math"
	"testing"
	"time"

	"github.com/devher0/polymarket-weather-bot/internal/weather"
)

// ─── helpers ──────────────────────────────────────────────────────────────────

// makePoint builds a HourlyPoint at a given UTC hour on 2026-06-01.
func makePoint(hour int, tempC, precipMM, precipProb, wind float64) HourlyPoint {
	return HourlyPoint{
		Time:       time.Date(2026, 6, 1, hour, 0, 0, 0, time.UTC),
		TempC:      tempC,
		PrecipMM:   precipMM,
		PrecipProb: precipProb,
		WindKMH:    wind,
	}
}

// makeFused returns a minimal FusedForecast with preset values.
func makeFused(maxT, minT, precipP, precipMM, wind float64, conf float64) *FusedForecast {
	return &FusedForecast{
		Forecast: weather.Forecast{
			City:                     "test_city",
			Date:                     "2026-06-01",
			MaxTempC:                 maxT,
			MinTempC:                 minT,
			PrecipitationProbability: precipP,
			PrecipitationMM:          precipMM,
			WindSpeedKMH:             wind,
		},
		Confidence: conf,
		Sources:    []string{"openmeteo"},
	}
}

// ─── TestFilterHourlyByDate ───────────────────────────────────────────────────

func TestFilterHourlyByDate_MatchesTarget(t *testing.T) {
	pts := []HourlyPoint{
		{Time: time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)},
		{Time: time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)},
		{Time: time.Date(2026, 6, 2, 0, 0, 0, 0, time.UTC)},
		{Time: time.Date(2026, 6, 2, 6, 0, 0, 0, time.UTC)},
	}
	got := FilterHourlyByDate(pts, "2026-06-01")
	if len(got) != 2 {
		t.Fatalf("expected 2 points for 2026-06-01, got %d", len(got))
	}
	for _, p := range got {
		if p.Time.Format("2006-01-02") != "2026-06-01" {
			t.Errorf("unexpected date in filtered result: %s", p.Time)
		}
	}
}

func TestFilterHourlyByDate_Empty(t *testing.T) {
	got := FilterHourlyByDate(nil, "2026-06-01")
	if len(got) != 0 {
		t.Errorf("expected empty slice for nil input, got %d", len(got))
	}
}

func TestFilterHourlyByDate_NoMatch(t *testing.T) {
	pts := []HourlyPoint{
		{Time: time.Date(2026, 6, 3, 0, 0, 0, 0, time.UTC)},
	}
	got := FilterHourlyByDate(pts, "2026-06-01")
	if len(got) != 0 {
		t.Errorf("expected no match, got %d", len(got))
	}
}

// ─── TestHourlyRainProbability ────────────────────────────────────────────────

func TestHourlyRainProbability_NoPrecip(t *testing.T) {
	pts := []HourlyPoint{
		makePoint(6, 20, 0, 5, 10),
		makePoint(12, 25, 0, 5, 10),
		makePoint(18, 22, 0, 5, 10),
	}
	p := hourlyRainProbability(pts)
	if p > 0.10 {
		t.Errorf("expected low probability (no precip), got %.2f", p)
	}
}

func TestHourlyRainProbability_HighProbLowPrecip(t *testing.T) {
	// High probability forecasted but little actual accumulation.
	pts := []HourlyPoint{
		makePoint(6, 20, 0.1, 70, 15),
		makePoint(12, 22, 0.2, 80, 20),
		makePoint(18, 21, 0.1, 60, 15),
	}
	p := hourlyRainProbability(pts)
	// Dominant signal: maxProb=80% → 0.80; total=0.4mm (<1.5) → no boost
	want := 0.80
	if math.Abs(p-want) > 0.01 {
		t.Errorf("expected %.2f, got %.2f", want, p)
	}
}

func TestHourlyRainProbability_ModeratePrecipBoost(t *testing.T) {
	// Moderate accumulation (≥1.5mm) → +5% boost on maxProb.
	pts := []HourlyPoint{
		makePoint(8, 18, 0.5, 60, 20),
		makePoint(12, 19, 0.8, 70, 25),
		makePoint(16, 18, 0.5, 50, 20),
	}
	p := hourlyRainProbability(pts)
	// maxProb=70% → 0.70; total=1.8mm (≥1.5) → +0.05 = 0.75
	want := 0.75
	if math.Abs(p-want) > 0.01 {
		t.Errorf("expected %.2f, got %.2f", want, p)
	}
}

func TestHourlyRainProbability_HeavyRain(t *testing.T) {
	// Heavy accumulation (≥5mm) → +15% boost.
	pts := []HourlyPoint{
		makePoint(8, 17, 2.0, 90, 30),
		makePoint(12, 18, 3.0, 95, 35),
		makePoint(16, 17, 2.0, 80, 30),
	}
	p := hourlyRainProbability(pts)
	// maxProb=95% → 0.95; total=7mm (≥5) → 0.95+0.15 → capped at 1.0
	if p != 1.0 {
		t.Errorf("expected 1.0 (capped) for heavy rain, got %.2f", p)
	}
}

func TestHourlyRainProbability_Empty(t *testing.T) {
	if p := hourlyRainProbability(nil); p != 0 {
		t.Errorf("expected 0 for nil input, got %.2f", p)
	}
}

// ─── TestHourlyMaxMinTemp ─────────────────────────────────────────────────────

func TestHourlyMaxMinTemp(t *testing.T) {
	pts := []HourlyPoint{
		makePoint(0, 15, 0, 0, 0),
		makePoint(6, 12, 0, 0, 0), // min
		makePoint(14, 28, 0, 0, 0), // max
		makePoint(20, 20, 0, 0, 0),
	}
	if got := hourlyMaxTemp(pts); got != 28 {
		t.Errorf("hourlyMaxTemp: expected 28, got %.1f", got)
	}
	if got := hourlyMinTemp(pts); got != 12 {
		t.Errorf("hourlyMinTemp: expected 12, got %.1f", got)
	}
}

func TestHourlyMaxMinTemp_SinglePoint(t *testing.T) {
	pts := []HourlyPoint{makePoint(10, 22, 0, 0, 0)}
	if got := hourlyMaxTemp(pts); got != 22 {
		t.Errorf("expected 22, got %.1f", got)
	}
	if got := hourlyMinTemp(pts); got != 22 {
		t.Errorf("expected 22, got %.1f", got)
	}
}

// ─── TestHourlyMaxWind ────────────────────────────────────────────────────────

func TestHourlyMaxWind(t *testing.T) {
	pts := []HourlyPoint{
		makePoint(6, 20, 0, 0, 30),
		makePoint(12, 22, 0, 0, 75), // peak
		makePoint(18, 21, 0, 0, 45),
	}
	if got := hourlyMaxWind(pts); got != 75 {
		t.Errorf("expected 75 km/h, got %.1f", got)
	}
}

// ─── TestRefineWithHourly ─────────────────────────────────────────────────────

func TestRefineWithHourly_UpdatesFields(t *testing.T) {
	ff := makeFused(25, 15, 30, 1.0, 20, 0.60)
	pts := []HourlyPoint{
		makePoint(6, 17, 0.0, 10, 25),
		makePoint(10, 22, 0.3, 40, 30),
		makePoint(14, 30, 0.5, 55, 35), // peak temp & wind
		makePoint(18, 26, 0.2, 45, 28),
	}
	RefineWithHourly(ff, pts)

	if ff.MaxTempC != 30 {
		t.Errorf("MaxTempC: expected 30, got %.1f", ff.MaxTempC)
	}
	if ff.MinTempC != 17 {
		t.Errorf("MinTempC: expected 17, got %.1f", ff.MinTempC)
	}
	if ff.WindSpeedKMH != 35 {
		t.Errorf("WindSpeedKMH: expected 35, got %.1f", ff.WindSpeedKMH)
	}
	// maxProb=55%, total=1.0mm < 1.5 → no boost → PrecipP=55
	if math.Abs(ff.PrecipitationProbability-55) > 0.5 {
		t.Errorf("PrecipitationProbability: expected 55, got %.1f", ff.PrecipitationProbability)
	}
	// Confidence boosted by 0.05
	if math.Abs(ff.Confidence-0.65) > 0.001 {
		t.Errorf("Confidence: expected 0.65, got %.3f", ff.Confidence)
	}
	// "hourly" appended to Sources
	found := false
	for _, s := range ff.Sources {
		if s == "hourly" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected 'hourly' in Sources, got %v", ff.Sources)
	}
}

func TestRefineWithHourly_NilForecast(t *testing.T) {
	// Must not panic.
	RefineWithHourly(nil, []HourlyPoint{makePoint(12, 20, 0, 0, 10)})
}

func TestRefineWithHourly_EmptyPoints(t *testing.T) {
	ff := makeFused(25, 15, 30, 1.0, 20, 0.60)
	original := *ff
	RefineWithHourly(ff, nil)
	// No changes when points is empty.
	if ff.MaxTempC != original.MaxTempC || ff.Confidence != original.Confidence {
		t.Errorf("RefineWithHourly with empty points must not change forecast")
	}
}

func TestRefineWithHourly_ConfidenceCappedAt1(t *testing.T) {
	ff := makeFused(25, 15, 30, 1.0, 20, 0.98)
	pts := []HourlyPoint{makePoint(12, 25, 0, 30, 10)}
	RefineWithHourly(ff, pts)
	if ff.Confidence > 1.0 {
		t.Errorf("Confidence must not exceed 1.0, got %.3f", ff.Confidence)
	}
}

// ─── TestHourlyTotalPrecip ────────────────────────────────────────────────────

func TestHourlyTotalPrecip(t *testing.T) {
	pts := []HourlyPoint{
		makePoint(6, 18, 0.5, 0, 0),
		makePoint(12, 20, 1.2, 0, 0),
		makePoint(18, 19, 0.3, 0, 0),
	}
	got := hourlyTotalPrecip(pts)
	want := 2.0
	if math.Abs(got-want) > 0.001 {
		t.Errorf("expected %.3f, got %.3f", want, got)
	}
}
