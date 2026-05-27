// telegram_commands.go — TASK-111: Telegram bot command polling.
//
// Supported commands:
//   /status   — Brier score, open positions, P&L for today
//   /positions — list of open (unresolved) bets
//   /next     — top-3 best bets right now (dry-run, from cached forecasts)
//   /pause    — suspend all trading
//   /resume   — resume trading
//
// Uses long-poll (getUpdates with timeout=60) — no webhook required.
// StartCommandPoller runs in a background goroutine; call it after bot setup.
package notifier

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/devher0/polymarket-weather-bot/internal/calibration"
	"github.com/devher0/polymarket-weather-bot/internal/collectors"
	"github.com/devher0/polymarket-weather-bot/internal/markets"
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
				case "/pause":
					paused.Store(1)
					sendReply(cfg, chatID, "⏸ Trading <b>paused</b>. Send /resume to restart.")
					slog.Info("telegram: trading paused via /pause command")
				case "/resume":
					paused.Store(0)
					sendReply(cfg, chatID, "▶️ Trading <b>resumed</b>.")
					slog.Info("telegram: trading resumed via /resume command")
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

	sharpeStr := "n/a (need ≥2 days)"
	if sh, cnt, err := calibration.RollingSharpe(bcfg.DataRoot, 30); err == nil && cnt >= 2 {
		sharpeStr = fmt.Sprintf("%.3f [%s, %d days]", sh, calibration.SharpeQuality(sh), cnt)
	}

	return fmt.Sprintf(
		"📊 <b>Bot Status</b>\n"+
			"State: %s\n"+
			"Brier score: <code>%s</code>\n"+
			"Sharpe (30d): <code>%s</code>\n"+
			"Open positions: <b>%d</b>\n"+
			"Today P&amp;L: <b>%+.2f USDC</b>",
		pauseState, brierStr, sharpeStr, open, pnlToday,
	)
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
