// correlation.go — prevents placing correlated bets on geographically or
// climatically related cities within the same trading cycle.
//
// When two cities share a high weather correlation (e.g. London–Paris = 0.80)
// AND the same signal direction, betting both would effectively double the
// exposure to a single weather pattern — offsetting the diversification
// benefits of a multi-city strategy.
//
// Rule: if correlation > 0.75 AND same signal → skip the second market.
package risk

import (
	"fmt"
	"log/slog"

	"github.com/devher0/polymarket-weather-bot/internal/markets"
)

// cityCorrelations maps ordered city-pair keys to Pearson-like weather
// correlation coefficients derived from historical seasonal co-movement.
// Only pairs with r > correlationThreshold need to be listed.
var cityCorrelations = map[[2]string]float64{
	// East Coast USA — driven by the same Atlantic synoptic systems
	{"new_york", "miami"}:   0.70,
	{"miami", "new_york"}:   0.70,

	// Western Europe — dominated by shared Atlantic low-pressure tracks
	{"london", "paris"}:     0.80,
	{"paris", "london"}:     0.80,

	// California — nearly identical Mediterranean climate
	{"los_angeles", "san_francisco"}: 0.85,
	{"san_francisco", "los_angeles"}: 0.85,

	// US Midwest–East corridor
	{"chicago", "new_york"}: 0.65,
	{"new_york", "chicago"}: 0.65,
}

// correlationThreshold is the minimum r above which we consider positions
// to be "too correlated" to both hold simultaneously with the same signal.
const correlationThreshold = 0.75

// CorrelatedCitiesOpen returns (true, reason) if any of the placedMarkets
// is correlated with m above the threshold AND has the same signal direction.
// Returns (false, "") when no correlation conflict is found.
//
// placedMarkets is typically the slice of markets for which bets have already
// been placed in the current bot cycle.
func CorrelatedCitiesOpen(m markets.Market, placedMarkets []markets.Market) (bool, string) {
	if m.City == "" || m.Signal == "" {
		return false, ""
	}

	for _, placed := range placedMarkets {
		if placed.City == "" || placed.Signal == "" {
			continue
		}
		if placed.City == m.City {
			continue // same city is handled by dedup, not correlation guard
		}

		r := cityCorrelations[[2]string{m.City, placed.City}]
		if r <= correlationThreshold {
			continue
		}

		if placed.Signal != m.Signal {
			continue // different signals can coexist even in correlated cities
		}

		reason := fmt.Sprintf("correlated position in %s (r=%.2f, signal=%s)",
			placed.City, r, m.Signal)
		slog.Info("skipped: "+reason, "conditionID", m.ConditionID,
			"question", m.Question)
		return true, reason
	}

	return false, ""
}

// CityCorrelation returns the correlation coefficient for a city pair,
// or 0 if no entry exists. Exposed for testing and dashboard display.
func CityCorrelation(cityA, cityB string) float64 {
	return cityCorrelations[[2]string{cityA, cityB}]
}
