// price_tracker.go — tracks CLOB mid-price for open positions over time.
//
// TASK-056: Market price snapshot tracker.
//
// Each bot cycle can call SnapshotPrice to append the current mid-price to
// a per-market JSONL file under data/price_snapshots/{conditionID}.jsonl.
// DetectAdverseMove detects when the price of our side has fallen significantly
// (>0.15) over the last 3 recorded snapshots — a signal of informed trading
// against our position.
//
// TASK-060: DetectMomentum analyses the price trend to identify whether the
// market is moving in our favour or against us, enabling momentum-aware edge
// requirements.
package markets

import (
	"bufio"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

const (
	snapshotDir          = "data/price_snapshots"
	adverseMoveThreshold = 0.15 // price drop of our side required to flag adverse move
	adverseMoveWindow    = 3    // number of most-recent snapshots to compare

	// TASK-060: momentum detection parameters.
	momentumWindow      = 5 // number of snapshots to examine for trend
	momentumMinPoints   = 4 // minimum snapshots required to compute momentum
	momentumRunRequired = 3 // consecutive moves in same direction to flag momentum
)

// MomentumDirection describes whether the market is moving in our favour.
type MomentumDirection string

const (
	MomentumFavorable MomentumDirection = "favorable" // price of our side is rising
	MomentumAdverse   MomentumDirection = "adverse"   // price of our side is falling
	MomentumNeutral   MomentumDirection = "neutral"   // no clear trend
)

// PricePoint is one CLOB mid-price observation for a market.
type PricePoint struct {
	Timestamp time.Time `json:"timestamp"`
	YesPrice  float64   `json:"yes_price"`
	NoPrice   float64   `json:"no_price"`
}

// midPrice returns the mid-price (average of best bid and best ask) for the
// given token, using the CLOB order book. Returns (0, err) on failure.
func midPrice(tokenID string) (float64, error) {
	url := fmt.Sprintf("%s?token_id=%s", clobBookURL, tokenID)
	resp, err := liquidityHTTPClient.Get(url)
	if err != nil {
		return 0, fmt.Errorf("price_tracker: request: %w", err)
	}
	defer resp.Body.Close()

	var book orderBook
	if err := json.NewDecoder(resp.Body).Decode(&book); err != nil {
		return 0, fmt.Errorf("price_tracker: parse: %w", err)
	}
	if len(book.Bids) == 0 || len(book.Asks) == 0 {
		return 0, fmt.Errorf("price_tracker: empty book for token %s", tokenID)
	}
	bid, err := strconv.ParseFloat(book.Bids[0].Price, 64)
	if err != nil {
		return 0, fmt.Errorf("price_tracker: parse bid: %w", err)
	}
	ask, err := strconv.ParseFloat(book.Asks[0].Price, 64)
	if err != nil {
		return 0, fmt.Errorf("price_tracker: parse ask: %w", err)
	}
	return (bid + ask) / 2.0, nil
}

// snapshotPath returns the JSONL file path for conditionID under dataRoot.
func snapshotPath(conditionID, dataRoot string) string {
	if dataRoot == "" {
		dataRoot = "."
	}
	return filepath.Join(dataRoot, snapshotDir, conditionID+".jsonl")
}

// SnapshotPrice fetches the current YES mid-price for the given market and
// appends it as a JSON line to data/price_snapshots/{conditionID}.jsonl.
// noTokenID is used to derive the NO mid-price as 1 - yesMid when omitted.
// Errors are non-fatal — the function logs and returns them.
func SnapshotPrice(conditionID, yesTokenID, dataRoot string) error {
	yesMid, err := midPrice(yesTokenID)
	if err != nil {
		slog.Warn("price_tracker: snapshot failed", "conditionID", conditionID, "err", err)
		return err
	}

	pp := PricePoint{
		Timestamp: time.Now().UTC(),
		YesPrice:  yesMid,
		NoPrice:   1.0 - yesMid,
	}

	path := snapshotPath(conditionID, dataRoot)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("price_tracker: mkdir: %w", err)
	}

	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("price_tracker: open: %w", err)
	}
	defer f.Close()

	enc := json.NewEncoder(f)
	if err := enc.Encode(pp); err != nil {
		return fmt.Errorf("price_tracker: write: %w", err)
	}

	slog.Debug("price_tracker: snapshot saved",
		"conditionID", conditionID,
		"yes_price", fmt.Sprintf("%.3f", yesMid),
	)
	return nil
}

// GetPriceHistory loads all stored PricePoints for a market from its JSONL file.
// Returns an empty slice (not error) when the file does not yet exist.
func GetPriceHistory(conditionID, dataRoot string) ([]PricePoint, error) {
	path := snapshotPath(conditionID, dataRoot)
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("price_tracker: open history: %w", err)
	}
	defer f.Close()

	var points []PricePoint
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var pp PricePoint
		if err := json.Unmarshal(line, &pp); err != nil {
			slog.Warn("price_tracker: skip bad line", "conditionID", conditionID, "err", err)
			continue
		}
		points = append(points, pp)
	}
	if err := scanner.Err(); err != nil {
		return points, fmt.Errorf("price_tracker: scan: %w", err)
	}
	return points, nil
}

// DetectAdverseMove returns (true, drop) when the price of ourSide ("YES" or "NO")
// has fallen by more than adverseMoveThreshold over the last adverseMoveWindow
// snapshots. Returns (false, 0) when there is insufficient history or no significant move.
//
// An adverse move suggests informed traders are pushing the market against our
// position — in such cases the caller may want to require a larger edge.
func DetectAdverseMove(ourSide string, history []PricePoint) (bool, float64) {
	if len(history) < adverseMoveWindow {
		return false, 0
	}

	// Take the last N points.
	window := history[len(history)-adverseMoveWindow:]
	first := window[0]
	last := window[len(window)-1]

	var firstP, lastP float64
	switch ourSide {
	case "YES":
		firstP, lastP = first.YesPrice, last.YesPrice
	case "NO":
		firstP, lastP = first.NoPrice, last.NoPrice
	default:
		return false, 0
	}

	drop := firstP - lastP // positive = price fell
	if drop >= adverseMoveThreshold {
		slog.Warn("price_tracker: adverse move detected",
			"our_side", ourSide,
			"price_start", fmt.Sprintf("%.3f", firstP),
			"price_now", fmt.Sprintf("%.3f", lastP),
			"drop", fmt.Sprintf("%.3f", drop),
			"window", adverseMoveWindow,
		)
		return true, drop
	}
	return false, 0
}

// SnapshotOpenPositions fetches and saves price snapshots for all currently
// open (unresolved) positions from the provided map of conditionID→yesTokenID.
// Individual failures are logged but do not abort the batch.
func SnapshotOpenPositions(openTokens map[string]string, dataRoot string) {
	for condID, yesTokenID := range openTokens {
		if yesTokenID == "" {
			continue
		}
		if err := SnapshotPrice(condID, yesTokenID, dataRoot); err != nil {
			// already logged inside SnapshotPrice
			_ = err
		}
	}
}

// DetectMomentum analyses the price history for our side and detects whether
// there is a sustained trend (TASK-060).
//
// Algorithm:
//  1. Take the last min(momentumWindow, len(history)) points.
//  2. Count consecutive price moves in the same direction starting from the
//     most-recent snapshot and working backwards.
//  3. If consecutiveRun >= momentumRunRequired in our side's direction:
//     - rising prices → MomentumFavorable
//     - falling prices → MomentumAdverse
//  4. strength = float64(consecutiveRun) / float64(momentumWindow), clamped [0,1].
//
// Returns MomentumNeutral with strength 0 when there is insufficient history
// or no clear trend.
func DetectMomentum(side string, history []PricePoint) (MomentumDirection, float64) {
	if len(history) < momentumMinPoints {
		return MomentumNeutral, 0
	}

	// Trim to the last momentumWindow points.
	start := len(history) - momentumWindow
	if start < 0 {
		start = 0
	}
	window := history[start:]

	// Helper: extract the relevant side price.
	sidePrice := func(pp PricePoint) float64 {
		if side == "NO" {
			return pp.NoPrice
		}
		return pp.YesPrice
	}

	// Walk from newest to oldest, counting the initial run direction.
	// We compare window[i] vs window[i-1] (i from last index downwards).
	upRun := 0
	downRun := 0
	for i := len(window) - 1; i >= 1; i-- {
		delta := sidePrice(window[i]) - sidePrice(window[i-1])
		if delta > 0 {
			if downRun > 0 {
				break // run ended
			}
			upRun++
		} else if delta < 0 {
			if upRun > 0 {
				break // run ended
			}
			downRun++
		}
		// delta == 0 → no-change, continue the same run
	}

	maxRun := upRun
	if downRun > maxRun {
		maxRun = downRun
	}

	if maxRun < momentumRunRequired {
		return MomentumNeutral, 0
	}

	strength := float64(maxRun) / float64(momentumWindow)
	if strength > 1.0 {
		strength = 1.0
	}

	if upRun >= momentumRunRequired {
		slog.Debug("price_tracker: favorable momentum",
			"side", side,
			"run", upRun,
			"strength", fmt.Sprintf("%.2f", strength),
		)
		return MomentumFavorable, strength
	}
	slog.Debug("price_tracker: adverse momentum",
		"side", side,
		"run", downRun,
		"strength", fmt.Sprintf("%.2f", strength),
	)
	return MomentumAdverse, strength
}
