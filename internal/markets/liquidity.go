// liquidity.go — fetches CLOB order book depth and marks thin markets.
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
	clobBookURL         = "https://clob.polymarket.com/book"
	thinSpreadThreshold = 0.10 // 10-cent spread limit
)

type bookLevel struct {
	Price string `json:"price"`
	Size  string `json:"size"`
}

type orderBook struct {
	Bids []bookLevel `json:"bids"`
	Asks []bookLevel `json:"asks"`
}

var liquidityHTTPClient = &http.Client{Timeout: 10 * time.Second}

// checkSpread fetches the CLOB order book for tokenID and returns the
// top-of-book bid-ask spread.  On any error it returns (1.0, true, err)
// so callers can treat unavailable books as thin.
func checkSpread(tokenID string) (float64, bool, error) {
	url := fmt.Sprintf("%s?token_id=%s", clobBookURL, tokenID)
	resp, err := liquidityHTTPClient.Get(url)
	if err != nil {
		return 1.0, true, fmt.Errorf("liquidity: request: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	var book orderBook
	if err := json.Unmarshal(body, &book); err != nil {
		return 1.0, true, fmt.Errorf("liquidity: parse book: %w", err)
	}

	if len(book.Bids) == 0 || len(book.Asks) == 0 {
		return 1.0, true, nil // empty book = extremely thin
	}

	bestBid, err := strconv.ParseFloat(book.Bids[0].Price, 64)
	if err != nil {
		return 1.0, true, fmt.Errorf("liquidity: parse bid %q: %w", book.Bids[0].Price, err)
	}
	bestAsk, err := strconv.ParseFloat(book.Asks[0].Price, 64)
	if err != nil {
		return 1.0, true, fmt.Errorf("liquidity: parse ask %q: %w", book.Asks[0].Price, err)
	}

	spread := bestAsk - bestBid
	thin := spread > thinSpreadThreshold
	return spread, thin, nil
}

// EnrichWithLiquidity fetches the CLOB order book for the YES token of each
// market and sets ThinLiquidity and Spread in-place.
// Errors per market are logged as warnings and do not abort the batch.
func EnrichWithLiquidity(mkts []Market) {
	for i := range mkts {
		if mkts[i].YesTokenID == "" {
			continue
		}
		spread, thin, err := checkSpread(mkts[i].YesTokenID)
		if err != nil {
			slog.Warn("liquidity check failed",
				"conditionID", mkts[i].ConditionID,
				"err", err,
			)
			continue
		}
		mkts[i].Spread = spread
		mkts[i].ThinLiquidity = thin
		if thin {
			q := mkts[i].Question
			if len(q) > 60 {
				q = q[:60] + "…"
			}
			slog.Info("thin market detected",
				"conditionID", mkts[i].ConditionID,
				"spread", fmt.Sprintf("%.3f", spread),
				"question", q,
			)
		}
	}
}
