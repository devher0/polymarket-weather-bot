// Package calibration — unrealized P&L fetching for open positions.
package calibration

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// priceToken is the YES/NO price entry inside the Gamma API market object.
type priceToken struct {
	Outcome string  `json:"outcome"`
	Price   float64 `json:"price"`
}

// priceMarketResp is the minimal Gamma API market shape needed to extract prices.
type priceMarketResp struct {
	Tokens []priceToken `json:"tokens"`
}

// UnrealizedPosition extends BetRecord with current market price data.
type UnrealizedPosition struct {
	BetRecord
	CurrentPrice  float64   // current market price for our side (0 if unavailable)
	UnrealizedPnL float64   // estimated unrealized profit/loss in USDC
	PriceChange   float64   // currentPrice - entryPrice (signed)
	FetchedAt     time.Time
	FetchError    string // non-empty if price fetch failed
}

// FetchUnrealizedPnL fetches the latest market price for each open (unresolved)
// bet and computes the unrealized profit/loss.
//
// Errors per position are captured in UnrealizedPosition.FetchError; the slice
// is always returned (never nil).
func FetchUnrealizedPnL(records []BetRecord) []UnrealizedPosition {
	var positions []UnrealizedPosition
	for _, r := range records {
		if r.Outcome != nil {
			continue // already resolved
		}
		pos := UnrealizedPosition{
			BetRecord: r,
			FetchedAt: time.Now(),
		}
		cur, err := fetchCurrentSidePrice(r.ConditionID, r.Side)
		if err != nil {
			pos.FetchError = err.Error()
		} else {
			pos.CurrentPrice = cur
			if r.MarketPrice > 0 {
				shares := r.SizeUSDC / r.MarketPrice
				pos.UnrealizedPnL = shares * (cur - r.MarketPrice)
			}
			pos.PriceChange = cur - r.MarketPrice
		}
		positions = append(positions, pos)
	}
	return positions
}

// TotalUnrealizedPnL sums unrealized P&L across all positions that had no fetch error.
func TotalUnrealizedPnL(positions []UnrealizedPosition) float64 {
	var total float64
	for _, p := range positions {
		if p.FetchError == "" {
			total += p.UnrealizedPnL
		}
	}
	return total
}

// fetchCurrentSidePrice queries the Polymarket Gamma API for the current price
// of the given side ("YES" or "NO") in the given market.
// Reuses resolverClient (15 s timeout) from resolver.go (same package).
func fetchCurrentSidePrice(conditionID, side string) (float64, error) {
	url := fmt.Sprintf(gammaMarketURL, conditionID)
	resp, err := resolverClient.Get(url)
	if err != nil {
		return 0, fmt.Errorf("gamma get: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return 0, fmt.Errorf("gamma HTTP %d", resp.StatusCode)
	}

	// Decode directly into our price struct so we don't need to re-read body.
	var pm priceMarketResp
	if err := json.NewDecoder(resp.Body).Decode(&pm); err != nil {
		return 0, fmt.Errorf("gamma parse: %w", err)
	}

	// Gamma might also wrap as an array of one element.
	// If Tokens is empty, try array form.
	if len(pm.Tokens) == 0 {
		return 0, fmt.Errorf("no tokens in Gamma response for %s", conditionID)
	}

	for _, t := range pm.Tokens {
		if strings.EqualFold(t.Outcome, side) {
			return t.Price, nil
		}
	}
	return 0, fmt.Errorf("side %q not found in Gamma response", side)
}
