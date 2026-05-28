// telegram_commands.go — Telegram bot command polling.
//
// Supported commands:
//   /help            — list all available commands
//   /status          — Brier score, open positions, P&L for today
//   /positions       — list of open (unresolved) bets
//   /daily           — today's bets timeline with running P&L
//   /next            — top-3 best bets right now (dry-run, from cached forecasts)
//   /forecast [city] — current weather forecast from cache (or live fetch)
//   /forecast-quality — per-city forecast confidence + age
//   /summary         — compact multi-section health overview
//   /signals         — per-signal win rate + Brier breakdown
//   /export [days]   — send CSV of resolved bets as file
//   /healthcheck     — data source status, calibration, risk state, signal kelly
//   /source-weights  — current dynamic weights per data source vs static baseline
//   /pnl-city        — per-city P&L table: bets/wins/win%/pnl/roi
//   /winrate         — rolling win rate table per signal over last 20 bets
//   /explain <id>    — full strategy audit trail for a specific conditionID
//   /config          — show current bot configuration parameters
//   /markets         — top active weather markets: city/signal/prices/spread/expiry
//   /top-edge        — top markets ranked by edge×confidence score
//   /ev              — EV capture ratio: expected vs realized P&L last 50 bets
//   /pause           — suspend all trading
//   /resume          — resume trading
//
// Uses long-poll (getUpdates with timeout=60) — no webhook required.
// StartCommandPoller runs in a background goroutine; call it after bot setup.
package notifier

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math"
	"mime/multipart"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/devher0/polymarket-weather-bot/internal/calibration"
	"github.com/devher0/polymarket-weather-bot/internal/collectors"
	"github.com/devher0/polymarket-weather-bot/internal/markets"
	"github.com/devher0/polymarket-weather-bot/internal/risk"
	"github.com/devher0/polymarket-weather-bot/internal/strategy"
	"github.com/devher0/polymarket-weather-bot/internal/weather"
)

// ── global pause flag ─────────────────────────────────────────────────────

// paused is 1 when the bot is manually paused via /pause.
var paused atomic.Int32

// IsPaused returns true when trading has been suspended via /pause.
func IsPaused() bool { return paused.Load() == 1 }

// ── Telegram Bot API structs ──────────────────────────────────────────────

type tgUser struct {
	ID       int64  `json:"id"`
	Username string `json:"username"`
	IsBot    bool   `json:"is_bot"`
}

type tgChat struct {
	ID int64 `json:"id"`
}

type tgMessage struct {
	MessageID int    `json:"message_id"`
	From      tgUser `json:"from"`
	Chat      tgChat `json:"chat"`
	Text      string `json:"text"`
	Date      int64  `json:"date"`
}

type tgUpdate struct {
	UpdateID int       `json:"update_id"`
	Message  tgMessage `json:"message"`
}

type tgUpdatesResp struct {
	OK     bool       `json:"ok"`
	Result []tgUpdate `json:"result"`
}

// ── BotConfig carries runtime state needed by command handlers ────────────

// BotConfig is passed to StartCommandPoller so handlers can inspect live state.
type BotConfig struct {
	DataRoot         string
	Bankroll         float64
	MinEdge          float64
	MaxBet           float64
	StartTime        time.Time // when the bot process started (for uptime display in /healthcheck)
	DryRun           bool
	KellyFraction    float64
	MaxKellyFraction float64
	LoopSec          int
	ProtocolFeeRate  float64
}

// ── long-poll loop ────────────────────────────────────────────────────────

var pollClient = &http.Client{Timeout: 90 * time.Second}

func getUpdates(token string, offset int) ([]tgUpdate, error) {
	url := fmt.Sprintf("https://api.telegram.org/bot%s/getUpdates?timeout=60&offset=%d", token, offset)
	resp, err := pollClient.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	var r tgUpdatesResp
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, err
	}
	if !r.OK {
		return nil, fmt.Errorf("getUpdates not ok")
	}
	return r.Result, nil
}

func sendReply(cfg *telegramConfig, chatID int64, text string) {
	payload := sendMessageRequest{
		ChatID:    fmt.Sprint(chatID),
		Text:      text,
		ParseMode: "HTML",
	}
	body, _ := json.Marshal(payload)
	url := fmt.Sprintf(telegramAPI, cfg.token)
	resp, err := httpClient.Post(url, "application/json", strings.NewReader(string(body)))
	if err != nil {
		slog.Warn("telegram cmd: sendReply failed", "err", err)
		return
	}
	resp.Body.Close()
}

// StartCommandPoller starts a goroutine that polls for Telegram bot commands.
// It exits cleanly when ctx is cancelled.
// bcfg provides runtime state to the command handlers.
func StartCommandPoller(ctx context.Context, bcfg BotConfig) {
	cfg := config()
	if cfg == nil {
		slog.Info("telegram command poller disabled (no token/chatID)")
		return
	}
	go func() {
		slog.Info("telegram command poller started")
		offset := 0
		for {
			select {
			case <-ctx.Done():
				slog.Info("telegram command poller stopping")
				return
			default:
			}

			updates, err := getUpdates(cfg.token, offset)
			if err != nil {
				slog.Warn("telegram poll error", "err", err)
				select {
				case <-ctx.Done():
					return
				case <-time.After(5 * time.Second):
				}
				continue
			}

			for _, u := range updates {
				offset = u.UpdateID + 1
				text := strings.TrimSpace(u.Message.Text)
				if text == "" {
					continue
				}
				// Parse command (strip bot username suffix like /status@mybot).
				cmd := strings.ToLower(strings.SplitN(text, " ", 2)[0])
				cmd = strings.SplitN(cmd, "@", 2)[0]

				chatID := u.Message.Chat.ID
				switch cmd {
				case "/help":
					sendReply(cfg, chatID, handleHelp())
				case "/status":
					sendReply(cfg, chatID, handleStatus(bcfg))
				case "/positions":
					sendReply(cfg, chatID, handlePositions(bcfg))
				case "/daily":
					sendReply(cfg, chatID, handleDaily(bcfg))
				case "/next":
					sendReply(cfg, chatID, handleNext(bcfg))
				case "/forecast":
					// TASK-138: extract optional city arg from the full message text.
					arg := ""
					parts := strings.SplitN(text, " ", 2)
					if len(parts) == 2 {
						arg = strings.TrimSpace(parts[1])
					}
					sendReply(cfg, chatID, handleForecast(bcfg, arg))
				case "/forecast-quality":
					sendReply(cfg, chatID, handleForecastQuality(bcfg))
				case "/pause":
					paused.Store(1)
					sendReply(cfg, chatID, "⏸ Trading <b>paused</b>. Send /resume to restart.")
					slog.Info("telegram: trading paused via /pause command")
				case "/resume":
					paused.Store(0)
					sendReply(cfg, chatID, "▶️ Trading <b>resumed</b>.")
					slog.Info("telegram: trading resumed via /resume command")
				case "/summary":
					sendReply(cfg, chatID, handleSummary(bcfg))
				case "/signals":
					sendReply(cfg, chatID, handleSignals(bcfg))
				case "/export":
					arg := ""
					parts := strings.SplitN(text, " ", 2)
					if len(parts) == 2 {
						arg = strings.TrimSpace(parts[1])
					}
					days := 30
					if arg != "" {
						if n, err := strconv.Atoi(arg); err == nil && n > 0 {
							days = n
						}
					}
					handleExport(bcfg, chatID, days, cfg)
				case "/watchlist":
					arg := ""
					parts := strings.SplitN(text, " ", 2)
					if len(parts) == 2 {
						arg = strings.TrimSpace(parts[1])
					}
					sendReply(cfg, chatID, handleWatchlist(bcfg, arg))
				case "/healthcheck":
					sendReply(cfg, chatID, handleHealthcheck(bcfg))
				case "/source-weights":
					sendReply(cfg, chatID, handleSourceWeights(bcfg))
				case "/pnl-city":
					sendReply(cfg, chatID, handlePnLCity(bcfg))
				case "/winrate":
					sendReply(cfg, chatID, handleWinRate(bcfg))
				case "/explain":
					arg := ""
					parts := strings.SplitN(text, " ", 2)
					if len(parts) == 2 {
						arg = strings.TrimSpace(parts[1])
					}
					sendReply(cfg, chatID, handleExplainMarket(bcfg, arg))
				case "/config":
					sendReply(cfg, chatID, handleConfig(bcfg))
				case "/markets":
					sendReply(cfg, chatID, handleMarkets(bcfg))
				case "/top-edge":
					sendReply(cfg, chatID, handleTopEdge(bcfg))
				case "/ev":
					sendReply(cfg, chatID, handleEV(bcfg))
				}
			}
		}
	}()
}

// ── command handlers ──────────────────────────────────────────────────────

func handleStatus(bcfg BotConfig) string {
	records, err := calibration.LoadHistory(bcfg.DataRoot)
	if err != nil {
		return "❌ Could not load bet history."
	}

	brierStr := "n/a (need resolved bets)"
	if score, count, err := calibration.BrierScore(records); err == nil && count > 0 {
		brierStr = fmt.Sprintf("%.4f (%d resolved bets)", score, count)
	}

	open := 0
	pnlToday := 0.0
	today := time.Now().UTC().Format("2006-01-02")
	for _, r := range records {
		if r.Outcome == nil {
			open++
		}
		if strings.HasPrefix(r.Timestamp.UTC().Format("2006-01-02"), today) && r.Outcome != nil {
			if *r.Outcome {
				pnlToday += r.SizeUSDC/r.MarketPrice - r.SizeUSDC
			} else {
				pnlToday -= r.SizeUSDC
			}
		}
	}

	pauseState := "▶️ running"
	if IsPaused() {
		pauseState = "⏸ paused"
	}

	// TASK-139: current win/loss streak.
	streakStr := calibration.StreakStatusLine(records)
	if streakStr == "" {
		streakStr = "n/a"
	}

	sharpeStr := "n/a (need ≥2 days)"
	if sh, cnt, err := calibration.RollingSharpe(bcfg.DataRoot, 30); err == nil && cnt >= 2 {
		sharpeStr = fmt.Sprintf("%.3f [%s, %d days]", sh, calibration.SharpeQuality(sh), cnt)
	}

	// TASK-123: ASCII sparkline P&L for the last 14 days.
	sparkStr := ""
	if spark, total, days := buildPnLSparkline(records, 14); days >= 3 {
		sign := "+"
		if total < 0 {
			sign = ""
		}
		sparkStr = fmt.Sprintf("\nP&amp;L 14d: <code>%s</code> (%s%.2f USDC)", spark, sign, total)
	}

	// TASK-122: show Platt calibrator status.
	plattStr := ""
	if pc := calibration.LoadCalibrator(bcfg.DataRoot); pc.IsActive() {
		plattStr = fmt.Sprintf("\nCalibrator: <code>A=%.3f B=%.3f N=%d</code>", pc.A, pc.B, pc.N)
	}

	// TASK-147: calibration drift indicator.
	driftStr := ""
	if line := calibration.DriftStatusLine(records); line != "" {
		driftStr = "\n" + line
	}

	return fmt.Sprintf(
		"📊 <b>Bot Status</b>\n"+
			"State: %s\n"+
			"Brier score: <code>%s</code>\n"+
			"Sharpe (30d): <code>%s</code>\n"+
			"Streak: <code>%s</code>\n"+
			"Open positions: <b>%d</b>\n"+
			"Today P&amp;L: <b>%+.2f USDC</b>%s%s%s",
		pauseState, brierStr, sharpeStr, streakStr, open, pnlToday, sparkStr, plattStr, driftStr,
	)
}

// asciiSparkline maps a slice of float64 values to a compact Unicode bar string.
// Uses block elements ▁▂▃▄▅▆▇█ (8 levels).
func asciiSparkline(values []float64) string {
	if len(values) == 0 {
		return ""
	}
	bars := []rune{'▁', '▂', '▃', '▄', '▅', '▆', '▇', '█'}
	minV, maxV := values[0], values[0]
	for _, v := range values[1:] {
		if v < minV {
			minV = v
		}
		if v > maxV {
			maxV = v
		}
	}
	rng := maxV - minV
	result := make([]rune, len(values))
	for i, v := range values {
		idx := 0
		if rng > 0 {
			idx = int((v-minV)/rng*7 + 0.5)
			if idx > 7 {
				idx = 7
			}
		}
		result[i] = bars[idx]
	}
	return string(result)
}

// buildPnLSparkline computes daily P&L for the last nDays days from bet records.
// Returns the sparkline string, total P&L over the period, and number of days with data.
func buildPnLSparkline(records []calibration.BetRecord, nDays int) (spark string, total float64, days int) {
	now := time.Now().UTC()
	dailyPnL := make([]float64, nDays)
	hasData := make([]bool, nDays)

	for _, r := range records {
		if r.Outcome == nil {
			continue
		}
		age := now.Sub(r.ResolvedAt.UTC())
		dayIdx := int(age.Hours() / 24)
		if dayIdx < 0 || dayIdx >= nDays {
			continue
		}
		revIdx := nDays - 1 - dayIdx // oldest → index 0, newest → index nDays-1
		var pnl float64
		if *r.Outcome {
			pnl = r.SizeUSDC/r.MarketPrice - r.SizeUSDC
		} else {
			pnl = -r.SizeUSDC
		}
		dailyPnL[revIdx] += pnl
		hasData[revIdx] = true
		total += pnl
	}

	// Count days with any data.
	var vals []float64
	for i, v := range dailyPnL {
		if hasData[i] {
			days++
		}
		vals = append(vals, v)
	}
	if days < 3 {
		return "", total, days
	}
	return asciiSparkline(vals), total, days
}

func handlePositions(bcfg BotConfig) string {
	records, err := calibration.LoadHistory(bcfg.DataRoot)
	if err != nil {
		return "❌ Could not load bet history."
	}

	var lines []string
	for _, r := range records {
		if r.Outcome != nil {
			continue
		}
		lines = append(lines, fmt.Sprintf(
			"• %s/%s %s %.2f USDC @ %.2f (placed %s)",
			r.City, r.Signal, r.Side, r.SizeUSDC, r.MarketPrice,
			r.Timestamp.UTC().Format("Jan 02 15:04"),
		))
	}
	if len(lines) == 0 {
		return "📭 No open positions."
	}
	return "<b>📂 Open Positions</b>\n" + strings.Join(lines, "\n")
}

func handleNext(bcfg BotConfig) string {
	// Dry-run: fetch markets and evaluate top-3 best opportunities.
	mks, err := markets.GetWeatherMarkets()
	if err != nil {
		return "❌ Could not fetch markets: " + err.Error()
	}
	if len(mks) == 0 {
		return "📭 No weather markets found."
	}

	// Collect scored markets using cached forecasts.
	type item struct {
		score float64
		m     markets.Market
		d     *strategy.Decision
	}
	var scored []item
	seen := map[string]bool{}
	dataRoot := bcfg.DataRoot

	for _, m := range mks {
		if m.City == "" || m.Signal == "" || m.ThinLiquidity || seen[m.ConditionID] {
			continue
		}
		seen[m.ConditionID] = true

		forecasts, err := weather.GetForecast(m.City, 2)
		if err != nil || len(forecasts) == 0 {
			continue
		}
		ff := &collectors.FusedForecast{
			Forecast:   forecasts[0],
			Confidence: 0.75,
			Sources:    []string{"openmeteo"},
		}
		score := strategy.ScoreMarket(m, ff)
		d := strategy.EvaluateFused(m, ff, bcfg.Bankroll, bcfg.MinEdge, bcfg.MaxBet, dataRoot)
		scored = append(scored, item{score, m, d})
	}

	// Sort descending by score (simple insertion sort, ≤50 items).
	for i := 1; i < len(scored); i++ {
		for j := i; j > 0 && scored[j].score > scored[j-1].score; j-- {
			scored[j], scored[j-1] = scored[j-1], scored[j]
		}
	}

	var lines []string
	shown := 0
	for _, it := range scored {
		if shown >= 3 {
			break
		}
		if it.d == nil {
			continue
		}
		lines = append(lines, fmt.Sprintf(
			"%d. %s/%s → <b>%s %.2f USDC</b> (edge %.0f%%)",
			shown+1, it.m.City, it.m.Signal, it.d.Side,
			it.d.SizeUSDC, (it.d.OurProbability-it.m.YesPrice)*100,
		))
		shown++
	}
	if len(lines) == 0 {
		return "🔍 No actionable opportunities right now."
	}
	return "<b>🔮 Top Opportunities (dry-run)</b>\n" + strings.Join(lines, "\n")
}

// ── TASK-138: /forecast command ───────────────────────────────────────────

// forecastSummaryCities is the default set shown when /forecast has no arg.
var forecastSummaryCities = []string{
	"new_york", "london", "paris", "miami", "berlin",
}

// handleForecast returns a formatted weather summary.
// city="" → summary table for forecastSummaryCities.
// city="X" → detailed one-city block.
func handleForecast(bcfg BotConfig, city string) string {
	if city == "" {
		return handleForecastAll(bcfg)
	}
	// Validate city.
	if _, ok := weather.Cities[city]; !ok {
		return fmt.Sprintf("❌ Unknown city <code>%s</code>.\nKnown: new_york, london, paris, miami, berlin, chicago, los_angeles, san_francisco, tokyo", city)
	}
	return handleForecastOne(bcfg, city)
}

// handleForecastOne returns a detailed forecast block for a single city.
func handleForecastOne(bcfg BotConfig, city string) string {
	ff, age := loadForecastForDisplay(bcfg, city)
	if ff == nil {
		return fmt.Sprintf("❌ Could not fetch forecast for <code>%s</code>.", city)
	}
	return formatForecastBlock(city, ff, age)
}

// handleForecastAll returns a compact multi-city summary table.
func handleForecastAll(bcfg BotConfig) string {
	var sb strings.Builder
	sb.WriteString("<b>🌍 Forecast Summary</b>\n<pre>")
	sb.WriteString(fmt.Sprintf("%-15s %6s %6s %6s %5s %5s\n",
		"City", "MaxT°C", "Rain%", "Wnd", "Conf", "Age"))
	sb.WriteString(strings.Repeat("─", 50) + "\n")
	any := false
	for _, c := range forecastSummaryCities {
		ff, age := loadForecastForDisplay(bcfg, c)
		if ff == nil {
			sb.WriteString(fmt.Sprintf("%-15s  n/a\n", c))
			continue
		}
		ageStr := formatAge(age)
		sb.WriteString(fmt.Sprintf("%-15s %6.1f %5.0f%% %5.0f %4.0f%% %5s\n",
			c,
			ff.MaxTempC,
			ff.PrecipitationProbability,
			ff.WindSpeedKMH,
			ff.Confidence*100,
			ageStr,
		))
		any = true
	}
	if !any {
		return "❌ No forecast data available. Bot may not have run yet."
	}
	sb.WriteString("</pre>\n<i>Send /forecast [city] for details.</i>")
	return sb.String()
}

// loadForecastForDisplay tries the disk cache first; falls back to a live
// OpenMeteo fetch (day-0).  Returns nil if both fail.
func loadForecastForDisplay(bcfg BotConfig, city string) (*collectors.FusedForecast, time.Duration) {
	// Try disk cache (up to 3h stale is fine for display purposes).
	ff, ok := collectors.LoadForecastCache(city, 0, bcfg.DataRoot, 3*time.Hour)
	if ok && ff != nil {
		return ff, time.Since(ff.FetchedAt)
	}
	// Fallback: live fetch from OpenMeteo only.
	forecasts, err := weather.GetForecast(city, 1)
	if err != nil || len(forecasts) == 0 {
		return nil, 0
	}
	return &collectors.FusedForecast{
		Forecast:   forecasts[0],
		Confidence: 0,
		Sources:    []string{"openmeteo(live)"},
		FetchedAt:  time.Now(),
	}, 0
}

// formatForecastBlock formats a single-city forecast for Telegram HTML output.
func formatForecastBlock(city string, ff *collectors.FusedForecast, age time.Duration) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("<b>🌤 %s</b>\n", city))
	sb.WriteString(fmt.Sprintf("Temp: <b>%.1f°C</b> / %.1f°C\n", ff.MaxTempC, ff.MinTempC))
	if ff.ApparentMaxTempC != 0 && abs64(ff.ApparentMaxTempC-ff.MaxTempC) > 1 {
		sb.WriteString(fmt.Sprintf("Feels like: %.1f°C\n", ff.ApparentMaxTempC))
	}
	sb.WriteString(fmt.Sprintf("Precip: %.1f mm (%.0f%%)\n", ff.PrecipitationMM, ff.PrecipitationProbability))
	sb.WriteString(fmt.Sprintf("Wind: %.0f km/h\n", ff.WindSpeedKMH))
	if ff.CapeJkg > 500 {
		sb.WriteString(fmt.Sprintf("CAPE: %.0f J/kg ⚡\n", ff.CapeJkg))
	}
	if ff.UVIndexMax > 0 {
		sb.WriteString(fmt.Sprintf("UV: %.0f\n", ff.UVIndexMax))
	}
	if ff.Confidence > 0 {
		sb.WriteString(fmt.Sprintf("Confidence: %.0f%%\n", ff.Confidence*100))
	}
	if len(ff.Sources) > 0 {
		sb.WriteString(fmt.Sprintf("Sources: <code>%s</code>\n", strings.Join(ff.Sources, ", ")))
	}
	// NWS alert for US cities.
	if ff.AlertLevel > 0 && len(ff.AlertEvents) > 0 {
		emoji := forecastAlertEmoji(ff.AlertLevel)
		sb.WriteString(fmt.Sprintf("%s NWS: %s\n", emoji, strings.Join(ff.AlertEvents, "; ")))
	}
	sb.WriteString(fmt.Sprintf("<i>Updated %s ago</i>", formatAge(age)))
	return sb.String()
}

func abs64(x float64) float64 {
	if x < 0 {
		return -x
	}
	return x
}

func formatAge(d time.Duration) string {
	if d < time.Minute {
		return "just now"
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	return fmt.Sprintf("%.1fh", d.Hours())
}

// ── TASK-146: /summary command ────────────────────────────────────────────

// handleSummary returns a compact multi-section bot health overview for Telegram.
// Stays well under the 4096-char Telegram message limit.
func handleSummary(bcfg BotConfig) string {
	records, err := calibration.LoadHistory(bcfg.DataRoot)
	if err != nil {
		return "❌ Could not load bet history."
	}

	bankroll := calibration.LoadBankroll(bcfg.DataRoot)

	// ── performance ─────────────────────────────────────────────────────────
	var resolved, wins int
	var totalPnL float64
	for _, r := range records {
		if r.Outcome == nil {
			continue
		}
		resolved++
		if *r.Outcome {
			wins++
			totalPnL += r.SizeUSDC/r.MarketPrice - r.SizeUSDC
		} else {
			totalPnL -= r.SizeUSDC
		}
	}
	winRateStr := "—"
	if resolved > 0 {
		winRateStr = fmt.Sprintf("%.0f%% (%d/%d)", float64(wins)/float64(resolved)*100, wins, resolved)
	}

	brierStr := "—"
	if score, count, berr := calibration.BrierScore(records); berr == nil && count > 0 {
		brierStr = fmt.Sprintf("%.4f (%d bets)", score, count)
	}

	sharpeStr := "—"
	if sh, cnt, serr := calibration.RollingSharpe(bcfg.DataRoot, 30); serr == nil && cnt >= 2 {
		sharpeStr = fmt.Sprintf("%.3f [%s, %dd]", sh, calibration.SharpeQuality(sh), cnt)
	}

	streakStr := calibration.StreakStatusLine(records)
	if streakStr == "" {
		streakStr = "—"
	}

	// ── today ────────────────────────────────────────────────────────────────
	today := time.Now().UTC().Format("2006-01-02")
	var todayBets, todayOpen int
	var todayPnL float64
	for _, r := range records {
		if r.Timestamp.UTC().Format("2006-01-02") != today {
			continue
		}
		todayBets++
		if r.Outcome == nil {
			todayOpen++
		} else {
			if *r.Outcome {
				todayPnL += r.SizeUSDC/r.MarketPrice - r.SizeUSDC
			} else {
				todayPnL -= r.SizeUSDC
			}
		}
	}

	// ── top city / signal ────────────────────────────────────────────────────
	type bdEntry struct {
		name  string
		wr    float64
		count int
	}
	cityBD := calibration.CityBreakdown(records)
	var topCity string
	bestCityWR := -1.0
	for city, s := range cityBD {
		if s.Count >= 3 && s.WinRate() > bestCityWR {
			bestCityWR = s.WinRate()
			topCity = fmt.Sprintf("%s %.0f%%(%d)", city, s.WinRate(), s.Count)
		}
	}
	_ = bdEntry{}
	sigBD := calibration.SignalBreakdown(records)
	var topSig string
	bestSigWR := -1.0
	for sig, s := range sigBD {
		if s.Count >= 3 && s.WinRate() > bestSigWR {
			bestSigWR = s.WinRate()
			topSig = fmt.Sprintf("%s %.0f%%(%d)", sig, s.WinRate(), s.Count)
		}
	}

	pnlSign := "+"
	if totalPnL < 0 {
		pnlSign = ""
	}
	todayPnLSign := "+"
	if todayPnL < 0 {
		todayPnLSign = ""
	}

	var sb strings.Builder
	sb.WriteString("📊 <b>Bot Summary</b>\n")
	sb.WriteString("<pre>")
	sb.WriteString(fmt.Sprintf("%-14s %.2f USDC\n", "Bankroll:", bankroll))
	sb.WriteString(fmt.Sprintf("%-14s %s\n", "Win rate:", winRateStr))
	sb.WriteString(fmt.Sprintf("%-14s %s\n", "Brier:", brierStr))
	sb.WriteString(fmt.Sprintf("%-14s %s\n", "Sharpe 30d:", sharpeStr))
	sb.WriteString(fmt.Sprintf("%-14s %s\n", "Streak:", streakStr))
	sb.WriteString(fmt.Sprintf("%-14s %s%.2f USDC (all time)\n", "P&L:", pnlSign, totalPnL))
	sb.WriteString("────────────────────────────\n")
	sb.WriteString(fmt.Sprintf("%-14s %d bets (%d open)\n", "Today:", todayBets, todayOpen))
	sb.WriteString(fmt.Sprintf("%-14s %s%.2f USDC\n", "Today P&L:", todayPnLSign, todayPnL))
	if topCity != "" {
		sb.WriteString(fmt.Sprintf("%-14s %s\n", "Top city:", topCity))
	}
	if topSig != "" {
		sb.WriteString(fmt.Sprintf("%-14s %s\n", "Top signal:", topSig))
	}

	// TASK-150: P&L bar chart for the last 14 days.
	if pnlLine := calibration.DailyPnLLine(records, 14); pnlLine != "" {
		sb.WriteString(fmt.Sprintf("%-14s %s\n", "History:", pnlLine))
	}

	sb.WriteString("</pre>")

	return sb.String()
}

func forecastAlertEmoji(level int) string {
	switch level {
	case 3:
		return "🔴"
	case 2:
		return "🟡"
	default:
		return "🔵"
	}
}

// handleSignals returns a per-signal performance breakdown table for Telegram.
// Shows win rate, Brier score, and estimated P&L for each signal type.
// Signals with fewer than 3 resolved bets show "–" instead of stats.
func handleSignals(bcfg BotConfig) string {
	records, err := calibration.LoadHistory(bcfg.DataRoot)
	if err != nil {
		return "❌ Could not load bet history."
	}

	breakdown := calibration.SignalBreakdown(records)
	if len(breakdown) == 0 {
		return "📈 No resolved bets yet — signal stats unavailable."
	}

	// Compute P&L per signal from raw records.
	type sigPnL struct {
		pnl float64
	}
	pnlBySignal := make(map[string]float64)
	for _, r := range records {
		if r.Outcome == nil || r.Signal == "" {
			continue
		}
		if *r.Outcome {
			pnlBySignal[r.Signal] += r.SizeUSDC/r.MarketPrice - r.SizeUSDC
		} else {
			pnlBySignal[r.Signal] -= r.SizeUSDC
		}
	}

	// Collect and sort signals by win rate descending.
	type sigRow struct {
		name string
		s    calibration.BreakdownStats
	}
	rows := make([]sigRow, 0, len(breakdown))
	for name, s := range breakdown {
		rows = append(rows, sigRow{name, s})
	}
	// Sort: first by count desc (more data = more reliable), then win rate desc.
	for i := 0; i < len(rows); i++ {
		for j := i + 1; j < len(rows); j++ {
			wi := rows[i].s.WinRate()
			wj := rows[j].s.WinRate()
			if rows[i].s.Count < 3 && rows[j].s.Count >= 3 {
				rows[i], rows[j] = rows[j], rows[i]
			} else if rows[i].s.Count >= 3 && rows[j].s.Count >= 3 && wj > wi {
				rows[i], rows[j] = rows[j], rows[i]
			}
		}
	}

	const minSamples = 3
	var sb strings.Builder
	sb.WriteString("📈 <b>Signal Performance</b>\n")
	sb.WriteString("<pre>")
	sb.WriteString(fmt.Sprintf("%-8s %4s %5s %6s %8s\n", "Signal", "N", "Win%", "Brier", "P&L"))
	sb.WriteString(strings.Repeat("─", 38) + "\n")

	for _, row := range rows {
		name := row.name
		s := row.s
		if len(name) > 8 {
			name = name[:7] + "…"
		}

		if s.Count < minSamples {
			sb.WriteString(fmt.Sprintf("%-8s %4d    —      —        —\n", name, s.Count))
			continue
		}

		wr := s.WinRate()
		var emoji string
		switch {
		case wr >= 55:
			emoji = "🟢"
		case wr >= 45:
			emoji = "🟡"
		default:
			emoji = "🔴"
		}

		pnl := pnlBySignal[row.name]
		pnlSign := "+"
		if pnl < 0 {
			pnlSign = ""
		}

		sb.WriteString(fmt.Sprintf("%s %-7s %4d %4.0f%% %6.4f %s%.2f\n",
			emoji, name, s.Count, wr, s.BrierAvg(), pnlSign, pnl))
	}

	sb.WriteString("</pre>")
	sb.WriteString("\n🟢≥55% 🟡45-55% 🔴&lt;45% (min 3 bets)")
	return sb.String()
}

// ── TASK-154: /export command ─────────────────────────────────────────────

// handleExport sends a CSV of resolved bets for the last `days` days as a
// Telegram document.  If the file has ≥ 50 rows it is gzip-compressed first.
func handleExport(bcfg BotConfig, chatID int64, days int, cfg *telegramConfig) {
	records, err := calibration.LoadHistory(bcfg.DataRoot)
	if err != nil {
		sendReply(cfg, chatID, "❌ Could not load bet history.")
		return
	}

	cutoff := time.Now().UTC().Add(-time.Duration(days) * 24 * time.Hour)
	csvHeader := "timestamp,condition_id,city,signal,side,size_usdc,market_price,our_probability,outcome,resolved_at,pnl_usdc\n"

	var rows []string
	for _, r := range records {
		if r.Outcome == nil {
			continue // only resolved bets
		}
		if r.Timestamp.Before(cutoff) {
			continue
		}
		outcomeStr := "true"
		if !*r.Outcome {
			outcomeStr = "false"
		}
		var pnl float64
		if *r.Outcome {
			pnl = r.SizeUSDC/r.MarketPrice - r.SizeUSDC
		} else {
			pnl = -r.SizeUSDC
		}
		resolvedStr := ""
		if !r.ResolvedAt.IsZero() {
			resolvedStr = r.ResolvedAt.UTC().Format(time.RFC3339)
		}
		rows = append(rows, fmt.Sprintf(
			"%s,%s,%s,%s,%s,%.2f,%.6f,%.6f,%s,%s,%.4f\n",
			r.Timestamp.UTC().Format(time.RFC3339),
			r.ConditionID,
			r.City,
			r.Signal,
			r.Side,
			r.SizeUSDC,
			r.MarketPrice,
			r.OurProbability,
			outcomeStr,
			resolvedStr,
			pnl,
		))
	}

	if len(rows) == 0 {
		sendReply(cfg, chatID, fmt.Sprintf("📭 No resolved bets in the last %d days.", days))
		return
	}

	// Build raw CSV bytes.
	var buf bytes.Buffer
	buf.WriteString(csvHeader)
	for _, row := range rows {
		buf.WriteString(row)
	}

	var (
		fileBytes    []byte
		fileName     string
		mimeType     string
	)

	if len(rows) >= 50 {
		// Compress when the file is large.
		var gz bytes.Buffer
		w := gzip.NewWriter(&gz)
		if _, err := w.Write(buf.Bytes()); err != nil {
			sendReply(cfg, chatID, "❌ Compression error: "+err.Error())
			return
		}
		w.Close()
		fileBytes = gz.Bytes()
		fileName = fmt.Sprintf("bets_export_%dd.csv.gz", days)
		mimeType = "application/gzip"
	} else {
		fileBytes = buf.Bytes()
		fileName = fmt.Sprintf("bets_export_%dd.csv", days)
		mimeType = "text/csv"
	}

	if err := sendDocument(cfg, chatID, fileName, mimeType, fileBytes); err != nil {
		slog.Warn("telegram export: sendDocument failed", "err", err)
		sendReply(cfg, chatID, "❌ Failed to send file: "+err.Error())
		return
	}
	slog.Info("telegram export: sent bet history", "rows", len(rows), "days", days, "compressed", len(rows) >= 50)
}

// sendDocument uploads a file to a Telegram chat using the sendDocument API.
func sendDocument(cfg *telegramConfig, chatID int64, fileName, mimeType string, data []byte) error {
	var body bytes.Buffer
	w := multipart.NewWriter(&body)

	// chat_id field
	if err := w.WriteField("chat_id", fmt.Sprint(chatID)); err != nil {
		return err
	}

	// document field (binary file)
	part, err := w.CreateFormFile("document", fileName)
	if err != nil {
		return err
	}
	if _, err := part.Write(data); err != nil {
		return err
	}
	w.Close()

	url := fmt.Sprintf("https://api.telegram.org/bot%s/sendDocument", cfg.token)
	req, err := http.NewRequest(http.MethodPost, url, &body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", w.FormDataContentType())

	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		raw, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("telegram sendDocument HTTP %d: %s", resp.StatusCode, raw)
	}
	return nil
}

// ── /healthcheck handler ──────────────────────────────────────────────────

// handleHealthcheck returns a comprehensive system health overview:
//   - Bot uptime
//   - Data source status (from source_health.json)
//   - Brier score + drift alert
//   - Rolling win rate
//   - Daily risk state (bets today, daily P&L)
//   - Per-signal adaptive Kelly multipliers (only non-unity entries)
//   - Forecast cache freshness (open positions count)
//
// TASK-157
func handleHealthcheck(bcfg BotConfig) string {
	var sb strings.Builder
	sb.WriteString("<b>🩺 Health Check</b>\n")
	sb.WriteString("<pre>")

	// ── Uptime ───────────────────────────────────────────────────────────
	if !bcfg.StartTime.IsZero() {
		uptime := time.Since(bcfg.StartTime).Round(time.Second)
		sb.WriteString(fmt.Sprintf("Uptime      : %s\n", uptime))
	}
	pausedStatus := "running"
	if IsPaused() {
		pausedStatus = "PAUSED ⏸"
	}
	sb.WriteString(fmt.Sprintf("Status      : %s\n", pausedStatus))
	sb.WriteString("\n")

	// ── Data sources ─────────────────────────────────────────────────────
	sourceHealth := collectors.LoadSourceHealth(bcfg.DataRoot)
	if len(sourceHealth) > 0 {
		sb.WriteString("── Data Sources ──────────────────\n")
		// Sort by source name for stable output.
		names := make([]string, 0, len(sourceHealth))
		for k := range sourceHealth {
			names = append(names, k)
		}
		sort.Strings(names)
		now := time.Now().UTC()
		for _, name := range names {
			h := sourceHealth[name]
			status := h.Status(now)
			icon := "✅"
			if status == "degraded" {
				icon = "⚠️"
			} else if status == "down" || status == "unknown" {
				icon = "❌"
			}
			age := "never"
			if !h.LastSuccess.IsZero() {
				a := now.Sub(h.LastSuccess)
				if a < time.Minute {
					age = fmt.Sprintf("%ds", int(a.Seconds()))
				} else if a < time.Hour {
					age = fmt.Sprintf("%dm", int(a.Minutes()))
				} else {
					age = fmt.Sprintf("%.1fh", a.Hours())
				}
			}
			sb.WriteString(fmt.Sprintf("%-14s %s %-8s up=%.0f%% fails=%d\n",
				name, icon, age, h.UpRatePct(), h.ConsecFails))
		}
		sb.WriteString("\n")
	}

	// ── Calibration / performance ─────────────────────────────────────────
	records, err := calibration.LoadHistory(bcfg.DataRoot)
	if err != nil {
		sb.WriteString("❌ history unavailable\n")
		sb.WriteString("</pre>")
		return sb.String()
	}

	brier, brierN, _ := calibration.BrierScore(records)
	brierStr := "n/a"
	if brierN > 0 {
		brierStr = fmt.Sprintf("%.4f (n=%d)", brier, brierN)
	}

	winRate, wrN := calibration.ComputeRollingWinRate(records, 20)
	wrStr := "n/a"
	if wrN > 0 {
		wrStr = fmt.Sprintf("%.0f%% (last %d)", winRate*100, wrN)
	}

	sb.WriteString("── Calibration ───────────────────\n")
	sb.WriteString(fmt.Sprintf("Brier score : %s\n", brierStr))
	sb.WriteString(fmt.Sprintf("Win rate    : %s\n", wrStr))

	// Drift alert
	driftAlert, driftMsg := calibration.DriftAlert(records, 14, 30, 0.15)
	if driftAlert {
		sb.WriteString(fmt.Sprintf("Drift ⚠️  : %s\n", driftMsg))
	} else {
		sb.WriteString("Drift       : ok\n")
	}

	// Streak alert
	streakAlert, streakMsg := calibration.StreakAlert(records, 4)
	if streakAlert {
		sb.WriteString(fmt.Sprintf("Streak ⚠️ : %s\n", streakMsg))
	} else {
		sb.WriteString("Streak      : ok\n")
	}
	sb.WriteString("\n")

	// ── Risk / daily state ────────────────────────────────────────────────
	dailyCount, dailyPnL := risk.DailyStats(records)
	openPos := risk.OpenPositionsCount(records)
	sb.WriteString("── Risk State ────────────────────\n")
	sb.WriteString(fmt.Sprintf("Bets today  : %d\n", dailyCount))
	sb.WriteString(fmt.Sprintf("Daily P&L   : %+.2f USDC\n", dailyPnL))
	sb.WriteString(fmt.Sprintf("Open pos    : %d\n", openPos))
	sb.WriteString(fmt.Sprintf("Bankroll    : %.2f USDC\n", bcfg.Bankroll))
	sb.WriteString("\n")

	// ── Signal Kelly multipliers (deviating from 1.0x) ───────────────────
	skMults := calibration.SignalKellyMultipliers(records)
	nonNeutral := make([]string, 0)
	for sig, info := range skMults {
		if info.Multiplier != 1.0 && info.Count >= calibration.MinSignalSamples {
			nonNeutral = append(nonNeutral, sig)
		}
	}
	if len(nonNeutral) > 0 {
		sort.Strings(nonNeutral)
		sb.WriteString("── Signal Kelly ──────────────────\n")
		for _, sig := range nonNeutral {
			info := skMults[sig]
			icon := "📈"
			if info.Multiplier < 1.0 {
				icon = "📉"
			}
			sb.WriteString(fmt.Sprintf("%-8s %s %.2fx (brier=%.3f,n=%d)\n",
				sig, icon, info.Multiplier, info.BrierScore, info.Count))
		}
		sb.WriteString("\n")
	}

	sb.WriteString("</pre>")
	sb.WriteString(fmt.Sprintf("\n<i>%s UTC</i>", time.Now().UTC().Format("2006-01-02 15:04:05")))
	return sb.String()
}

// ── /source-weights handler (TASK-160) ────────────────────────────────────

// handleSourceWeights returns a table of current source weights (dynamic when
// enough data exists, static otherwise) compared to the static baseline.
func handleSourceWeights(bcfg BotConfig) string {
	accuracy := collectors.LoadSourceAccuracy(bcfg.DataRoot)

	static := map[string]float64{
		"ecmwf":     0.25,
		"openmeteo": 0.20,
		"nasa":      0.17,
		"noaa":      0.13,
		"goes":      0.08,
		"hrrr":      0.12,
		"gfs":       0.10,
	}

	var sb strings.Builder
	sb.WriteString("<b>⚖️ Source Weights</b>\n")
	sb.WriteString("<pre>")

	if len(accuracy) == 0 {
		sb.WriteString("Using static weights (no accuracy data yet).\n\n")
		sources := make([]string, 0, len(static))
		for s := range static {
			sources = append(sources, s)
		}
		sort.Strings(sources)
		sb.WriteString(fmt.Sprintf("%-12s  %6s\n", "Source", "Weight"))
		sb.WriteString(fmt.Sprintf("%s\n", repeatDash(22)))
		for _, src := range sources {
			sb.WriteString(fmt.Sprintf("%-12s  %5.1f%%\n", src, static[src]*100))
		}
		sb.WriteString("</pre>")
		return sb.String()
	}

	dynamic := collectors.DynamicWeights(accuracy)

	// Collect all known sources from both maps.
	seen := make(map[string]bool)
	for s := range static {
		seen[s] = true
	}
	for s := range accuracy {
		seen[s] = true
	}
	sources := make([]string, 0, len(seen))
	for s := range seen {
		sources = append(sources, s)
	}
	sort.Strings(sources)

	sb.WriteString(fmt.Sprintf("%-12s  %7s  %7s  %6s  %5s\n",
		"Source", "Dynamic", "Static", "Brier", "N"))
	sb.WriteString(fmt.Sprintf("%s\n", repeatDash(48)))

	for _, src := range sources {
		dw := dynamic[src]
		sw := static[src]
		brierStr := "  —  "
		nStr := "—"
		if st, ok := accuracy[src]; ok && st.Count > 0 {
			brierStr = fmt.Sprintf("%.3f", st.BrierScore())
			nStr = fmt.Sprintf("%d", st.Count)
		}
		delta := dw - sw
		deltaStr := fmt.Sprintf("%+.1f%%", delta*100)
		sb.WriteString(fmt.Sprintf("%-12s  %6.1f%%  %6.1f%%  %s  %5s  %s\n",
			src, dw*100, sw*100, brierStr, nStr, deltaStr))
	}

	sb.WriteString("</pre>")
	sb.WriteString(fmt.Sprintf("\n<i>%s UTC</i>", time.Now().UTC().Format("2006-01-02 15:04:05")))
	return sb.String()
}

// ── /pnl-city handler (TASK-163) ─────────────────────────────────────────────

// handlePnLCity returns a per-city P&L breakdown table for Telegram.
// Rows are sorted by total profit descending; only cities with ≥1 resolved bet appear.
func handlePnLCity(bcfg BotConfig) string {
	records, err := calibration.LoadHistory(bcfg.DataRoot)
	if err != nil {
		return fmt.Sprintf("⚠️ Could not load history: %v", err)
	}

	stats := calibration.CityPnL(records)
	if len(stats) == 0 {
		return "No resolved bets with city data yet."
	}

	var sb strings.Builder
	sb.WriteString("<b>🏙️ P&L by City</b>\n")
	sb.WriteString("<pre>")
	sb.WriteString(fmt.Sprintf("%-14s %4s %4s %5s %8s %6s\n",
		"City", "Bets", "Wins", "Win%", "PnL", "ROI%"))
	sb.WriteString(repeatDash(47) + "\n")

	var totalBets, totalWins int
	var totalPnL, totalRisked float64

	for _, s := range stats {
		sign := "+"
		if s.PnLUSDC < 0 {
			sign = ""
		}
		roi := 0.0
		if s.TotalRisked > 0 {
			roi = s.PnLUSDC / s.TotalRisked * 100
		}
		sb.WriteString(fmt.Sprintf("%-14s %4d %4d %4.0f%% %s%.2f %+5.1f%%\n",
			s.City, s.Bets, s.Wins, s.WinRate(), sign, s.PnLUSDC, roi))
		totalBets += s.Bets
		totalWins += s.Wins
		totalPnL += s.PnLUSDC
		totalRisked += s.TotalRisked
	}

	sb.WriteString(repeatDash(47) + "\n")
	overallROI := 0.0
	if totalRisked > 0 {
		overallROI = totalPnL / totalRisked * 100
	}
	sign := "+"
	if totalPnL < 0 {
		sign = ""
	}
	sb.WriteString(fmt.Sprintf("%-14s %4d %4d %4.0f%% %s%.2f %+5.1f%%\n",
		"TOTAL", totalBets, totalWins,
		float64(totalWins)/float64(totalBets)*100,
		sign, totalPnL, overallROI))
	sb.WriteString("</pre>")
	sb.WriteString(fmt.Sprintf("\n<i>%s UTC</i>", time.Now().UTC().Format("2006-01-02 15:04")))
	return sb.String()
}

// repeatDash returns a string of n dashes.
func repeatDash(n int) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = '-'
	}
	return string(b)
}

// ── TASK-169: /winrate command ─────────────────────────────────────────────

// handleWinRate returns a per-signal rolling win rate table for the last 20
// resolved bets of each signal type.
func handleWinRate(bcfg BotConfig) string {
	records, err := calibration.LoadHistory(bcfg.DataRoot)
	if err != nil {
		return "❌ Could not load bet history: " + err.Error()
	}

	// Filter resolved records only.
	var resolved []calibration.BetRecord
	for _, r := range records {
		if r.Outcome != nil {
			resolved = append(resolved, r)
		}
	}
	if len(resolved) == 0 {
		return "📭 No resolved bets yet."
	}

	// Group by signal, keeping only the last 20 per signal (newest first).
	type signalStats struct {
		bets  int
		wins  int
		pnl   float64
	}
	stats := map[string]*signalStats{}
	signalOrder := []string{}

	// Walk resolved in reverse (newest first) and collect up to 20 per signal.
	signalCount := map[string]int{}
	for i := len(resolved) - 1; i >= 0; i-- {
		r := resolved[i]
		sig := r.Signal
		if sig == "" {
			sig = "unknown"
		}
		if signalCount[sig] >= 20 {
			continue
		}
		signalCount[sig]++
		if _, ok := stats[sig]; !ok {
			stats[sig] = &signalStats{}
			signalOrder = append(signalOrder, sig)
		}
		stats[sig].bets++
		if *r.Outcome {
			stats[sig].wins++
			// net gain = return - stake; MarketPrice is the price paid (0-1 scale)
			if r.MarketPrice > 0 {
				stats[sig].pnl += r.SizeUSDC/r.MarketPrice - r.SizeUSDC
			}
		} else {
			stats[sig].pnl -= r.SizeUSDC
		}
	}

	// Sort signals by pnl descending.
	sort.Slice(signalOrder, func(i, j int) bool {
		return stats[signalOrder[i]].pnl > stats[signalOrder[j]].pnl
	})

	var sb strings.Builder
	sb.WriteString("<b>📊 Rolling Win Rate (last 20 bets/signal)</b>\n<pre>")
	sb.WriteString(fmt.Sprintf("%-10s %4s %5s %7s\n", "Signal", "N", "Win%", "PnL"))
	sb.WriteString(repeatDash(30) + "\n")

	totalBets, totalWins := 0, 0
	totalPnL := 0.0
	for _, sig := range signalOrder {
		s := stats[sig]
		wr := 0.0
		if s.bets > 0 {
			wr = float64(s.wins) / float64(s.bets) * 100
		}
		sb.WriteString(fmt.Sprintf("%-10s %4d %4.0f%% %+7.2f\n", sig, s.bets, wr, s.pnl))
		totalBets += s.bets
		totalWins += s.wins
		totalPnL += s.pnl
	}

	sb.WriteString(repeatDash(30) + "\n")
	overallWR := 0.0
	if totalBets > 0 {
		overallWR = float64(totalWins) / float64(totalBets) * 100
	}
	sb.WriteString(fmt.Sprintf("%-10s %4d %4.0f%% %+7.2f\n", "TOTAL", totalBets, overallWR, totalPnL))
	sb.WriteString("</pre>")
	sb.WriteString(fmt.Sprintf("\n<i>%s UTC</i>", time.Now().UTC().Format("2006-01-02 15:04")))
	return sb.String()
}

// ── TASK-167: /explain <conditionID> command ───────────────────────────────

// handleExplainMarket fetches markets, finds the one matching conditionID,
// runs ExplainEvaluate, and returns a formatted audit trail for Telegram.
func handleExplainMarket(bcfg BotConfig, conditionID string) string {
	if conditionID == "" {
		return "Usage: /explain &lt;conditionID&gt;\nExample: /explain 0x1234...abcd"
	}

	mks, err := markets.GetWeatherMarkets()
	if err != nil {
		return "❌ Could not fetch markets: " + err.Error()
	}

	var target *markets.Market
	for i := range mks {
		if strings.EqualFold(mks[i].ConditionID, conditionID) {
			target = &mks[i]
			break
		}
	}
	if target == nil {
		return fmt.Sprintf("❌ Market <code>%s</code> not found in current discovery window.", conditionID)
	}

	// Load forecast — try fused cache first, fall back to open-meteo.
	ff, _ := loadForecastForDisplay(bcfg, target.City)

	r := strategy.ExplainEvaluate(*target, ff, bcfg.Bankroll, bcfg.MinEdge, bcfg.MaxBet)

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("<b>🔍 Explain: %s / %s</b>\n", target.City, target.Signal))
	sb.WriteString(fmt.Sprintf("Market: <code>%s</code>\n", conditionID))
	sb.WriteString(fmt.Sprintf("YES %.2f  NO %.2f\n\n", target.YesPrice, target.NoPrice))

	if ff != nil {
		sb.WriteString(fmt.Sprintf("Confidence:  %.2f\n", r.Confidence))
		sb.WriteString(fmt.Sprintf("Consensus:   %.2f\n", r.ConsensusScore))
		sb.WriteString(fmt.Sprintf("Sources:     %s\n", strings.Join(r.Sources, ", ")))
		if r.EnsUnc > 0 {
			sb.WriteString(fmt.Sprintf("EnsUnc:      %.1f°C\n", r.EnsUnc))
		}
		sb.WriteString("\n")
		sb.WriteString(fmt.Sprintf("RawP →  %.3f\n", r.RawP))
		sb.WriteString(fmt.Sprintf("SeasonP → %.3f\n", r.SeasonP))
		sb.WriteString(fmt.Sprintf("FinalP:   %.3f\n", r.FinalP))
		sb.WriteString("\n")
		sb.WriteString(fmt.Sprintf("YES edge: %+.3f\n", r.YesEdge))
		sb.WriteString(fmt.Sprintf("NO  edge: %+.3f\n", r.NoEdge))
		if r.BestSide != "" {
			sb.WriteString(fmt.Sprintf("Best:     %s (edge %+.3f)\n", r.BestSide, r.BestEdge))
			sb.WriteString(fmt.Sprintf("Kelly:    $%.2f\n", r.KellyRaw))
			sb.WriteString(fmt.Sprintf("EnsScale: %.2f\n", r.EnsScale))
			sb.WriteString(fmt.Sprintf("Size:     $%.2f\n", r.FinalSize))
		}
	}

	sb.WriteString("\n")
	if r.IsBet() {
		sb.WriteString(fmt.Sprintf("✅ <b>%s</b>", r.Action))
	} else {
		sb.WriteString(fmt.Sprintf("⏭ <b>%s</b>", r.Action))
	}
	sb.WriteString(fmt.Sprintf("\n<i>%s UTC</i>", time.Now().UTC().Format("2006-01-02 15:04")))
	return sb.String()
}

// handleConfig returns the current bot configuration as a formatted Telegram
// message. Sensitive fields (private key, tokens) are never shown. (TASK-172)
func handleConfig(bcfg BotConfig) string {
	mode := "live"
	if bcfg.DryRun {
		mode = "dry-run"
	}
	loopStr := "once"
	if bcfg.LoopSec > 0 {
		loopStr = fmt.Sprintf("%ds", bcfg.LoopSec)
	}
	kelly := bcfg.KellyFraction
	if kelly == 0 {
		kelly = 0.5
	}
	maxKelly := bcfg.MaxKellyFraction
	if maxKelly == 0 {
		maxKelly = 0.05
	}
	fee := bcfg.ProtocolFeeRate
	if fee == 0 {
		fee = 0.02
	}
	bankroll := calibration.LoadBankroll(bcfg.DataRoot)

	var sb strings.Builder
	sb.WriteString("⚙️ <b>Bot Configuration</b>\n")
	sb.WriteString("<pre>")
	sb.WriteString(fmt.Sprintf("%-22s %s\n", "mode:", mode))
	sb.WriteString(fmt.Sprintf("%-22s %.4f (%.1f%%)\n", "min_edge:", bcfg.MinEdge, bcfg.MinEdge*100))
	sb.WriteString(fmt.Sprintf("%-22s $%.2f\n", "max_bet:", bcfg.MaxBet))
	sb.WriteString(fmt.Sprintf("%-22s $%.2f\n", "bankroll:", bankroll))
	sb.WriteString(fmt.Sprintf("%-22s %.2f (%.0f%%)\n", "kelly_fraction:", kelly, kelly*100))
	sb.WriteString(fmt.Sprintf("%-22s %.3f (%.1f%%)\n", "max_kelly_fraction:", maxKelly, maxKelly*100))
	sb.WriteString(fmt.Sprintf("%-22s %.2f (%.0f%%)\n", "protocol_fee:", fee, fee*100))
	sb.WriteString(fmt.Sprintf("%-22s %s\n", "loop_interval:", loopStr))
	sb.WriteString(fmt.Sprintf("%-22s %s\n", "data_root:", bcfg.DataRoot))
	sb.WriteString("</pre>")
	sb.WriteString(fmt.Sprintf("\n<i>%s UTC</i>", time.Now().UTC().Format("2006-01-02 15:04")))
	return sb.String()
}

// handleMarkets returns the top-5 active weather markets sorted by spread
// (tightest first = best trading opportunity). (TASK-179)
func handleMarkets(bcfg BotConfig) string {
	mks, err := markets.GetWeatherMarkets()
	if err != nil {
		return fmt.Sprintf("❌ Failed to fetch markets: %v", err)
	}

	// Enrich with live liquidity data (spread, thin flag, fair value).
	markets.EnrichWithLiquidity(mks)

	// Filter to markets with identified city+signal and not stale.
	var active []markets.Market
	for _, m := range mks {
		if m.City == "" || m.Signal == "" {
			continue
		}
		if m.Stale {
			continue
		}
		active = append(active, m)
	}

	if len(active) == 0 {
		return "📭 No active weather markets found."
	}

	// Sort by spread ascending (tightest = best opportunity).
	sort.Slice(active, func(i, j int) bool {
		return active[i].Spread < active[j].Spread
	})

	total := len(active)
	const maxShow = 5
	if len(active) > maxShow {
		active = active[:maxShow]
	}

	now := time.Now().UTC()
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("🌦 <b>Top %d Weather Markets</b> (of %d active)\n", len(active), total))
	sb.WriteString("<pre>")
	sb.WriteString(fmt.Sprintf("%-12s %-6s %5s %5s %5s %6s\n", "City", "Signal", "YES", "NO", "Spr", "Expiry"))
	sb.WriteString(strings.Repeat("─", 47) + "\n")
	for _, m := range active {
		expiryStr := "?"
		if !m.ExpiryUTC.IsZero() {
			h := m.ExpiryUTC.Sub(now).Hours()
			switch {
			case h < 0:
				expiryStr = "exp"
			case h < 24:
				expiryStr = fmt.Sprintf("%.0fh", h)
			default:
				expiryStr = fmt.Sprintf("%.0fd", h/24)
			}
		}
		city := m.City
		if len(city) > 12 {
			city = city[:12]
		}
		sb.WriteString(fmt.Sprintf("%-12s %-6s %5.2f %5.2f %5.2f %6s\n",
			city, m.Signal,
			m.YesPrice, m.NoPrice,
			m.Spread, expiryStr,
		))
	}
	sb.WriteString("</pre>")
	sb.WriteString(fmt.Sprintf("\n<i>%s UTC</i>", now.Format("15:04")))
	return sb.String()
}

// ── TASK-182: /top-edge command ───────────────────────────────────────────

// handleTopEdge returns the top markets ranked by edge×confidence score.
// Unlike /markets (sorted by spread), this ranks by true opportunity quality:
// a wide edge on a high-confidence forecast beats a tight spread on shaky data.
func handleTopEdge(bcfg BotConfig) string {
	mks, err := markets.GetWeatherMarkets()
	if err != nil {
		return fmt.Sprintf("❌ Failed to fetch markets: %v", err)
	}

	type scoredEntry struct {
		m     markets.Market
		side  string
		edge  float64
		conf  float64
		score float64
		ourP  float64
	}

	var entries []scoredEntry

	for _, m := range mks {
		if m.City == "" || m.Signal == "" {
			continue
		}

		// Load from disk cache (up to 3h stale is fine here).
		ff, ok := collectors.LoadForecastCache(m.City, m.DaysUntilExpiry(), bcfg.DataRoot, 3*time.Hour)
		if !ok || ff == nil {
			// fallback to day 0
			ff, ok = collectors.LoadForecastCache(m.City, 0, bcfg.DataRoot, 3*time.Hour)
		}
		if !ok || ff == nil {
			continue
		}

		// Compute raw signal probability (same logic as ScoreMarket).
		heatThreshold := 35.0
		if m.ThresholdC != 0 {
			heatThreshold = m.ThresholdC
		}
		var ourP float64
		switch m.Signal {
		case "heat":
			ourP = weather.HeatProbability(ff.Forecast, heatThreshold)
		case "cold":
			ourP = 1 - weather.HeatProbability(ff.Forecast, heatThreshold)
		case "rain":
			ourP = weather.RainProbability(ff.Forecast)
		case "sunny":
			ourP = weather.SunnyProbability(ff.Forecast)
		case "wind":
			ourP = math.Min(0.95, ff.WindSpeedKMH/80.0)
		case "snow":
			ourP = weather.SnowProbability(ff.Forecast)
		case "fog":
			ourP = weather.FogProbability(ff.Forecast)
		case "humid":
			ourP = weather.HumidProbability(ff.Forecast, m.ThresholdC)
		case "dry":
			ourP = weather.DryProbability(ff.Forecast)
		default:
			ourP = 0.5
		}

		yesEdge := ourP - m.YesPrice
		noEdge := (1 - ourP) - m.NoPrice

		var side string
		var bestEdge float64
		if yesEdge > noEdge && yesEdge > 0 {
			side, bestEdge = "YES", yesEdge
		} else if noEdge > 0 {
			side, bestEdge = "NO", noEdge
		} else {
			continue // no positive edge on either side
		}

		score := bestEdge * ff.Confidence
		entries = append(entries, scoredEntry{
			m:     m,
			side:  side,
			edge:  bestEdge,
			conf:  ff.Confidence,
			score: score,
			ourP:  ourP,
		})
	}

	if len(entries) == 0 {
		return "📭 No markets with positive edge found in forecast cache."
	}

	// Sort by score descending.
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].score > entries[j].score
	})

	const maxShow = 5
	if len(entries) > maxShow {
		entries = entries[:maxShow]
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("🎯 <b>Top %d Markets by Edge×Confidence</b>\n", len(entries)))
	sb.WriteString("<pre>")
	sb.WriteString(fmt.Sprintf("%-12s %-6s %-4s %5s %5s %6s %5s\n",
		"City", "Signal", "Side", "Edge", "Conf", "Score", "Price"))
	sb.WriteString(strings.Repeat("─", 51) + "\n")
	for _, e := range entries {
		price := e.m.YesPrice
		if e.side == "NO" {
			price = e.m.NoPrice
		}
		city := e.m.City
		if len(city) > 12 {
			city = city[:12]
		}
		sb.WriteString(fmt.Sprintf("%-12s %-6s %-4s %5.2f %4.0f%% %6.4f %5.2f\n",
			city, e.m.Signal, e.side,
			e.edge, e.conf*100, e.score, price))
	}
	sb.WriteString("</pre>")
	sb.WriteString(fmt.Sprintf("\n<i>Score = edge × confidence | %s UTC</i>",
		time.Now().UTC().Format("15:04")))
	return sb.String()
}

// handleEV returns the EV capture ratio for the last 50 resolved bets,
// plus a per-signal breakdown. (TASK-187)
func handleEV(bcfg BotConfig) string {
	records, err := calibration.LoadHistory(bcfg.DataRoot)
	if err != nil {
		return fmt.Sprintf("❌ Error loading history: %v", err)
	}

	const window = 50
	res := calibration.RollingEV(records, window)
	if res.Count == 0 {
		return "📭 No resolved bets yet — EV capture unavailable."
	}

	// Status emoji for capture ratio.
	statusEmoji := "🚨"
	switch {
	case res.CaptureRatio >= 0.70:
		statusEmoji = "✅"
	case res.CaptureRatio >= 0.50:
		statusEmoji = "⚠️"
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("📊 <b>EV Capture Ratio (last %d bets)</b>\n\n", res.Count))
	sb.WriteString(fmt.Sprintf("Expected EV:  <b>$%.2f</b>\n", res.ExpectedEV))
	sb.WriteString(fmt.Sprintf("Realized P&L: <b>$%.2f</b>\n", res.RealizedPnL))
	sb.WriteString(fmt.Sprintf("Capture:      <b>%.0f%%</b> %s\n\n", res.CaptureRatio*100, statusEmoji))

	// Per-signal breakdown.
	bySignal := calibration.RollingEVBySignal(records, window)
	if len(bySignal) > 0 {
		sb.WriteString("<pre>")
		sb.WriteString(fmt.Sprintf("%-8s %5s %7s %7s %7s\n", "Signal", "N", "ExpEV", "PnL", "Cap%"))
		sb.WriteString(strings.Repeat("─", 40) + "\n")

		// Collect and sort signals.
		type sigRow struct {
			sig string
			ev  calibration.EVResult
		}
		var rows []sigRow
		for sig, ev := range bySignal {
			rows = append(rows, sigRow{sig, ev})
		}
		sort.Slice(rows, func(i, j int) bool {
			return rows[i].ev.CaptureRatio > rows[j].ev.CaptureRatio
		})

		for _, row := range rows {
			capStr := "N/A"
			if row.ev.ExpectedEV > 0 {
				capStr = fmt.Sprintf("%.0f%%", row.ev.CaptureRatio*100)
			}
			sb.WriteString(fmt.Sprintf("%-8s %5d %7.2f %7.2f %7s\n",
				row.sig, row.ev.Count, row.ev.ExpectedEV, row.ev.RealizedPnL, capStr))
		}
		sb.WriteString("</pre>")
	}

	sb.WriteString(fmt.Sprintf("\n<i>✅≥70%% ⚠️≥50%% 🚨<50%% | %s UTC</i>", time.Now().UTC().Format("15:04")))
	return sb.String()
}

// handleHelp returns a formatted list of all available Telegram commands. (TASK-189)
func handleHelp() string {
	return `<b>🤖 Available Commands</b>

<b>📊 Analytics</b>
<pre>/status          Brier score, open positions, today P&L
/summary         Multi-section health overview
/signals         Per-signal win rate + Brier breakdown
/winrate         Rolling win rate per signal (last 20)
/pnl-city        P&L breakdown by city
/ev              EV capture ratio (last 50 bets)
/daily           Today's bets timeline + running P&L</pre>

<b>🌤 Forecasts &amp; Markets</b>
<pre>/forecast [city] Weather forecast (all cities or one)
/forecast-quality Confidence + age for all city forecasts
/next            Top-3 best bet opportunities right now
/markets         Active weather markets (price/spread/expiry)
/top-edge        Markets ranked by edge×confidence</pre>

<b>📂 Positions &amp; History</b>
<pre>/positions       Open (unresolved) bets
/explain &lt;id&gt;   Strategy audit trail for a conditionID
/export [days]   Send bet history CSV (default: 30 days)</pre>

<b>🩺 System</b>
<pre>/healthcheck     Data sources, calibration, risk state
/source-weights  Dynamic source weights vs baseline
/config          Current bot configuration
/watchlist       Manage watchlisted markets</pre>

<b>⚙️ Control</b>
<pre>/pause           Suspend all trading
/resume          Resume trading</pre>`
}

// handleDaily returns a chronological timeline of today's bets with running P&L. (TASK-190)
func handleDaily(bcfg BotConfig) string {
	records, err := calibration.LoadHistory(bcfg.DataRoot)
	if err != nil {
		return "❌ Could not load bet history."
	}

	today := time.Now().UTC().Format("2006-01-02")
	type dailyBet struct {
		r          calibration.BetRecord
		runningPnL float64
	}
	var bets []dailyBet
	var running float64
	for _, r := range records {
		if r.Timestamp.UTC().Format("2006-01-02") != today {
			continue
		}
		var delta float64
		if r.Outcome != nil {
			if *r.Outcome {
				delta = r.SizeUSDC/r.MarketPrice - r.SizeUSDC
			} else {
				delta = -r.SizeUSDC
			}
		}
		running += delta
		bets = append(bets, dailyBet{r, running})
	}

	if len(bets) == 0 {
		return fmt.Sprintf("📭 No bets today (%s UTC).", today)
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("<b>📅 Today's Bets — %s UTC</b>\n\n", today))
	sb.WriteString("<pre>")
	sb.WriteString(fmt.Sprintf("%-5s %-16s %-4s %5s %5s %-7s %7s\n",
		"Time", "City/Signal", "Side", "Size", "Entry", "Outcome", "RunPnL"))
	sb.WriteString(strings.Repeat("─", 55) + "\n")

	var resolvedCount int
	for _, b := range bets {
		r := b.r
		timeStr := r.Timestamp.UTC().Format("15:04")
		label := r.City + "/" + r.Signal
		if len(label) > 15 {
			label = label[:15]
		}
		outcomeStr := "open"
		if r.Outcome != nil {
			resolvedCount++
			if *r.Outcome {
				outcomeStr = "WIN ✅"
			} else {
				outcomeStr = "LOSS ❌"
			}
		}
		pnlStr := fmt.Sprintf("%+.2f", b.runningPnL)
		sb.WriteString(fmt.Sprintf("%-5s %-16s %-4s %5.2f %5.2f %-7s %7s\n",
			timeStr, label, r.Side, r.SizeUSDC, r.MarketPrice, outcomeStr, pnlStr))
	}

	sb.WriteString(strings.Repeat("─", 55) + "\n")
	sb.WriteString(fmt.Sprintf("Total: %d bets | %d resolved | P&L: %+.2f USDC",
		len(bets), resolvedCount, running))
	sb.WriteString("</pre>")
	return sb.String()
}

// handleForecastQuality returns per-city forecast confidence and cache age. (TASK-191)
func handleForecastQuality(bcfg BotConfig) string {
	type cityRow struct {
		city       string
		confidence float64
		sources    []string
		age        time.Duration
		found      bool
	}

	cities := make([]string, 0, len(weather.Cities))
	for c := range weather.Cities {
		cities = append(cities, c)
	}
	sort.Strings(cities)

	rows := make([]cityRow, 0, len(cities))
	const maxAge = 6 * time.Hour
	for _, city := range cities {
		ff, ok := collectors.LoadForecastCache(city, 0, bcfg.DataRoot, maxAge)
		if !ok || ff == nil {
			rows = append(rows, cityRow{city: city})
			continue
		}
		rows = append(rows, cityRow{
			city:       city,
			confidence: ff.Confidence,
			sources:    ff.Sources,
			age:        time.Since(ff.FetchedAt),
			found:      true,
		})
	}

	var sb strings.Builder
	sb.WriteString("<b>🌤 Forecast Quality</b>\n\n")
	sb.WriteString("<pre>")
	sb.WriteString(fmt.Sprintf("%-14s %5s %5s %-6s\n", "City", "Conf", "Age", "Status"))
	sb.WriteString(strings.Repeat("─", 32) + "\n")

	ready := 0
	for _, row := range rows {
		if !row.found {
			sb.WriteString(fmt.Sprintf("%-14s  n/a   n/a ❌ no cache\n", row.city))
			continue
		}
		ageStr := fmt.Sprintf("%.0fm", row.age.Minutes())
		if row.age >= time.Hour {
			ageStr = fmt.Sprintf("%.1fh", row.age.Hours())
		}
		status := "✅"
		if row.confidence >= 0.5 && row.age < 3*time.Hour {
			ready++
		} else if row.confidence >= 0.35 && row.age < 6*time.Hour {
			status = "⚠️"
		} else {
			status = "❌"
		}
		sb.WriteString(fmt.Sprintf("%-14s %4.0f%% %5s %s\n",
			row.city, row.confidence*100, ageStr, status))
	}
	sb.WriteString(strings.Repeat("─", 32) + "\n")
	sb.WriteString(fmt.Sprintf("Ready: %d/%d cities", ready, len(cities)))
	sb.WriteString("</pre>")
	sb.WriteString(fmt.Sprintf("\n<i>✅ conf≥50%% &amp; age<3h | ⚠️ marginal | ❌ stale/missing | %s UTC</i>",
		time.Now().UTC().Format("15:04")))
	return sb.String()
}
