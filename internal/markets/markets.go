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
	ConditionID string
	Question    string
	YesTokenID  string
	NoTokenID   string
	YesPrice    float64
	NoPrice     float64
	City        string  // may be empty if no city matched
	Signal      string  // rain|heat|cold|snow|wind|sunny
	EndDate     string
	ThresholdC  float64 // parsed temperature threshold in Celsius (0 = not set)
}

type signal struct {
	re  *regexp.Regexp
	sig string
}

var signals = []signal{
	{regexp.MustCompile(`(?i)rain|precipitation|rainfall|rainy`), "rain"},
	{regexp.MustCompile(`(?i)temperature.{0,20}above|above.{0,10}\d+.{0,10}degree|heat.?wave|heatwave|hot day`), "heat"},
	{regexp.MustCompile(`(?i)temperature.{0,20}below|below.{0,10}\d+.{0,10}degree|cold snap|freeze`), "cold"},
	{regexp.MustCompile(`(?i)snow|snowfall|blizzard`), "snow"},
	{regexp.MustCompile(`(?i)wind|hurricane|typhoon|storm`), "wind"},
	{regexp.MustCompile(`(?i)sunny|sunshine|clear sky`), "sunny"},
}

// tempThresholdRe extracts a numeric temperature and optional F/C unit from market questions.
// Examples: "above 95°F", "above 35°C", "exceed 40 degrees", "above 100F"
var tempThresholdRe = regexp.MustCompile(`(\d+(?:\.\d+)?)\s*°?\s*([FC])\b`)

// parseTempThresholdC extracts a temperature threshold from a market question
// and returns it in Celsius. Returns 0 if none found.
func parseTempThresholdC(question string) float64 {
	m := tempThresholdRe.FindStringSubmatch(question)
	if len(m) < 3 {
		return 0
	}
	val, err := strconv.ParseFloat(m[1], 64)
	if err != nil {
		return 0
	}
	unit := strings.ToUpper(m[2])
	if unit == "F" {
		// Convert Fahrenheit → Celsius
		val = (val - 32) * 5 / 9
	}
	return val
}

var cityPatterns = map[string]*regexp.Regexp{
	"new_york":      regexp.MustCompile(`(?i)new york|nyc|manhattan|NYC|Chi-town`),
	"london":        regexp.MustCompile(`(?i)\blondon\b|uk weather`),
	"tokyo":         regexp.MustCompile(`(?i)\btokyo\b|japan weather`),
	"miami":         regexp.MustCompile(`(?i)\bmiami\b|florida weather`),
	"paris":         regexp.MustCompile(`(?i)\bparis\b|france weather`),
	"chicago":       regexp.MustCompile(`(?i)\bchicago\b|chi-town`),
	"los_angeles":   regexp.MustCompile(`(?i)los angeles|\bLA\b|l\.a\.|southern california`),
	"san_francisco": regexp.MustCompile(`(?i)san francisco|\bSF\b|bay area|s\.f\.|frisco`),
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
