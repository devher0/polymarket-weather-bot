// telegram_commands.go — Telegram bot command polling.
//
// Supported commands:
//   /help            — list all available commands
//   /status          — Brier score, open positions, P&L for today
//   /positions       — list of open (unresolved) bets
//   /daily           — today's bets timeline with running P&L
//   /compare         — compare today vs yesterday: bets/wins/edge/PnL
//   /trend [city]    — 7-day trend for a city: daily bets/wins/edge/PnL
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
//   /countdown       — active markets sorted by time-to-close with urgency badges (TASK-201)
//   /breakdown       — city×signal P&L matrix sorted by ROI% (TASK-207)
//   /missed          — today's evaluated-but-skipped markets sorted by edge (TASK-209)
//   /pause           — suspend all trading
//   /resume          — resume trading
//   /watchdog        — last successful cycle time + overdue status (TASK-200)
//   /sharpe          — 30-day + 7-day Sharpe ratio with trend (TASK-210)
//   /timing          — best/worst UTC hours to bet by win rate (TASK-211)
//   /drawdown        — current bankroll drawdown from historical peak (TASK-212)
//   /streak          — current win/loss streak + historical best/worst (TASK-213)
//   /weekly          — 4-week P&L table with best/worst week (TASK-214)
//   /roi             — cumulative ROI% from starting bankroll with weekly sparkline (TASK-215)
//   /compare-signals — compare per-signal win rate: last 30d vs previous 30d (TASK-220)
//   /volume          — top markets by volume: total + 24h with HighVolume badge (TASK-221)
//   /alerts          — NWS active alerts for US cities from cache (TASK-227)
//   /signals         — per-signal win rate + Brier + 7d trend (TASK-228)
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

	"github.com/devher0/polymarket-weather-bot/internal/aggregation"
	"github.com/devher0/polymarket-weather-bot/internal/calibration"
	"github.com/devher0/polymarket-weather-bot/internal/collectors"
	"github.com/devher0/polymarket-weather-bot/internal/markets"
	"github.com/devher0/polymarket-weather-bot/internal/metrics"
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
				case "/compare":
					sendReply(cfg, chatID, handleCompare(bcfg))
				case "/trend":
					arg := ""
					parts := strings.SplitN(text, " ", 2)
					if len(parts) == 2 {
						arg = strings.TrimSpace(parts[1])
					}
					sendReply(cfg, chatID, handleTrend(arg, bcfg))
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
				case "/watchdog":
					sendReply(cfg, chatID, handleWatchdog(bcfg))
				case "/countdown":
					sendReply(cfg, chatID, handleCountdown(bcfg))
				case "/sources":
					sendReply(cfg, chatID, handleSources(bcfg))
				case "/breakdown":
					sendReply(cfg, chatID, handleBreakdown(bcfg))
				case "/missed":
					sendReply(cfg, chatID, handleMissed(bcfg))
				case "/sharpe":
					sendReply(cfg, chatID, handleSharpe(bcfg))
				case "/timing":
					sendReply(cfg, chatID, handleTiming(bcfg))
				case "/drawdown":
					sendReply(cfg, chatID, handleDrawdown(bcfg))
				case "/streak":
					sendReply(cfg, chatID, handleStreak(bcfg))
				case "/weekly":
					sendReply(cfg, chatID, handleWeekly(bcfg))
				case "/roi":
					sendReply(cfg, chatID, handleROI(bcfg))
				case "/compare-signals":
					sendReply(cfg, chatID, handleCompareSignals(bcfg))
				case "/volume":
					sendReply(cfg, chatID, handleVolume(bcfg))
				case "/uncertainty":
					sendReply(cfg, chatID, handleUncertainty(bcfg))
				case "/alerts":
					sendReply(cfg, chatID, handleAlerts(bcfg))
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

	// TASK-198: Brier history sparkline (30-day trend).
	brierHistStr := ""
	if snaps, err := calibration.LoadBrierSnapshots(bcfg.DataRoot); err == nil && len(snaps) >= 5 {
		spark := calibration.BrierSparkline(snaps, 30)
		trend := calibration.BrierTrendLabel(snaps, 30)
		trendEmoji := "→"
		switch trend {
		case "improving":
			trendEmoji = "↑"
		case "worsening":
			trendEmoji = "↓"
		}
		if spark != "" {
			brierHistStr = fmt.Sprintf("\nBrier 30d: <code>%s</code> %s", spark, trendEmoji)
		}
	}

	return fmt.Sprintf(
		"📊 <b>Bot Status</b>\n"+
			"State: %s\n"+
			"Brier score: <code>%s</code>\n"+
			"Sharpe (30d): <code>%s</code>\n"+
			"Streak: <code>%s</code>\n"+
			"Open positions: <b>%d</b>\n"+
			"Today P&amp;L: <b>%+.2f USDC</b>%s%s%s%s",
		pauseState, brierStr, sharpeStr, streakStr, open, pnlToday, sparkStr, plattStr, driftStr, brierHistStr,
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
// Shows win rate, Brier score, estimated P&L, and 7-day trend (TASK-228).
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
	sb.WriteString(fmt.Sprintf("%-8s %4s  %-10s %6s %8s  %s\n", "Signal", "N", "Win% CI", "Brier", "P&L", "7dΔ"))
	sb.WriteString(strings.Repeat("─", 54) + "\n")

	for _, row := range rows {
		name := row.name
		s := row.s
		if len(name) > 8 {
			name = name[:7] + "…"
		}

		if s.Count < minSamples {
			sb.WriteString(fmt.Sprintf("%-8s %4d    —      —        —     N/A\n", name, s.Count))
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

		badge := calibration.SignificanceBadge(s.Wins, s.Count)
		ciStr := calibration.WinRateWithCI(s.Wins, s.Count)

		pnl := pnlBySignal[row.name]
		pnlSign := "+"
		if pnl < 0 {
			pnlSign = ""
		}

		delta, trendOK := calibration.SignalTrend7d(records, row.name, 3)
		trendStr := calibration.FormatTrend(delta, trendOK)

		sb.WriteString(fmt.Sprintf("%s %-7s %4d  %-10s %6.4f %s%.2f %s  %s\n",
			emoji, name, s.Count, ciStr, s.BrierAvg(), pnlSign, pnl, badge, trendStr))
	}

	sb.WriteString("</pre>")
	sb.WriteString("\n🟢≥55% 🟡45-55% 🔴&lt;45% (min 3 bets)\n⚡sig. above 50% ❓CI crosses 50%\n7dΔ: win rate change vs prev 7 days")
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
	sb.WriteString(fmt.Sprintf("%-14s %4s  %-10s %8s %6s\n",
		"City", "N", "Win% CI", "PnL", "ROI%"))
	sb.WriteString(repeatDash(48) + "\n")

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
		ciStr := calibration.WinRateWithCI(s.Wins, s.Bets)
		badge := calibration.SignificanceBadge(s.Wins, s.Bets)
		sb.WriteString(fmt.Sprintf("%-14s %4d  %-10s %s%.2f %+5.1f%% %s\n",
			s.City, s.Bets, ciStr, sign, s.PnLUSDC, roi, badge))
		totalBets += s.Bets
		totalWins += s.Wins
		totalPnL += s.PnLUSDC
		totalRisked += s.TotalRisked
	}

	sb.WriteString(repeatDash(48) + "\n")
	overallROI := 0.0
	if totalRisked > 0 {
		overallROI = totalPnL / totalRisked * 100
	}
	sign := "+"
	if totalPnL < 0 {
		sign = ""
	}
	totalCIStr := calibration.WinRateWithCI(totalWins, totalBets)
	sb.WriteString(fmt.Sprintf("%-14s %4d  %-10s %s%.2f %+5.1f%%\n",
		"TOTAL", totalBets, totalCIStr, sign, totalPnL, overallROI))
	sb.WriteString("</pre>")
	sb.WriteString(fmt.Sprintf("\n⚡sig.>50%% ❓CI crosses 50%% — <i>%s UTC</i>",
		time.Now().UTC().Format("2006-01-02 15:04")))
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
/breakdown       City×signal matrix sorted by ROI%
/ev              EV capture ratio (last 50 bets)
/missed          Today's skipped markets sorted by edge
/daily           Today's bets timeline + running P&L
/sharpe          30-day + 7-day Sharpe ratio with trend
/timing          Best/worst UTC hours to bet (win rate)
/drawdown        Current drawdown from bankroll peak
/streak          Current win/loss streak + historical best/worst
/weekly          4-week P&L table with best/worst week
/roi             Cumulative ROI% from start with weekly sparkline
/compare-signals Compare per-signal win rate: last 30d vs prev 30d
/volume          Top markets by traded volume (24h + total)</pre>

<b>🌤 Forecasts &amp; Markets</b>
<pre>/forecast [city] Weather forecast (all cities or one)
/forecast-quality Confidence + age for all city forecasts
/next            Top-3 best bet opportunities right now
/markets         Active weather markets (price/spread/expiry)
/top-edge        Markets ranked by edge×confidence
/countdown       Markets sorted by time-to-close (urgency)
/uncertainty     Per-city source probability spread
/alerts          NWS active alerts for US cities</pre>

<b>📂 Positions &amp; History</b>
<pre>/positions       Open (unresolved) bets
/explain &lt;id&gt;   Strategy audit trail for a conditionID
/export [days]   Send bet history CSV (default: 30 days)</pre>

<b>🩺 System</b>
<pre>/healthcheck     Data sources, calibration, risk state
/source-weights  Dynamic source weights vs baseline
/sources         Live status of all data collectors
/config          Current bot configuration
/watchlist       Manage watchlisted markets
/watchdog        Last cycle time + delay status</pre>

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

// handleForecastQuality returns per-city forecast confidence, cache age, and
// source agreement entropy. (TASK-191, updated TASK-202)
func handleForecastQuality(bcfg BotConfig) string {
	type cityRow struct {
		city       string
		confidence float64
		sources    []string
		age        time.Duration
		found      bool
		ff         *collectors.FusedForecast
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
			ff:         ff,
		})
	}

	var sb strings.Builder
	sb.WriteString("<b>🌤 Forecast Quality</b>\n\n")
	sb.WriteString("<pre>")
	sb.WriteString(fmt.Sprintf("%-14s %5s %5s %4s %-6s\n", "City", "Conf", "Age", "Agr", "Status"))
	sb.WriteString(strings.Repeat("─", 38) + "\n")

	ready := 0
	for _, row := range rows {
		if !row.found {
			sb.WriteString(fmt.Sprintf("%-14s  n/a   n/a  n/a ❌ no cache\n", row.city))
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
		// TASK-202: source agreement from entropy analysis.
		agrStr := "n/a"
		if row.ff != nil && len(row.ff.PerSourceForecasts) >= 2 {
			rep := collectors.ForecastDisagreement(row.ff)
			agrStr = fmt.Sprintf("%.0f%%", rep.Agreement*100)
		}
		sb.WriteString(fmt.Sprintf("%-14s %4.0f%% %5s %4s %s\n",
			row.city, row.confidence*100, ageStr, agrStr, status))
	}
	sb.WriteString(strings.Repeat("─", 38) + "\n")
	sb.WriteString(fmt.Sprintf("Ready: %d/%d cities", ready, len(cities)))
	sb.WriteString("</pre>")
	sb.WriteString(fmt.Sprintf("\n<i>✅ conf≥50%% &amp; age<3h | ⚠️ marginal | ❌ stale/missing | Agr=source agreement | %s UTC</i>",
		time.Now().UTC().Format("15:04")))
	return sb.String()
}

// handleTrend returns 7-day trend for a specific city. (TASK-194)
func handleTrend(city string, bcfg BotConfig) string {
	records, err := calibration.LoadHistory(bcfg.DataRoot)
	if err != nil {
		return "❌ Could not load bet history."
	}

	// Normalize city name (to uppercase for consistency).
	city = strings.ToUpper(city)

	// If no city specified, list available cities.
	if city == "" {
		citySet := make(map[string]bool)
		for _, r := range records {
			if r.City != "" {
				citySet[r.City] = true
			}
		}
		if len(citySet) == 0 {
			return "📭 No cities found in bet history."
		}
		var cities []string
		for c := range citySet {
			cities = append(cities, c)
		}
		sort.Strings(cities)

		var sb strings.Builder
		sb.WriteString("<b>📊 Available Cities</b>\n\n")
		sb.WriteString("Use `/trend CITY_NAME` to see trend. Examples:\n")
		sb.WriteString("<pre>")
		for _, c := range cities {
			sb.WriteString(c + "\n")
		}
		sb.WriteString("</pre>")
		return sb.String()
	}

	// Collect 7-day stats for the city.
	now := time.Now().UTC()
	startDate := now.AddDate(0, 0, -6)

	type dayStats struct {
		date     string
		count    int
		wins     int
		pnl      float64
		totalEdge float64
		totalSize float64
	}

	dayMap := make(map[string]*dayStats)
	for i := 0; i < 7; i++ {
		d := startDate.AddDate(0, 0, i)
		dateStr := d.Format("2006-01-02")
		dayMap[dateStr] = &dayStats{date: dateStr}
	}

	// Populate day stats.
	for _, r := range records {
		if !strings.EqualFold(r.City, city) {
			continue
		}
		dateStr := r.Timestamp.UTC().Format("2006-01-02")
		if _, ok := dayMap[dateStr]; !ok {
			continue
		}
		dayMap[dateStr].count++
		dayMap[dateStr].totalSize += r.SizeUSDC
		dayMap[dateStr].totalEdge += (r.OurProbability - r.MarketPrice) * r.SizeUSDC

		if r.Outcome != nil {
			if *r.Outcome {
				dayMap[dateStr].wins++
				dayMap[dateStr].pnl += r.SizeUSDC/r.MarketPrice - r.SizeUSDC
			} else {
				dayMap[dateStr].pnl -= r.SizeUSDC
			}
		}
	}

	// Organize into sorted order.
	var days []dayStats
	for _, s := range dayMap {
		days = append(days, *s)
	}
	sort.Slice(days, func(i, j int) bool {
		return days[i].date < days[j].date
	})

	// Build output table.
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("<b>📈 %s — 7-Day Trend</b>\n\n", city))
	sb.WriteString("<pre>")
	sb.WriteString(fmt.Sprintf("%-12s %4s %5s %8s %8s\n", "Date", "Bets", "W/L%", "Avg Edge", "P&L"))
	sb.WriteString(strings.Repeat("─", 45) + "\n")

	bestDay := ""
	bestPnL := math.Inf(-1)
	worstDay := ""
	worstPnL := math.Inf(1)
	totalBets := 0
	totalPnL := 0.0

	for _, d := range days {
		if d.count == 0 {
			sb.WriteString(fmt.Sprintf("%-12s  —    —        —         —\n", d.date))
			continue
		}
		totalBets += d.count

		winRate := float64(d.wins) / float64(d.count) * 100
		avgEdge := 0.0
		if d.totalSize > 0 {
			avgEdge = d.totalEdge / d.totalSize
		}
		pnlStr := fmt.Sprintf("%+.2f", d.pnl)
		if d.pnl > bestPnL {
			bestDay = d.date
			bestPnL = d.pnl
		}
		if d.pnl < worstPnL {
			worstDay = d.date
			worstPnL = d.pnl
		}
		totalPnL += d.pnl

		sb.WriteString(fmt.Sprintf("%-12s %4d %4.0f%% %+8.4f %8s\n",
			d.date, d.count, winRate, avgEdge, pnlStr))
	}

	sb.WriteString(strings.Repeat("─", 45) + "\n")
	sb.WriteString(fmt.Sprintf("%-12s %4d         %+8.2f USDC\n", "Total", totalBets, totalPnL))
	sb.WriteString("</pre>")

	if bestDay != "" && worstDay != "" {
		trend := "→"
		if bestPnL > 100 {
			trend = "↑"
		} else if worstPnL < -100 {
			trend = "↓"
		}
		sb.WriteString(fmt.Sprintf("\n%s Best: %s | Worst: %s",
			trend, bestDay, worstDay))
	}

	return sb.String()
}

// handleCompare returns comparison of today vs yesterday metrics. (TASK-192)
func handleCompare(bcfg BotConfig) string {
	records, err := calibration.LoadHistory(bcfg.DataRoot)
	if err != nil {
		return "❌ Could not load bet history."
	}

	now := time.Now().UTC()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	yesterday := today.AddDate(0, 0, -1)
	todayStr := today.Format("2006-01-02")
	yesterdayStr := yesterday.Format("2006-01-02")

	type dayStats struct {
		totalBets    int
		resolved     int
		wins         int
		pnl          float64
		totalEdge    float64
		totalSize    float64
	}

	computeDayStats := func(day time.Time, records []calibration.BetRecord) dayStats {
		dayStr := day.Format("2006-01-02")
		s := dayStats{}
		for _, r := range records {
			if r.Timestamp.UTC().Format("2006-01-02") != dayStr {
				continue
			}
			s.totalBets++
			s.totalSize += r.SizeUSDC

			if r.Outcome != nil {
				s.resolved++
				s.totalEdge += (r.OurProbability - r.MarketPrice) * r.SizeUSDC
				if *r.Outcome {
					s.wins++
					s.pnl += r.SizeUSDC/r.MarketPrice - r.SizeUSDC
				} else {
					s.pnl -= r.SizeUSDC
				}
			}
		}
		return s
	}

	todaySt := computeDayStats(today, records)
	yesterdaySt := computeDayStats(yesterday, records)

	// Compute metrics
	todayWR := 0.0
	if todaySt.resolved > 0 {
		todayWR = float64(todaySt.wins) / float64(todaySt.resolved) * 100
	}
	yesterdayWR := 0.0
	if yesterdaySt.resolved > 0 {
		yesterdayWR = float64(yesterdaySt.wins) / float64(yesterdaySt.resolved) * 100
	}

	todayAvgEdge := 0.0
	if todaySt.totalBets > 0 {
		todayAvgEdge = todaySt.totalEdge / todaySt.totalSize
	}
	yesterdayAvgEdge := 0.0
	if yesterdaySt.totalBets > 0 {
		yesterdayAvgEdge = yesterdaySt.totalEdge / yesterdaySt.totalSize
	}

	todayROI := 0.0
	if todaySt.totalSize > 0 {
		todayROI = todaySt.pnl / todaySt.totalSize * 100
	}
	yesterdayROI := 0.0
	if yesterdaySt.totalSize > 0 {
		yesterdayROI = yesterdaySt.pnl / yesterdaySt.totalSize * 100
	}

	// Build comparison table
	var sb strings.Builder
	sb.WriteString("<b>📊 Today vs Yesterday</b>\n\n")
	sb.WriteString("<pre>")
	sb.WriteString(fmt.Sprintf("%-18s %10s %10s %6s\n", "Metric", "Today", "Yesterday", "Δ%"))
	sb.WriteString(strings.Repeat("─", 47) + "\n")

	// Total Bets
	deltaBets := 0.0
	if yesterdaySt.totalBets > 0 {
		deltaBets = float64(todaySt.totalBets-yesterdaySt.totalBets) / float64(yesterdaySt.totalBets) * 100
	}
	sb.WriteString(fmt.Sprintf("%-18s %10d %10d %5.0f%%\n",
		"Total Bets", todaySt.totalBets, yesterdaySt.totalBets, deltaBets))

	// Resolved
	deltaResolved := 0.0
	if yesterdaySt.resolved > 0 {
		deltaResolved = float64(todaySt.resolved-yesterdaySt.resolved) / float64(yesterdaySt.resolved) * 100
	}
	sb.WriteString(fmt.Sprintf("%-18s %10d %10d %5.0f%%\n",
		"Resolved", todaySt.resolved, yesterdaySt.resolved, deltaResolved))

	// Win Rate
	deltaWR := 0.0
	if yesterdayWR > 0 {
		deltaWR = (todayWR - yesterdayWR) / yesterdayWR * 100
	}
	sb.WriteString(fmt.Sprintf("%-18s %9.0f%% %9.0f%% %5.0f%%\n",
		"Win Rate", todayWR, yesterdayWR, deltaWR))

	// Avg Edge
	deltaEdge := 0.0
	if yesterdayAvgEdge > 0 {
		deltaEdge = (todayAvgEdge - yesterdayAvgEdge) / yesterdayAvgEdge * 100
	}
	sb.WriteString(fmt.Sprintf("%-18s %9.4f %9.4f %5.0f%%\n",
		"Avg Edge", todayAvgEdge, yesterdayAvgEdge, deltaEdge))

	// PnL
	deltaPnL := 0.0
	if yesterdaySt.pnl != 0 {
		deltaPnL = (todaySt.pnl - yesterdaySt.pnl) / math.Abs(yesterdaySt.pnl) * 100
	}
	pnlTodayStr := fmt.Sprintf("%+.2f", todaySt.pnl)
	pnlYesterdayStr := fmt.Sprintf("%+.2f", yesterdaySt.pnl)
	sb.WriteString(fmt.Sprintf("%-18s %10s %10s %5.0f%%\n",
		"PnL USDC", pnlTodayStr, pnlYesterdayStr, deltaPnL))

	// ROI
	deltaROI := 0.0
	if yesterdayROI > 0 {
		deltaROI = (todayROI - yesterdayROI) / yesterdayROI * 100
	}
	sb.WriteString(fmt.Sprintf("%-18s %9.1f%% %9.1f%% %5.0f%%\n",
		"ROI", todayROI, yesterdayROI, deltaROI))

	sb.WriteString(strings.Repeat("─", 47) + "\n")

	// Trend summary
	trend := "→"
	trendColor := "🟡"
	if todaySt.pnl > yesterdaySt.pnl {
		trend = "↑"
		trendColor = "🟢"
	} else if todaySt.pnl < yesterdaySt.pnl {
		trend = "↓"
		trendColor = "🔴"
	}
	sb.WriteString(fmt.Sprintf("Trend: %s %s (%s vs %s UTC)\n",
		trendColor, trend, todayStr, yesterdayStr))

	sb.WriteString("</pre>")
	return sb.String()
}

// ── handleWatchdog (TASK-200) ─────────────────────────────────────────────────

func handleWatchdog(bcfg BotConfig) string {
	last := metrics.LastCycleAt()
	if last.IsZero() {
		return "🐕 <b>Watchdog</b>\n\nNo cycle has run yet this session."
	}

	since := time.Since(last).Round(time.Second)
	lastStr := last.UTC().Format("2006-01-02 15:04:05 UTC")

	status := "✅ ok"
	detail := ""
	if bcfg.LoopSec > 0 {
		threshold := time.Duration(bcfg.LoopSec*2) * time.Second
		if since > threshold {
			status = "⚠️ delayed"
			detail = fmt.Sprintf(" (expected ≤%ds)", bcfg.LoopSec*2)
		}
	}

	return fmt.Sprintf(
		"🐕 <b>Watchdog</b>\n\n"+
			"Status:         <b>%s</b>%s\n"+
			"Last cycle:     %s\n"+
			"Time since:     %s\n"+
			"Loop interval:  %ds",
		status, detail, lastStr, since, bcfg.LoopSec,
	)
}

// ── handleCountdown (TASK-201) ────────────────────────────────────────────

// handleCountdown returns active weather markets sorted by time-to-close,
// with urgency badges so the operator can quickly spot markets about to expire.
func handleCountdown(bcfg BotConfig) string {
	mks, err := markets.GetWeatherMarkets()
	if err != nil {
		return fmt.Sprintf("❌ Failed to fetch markets: %v", err)
	}

	now := time.Now().UTC()

	// Filter to markets with known city/signal that haven't expired yet.
	type timedMarket struct {
		m     markets.Market
		hours float64
	}
	var active []timedMarket
	for _, m := range mks {
		if m.City == "" || m.Signal == "" {
			continue
		}
		h := m.HoursUntilExpiry()
		if h < 0 {
			continue // already expired
		}
		active = append(active, timedMarket{m, h})
	}

	if len(active) == 0 {
		return "📭 No active weather markets with known expiry found."
	}

	// Sort by soonest expiry first.
	sort.Slice(active, func(i, j int) bool {
		return active[i].hours < active[j].hours
	})

	// Count markets closing within 6h for the summary line.
	soonCount := 0
	for _, tm := range active {
		if tm.hours <= 6 {
			soonCount++
		}
	}

	const maxShow = 10
	if len(active) > maxShow {
		active = active[:maxShow]
	}

	var sb strings.Builder
	sb.WriteString("⏳ <b>Market Countdown</b>\n")
	sb.WriteString("<pre>")
	sb.WriteString(fmt.Sprintf("%-3s %-12s %-6s %5s %5s  %s\n", "Urg", "City", "Signal", "YES", "NO", "Closes In"))
	sb.WriteString(strings.Repeat("─", 50) + "\n")

	for _, tm := range active {
		m := tm.m
		h := tm.hours

		// Urgency badge.
		urgency := "⚪"
		switch {
		case h < 2:
			urgency = "🔴"
		case h < 6:
			urgency = "🟡"
		case h < 24:
			urgency = "🟢"
		}

		// Human-readable time remaining.
		var closesIn string
		switch {
		case h < 1:
			mins := int(h * 60)
			closesIn = fmt.Sprintf("%dm", mins)
			if mins < 30 {
				closesIn += " ‼️"
			}
		case h < 24:
			closesIn = fmt.Sprintf("%.1fh", h)
		default:
			closesIn = fmt.Sprintf("%.1fd", h/24)
		}

		city := m.City
		if len(city) > 12 {
			city = city[:12]
		}
		sig := m.Signal
		if len(sig) > 6 {
			sig = sig[:6]
		}

		sb.WriteString(fmt.Sprintf("%-3s %-12s %-6s %5.2f %5.2f  %s\n",
			urgency, city, sig, m.YesPrice, m.NoPrice, closesIn))
	}
	sb.WriteString("</pre>")
	sb.WriteString(fmt.Sprintf("Closing soon (≤6h): <b>%d</b> market(s)\n", soonCount))
	sb.WriteString(fmt.Sprintf("<i>%s UTC</i>", now.Format("15:04")))
	return sb.String()
}

// handleSources returns a formatted table of data source health and circuit-breaker state. (TASK-206)
func handleSources(bcfg BotConfig) string {
	health := collectors.LoadSourceHealth(bcfg.DataRoot)

	type row struct {
		name      string
		h         collectors.SourceHealth
		statusStr string
		sortKey   int // lower = show first (problems first)
	}

	now := time.Now().UTC()
	var rows []row
	for name, h := range health {
		var statusStr string
		key := 0
		switch {
		case !h.TripUntil.IsZero() && now.Before(h.TripUntil):
			left := time.Until(h.TripUntil)
			mins := int(left.Minutes()) + 1
			statusStr = fmt.Sprintf("🔴 tripped (%dm left)", mins)
			key = 0
		case h.Status(now) == "down":
			statusStr = "❌ down"
			key = 1
		case h.Status(now) == "degraded":
			statusStr = "⚠️ degraded"
			key = 2
		default:
			statusStr = "✅ ok"
			key = 3
		}
		rows = append(rows, row{name: name, h: h, statusStr: statusStr, sortKey: key})
	}

	sort.Slice(rows, func(i, j int) bool {
		if rows[i].sortKey != rows[j].sortKey {
			return rows[i].sortKey < rows[j].sortKey
		}
		return rows[i].name < rows[j].name
	})

	var sb strings.Builder
	sb.WriteString("<b>📡 Data Source Status</b>\n<pre>")
	sb.WriteString(fmt.Sprintf("%-12s %-22s %-8s %5s %s\n", "Source", "Status", "Last OK", "Up%", "Fails"))
	sb.WriteString(strings.Repeat("─", 62) + "\n")

	healthy := 0
	for _, r := range rows {
		lastOK := "-"
		if !r.h.LastSuccess.IsZero() {
			age := now.Sub(r.h.LastSuccess)
			switch {
			case age < time.Minute:
				lastOK = fmt.Sprintf("%ds", int(age.Seconds()))
			case age < time.Hour:
				lastOK = fmt.Sprintf("%dm", int(age.Minutes()))
			default:
				lastOK = fmt.Sprintf("%.1fh", age.Hours())
			}
		}
		upPct := r.h.UpRatePct()
		sb.WriteString(fmt.Sprintf("%-12s %-22s %-8s %4.0f%% %d\n",
			r.name, r.statusStr, lastOK, upPct, r.h.ConsecFails))
		if r.h.Status(now) == "ok" {
			healthy++
		}
	}
	sb.WriteString("</pre>")

	total := len(rows)
	sb.WriteString(fmt.Sprintf("<b>%d/%d</b> sources healthy", healthy, total))
	if total == 0 {
		sb.WriteString("\n<i>No health data yet — starts accumulating on first cycle.</i>")
	}
	sb.WriteString(fmt.Sprintf("\n<i>%s UTC</i>", now.Format("15:04")))
	return sb.String()
}

// ── TASK-207: /breakdown command ─────────────────────────────────────────────

// handleBreakdown returns a city×signal performance matrix sorted by ROI%.
// Only combinations with ≥2 resolved bets are shown (max 15 rows).
func handleBreakdown(bcfg BotConfig) string {
	records, err := calibration.LoadHistory(bcfg.DataRoot)
	if err != nil {
		return "❌ Could not load bet history."
	}

	type key struct{ city, signal string }
	type stats struct {
		bets        int
		wins        int
		pnl         float64
		totalRisked float64
	}

	m := make(map[key]*stats)
	for _, r := range records {
		if r.Outcome == nil || r.City == "" || r.Signal == "" {
			continue
		}
		k := key{r.City, r.Signal}
		s, ok := m[k]
		if !ok {
			s = &stats{}
			m[k] = s
		}
		s.bets++
		s.totalRisked += r.SizeUSDC
		if *r.Outcome {
			s.wins++
			if r.MarketPrice > 0 {
				s.pnl += r.SizeUSDC/r.MarketPrice - r.SizeUSDC
			}
		} else {
			s.pnl -= r.SizeUSDC
		}
	}

	type row struct {
		city, signal string
		bets, wins   int
		pnl, roi     float64
		totalRisked  float64
	}
	var rows []row
	for k, s := range m {
		if s.bets < 2 {
			continue
		}
		roi := 0.0
		if s.totalRisked > 0 {
			roi = s.pnl / s.totalRisked * 100
		}
		rows = append(rows, row{k.city, k.signal, s.bets, s.wins, s.pnl, roi, s.totalRisked})
	}
	if len(rows) == 0 {
		return "📭 Not enough resolved bets yet (need ≥2 per city+signal combo)."
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].roi > rows[j].roi })
	if len(rows) > 15 {
		rows = rows[:15]
	}

	totalBets := 0
	combos := len(m)
	for _, s := range m {
		if s.bets >= 2 {
			totalBets += s.bets
		}
	}

	var sb strings.Builder
	sb.WriteString("🗺 <b>City×Signal Breakdown</b> (≥2 bets, by ROI%)\n<pre>")
	sb.WriteString(fmt.Sprintf("%-10s %-7s %4s %5s %7s %6s\n", "City", "Signal", "N", "Win%", "P&L", "ROI%"))
	sb.WriteString(strings.Repeat("─", 48) + "\n")
	for _, r := range rows {
		city := r.city
		if len(city) > 9 {
			city = city[:8] + "…"
		}
		wr := 0.0
		if r.bets > 0 {
			wr = float64(r.wins) / float64(r.bets) * 100
		}
		sign := "+"
		if r.pnl < 0 {
			sign = ""
		}
		sb.WriteString(fmt.Sprintf("%-10s %-7s %4d %4.0f%% %s%6.2f %+5.1f%%\n",
			city, r.signal, r.bets, wr, sign, r.pnl, r.roi))
	}
	sb.WriteString("</pre>")
	sb.WriteString(fmt.Sprintf("<i>%d resolved bets across %d combinations</i>\n", totalBets, combos))
	sb.WriteString(fmt.Sprintf("<i>%s UTC</i>", time.Now().UTC().Format("2006-01-02 15:04")))
	return sb.String()
}

// ── TASK-209: /missed command ─────────────────────────────────────────────────

// handleMissed shows today's evaluated markets that were skipped, sorted by
// how close they were to the betting threshold (highest edge first).
func handleMissed(bcfg BotConfig) string {
	today := time.Now().UTC().Format("2006-01-02")
	preds, err := strategy.LoadPredictions(today, bcfg.DataRoot)
	if err != nil || len(preds) == 0 {
		return "📭 No prediction data for today yet."
	}

	var skipped []strategy.PredictionRecord
	for _, p := range preds {
		if strings.HasPrefix(p.Decision, "SKIP:") {
			skipped = append(skipped, p)
		}
	}
	if len(skipped) == 0 {
		return "✅ All evaluated markets were bet on today."
	}

	// Sort by max edge descending — nearest-to-threshold first.
	sort.Slice(skipped, func(i, j int) bool {
		edgeI := math.Max(skipped[i].YesEdge, skipped[i].NoEdge)
		edgeJ := math.Max(skipped[j].YesEdge, skipped[j].NoEdge)
		return edgeI > edgeJ
	})
	if len(skipped) > 10 {
		skipped = skipped[:10]
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("⏭ <b>Missed Markets Today</b> (%d skipped)\n<pre>", len(preds)-len(skipped)))
	sb.WriteString(fmt.Sprintf("%-10s %-6s %-18s %6s %5s\n", "City", "Signal", "Reason", "Edge", "Conf"))
	sb.WriteString(strings.Repeat("─", 52) + "\n")
	for _, p := range skipped {
		city := p.City
		if len(city) > 9 {
			city = city[:9]
		}
		sig := p.Signal
		if len(sig) > 5 {
			sig = sig[:5]
		}
		reason := strings.TrimPrefix(p.Decision, "SKIP:")
		if len(reason) > 17 {
			reason = reason[:16] + "…"
		}
		maxEdge := math.Max(p.YesEdge, p.NoEdge)
		sb.WriteString(fmt.Sprintf("%-10s %-6s %-18s %5.3f %4.2f\n",
			city, sig, reason, maxEdge, p.Confidence))
	}
	sb.WriteString("</pre>")
	sb.WriteString(fmt.Sprintf("<i>%s UTC | sorted by edge</i>", time.Now().UTC().Format("15:04")))
	return sb.String()
}

// ── TASK-210: /sharpe command ─────────────────────────────────────────────────

// handleSharpe shows the rolling 30-day and 7-day Sharpe ratios with a trend indicator.
func handleSharpe(bcfg BotConfig) string {
	sharpe30, count30, err30 := calibration.RollingSharpe(bcfg.DataRoot, 30)
	sharpe7, count7, _ := calibration.RollingSharpe(bcfg.DataRoot, 7)

	if err30 != nil || count30 < 2 {
		return "📊 Not enough data yet — need at least 2 days of returns."
	}

	q30 := calibration.SharpeQuality(sharpe30)

	var trendEmoji, trendLabel string
	if count7 >= 2 {
		if sharpe7 > sharpe30 {
			trendEmoji = "↑"
			trendLabel = "improving"
		} else {
			trendEmoji = "↓"
			trendLabel = "declining"
		}
	}

	var sb strings.Builder
	sb.WriteString("📈 <b>Sharpe Ratio</b>\n<pre>")
	sb.WriteString(fmt.Sprintf("30-day Sharpe : %.3f (%s)\n", sharpe30, q30))
	if count7 >= 2 {
		sb.WriteString(fmt.Sprintf("7-day Sharpe  : %.3f %s %s\n", sharpe7, trendEmoji, trendLabel))
	}
	sb.WriteString(fmt.Sprintf("Data points   : %d days\n", count30))
	sb.WriteString("\nBenchmarks:\n")
	sb.WriteString("  >2.0 excellent  >1.0 good\n")
	sb.WriteString("  >0.5 acceptable ≤0.5 poor\n")
	sb.WriteString("</pre>")
	sb.WriteString(fmt.Sprintf("<i>%s UTC</i>", time.Now().UTC().Format("2006-01-02 15:04")))
	return sb.String()
}

// ── TASK-211: /timing command ─────────────────────────────────────────────────

// handleTiming shows win rate by UTC hour and the best/worst hours to bet.
func handleTiming(bcfg BotConfig) string {
	buckets, err := calibration.LoadHourlyStats(bcfg.DataRoot)
	if err != nil {
		return "❌ Could not load hourly stats."
	}

	const minBets = 3
	totalBets := 0
	for _, b := range buckets {
		totalBets += b.Total()
	}
	if totalBets < minBets {
		return "⏰ Not enough data yet — need 3+ resolved bets."
	}

	// Collect hours with enough data.
	type hourStat struct {
		hour    int
		winRate float64
		total   int
		mult    float64
	}
	var valid []hourStat
	for i, b := range buckets {
		if b.Total() >= minBets {
			valid = append(valid, hourStat{
				hour:    i,
				winRate: b.WinRate(),
				total:   b.Total(),
				mult:    calibration.TimingMultiplier(buckets, i),
			})
		}
	}

	sort.Slice(valid, func(i, j int) bool { return valid[i].winRate > valid[j].winRate })

	nowHour := time.Now().UTC().Hour()
	nowBucket := buckets[nowHour]

	var sb strings.Builder
	sb.WriteString("⏰ <b>Bet Timing Analysis</b>\n<pre>")

	// Best hours.
	top := 3
	if len(valid) < top {
		top = len(valid)
	}
	if top > 0 {
		sb.WriteString("Best hours (UTC):\n")
		for _, h := range valid[:top] {
			sb.WriteString(fmt.Sprintf("  %02d:00  win%% %4.0f%%  n=%d  ×%.2f\n",
				h.hour, h.winRate*100, h.total, h.mult))
		}
	}

	// Worst hours.
	worst := 3
	if len(valid) < worst {
		worst = len(valid)
	}
	if worst > 0 && len(valid) > worst {
		sb.WriteString("Worst hours (UTC):\n")
		for i := len(valid) - worst; i < len(valid); i++ {
			h := valid[i]
			sb.WriteString(fmt.Sprintf("  %02d:00  win%% %4.0f%%  n=%d  ×%.2f\n",
				h.hour, h.winRate*100, h.total, h.mult))
		}
	}

	// Current hour.
	sb.WriteString(fmt.Sprintf("\nNow: %02d UTC", nowHour))
	if nowBucket.Total() >= minBets {
		nowMult := calibration.TimingMultiplier(buckets, nowHour)
		sb.WriteString(fmt.Sprintf(" | win%% %4.0f%% | ×%.2f", nowBucket.WinRate()*100, nowMult))
		if nowMult >= 1.0 {
			sb.WriteString(" ✅")
		} else {
			sb.WriteString(" ⚠️")
		}
	} else {
		sb.WriteString(" | no data yet")
	}
	sb.WriteString("\n</pre>")
	sb.WriteString(fmt.Sprintf("<i>%s UTC</i>", time.Now().UTC().Format("2006-01-02 15:04")))
	return sb.String()
}

// ── TASK-212: /drawdown command ───────────────────────────────────────────────

// handleDrawdown shows current bankroll drawdown from the historical peak.
func handleDrawdown(bcfg BotConfig) string {
	history, err := calibration.LoadBankrollHistory(bcfg.DataRoot)
	if err != nil || len(history) == 0 {
		return "📉 No bankroll history yet."
	}

	current := calibration.LoadBankroll(bcfg.DataRoot)
	if current <= 0 {
		current = bcfg.Bankroll
	}

	// Find peak balance across all snapshots.
	peak := current
	for _, snap := range history {
		if snap.BalanceUSDC > peak {
			peak = snap.BalanceUSDC
		}
	}

	dd := calibration.DrawdownFraction(peak, current)
	mult := calibration.DrawdownMultiplier(dd, 0.20)
	ddPct := dd * 100

	var status string
	switch {
	case dd == 0:
		status = "at peak 🏔"
	case dd < 0.05:
		status = "minimal"
	case dd < 0.10:
		status = "moderate"
	case dd < 0.20:
		status = "significant ⚠️"
	default:
		status = "severe 🚨"
	}

	var sb strings.Builder
	sb.WriteString("📉 <b>Bankroll Drawdown</b>\n<pre>")
	sb.WriteString(fmt.Sprintf("Peak      : $%.2f USDC\n", peak))
	sb.WriteString(fmt.Sprintf("Current   : $%.2f USDC\n", current))
	if dd > 0 {
		sb.WriteString(fmt.Sprintf("Drawdown  : -%.1f%% (%s)\n", ddPct, status))
		sb.WriteString(fmt.Sprintf("Kelly ×   : %.2f (reduced sizing)\n", mult))
		recovery := peak - current
		sb.WriteString(fmt.Sprintf("Recovery  : +$%.2f needed to reach peak\n", recovery))
	} else {
		sb.WriteString(fmt.Sprintf("Drawdown  : 0.0%% (%s)\n", status))
		sb.WriteString("Kelly ×   : 1.00 (no reduction)\n")
	}
	sb.WriteString(fmt.Sprintf("Snapshots : %d days tracked\n", len(history)))
	sb.WriteString("</pre>")
	sb.WriteString(fmt.Sprintf("<i>%s UTC</i>", time.Now().UTC().Format("2006-01-02 15:04")))
	return sb.String()
}

// ── TASK-213: /streak command ─────────────────────────────────────────────────

// handleStreak shows the current win/loss streak and historical best/worst.
func handleStreak(bcfg BotConfig) string {
	records, err := calibration.LoadHistory(bcfg.DataRoot)
	if err != nil {
		return "❌ Could not load bet history."
	}

	// Collect resolved bets sorted oldest-first.
	var resolved []calibration.BetRecord
	for _, r := range records {
		if r.Outcome != nil {
			resolved = append(resolved, r)
		}
	}
	if len(resolved) == 0 {
		return "🎯 <b>Streak</b>\nNo resolved bets yet."
	}
	sort.Slice(resolved, func(i, j int) bool {
		ti := resolved[i].ResolvedAt
		if ti.IsZero() {
			ti = resolved[i].Timestamp
		}
		tj := resolved[j].ResolvedAt
		if tj.IsZero() {
			tj = resolved[j].Timestamp
		}
		return ti.Before(tj)
	})

	// Current streak via calibration helper.
	curN, curKind := calibration.ComputeStreak(resolved)

	// Scan full history for best win streak and worst loss streak.
	bestWin, worstLoss := 0, 0
	run, runIsWin := 1, *resolved[0].Outcome
	for i := 1; i < len(resolved); i++ {
		same := *resolved[i].Outcome == runIsWin
		if same {
			run++
		} else {
			if runIsWin && run > bestWin {
				bestWin = run
			} else if !runIsWin && run > worstLoss {
				worstLoss = run
			}
			run, runIsWin = 1, *resolved[i].Outcome
		}
	}
	// Flush last run.
	if runIsWin && run > bestWin {
		bestWin = run
	} else if !runIsWin && run > worstLoss {
		worstLoss = run
	}

	// Streak Kelly factor.
	sr := calibration.CurrentStreak(resolved)
	kellyFactor := calibration.StreakKellyFactor(sr)

	// Emoji for current streak.
	var streakEmoji string
	switch {
	case curKind == "wins" && curN >= 5:
		streakEmoji = "🔥"
	case curKind == "wins":
		streakEmoji = "✅"
	case curKind == "losses" && curN >= 4:
		streakEmoji = "🚨"
	case curKind == "losses":
		streakEmoji = "⚠️"
	default:
		streakEmoji = "➖"
	}

	var sb strings.Builder
	sb.WriteString("🎯 <b>Win/Loss Streak</b>\n<pre>")

	// Current streak.
	if curKind == "wins" {
		sb.WriteString(fmt.Sprintf("Current : %s +%d %s\n", streakEmoji, curN, curKind))
	} else if curKind == "losses" {
		sb.WriteString(fmt.Sprintf("Current : %s -%d %s\n", streakEmoji, curN, curKind))
	} else {
		sb.WriteString("Current : ➖ no data\n")
	}

	// Historical records.
	if bestWin > 0 {
		sb.WriteString(fmt.Sprintf("Best    : +%d consecutive wins\n", bestWin))
	}
	if worstLoss > 0 {
		sb.WriteString(fmt.Sprintf("Worst   : -%d consecutive losses\n", worstLoss))
	}
	sb.WriteString(fmt.Sprintf("Kelly × : %.2f\n", kellyFactor))
	sb.WriteString(fmt.Sprintf("Total   : %d resolved bets\n", len(resolved)))
	sb.WriteString("</pre>")
	sb.WriteString(fmt.Sprintf("<i>%s UTC</i>", time.Now().UTC().Format("2006-01-02 15:04")))
	return sb.String()
}

// ── TASK-214: /weekly command ─────────────────────────────────────────────────

// handleWeekly shows a 4-week P&L table with best and worst week highlights.
func handleWeekly(bcfg BotConfig) string {
	records, err := calibration.LoadHistory(bcfg.DataRoot)
	if err != nil {
		return "❌ Could not load bet history."
	}

	weeks := calibration.WeeklyBreakdown(records, 4)

	// Count weeks that have any data.
	weeksWithData := 0
	for _, w := range weeks {
		if w.Bets > 0 {
			weeksWithData++
		}
	}
	if weeksWithData < 2 {
		return "📅 <b>Weekly P&L</b>\nNot enough history yet (need 2+ weeks of data)."
	}

	best := calibration.BestWeek(weeks)
	worst := calibration.WorstWeek(weeks)

	currentWeekStart := weeks[len(weeks)-1].WeekStart
	now := time.Now().UTC()

	var sb strings.Builder
	sb.WriteString("📅 <b>Weekly P&L</b>\n<pre>")
	sb.WriteString(fmt.Sprintf("%-12s %5s %6s %8s\n", "Week", "Bets", "Win%", "P&L"))
	sb.WriteString(strings.Repeat("-", 35) + "\n")

	for _, w := range weeks {
		label := w.WeekStart.Format("Jan 02")
		marker := ""
		if w.WeekStart.Equal(currentWeekStart) && now.Weekday() != time.Sunday {
			marker = "*"
		}
		winPct := "-  "
		if w.Bets > 0 {
			winPct = fmt.Sprintf("%.0f%%", w.WinPct())
		}
		pnlStr := fmt.Sprintf("%+.2f", w.PnLUSDC)
		sb.WriteString(fmt.Sprintf("%-11s%s %5d %6s %8s\n",
			label, marker, w.Bets, winPct, pnlStr))
	}

	sb.WriteString(strings.Repeat("-", 35) + "\n")
	if best.Bets > 0 {
		sb.WriteString(fmt.Sprintf("Best  : %s  %+.2f USDC\n",
			best.WeekStart.Format("Jan 02"), best.PnLUSDC))
	}
	if worst.Bets > 0 {
		sb.WriteString(fmt.Sprintf("Worst : %s  %+.2f USDC\n",
			worst.WeekStart.Format("Jan 02"), worst.PnLUSDC))
	}
	sb.WriteString("</pre>")
	sb.WriteString("<i>* = current week (partial)</i>\n")
	sb.WriteString(fmt.Sprintf("<i>%s UTC</i>", now.UTC().Format("2006-01-02 15:04")))
	return sb.String()
}

// ── TASK-215: /roi command ────────────────────────────────────────────────────

// handleROI shows cumulative ROI% from the starting bankroll with a weekly sparkline.
func handleROI(bcfg BotConfig) string {
	records, err := calibration.LoadHistory(bcfg.DataRoot)
	if err != nil {
		return "❌ Could not load bet history."
	}

	initial := calibration.DefaultBankroll
	current := calibration.LoadBankroll(bcfg.DataRoot)
	if current <= 0 {
		current = bcfg.Bankroll
	}

	roiPct := (current - initial) / initial * 100
	var roiSign string
	if roiPct >= 0 {
		roiSign = "+"
	}

	// 8-week breakdown for sparkline.
	weeks := calibration.WeeklyBreakdown(records, 8)

	// Build cumulative P&L series.
	var cumValues []float64
	cum := 0.0
	for _, w := range weeks {
		cum += w.PnLUSDC
		cumValues = append(cumValues, cum)
	}

	spark := ""
	if len(cumValues) > 1 {
		spark = asciiSparkline(cumValues)
	}

	// Count resolved bets.
	totalBets := 0
	for _, r := range records {
		if r.Outcome != nil {
			totalBets++
		}
	}

	var sb strings.Builder
	sb.WriteString("📈 <b>Cumulative ROI</b>\n<pre>")
	sb.WriteString(fmt.Sprintf("Initial   : $%.2f USDC\n", initial))
	sb.WriteString(fmt.Sprintf("Current   : $%.2f USDC\n", current))
	sb.WriteString(fmt.Sprintf("ROI       : %s%.1f%%\n", roiSign, roiPct))
	sb.WriteString(fmt.Sprintf("Net P&L   : %+.2f USDC\n", current-initial))
	sb.WriteString(fmt.Sprintf("Resolved  : %d bets\n", totalBets))
	if spark != "" {
		sb.WriteString(fmt.Sprintf("\n8-wk P&L  : %s\n", spark))
	}
	sb.WriteString("</pre>")
	sb.WriteString(fmt.Sprintf("<i>%s UTC</i>", time.Now().UTC().Format("2006-01-02 15:04")))
	return sb.String()
}

// handleCompareSignals compares per-signal win rate for the last 30 days vs
// the previous 30 days, showing trend arrows and Δ. (TASK-220)
func handleCompareSignals(bcfg BotConfig) string {
	records, err := calibration.LoadHistory(bcfg.DataRoot)
	if err != nil {
		return "❌ Could not load bet history."
	}

	recent := calibration.SignalBreakdownForPeriod(records, 30)
	prev := calibration.SignalBreakdownForPeriod(records, 60)

	// prev[sig] contains bets from last 60 days; subtract recent to get days 31-60.
	prevOnly := make(map[string]calibration.BreakdownStats)
	for sig, p := range prev {
		r := recent[sig]
		prevOnly[sig] = calibration.BreakdownStats{
			Count:    p.Count - r.Count,
			BrierSum: p.BrierSum - r.BrierSum,
			Wins:     p.Wins - r.Wins,
		}
	}

	// Collect all known signals.
	sigSet := make(map[string]struct{})
	for s := range recent {
		sigSet[s] = struct{}{}
	}
	for s := range prevOnly {
		sigSet[s] = struct{}{}
	}
	if len(sigSet) == 0 {
		return "📈 No resolved bets yet — signal comparison unavailable."
	}

	type row struct {
		sig   string
		recentWR float64
		prevWR   float64
		delta    float64
		recentN  int
		prevN    int
	}
	rows := make([]row, 0, len(sigSet))
	for sig := range sigSet {
		r := recent[sig]
		p := prevOnly[sig]

		rWR := -1.0
		if r.Count >= 3 {
			rWR = r.WinRate()
		}
		pWR := -1.0
		if p.Count >= 3 {
			pWR = p.WinRate()
		}
		delta := 0.0
		if rWR >= 0 && pWR >= 0 {
			delta = rWR - pWR
		}
		rows = append(rows, row{sig, rWR, pWR, delta, r.Count, p.Count})
	}
	// Sort by recent win rate desc (signals with data first).
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].recentWR < 0 && rows[j].recentWR >= 0 {
			return false
		}
		if rows[j].recentWR < 0 && rows[i].recentWR >= 0 {
			return true
		}
		return rows[i].recentWR > rows[j].recentWR
	})

	var sb strings.Builder
	sb.WriteString("📊 <b>Signal Comparison — Recent vs Previous 30d</b>\n")
	sb.WriteString("<pre>")
	sb.WriteString(fmt.Sprintf("%-8s %7s %7s %5s %5s\n", "Signal", "Rcnt%", "Prev%", "Δ", "Trend"))
	sb.WriteString(strings.Repeat("─", 38) + "\n")

	for _, r := range rows {
		name := r.sig
		if len(name) > 8 {
			name = name[:7] + "…"
		}

		rcntStr := "N/A"
		if r.recentWR >= 0 {
			rcntStr = fmt.Sprintf("%.0f%%(%d)", r.recentWR, r.recentN)
		}
		prevStr := "N/A"
		if r.prevWR >= 0 {
			prevStr = fmt.Sprintf("%.0f%%(%d)", r.prevWR, r.prevN)
		}

		trend := "➡️"
		deltaStr := "n/a"
		if r.recentWR >= 0 && r.prevWR >= 0 {
			deltaStr = fmt.Sprintf("%+.0f%%", r.delta)
			if r.delta > 5 {
				trend = "📈"
			} else if r.delta < -5 {
				trend = "📉"
			}
		}

		sb.WriteString(fmt.Sprintf("%-8s %7s %7s %5s %s\n",
			name, rcntStr, prevStr, deltaStr, trend))
	}

	sb.WriteString(strings.Repeat("─", 38) + "\n")
	sb.WriteString("(N/A = fewer than 3 resolved bets)\n")
	sb.WriteString("</pre>")
	sb.WriteString(fmt.Sprintf("<i>%s UTC</i>", time.Now().UTC().Format("2006-01-02 15:04")))
	return sb.String()
}

// handleVolume returns top active weather markets sorted by traded volume.
// Shows total volume, HighVolume badge, and today's snapshot data. (TASK-221)
func handleVolume(bcfg BotConfig) string {
	// Load today's cached snapshots first; supplement with live markets if empty.
	snaps, _ := markets.LoadTodayVolumeSnapshots(bcfg.DataRoot)

	if len(snaps) == 0 {
		// Attempt live fetch.
		mks, err := markets.GetWeatherMarkets()
		if err != nil {
			return "❌ Could not fetch market data: " + err.Error()
		}
		if len(mks) == 0 {
			return "📭 No active weather markets found."
		}
		snaps = markets.SnapshotsFromMarkets(mks)
		// Persist for future calls.
		_ = markets.SaveVolumeSnapshots(snaps, bcfg.DataRoot)
	}

	if len(snaps) == 0 {
		return "📭 No volume data available."
	}

	// Sort by TotalVolume desc.
	sorted := make([]markets.VolumeSnapshot, len(snaps))
	copy(sorted, snaps)
	for i := 0; i < len(sorted)-1; i++ {
		for j := i + 1; j < len(sorted); j++ {
			if sorted[j].TotalVolume > sorted[i].TotalVolume {
				sorted[i], sorted[j] = sorted[j], sorted[i]
			}
		}
	}

	top := sorted
	if len(top) > 5 {
		top = top[:5]
	}

	var sb strings.Builder
	sb.WriteString("💰 <b>Top Markets by Volume</b>\n")
	sb.WriteString("<pre>")
	sb.WriteString(fmt.Sprintf("%-32s %10s  %s\n", "Market", "Vol USDC", ""))
	sb.WriteString(strings.Repeat("─", 50) + "\n")

	for _, s := range top {
		q := s.Question
		if len(q) > 30 {
			q = q[:29] + "…"
		}
		badge := ""
		if s.TotalVolume >= 10_000 {
			badge = "🔥"
		}
		volStr := formatVolume(s.TotalVolume)
		sb.WriteString(fmt.Sprintf("%-32s %10s %s\n", q, volStr, badge))
	}

	sb.WriteString(strings.Repeat("─", 50) + "\n")
	sb.WriteString(fmt.Sprintf("Total markets tracked: %d\n", len(snaps)))
	sb.WriteString("</pre>")
	sb.WriteString(fmt.Sprintf("<i>%s UTC</i>", time.Now().UTC().Format("2006-01-02 15:04")))
	return sb.String()
}

// formatVolume renders a USDC volume as a compact string (e.g. "12.3k", "1.5M").
func formatVolume(v float64) string {
	switch {
	case v >= 1_000_000:
		return fmt.Sprintf("%.1fM", v/1_000_000)
	case v >= 1_000:
		return fmt.Sprintf("%.1fk", v/1_000)
	default:
		return fmt.Sprintf("%.0f", v)
	}
}

// handleUncertainty — TASK-226: per-city source probability spread from cache.
// Shows how much the weather data sources disagree on rain probability for each
// city. Wide spread → low confidence → bets skipped by the spread gate.
func handleUncertainty(bcfg BotConfig) string {
	cities := []string{
		"new_york", "london", "tokyo", "miami", "paris",
		"chicago", "los_angeles", "san_francisco", "berlin",
	}

	type row struct {
		city    string
		spread  aggregation.SourceSpread
		sources int
		ok      bool
	}

	rows := make([]row, 0, len(cities))
	for _, city := range cities {
		ff, ok := collectors.LoadForecastCache(city, 0, bcfg.DataRoot, 0)
		if !ok || ff == nil || len(ff.PerSourceForecasts) < 2 {
			rows = append(rows, row{city: city})
			continue
		}
		perProbs := strategy.ComputePerSourceProbsRain(ff.PerSourceForecasts)
		if len(perProbs) < 2 {
			rows = append(rows, row{city: city})
			continue
		}
		probs := make([]float64, 0, len(perProbs))
		for _, p := range perProbs {
			probs = append(probs, p)
		}
		sg := aggregation.ComputeSpread(probs)
		rows = append(rows, row{city: city, spread: sg, sources: len(probs), ok: true})
	}

	var sb strings.Builder
	sb.WriteString("<b>📊 Source Probability Spread (rain signal)</b>\n")
	sb.WriteString("<pre>")
	sb.WriteString(fmt.Sprintf("%-14s %4s %4s %6s %8s\n", "City", "Min", "Max", "Spread", "Label"))
	sb.WriteString(strings.Repeat("─", 42) + "\n")

	for _, r := range rows {
		if !r.ok {
			sb.WriteString(fmt.Sprintf("%-14s  — no cached forecast\n", r.city))
			continue
		}
		gateFlag := ""
		if r.spread.Exceeds(strategy.MaxSourceSpread) {
			gateFlag = " ⛔"
		}
		sb.WriteString(fmt.Sprintf("%-14s %4.0f%% %4.0f%% %5.0fpp %-8s%s\n",
			r.city,
			r.spread.Min*100, r.spread.Max*100,
			r.spread.Spread*100,
			r.spread.Label(),
			gateFlag,
		))
	}
	sb.WriteString("</pre>")
	sb.WriteString(fmt.Sprintf(
		"<i>⛔ = spread > %.0fpp gate threshold | %s UTC</i>",
		strategy.MaxSourceSpread*100,
		time.Now().UTC().Format("15:04"),
	))
	return sb.String()
}

// handleAlerts — TASK-227: NWS active alerts for US cities from forecast cache.
// Iterates US cities, shows AlertLevel + AlertEvents from FusedForecast,
// sorted by AlertLevel descending so highest-severity alerts appear first.
func handleAlerts(bcfg BotConfig) string {
	usCities := []string{"new_york", "miami", "chicago", "los_angeles"}

	type alertRow struct {
		city       string
		alertLevel int
		events     []string
	}

	rows := make([]alertRow, 0, len(usCities))
	for _, city := range usCities {
		ff, ok := collectors.LoadForecastCache(city, 0, bcfg.DataRoot, 0)
		if !ok || ff == nil {
			rows = append(rows, alertRow{city: city, alertLevel: -1})
			continue
		}
		rows = append(rows, alertRow{
			city:       city,
			alertLevel: ff.AlertLevel,
			events:     ff.AlertEvents,
		})
	}

	// Sort by AlertLevel descending (highest severity first); missing data last.
	for i := 0; i < len(rows); i++ {
		for j := i + 1; j < len(rows); j++ {
			if rows[j].alertLevel > rows[i].alertLevel {
				rows[i], rows[j] = rows[j], rows[i]
			}
		}
	}

	cityLabel := map[string]string{
		"new_york":    "NYC",
		"miami":       "Miami",
		"chicago":     "Chicago",
		"los_angeles": "LA",
	}

	alertEmoji := func(level int) string {
		switch level {
		case collectors.AlertLevelWarning:
			return "🔴"
		case collectors.AlertLevelWatch:
			return "🟠"
		case collectors.AlertLevelAdvisory:
			return "🟡"
		default:
			return "🟢"
		}
	}

	var sb strings.Builder
	sb.WriteString("<b>⚠️ NWS Active Alerts — US Cities</b>\n\n")

	anyAlerts := false
	for _, r := range rows {
		label := cityLabel[r.city]
		if label == "" {
			label = r.city
		}
		if r.alertLevel < 0 {
			sb.WriteString(fmt.Sprintf("❓ %s: no cached data\n", label))
			continue
		}
		if r.alertLevel == collectors.AlertLevelNone || len(r.events) == 0 {
			sb.WriteString(fmt.Sprintf("🟢 %s: no alerts\n", label))
			continue
		}
		anyAlerts = true
		emoji := alertEmoji(r.alertLevel)
		sb.WriteString(fmt.Sprintf("%s %s: %s\n", emoji, label, strings.Join(r.events, ", ")))
	}

	if !anyAlerts {
		sb.WriteString("\n<i>All clear — no active NWS warnings.</i>")
	}
	sb.WriteString(fmt.Sprintf("\n<i>%s UTC</i>", time.Now().UTC().Format("15:04")))
	return sb.String()
}
