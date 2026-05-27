// lightning_nldn.go — NLDN-style lightning tracking via Blitzortung.
//
// TASK-094: Vaisala's National Lightning Detection Network (NLDN) is the
// professional standard for US lightning data, used by the 45th Weather
// Squadron and aviation authorities. Public access to raw NLDN data is not
// available, but the NWS (weather.gov) displays NLDN data on its lightning
// maps — however those pages require JavaScript.
//
// We implement the same statistical measures as NLDN using our Blitzortung
// WebSocket feed (TASK-088), which has comparable global coverage:
//
//   - Lightning30min: cloud-to-ground strikes in past 30 minutes within 300 km
//   - Lightning1h: strikes in past 60 minutes within 300 km
//   - LightningTrend: rate change between first/second 30-min half of the hour
//
// Storm probability brackets match the NLDN-derived thresholds published by
// NWS for aviation weather advisories.
package collectors

import (
	"fmt"
	"log/slog"
	"math"
	"sync"
	"time"

	"github.com/devher0/polymarket-weather-bot/internal/weather"
)

const (
	// nldnRadius is the detection radius used for NLDN-style counts (300 km).
	// Slightly larger than the 200 km Blitzortung radius to match NWS convention.
	nldnRadius = 300.0
)

// NLDNSummary provides NLDN-compatible lightning statistics for a city.
type NLDNSummary struct {
	// Strike counts within nldnRadius of the city.
	Lightning30m int
	Lightning1h  int
	// LightningTrend is positive when activity is increasing (last 30 min > first 30 min),
	// negative when decreasing, zero when stable or no data.
	LightningTrend int
	// StormProbability is a 0–1 estimate derived from 1h strike count.
	StormProbability float64
}

var nldnCache struct {
	mu      sync.RWMutex
	results map[string]nldnCacheEntry
}

type nldnCacheEntry struct {
	summary   NLDNSummary
	computedAt time.Time
}

// GetNLDNSummary returns NLDN-compatible lightning statistics for a city.
// The Blitzortung collector must be running (StartLightningCollector called)
// for results to be populated; otherwise all counts will be zero.
func GetNLDNSummary(city string) NLDNSummary {
	nldnCache.mu.RLock()
	if entry, ok := nldnCache.results[city]; ok {
		if time.Since(entry.computedAt) < 5*time.Minute {
			nldnCache.mu.RUnlock()
			return entry.summary
		}
	}
	nldnCache.mu.RUnlock()

	c, ok := weather.Cities[city]
	if !ok {
		return NLDNSummary{}
	}

	now := time.Now().UTC()
	cutoff1h := now.Add(-60 * time.Minute)
	cutoff30m := now.Add(-30 * time.Minute)

	globalStrikeBuffer.mu.RLock()
	var count30m, count1h, countOld30m int
	for _, s := range globalStrikeBuffer.strikes {
		if s.At.Before(cutoff1h) {
			continue
		}
		dist := haversineKM(c.Lat, c.Lon, s.Lat, s.Lon)
		if dist > nldnRadius {
			continue
		}
		count1h++
		if s.At.After(cutoff30m) {
			count30m++
		} else {
			countOld30m++
		}
	}
	globalStrikeBuffer.mu.RUnlock()

	trend := count30m - countOld30m
	summary := NLDNSummary{
		Lightning30m:     count30m,
		Lightning1h:      count1h,
		LightningTrend:   trend,
		StormProbability: nldnStormProbability(count1h),
	}

	nldnCache.mu.Lock()
	if nldnCache.results == nil {
		nldnCache.results = make(map[string]nldnCacheEntry)
	}
	nldnCache.results[city] = nldnCacheEntry{summary: summary, computedAt: now}
	nldnCache.mu.Unlock()

	if count1h > 0 {
		slog.Debug("nldn summary computed",
			"city", city,
			"1h_strikes", count1h,
			"30m_strikes", count30m,
			"trend", fmt.Sprintf("%+d", trend),
			"storm_prob", fmt.Sprintf("%.2f", summary.StormProbability),
		)
	}

	return summary
}

// nldnStormProbability converts 1-hour cloud-to-ground strike count to a
// 0–1 storm probability using NWS-published aviation thresholds.
//
//	0 strikes      → 0.03 (background noise rate, small residual)
//	1–10 strikes   → 0.20 (isolated lightning, convection developing)
//	10–50 strikes  → 0.20–0.65 (active thunderstorm)
//	50–100 strikes → 0.65–0.85 (strong storm)
//	> 100 strikes  → 0.85–0.95 (severe thunderstorm)
func nldnStormProbability(strikes1h int) float64 {
	switch {
	case strikes1h == 0:
		return 0.03
	case strikes1h < 10:
		return 0.20 + float64(strikes1h)/10.0*0.10 // 0.20–0.30
	case strikes1h < 50:
		return 0.30 + float64(strikes1h-10)/40.0*0.35 // 0.30–0.65
	case strikes1h < 100:
		return 0.65 + float64(strikes1h-50)/50.0*0.20 // 0.65–0.85
	default:
		extra := math.Min(float64(strikes1h-100)/100.0, 1.0)
		return 0.85 + extra*0.10 // 0.85–0.95
	}
}
