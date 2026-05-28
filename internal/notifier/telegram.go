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
	"log/slog"
	"math"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/devher0/polymarket-weather-bot/internal/calibration"
	"github.com/devher0/polymarket-weather-bot/internal/collectors"
	"github.com/devher0/polymarket-weather-bot/internal/strategy"
)

// usCitiesForAlerts is the list of US cities we check for active NWS alerts in
// the daily digest.  Matches the list in collectors/noaa_alerts.go.
var usCitiesForAlerts = []string{
	"new_york",
	"miami",
	"chicago",
	"los_angeles",
	"san_francisco",
}

// cityDisplayName maps snake_case city keys to human-readable names for Telegram
// messages.
var cityDisplayName = map[string]string{
	"new_york":      "New York",
	"miami":         "Miami",
	"chicago":       "Chicago",
	"los_angeles":   "Los Angeles",
	"san_francisco": "San Francisco",
}

// alertEmoji returns the appropriate emoji for an NWS alert level.
func alertEmoji(level int) string {
	switch level {
	case collectors.AlertLevelWarning:
		return "🔴"
	case collectors.AlertLevelWatch:
		return "🟡"
	case collectors.AlertLevelAdvisory:
		return "🔵"
	default:
		return "ℹ️"
	}
}

// buildAlertDigest fetches active NWS alerts for all US cities and returns a
// formatted HTML string for the Telegram message.  Returns "" if no alerts are
// active.
func buildAlertDigest() string {
	var lines []string
	for _, city := range usCitiesForAlerts {
		summary, err := collectors.FetchAlerts(city)
		if err != nil || summary.Level == collectors.AlertLevelNone {
			continue
		}
		name := cityDisplayName[city]
		if name == "" {
			name = city
		}
		events := strings.Join(summary.Events, ", ")
		if events == "" {
			events = "active alert"
		}
		lines = append(lines, fmt.Sprintf(
			"%s <b>%s:</b> %s",
			alertEmoji(summary.Level),
			name,
			events,
		))
	}
	if len(lines) == 0 {
		return ""
	}
	return "\n\n<b>⚠️ Active Weather Alerts</b>\n" + strings.Join(lines, "\n")
}

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

	// Fetch active weather alerts for US cities (best-effort; errors are ignored
	// so that an NWS outage never blocks the digest).
	alertSection := buildAlertDigest()

	msg := fmt.Sprintf(
		"<b>🌦️ Weather Bot — Daily Digest</b>\n"+
			"<i>%s UTC</i>\n\n"+
			"%s <b>All-time P&amp;L: %+.2f USDC</b>\n"+
			"Win rate: %s (%d/%d resolved)\n"+
			"Open positions: %d\n"+
			"Bets last 24h: %d\n"+
			"Brier score: %s%s\n\n"+
			"<i>Next cycle: automatic</i>",
		now.Format("2006-01-02 15:04"),
		pnlEmoji, allPnL,
		winRateStr, allWins, allResolved,
		openCount,
		recentBets,
		brierStr,
		alertSection,
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

// NotifyForecastShift sends a Telegram alert when the new forecast differs
// significantly from the previous cached one (TASK-042).
//
// Triggers when ΔMaxTemp > 5°C or ΔPrecipProb > 20%.
// No-op if Telegram is not configured.
func NotifyForecastShift(city string, oldMaxTemp, newMaxTemp, oldPrecipP, newPrecipP float64) error {
	cfg := config()
	if cfg == nil {
		return nil
	}

	dTemp := newMaxTemp - oldMaxTemp
	dPrecip := newPrecipP - oldPrecipP

	tempArrow := "↑"
	if dTemp < 0 {
		tempArrow = "↓"
	}
	precipArrow := "↑"
	if dPrecip < 0 {
		precipArrow = "↓"
	}

	msg := fmt.Sprintf(
		"<b>⚠️ Forecast Shift — %s</b>\n\n"+
			"MaxTemp: <b>%.1f°C %s %.1f°C</b> (Δ%+.1f°C)\n"+
			"RainProb: <b>%.0f%% %s %.0f%%</b> (Δ%+.0f%%)\n\n"+
			"<i>Markets for this city may need re-evaluation.</i>\n"+
			"<i>%s UTC</i>",
		city,
		oldMaxTemp, tempArrow, newMaxTemp, dTemp,
		oldPrecipP, precipArrow, newPrecipP, dPrecip,
		time.Now().UTC().Format("2006-01-02 15:04"),
	)

	return cfg.send(msg)
}

// NotifyProfitOpportunity sends a Telegram alert when an open position's
// current price has risen significantly above the entry price, suggesting
// a good time to close/take profit.
//
// condID is the Polymarket condition ID, side is "YES" or "NO", entry is the
// price at which we bet, and current is the current market mid-price for our
// side. The implied P&L percentage is (current-entry)/entry*100.
//
// No-op if Telegram is not configured.
func NotifyProfitOpportunity(condID, side string, entry, current float64) error {
	cfg := config()
	if cfg == nil {
		return nil
	}

	pnlPct := 0.0
	if entry > 0 {
		pnlPct = (current - entry) / entry * 100
	}

	msg := fmt.Sprintf(
		"<b>💰 Profit Opportunity</b>\n\n"+
			"Market: <code>%s</code>\n"+
			"Side: <b>%s</b>\n"+
			"Entry: %.2f  →  Now: %.2f\n"+
			"Implied P&amp;L: <b>+%.0f%%</b>\n\n"+
			"<i>Consider closing this position.</i>\n"+
			"<i>%s UTC</i>",
		condID,
		side,
		entry, current,
		pnlPct,
		time.Now().UTC().Format("2006-01-02 15:04"),
	)

	return cfg.send(msg)
}

// WeeklyDigest sends a comprehensive 7-day performance summary to Telegram
// with per-city and per-signal breakdown.
//
// The timestamp of the last sent digest is persisted in
// data/last_weekly_digest.txt (RFC3339). If that file shows a digest was sent
// within the last 7 days the function is a no-op.
// No-op if Telegram is not configured.
func WeeklyDigest(dataRoot string) error {
	cfg := config()
	if cfg == nil {
		return nil
	}

	// Check if we already sent a digest in the last 7 days.
	sentFile := "data/last_weekly_digest.txt"
	if dataRoot != "" && dataRoot != "." {
		sentFile = dataRoot + "/" + sentFile
	}
	if raw, err := os.ReadFile(sentFile); err == nil {
		if t, err := time.Parse(time.RFC3339, strings.TrimSpace(string(raw))); err == nil {
			if time.Since(t) < 7*24*time.Hour {
				slog.Info("weekly digest: skipped (already sent recently)", "last_sent", t.Format("2006-01-02"))
				return nil
			}
		}
	}

	records, err := calibration.LoadHistory(dataRoot)
	if err != nil {
		return fmt.Errorf("telegram weekly: load history: %w", err)
	}

	now := time.Now().UTC()
	weekAgo := now.Add(-7 * 24 * time.Hour)

	// Filter last 7 days
	week := make([]calibration.BetRecord, 0, len(records))
	for _, r := range records {
		if r.Timestamp.After(weekAgo) {
			week = append(week, r)
		}
	}

	// Weekly stats
	weekBets := len(week)
	weekWins, weekLosses := 0, 0
	weekPnL := 0.0
	for _, r := range week {
		if r.Outcome == nil {
			continue
		}
		if *r.Outcome {
			weekWins++
			odds := 1.0
			if r.MarketPrice > 0 {
				odds = 1.0 / r.MarketPrice
			}
			weekPnL += r.SizeUSDC * (odds - 1)
		} else {
			weekLosses++
			weekPnL -= r.SizeUSDC
		}
	}

	// All-time stats for Brier
	brierScore, brierCount, _ := calibration.BrierScore(records)

	// Per-signal breakdown (all-time)
	sigBreakdown := calibration.SignalBreakdown(records)
	// Find best and worst signal by win rate (min 5 samples)
	bestSig, worstSig := "", ""
	bestWR, worstWR := -1.0, 101.0
	for sig, bs := range sigBreakdown {
		if bs.Count < 5 || sig == "(unknown)" {
			continue
		}
		wr := bs.WinRate()
		if wr > bestWR {
			bestWR = wr
			bestSig = sig
		}
		if wr < worstWR {
			worstWR = wr
			worstSig = sig
		}
	}

	// Per-city breakdown (all-time)
	cityBreakdown := calibration.CityBreakdown(records)
	bestCity, worstCity := "", ""
	bestCityWR, worstCityWR := -1.0, 101.0
	for city, bs := range cityBreakdown {
		if bs.Count < 5 || city == "(unknown)" {
			continue
		}
		wr := bs.WinRate()
		if wr > bestCityWR {
			bestCityWR = wr
			bestCity = city
		}
		if wr < worstCityWR {
			worstCityWR = wr
			worstCity = city
		}
	}

	// Open positions
	openCount := 0
	for _, r := range records {
		if r.Outcome == nil {
			openCount++
		}
	}

	pnlEmoji := "📈"
	if weekPnL < 0 {
		pnlEmoji = "📉"
	}

	weekWR := "N/A"
	resolved := weekWins + weekLosses
	if resolved > 0 {
		weekWR = fmt.Sprintf("%.1f%%", float64(weekWins)/float64(resolved)*100)
	}

	brierStr := "N/A"
	if brierCount > 0 {
		brierStr = fmt.Sprintf("%.4f", brierScore)
	}

	bestSigLine, worstSigLine := "", ""
	if bestSig != "" {
		bestSigLine = fmt.Sprintf("\n🏆 Best signal: <b>%s</b> (%.1f%% WR)", bestSig, bestWR)
	}
	if worstSig != "" && worstSig != bestSig {
		worstSigLine = fmt.Sprintf("\n⚠️ Worst signal: <b>%s</b> (%.1f%% WR)", worstSig, worstWR)
	}
	bestCityLine, worstCityLine := "", ""
	if bestCity != "" {
		bestCityLine = fmt.Sprintf("\n🌍 Best city: <b>%s</b> (%.1f%% WR)", bestCity, bestCityWR)
	}
	if worstCity != "" && worstCity != bestCity {
		worstCityLine = fmt.Sprintf("\n📍 Worst city: <b>%s</b> (%.1f%% WR)", worstCity, worstCityWR)
	}

	msg := fmt.Sprintf(
		"<b>📊 Weather Bot — Weekly Digest</b>\n"+
			"<i>%s → %s UTC</i>\n\n"+
			"%s <b>Week P&amp;L: %+.2f USDC</b>\n"+
			"Bets this week: %d (%d won / %d lost)\n"+
			"Win rate (week): %s\n"+
			"Open positions: %d\n"+
			"Brier score (all-time): %s\n"+
			"\n<b>Breakdown:</b>%s%s%s%s\n\n"+
			"<i>Next weekly digest: ~7 days</i>",
		weekAgo.Format("Jan 02"), now.Format("Jan 02, 2006"),
		pnlEmoji, weekPnL,
		weekBets, weekWins, weekLosses,
		weekWR,
		openCount,
		brierStr,
		bestSigLine, worstSigLine, bestCityLine, worstCityLine,
	)

	if err := cfg.send(msg); err != nil {
		return err
	}

	// Persist timestamp so we don't re-send for 7 days.
	_ = os.MkdirAll("data", 0o755)
	_ = os.WriteFile(sentFile, []byte(now.Format(time.RFC3339)), 0o644)
	slog.Info("weekly digest sent")
	return nil
}

// ── Helpers ────────────────────────────────────────────────────────────────

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// NotifyDuplicates sends a Telegram alert listing duplicate market fingerprints
// (TASK-136).  No-op if dupes is empty or Telegram is not configured.
func NotifyDuplicates(dupes map[string][]string) error {
	if len(dupes) == 0 {
		return nil
	}
	cfg := config()
	if cfg == nil {
		return nil
	}

	// Import cycle prevention: build the text here instead of calling
	// markets.BuildDuplicateAlertText so that the notifier package does not
	// depend on the markets package.
	lines := make([]string, 0, len(dupes)+1)
	lines = append(lines, fmt.Sprintf("⚠️ <b>Duplicate markets detected (%d group(s)):</b>", len(dupes)))

	// Sort fingerprints for deterministic output.
	fps := make([]string, 0, len(dupes))
	for fp := range dupes {
		fps = append(fps, fp)
	}
	for i := 1; i < len(fps); i++ {
		for j := i; j > 0 && fps[j] < fps[j-1]; j-- {
			fps[j], fps[j-1] = fps[j-1], fps[j]
		}
	}
	for _, fp := range fps {
		ids := dupes[fp]
		lines = append(lines, fmt.Sprintf("• <code>%s</code> — %d markets", fp, len(ids)))
	}

	return cfg.send(strings.Join(lines, "\n"))
}

// Abs returns the absolute value of a float64 (avoids importing math for one call).
func absF(v float64) float64 {
	return math.Abs(v)
}

// ensure absF is used to avoid "declared and not used" error in some toolchains
var _ = absF
