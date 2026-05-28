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

// polyHost is the Polymarket CLOB API base URL.
// Overridable in tests via SetPolyHost.
var polyHost = "https://clob.polymarket.com"

// SetPolyHost overrides the Polymarket CLOB base URL. For testing only.
func SetPolyHost(u string) { polyHost = u }

// Market represents a single Polymarket weather prediction market.
type Market struct {
	ConditionID    string
	Question       string
	YesTokenID     string
	NoTokenID      string
	YesPrice       float64
	NoPrice        float64
	City           string  // may be empty if no city matched
	Signal         string  // rain|heat|cold|snow|wind|sunny
	EndDate        string
	ThresholdC     float64    // parsed temperature threshold in Celsius (0 = not set)
	ThinLiquidity  bool       // true when top-of-book bid-ask spread > 0.10 (set by EnrichWithLiquidity)
	Spread         float64    // top-of-book bid-ask spread (set by EnrichWithLiquidity)
	Stale          bool       // TASK-063: no trades >24h AND spread > 0.08
	LastTradeTime  time.Time  // TASK-063: timestamp of last recorded trade (zero if unknown)
	ExpiryUTC      time.Time  // TASK-079: parsed expiry time (UTC); zero if unparseable
	// TASK-128: CLOB depth-weighted VWAP fair value (set by EnrichWithLiquidity).
	// Zero means not yet fetched; strategy should fall back to YesPrice/NoPrice.
	FairYesPrice float64
	FairNoPrice  float64
	// TASK-175: total traded volume in USDC parsed from the API response.
	// Zero means the field was absent in the response (not necessarily zero volume).
	VolumeUSDC float64
	// TASK-221: true when VolumeUSDC > 10 000 USDC — signals reliable price discovery.
	HighVolume bool
}

type signal struct {
	re  *regexp.Regexp
	sig string
}

var signals = []signal{
	{regexp.MustCompile(`(?i)rain|precipitation|rainfall|rainy`), "rain"},
	// TASK-048: added "exceed" and temperature range as heat indicator.
	// Also matches "highest temperature in London" / "maximum temp" range markets.
	{regexp.MustCompile(`(?i)temperature.{0,20}above|above.{0,10}\d+.{0,10}degree|exceed.{0,10}\d+.{0,10}degree|heat.?wave|heatwave|hot day|between\s+\d+.*and\s+\d+.*°[CF]|highest.{0,10}temperature|max(?:imum)?.{0,10}temp`), "heat"},
	{regexp.MustCompile(`(?i)temperature.{0,20}below|below.{0,10}\d+.{0,10}degree|cold snap|freeze`), "cold"},
	{regexp.MustCompile(`(?i)snow|snowfall|blizzard`), "snow"},
	{regexp.MustCompile(`(?i)wind|hurricane|typhoon|storm`), "wind"},
	{regexp.MustCompile(`(?i)sunny|sunshine|clear sky`), "sunny"},
	// TASK-048: additional signals
	{regexp.MustCompile(`(?i)\bfog\b|foggy|misty|mist`), "fog"},
	{regexp.MustCompile(`(?i)humid(?:ity)?|dew point`), "humid"},
	{regexp.MustCompile(`(?i)\bdry\b|drought|arid`), "dry"},
	// TASK-083: UV index signal — must appear before general number regexes
	{regexp.MustCompile(`(?i)\buv.?index\b|\buv\s+level\b|ultraviolet\s+index`), "uv"},
}

// uvThresholdRe extracts a UV index threshold from questions like
// "UV index above 8", "UV index exceeds 10", "UV level of 6".
// TASK-083: matches integer UV values 1-12 following UV keywords.
var uvThresholdRe = regexp.MustCompile(`(?i)(?:uv.?index|uv\s+level|ultraviolet)[^\d]{0,20}(\d{1,2})`)

// parseUVThreshold returns the numeric UV index threshold from a market question,
// or 0 if none found. Valid UV index range is 1–12+.
// TASK-083.
func parseUVThreshold(question string) float64 {
	m := uvThresholdRe.FindStringSubmatch(question)
	if len(m) >= 2 {
		val, err := strconv.ParseFloat(m[1], 64)
		if err == nil && val >= 1 && val <= 20 {
			return val
		}
	}
	return 0
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

// outcomeThresholdRe extracts a temperature value from outcome strings like
// "26°C", "20°C or below", "30°C or higher", "75°F".
var outcomeThresholdRe = regexp.MustCompile(`(\d+(?:\.\d+)?)\s*°?\s*([CF])\b`)

// parseTempThresholdFromOutcome extracts the temperature threshold in Celsius
// from a token outcome string (e.g., "26°C" → 26.0, "75°F" → ~23.9).
// Returns 0 if no temperature is found.
func parseTempThresholdFromOutcome(outcome string) float64 {
	m := outcomeThresholdRe.FindStringSubmatch(outcome)
	if len(m) >= 3 {
		val, err := strconv.ParseFloat(m[1], 64)
		if err == nil {
			if strings.ToUpper(m[2]) == "F" {
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

	// TASK-083: parse UV index threshold (stored in ThresholdC field, default 8 = "very high")
	if sig == "uv" {
		thresholdC = parseUVThreshold(question)
		if thresholdC == 0 {
			thresholdC = 8 // sensible default: "UV index above 8" = very high UV
		}
	}

	return city, sig, thresholdC
}

// -- Polymarket CLOB types (minimal) --

type polyToken struct {
	Outcome string      `json:"outcome"`
	TokenID string      `json:"token_id"`
	Price   json.Number `json:"price"`
}

func (t polyToken) price() float64 {
	if t.Price == "" {
		return 0
	}
	v, _ := t.Price.Float64()
	return v
}

type polyMarket struct {
	ConditionID    string      `json:"condition_id"`
	Question       string      `json:"question"`
	Closed         bool        `json:"closed"`
	Archived       bool        `json:"archived"`
	Tokens         []polyToken `json:"tokens"`
	EndDateISO     string      `json:"end_date_iso"`
	LastTradePrice string      `json:"last_trade_price"` // TASK-063: decimal string; "" if never traded
	LastTradeSize  string      `json:"last_trade_size"`  // TASK-063: decimal string; unused but kept for completeness
	// Polymarket Gamma API uses "last_traded_at" (RFC3339); CLOB API may omit it.
	LastTradedAt string `json:"last_traded_at"` // TASK-063: RFC3339 timestamp or ""
	// TASK-175: total traded volume; Gamma API returns this as a decimal string.
	Volume string `json:"volume"`
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
// Results are cached to disk for up to 1 hour (TASK-170) to reduce API calls.
func GetWeatherMarkets() ([]Market, error) {
	if cached, ok := loadMarketCache(); ok {
		return cached, nil
	}

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

			// For highest-temperature range markets the threshold is not in the
			// question itself but in the YES token outcome (e.g., "26°C", "30°C or higher").
			// Fall back to parsing the outcome when the question parse returned nothing.
			if sig == "heat" && thresholdC == 0 && yes.Outcome != "" {
				thresholdC = parseTempThresholdFromOutcome(yes.Outcome)
			}

			// TASK-063: parse last trade timestamp to determine staleness.
			var lastTrade time.Time
			if m.LastTradedAt != "" {
				if t, err := time.Parse(time.RFC3339, m.LastTradedAt); err == nil {
					lastTrade = t
				}
			}

			// TASK-079: parse ExpiryUTC from EndDateISO for use in rain-window probability.
			var expiryUTC time.Time
			if m.EndDateISO != "" {
				if t, err := time.Parse(time.RFC3339, m.EndDateISO); err == nil {
					expiryUTC = t.UTC()
				} else if t, err := time.Parse("2006-01-02T15:04:05Z", m.EndDateISO); err == nil {
					expiryUTC = t.UTC()
				} else if t, err := time.Parse("2006-01-02", m.EndDateISO); err == nil {
					// Plain date: treat as end-of-day UTC (23:59:59).
					expiryUTC = t.Add(23*time.Hour + 59*time.Minute + 59*time.Second).UTC()
				}
			}

			// TASK-175: parse volume string → float64 (0 when absent/unparseable).
			var volumeUSDC float64
			if m.Volume != "" {
				if v, err := strconv.ParseFloat(m.Volume, 64); err == nil {
					volumeUSDC = v
				}
			}

			out = append(out, Market{
				ConditionID:   m.ConditionID,
				Question:      m.Question,
				YesTokenID:    yes.TokenID,
				NoTokenID:     no.TokenID,
				YesPrice:      yes.price(),
				NoPrice:       no.price(),
				City:          city,
				Signal:        sig,
				EndDate:       m.EndDateISO,
				ThresholdC:    thresholdC,
				LastTradeTime: lastTrade,
				ExpiryUTC:     expiryUTC,
				VolumeUSDC:    volumeUSDC,
				HighVolume:    volumeUSDC >= 10_000,
			})
		}

		if pr.NextCursor == "" || pr.NextCursor == "LTE=" {
			break
		}
		cursor = pr.NextCursor
	}

	if len(out) > 0 {
		saveMarketCache(out)
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
			updated.YesPrice = t.price()
		case "no":
			updated.NoPrice = t.price()
		}
	}
	return updated, true, nil
}
