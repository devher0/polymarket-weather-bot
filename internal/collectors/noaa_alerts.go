// noaa_alerts.go — fetch active NWS weather alerts for US cities.
// API: https://api.weather.gov/alerts/active?point={lat},{lon}
// No API key required; requires a User-Agent header.
// Results are cached in memory for 30 minutes to respect NWS rate limits.
package collectors

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/devher0/polymarket-weather-bot/internal/weather"
)

// AlertLevel represents the severity of an active NWS alert.
const (
	AlertLevelNone     = 0 // no active alerts
	AlertLevelAdvisory = 1 // Heat Advisory, Frost Advisory, Wind Advisory, etc.
	AlertLevelWatch    = 2 // Tornado Watch, Winter Storm Watch, Flash Flood Watch
	AlertLevelWarning  = 3 // Tornado Warning, Excessive Heat Warning, Blizzard Warning
)

// AlertSummary holds the aggregated alert information for a city.
type AlertSummary struct {
	Level  int      // AlertLevelNone .. AlertLevelWarning
	Events []string // unique event names (e.g. "Tornado Warning")
}

// alertCacheEntry holds a cached summary and its expiry time.
type alertCacheEntry struct {
	summary  AlertSummary
	expireAt time.Time
}

var (
	alertCacheMu sync.Mutex
	alertCache   = make(map[string]alertCacheEntry) // key = city name
)

const alertCacheTTL = 30 * time.Minute

// alertsUsCities maps city names to true for cities that have NWS alert coverage.
// Note: noaa_nws.go also defines usCities for forecast endpoints; this is
// kept separate to avoid a naming conflict in the same package.
var alertsUsCities = map[string]bool{
	"new_york":      true,
	"miami":         true,
	"chicago":       true,
	"los_angeles":   true,
	"san_francisco": true,
}

// nwsAlertsUserAgent is required by the NWS API (they block generic UA strings).
const nwsAlertsUserAgent = "polymarket-weather-bot/1.0 (github.com/devher0/polymarket-weather-bot)"

// alertsHTTPClient has a short timeout — alerts are optional, not critical path.
var alertsHTTPClient = &http.Client{Timeout: 5 * time.Second}

// nwsAlertResp is a partial representation of the GeoJSON FeatureCollection
// returned by api.weather.gov/alerts/active.
type nwsAlertResp struct {
	Features []struct {
		Properties struct {
			Event    string `json:"event"`
			Severity string `json:"severity"` // Extreme, Severe, Moderate, Minor, Unknown
			Urgency  string `json:"urgency"`  // Immediate, Expected, Future, Past, Unknown
			Expires  string `json:"expires"`
		} `json:"properties"`
	} `json:"features"`
}

// eventToLevel maps NWS event name keywords to an AlertLevel.
// We scan each event string for these keywords (case-insensitive).
var eventLevelKeywords = []struct {
	keyword string
	level   int
}{
	// Warnings (level 3) — imminent or occurring
	{"tornado warning", AlertLevelWarning},
	{"severe thunderstorm warning", AlertLevelWarning},
	{"flash flood warning", AlertLevelWarning},
	{"flood warning", AlertLevelWarning},
	{"excessive heat warning", AlertLevelWarning},
	{"extreme cold warning", AlertLevelWarning},
	{"blizzard warning", AlertLevelWarning},
	{"winter storm warning", AlertLevelWarning},
	{"ice storm warning", AlertLevelWarning},
	{"high wind warning", AlertLevelWarning},
	{"hurricane warning", AlertLevelWarning},
	{"tropical storm warning", AlertLevelWarning},
	{"freeze warning", AlertLevelWarning},
	{"hard freeze warning", AlertLevelWarning},

	// Watches (level 2) — conditions possible
	{"tornado watch", AlertLevelWatch},
	{"severe thunderstorm watch", AlertLevelWatch},
	{"flash flood watch", AlertLevelWatch},
	{"flood watch", AlertLevelWatch},
	{"heat watch", AlertLevelWatch},
	{"excessive heat watch", AlertLevelWatch},
	{"winter storm watch", AlertLevelWatch},
	{"blizzard watch", AlertLevelWatch},
	{"freeze watch", AlertLevelWatch},
	{"high wind watch", AlertLevelWatch},
	{"hurricane watch", AlertLevelWatch},
	{"tropical storm watch", AlertLevelWatch},

	// Advisories (level 1) — less severe / nuisance events
	{"heat advisory", AlertLevelAdvisory},
	{"wind advisory", AlertLevelAdvisory},
	{"frost advisory", AlertLevelAdvisory},
	{"freeze advisory", AlertLevelAdvisory},
	{"winter weather advisory", AlertLevelAdvisory},
	{"dense fog advisory", AlertLevelAdvisory},
	{"flood advisory", AlertLevelAdvisory},
	{"air quality alert", AlertLevelAdvisory},
}

// classifyEvent returns the AlertLevel for a given event string.
func classifyEvent(event string) int {
	lower := strings.ToLower(event)
	for _, kw := range eventLevelKeywords {
		if strings.Contains(lower, kw.keyword) {
			return kw.level
		}
	}
	// Fall back to severity field heuristic handled in FetchAlerts.
	return AlertLevelNone
}

// FetchAlerts returns the active NWS alert summary for the given city.
// Returns (AlertSummary{Level: AlertLevelNone}, nil) for non-US cities.
// Results are cached for 30 minutes to avoid hammering the NWS API.
func FetchAlerts(city string) (AlertSummary, error) {
	if !alertsUsCities[city] {
		return AlertSummary{}, nil // non-US cities: no NWS data, not an error
	}

	// Check cache.
	alertCacheMu.Lock()
	if entry, ok := alertCache[city]; ok && time.Now().Before(entry.expireAt) {
		alertCacheMu.Unlock()
		return entry.summary, nil
	}
	alertCacheMu.Unlock()

	c, ok := weather.Cities[city]
	if !ok {
		return AlertSummary{}, fmt.Errorf("noaa_alerts: unknown city %q", city)
	}

	url := fmt.Sprintf(
		"https://api.weather.gov/alerts/active?point=%.4f,%.4f&status=actual",
		c.Lat, c.Lon,
	)

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return AlertSummary{}, fmt.Errorf("noaa_alerts: build request: %w", err)
	}
	req.Header.Set("User-Agent", nwsAlertsUserAgent)
	req.Header.Set("Accept", "application/ld+json")

	resp, err := alertsHTTPClient.Do(req)
	if err != nil {
		return AlertSummary{}, fmt.Errorf("noaa_alerts: HTTP request for %s: %w", city, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return AlertSummary{}, fmt.Errorf("noaa_alerts: status %d for %s", resp.StatusCode, city)
	}

	body, _ := io.ReadAll(resp.Body)
	var raw nwsAlertResp
	if err := json.Unmarshal(body, &raw); err != nil {
		return AlertSummary{}, fmt.Errorf("noaa_alerts: parse response for %s: %w", city, err)
	}

	// Aggregate alerts into a single summary.
	summary := AlertSummary{}
	seen := make(map[string]bool)
	for _, f := range raw.Features {
		event := f.Properties.Event
		if event == "" {
			continue
		}
		level := classifyEvent(event)
		if level == AlertLevelNone {
			// Fallback: use severity field
			switch strings.ToLower(f.Properties.Severity) {
			case "extreme", "severe":
				level = AlertLevelWarning
			case "moderate":
				level = AlertLevelWatch
			case "minor":
				level = AlertLevelAdvisory
			}
		}
		if level > summary.Level {
			summary.Level = level
		}
		if !seen[event] {
			seen[event] = true
			summary.Events = append(summary.Events, event)
		}
	}

	if summary.Level > AlertLevelNone {
		slog.Info("noaa alerts active",
			"city", city,
			"level", summary.Level,
			"events", strings.Join(summary.Events, "; "),
		)
	}

	// Store in cache.
	alertCacheMu.Lock()
	alertCache[city] = alertCacheEntry{
		summary:  summary,
		expireAt: time.Now().Add(alertCacheTTL),
	}
	alertCacheMu.Unlock()

	return summary, nil
}

// AlertBoost computes the probability boost for a given signal based on
// active NWS alerts. Returns (boostFraction, confidenceBoost).
//
// Example: AlertLevelWarning with a "heat" signal returns (0.15, 0.10).
// The caller should add boostFraction to the raw probability and
// confidenceBoost to the forecast confidence, then clamp both to [0, 0.97].
func AlertBoost(summary AlertSummary, signal string) (probBoost float64, confBoost float64) {
	if summary.Level == AlertLevelNone {
		return 0, 0
	}

	// Determine whether any active alert event is relevant to the signal.
	relevant := false
	eventsLower := make([]string, len(summary.Events))
	for i, e := range summary.Events {
		eventsLower[i] = strings.ToLower(e)
	}

	switch signal {
	case "heat":
		for _, e := range eventsLower {
			if strings.Contains(e, "heat") || strings.Contains(e, "hot") {
				relevant = true
				break
			}
		}
	case "cold", "snow":
		for _, e := range eventsLower {
			if strings.Contains(e, "freeze") || strings.Contains(e, "frost") ||
				strings.Contains(e, "winter") || strings.Contains(e, "blizzard") ||
				strings.Contains(e, "ice") || strings.Contains(e, "cold") {
				relevant = true
				break
			}
		}
	case "rain", "flood":
		for _, e := range eventsLower {
			if strings.Contains(e, "flood") || strings.Contains(e, "rain") ||
				strings.Contains(e, "thunderstorm") || strings.Contains(e, "tornado") ||
				strings.Contains(e, "tropical") {
				relevant = true
				break
			}
		}
	case "wind":
		for _, e := range eventsLower {
			if strings.Contains(e, "wind") || strings.Contains(e, "tornado") ||
				strings.Contains(e, "hurricane") || strings.Contains(e, "thunderstorm") {
				relevant = true
				break
			}
		}
	}

	if !relevant {
		return 0, 0
	}

	// Scale boost by alert level.
	switch summary.Level {
	case AlertLevelWarning:
		return 0.15, 0.10
	case AlertLevelWatch:
		return 0.08, 0.05
	case AlertLevelAdvisory:
		return 0.04, 0.02
	}
	return 0, 0
}
