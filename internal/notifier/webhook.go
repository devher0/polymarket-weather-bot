// Package notifier — webhook.go sends HTTP POST notifications to an external
// webhook URL after bet events, risk blocks, cycle completion, and errors.
//
// Configuration:
//
//	WEBHOOK_URL — full URL to POST to (e.g. https://hooks.example.com/polybot)
//	             Set webhook_url in config.yaml or WEBHOOK_URL env variable.
//
// If WEBHOOK_URL is empty, all functions are no-ops and return nil.
package notifier

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"
)

const webhookTimeout = 3 * time.Second

var webhookClient = &http.Client{Timeout: webhookTimeout}

// WebhookPayload is the JSON body sent to the configured webhook endpoint.
// All events share the same envelope; unused fields are omitted (omitempty).
type WebhookPayload struct {
	Event       string    `json:"event"`                  // "bet_placed" | "bet_skipped_risk" | "cycle_complete" | "error"
	Timestamp   time.Time `json:"timestamp"`
	ConditionID string    `json:"conditionID,omitempty"`
	Side        string    `json:"side,omitempty"`         // "YES" | "NO"
	Size        float64   `json:"size,omitempty"`         // USDC
	Edge        float64   `json:"edge,omitempty"`         // our edge (fractional)
	OurP        float64   `json:"ourP,omitempty"`         // our probability estimate
	MarketP     float64   `json:"marketP,omitempty"`      // market-implied probability
	City        string    `json:"city,omitempty"`
	Signal      string    `json:"signal,omitempty"`       // "rain" | "heat" | "cold" | ...
	Reason      string    `json:"reason,omitempty"`       // human-readable reason / error message
	// Cycle-level fields (cycle_complete only)
	CycleBets   int     `json:"cycleBets,omitempty"`
	CycleMarketsEvaluated int `json:"cycleMarketsEvaluated,omitempty"`
}

// webhookURL returns the configured webhook URL, or empty string if unset.
func webhookURL() string {
	if v := os.Getenv("WEBHOOK_URL"); v != "" {
		return v
	}
	return ""
}

// PostWebhook sends payload to url as a JSON POST with a 3 s timeout and one
// retry on transient failure. Returns nil when webhook is not configured.
func PostWebhook(url string, payload WebhookPayload) error {
	if url == "" {
		return nil
	}
	payload.Timestamp = time.Now().UTC()

	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("webhook: marshal: %w", err)
	}

	var lastErr error
	for attempt := 0; attempt <= 1; attempt++ {
		lastErr = doPost(url, body)
		if lastErr == nil {
			return nil
		}
	}
	return fmt.Errorf("webhook: %w", lastErr)
}

func doPost(url string, body []byte) error {
	resp, err := webhookClient.Post(url, "application/json", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("post: %w", err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body) //nolint:errcheck

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("unexpected status %d", resp.StatusCode)
	}
	return nil
}

// ── Convenience wrappers ───────────────────────────────────────────────────

// WebhookBetPlaced fires a "bet_placed" event.
func WebhookBetPlaced(conditionID, side, city, signal, reason string, size, edge, ourP, marketP float64) error {
	url := webhookURL()
	if url == "" {
		return nil
	}
	return PostWebhook(url, WebhookPayload{
		Event:       "bet_placed",
		ConditionID: conditionID,
		Side:        side,
		Size:        size,
		Edge:        edge,
		OurP:        ourP,
		MarketP:     marketP,
		City:        city,
		Signal:      signal,
		Reason:      reason,
	})
}

// WebhookBetSkippedRisk fires a "bet_skipped_risk" event when the risk
// manager blocks a bet.
func WebhookBetSkippedRisk(conditionID, reason string) error {
	url := webhookURL()
	if url == "" {
		return nil
	}
	return PostWebhook(url, WebhookPayload{
		Event:       "bet_skipped_risk",
		ConditionID: conditionID,
		Reason:      reason,
	})
}

// WebhookCycleComplete fires a "cycle_complete" event at the end of each run loop.
func WebhookCycleComplete(betsPlaced, marketsEvaluated int) error {
	url := webhookURL()
	if url == "" {
		return nil
	}
	return PostWebhook(url, WebhookPayload{
		Event:                 "cycle_complete",
		CycleBets:             betsPlaced,
		CycleMarketsEvaluated: marketsEvaluated,
	})
}

// WebhookError fires an "error" event for unexpected failures.
func WebhookError(context string, err error) error {
	url := webhookURL()
	if url == "" {
		return nil
	}
	return PostWebhook(url, WebhookPayload{
		Event:  "error",
		Reason: fmt.Sprintf("%s: %v", context, err),
	})
}
