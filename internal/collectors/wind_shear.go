// wind_shear.go — vertical wind shear profile via Open-Meteo hourly.
//
// TASK-096: Wind shear (the change in wind speed with altitude) is a critical
// parameter for storm and wind markets. In synoptic meteorology:
//
//   - High shear (>30 km/h between 10m and 180m) with frontal systems →
//     stronger surface gusts, gale warnings, structural damage risk.
//   - Very high shear (>50 km/h) suppresses tornado formation but produces
//     damaging straight-line winds and squall lines.
//   - Low shear + strong convection → supercell thunderstorms (tornadic).
//
// We fetch wind_speed_80m, wind_speed_120m, and wind_speed_180m from
// Open-Meteo hourly and compute a daily-max shear profile for the city.
// The result is used in EvaluateFused() as a wind probability modifier.
package collectors

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"sync"
	"time"

	"github.com/devher0/polymarket-weather-bot/internal/weather"
)

// WindShearProfile holds vertical wind speed measurements at standard levels.
type WindShearProfile struct {
	// Wind speeds in km/h at each level.
	Wind10M  float64 // surface (10 m) — daily max
	Wind80M  float64 // low boundary layer
	Wind120M float64
	Wind180M float64 // upper boundary layer
	// Derived shear values.
	ShearLow  float64 // Wind180M − Wind10M (total boundary-layer shear)
	ShearMid  float64 // Wind120M − Wind80M (mid-layer shear)
}

// WindShearBoost returns a 0–0.20 probability boost for wind markets when
// the vertical shear indicates strong boundary-layer coupling.
//
//	shear < 20 km/h → 0 (no boost)
//	20–50 km/h      → proportional up to +0.12
//	> 50 km/h       → +0.12 to +0.20
func (p WindShearProfile) WindShearBoost() float64 {
	shear := p.ShearLow
	switch {
	case shear < 20:
		return 0
	case shear < 50:
		return (shear - 20) / 30.0 * 0.12
	default:
		extra := math.Min((shear-50)/30.0, 1.0)
		return 0.12 + extra*0.08
	}
}

// shearCache holds the most recent WindShearProfile per city.
type shearCacheEntry struct {
	profile   WindShearProfile
	fetchedAt time.Time
}

var (
	shearCache  sync.Map
	shearTTL    = 3 * time.Hour
	shearClient = &http.Client{Timeout: 12 * time.Second}
)

// windShearHourlyResp is the minimal JSON we need from Open-Meteo.
type windShearHourlyResp struct {
	Hourly struct {
		Time         []string  `json:"time"`
		Wind10M      []float64 `json:"wind_speed_10m"`
		Wind80M      []float64 `json:"wind_speed_80m"`
		Wind120M     []float64 `json:"wind_speed_120m"`
		Wind180M     []float64 `json:"wind_speed_180m"`
	} `json:"hourly"`
}

// GetWindShearProfile fetches the current-day wind shear profile for a city.
// Results are cached for shearTTL. Returns a zero-value profile on error.
func GetWindShearProfile(city string) WindShearProfile {
	if v, ok := shearCache.Load(city); ok {
		entry := v.(shearCacheEntry)
		if time.Since(entry.fetchedAt) < shearTTL {
			return entry.profile
		}
	}

	c, ok := weather.Cities[city]
	if !ok {
		return WindShearProfile{}
	}

	url := fmt.Sprintf(
		"https://api.open-meteo.com/v1/forecast"+
			"?latitude=%.4f&longitude=%.4f"+
			"&hourly=wind_speed_10m,wind_speed_80m,wind_speed_120m,wind_speed_180m"+
			"&forecast_days=1&timezone=UTC",
		c.Lat, c.Lon,
	)

	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		slog.Debug("wind_shear: build request", "city", city, "err", err)
		return WindShearProfile{}
	}
	req.Header.Set("User-Agent", "polymarket-weather-bot/1.0")

	resp, err := shearClient.Do(req)
	if err != nil {
		slog.Debug("wind_shear: fetch", "city", city, "err", err)
		return WindShearProfile{}
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		slog.Debug("wind_shear: bad status", "city", city, "status", resp.StatusCode)
		return WindShearProfile{}
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 256*1024))
	if err != nil {
		return WindShearProfile{}
	}

	var r windShearHourlyResp
	if err := json.Unmarshal(body, &r); err != nil {
		return WindShearProfile{}
	}

	// Compute daily-maximum values.
	profile := maxWindShearProfile(r)
	shearCache.Store(city, shearCacheEntry{profile: profile, fetchedAt: time.Now()})

	slog.Debug("wind_shear profile fetched",
		"city", city,
		"shear_low", fmt.Sprintf("%.1f km/h", profile.ShearLow),
		"boost", fmt.Sprintf("%.3f", profile.WindShearBoost()),
	)
	return profile
}

// maxWindShearProfile extracts the daily-maximum wind speed at each height
// and derives shear from the raw hourly response.
func maxWindShearProfile(r windShearHourlyResp) WindShearProfile {
	var p WindShearProfile
	for i := range r.Hourly.Time {
		if i < len(r.Hourly.Wind10M) && r.Hourly.Wind10M[i] > p.Wind10M {
			p.Wind10M = r.Hourly.Wind10M[i]
		}
		if i < len(r.Hourly.Wind80M) && r.Hourly.Wind80M[i] > p.Wind80M {
			p.Wind80M = r.Hourly.Wind80M[i]
		}
		if i < len(r.Hourly.Wind120M) && r.Hourly.Wind120M[i] > p.Wind120M {
			p.Wind120M = r.Hourly.Wind120M[i]
		}
		if i < len(r.Hourly.Wind180M) && r.Hourly.Wind180M[i] > p.Wind180M {
			p.Wind180M = r.Hourly.Wind180M[i]
		}
	}
	p.ShearLow = p.Wind180M - p.Wind10M
	p.ShearMid = p.Wind120M - p.Wind80M
	if p.ShearLow < 0 {
		p.ShearLow = 0
	}
	if p.ShearMid < 0 {
		p.ShearMid = 0
	}
	return p
}

// WindShear returns the difference in wind speed between high and low altitudes.
// A utility function for use in tests and strategy overrides.
func WindShear(lowKMH, highKMH float64) float64 {
	return math.Max(0, highKMH-lowKMH)
}
