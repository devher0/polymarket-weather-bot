// momentum.go — in-memory market price momentum tracker.
// Detects when YES price moves toward our probability estimate between cycles,
// which is evidence the market is confirming our forecast.
package markets

import "sync"

var priceCache sync.Map // map[conditionID string] → float64

// RecordPrice stores the YES price for a market at the end of a cycle.
// Called by strategy.EvaluateFused after the decision is made.
func RecordPrice(conditionID string, yesPrice float64) {
	priceCache.Store(conditionID, yesPrice)
}

// PriceDelta returns the change in YES price since the last recorded cycle.
// Returns (delta, true) when a previous price exists; (0, false) on first call.
// A positive delta means the YES price rose.
func PriceDelta(conditionID string, currentPrice float64) (delta float64, ok bool) {
	v, found := priceCache.Load(conditionID)
	if !found {
		return 0, false
	}
	prev, _ := v.(float64)
	return currentPrice - prev, true
}

// IsConfirming returns true when the price moved at least minDelta toward ourP.
// threshold is typically 0.03 (3 percentage points).
func IsConfirming(conditionID string, currentPrice, ourP, threshold float64) bool {
	delta, ok := PriceDelta(conditionID, currentPrice)
	if !ok || delta == 0 {
		return false
	}
	// We bet YES when ourP > currentPrice (price should rise) → delta > 0 confirms.
	// We bet NO when ourP < currentPrice (price should fall) → delta < 0 confirms.
	if ourP > currentPrice && delta >= threshold {
		return true
	}
	if ourP < currentPrice && delta <= -threshold {
		return true
	}
	return false
}
