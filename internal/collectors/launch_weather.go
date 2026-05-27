// launch_weather.go — 45th Weather Squadron (Patrick SFB) launch weather parser.
//
// TASK-090: The 45th Weather Squadron at Patrick Space Force Base publishes
// real-time launch weather advisories for the Cape Canaveral / Kennedy Space
// Center launch range. Their 11 Launch Commit Criteria (LCC) assess conditions
// that could threaten a launch vehicle or cause damage; the same criteria are
// used by SpaceX, ULA, and NASA for launch go/no-go decisions.
//
// This collector scrapes the public weather page and extracts the overall
// probability of weather rule violation. A high violation probability strongly
// implies severe convective / electrical weather over the Florida coast —
// directly relevant to rain, storm, and wind markets for the "miami" city.
//
// Graceful fallback: if the page cannot be fetched or parsed the functions
// return a zero-value result and log a warning; no market is blocked.
package collectors

import (
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// LaunchWeatherForecast summarises weather go/no-go for the Cape Canaveral area.
type LaunchWeatherForecast struct {
	// ViolationProbability is the chance (0–1) that at least one of the 11
	// Launch Commit Criteria will be violated. High values → bad weather.
	ViolationProbability float64
	// GoRules / NoGoRules count how many of the 11 LCC are currently "Go" vs "No-Go".
	GoRules   int
	NoGoRules int
	// Summary is a short human-readable description from the page, if found.
	Summary string
	// FetchedAt records when this data was collected.
	FetchedAt time.Time
}

const (
	launchWeatherURL = "https://www.patrick.spaceforce.mil/About/Weather/"
	launchWeatherTTL = 30 * time.Minute
)

var (
	launchWeatherCache struct {
		mu        sync.RWMutex
		result    *LaunchWeatherForecast
		fetchedAt time.Time
	}
	launchHTTPClient = &http.Client{Timeout: 10 * time.Second}

	// Patterns that indicate a "Go" or "No-Go" ruling in the page text.
	violationPctRe   = regexp.MustCompile(`(?i)(\d{1,3})\s*%\s*(?:probability\s+of\s+)?(?:weather\s+)?(?:violation|violating|rule\s+violation)`)
	goCountRe        = regexp.MustCompile(`(?i)(\d+)\s+(?:of\s+11\s+)?(?:rules?\s+)?(?:are\s+)?(?:go|favorable)`)
	noGoCountRe      = regexp.MustCompile(`(?i)(\d+)\s+(?:of\s+11\s+)?(?:rules?\s+)?(?:are\s+)?(?:no[\-\s]go|unfavorable|violated)`)
	summaryRe        = regexp.MustCompile(`(?i)(?:launch\s+weather|weather\s+outlook)[:\s]+([^\n<]{10,120})`)
)

// FetchLaunchWeather retrieves launch-commit-criteria weather data for the
// Cape Canaveral area from the 45th Weather Squadron public page.
// Results are cached for launchWeatherTTL.
func FetchLaunchWeather() (*LaunchWeatherForecast, error) {
	launchWeatherCache.mu.RLock()
	if launchWeatherCache.result != nil &&
		time.Since(launchWeatherCache.fetchedAt) < launchWeatherTTL {
		r := launchWeatherCache.result
		launchWeatherCache.mu.RUnlock()
		return r, nil
	}
	launchWeatherCache.mu.RUnlock()

	req, err := http.NewRequest(http.MethodGet, launchWeatherURL, nil)
	if err != nil {
		return nil, fmt.Errorf("launch_weather: build request: %w", err)
	}
	req.Header.Set("User-Agent", "polymarket-weather-bot/1.0 (weather research)")
	req.Header.Set("Accept", "text/html,application/xhtml+xml")

	resp, err := launchHTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("launch_weather: fetch: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("launch_weather: status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 512*1024)) // limit to 512 KB
	if err != nil {
		return nil, fmt.Errorf("launch_weather: read body: %w", err)
	}

	result := parseLaunchWeatherPage(string(body))
	result.FetchedAt = time.Now().UTC()

	launchWeatherCache.mu.Lock()
	launchWeatherCache.result = result
	launchWeatherCache.fetchedAt = time.Now()
	launchWeatherCache.mu.Unlock()

	slog.Info("launch weather fetched",
		"violation_prob", fmt.Sprintf("%.0f%%", result.ViolationProbability*100),
		"go_rules", result.GoRules,
		"no_go_rules", result.NoGoRules,
	)
	return result, nil
}

// parseLaunchWeatherPage extracts launch weather data from raw HTML.
// If no structured data can be found the result contains sensible zero values
// rather than an error, keeping callers simple.
func parseLaunchWeatherPage(html string) *LaunchWeatherForecast {
	result := &LaunchWeatherForecast{}

	// Strip HTML tags for text-only matching.
	text := stripHTMLTags(html)

	// Try to extract violation percentage.
	if m := violationPctRe.FindStringSubmatch(text); len(m) == 2 {
		if pct, err := strconv.ParseFloat(m[1], 64); err == nil {
			result.ViolationProbability = pct / 100.0
		}
	}

	// Fallback: count Go / No-Go rule mentions.
	if m := goCountRe.FindStringSubmatch(text); len(m) == 2 {
		result.GoRules, _ = strconv.Atoi(m[1])
	}
	if m := noGoCountRe.FindStringSubmatch(text); len(m) == 2 {
		result.NoGoRules, _ = strconv.Atoi(m[1])
	}

	// Derive ViolationProbability from rule counts if not found directly.
	if result.ViolationProbability == 0 && (result.GoRules+result.NoGoRules) > 0 {
		total := result.GoRules + result.NoGoRules
		result.ViolationProbability = float64(result.NoGoRules) / float64(total)
	}

	// Look for a short weather summary sentence.
	if m := summaryRe.FindStringSubmatch(text); len(m) == 2 {
		result.Summary = strings.TrimSpace(m[1])
	}

	return result
}

// stripHTMLTags removes HTML tags and decodes common entities.
func stripHTMLTags(html string) string {
	var out strings.Builder
	inTag := false
	for _, r := range html {
		switch {
		case r == '<':
			inTag = true
			out.WriteRune(' ')
		case r == '>':
			inTag = false
		case !inTag:
			out.WriteRune(r)
		}
	}
	s := out.String()
	s = strings.ReplaceAll(s, "&amp;", "&")
	s = strings.ReplaceAll(s, "&lt;", "<")
	s = strings.ReplaceAll(s, "&gt;", ">")
	s = strings.ReplaceAll(s, "&nbsp;", " ")
	s = strings.ReplaceAll(s, "&#160;", " ")
	// Collapse whitespace.
	wsRe := regexp.MustCompile(`\s+`)
	return wsRe.ReplaceAllString(s, " ")
}

// LaunchWeatherBoost returns a probability boost for storm/wind/rain markets
// near Cape Canaveral (city "miami") based on LCC violation probability.
//
//	violation < 20%  → 0 (no boost; weather is fine)
//	violation 20–50% → proportional boost up to +0.08
//	violation > 50%  → +0.08 to +0.15
func LaunchWeatherBoost(city string) float64 {
	if city != "miami" {
		return 0
	}
	lw, err := FetchLaunchWeather()
	if err != nil {
		slog.Debug("launch weather unavailable (non-critical)", "err", err)
		return 0
	}
	v := lw.ViolationProbability
	switch {
	case v < 0.20:
		return 0
	case v < 0.50:
		return (v - 0.20) / 0.30 * 0.08 // 0 → 0.08
	default:
		return 0.08 + (v-0.50)/0.50*0.07 // 0.08 → 0.15
	}
}
