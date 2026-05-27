// sentiment.go — order flow imbalance analysis from the Polymarket CLOB.
//
// Order flow imbalance (OFI) measures buying vs selling pressure in the order book:
//
//	OFI = (bid_volume - ask_volume) / (bid_volume + ask_volume)  →  [-1, 1]
//
//	+1.0 = all bids, no asks (strong buying pressure → price likely rising)
//	 0.0 = balanced order flow
//	-1.0 = all asks, no bids (strong selling pressure → price likely falling)
//
// Usage in strategy:
//   - Positive OFI + our YES forecast → +5% edge boost (smart money agrees)
//   - Negative OFI + our YES forecast → cautious (-3% edge) or skip at strong -OFI
//   - Neutral OFI (|ofi| < 0.15) → no adjustment
package markets

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"time"
)

const (
	// ofiPositiveThreshold — OFI above this value signals buying pressure.
	ofiPositiveThreshold = 0.15
	// ofiNegativeThreshold — OFI below this value signals selling pressure.
	ofiNegativeThreshold = -0.15
	// ofiEdgeBoost is the edge adjustment for aligned order flow (fraction, not %).
	ofiEdgeBoost = 0.05
	// ofiEdgePenalty is the edge reduction for adverse order flow.
	ofiEdgePenalty = 0.03
)

var sentimentHTTPClient = &http.Client{Timeout: 5 * time.Second}

// OrderFlowResult holds the computed OFI for a token.
type OrderFlowResult struct {
	TokenID         string
	BidVolume       float64 // total volume across all bid levels
	AskVolume       float64 // total volume across all ask levels
	Imbalance       float64 // OFI ∈ [-1, 1]; NaN if unavailable
	Available       bool    // false when the CLOB request failed
}

// FetchOrderFlow fetches the full order book for tokenID from the Polymarket CLOB
// and returns the order flow imbalance.  On error, Available is set to false
// and Imbalance is 0 (neutral) so callers can always read the field safely.
func FetchOrderFlow(tokenID string) OrderFlowResult {
	if tokenID == "" {
		return OrderFlowResult{TokenID: tokenID}
	}

	url := fmt.Sprintf("%s/book?token_id=%s", polyHost, tokenID)
	resp, err := sentimentHTTPClient.Get(url)
	if err != nil {
		slog.Debug("order flow fetch failed", "token_id", tokenID, "err", err)
		return OrderFlowResult{TokenID: tokenID}
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	var book orderBook // reuses the orderBook type from liquidity.go
	if err := json.Unmarshal(body, &book); err != nil {
		slog.Debug("order flow parse failed", "token_id", tokenID, "err", err)
		return OrderFlowResult{TokenID: tokenID}
	}

	bidVol := sumVolume(book.Bids)
	askVol := sumVolume(book.Asks)
	total := bidVol + askVol
	if total == 0 {
		return OrderFlowResult{TokenID: tokenID, Available: true} // empty book = neutral
	}

	ofi := (bidVol - askVol) / total
	slog.Debug("order flow computed",
		"token_id", tokenID,
		"bid_vol", fmt.Sprintf("%.2f", bidVol),
		"ask_vol", fmt.Sprintf("%.2f", askVol),
		"ofi", fmt.Sprintf("%.3f", ofi),
	)
	return OrderFlowResult{
		TokenID:   tokenID,
		BidVolume: bidVol,
		AskVolume: askVol,
		Imbalance: ofi,
		Available: true,
	}
}

// sumVolume sums the Size field of all order book levels.
func sumVolume(levels []bookLevel) float64 {
	var total float64
	for _, l := range levels {
		v, err := strconv.ParseFloat(l.Size, 64)
		if err == nil {
			total += v
		}
	}
	return total
}

// EdgeAdjustment returns the edge delta to apply when order flow is available.
//
// Logic:
//   - If OFI is positive (buyers dominate) and we plan to bet YES → +ofiEdgeBoost
//   - If OFI is negative (sellers dominate) and we plan to bet YES → -ofiEdgePenalty
//   - Inverse applies for NO bets.
//   - When OFI magnitude < ofiPositiveThreshold → no adjustment.
//
// The returned value should be added to the effective edge before the min-edge gate.
// Returns 0 when the result is not Available.
func (r OrderFlowResult) EdgeAdjustment(side string) float64 {
	if !r.Available {
		return 0
	}
	ofi := r.Imbalance

	switch side {
	case "YES":
		if ofi > ofiPositiveThreshold {
			return ofiEdgeBoost
		}
		if ofi < ofiNegativeThreshold {
			return -ofiEdgePenalty
		}
	case "NO":
		// NO side benefits when sellers dominate (YES price falling = NO price rising).
		if ofi < ofiNegativeThreshold {
			return ofiEdgeBoost
		}
		if ofi > ofiPositiveThreshold {
			return -ofiEdgePenalty
		}
	}
	return 0
}

// Description returns a human-readable label for the OFI value.
func (r OrderFlowResult) Description() string {
	if !r.Available {
		return "unavailable"
	}
	switch {
	case r.Imbalance > 0.50:
		return "strong_buy"
	case r.Imbalance > ofiPositiveThreshold:
		return "buy"
	case r.Imbalance < -0.50:
		return "strong_sell"
	case r.Imbalance < ofiNegativeThreshold:
		return "sell"
	default:
		return "neutral"
	}
}
