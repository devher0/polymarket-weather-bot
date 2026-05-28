// signal_correlation.go — prevents betting on anti-correlated signals within
// the same city in a single trading cycle.
//
// Some signal pairs are mutually exclusive by definition: if it's raining in
// New York today, it cannot simultaneously be a "sunny" day. Betting both YES
// would be internally inconsistent and expose the bankroll to needless
// concentration risk — one bet wins and the other almost certainly loses.
//
// Rule: if an already-placed bet in the same city has an anti-correlated
// signal with the candidate market, skip the candidate.
package risk

import (
	"fmt"
	"log/slog"

	"github.com/devher0/polymarket-weather-bot/internal/markets"
)

// antiCorrelatedPairs lists signal pairs that are climatologically
// anti-correlated within the same city on the same day.
// The map is keyed by ordered pair [A, B]; both directions are listed.
var antiCorrelatedPairs = map[[2]string]bool{
	// Precipitation vs clear-sky
	{"rain", "sunny"}: true, {"sunny", "rain"}: true,
	{"rain", "dry"}:   true, {"dry", "rain"}: true,

	// Temperature extremes
	{"heat", "cold"}: true, {"cold", "heat"}: true,
	{"snow", "heat"}: true, {"heat", "snow"}: true,

	// Visibility conflicts
	{"fog", "sunny"}: true, {"sunny", "fog"}: true,
}

// SignalsAntiCorrelated reports whether two signal names are climatologically
// anti-correlated (i.e. cannot both be "YES" on the same day in the same city).
// Exposed for testing.
func SignalsAntiCorrelated(s1, s2 string) bool {
	return antiCorrelatedPairs[[2]string{s1, s2}]
}

// IntraCitySignalConflict returns (true, reason) when any already-placed bet
// in placedMarkets has the same City as m AND an anti-correlated Signal.
// Returns (false, "") when the candidate market is safe to bet.
//
// placedMarkets is the slice of markets for which bets have been placed
// in the current cycle.
func IntraCitySignalConflict(m markets.Market, placedMarkets []markets.Market) (bool, string) {
	if m.City == "" || m.Signal == "" {
		return false, ""
	}
	for _, placed := range placedMarkets {
		if placed.City != m.City {
			continue
		}
		if placed.Signal == m.Signal {
			continue // same signal — handled by per-market dedup
		}
		if !SignalsAntiCorrelated(m.Signal, placed.Signal) {
			continue
		}
		reason := fmt.Sprintf("intra-city anti-correlation: %s/%s conflicts with placed %s/%s",
			m.City, m.Signal, placed.City, placed.Signal)
		slog.Info("skipped: "+reason, "conditionID", m.ConditionID)
		return true, reason
	}
	return false, ""
}
