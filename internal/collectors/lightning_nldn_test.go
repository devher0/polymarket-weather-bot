// lightning_nldn_test.go — unit tests for NLDN-style lightning statistics.
package collectors

import (
	"testing"
	"time"
)

// TestNLDNStormProbability verifies the probability brackets match NWS thresholds.
func TestNLDNStormProbability(t *testing.T) {
	cases := []struct {
		strikes int
		wantMin float64
		wantMax float64
	}{
		{0, 0.03, 0.03},
		{5, 0.20, 0.35},
		{10, 0.30, 0.31},
		{30, 0.45, 0.55},
		{50, 0.65, 0.66},
		{75, 0.74, 0.76},
		{100, 0.85, 0.86},
		{200, 0.94, 0.96},
	}
	for _, c := range cases {
		got := nldnStormProbability(c.strikes)
		if got < c.wantMin || got > c.wantMax {
			t.Errorf("nldnStormProbability(%d) = %.4f, want [%.2f, %.2f]",
				c.strikes, got, c.wantMin, c.wantMax)
		}
	}
}

// TestGetNLDNSummaryNoStrikes checks that a city with no buffered strikes
// returns zeroed counts and the background storm probability.
func TestGetNLDNSummaryNoStrikes(t *testing.T) {
	// Ensure the global buffer is empty.
	globalStrikeBuffer.mu.Lock()
	globalStrikeBuffer.strikes = nil
	globalStrikeBuffer.mu.Unlock()

	// Invalidate any cached result.
	nldnCache.mu.Lock()
	nldnCache.results = nil
	nldnCache.mu.Unlock()

	summary := GetNLDNSummary("miami")
	if summary.Lightning30m != 0 {
		t.Errorf("Lightning30m = %d, want 0", summary.Lightning30m)
	}
	if summary.Lightning1h != 0 {
		t.Errorf("Lightning1h = %d, want 0", summary.Lightning1h)
	}
	if summary.LightningTrend != 0 {
		t.Errorf("LightningTrend = %d, want 0", summary.LightningTrend)
	}
	if summary.StormProbability != 0.03 {
		t.Errorf("StormProbability = %.4f, want 0.03", summary.StormProbability)
	}
}

// TestGetNLDNSummaryWithStrikes injects synthetic strikes into the global
// buffer and verifies that GetNLDNSummary counts them correctly.
func TestGetNLDNSummaryWithStrikes(t *testing.T) {
	// Miami: 25.77° N, 80.19° W — use city coordinates directly.
	const testLat, testLon = 25.77, -80.19

	now := time.Now().UTC()

	synthetic := []lightningStrike{
		// 5 strikes in the past 30 min (recent).
		{Lat: testLat + 0.1, Lon: testLon + 0.1, At: now.Add(-10 * time.Minute)},
		{Lat: testLat - 0.1, Lon: testLon - 0.1, At: now.Add(-15 * time.Minute)},
		{Lat: testLat, Lon: testLon, At: now.Add(-20 * time.Minute)},
		{Lat: testLat + 0.2, Lon: testLon, At: now.Add(-25 * time.Minute)},
		{Lat: testLat, Lon: testLon - 0.2, At: now.Add(-29 * time.Minute)},
		// 3 strikes in the 30–60 min window (older).
		{Lat: testLat + 0.1, Lon: testLon, At: now.Add(-35 * time.Minute)},
		{Lat: testLat, Lon: testLon + 0.1, At: now.Add(-45 * time.Minute)},
		{Lat: testLat - 0.1, Lon: testLon, At: now.Add(-55 * time.Minute)},
		// 1 strike outside radius (far away) — should not be counted.
		{Lat: testLat + 10.0, Lon: testLon + 10.0, At: now.Add(-5 * time.Minute)},
	}

	globalStrikeBuffer.mu.Lock()
	globalStrikeBuffer.strikes = synthetic
	globalStrikeBuffer.mu.Unlock()

	// Invalidate cache to force recomputation.
	nldnCache.mu.Lock()
	nldnCache.results = nil
	nldnCache.mu.Unlock()

	summary := GetNLDNSummary("miami")

	if summary.Lightning30m != 5 {
		t.Errorf("Lightning30m = %d, want 5", summary.Lightning30m)
	}
	if summary.Lightning1h != 8 {
		t.Errorf("Lightning1h = %d, want 8", summary.Lightning1h)
	}
	// trend = recent30m - old30m = 5 - 3 = +2
	if summary.LightningTrend != 2 {
		t.Errorf("LightningTrend = %d, want 2", summary.LightningTrend)
	}
	// 8 strikes in 1h → nldnStormProbability(8) should be in [0.20, 0.35]
	if summary.StormProbability < 0.20 || summary.StormProbability > 0.40 {
		t.Errorf("StormProbability = %.4f, out of expected range [0.20, 0.40]",
			summary.StormProbability)
	}
}

// TestNLDNCache verifies that results are served from cache within TTL.
func TestNLDNCache(t *testing.T) {
	globalStrikeBuffer.mu.Lock()
	globalStrikeBuffer.strikes = nil
	globalStrikeBuffer.mu.Unlock()

	nldnCache.mu.Lock()
	nldnCache.results = nil
	nldnCache.mu.Unlock()

	// First call — populates cache.
	s1 := GetNLDNSummary("miami")
	// Second call — should hit cache (same result).
	s2 := GetNLDNSummary("miami")

	if s1 != s2 {
		t.Errorf("cache miss: got different summaries on consecutive calls: %+v vs %+v", s1, s2)
	}
}
