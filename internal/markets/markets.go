// Package markets finds and classifies weather markets on Polymarket.
package markets

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const polyHost = "https://clob.polymarket.com"

// Market represents a single Polymarket weather prediction market.
type Market struct {
	ConditionID   string
	Question      string
	YesTokenID    string
	NoTokenID     string
	YesPrice      float64
	NoPrice       float64
	City          string  // may be empty if no city matched
	Signal        string  // rain|heat|cold|snow|wind|sunny
	EndDate       string
	ThresholdC    float64 // parsed temperature threshold in Celsius (0 = not set)
	ThinLiquidity bool    // true when top-of-book bid-ask spread > 0.10 (set by EnrichWithLiquidity)
	Spread        float64 // top-of-book bid-ask spread (set by EnrichWithLiquidity)
}

type signal struct {
	re  *regexp.Regexp
	sig string
}

var signals = []signal{
	{regexp.MustCompile(`(?i)rain|precipitation|rainfall|rainy`), "rain"},
	// TASK-048: added "exceed" and temperature range as heat indicator.
	{regexp.MustCompile(`(?i)temperature.{0,20}above|above.{0,10}\d+.{0,10}degree|exceed.{0,10}\d+.{0,10}degree|heat.?wave|heatwave|hot day|between\s+\d+.*and\s+\d+.*°[CF]`), "heat"},
	{regexp.MustCompile(`(?i)temperature.{0,20}below|below.{0,10}\d+.{0,10}degree|cold snap|freeze`), "cold"},
	{regexp.MustCompile(`(?i)snow|snowfall|blizzard`), "snow"},
	{regexp.MustCompile(`(?i)wind|hurricane|typhoon|storm`), "wind"},
	{regexp.MustCompile(`(?i)sunny|sunshine|clear sky`), "sunny"},
	// TASK-048: additional signals
	{regexp.MustCompile(`(?i)\bfog\b|foggy|misty|mist`), "fog"},
	{regexp.MustCompile(`(?i)humid(?:ity)?|dew point`), "humid"},
	{regexp.MustCompile(`(?i)\bdry\b|drought|arid`), "dry"},
}

// tempThresholdRe extracts a numeric temperature and optional F/C unit from market questions.
// Examples: "above 95°F", "above 35°C", "exceed 40 degrees", "above 100F"
var tempThresholdRe = regexp.MustCompile(`(\d+(?:\.\d+)?)\s*°?\s*([FC])\b`)

// tempDegreesOnlyRe matches temperatures without explicit unit: "30 degrees", "42degrees"
// TASK-048: if value > 50 it is interpreted as Fahrenheit, otherwise Celsius.
var tempDegreesOnlyRe = regexp.MustCompile(`(\d+(?:\.\d+)?)\s*degrees?\b`)

// tempRangeRe matches "between X°C and Y°C" or "between X°F and Y°F".
// TASK-048: ThresholdC is set to the upper bound.
var tempRangeRe = regexp.MustCompile(`(?i)between\s+(\d+(?:\.\d+)?)\s*°?\s*([CF])\s+and\s+(\d+(?:\.\d+)?)\s*°?\s*([CF])`)

// parseTempThresholdC extracts a temperature threshold from a market question
// and returns it in Celsius. Returns 0 if none found.
// Parsing priority:
//  1. Temperature range: "between 20°C and 30°C" → upper bound (checked first
//     to avoid the single-value regex picking up the lower bound)
//  2. Explicit unit with °: "35°C", "95°F"
//  3. Unitless "degrees": value > 50 → assume °F, else °C
func parseTempThresholdC(question string) float64 {
	// 1. Temperature range — use upper bound.
	rm := tempRangeRe.FindStringSubmatch(question)
	if len(rm) >= 5 {
		upper, err := strconv.ParseFloat(rm[3], 64)
		if err == nil {
			if strings.ToUpper(rm[4]) == "F" {
				upper = (upper - 32) * 5 / 9
			}
			return upper
		}
	}

	// 2. Explicit unit.
	m := tempThresholdRe.FindStringSubmatch(question)
	if len(m) >= 3 {
		val, err := strconv.ParseFloat(m[1], 64)
		if err == nil {
			if strings.ToUpper(m[2]) == "F" {
				val = (val - 32) * 5 / 9
			}
			return val
		}
	}

	// 3. Unitless "degrees" — guess by magnitude.
	dm := tempDegreesOnlyRe.FindStringSubmatch(question)
	if len(dm) >= 2 {
		val, err := strconv.ParseFloat(dm[1], 64)
		if err == nil {
			if val > 50 { // almost certainly Fahrenheit
				val = (val - 32) * 5 / 9
			}
			return val
		}
	}

	return 0
}

var cityPatterns = map[string]*regexp.Regexp{
	// TASK-048: added city nicknames alongside existing patterns.
	"new_york":      regexp.MustCompile(`(?i)new york|nyc|manhattan|\bbig apple\b`),
	"london":        regexp.MustCompile(`(?i)\blondon\b|uk weather`),
	"tokyo":         regexp.MustCompile(`(?i)\btokyo\b|japan weather`),
	"miami":         regexp.MustCompile(`(?i)\bmiami\b|florida weather`),
	"paris":         regexp.MustCompile(`(?i)\bparis\b|france weather|city of light`),
	"chicago":       regexp.MustCompile(`(?i)\bchicago\b|chi-town|windy city`),
	"los_angeles":   regexp.MustCompile(`(?i)los angeles|\bLA\b|l\.a\.|southern california`),
	"san_francisco": regexp.MustCompile(`(?i)san francisco|\bSF\b|bay area|s\.f\.|frisco|silicon valley`),
	"berlin":        regexp.MustCompile(`(?i)\bberlin\b|germany weather`),
}

func classify(question string) (city, sig string, thresholdC float64) {
	for _, s := range signals {
		if s.re.MatchString(question) {
			sig = s.sig
			break
		}
	}
	if sig == "" {
		return "", "", 0
	}
	for c, re := range cityPatterns {
		if re.MatchString(question) {
			city = c
			break
		}
	}

	// Parse temperature threshold for heat/cold signals
	if sig == "heat" || sig == "cold" {
		thresholdC = parseTempThresholdC(question)
	}

	return city, sig, thresholdC
}

// -- Polymarket CLOB types (minimal) --

type polyToken struct {
	Outcome string  `json:"outcome"`
	TokenID string  `json:"token_id"`
	Price   float64 `json:"price,string"`
}

type polyMarket struct {
	ConditionID string      `json:"condition_id"`
	Question    string      `json:"question"`
	Closed      bool        `json:"closed"`
	Archived    bool        `json:"archived"`
	Tokens      []polyToken `json:"tokens"`
	EndDateISO  string      `json:"end_date_iso"`
}

type polyResp struct {
	Data       []polyMarket `json:"data"`
	NextCursor string       `json:"next_cursor"`
}

var httpClient = &http.Client{Timeout: 20 * time.Second}

// HoursUntilExpiry returns the fractional hours from now until this market
// closes. Returns 0 when the end date is missing, already past, or cannot be
// parsed. Used for the near-expiry filter (TASK-037).
func (m Market) HoursUntilExpiry() float64 {
	if m.EndDate == "" {
		return 0
	}
	t, err := time.Parse(time.RFC3339, m.EndDate)
	if err != nil {
		t, err = time.Parse("2006-01-02T15:04:05Z", m.EndDate)
	}
	if err != nil {
		t, err = time.Parse("2006-01-02", m.EndDate)
	}
	if err != nil {
		return 0
	}
	h := time.Until(t).Hours()
	if h < 0 {
		return 0
	}
	return h
}

// DaysUntilExpiry returns the number of full days from now until this market
// closes, clamped to [0, 6] for safe forecast indexing.
// Returns 0 when the end date is missing, already past, or cannot be parsed.
func (m Market) DaysUntilExpiry() int {
	if m.EndDate == "" {
		return 0
	}
	// Polymarket uses RFC3339 or plain date strings.
	t, err := time.Parse(time.RFC3339, m.EndDate)
	if err != nil {
		t, err = time.Parse("2006-01-02T15:04:05Z", m.EndDate)
	}
	if err != nil {
		t, err = time.Parse("2006-01-02", m.EndDate)
	}
	if err != nil {
		return 0
	}
	days := int(time.Until(t).Hours() / 24)
	if days < 0 {
		return 0
	}
	if days > 6 {
		return 6
	}
	return days
}

// GetWeatherMarkets pages through Polymarket and returns weather-related markets.
func GetWeatherMarkets() ([]Market, error) {
	var out []Market
	cursor := ""

	for {
		url := polyHost + "/markets"
		if cursor != "" {
			url += "?next_cursor=" + cursor
		}

		resp, err := httpClient.Get(url)
		if err != nil {
			return nil, fmt.Errorf("polymarket request: %w", err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		var pr polyResp
		if err := json.Unmarshal(body, &pr); err != nil {
			return nil, fmt.Errorf("polymarket parse: %w", err)
		}

		for _, m := range pr.Data {
			if m.Closed || m.Archived {
				continue
			}
			city, sig, thresholdC := classify(m.Question)
			if sig == "" {
				continue
			}

			var yes, no polyToken
			for _, t := range m.Tokens {
				switch strings.ToLower(t.Outcome) {
				case "yes":
					yes = t
				case "no":
					no = t
				}
			}
			if yes.TokenID == "" || no.TokenID == "" {
				continue
			}

			out = append(out, Market{
				ConditionID: m.ConditionID,
				Question:    m.Question,
				YesTokenID:  yes.TokenID,
				NoTokenID:   no.TokenID,
				YesPrice:    yes.Price,
				NoPrice:     no.Price,
				City:        city,
				Signal:      sig,
				EndDate:     m.EndDateISO,
				ThresholdC:  thresholdC,
			})
		}

		if pr.NextCursor == "" || pr.NextCursor == "LTE=" {
			break
		}
		cursor = pr.NextCursor
	}

	return out, nil
}

// priceRefreshClient has a short timeout so stale-price checks don't block the
// bet loop for more than 2 seconds per market.
var priceRefreshClient = &http.Client{Timeout: 2 * time.Second}

// RefreshPrices fetches the latest YES/NO prices for a single market from the
// Polymarket CLOB API (GET /markets/{conditionID}).
//
// On success it returns an updated copy of m with fresh YesPrice/NoPrice and
// refreshed=true. On any error (timeout, 4xx, parse failure) it returns the
// original m unchanged with refreshed=false so the caller can decide whether
// to proceed with the stale price or skip the bet.
func RefreshPrices(m Market) (updated Market, refreshed bool, err error) {
	url := polyHost + "/markets/" + m.ConditionID
	resp, err := priceRefreshClient.Get(url)
	if err != nil {
		return m, false, fmt.Errorf("price refresh get: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return m, false, fmt.Errorf("price refresh: HTTP %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return m, false, fmt.Errorf("price refresh read: %w", err)
	}

	var pm polyMarket
	if err := json.Unmarshal(body, &pm); err != nil {
		return m, false, fmt.Errorf("price refresh parse: %w", err)
	}

	updated = m
	for _, t := range pm.Tokens {
		switch strings.ToLower(t.Outcome) {
		case "yes":
			updated.YesPrice = t.Price
		case "no":
			updated.NoPrice = t.Price
		}
	}
	return updated, true, nil
}
