// Package notifier sends Telegram alerts for bets and daily P&L digests.
//
// Configuration via environment variables:
//
//	TELEGRAM_BOT_TOKEN — bot token from @BotFather
//	TELEGRAM_CHAT_ID   — target chat/channel ID (use @username or numeric ID)
//
// If either variable is missing, all functions are no-ops and return nil.
package notifier

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"time"

	"github.com/devher0/polymarket-weather-bot/internal/calibration"
	"github.com/devher0/polymarket-weather-bot/internal/strategy"
)

const telegramAPI = "https://api.telegram.org/bot%s/sendMessage"

var httpClient = &http.Client{Timeout: 15 * time.Second}

// telegramConfig holds resolved credentials.
type telegramConfig struct {
	token  string
	chatID string
}

// config reads TELEGRAM_BOT_TOKEN and TELEGRAM_CHAT_ID from the environment.
// Returns nil if either is missing.
func config() *telegramConfig {
	token := os.Getenv("TELEGRAM_BOT_TOKEN")
	chatID := os.Getenv("TELEGRAM_CHAT_ID")
	if token == "" || chatID == "" {
		return nil
	}
	return &telegramConfig{token: token, chatID: chatID}
}

// ── API client ─────────────────────────────────────────────────────────────

type sendMessageRequest struct {
	ChatID    string `json:"chat_id"`
	Text      string `json:"text"`
	ParseMode string `json:"parse_mode"`
}

type sendMessageResponse struct {
	OK          bool   `json:"ok"`
	Description string `json:"description"`
}

// send posts a message to the configured Telegram chat.
func (c *telegramConfig) send(text string) error {
	payload := sendMessageRequest{
		ChatID:    c.chatID,
		Text:      text,
		ParseMode: "HTML",
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("telegram: marshal: %w", err)
	}

	url := fmt.Sprintf(telegramAPI, c.token)
	resp, err := httpClient.Post(url, "application/json", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("telegram: post: %w", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	var smr sendMessageResponse
	if err := json.Unmarshal(respBody, &smr); err != nil {
		return fmt.Errorf("telegram: parse response: %w", err)
	}
	if !smr.OK {
		return fmt.Errorf("telegram: API error: %s", smr.Description)
	}
	return nil
}

// ── Public API ─────────────────────────────────────────────────────────────

// NotifyBet sends a Telegram message when a real bet is placed.
// No-op (returns nil) if TELEGRAM_BOT_TOKEN or TELEGRAM_CHAT_ID is not set.
func NotifyBet(d *strategy.Decision) error {
	if d == nil {
		return nil
	}
	cfg := config()
	if cfg == nil {
		return nil // Telegram not configured; silently skip
	}

	sideEmoji := "🟢" // YES
	if d.Side == "NO" {
		sideEmoji = "🔴"
	}

	msg := fmt.Sprintf(
		"<b>⚡ New Bet Placed</b>\n\n"+
			"%s <b>%s</b> @ %.2f\n"+
			"City/Signal: <code>%s/%s</code>\n"+
			"Our P: %.2f | Edge: %+.2f%%\n"+
			"Size: <b>$%.2f USDC</b>\n\n"+
			"<i>%s</i>",
		sideEmoji, d.Side, d.MarketPrice,
		d.Market.City, d.Market.Signal,
		d.OurProbability, d.Edge*100,
		d.SizeUSDC,
		truncate(d.Market.Question, 80),
	)

	return cfg.send(msg)
}

// DailyDigest sends a P&L summary message.
// dataRoot is the repo root path for reading bets_history.csv.
// No-op if Telegram is not configured.
func DailyDigest(dataRoot string) error {
	cfg := config()
	if cfg == nil {
		return nil
	}

	records, err := calibration.LoadHistory(dataRoot)
	if err != nil {
		return fmt.Errorf("telegram: load history: %w", err)
	}

	// Filter to last 24 hours for "today's" summary
	now := time.Now().UTC()
	yesterday := now.Add(-24 * time.Hour)

	recent := make([]calibration.BetRecord, 0)
	for _, r := range records {
		if r.Timestamp.After(yesterday) {
			recent = append(recent, r)
		}
	}

	// Compute all-time stats
	score, brierCount, _ := calibration.BrierScore(records)

	allWins := 0
	allResolved := 0
	allPnL := 0.0
	for _, r := range records {
		if r.Outcome == nil {
			continue
		}
		allResolved++
		if *r.Outcome {
			allWins++
			odds := 1.0
			if r.MarketPrice > 0 {
				odds = 1.0 / r.MarketPrice
			}
			allPnL += r.SizeUSDC * (odds - 1)
		} else {
			allPnL -= r.SizeUSDC
		}
	}

	// Recent (last 24h) stats
	recentBets := len(recent)
	openCount := 0
	for _, r := range records {
		if r.Outcome == nil {
			openCount++
		}
	}

	// Build message
	pnlEmoji := "📈"
	if allPnL < 0 {
		pnlEmoji = "📉"
	}

	brierStr := "N/A"
	if brierCount > 0 {
		brierStr = fmt.Sprintf("%.4f", score)
	}

	winRateStr := "N/A"
	if allResolved > 0 {
		winRateStr = fmt.Sprintf("%.1f%%", float64(allWins)/float64(allResolved)*100)
	}

	msg := fmt.Sprintf(
		"<b>🌦️ Weather Bot — Daily Digest</b>\n"+
			"<i>%s UTC</i>\n\n"+
			"%s <b>All-time P&amp;L: %+.2f USDC</b>\n"+
			"Win rate: %s (%d/%d resolved)\n"+
			"Open positions: %d\n"+
			"Bets last 24h: %d\n"+
			"Brier score: %s\n\n"+
			"<i>Next cycle: automatic</i>",
		now.Format("2006-01-02 15:04"),
		pnlEmoji, allPnL,
		winRateStr, allWins, allResolved,
		openCount,
		recentBets,
		brierStr,
	)

	return cfg.send(msg)
}

// NotifyError sends an error alert to Telegram.
// Intended for critical failures (e.g., exchange connection down).
func NotifyError(context string, err error) error {
	cfg := config()
	if cfg == nil {
		return nil
	}

	msg := fmt.Sprintf(
		"<b>❌ Bot Error</b>\n\n"+
			"Context: <code>%s</code>\n"+
			"Error: <code>%s</code>\n\n"+
			"<i>%s UTC</i>",
		context,
		truncate(err.Error(), 200),
		time.Now().UTC().Format("2006-01-02 15:04:05"),
	)

	return cfg.send(msg)
}

// NotifyStop sends a Telegram session-summary message when the bot is
// shutting down.  summary is a pre-formatted plain-text string.
// No-op if Telegram is not configured.
func NotifyStop(summary string) error {
	cfg := config()
	if cfg == nil {
		return nil
	}
	msg := fmt.Sprintf(
		"🛑 <b>Bot Stopped</b>\n\n"+
			"<i>Session summary:</i>\n<pre>%s</pre>\n\n"+
			"<i>%s UTC</i>",
		summary,
		time.Now().UTC().Format("2006-01-02 15:04:05"),
	)
	return cfg.send(msg)
}

// SendTestMessage sends a simple ping to verify Telegram config is working.
func SendTestMessage() error {
	cfg := config()
	if cfg == nil {
		return fmt.Errorf("telegram: TELEGRAM_BOT_TOKEN or TELEGRAM_CHAT_ID not set")
	}
	return cfg.send("✅ <b>Polymarket Weather Bot</b> — Telegram notifications active!")
}

// ── Helpers ────────────────────────────────────────────────────────────────

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// Abs returns the absolute value of a float64 (avoids importing math for one call).
func absF(v float64) float64 {
	return math.Abs(v)
}

// ensure absF is used to avoid "declared and not used" error in some toolchains
var _ = absF
