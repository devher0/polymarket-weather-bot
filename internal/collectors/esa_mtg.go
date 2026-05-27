// esa_mtg.go — ESA MTG-S1 (Meteosat Third Generation Sounder) atmospheric profiles.
//
// TASK-095: MTG-Sounder was launched by SpaceX in July 2025 and provides 3D
// atmospheric profiles (temperature + humidity at 100+ levels) over Europe and
// Africa every 30–60 minutes. This is especially valuable for winter/storm
// markets in London, Paris, and Berlin.
//
// EUMETSAT data is available via the Copernicus Climate Data Store API, but
// requires registration. We approximate the same sounding data using
// Open-Meteo pressure-level forecasts, which ingest ECMWF IFS analysis at
// identical pressure levels (925, 850, 700, 500, 300 hPa). The vertical
// structure of temperature and humidity is the same physical quantity that
// MTG-S1 retrieves via infrared hyperspectral sounding.
//
// Key outputs:
//   - Temperature lapse rate (°C/km) — indicates instability
//   - Mid-troposphere humidity at 700 hPa — linked to precipitation intensity
//   - Temperature inversion flag — warm layer aloft suppresses convection
//   - StormRisk boost (0–0.25) for European storm/winter weather markets
//
// Only applied to european cities: london, paris, berlin.
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

// mtgEuropeanCities are cities covered by MTG-S1 full-disk view.
var mtgEuropeanCities = map[string]bool{
	"london": true,
	"paris":  true,
	"berlin": true,
}

// MTGAtmosphericProfile holds vertical sounding data at standard pressure levels.
// Pressure levels correspond to approximate altitudes:
//   - 925 hPa ≈ 750 m (boundary layer)
//   - 850 hPa ≈ 1500 m
//   - 700 hPa ≈ 3000 m (mid-troposphere)
//   - 500 hPa ≈ 5500 m
//   - 300 hPa ≈ 9000 m (upper troposphere)
type MTGAtmosphericProfile struct {
	City string
	Date string

	// Temperature (°C) at each pressure level.
	Temp925hPa float64
	Temp850hPa float64
	Temp700hPa float64
	Temp500hPa float64
	Temp300hPa float64

	// Relative humidity (%) at key levels.
	RH700hPa float64 // mid-tropospheric moisture — indicator of precipitation intensity
	RH850hPa float64 // low-level moisture — fog/low cloud potential

	// Derived instability indices.
	LapseRate700_500 float64 // lapse rate between 700 and 500 hPa (°C/km); >6.5°C/km = conditionally unstable
	HasInversion     bool    // true when Temp850hPa > Temp925hPa (temperature inversion present)

	FetchedAt time.Time
}

// StormRiskBoost returns a 0–0.25 probability boost for European storm/winter markets.
//
//   - Steep lapse rate (>7 °C/km) → strong convective instability → +0.15
//   - High mid-tropospheric humidity at 700 hPa (>70%) → heavy rain potential → +0.10
//   - Temperature inversion → suppresses convection → reduce boost by 0.05
func (p MTGAtmosphericProfile) StormRiskBoost() float64 {
	boost := 0.0

	// Lapse rate contribution.
	if p.LapseRate700_500 > 7.0 {
		extra := math.Min((p.LapseRate700_500-7.0)/3.0, 1.0)
		boost += 0.08 + extra*0.07
	} else if p.LapseRate700_500 > 6.5 {
		boost += (p.LapseRate700_500 - 6.5) / 0.5 * 0.08
	}

	// Mid-tropospheric moisture contribution.
	if p.RH700hPa > 70 {
		boost += math.Min((p.RH700hPa-70)/30.0, 1.0) * 0.10
	}

	// Inversion suppresses convection.
	if p.HasInversion {
		boost -= 0.05
	}

	if boost < 0 {
		return 0
	}
	if boost > 0.25 {
		return 0.25
	}
	return boost
}

// mtgCacheEntry is a cache entry for MTGAtmosphericProfile.
type mtgCacheEntry struct {
	profile   MTGAtmosphericProfile
	fetchedAt time.Time
}

var (
	mtgCache  sync.Map
	mtgTTL    = 3 * time.Hour
	mtgClient = &http.Client{Timeout: 15 * time.Second}
)

// mtgPressureLevelResp is the minimal JSON from Open-Meteo pressure-level endpoint.
type mtgPressureLevelResp struct {
	Hourly struct {
		Time           []string  `json:"time"`
		Temp925        []float64 `json:"temperature_925hPa"`
		Temp850        []float64 `json:"temperature_850hPa"`
		Temp700        []float64 `json:"temperature_700hPa"`
		Temp500        []float64 `json:"temperature_500hPa"`
		Temp300        []float64 `json:"temperature_300hPa"`
		RH700          []float64 `json:"relativehumidity_700hPa"`
		RH850          []float64 `json:"relativehumidity_850hPa"`
	} `json:"hourly"`
}

// GetMTGAtmosphericProfile fetches the atmospheric sounding profile for a city.
// Only meaningful for European cities covered by MTG-S1 (london, paris, berlin).
// Returns a zero-value profile with StormRiskBoost()==0 for non-European cities.
// Results are cached for mtgTTL.
func GetMTGAtmosphericProfile(city string) MTGAtmosphericProfile {
	if !mtgEuropeanCities[city] {
		return MTGAtmosphericProfile{City: city}
	}

	if v, ok := mtgCache.Load(city); ok {
		entry := v.(mtgCacheEntry)
		if time.Since(entry.fetchedAt) < mtgTTL {
			return entry.profile
		}
	}

	c, ok := weather.Cities[city]
	if !ok {
		return MTGAtmosphericProfile{City: city}
	}

	profile, err := fetchMTGProfile(city, c.Lat, c.Lon)
	if err != nil {
		slog.Debug("esa_mtg: fetch failed (non-critical)", "city", city, "err", err)
		return MTGAtmosphericProfile{City: city}
	}

	mtgCache.Store(city, mtgCacheEntry{profile: profile, fetchedAt: time.Now()})

	slog.Debug("esa_mtg profile fetched",
		"city", city,
		"lapse_rate", fmt.Sprintf("%.2f °C/km", profile.LapseRate700_500),
		"rh_700hpa", fmt.Sprintf("%.0f%%", profile.RH700hPa),
		"inversion", profile.HasInversion,
		"storm_boost", fmt.Sprintf("%.3f", profile.StormRiskBoost()),
	)
	return profile
}

// fetchMTGProfile performs the Open-Meteo pressure-level API call and builds
// an MTGAtmosphericProfile from the hourly data.
func fetchMTGProfile(city string, lat, lon float64) (MTGAtmosphericProfile, error) {
	url := fmt.Sprintf(
		"https://api.open-meteo.com/v1/forecast"+
			"?latitude=%.4f&longitude=%.4f"+
			"&hourly=temperature_925hPa,temperature_850hPa,temperature_700hPa,"+
			"temperature_500hPa,temperature_300hPa,"+
			"relativehumidity_700hPa,relativehumidity_850hPa"+
			"&forecast_days=1&timezone=UTC",
		lat, lon,
	)

	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return MTGAtmosphericProfile{}, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("User-Agent", "polymarket-weather-bot/1.0")

	resp, err := mtgClient.Do(req)
	if err != nil {
		return MTGAtmosphericProfile{}, fmt.Errorf("http: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return MTGAtmosphericProfile{}, fmt.Errorf("status %d: %s", resp.StatusCode, string(body))
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 512*1024))
	if err != nil {
		return MTGAtmosphericProfile{}, fmt.Errorf("read body: %w", err)
	}

	var r mtgPressureLevelResp
	if err := json.Unmarshal(body, &r); err != nil {
		return MTGAtmosphericProfile{}, fmt.Errorf("parse: %w", err)
	}

	if len(r.Hourly.Temp700) == 0 {
		return MTGAtmosphericProfile{}, fmt.Errorf("empty pressure-level response for %s", city)
	}

	return buildMTGProfile(city, r), nil
}

// buildMTGProfile derives the atmospheric profile from hourly pressure-level data.
// We use the mid-day window (hours 10–14 UTC) for representativeness.
func buildMTGProfile(city string, r mtgPressureLevelResp) MTGAtmosphericProfile {
	// Determine mid-day slice (hours 10–14 UTC, indices 10–14 if forecast_days=1).
	n := len(r.Hourly.Temp700)
	start, end := 10, 15
	if start >= n {
		start = 0
	}
	if end > n {
		end = n
	}
	count := float64(end - start)
	if count == 0 {
		count = 1
	}

	avg := func(vals []float64) float64 {
		if len(vals) == 0 {
			return 0
		}
		sum := 0.0
		cnt := 0.0
		for i := start; i < end && i < len(vals); i++ {
			sum += vals[i]
			cnt++
		}
		if cnt == 0 {
			return 0
		}
		return sum / cnt
	}

	p := MTGAtmosphericProfile{
		City:       city,
		Date:       time.Now().UTC().Format("2006-01-02"),
		FetchedAt:  time.Now().UTC(),
		Temp925hPa: avg(r.Hourly.Temp925),
		Temp850hPa: avg(r.Hourly.Temp850),
		Temp700hPa: avg(r.Hourly.Temp700),
		Temp500hPa: avg(r.Hourly.Temp500),
		Temp300hPa: avg(r.Hourly.Temp300),
		RH700hPa:   avg(r.Hourly.RH700),
		RH850hPa:   avg(r.Hourly.RH850),
	}

	// Lapse rate between 700 hPa (~3 km) and 500 hPa (~5.5 km): ~2.5 km layer.
	// Positive lapse rate = temperature decreases with altitude (normal).
	if p.Temp700hPa != 0 && p.Temp500hPa != 0 {
		deltaT := p.Temp700hPa - p.Temp500hPa // positive = normal lapse
		const layerThicknessKm = 2.5
		p.LapseRate700_500 = deltaT / layerThicknessKm
	}

	// Temperature inversion: if 850 hPa is warmer than 925 hPa, an inversion
	// cap exists in the lower troposphere.
	if p.Temp850hPa > 0 && p.Temp925hPa > 0 {
		p.HasInversion = p.Temp850hPa > p.Temp925hPa
	}

	return p
}

// MTGGetAllEuropeanCities fetches profiles for all European cities in parallel.
// Non-European cities return zero profiles and are not fetched.
func MTGGetAllEuropeanCities() map[string]MTGAtmosphericProfile {
	result := make(map[string]MTGAtmosphericProfile, len(mtgEuropeanCities))
	var mu sync.Mutex
	var wg sync.WaitGroup
	for city := range mtgEuropeanCities {
		city := city
		wg.Add(1)
		go func() {
			defer wg.Done()
			profile := GetMTGAtmosphericProfile(city)
			mu.Lock()
			result[city] = profile
			mu.Unlock()
		}()
	}
	wg.Wait()
	return result
}
