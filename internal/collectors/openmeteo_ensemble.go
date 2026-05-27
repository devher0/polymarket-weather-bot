// openmeteo_ensemble.go — fetches ICON seamless ensemble forecast (16 members)
// from Open-Meteo to derive a probabilistic uncertainty estimate.
//
// Endpoint: https://ensemble-api.open-meteo.com/v1/ensemble
// No API key required. Global coverage via ICON-EPS.
//
// Returns an EnsembleResult with:
//   - Mean daily forecast aggregated from member hourly data
//   - TempStdDev  — stddev of max-temperature across members (°C)
//   - PrecipStdDev — stddev of daily precipitation sum across members (mm)
//   - MemberCount — how many members were actually returned (API may differ)
package collectors

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/devher0/polymarket-weather-bot/internal/weather"
)

const (
	ensembleBaseURL = "https://ensemble-api.open-meteo.com/v1/ensemble"
	ensembleModel   = "icon_seamless"
	ensembleN       = 16 // number of members to request
)

// memberRe matches variable names like temperature_2m_member03.
var memberRe = regexp.MustCompile(`^(.+)_member(\d+)$`)

// EnsembleResult holds the fused mean forecast plus member-spread uncertainty.
type EnsembleResult struct {
	Forecast     weather.Forecast
	TempStdDev   float64 // °C — stddev of daily max-temp across members
	PrecipStdDev float64 // mm — stddev of daily precip sum across members
	MemberCount  int     // number of members actually available
}

// rawEnsembleResp is used for flexible parsing of the ensemble JSON.
// The hourly object contains "time" plus dynamic member keys.
type rawEnsembleResp struct {
	Hourly map[string]json.RawMessage `json:"hourly"`
}

// GetEnsembleForecast fetches ICON ensemble data and returns the mean forecast
// and member-spread uncertainty for the given city and day offset (0=today).
// Returns an error if the city is unknown or the API is unavailable.
func GetEnsembleForecast(city string, dayOffset int) (*EnsembleResult, error) {
	c, ok := weather.Cities[city]
	if !ok {
		return nil, fmt.Errorf("ensemble: unknown city %q", city)
	}
	if dayOffset < 0 {
		dayOffset = 0
	}
	if dayOffset > 6 {
		dayOffset = 6
	}

	// Build comma-separated list of hourly member variables.
	params := make([]string, 0, ensembleN*2)
	for i := 1; i <= ensembleN; i++ {
		params = append(params,
			fmt.Sprintf("temperature_2m_member%02d", i),
			fmt.Sprintf("precipitation_member%02d", i),
		)
	}

	url := fmt.Sprintf(
		"%s?latitude=%.4f&longitude=%.4f&hourly=%s&models=%s&forecast_days=7&timezone=UTC",
		ensembleBaseURL, c.Lat, c.Lon,
		strings.Join(params, ","),
		ensembleModel,
	)

	cl := &http.Client{Timeout: 20 * time.Second}
	resp, err := cl.Get(url)
	if err != nil {
		return nil, fmt.Errorf("ensemble: request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("ensemble: HTTP %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("ensemble: read body: %w", err)
	}

	var raw rawEnsembleResp
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("ensemble: parse JSON: %w", err)
	}

	// Parse the flat "time" array.
	timeRaw, ok := raw.Hourly["time"]
	if !ok {
		return nil, fmt.Errorf("ensemble: missing time field")
	}
	var times []string
	if err := json.Unmarshal(timeRaw, &times); err != nil {
		return nil, fmt.Errorf("ensemble: parse time: %w", err)
	}

	// Determine which hourly indices belong to the target day.
	targetDate := time.Now().UTC().AddDate(0, 0, dayOffset).Format("2006-01-02")
	targetIndices := make([]int, 0, 24)
	for i, ts := range times {
		// timestamps are like "2026-05-27T00:00"
		if len(ts) >= 10 && ts[:10] == targetDate {
			targetIndices = append(targetIndices, i)
		}
	}
	if len(targetIndices) == 0 {
		return nil, fmt.Errorf("ensemble: no data for day +%d (%s)", dayOffset, targetDate)
	}

	// Accumulate per-member daily aggregates.
	type memberStats struct {
		maxTemp float64
		precip  float64
	}
	memberData := make(map[int]*memberStats)

	for key, raw := range raw.Hourly {
		m := memberRe.FindStringSubmatch(key)
		if m == nil {
			continue
		}
		varName := m[1]
		var memberNum int
		if _, err := fmt.Sscanf(m[2], "%d", &memberNum); err != nil {
			continue
		}
		if memberNum < 1 || memberNum > ensembleN {
			continue
		}

		var vals []float64
		if err := json.Unmarshal(raw, &vals); err != nil {
			continue
		}

		if _, exists := memberData[memberNum]; !exists {
			memberData[memberNum] = &memberStats{maxTemp: -999}
		}
		ms := memberData[memberNum]

		switch varName {
		case "temperature_2m":
			for _, idx := range targetIndices {
				if idx < len(vals) && vals[idx] > ms.maxTemp {
					ms.maxTemp = vals[idx]
				}
			}
		case "precipitation":
			for _, idx := range targetIndices {
				if idx < len(vals) {
					ms.precip += vals[idx]
				}
			}
		}
	}

	if len(memberData) == 0 {
		return nil, fmt.Errorf("ensemble: no member data parsed")
	}

	// Compute mean and stddev across members.
	temps := make([]float64, 0, len(memberData))
	precips := make([]float64, 0, len(memberData))

	for _, ms := range memberData {
		if ms.maxTemp > -999 {
			temps = append(temps, ms.maxTemp)
		}
		precips = append(precips, ms.precip)
	}

	meanTemp, sdTemp := meanStdDev(temps)
	meanPrecip, sdPrecip := meanStdDev(precips)

	// Determine a representative weather code: use simple heuristic.
	var wmoCode int
	if meanPrecip > 5 {
		wmoCode = 63 // moderate rain
	} else if meanPrecip > 1 {
		wmoCode = 51 // light drizzle
	} else if sdTemp < 2 {
		wmoCode = 0 // clear (low spread usually means settled weather)
	} else {
		wmoCode = 2 // partly cloudy
	}

	result := &EnsembleResult{
		Forecast: weather.Forecast{
			City:                     city,
			Date:                     targetDate,
			MaxTempC:                 meanTemp,
			MinTempC:                 meanTemp - 8, // rough diurnal range
			PrecipitationMM:          meanPrecip,
			PrecipitationProbability: math.Min(99, sdPrecip*10+meanPrecip*3), // heuristic
			WindSpeedKMH:             0, // not available from ensemble
			WeatherCode:              wmoCode,
		},
		TempStdDev:   sdTemp,
		PrecipStdDev: sdPrecip,
		MemberCount:  len(memberData),
	}
	return result, nil
}

// EnsembleToConfidence converts ensemble temperature spread to a 0-1 confidence
// value: 0°C stddev → 1.0 confidence; ≥5°C stddev → 0.0.
// This replaces the multi-source inter-model confidence when ensemble is available.
func EnsembleToConfidence(tempStdDev float64) float64 {
	if tempStdDev <= 0 {
		return 1.0
	}
	// Linear decay: 0°C → 1.0, 5°C → 0.0
	c := 1.0 - tempStdDev/5.0
	if c < 0 {
		return 0
	}
	return c
}

// meanStdDev computes mean and population stddev of a float slice.
func meanStdDev(vals []float64) (mean, sd float64) {
	if len(vals) == 0 {
		return 0, 0
	}
	for _, v := range vals {
		mean += v
	}
	mean /= float64(len(vals))

	var variance float64
	for _, v := range vals {
		d := v - mean
		variance += d * d
	}
	variance /= float64(len(vals))
	sd = math.Sqrt(variance)
	return
}
