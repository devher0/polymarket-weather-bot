// telegram_commands.go — TASK-111: Telegram bot command polling.
//
// Supported commands:
//   /status      — Brier score, open positions, P&L for today
//   /positions   — list of open (unresolved) bets
//   /next        — top-3 best bets right now (dry-run, from cached forecasts)
//   /forecast [city] — current weather forecast from cache (or live fetch)
//   /summary     — compact multi-section health overview (TASK-146)
//   /signals     — per-signal win rate + Brier breakdown (TASK-151)
//   /export [days] — send CSV of resolved bets as file (TASK-154)
//   /healthcheck — data source status, calibration, risk state, signal kelly (TASK-157)
//   /pause       — suspend all trading
//   /resume      — resume trading
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
	DataRoot  string
	Bankroll  float64
	MinEdge   float64
	MaxBet    float64
	StartTime time.Time // when the bot process started (for uptime display in /healthcheck)
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
				case "/status":
					sendReply(cfg, chatID, handleStatus(bcfg))
				case "/positions":
					sendReply(cfg, chatID, handlePositions(bcfg))
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
