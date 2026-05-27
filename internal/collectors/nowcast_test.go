// nowcast_test.go — unit tests for TASK-106 nowcast logic.
// Tests cover buildNowcastSummary (pure, no network) and NowcastRainProbability
// fallback behaviour (graceful error handling).
package collectors

import (
	"fmt"
	"testing"
	"time"
)

// buildSlots returns a minutely15Resp with n 15-min slots starting from
// baseTime, each with the given precip, temp, and wind values.
func buildSlots(baseTime time.Time, n int, precip, temp, wind float64) minutely15Resp {
	var r minutely15Resp
	for i := range n {
		t := baseTime.Add(time.Duration(i) * 15 * time.Minute)
		r.Minutely15.Time = append(r.Minutely15.Time, t.UTC().Format("2006-01-02T15:04"))
		r.Minutely15.Precipitation = append(r.Minutely15.Precipitation, precip)
		r.Minutely15.Temperature2M = append(r.Minutely15.Temperature2M, temp)
		r.Minutely15.WindSpeed10M = append(r.Minutely15.WindSpeed10M, wind)
	}
	return r
}

// TestBuildNowcastSummary_AllRain verifies that all rainy slots are counted correctly.
func TestBuildNowcastSummary_AllRain(t *testing.T) {
	base := time.Now().UTC().Add(1 * time.Minute) // start slightly in future
	m := buildSlots(base, 8, 2.0, 18.0, 20.0)    // 8 slots × 15 min = 120 min window
	s := buildNowcastSummary("new_york", 120, m)

	if s.RainProbability < 0.99 {
		t.Errorf("expected RainProbability≈1.0, got %.4f", s.RainProbability)
	}
	if s.PrecipMM < 15.0 {
		t.Errorf("expected PrecipMM >= 15, got %.2f", s.PrecipMM)
	}
	if s.MaxWindKMH != 20.0 {
		t.Errorf("expected MaxWindKMH=20, got %.1f", s.MaxWindKMH)
	}
}

// TestBuildNowcastSummary_NoRain verifies zero rain probability when precip=0.
func TestBuildNowcastSummary_NoRain(t *testing.T) {
	base := time.Now().UTC().Add(1 * time.Minute)
	m := buildSlots(base, 4, 0.0, 25.0, 10.0)
	s := buildNowcastSummary("miami", 60, m)

	if s.RainProbability != 0.0 {
		t.Errorf("expected RainProbability=0, got %.4f", s.RainProbability)
	}
	if s.PrecipMM != 0.0 {
		t.Errorf("expected PrecipMM=0, got %.2f", s.PrecipMM)
	}
}

// TestBuildNowcastSummary_PartialRain checks mixed slots.
func TestBuildNowcastSummary_PartialRain(t *testing.T) {
	// 4 rainy + 4 dry slots in a 120-min window.
	base := time.Now().UTC().Add(1 * time.Minute)
	var r minutely15Resp
	for i := range 8 {
		t2 := base.Add(time.Duration(i) * 15 * time.Minute)
		r.Minutely15.Time = append(r.Minutely15.Time, t2.UTC().Format("2006-01-02T15:04"))
		if i < 4 {
			r.Minutely15.Precipitation = append(r.Minutely15.Precipitation, 1.5)
		} else {
			r.Minutely15.Precipitation = append(r.Minutely15.Precipitation, 0.0)
		}
		r.Minutely15.Temperature2M = append(r.Minutely15.Temperature2M, 20.0)
		r.Minutely15.WindSpeed10M = append(r.Minutely15.WindSpeed10M, 5.0)
	}
	s := buildNowcastSummary("london", 120, r)

	const want = 0.5
	if s.RainProbability < want-0.01 || s.RainProbability > want+0.01 {
		t.Errorf("expected RainProbability≈%.2f, got %.4f", want, s.RainProbability)
	}
}

// TestBuildNowcastSummary_EmptySlots verifies graceful handling of empty data.
func TestBuildNowcastSummary_EmptySlots(t *testing.T) {
	var empty minutely15Resp
	s := buildNowcastSummary("paris", 60, empty)
	if s.RainProbability != 0 || s.PrecipMM != 0 || s.MaxWindKMH != 0 {
		t.Errorf("expected zero summary for empty data, got %+v", s)
	}
}

// TestBuildNowcastSummary_PastSlotsIgnored verifies that past timestamps
// are excluded from the aggregation window.
func TestBuildNowcastSummary_PastSlotsIgnored(t *testing.T) {
	// All slots 1 hour in the past — should produce zero count.
	base := time.Now().UTC().Add(-60 * time.Minute)
	m := buildSlots(base, 4, 3.0, 20.0, 15.0)
	s := buildNowcastSummary("new_york", 60, m)

	if s.RainProbability != 0 {
		t.Errorf("expected 0 rain probability for past-only data, got %.4f", s.RainProbability)
	}
}

// TestNowcastRainProbability_UnknownCity verifies fallback (0.10) for unknown city.
func TestNowcastRainProbability_UnknownCity(t *testing.T) {
	p := NowcastRainProbability("__nonexistent_city__", 120)
	if p != 0.10 {
		t.Errorf("expected fallback 0.10 for unknown city, got %.4f", p)
	}
}

// TestNowcastRainProbability_Bounds verifies the return value is always in (0,1).
func TestNowcastRainProbability_Bounds(t *testing.T) {
	cities := []string{"new_york", "london", "miami", "paris", "__unknown__"}
	for _, city := range cities {
		p := NowcastRainProbability(city, 360)
		if p <= 0 || p >= 1 {
			t.Errorf("city=%s: probability %.4f out of (0,1)", city, p)
		}
	}
}

// TestNowcastSummary_PrecipBoost verifies that high accumulated precip increases
// the final NowcastRainProbability above the raw slot fraction.
func TestNowcastSummary_PrecipBoost(t *testing.T) {
	// Inject a cache entry with known values: 1 rainy slot out of 4 (25%) but
	// >2mm total precip → precipBoost = 0.15 → result = 0.40.
	cacheKey := fmt.Sprintf("__test_precip__:%d", 60)
	nowcastCache.mu.Lock()
	nowcastCache.entries[cacheKey] = nowcastCacheEntry{
		summary: NowcastSummary{
			City:            "__test_precip__",
			WindowMinutes:   60,
			RainProbability: 0.25,
			PrecipMM:        3.0, // > 2.0 → precipBoost = 0.15
		},
		expiresAt: time.Now().Add(5 * time.Minute),
	}
	nowcastCache.mu.Unlock()

	p := NowcastRainProbability("__test_precip__", 60)
	const want = 0.25 + 0.15 // = 0.40
	if p < want-0.001 || p > want+0.001 {
		t.Errorf("expected precipBoost result ≈%.2f, got %.4f", want, p)
	}
}
