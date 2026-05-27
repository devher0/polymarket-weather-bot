// fair_value.go — CLOB depth-weighted fair-value enrichment (TASK-128).
// Computes VWAP over the top-N order-book levels on both bid and ask sides,
// then sets FairYesPrice / FairNoPrice as the mid-point VWAP on each market.
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

const fairValueTopN = 5

var fairValueHTTPClient = &http.Client{Timeout: 5 * time.Second}

// DepthWeightedPrice computes a size-weighted average price (VWAP) over the
// best topN levels of an order-book side (bids or asks).
// Returns 0 if levels is empty.
func DepthWeightedPrice(levels []bookLevel, topN int) float64 {
	if len(levels) == 0 {
		return 0
	}
	n := topN
	if n > len(levels) {
		n = len(levels)
	}
	var totalSize, totalValue float64
	for i := 0; i < n; i++ {
		price, err1 := strconv.ParseFloat(levels[i].Price, 64)
		size, err2 := strconv.ParseFloat(levels[i].Size, 64)
		if err1 != nil || err2 != nil || size <= 0 {
			continue
		}
		totalSize += size
		totalValue += price * size
	}
	if totalSize == 0 {
		return 0
	}
	return totalValue / totalSize
}

// FetchFairValue fetches the CLOB order book for yesTokenID and computes
// VWAP-based fair prices for both the YES and NO sides.
//
//   - fairYes = mid-point between VWAP(asks) and VWAP(bids) of the YES book
//   - fairNo  = 1 - fairYes  (binary market)
//
// Returns (0, 0, err) on any failure so callers can fall back to last-trade prices.
func FetchFairValue(tokenID string) (fairYes, fairNo float64, err error) {
	url := fmt.Sprintf("%s?token_id=%s", clobBookURL, tokenID)
	resp, err := fairValueHTTPClient.Get(url)
	if err != nil {
		return 0, 0, fmt.Errorf("fair_value: request: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	var book orderBook
	if err := json.Unmarshal(body, &book); err != nil {
		return 0, 0, fmt.Errorf("fair_value: parse: %w", err)
	}

	if len(book.Bids) == 0 && len(book.Asks) == 0 {
		return 0, 0, fmt.Errorf("fair_value: empty book")
	}

	vwapBid := DepthWeightedPrice(book.Bids, fairValueTopN)
	vwapAsk := DepthWeightedPrice(book.Asks, fairValueTopN)

	switch {
	case vwapBid > 0 && vwapAsk > 0:
		fairYes = (vwapBid + vwapAsk) / 2
	case vwapAsk > 0:
		fairYes = vwapAsk
	case vwapBid > 0:
		fairYes = vwapBid
	default:
		return 0, 0, fmt.Errorf("fair_value: could not compute VWAP")
	}

	fairNo = 1 - fairYes
	return fairYes, fairNo, nil
}

// enrichFairValue fetches the CLOB fair value for m and sets FairYesPrice /
// FairNoPrice in place. Errors are logged as debug; a failed fetch leaves the
// fields at 0 (callers fall back to YesPrice / NoPrice).
func enrichFairValue(m *Market) {
	if m.YesTokenID == "" {
		return
	}
	fairYes, fairNo, err := FetchFairValue(m.YesTokenID)
	if err != nil {
		slog.Debug("fair_value: fetch failed", "conditionID", m.ConditionID, "err", err)
		return
	}
	m.FairYesPrice = fairYes
	m.FairNoPrice = fairNo
	slog.Debug("fair_value enriched",
		"conditionID", m.ConditionID,
		"yes_last", fmt.Sprintf("%.3f", m.YesPrice),
		"fair_yes", fmt.Sprintf("%.3f", fairYes),
		"fair_no", fmt.Sprintf("%.3f", fairNo),
	)
}
