// Package calibration — resolver polls the Gamma API to auto-resolve open
// bets once their market's end date has passed.
//
// Run StartResolver(dataRoot) in a goroutine; it will check every hour and
// update bets_history.csv outcomes automatically.
package calibration

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"
)

const gammaMarketURL = "https://gamma-api.polymarket.com/markets/%s"

// gammaMarketResp is the minimal Gamma API market response we care about.
type gammaMarketResp struct {
	ConditionID string `json:"conditionId"`
	// resolved == true when the market has settled
	Resolved bool `json:"resolved"`
	// outcome is the winning outcome: "Yes" or "No" (may be empty when unresolved)
	Outcome string `json:"outcome"`
	// endDate is ISO-8601; used to decide whether to even bother querying
	EndDate string `json:"endDate"`
}

var resolverClient = &http.Client{Timeout: 15 * time.Second}

// queryGammaMarket fetches market metadata from the Gamma API.
func queryGammaMarket(conditionID string) (*gammaMarketResp, error) {
	url := fmt.Sprintf(gammaMarketURL, conditionID)
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "polymarket-weather-bot/1.0")

	resp, err := resolverClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("gamma request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("market %s not found (404)", conditionID)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("gamma status %d for %s", resp.StatusCode, conditionID)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("gamma read: %w", err)
	}

	// Gamma returns either a single object or an array with one element.
	// Try single object first.
	var single gammaMarketResp
	if err := json.Unmarshal(body, &single); err == nil && single.ConditionID != "" {
		return &single, nil
	}

	// Try array.
	var arr []gammaMarketResp
	if err := json.Unmarshal(body, &arr); err == nil && len(arr) > 0 {
		return &arr[0], nil
	}

	return nil, fmt.Errorf("gamma: unexpected response shape for %s", conditionID)
}

// ResolveOpenBets checks all unresolved bets; for each market whose end date
// has passed it queries the Gamma API and updates the outcome in the CSV.
//
// Returns the number of bets successfully resolved in this call.
func ResolveOpenBets(dataRoot string) (int, error) {
	records, err := LoadHistory(dataRoot)
	if err != nil {
		return 0, fmt.Errorf("resolver: load history: %w", err)
	}

	now := time.Now().UTC()
	resolved := 0

	for _, r := range records {
		if r.Outcome != nil {
			continue // already resolved
		}

		// Skip markets that are still clearly open (end date in the future).
		// We allow a 2-hour grace period for settlement.
		if !r.Timestamp.IsZero() {
			// We don't store the market end date in the CSV, so we rely on
			// the Gamma API to tell us whether it's resolved.
			// Only query markets where the bet was placed > 0 hours ago;
			// brand-new bets are unlikely to be resolved yet.
			if now.Sub(r.Timestamp) < 2*time.Hour {
				continue
			}
		}

		info, err := queryGammaMarket(r.ConditionID)
		if err != nil {
			slog.Warn("resolver: gamma query failed", "conditionID", r.ConditionID, "err", err)
			continue
		}

		if !info.Resolved {
			slog.Debug("resolver: market still open", "conditionID", r.ConditionID)
			continue
		}

		// Determine if our side won.
		// r.Side is "YES" or "NO"; info.Outcome is "Yes" or "No".
		won := false
		switch r.Side {
		case "YES":
			won = (info.Outcome == "Yes" || info.Outcome == "YES")
		case "NO":
			won = (info.Outcome == "No" || info.Outcome == "NO")
		default:
			slog.Warn("resolver: unknown side in bet record", "side", r.Side, "conditionID", r.ConditionID)
			continue
		}

		if err := UpdateOutcome(r.ConditionID, won, dataRoot); err != nil {
			slog.Warn("resolver: update outcome failed", "conditionID", r.ConditionID, "err", err)
			continue
		}

		slog.Info("resolver: bet resolved",
			"conditionID", r.ConditionID,
			"side", r.Side,
			"won", won,
			"marketOutcome", info.Outcome,
		)
		resolved++
	}

	return resolved, nil
}

// StartResolver runs ResolveOpenBets in a background goroutine every hour.
// It logs but does not propagate errors.  dataRoot is typically ".".
// The goroutine exits cleanly when ctx is cancelled (e.g. on SIGTERM/SIGINT).
func StartResolver(dataRoot string, ctx context.Context) {
	go func() {
		slog.Info("resolver: started background goroutine (runs every hour)")
		// Run immediately at startup, then every hour.
		for {
			n, err := ResolveOpenBets(dataRoot)
			if err != nil {
				slog.Warn("resolver: cycle error", "err", err)
			} else if n > 0 {
				slog.Info("resolver: resolved bets in this cycle", "count", n)
			}
			select {
			case <-ctx.Done():
				slog.Info("resolver: stopping gracefully")
				return
			case <-time.After(1 * time.Hour):
			}
		}
	}()
}
