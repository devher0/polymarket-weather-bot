// slippage.go — TASK-176: Pre-trade slippage guard.
//
// Simulates executing a market order against the CLOB order book to estimate
// the average fill price and price impact (slippage). High slippage indicates
// thin liquidity — the order itself would move the market against us.
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

// slippageThresholdReduce is the slippage level above which we halve the order size.
const slippageThresholdReduce = 0.03 // 3 cents

// slippageThresholdSkip is the slippage level above which we skip the bet entirely.
const slippageThresholdSkip = 0.07 // 7 cents

var slippageHTTPClient = &http.Client{Timeout: 3 * time.Second}

// EstimateSlippage simulates filling a buy order of `sizeUSDC` against the
// provided order-book levels (should be the ask side for a buy).
//
// It walks levels from best to worst, filling `sizeUSDC` worth of shares at
// each price until the order is fully consumed.
//
// Returns the difference between the size-weighted average fill price and the
// best (first) level price. Returns 0 when the book is empty or the order
// would be filled entirely at the best level.
func EstimateSlippage(sizeUSDC float64, levels []bookLevel) float64 {
	if len(levels) == 0 || sizeUSDC <= 0 {
		return 0
	}

	bestPrice, err := strconv.ParseFloat(levels[0].Price, 64)
	if err != nil || bestPrice <= 0 {
		return 0
	}

	var filledUSDC, weightedPriceSum float64
	remaining := sizeUSDC

	for _, lv := range levels {
		if remaining <= 0 {
			break
		}
		price, err1 := strconv.ParseFloat(lv.Price, 64)
		size, err2 := strconv.ParseFloat(lv.Size, 64)
		if err1 != nil || err2 != nil || price <= 0 || size <= 0 {
			continue
		}
		// USDC available at this level = size (shares) × price
		levelUSDC := size * price
		fillUSDC := remaining
		if fillUSDC > levelUSDC {
			fillUSDC = levelUSDC
		}
		weightedPriceSum += price * fillUSDC
		filledUSDC += fillUSDC
		remaining -= fillUSDC
	}

	if filledUSDC <= 0 {
		return 0
	}
	avgFillPrice := weightedPriceSum / filledUSDC
	return avgFillPrice - bestPrice
}

// FetchBookForToken fetches the CLOB order book for the given token ID.
// Returns the raw orderBook struct; callers choose which side to use.
func FetchBookForToken(tokenID string) (orderBook, error) {
	url := fmt.Sprintf("%s?token_id=%s", clobBookURL, tokenID)
	resp, err := slippageHTTPClient.Get(url)
	if err != nil {
		return orderBook{}, fmt.Errorf("slippage: request: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	var book orderBook
	if err := json.Unmarshal(body, &book); err != nil {
		return orderBook{}, fmt.Errorf("slippage: parse book: %w", err)
	}
	return book, nil
}

// SlippageResult summarises the slippage estimate for one potential bet.
type SlippageResult struct {
	Slippage    float64 // size-weighted avg fill price minus best price
	AdjustedSize float64 // recommended size after slippage adjustment (may equal original)
	Skip        bool    // true = skip the bet entirely (slippage too high)
}

// CheckSlippage fetches the CLOB order book and estimates price impact for a
// potential bet of `sizeUSDC`. `buyingYes` indicates whether we are buying the
// YES token (true) or the NO token (false).
//
// Behaviour:
//   - slippage > slippageThresholdSkip (7¢) → Skip=true
//   - slippage > slippageThresholdReduce (3¢) → AdjustedSize = sizeUSDC × 0.5
//   - otherwise → AdjustedSize = sizeUSDC
//
// On any fetch error the function returns a zero-slippage result (proceed normally).
func CheckSlippage(tokenID string, sizeUSDC float64, buyingYes bool) SlippageResult {
	book, err := FetchBookForToken(tokenID)
	if err != nil {
		slog.Debug("slippage: book fetch failed, proceeding normally",
			"tokenID", tokenID, "err", err)
		return SlippageResult{AdjustedSize: sizeUSDC}
	}

	var levels []bookLevel
	if buyingYes {
		// Buying YES: we take from the asks (sellers of YES).
		levels = book.Asks
	} else {
		// Buying NO: equivalent to selling YES, so we take from the bids.
		levels = book.Bids
	}

	slippage := EstimateSlippage(sizeUSDC, levels)

	if slippage > slippageThresholdSkip {
		slog.Info("slippage: high slippage — skipping bet",
			"tokenID", tokenID,
			"slippage", fmt.Sprintf("%.3f", slippage),
			"threshold_skip", fmt.Sprintf("%.3f", slippageThresholdSkip),
		)
		return SlippageResult{Slippage: slippage, AdjustedSize: sizeUSDC, Skip: true}
	}

	if slippage > slippageThresholdReduce {
		reduced := sizeUSDC * 0.5
		slog.Info("slippage: moderate slippage — reducing size",
			"tokenID", tokenID,
			"slippage", fmt.Sprintf("%.3f", slippage),
			"original_size", fmt.Sprintf("%.2f", sizeUSDC),
			"reduced_size", fmt.Sprintf("%.2f", reduced),
		)
		return SlippageResult{Slippage: slippage, AdjustedSize: reduced}
	}

	slog.Debug("slippage: acceptable",
		"tokenID", tokenID,
		"slippage", fmt.Sprintf("%.4f", slippage),
	)
	return SlippageResult{Slippage: slippage, AdjustedSize: sizeUSDC}
}
