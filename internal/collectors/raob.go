// raob.go — Radiosondes / upper-air sounding data via NOAA rucsoundings.
//
// TASK-087: Fetch atmospheric profiles (wind by pressure level) from
// https://rucsoundings.noaa.gov/get_soundings.cgi using GFS model output.
// This gives vertical wind profiles at 850/700/500 hPa pressure levels
// (~1.5 / ~3 / ~5.5 km altitude) which are valuable signals for wind markets.
//
// If the remote API is unavailable the functions return a zero-value profile
// (graceful fallback) and log a warning, so callers never hard-fail.
package collectors

import (
	"bufio"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/devher0/polymarket-weather-bot/internal/weather"
)

// AtmosphericProfile holds upper-air wind speeds at key pressure levels.
// All wind speeds are in km/h.
type AtmosphericProfile struct {
	// Wind speeds at standard pressure levels.
	WindKMH850hPa float64 // ~1500 m altitude
	WindKMH700hPa float64 // ~3000 m altitude
	WindKMH500hPa float64 // ~5500 m altitude
	// MaxWindShear is the largest wind speed difference between adjacent levels
	// (850→700 or 700→500 hPa) in km/h. High shear indicates instability.
	MaxWindShear float64
}

// WindBoost returns an additive probability boost for wind markets based on
// 850 hPa winds. A value of 0 means no boost; 0.20 is the maximum.
//
// Rule: 850 hPa wind > 50 km/h → boost proportional to excess, capped at 0.20.
func (p AtmosphericProfile) WindBoost() float64 {
	const threshold = 50.0
	const maxBoost = 0.20
	if p.WindKMH850hPa <= threshold {
		return 0
	}
	// Linear ramp from 50 km/h (0 boost) to 120 km/h (full boost).
	boost := (p.WindKMH850hPa - threshold) / (120.0 - threshold) * maxBoost
	return math.Min(boost, maxBoost)
}

// raobCacheEntry caches a profile per city.
type raobCacheEntry struct {
	profile   AtmosphericProfile
	fetchedAt time.Time
}

var (
	raobCache  sync.Map
	raobTTL    = 3 * time.Hour // GFS soundings update every 6 h; 3 h is a reasonable TTL
	raobClient = &http.Client{Timeout: 20 * time.Second}
)

// GetAtmosphericProfile returns the upper-air wind profile for a city.
// On any error (network, parse, unknown city) it returns a zero-value profile
// so callers can proceed without crashing.
func GetAtmosphericProfile(city string) AtmosphericProfile {
	// Check cache first.
	if v, ok := raobCache.Load(city); ok {
		entry := v.(raobCacheEntry)
		if time.Since(entry.fetchedAt) < raobTTL {
			return entry.profile
		}
	}

	c, ok := weather.Cities[city]
	if !ok {
		slog.Warn("raob: unknown city, skipping", "city", city)
		return AtmosphericProfile{}
	}

	profile, err := fetchRAOBProfile(c.Lat, c.Lon)
	if err != nil {
		slog.Warn("raob: fetch failed, using zero profile", "city", city, "err", err)
		return AtmosphericProfile{}
	}

	raobCache.Store(city, raobCacheEntry{profile: profile, fetchedAt: time.Now()})
	return profile
}

// fetchRAOBProfile queries the NOAA rucsoundings CGI for a GFS sounding near
// the given lat/lon and parses the fixed-width text response to extract wind
// at 850, 700, and 500 hPa.
func fetchRAOBProfile(lat, lon float64) (AtmosphericProfile, error) {
	// The CGI accepts lat/lon directly as the "airport" parameter when prefixed.
	// time1/time2 window: current hour to current+6h for a single GFS cycle.
	now := time.Now().UTC()
	// Round down to the latest 6-hourly GFS cycle (00/06/12/18 UTC).
	cycle := (now.Hour() / 6) * 6
	initTime := time.Date(now.Year(), now.Month(), now.Day(), cycle, 0, 0, 0, time.UTC)

	url := fmt.Sprintf(
		"https://rucsoundings.noaa.gov/get_soundings.cgi"+
			"?data_source=GFS&airport=%.4f,%.4f"+
			"&start_year=%d&start_month_name=%s&start_mday=%d"+
			"&start_hour=%d&start_min=0&n_hrs=1.0"+
			"&fcst_len=shortest&airport=%.4f,%.4f"+
			"&text=Ascii%%20text%%20(GSL%%20format)&hydrometeors=false&start=latest",
		lat, lon,
		initTime.Year(), initTime.Format("Jan"), initTime.Day(),
		initTime.Hour(),
		lat, lon,
	)

	resp, err := raobClient.Get(url)
	if err != nil {
		return AtmosphericProfile{}, fmt.Errorf("raob: http: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
		return AtmosphericProfile{}, fmt.Errorf("raob: status %d: %s", resp.StatusCode, string(body))
	}

	return parseRAOBText(resp.Body)
}

// parseRAOBText parses a NOAA GSD/GSL sounding text file.
//
// The format has header lines followed by data lines. Each data line has the
// form (space-separated, fixed-width columns):
//
//	type pressure(hPa×10) height(m) temp(C×10) dew(C×10) windDir(deg) windSpd(kt)
//
// type=9 means a significant pressure level. We look for pressure values
// closest to 8500 (850 hPa), 7000 (700 hPa), 5000 (500 hPa).
func parseRAOBText(r io.Reader) (AtmosphericProfile, error) {
	// Target pressures in tenths of hPa.
	const (
		target850 = 8500
		target700 = 7000
		target500 = 5000
		tolerance = 250 // ±25 hPa
	)

	type levelWind struct {
		windKt float64
		found  bool
	}
	var (
		w850 levelWind
		w700 levelWind
		w500 levelWind
	)

	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		line := scanner.Text()
		fields := strings.Fields(line)
		// Data lines have at least 7 fields; type must be a digit.
		if len(fields) < 7 {
			continue
		}
		// First field is record type (1=station info, 2=sounding checks,
		// 3=station id, 4=mandatory levels, 5=significant temp, 6=wind,
		// 9=data level). We accept types 4 and 9 (mandatory/significant levels
		// that carry pressure + wind).
		recType := fields[0]
		if recType != "4" && recType != "9" {
			continue
		}

		pressureTenths, err := strconv.Atoi(fields[1])
		if err != nil {
			continue
		}
		// field[5] = wind direction (degrees), field[6] = wind speed (knots)
		windSpeedKt, err := strconv.ParseFloat(fields[6], 64)
		if err != nil {
			continue
		}
		// Bad/missing data is often encoded as 99999.
		if windSpeedKt >= 9999 {
			continue
		}

		if !w850.found && abs(pressureTenths-target850) <= tolerance {
			w850 = levelWind{windKt: windSpeedKt, found: true}
		} else if !w700.found && abs(pressureTenths-target700) <= tolerance {
			w700 = levelWind{windKt: windSpeedKt, found: true}
		} else if !w500.found && abs(pressureTenths-target500) <= tolerance {
			w500 = levelWind{windKt: windSpeedKt, found: true}
		}
	}
	if err := scanner.Err(); err != nil {
		return AtmosphericProfile{}, fmt.Errorf("raob: scan: %w", err)
	}

	// Convert knots → km/h (1 kt = 1.852 km/h).
	const ktToKmh = 1.852
	p := AtmosphericProfile{
		WindKMH850hPa: w850.windKt * ktToKmh,
		WindKMH700hPa: w700.windKt * ktToKmh,
		WindKMH500hPa: w500.windKt * ktToKmh,
	}

	// Compute maximum wind shear between adjacent levels.
	shear850to700 := math.Abs(p.WindKMH700hPa - p.WindKMH850hPa)
	shear700to500 := math.Abs(p.WindKMH500hPa - p.WindKMH700hPa)
	p.MaxWindShear = math.Max(shear850to700, shear700to500)

	return p, nil
}

// abs is an integer absolute value helper.
func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}
