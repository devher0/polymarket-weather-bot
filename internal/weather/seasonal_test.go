package weather

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// tolerance for float comparisons
const eps = 0.02

func TestGetSeasonal_KnownCity(t *testing.T) {
	mc, ok := GetSeasonal("miami", time.July)
	if !ok {
		t.Fatal("expected data for miami/July")
	}
	// Miami July: AvgMaxTempC=33, RainProb=0.63, SunProb=0.50
	if math.Abs(mc.AvgMaxTempC-33) > 1 {
		t.Errorf("AvgMaxTempC: got %.1f, want ~33", mc.AvgMaxTempC)
	}
	if math.Abs(mc.RainProb-0.63) > eps {
		t.Errorf("RainProb: got %.2f, want ~0.63", mc.RainProb)
	}
}

func TestGetSeasonal_UnknownCity(t *testing.T) {
	_, ok := GetSeasonal("atlantis", time.June)
	if ok {
		t.Fatal("expected false for unknown city")
	}
}

func TestGetSeasonal_AllCities(t *testing.T) {
	cities := []string{
		"new_york", "london", "tokyo", "miami", "paris",
		"chicago", "los_angeles", "san_francisco", "berlin",
	}
	for _, city := range cities {
		for m := time.January; m <= time.December; m++ {
			mc, ok := GetSeasonal(city, m)
			if !ok {
				t.Errorf("%s/%s: expected data", city, m)
				continue
			}
			if mc.RainProb < 0 || mc.RainProb > 1 {
				t.Errorf("%s/%s: RainProb %.2f out of range", city, m, mc.RainProb)
			}
			if mc.SunProb < 0 || mc.SunProb > 1 {
				t.Errorf("%s/%s: SunProb %.2f out of range", city, m, mc.SunProb)
			}
		}
	}
}

func TestAdjustForSeason_RainMiami_Summer(t *testing.T) {
	// Miami July has RainProb=0.63; alpha for day 0 = 0.80
	// rawP=0.40 (model underestimates rain) → adjusted should be > 0.40
	adjusted := AdjustForSeason("miami", todayPlusN(0), 0.40, "rain", 0)
	if adjusted <= 0.40 {
		t.Errorf("expected seasonal pull to increase rain prob for Miami summer, got %.3f", adjusted)
	}
}

func TestAdjustForSeason_SunnyLA_Summer(t *testing.T) {
	// LA July has SunProb=0.87; rawP=0.70 should be pulled up
	adjusted := AdjustForSeason("los_angeles", todayPlusN(0), 0.70, "sunny", 0)
	if adjusted <= 0.70 {
		t.Errorf("expected seasonal pull to increase sunny prob for LA summer, got %.3f", adjusted)
	}
}

func TestAdjustForSeason_LondonRain_Winter(t *testing.T) {
	// London January RainProb=0.52; rawP=0.30 (model underestimates) → pull up
	adjusted := AdjustForSeason("london", "2026-01-15", 0.30, "rain", 0)
	if adjusted <= 0.30 {
		t.Errorf("expected seasonal pull to increase rain prob for London January, got %.3f", adjusted)
	}
}

func TestAdjustForSeason_Alpha_Decreases_With_Horizon(t *testing.T) {
	// For a city where prior (0.63) > rawP (0.20),
	// a far forecast should be pulled MORE toward the prior than a near one.
	day0 := AdjustForSeason("miami", todayPlusN(0), 0.20, "rain", 0)
	day6 := AdjustForSeason("miami", todayPlusN(6), 0.20, "rain", 0)
	if day6 <= day0 {
		t.Errorf("day+6 should be closer to prior than day+0: day0=%.3f day6=%.3f", day0, day6)
	}
}

func TestAdjustForSeason_UnknownCity_Passthrough(t *testing.T) {
	rawP := 0.42
	adjusted := AdjustForSeason("atlantis", todayPlusN(1), rawP, "rain", 0)
	if math.Abs(adjusted-rawP) > 0.001 {
		t.Errorf("unknown city should return rawP unchanged, got %.3f", adjusted)
	}
}

func TestAdjustForSeason_WindNoAdjustment(t *testing.T) {
	rawP := 0.55
	adjusted := AdjustForSeason("new_york", todayPlusN(1), rawP, "wind", 0)
	if math.Abs(adjusted-rawP) > 0.001 {
		t.Errorf("wind signal has no prior, should return rawP unchanged, got %.3f", adjusted)
	}
}

func TestAdjustForSeason_ClampedOutput(t *testing.T) {
	// Even extreme priors shouldn't push output outside [0.02, 0.97]
	for _, rawP := range []float64{0.001, 0.999} {
		adj := AdjustForSeason("miami", todayPlusN(0), rawP, "rain", 0)
		if adj < 0.02 || adj > 0.97 {
			t.Errorf("output %.4f out of clamped range [0.02, 0.97]", adj)
		}
	}
}

func TestAdjustForSeason_HeatThreshold_Chicago_Winter(t *testing.T) {
	// Chicago January AvgMax=-2°C, threshold 35°C → heat prob very low
	rawP := 0.50 // model uncertainty
	adjusted := AdjustForSeason("chicago", "2026-01-20", rawP, "heat", 35.0)
	// Seasonal prior should pull heat prob DOWN significantly
	if adjusted >= 0.50 {
		t.Errorf("Chicago winter heat should be pulled down, got %.3f", adjusted)
	}
}

func TestSeasonalSummary(t *testing.T) {
	s := SeasonalSummary("miami", time.July)
	if s == "" || s == "no seasonal data for miami" {
		t.Errorf("expected non-empty summary, got: %q", s)
	}
	t.Log("seasonal summary:", s)
}

// todayPlusN returns the date string for today + n days in "2006-01-02" format.
func todayPlusN(n int) string {
	return time.Now().UTC().AddDate(0, 0, n).Format("2006-01-02")
}

// ── TASK-158: PrecipitationZScore tests ──────────────────────────────────

// writePrecipHistFile writes a minimal historical JSON file for testing.
func writePrecipHistFile(t *testing.T, dir, city string, values []float64) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, "data", "historical"), 0o755); err != nil {
		t.Fatal(err)
	}
	type rec struct {
		PrecipitationMM float64 `json:"PrecipitationMM"`
	}
	type file struct {
		Records []rec `json:"records"`
	}
	recs := make([]rec, len(values))
	for i, v := range values {
		recs[i] = rec{PrecipitationMM: v}
	}
	data, _ := json.Marshal(file{Records: recs})
	path := filepath.Join(dir, "data", "historical", city+".json")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestPrecipitationZScore_MissingFile(t *testing.T) {
	tmp := t.TempDir()
	z := PrecipitationZScore("new_york", 10.0, tmp)
	if z != 0 {
		t.Errorf("expected 0 for missing file, got %.2f", z)
	}
}

func TestPrecipitationZScore_TooFewRecords(t *testing.T) {
	tmp := t.TempDir()
	writePrecipHistFile(t, tmp, "london", []float64{1, 2, 3, 4, 5})
	z := PrecipitationZScore("london", 20.0, tmp)
	if z != 0 {
		t.Errorf("expected 0 for < 7 records, got %.2f", z)
	}
}

func TestPrecipitationZScore_ArideCity_SmallSigma(t *testing.T) {
	tmp := t.TempDir()
	// Dubai summer: near-zero precipitation every day → sigma ≈ 0
	vals := make([]float64, 10) // all zeros
	writePrecipHistFile(t, tmp, "dubai", vals)
	z := PrecipitationZScore("dubai", 5.0, tmp)
	if z != 0 {
		t.Errorf("expected 0 for arid city (sigma < 0.5), got %.2f", z)
	}
}

func TestPrecipitationZScore_HeavyRainAnomaly(t *testing.T) {
	tmp := t.TempDir()
	// 10 days with realistic variability (σ ≈ 2.7 mm); today forecast = 30 mm.
	vals := []float64{0, 5, 1, 8, 2, 6, 0, 4, 1, 3}
	writePrecipHistFile(t, tmp, "miami", vals)
	z := PrecipitationZScore("miami", 30.0, tmp)
	if z <= 2.0 {
		t.Errorf("expected z > 2 for heavy rain anomaly, got %.2f", z)
	}
	if z > 5 {
		t.Errorf("expected z clamped to 5, got %.2f", z)
	}
}

func TestPrecipitationZScore_DryAnomaly(t *testing.T) {
	tmp := t.TempDir()
	// 10 days of ~10 mm baseline; today = 0 mm → negative z
	vals := []float64{9, 10, 11, 10, 9, 10, 11, 10, 9, 10}
	writePrecipHistFile(t, tmp, "london", vals)
	z := PrecipitationZScore("london", 0.0, tmp)
	if z >= 0 {
		t.Errorf("expected negative z for dry anomaly, got %.2f", z)
	}
}
