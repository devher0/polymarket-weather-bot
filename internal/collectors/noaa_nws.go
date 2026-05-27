// noaa_nws.go — NOAA National Weather Service API collector (US only).
// Endpoint: https://api.weather.gov
// No API key required; User-Agent header is mandatory.
package collectors

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/devher0/polymarket-weather-bot/internal/weather"
)

const (
	noaaBase      = "https://api.weather.gov"
	noaaUserAgent = "polymarket-weather-bot/1.0 (github.com/devher0/polymarket-weather-bot)"
)

// usCities lists cities supported by the NWS API (US only).
var usCities = map[string]bool{
	"new_york":      true,
	"miami":         true,
	"chicago":       true,
	"los_angeles":   true,
	"san_francisco": true,
}

// noaaPointsResp is the /points/{lat},{lon} response.
type noaaPointsResp struct {
	Properties struct {
		GridID  string `json:"gridId"`
		GridX   int    `json:"gridX"`
		GridY   int    `json:"gridY"`
		Forecast string `json:"forecast"` // pre-built forecast URL
	} `json:"properties"`
}

// noaaForecastResp is the /gridpoints/.../forecast response.
type noaaForecastResp struct {
	Properties struct {
		Periods []noaaPeriod `json:"periods"`
	} `json:"properties"`
}

type noaaPeriod struct {
	Name                  string `json:"name"`
	StartTime             string `json:"startTime"`
	IsDaytime             bool   `json:"isDaytime"`
	Temperature           int    `json:"temperature"`
	TemperatureUnit       string `json:"temperatureUnit"`
	WindSpeed             string `json:"windSpeed"`
	ShortForecast         string `json:"shortForecast"`
	DetailedForecast      string `json:"detailedForecast"`
	ProbabilityOfPrecipitation struct {
		Value *float64 `json:"value"`
	} `json:"probabilityOfPrecipitation"`
}

var noaaClient = &http.Client{Timeout: 20 * time.Second}

// noaaGet performs a GET request with the required NWS User-Agent.
func noaaGet(url string) ([]byte, error) {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", noaaUserAgent)
	req.Header.Set("Accept", "application/geo+json")

	resp, err := noaaClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("noaa_nws: status %d: %s", resp.StatusCode, string(body))
	}
	return io.ReadAll(resp.Body)
}

// NOAAGetForecast fetches a multi-day forecast from NOAA NWS.
// Only US cities are supported; others return an error.
func NOAAGetForecast(city string, days int) ([]weather.Forecast, error) {
	if !usCities[city] {
		return nil, fmt.Errorf("noaa_nws: city %q is not a supported US city", city)
	}

	c, ok := weather.Cities[city]
	if !ok {
		return nil, fmt.Errorf("noaa_nws: unknown city %q", city)
	}

	// Step 1: resolve grid point
	pointsURL := fmt.Sprintf("%s/points/%.4f,%.4f", noaaBase, c.Lat, c.Lon)
	pointsBody, err := noaaGet(pointsURL)
	if err != nil {
		return nil, fmt.Errorf("noaa_nws: points lookup: %w", err)
	}

	var pr noaaPointsResp
	if err := json.Unmarshal(pointsBody, &pr); err != nil {
		return nil, fmt.Errorf("noaa_nws: parse points: %w", err)
	}

	forecastURL := pr.Properties.Forecast
	if forecastURL == "" {
		forecastURL = fmt.Sprintf("%s/gridpoints/%s/%d,%d/forecast",
			noaaBase, pr.Properties.GridID,
			pr.Properties.GridX, pr.Properties.GridY)
	}

	// Step 2: fetch forecast
	fcBody, err := noaaGet(forecastURL)
	if err != nil {
		return nil, fmt.Errorf("noaa_nws: forecast fetch: %w", err)
	}

	var fr noaaForecastResp
	if err := json.Unmarshal(fcBody, &fr); err != nil {
		return nil, fmt.Errorf("noaa_nws: parse forecast: %w", err)
	}

	// NWS returns 12-hour periods (day/night). Pair them into daily forecasts.
	// We need max_temp (daytime), min_temp (nighttime), precipitation.
	type dayData struct {
		maxTempC   float64
		minTempC   float64
		precipProb float64
		wind       string
		forecast   string
		date       string
	}

	days2collect := days * 2 // each day has 2 periods
	if days2collect > len(fr.Properties.Periods) {
		days2collect = len(fr.Properties.Periods)
	}

	dayMap := make(map[string]*dayData)
	dateOrder := make([]string, 0)

	for i := 0; i < days2collect; i++ {
		p := fr.Properties.Periods[i]
		t, err := time.Parse(time.RFC3339, p.StartTime)
		if err != nil {
			continue
		}
		dateKey := t.Format("2006-01-02")

		d, exists := dayMap[dateKey]
		if !exists {
			d = &dayData{date: dateKey}
			dayMap[dateKey] = d
			dateOrder = append(dateOrder, dateKey)
		}

		tempC := fahrenheitToCelsius(float64(p.Temperature))
		if p.TemperatureUnit == "C" {
			tempC = float64(p.Temperature)
		}

		if p.IsDaytime {
			d.maxTempC = tempC
			d.wind = p.WindSpeed
			d.forecast = p.ShortForecast
		} else {
			d.minTempC = tempC
		}

		if p.ProbabilityOfPrecipitation.Value != nil {
			v := *p.ProbabilityOfPrecipitation.Value
			if v > d.precipProb {
				d.precipProb = v
			}
		}
	}

	out := make([]weather.Forecast, 0, days)
	for _, dateKey := range dateOrder {
		if len(out) >= days {
			break
		}
		d := dayMap[dateKey]

		// Estimate precipitation mm from probability and forecast text
		precipMM := estimatePrecipFromProb(d.precipProb, d.forecast)
		windKMH := parseWindSpeed(d.wind)

		out = append(out, weather.Forecast{
			City:                     city,
			Date:                     d.date,
			MaxTempC:                 d.maxTempC,
			MinTempC:                 d.minTempC,
			PrecipitationMM:          precipMM,
			PrecipitationProbability: d.precipProb,
			WindSpeedKMH:             windKMH,
			WeatherCode:              noaaWeatherCode(d.forecast),
		})
	}

	if len(out) == 0 {
		return nil, fmt.Errorf("noaa_nws: no forecast periods returned for %s", city)
	}

	return out, nil
}

func fahrenheitToCelsius(f float64) float64 {
	return (f - 32) * 5 / 9
}

// estimatePrecipFromProb converts NWS precipitation probability (0-100) and
// forecast text to approximate precipitation mm.
func estimatePrecipFromProb(prob float64, forecast string) float64 {
	switch {
	case prob > 80:
		return 8
	case prob > 60:
		return 4
	case prob > 40:
		return 2
	case prob > 20:
		return 0.5
	default:
		return 0
	}
}

// parseWindSpeed parses NWS wind speed strings like "10 to 15 mph" or "5 mph".
// Returns km/h.
func parseWindSpeed(wind string) float64 {
	var lo, hi float64
	if n, _ := fmt.Sscanf(wind, "%f to %f mph", &lo, &hi); n == 2 {
		return ((lo + hi) / 2) * 1.60934
	}
	var mph float64
	if n, _ := fmt.Sscanf(wind, "%f mph", &mph); n == 1 {
		return mph * 1.60934
	}
	return 0
}

// noaaWeatherCode maps NWS short forecast text to approximate WMO weather code.
func noaaWeatherCode(forecast string) int {
	switch {
	case containsAny(forecast, "Snow", "Blizzard"):
		return 71
	case containsAny(forecast, "Rain", "Showers", "Thunderstorm"):
		return 61
	case containsAny(forecast, "Drizzle"):
		return 51
	case containsAny(forecast, "Cloudy", "Overcast"):
		return 3
	case containsAny(forecast, "Partly Cloudy"):
		return 2
	case containsAny(forecast, "Sunny", "Clear"):
		return 0
	default:
		return 1
	}
}

func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		if len(s) >= len(sub) {
			for i := 0; i <= len(s)-len(sub); i++ {
				if s[i:i+len(sub)] == sub {
					return true
				}
			}
		}
	}
	return false
}
