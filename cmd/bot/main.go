// Polymarket Weather Bot
// Usage:
//   go run ./cmd/bot                         — dry run (no real orders)
//   go run ./cmd/bot --live                  — real money mode
//   go run ./cmd/bot --loop 3600             — repeat every N seconds
//   go run ./cmd/bot --collect-history       — download 90-day historical data
//   go run ./cmd/bot --config path/to/config.yaml  — use a specific config file
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"os"
	"os/signal"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/joho/godotenv"

	"github.com/devher0/polymarket-weather-bot/config"
	"github.com/devher0/polymarket-weather-bot/internal/calibration"
	"github.com/devher0/polymarket-weather-bot/internal/collectors"
	"github.com/devher0/polymarket-weather-bot/internal/markets"
	"github.com/devher0/polymarket-weather-bot/internal/metrics"
	"github.com/devher0/polymarket-weather-bot/internal/notifier"
	"github.com/devher0/polymarket-weather-bot/internal/polymarket"
	"github.com/devher0/polymarket-weather-bot/internal/ratelimit"
	"github.com/devher0/polymarket-weather-bot/internal/risk"
	"github.com/devher0/polymarket-weather-bot/internal/strategy"
	"github.com/devher0/polymarket-weather-bot/internal/weather"
)

// notifiedOpportunities deduplicates per-session opportunity alerts so each
// market is only alerted once (TASK-165).
var notifiedOpportunities sync.Map

// sessionStats tracks cumulative statistics for the current process run.
type sessionStats struct {
	startTime    time.Time
	cycleCount   atomic.Int64
	marketsFound atomic.Int64
	betsPlaced   atomic.Int64
	dryRunPnL    atomic.Int64 // cents (integer to avoid atomic float issues)
}

func (s *sessionStats) summary(dryRun bool) string {
	dur := time.Since(s.startTime).Round(time.Second)
	pnl := float64(s.dryRunPnL.Load()) / 100.0
	lines := []string{
		fmt.Sprintf("Uptime:        %s", dur),
		fmt.Sprintf("Cycles:        %d", s.cycleCount.Load()),
		fmt.Sprintf("Markets seen:  %d", s.marketsFound.Load()),
		fmt.Sprintf("Bets placed:   %d", s.betsPlaced.Load()),
	}
	if dryRun {
		lines = append(lines, fmt.Sprintf("Dry-run P&L:   %+.2f USDC (estimated)", pnl))
	}
	return strings.Join(lines, "\n")
}

// dryRunRecord is one market decision written to the --dry-run-file JSON output.
type dryRunRecord struct {
	Market string  `json:"market"`
	Side   string  `json:"side"`
	OurP   float64 `json:"ourP"`
	Edge   float64 `json:"edge"`
	Size   float64 `json:"size"`
	Reason string  `json:"reason"`
}

// dryRunOutput is the full JSON file written after each cycle.
type dryRunOutput struct {
	Timestamp          string         `json:"timestamp"`
	Cycle              int64          `json:"cycle"`
	MarketsEvaluated   int            `json:"markets_evaluated"`
	BetsRecommended    int            `json:"bets_recommended"`
	Decisions          []dryRunRecord `json:"decisions"`
}

// profitAlertsFile is the JSON file that tracks which condition IDs have
// already received a profit-opportunity Telegram alert (TASK-061).
const profitAlertsFile = "data/profit_alerts.json"

// loadProfitAlerts reads the set of condition IDs that were already alerted.
// Returns an empty map on any error or when the file doesn't exist.
func loadProfitAlerts(dataRoot string) map[string]bool {
	path := profitAlertsFile
	if dataRoot != "" && dataRoot != "." {
		path = dataRoot + "/" + profitAlertsFile
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return map[string]bool{}
	}
	var alerted []string
	if err := json.Unmarshal(data, &alerted); err != nil {
		return map[string]bool{}
	}
	m := make(map[string]bool, len(alerted))
	for _, id := range alerted {
		m[id] = true
	}
	return m
}

// saveProfitAlerts persists the set of alerted condition IDs to disk.
func saveProfitAlerts(dataRoot string, alerted map[string]bool) {
	path := profitAlertsFile
	if dataRoot != "" && dataRoot != "." {
		path = dataRoot + "/" + profitAlertsFile
	}
	ids := make([]string, 0, len(alerted))
	for id := range alerted {
		ids = append(ids, id)
	}
	data, err := json.Marshal(ids)
	if err != nil {
		slog.Warn("profit_alerts: marshal failed", "err", err)
		return
	}
	if err := os.MkdirAll("data", 0o755); err == nil {
		_ = os.WriteFile(path, data, 0o644)
	}
}

// checkProfitAlerts scans open positions for profitable exits and sends
// Telegram notifications for newly profitable ones (TASK-061).
//
// A position is flagged when the current price for our side is ≥0.25 higher
// than our entry price (market_price field in BetRecord).
// Each condition ID is alerted at most once (tracked in data/profit_alerts.json).
func checkProfitAlerts(
	history []calibration.BetRecord,
	mktPrices map[string]markets.Market, // conditionID → market with current prices
	dataRoot string,
) {
	alerted := loadProfitAlerts(dataRoot)
	const profitThreshold = 0.25

	changed := false
	for _, rec := range history {
		// Only open (unresolved) positions.
		if rec.Outcome != nil {
			continue
		}
		// Already alerted for this position.
		if alerted[rec.ConditionID] {
			continue
		}
		m, ok := mktPrices[rec.ConditionID]
		if !ok {
			continue
		}
		// Determine current price for our side.
		var currentPrice float64
		switch rec.Side {
		case "YES":
			currentPrice = m.YesPrice
		case "NO":
			currentPrice = m.NoPrice
		default:
			continue
		}
		// Entry price is the market price recorded at bet time.
		entry := rec.MarketPrice
		if entry <= 0 {
			continue
		}
		gain := currentPrice - entry
		if gain >= profitThreshold {
			slog.Info("profit opportunity detected",
				"conditionID", rec.ConditionID,
				"side", rec.Side,
				"entry", fmt.Sprintf("%.2f", entry),
				"current", fmt.Sprintf("%.2f", currentPrice),
				"gain", fmt.Sprintf("%.2f", gain),
			)
			if err := notifier.NotifyProfitOpportunity(rec.ConditionID, rec.Side, entry, currentPrice); err != nil {
				slog.Warn("profit alert send failed", "conditionID", rec.ConditionID, "err", err)
			}
			alerted[rec.ConditionID] = true
			changed = true
		}
	}

	if changed {
		saveProfitAlerts(dataRoot, alerted)
	}
}

func main() {
	live            := flag.Bool("live", false, "Disable dry-run (real money)")
	loopFlag        := flag.Int("loop", 0, "Repeat interval in seconds (0 = run once; overrides config)")
	collectHistory  := flag.Bool("collect-history", false, "Download 90-day historical data and exit")
	testTelegram    := flag.Bool("test-telegram", false, "Send a test Telegram message and exit")
	metricsPortFlag := flag.Int("metrics-port", -1, "Prometheus /metrics port (0=disabled; overrides config)")
	configFile      := flag.String("config", "", "Path to config.yaml (default: config/config.yaml)")
	dryRunFile      := flag.String("dry-run-file", "", "Write cycle results to this JSON file (TASK-049)")
	validateFlag    := flag.Bool("validate", false, "Check config and API connectivity then exit (0=ok, 1=errors)")
	flag.Parse()

	// Set up graceful shutdown context — cancelled on SIGTERM or SIGINT.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// TASK-073: SIGHUP triggers a config hot-reload without restarting the bot.
	sighupCh := make(chan os.Signal, 1)
	signal.Notify(sighupCh, syscall.SIGHUP)

	// Load .env first so ENV vars are available for config overlay.
	_ = godotenv.Load()

	// Load configuration: yaml file + ENV overlay.
	cfgPath := *configFile
	if cfgPath == "" {
		cfgPath = "config/config.yaml"
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		slog.Warn("config load failed, using defaults", "err", err)
		cfg, _ = config.Load("") // pure defaults + ENV
	}

	// CLI flags win over config file for loop and metrics-port.
	if *loopFlag > 0 {
		cfg.LoopSec = *loopFlag
	}
	if *metricsPortFlag >= 0 {
		cfg.MetricsPort = *metricsPortFlag
	}

	// TASK-109: register any custom city lat/lon from config before validation.
	for _, cd := range cfg.CityDefs {
		if cd.Name != "" {
			weather.RegisterCity(cd.Name, cd.Lat, cd.Lon)
		}
	}

	// TASK-080: apply Kelly parameters from config to strategy package vars.
	strategy.KellyFraction = cfg.KellyFraction
	strategy.MaxKellyFraction = cfg.MaxKellyFraction
	// TASK-141: fee-adjusted Kelly — use real protocol fee rate.
	strategy.ProtocolFeeRate = cfg.ProtocolFeeRate
	// TASK-162: configurable minimum bet size.
	strategy.MinBetUSDC = cfg.MinBetUSDC

	// TASK-177: apply per-source timeouts from config to the collectors package.
	collectors.SetSourceTimeouts(cfg.SourceTimeouts)

	// TASK-082: validate config at startup.
	{
		vr := config.Validate(cfg)
		for _, w := range vr.Warnings {
			slog.Warn("config warning", "msg", w)
		}
		if len(vr.Errors) > 0 {
			for _, e := range vr.Errors {
				slog.Error("config error", "msg", e)
			}
			slog.Error("config validation failed — fix errors above and restart")
			os.Exit(1)
		}
	}

	// Start the Prometheus metrics server unless disabled.
	var metricsSrv *http.Server
	if cfg.MetricsPort > 0 {
		metricsSrv = metrics.Start(fmt.Sprintf(":%d", cfg.MetricsPort), cfg.DataRoot)
	}

	slog.Info("config loaded",
		"cities", cfg.Cities,
		"min_edge", cfg.MinEdge,
		"max_bet", cfg.MaxBet,
		"loop_sec", cfg.LoopSec,
		"metrics_port", cfg.MetricsPort,
		"data_root", cfg.DataRoot,
	)

	// TASK-170: initialise market discovery cache root so cache.go knows where
	// to write/read data/markets_cache.json.
	markets.SetCacheDataRoot(cfg.DataRoot)

	// --- Telegram test mode ---
	if *testTelegram {
		if err := notifier.SendTestMessage(); err != nil {
			slog.Error("Telegram test failed", "err", err)
			os.Exit(1)
		}
		slog.Info("Telegram test message sent successfully")
		return
	}

	// --- Historical collection mode ---
	if *collectHistory {
		slog.Info("collecting 90-day historical data for all cities...")
		if err := collectors.CollectHistory(cfg.DataRoot); err != nil {
			slog.Error("history collection failed", "err", err)
			os.Exit(1)
		}
		slog.Info("historical data collection complete")
		return
	}

	// --- Validate mode (TASK-143) ---
	if *validateFlag {
		runValidate(cfg)
	}

	dryRun := !*live
	if dryRun {
		slog.Info("DRY RUN mode — no real orders will be placed")
	} else {
		slog.Warn("LIVE MODE — real money!")

		// TASK-053: validate required credentials before risking real money.
		var missingEnv []string
		if cfg.PolyPrivateKey == "" {
			missingEnv = append(missingEnv, "POLYMARKET_PRIVATE_KEY")
		}
		if cfg.PolyAddress == "" {
			missingEnv = append(missingEnv, "POLYMARKET_ADDRESS")
		}
		if len(missingEnv) > 0 {
			fmt.Fprintln(os.Stderr, "ERROR: --live mode requires the following environment variables to be set:")
			for _, v := range missingEnv {
				fmt.Fprintf(os.Stderr, "  %s\n", v)
			}
			fmt.Fprintln(os.Stderr, "\nSet them via .env file or export before running:")
			for _, v := range missingEnv {
				fmt.Fprintf(os.Stderr, "  export %s=<value>\n", v)
			}
			os.Exit(1)
		}
	}

	// Print Brier score from past bets at startup
	calibration.PrintBrierScore(cfg.DataRoot)
	// TASK-147: log calibration drift status at startup.
	// TASK-178: check 3-week Brier trend and alert on sustained worsening.
	// TASK-198: persist daily Brier snapshot for long-term trend visualization.
	if startupDriftRecords, err := calibration.LoadHistory(cfg.DataRoot); err == nil {
		if line := calibration.DriftStatusLine(startupDriftRecords); line != "" {
			slog.Info("calibration drift status", "status", line)
		}
		if alerted, msg := calibration.BrierTrendAlert(startupDriftRecords); alerted {
			slog.Warn("calibration trend alert", "msg", msg)
			_ = notifier.NotifyError("Brier trend", fmt.Errorf("%s", msg))
		} else if trendLine := calibration.BrierTrendLine(startupDriftRecords); trendLine != "" {
			slog.Info("calibration trend", "status", trendLine)
		}
		if err2 := calibration.AppendBrierSnapshot(startupDriftRecords, cfg.DataRoot); err2 != nil {
			slog.Warn("brier snapshot: failed to persist", "err", err2)
		}
	}

	// TASK-072: weak signal alert — warn if any signal type has <40% win rate (≥10 samples).
	// TASK-074: export calibration snapshot at startup.
	// TASK-132: rolling win-rate alert at startup.
	if startupHistory, err := calibration.LoadHistory(cfg.DataRoot); err == nil {
		sigBreakdown := calibration.SignalBreakdown(startupHistory)
		weakSignals := calibration.WeakSignalAlert(sigBreakdown, 10, 40.0)
		for _, warn := range weakSignals {
			slog.Warn("weak signal detected: "+warn, "action", "consider raising min_edge")
			_ = notifier.NotifyError("weak signal", fmt.Errorf("%s", warn))
		}
		// TASK-074: persist snapshot for dashboard / external tools
		if err := calibration.ExportSnapshot(startupHistory, cfg.MinEdge, cfg.MaxDrawdownFraction, cfg.DataRoot); err != nil {
			slog.Warn("calibration snapshot export failed", "err", err)
		}
		// TASK-132: check rolling win rate at startup; alert if below threshold.
		if cfg.RollingWinRateWindow > 0 {
			if alert, msg := calibration.WinRateAlert(startupHistory, cfg.RollingWinRateWindow, cfg.RollingWinRateThreshold); alert {
				slog.Warn("rolling win rate alert", "message", msg)
				_ = notifier.NotifyError("rolling_winrate", fmt.Errorf("⚠️ %s", msg))
			}
		}
		// TASK-133: rebuild hourly timing stats from full history at startup.
		if err := calibration.RebuildHourlyStats(startupHistory, cfg.DataRoot); err != nil {
			slog.Warn("timing: hourly stats rebuild failed", "err", err)
		}
	}

	// Initialise risk manager from config.
	riskMgr := risk.New(risk.Config{
		MaxDailyLossUSDC:      cfg.MaxDailyLossUSDC,
		MaxDailyProfitUSDC:    cfg.MaxDailyProfitUSDC,
		MaxDailyBets:          cfg.MaxDailyBets,
		MaxOpenPositions:      cfg.MaxOpenPositions,
		MaxSameCitySignalBets: cfg.MaxSameCitySignalBets,
		MaxExposureUSDC:       cfg.MaxExposureUSDC,
	})

	sess := &sessionStats{startTime: time.Now()}

	// TASK-136: track when we last sent a duplicate-market Telegram alert.
	// We only send one alert per 24 hours to avoid spamming.
	var lastDuplicateAlert time.Time

	// TASK-119: track consecutive Polymarket API failures across cycles.
	// A Telegram alert is sent when the streak transitions from 2 → 3 failures.
	consecutiveAPIFails := 0

	// cycleResult holds summary data from one run() call used for adaptive
	// loop scheduling (TASK-047) and dry-run-file output (TASK-049).
	type cycleResult struct {
		placed            int
		marketsFound      int
		highEdgeBet       bool           // at least one bet with edge > 0.15
		thinLiquidityOnly bool           // all candidate markets had thin liquidity
		decisions         []dryRunRecord // populated for dry-run-file (TASK-049)
		confSum           float64        // sum of ff.Confidence for placed bets (TASK-199)
		confCount         int            // bets that had a FusedForecast (TASK-199)
	}

	run := func() cycleResult {
		var res cycleResult
		sess.cycleCount.Add(1)
		slog.Info("=== cycle start", "time", time.Now().Format(time.RFC3339))

		// TASK-111: respect /pause command from Telegram.
		if notifier.IsPaused() {
			slog.Info("trading paused via /pause — skipping cycle")
			return res
		}

		// 0. Load bet history for dedup and risk checks.
		history, err := calibration.LoadHistory(cfg.DataRoot)
		if err != nil {
			slog.Warn("failed to load bet history, proceeding without dedup/risk", "err", err)
			history = nil
		}

		// TASK-065: build/refresh the market loss blacklist from recent lost bets.
		// Any bet resolved as a loss within the last LossBlacklistDays days is added.
		if cfg.LossBlacklistDays > 0 {
			for _, r := range history {
				if r.Outcome != nil && !*r.Outcome {
					age := time.Since(r.ResolvedAt)
					if age < time.Duration(cfg.LossBlacklistDays)*24*time.Hour {
						_ = markets.AddToBlacklist(r.ConditionID, r.City, r.Signal, cfg.LossBlacklistDays, cfg.DataRoot)
					}
				}
			}
		}
		blacklist := markets.LoadBlacklist(cfg.DataRoot)
		if len(blacklist) > 0 {
			slog.Info("blacklist loaded", "active_entries", len(blacklist))
		}

		// TASK-044: load persisted bankroll (updated across sessions via bankroll.json).
		// Apply the Brier-score multiplier on top of the persisted amount so that
		// a well-calibrated bot naturally bets larger as it proves itself.
		baseBankroll := calibration.LoadBankroll(cfg.DataRoot)
		brierScore, _, _ := calibration.BrierScore(history)
		bankrollMultiplier := calibration.BankrollMultiplier(brierScore)
		effectiveBankroll := baseBankroll * bankrollMultiplier
		slog.Info("bankroll loaded",
			"base", fmt.Sprintf("%.2f USDC", baseBankroll),
			"brier_score", fmt.Sprintf("%.4f", brierScore),
			"multiplier", fmt.Sprintf("%.2f", bankrollMultiplier),
			"effective", fmt.Sprintf("%.2f USDC", effectiveBankroll),
		)

		// TASK-069: peak drawdown circuit-breaker.
		// Update the all-time peak, then reduce effectiveBankroll if we are in a
		// significant drawdown. This scales down bet sizes automatically without
		// halting the bot entirely.
		peakBankroll, _ := calibration.UpdatePeakBankroll(baseBankroll, cfg.DataRoot)
		drawdownFrac := calibration.DrawdownFraction(peakBankroll, baseBankroll)
		drawdownMult := calibration.DrawdownMultiplier(drawdownFrac, cfg.MaxDrawdownFraction)
		if drawdownMult < 1.0 {
			calibration.LogDrawdown(peakBankroll, baseBankroll)
			effectiveBankroll *= drawdownMult
			slog.Warn("drawdown guard: bet sizes reduced",
				"drawdown_pct", fmt.Sprintf("%.1f%%", drawdownFrac*100),
				"drawdown_mult", fmt.Sprintf("%.2f", drawdownMult),
				"effective_bankroll", fmt.Sprintf("%.2f USDC", effectiveBankroll),
			)
		}

		// TASK-171: reduce Kelly on losing streaks to limit drawdown during bad runs.
		// 2 losses → 0.85×, 3+ losses → 0.70×; resets to 1.0 on any win.
		{
			streak := calibration.CurrentStreak(history)
			streakFactor := calibration.StreakKellyFactor(streak)
			if streakFactor < 1.0 {
				strategy.KellyFraction = cfg.KellyFraction * streakFactor
				slog.Warn("streak Kelly reduction applied",
					"consecutive_losses", streak.Count,
					"kelly_factor", fmt.Sprintf("%.2f", streakFactor),
					"kelly_fraction", fmt.Sprintf("%.4f", strategy.KellyFraction),
				)
			} else {
				strategy.KellyFraction = cfg.KellyFraction
			}
		}

		// TASK-152: dynamic Kelly scaling — scale MaxKellyFraction up/down based on
		// bankroll growth relative to the configured initial reference.
		{
			initialBR := cfg.InitialBankroll
			if initialBR <= 0 {
				initialBR = calibration.DefaultBankroll
			}
			kellyScale := calibration.BankrollKellyScale(baseBankroll, initialBR)
			if kellyScale != 1.0 {
				strategy.MaxKellyFraction = cfg.MaxKellyFraction * kellyScale
				slog.Debug("kelly scale applied",
					"initial_bankroll", fmt.Sprintf("%.2f", initialBR),
					"current_bankroll", fmt.Sprintf("%.2f", baseBankroll),
					"scale", fmt.Sprintf("%.4f", kellyScale),
					"max_kelly_fraction", fmt.Sprintf("%.4f", strategy.MaxKellyFraction),
				)
			} else {
				strategy.MaxKellyFraction = cfg.MaxKellyFraction
			}
		}

		// TASK-066: compute adaptive min_edge based on rolling Brier performance.
		// This relaxes the threshold when we're well-calibrated and tightens it
		// when recent performance has been poor.
		adaptiveMinEdge := calibration.AdaptiveMinEdge(history, cfg.MinEdge)
		if adaptiveMinEdge != cfg.MinEdge {
			slog.Info("adaptive min_edge applied",
				"base", cfg.MinEdge,
				"adjusted", fmt.Sprintf("%.4f", adaptiveMinEdge),
			)
		}

		// TASK-122: load Platt probability calibrator (fitted from resolved bet history).
		// When active (N >= 20 resolved bets), it corrects systematic over/under-confidence.
		plattCal := calibration.LoadCalibrator(cfg.DataRoot)

		// TASK-181: optionally use isotonic regression calibration instead of Platt scaling.
		isoCal := calibration.LoadIsotonic(cfg.DataRoot)

		// TASK-156: per-signal adaptive Kelly multiplier based on historical Brier performance.
		// Computed once per cycle from resolved history; applied per-bet below.
		signalKellyMults := calibration.SignalKellyMultipliers(history)
		if len(signalKellyMults) > 0 {
			slog.Debug("signal kelly multipliers loaded", "count", len(signalKellyMults))
		}

		// Risk summary at cycle start.
		slog.Info(risk.Summary(history, risk.Config{
			MaxDailyLossUSDC:   cfg.MaxDailyLossUSDC,
			MaxDailyProfitUSDC: cfg.MaxDailyProfitUSDC,
			MaxDailyBets:       cfg.MaxDailyBets,
			MaxOpenPositions:   cfg.MaxOpenPositions,
		}))

		// Pre-check: if already over a session-level limit, skip entire cycle.
		if err := riskMgr.AllowBet(history); err != nil {
			slog.Warn("risk gate blocked entire cycle", "reason", err.Error())
			return res
		}

		openPositions := make(map[string]bool, len(history))
		for _, r := range history {
			if r.Outcome == nil {
				openPositions[r.ConditionID] = true
			}
		}
		slog.Info("open positions loaded", "count", len(openPositions))

		// TASK-135: build open-bet info slice for duplicate-market guard.
		openBetInfos := make([]markets.OpenBetInfo, len(history))
		for i, r := range history {
			openBetInfos[i] = markets.OpenBetInfo{
				City:     r.City,
				Signal:   r.Signal,
				PlacedAt: r.Timestamp,
				Resolved: r.Outcome != nil,
			}
		}

		// 1. Discover weather markets FIRST so we know which cities are active.
		// TASK-043: only fetch fresh forecasts for cities that have live markets.
		mkt, err := markets.GetWeatherMarkets()
		if err != nil {
			consecutiveAPIFails++
			slog.Error("markets fetch failed",
				"err", err,
				"api_fail_streak", consecutiveAPIFails,
			)
			// TASK-119: send a Telegram alert at the 2→3 failure transition only.
			if consecutiveAPIFails == 3 {
				msg := fmt.Sprintf("⚠️ Polymarket API down: %d consecutive failures\nLast error: %s",
					consecutiveAPIFails, err.Error())
				_ = notifier.NotifyError("polymarket_api_down", fmt.Errorf("%s", msg))
			}
			return res
		}
		consecutiveAPIFails = 0 // reset streak on success
		slog.Info("weather markets found", "count", len(mkt))

		// TASK-153: merge watchlisted conditionIDs into the market set.
		// Fetches each watchlisted market via RefreshPrices so it gets live prices.
		{
			watchlistIDs := notifier.LoadWatchlist(cfg.DataRoot)
			if len(watchlistIDs) > 0 {
				// Build existing conditionID set to avoid duplicates.
				existing := make(map[string]bool, len(mkt))
				for _, m := range mkt {
					existing[m.ConditionID] = true
				}
				added := 0
				for _, condID := range watchlistIDs {
					if existing[condID] {
						continue
					}
					placeholder := markets.Market{ConditionID: condID}
					refreshed, ok, err := markets.RefreshPrices(placeholder)
					if err != nil || !ok {
						slog.Warn("watchlist: could not fetch market", "conditionID", condID, "err", err)
						continue
					}
					mkt = append(mkt, refreshed)
					existing[condID] = true
					added++
					slog.Info("watchlist: added market", "conditionID", condID)
				}
				if added > 0 {
					slog.Info("watchlist: merged markets", "added", added, "total", len(mkt))
				}
			}
		}

		// TASK-136: detect duplicate-market fingerprints and alert once per day.
		if dupes := markets.FindDuplicates(mkt); len(dupes) > 0 {
			slog.Info("duplicate markets detected", "groups", len(dupes))
			if time.Since(lastDuplicateAlert) > 24*time.Hour {
				if err := notifier.NotifyDuplicates(dupes); err != nil {
					slog.Warn("duplicate markets alert failed", "err", err)
				} else {
					lastDuplicateAlert = time.Now()
				}
			}
		}

		if len(mkt) == 0 {
			slog.Warn("no weather markets found on Polymarket right now")
			return res
		}
		sess.marketsFound.Add(int64(len(mkt)))

		// Collect unique city slugs from active markets, intersected with configured cities.
		configuredCities := make(map[string]bool, len(cfg.Cities))
		for _, c := range cfg.Cities {
			configuredCities[c] = true
		}
		activeCitySet := make(map[string]bool)
		for _, m := range mkt {
			if m.City != "" && configuredCities[m.City] {
				activeCitySet[m.City] = true
			}
		}
		activeCitiesSlice := make([]string, 0, len(activeCitySet))
		for c := range activeCitySet {
			activeCitiesSlice = append(activeCitiesSlice, c)
		}
		slog.Info("active cities with markets", "count", len(activeCitiesSlice), "cities", activeCitiesSlice)

		// 2. Fetch fused forecasts — fresh only for cities with active markets.
		// TASK-042: detect significant forecast shifts (e.g. incoming storms)
		// DetectForecastShift must be called BEFORE AggregateForCities overwrites cache.
		for _, city := range activeCitiesSlice {
			if shift := collectors.DetectForecastShift(city, 0, nil, cfg.DataRoot); shift != nil && shift.Significant {
				_ = shift // will re-check after fetching new data below
			}
		}

		fusedForecasts, err := collectors.AggregateForCities(activeCitiesSlice, cfg.DataRoot)
		if err != nil {
			slog.Warn("aggregator failed, falling back to OpenMeteo only", "err", err)
		}

		// TASK-042: notify on significant forecast shifts for active cities.
		for city, ff := range fusedForecasts {
			if !activeCitySet[city] {
				continue // only alert on cities with live markets
			}
			shift := collectors.DetectForecastShift(city, 0, ff, cfg.DataRoot)
			if shift != nil && shift.Significant {
				oldMaxTemp := ff.Forecast.MaxTempC - shift.DeltaMaxTempC
				oldPrecipP := ff.Forecast.PrecipitationProbability - shift.DeltaPrecipP
				if err := notifier.NotifyForecastShift(city, oldMaxTemp, ff.Forecast.MaxTempC,
					oldPrecipP, ff.Forecast.PrecipitationProbability); err != nil {
					slog.Warn("forecast shift notification failed", "city", city, "err", err)
				}
			}
		}

		// 3. Fetch plain OpenMeteo forecasts for fallback Evaluate() (active cities only).
		legacyForecasts := make(map[string][]weather.Forecast)
		for city := range activeCitySet {
			fc, err := weather.GetForecast(city, 3)
			if err != nil {
				slog.Warn("forecast failed", "city", city, "err", err)
				continue
			}
			legacyForecasts[city] = fc
			f := fc[0]

			confidence := 0.0
			if ff, ok := fusedForecasts[city]; ok {
				confidence = ff.Confidence
			}

			slog.Info("forecast",
				"city", city,
				"max_c", fmt.Sprintf("%.1f", f.MaxTempC),
				"precip_mm", fmt.Sprintf("%.1f", f.PrecipitationMM),
				"rain_p", fmt.Sprintf("%.2f", weather.RainProbability(f)),
				"confidence", fmt.Sprintf("%.2f", confidence),
			)
		}

		// TASK-056: snapshot prices for open positions using current market data.
		// Build a conditionID→yesTokenID lookup from fetched markets and snapshot
		// each open position so we can detect adverse price moves later.
		{
			openTokens := make(map[string]string, len(openPositions))
			for _, m := range mkt {
				if openPositions[m.ConditionID] && m.YesTokenID != "" {
					openTokens[m.ConditionID] = m.YesTokenID
				}
			}
			if len(openTokens) > 0 {
				markets.SnapshotOpenPositions(openTokens, cfg.DataRoot)
			}
		}

		// TASK-061: check profit-taking opportunities for all open positions.
		// Build a lookup map from conditionID to current Market prices.
		{
			mktByCondID := make(map[string]markets.Market, len(mkt))
			for _, m := range mkt {
				mktByCondID[m.ConditionID] = m
			}
			checkProfitAlerts(history, mktByCondID, cfg.DataRoot)
		}

		// TASK-188: compute exit signals from latest prediction log.
		// For each open position, compare entry ourP to the latest evaluated
		// ourP from today's prediction log and alert via Telegram when the
		// forecast has deteriorated by more than 0.20 (SELL signal).
		{
			forecasts := make(map[string]float64)
			if preds, predErr := strategy.LoadPredictions(
				time.Now().UTC().Format("2006-01-02"), cfg.DataRoot,
			); predErr == nil {
				// Walk newest-first; first hit per conditionID is the latest value.
				for i := len(preds) - 1; i >= 0; i-- {
					p := preds[i]
					if _, ok := forecasts[p.ConditionID]; !ok {
						forecasts[p.ConditionID] = p.OurP
					}
				}
			}
			exitSignals := calibration.ComputeExitSignals(history, forecasts, nil)
			for _, es := range calibration.SellSignals(exitSignals) {
				slog.Warn("exit signal: SELL recommended",
					"conditionID", es.ConditionID,
					"side", es.Side,
					"entry_p", fmt.Sprintf("%.3f", es.EntryP),
					"current_p", fmt.Sprintf("%.3f", es.CurrentP),
					"delta", fmt.Sprintf("%+.3f", es.Delta),
				)
				msg := fmt.Sprintf(
					"🚨 Exit Signal — SELL recommended\nconditionID: %s\nSide: %s\nEntry P: %.3f → Current P: %.3f (Δ%+.3f)\nForecast worsened >0.20 — consider selling the position.",
					es.ConditionID, es.Side, es.EntryP, es.CurrentP, es.Delta,
				)
				if nErr := notifier.NotifyError("exit_signal_sell", fmt.Errorf("%s", msg)); nErr != nil {
					slog.Warn("exit signal notify failed", "err", nErr)
				}
			}
		}

		// 4. Enrich markets with liquidity data (order book depth).
		markets.EnrichWithLiquidity(mkt)

		// 4b. TASK-030: score and sort markets before evaluation.
		// This ensures the highest-value opportunities are evaluated first,
		// preventing the daily cap from being consumed by marginal markets.
		type scored struct {
			m     markets.Market
			ff    *collectors.FusedForecast
			score float64
			isNew bool // TASK-124: true when market was first seen < 2 hours ago
		}
		scoredList := make([]scored, 0, len(mkt))

		for _, m := range mkt {
			if m.City != "" && !configuredCities[m.City] {
				continue
			}

			// TASK-124: record first-seen timestamp for new markets.
			isNewMarket := markets.RecordFirstSeen(m.ConditionID, cfg.DataRoot)
			if isNewMarket {
				slog.Info("new market detected — reduced min_edge applied",
					"conditionID", m.ConditionID, "city", m.City, "signal", m.Signal)
			}

			// Resolve the best fused forecast for this market's expiry day.
			dayOffset := m.DaysUntilExpiry()
			var ff *collectors.FusedForecast
			if dayOffset > 0 && m.City != "" {
				if dayFF, err := collectors.AggregateForDay(m.City, dayOffset, cfg.DataRoot); err == nil {
					ff = dayFF
				}
			}
			if ff == nil {
				if v, ok := fusedForecasts[m.City]; ok {
					ff = v
				}
			}

			// TASK-029 / TASK-155: skip stale forecasts.
			if ff != nil && collectors.IsForecastStale(ff, cfg.MaxForecastAgeHours) {
				age := collectors.ForecastAge(ff)
				slog.Warn("stale forecast, skipping market",
					"city", m.City,
					"age", age.Round(time.Minute).String(),
					"max_age_hours", cfg.MaxForecastAgeHours,
				)
				metrics.IncStaleForecastSkipped()
				ff = nil
			}

			sc := strategy.ScoreMarket(m, ff)
			scoredList = append(scoredList, scored{m, ff, sc, isNewMarket})
		}

		// TASK-047: detect if all candidates have thin liquidity.
		if len(scoredList) > 0 {
			allThin := true
			for _, sc := range scoredList {
				if !sc.m.ThinLiquidity {
					allThin = false
					break
				}
			}
			res.thinLiquidityOnly = allThin
		}
		res.marketsFound = len(scoredList)

		// Sort descending by score.
		sort.Slice(scoredList, func(i, j int) bool {
			return scoredList[i].score > scoredList[j].score
		})

		// Respect MaxBetsPerCycle limit when sorting (cap the candidate list).
		maxCycle := cfg.MaxBetsPerCycle
		if maxCycle > 0 && len(scoredList) > maxCycle*3 {
			// Keep 3× the cap as candidates (some will be skipped by guards).
			scoredList = scoredList[:maxCycle*3]
		}

		// 5. Evaluate and place bets
		placed := 0
		// placedThisCycle tracks markets we've committed to this cycle —
		// used by the correlation guard (TASK-028).
		var placedThisCycle []markets.Market

		for _, sc := range scoredList {
			m := sc.m
			ff := sc.ff

			// TASK-037: skip markets that are too close to expiry.
			if cfg.MinHoursToExpiry > 0 {
				if hoursLeft := m.HoursUntilExpiry(); hoursLeft < cfg.MinHoursToExpiry {
					slog.Info("skipped: near-expiry market",
						"conditionID", m.ConditionID,
						"hours_left", fmt.Sprintf("%.1fh", hoursLeft),
						"min_hours", fmt.Sprintf("%.1fh", cfg.MinHoursToExpiry),
						"question", truncate(m.Question, 60))
					continue
				}
			}

			// TASK-175: skip low-volume markets (volume=0 means unknown, so we only skip when explicitly below threshold).
			if cfg.MinVolumeUSDC > 0 && m.VolumeUSDC > 0 && m.VolumeUSDC < cfg.MinVolumeUSDC {
				slog.Info("skipped: low volume",
					"conditionID", m.ConditionID,
					"volume_usdc", fmt.Sprintf("%.0f", m.VolumeUSDC),
					"min_volume_usdc", fmt.Sprintf("%.0f", cfg.MinVolumeUSDC),
					"question", truncate(m.Question, 60))
				continue
			}

			// Skip markets where we already have an open position.
			if openPositions[m.ConditionID] {
				slog.Info("skipped: already have position on", "conditionID", m.ConditionID,
					"question", truncate(m.Question, 60))
				continue
			}

			// TASK-065: skip markets on the loss blacklist.
			if cfg.LossBlacklistDays > 0 {
				if bl, until := markets.IsBlacklisted(m.ConditionID, blacklist); bl {
					slog.Info("skipped: loss-blacklisted market",
						"conditionID", m.ConditionID,
						"until", until.Format(time.DateOnly),
						"question", truncate(m.Question, 60))
					continue
				}
			}

			// TASK-131: skip markets where the (city, signal) pair is auto-blacklisted.
			if m.City != "" && m.Signal != "" && markets.IsAutoBlacklisted(m.City, m.Signal, cfg.DataRoot) {
				slog.Info("skipped: auto-blacklisted (city+signal)",
					"city", m.City,
					"signal", m.Signal,
					"conditionID", m.ConditionID,
					"question", truncate(m.Question, 60))
				continue
			}

			// TASK-135: skip if we already have an open bet on the same weather event
			// (same city+signal placed within the last 14 days).
			if m.City != "" && m.Signal != "" && markets.IsDuplicateOf(m, openBetInfos) {
				slog.Info("duplicate-market: already bet on same event",
					"city", m.City,
					"signal", m.Signal,
					"conditionID", m.ConditionID,
					"question", truncate(m.Question, 60))
				continue
			}

			// TASK-028: skip if a correlated city has already been bet this cycle.
			if corr, reason := risk.CorrelatedCitiesOpen(m, placedThisCycle); corr {
				slog.Info("skipped: "+reason, "conditionID", m.ConditionID)
				continue
			}

			// TASK-030: enforce MaxBetsPerCycle hard cap.
			if maxCycle > 0 && placed >= maxCycle {
				slog.Info("max_bets_per_cycle reached, stopping",
					"limit", maxCycle, "placed", placed)
				break
			}

			// TASK-118: apply per-signal min_edge override, scaled by the same
			// adaptive factor used for the global threshold.
			signalMinEdge := config.GetMinEdgeForSignal(cfg, m.Signal)
			adaptedSignalMinEdge := adaptiveMinEdge
			if signalMinEdge != cfg.MinEdge && cfg.MinEdge > 0 {
				adaptiveFactor := adaptiveMinEdge / cfg.MinEdge
				adaptedSignalMinEdge = signalMinEdge * adaptiveFactor
			}
			if adaptedSignalMinEdge != adaptiveMinEdge {
				slog.Info("using signal min_edge",
					"signal", m.Signal,
					"min_edge", fmt.Sprintf("%.4f", adaptedSignalMinEdge),
					"global_min_edge", fmt.Sprintf("%.4f", adaptiveMinEdge),
				)
			}
			// TASK-124: new markets get a 30% reduced min_edge for better price discovery.
			if sc.isNew || markets.IsNew(m.ConditionID, cfg.DataRoot) {
				adaptedSignalMinEdge *= 0.70
				slog.Debug("new market: reduced min_edge by 30%",
					"conditionID", m.ConditionID,
					"min_edge", fmt.Sprintf("%.4f", adaptedSignalMinEdge),
				)
			}

			var d *strategy.Decision
			if ff != nil {
				d = strategy.EvaluateFused(m, ff, effectiveBankroll, adaptedSignalMinEdge, cfg.MaxBet, cfg.DataRoot)
			}
			if d == nil {
				d = strategy.Evaluate(m, legacyForecasts, effectiveBankroll, signalMinEdge, cfg.MaxBet)
			}
			if d == nil {
				continue
			}

			// TASK-122/181: apply probability calibration (isotonic or Platt).
			if cfg.UseIsotonic && isoCal.IsActive() {
				d = applyIsotonicCalibration(d, isoCal, adaptedSignalMinEdge, effectiveBankroll)
			} else {
				d = applyPlattCalibration(d, plattCal, adaptedSignalMinEdge, effectiveBankroll)
			}
			if d == nil {
				continue
			}

			// TASK-174: apply per-(city,signal) bias correction.
			// Bias = mean(ourP - outcome) over last 30 resolved bets for this pair.
			// Active only when >= 5 resolved samples exist.
			if correctedP, bias := calibration.CorrectProbability(m.City, m.Signal, d.OurProbability, cfg.DataRoot); bias != 0 {
				newEdge := correctedP - d.MarketPrice
				if newEdge < adaptedSignalMinEdge {
					slog.Info("skipped: edge below min after bias correction",
						"city", m.City, "signal", m.Signal,
						"ourP", fmt.Sprintf("%.3f", d.OurProbability),
						"correctedP", fmt.Sprintf("%.3f", correctedP),
						"bias", fmt.Sprintf("%+.3f", bias),
						"edge", fmt.Sprintf("%.3f", newEdge),
					)
					continue
				}
				slog.Debug("bias correction applied",
					"city", m.City, "signal", m.Signal,
					"ourP", fmt.Sprintf("%.3f", d.OurProbability),
					"correctedP", fmt.Sprintf("%.3f", correctedP),
					"bias", fmt.Sprintf("%+.3f", bias),
				)
				out := *d
				out.OurProbability = correctedP
				out.Edge = newEdge
				out.Reason = fmt.Sprintf("%s [bias:%+.3f]", d.Reason, bias)
				d = &out
			}

			// TASK-133: apply time-of-day sizing multiplier based on historical win rate.
			timingMult := calibration.TimingMultiplierNow(cfg.DataRoot)
			if timingMult != 1.0 {
				d.SizeUSDC *= timingMult
				if d.SizeUSDC > cfg.MaxBet {
					d.SizeUSDC = cfg.MaxBet
				}
				slog.Debug("timing multiplier applied",
					"hour_utc", time.Now().UTC().Hour(),
					"multiplier", fmt.Sprintf("%.3f", timingMult),
					"size_usdc", fmt.Sprintf("%.2f", d.SizeUSDC),
				)
			}

			// TASK-156: apply per-signal adaptive Kelly multiplier.
			// Signals with a strong historical Brier score bet larger; poorly
			// calibrated signals are scaled back automatically.
			{
				skInfo := calibration.LookupSignalKelly(signalKellyMults, m.Signal)
				if skInfo.Multiplier != 1.0 {
					before := d.SizeUSDC
					d.SizeUSDC = math.Round(d.SizeUSDC*skInfo.Multiplier*100) / 100
					if d.SizeUSDC > cfg.MaxBet {
						d.SizeUSDC = cfg.MaxBet
					}
					d.Reason += " " + skInfo.String(m.Signal)
					slog.Debug("signal kelly multiplier applied",
						"signal", m.Signal,
						"multiplier", fmt.Sprintf("%.2f", skInfo.Multiplier),
						"brier", fmt.Sprintf("%.4f", skInfo.BrierScore),
						"n", skInfo.Count,
						"size_before", fmt.Sprintf("%.2f", before),
						"size_after", fmt.Sprintf("%.2f", d.SizeUSDC),
					)
					if d.SizeUSDC < 0.5 {
						slog.Info("skipped: size below minimum after signal kelly scaling",
							"conditionID", m.ConditionID, "signal", m.Signal)
						continue
					}
				}
			}

			// TASK-165: proactive opportunity alert — notify when edge > threshold.
			if cfg.OpportunityAlertThreshold > 0 && d.Edge >= cfg.OpportunityAlertThreshold {
				condID := d.Market.ConditionID
				if _, already := notifiedOpportunities.LoadOrStore(condID, true); !already {
					if aErr := notifier.SendOpportunityAlert(d); aErr != nil {
						slog.Warn("opportunity alert failed", "err", aErr)
					} else {
						slog.Info("opportunity alert sent",
							"conditionID", condID,
							"edge", fmt.Sprintf("%.3f", d.Edge),
						)
					}
				}
			}

			// Per-bet risk gate: re-evaluate after each bet (history may grow).
			if err := riskMgr.AllowBet(history); err != nil {
				slog.Warn("risk gate blocked bet", "reason", err.Error())
				// TASK-046: webhook notification for risk-blocked bets.
				if wErr := notifier.WebhookBetSkippedRisk(m.ConditionID, err.Error()); wErr != nil {
					slog.Warn("webhook notify failed", "event", "bet_skipped_risk", "err", wErr)
				}
				break // stop placing more bets this cycle
			}

			// TASK-071: total exposure cap — skip if total USDC at risk exceeds max.
			if err := riskMgr.CheckExposure(history); err != nil {
				slog.Info("exposure guard: skip bet", "reason", err.Error())
				break // stop placing more bets this cycle
			}

			// TASK-054: correlation guard — skip if already over-concentrated in (city, signal).
			if err := riskMgr.CheckCorrelation(history, m.City, m.Signal); err != nil {
				slog.Info("corr guard: skip bet",
					"city", m.City,
					"signal", m.Signal,
					"reason", err.Error(),
				)
				continue
			}

			// TASK-127: signal concentration guard — ensure no single signal type
			// (rain/heat/cold/etc.) dominates more than MaxSignalExposurePct of
			// total open exposure, preventing model-bias-driven correlated losses.
			if err := riskMgr.CheckSignalConcentration(history, m.Signal, d.SizeUSDC); err != nil {
				slog.Info("signal concentration guard: skip bet",
					"signal", m.Signal,
					"size_usdc", fmt.Sprintf("%.2f", d.SizeUSDC),
					"reason", err.Error(),
				)
				continue
			}

			// TASK-056: adverse move check — if price of our side has fallen >0.15
			// over the last 3 snapshots, require extra edge (+0.05) as safety margin.
			// TASK-060: momentum signal — if favorable trend, log boost; if strong
			// adverse momentum, require extra edge (+0.03) on top of adverse-move check.
			if priceHistory, phErr := markets.GetPriceHistory(m.ConditionID, cfg.DataRoot); phErr == nil {
				if adverse, drop := markets.DetectAdverseMove(d.Side, priceHistory); adverse {
					requiredEdge := cfg.MinEdge + 0.05
					if d.Edge < requiredEdge {
						slog.Info("adverse move: edge below elevated threshold, skipping",
							"conditionID", m.ConditionID,
							"our_side", d.Side,
							"edge", fmt.Sprintf("%.3f", d.Edge),
							"required", fmt.Sprintf("%.3f", requiredEdge),
							"price_drop", fmt.Sprintf("%.3f", drop),
						)
						continue
					}
					slog.Info("adverse move detected but edge sufficient",
						"conditionID", m.ConditionID,
						"edge", fmt.Sprintf("%.3f", d.Edge),
						"required", fmt.Sprintf("%.3f", requiredEdge),
					)
				}

				// TASK-060: momentum-aware edge requirement.
				momDir, momStrength := markets.DetectMomentum(d.Side, priceHistory)
				switch momDir {
				case markets.MomentumFavorable:
					slog.Info("momentum: market moving in our favour",
						"conditionID", m.ConditionID,
						"side", d.Side,
						"strength", fmt.Sprintf("%.2f", momStrength),
					)
					// Append momentum info to decision reason.
					d.Reason += fmt.Sprintf(" | momentum=favorable(%.2f)", momStrength)
				case markets.MomentumAdverse:
					// Strong adverse momentum (strength > 0.60) → raise the bar.
					if momStrength > 0.60 {
						requiredEdge := cfg.MinEdge + 0.03
						if d.Edge < requiredEdge {
							slog.Info("momentum: adverse trend, skipping (edge insufficient)",
								"conditionID", m.ConditionID,
								"side", d.Side,
								"strength", fmt.Sprintf("%.2f", momStrength),
								"edge", fmt.Sprintf("%.3f", d.Edge),
								"required", fmt.Sprintf("%.3f", requiredEdge),
							)
							continue
						}
						d.Reason += fmt.Sprintf(" | momentum=adverse(%.2f,+0.03 req)", momStrength)
						slog.Info("momentum: adverse but edge sufficient, proceeding",
							"conditionID", m.ConditionID,
							"side", d.Side,
							"strength", fmt.Sprintf("%.2f", momStrength),
						)
					} else {
						d.Reason += fmt.Sprintf(" | momentum=adverse(%.2f)", momStrength)
					}
				}
			}

			prefix := ""
			if dryRun {
				prefix = "[DRY RUN] "
			}
			slog.Info(prefix+"bet",
				"side", d.Side,
				"size", fmt.Sprintf("$%.2f", d.SizeUSDC),
				"score", fmt.Sprintf("%.3f", sc.score),
				"question", truncate(d.Market.Question, 60),
				"reason", d.Reason,
			)

			if !dryRun {
				// TASK-036: refresh prices immediately before placing the order.
				// This guards against stale evaluation prices in volatile markets.
				refreshed, skip := preOrderRefresh(d, cfg.MinEdge)
				if skip {
					continue // edge evaporated after price refresh
				}
				if refreshed != nil {
					d = refreshed
				}

				// TASK-176: pre-trade slippage guard — simulate filling the order
				// against the live CLOB book to detect thin liquidity before committing.
				if d.TokenID != "" {
					buyingYes := d.Side == "YES"
					slippageRes := markets.CheckSlippage(d.TokenID, d.SizeUSDC, buyingYes)
					if slippageRes.Skip {
						slog.Info("slippage: skipping bet (slippage too high)",
							"conditionID", m.ConditionID,
							"slippage", fmt.Sprintf("%.3f", slippageRes.Slippage),
						)
						continue
					}
					if slippageRes.AdjustedSize < d.SizeUSDC {
						d2 := *d
						d2.SizeUSDC = slippageRes.AdjustedSize
						d2.Reason += fmt.Sprintf(" | slippage=%.3f(size_halved)", slippageRes.Slippage)
						d = &d2
					}
				}

				// Anti-detection: sleep a random human-like delay before each bet.
				if cfg.BetJitterEnabled {
					slog.Info("bet jitter: sleeping before order placement")
					ratelimit.BetJitter()
				}

				if err := placeBet(d); err != nil {
					slog.Error("order failed", "err", err)
					_ = notifier.NotifyError("placeBet", err)
					_ = notifier.WebhookError("placeBet", err) // TASK-046
				} else {
					if err := calibration.SaveBet(d, cfg.DataRoot); err != nil {
						slog.Warn("calibration save failed", "err", err)
					}
					if err := notifier.NotifyBet(d); err != nil {
						slog.Warn("telegram notify failed", "err", err)
					}
					// TASK-046: webhook notification for placed bets.
					if wErr := notifier.WebhookBetPlaced(
						d.Market.ConditionID, d.Side, d.Market.City, d.Market.Signal, d.Reason,
						d.SizeUSDC, d.Edge, d.OurProbability, d.MarketPrice,
					); wErr != nil {
						slog.Warn("webhook notify failed", "event", "bet_placed", "err", wErr)
					}
					// TASK-044: deduct bet size from persisted bankroll.
					newBankroll, err := calibration.AdjustBankrollOnBet(d.SizeUSDC, cfg.DataRoot)
					if err != nil {
						slog.Warn("bankroll update failed", "err", err)
					} else {
						effectiveBankroll = newBankroll * calibration.BankrollMultiplier(brierScore)
					}
					placed++
					sess.betsPlaced.Add(1)
					placedThisCycle = append(placedThisCycle, m)
					if d.Edge > 0.15 {
						res.highEdgeBet = true // TASK-047
					}
					// TASK-049: record decision for dry-run-file (live mode too).
					res.decisions = append(res.decisions, dryRunRecord{
						Market: truncate(d.Market.Question, 80),
						Side:   d.Side,
						OurP:   d.OurProbability,
						Edge:   d.Edge,
						Size:   d.SizeUSDC,
						Reason: d.Reason,
					})
					// TASK-199: accumulate confidence for cycle stats.
					if ff != nil {
						res.confSum += ff.Confidence
						res.confCount++
					}
				}
			} else {
				placed++
				sess.betsPlaced.Add(1)
				placedThisCycle = append(placedThisCycle, m)
				if d.Edge > 0.15 {
					res.highEdgeBet = true // TASK-047
				}
				// TASK-049: record decision for dry-run-file.
				res.decisions = append(res.decisions, dryRunRecord{
					Market: truncate(d.Market.Question, 80),
					Side:   d.Side,
					OurP:   d.OurProbability,
					Edge:   d.Edge,
					Size:   d.SizeUSDC,
					Reason: d.Reason,
				})
				// TASK-199: accumulate confidence for cycle stats.
				if ff != nil {
					res.confSum += ff.Confidence
					res.confCount++
				}
				// Accumulate estimated dry-run P&L (edge × size, in cents).
				pnlCents := int64(d.Edge * d.SizeUSDC * 100)
				sess.dryRunPnL.Add(pnlCents)
			}
		}

		if placed == 0 {
			slog.Info("no bets placed (no sufficient edge)")
		} else {
			slog.Info("cycle done", "bets_placed", placed)
		}

		// TASK-046: webhook cycle_complete event.
		if wErr := notifier.WebhookCycleComplete(placed, len(scoredList)); wErr != nil {
			slog.Warn("webhook notify failed", "event", "cycle_complete", "err", wErr)
		}

		// TASK-131: update auto-blacklist for each (city, signal) pair that was
		// evaluated this cycle. If a pair has accumulated too many losses it gets
		// suppressed automatically.
		if cfg.AutoBlacklistMinBets > 0 {
			abCfg := markets.AutoBlacklistCfg{
				MinBets:           cfg.AutoBlacklistMinBets,
				LossThresholdUSDC: cfg.AutoBlacklistLossUSDC,
				BlacklistDays:     cfg.AutoBlacklistDays,
			}
			seenPairs := make(map[string]bool)
			for _, sc := range scoredList {
				if sc.m.City == "" || sc.m.Signal == "" {
					continue
				}
				key := sc.m.City + "/" + sc.m.Signal
				if seenPairs[key] {
					continue
				}
				seenPairs[key] = true
				// Reload fresh history to include bets placed this cycle.
				if freshHistory, err := calibration.LoadHistory(cfg.DataRoot); err == nil {
					// Convert to AutoBetRecord to avoid import cycle.
					autoBets := make([]markets.AutoBetRecord, len(freshHistory))
					for i, r := range freshHistory {
						autoBets[i] = markets.AutoBetRecord{
							City:           r.City,
							Signal:         r.Signal,
							Outcome:        r.Outcome,
							SizeUSDC:       r.SizeUSDC,
							OurProbability: r.OurProbability,
							MarketPrice:    r.MarketPrice,
						}
					}
					_ = markets.AutoBlacklistCheck(autoBets, sc.m.City, sc.m.Signal, cfg.DataRoot, abCfg)
				}
			}
		}

		// TASK-132: rolling win-rate alert — check after every cycle in case recent
		// resolved updates have pushed the rolling rate below the threshold.
		if cfg.RollingWinRateWindow > 0 {
			if freshHistory, err := calibration.LoadHistory(cfg.DataRoot); err == nil {
				if alert, msg := calibration.WinRateAlert(freshHistory, cfg.RollingWinRateWindow, cfg.RollingWinRateThreshold); alert {
					slog.Warn("rolling win rate alert", "message", msg)
					_ = notifier.NotifyError("rolling_winrate", fmt.Errorf("⚠️ %s", msg))
				}
			}
		}

		res.placed = placed
		return res
	}

	// TASK-049: writeDryRunFile persists cycle results to a JSON file after each
	// run (in both dry-run and live modes). It overwrites the file each cycle.
	writeDryRunFile := func(result cycleResult) {
		if *dryRunFile == "" {
			return
		}
		out := dryRunOutput{
			Timestamp:        time.Now().UTC().Format(time.RFC3339),
			Cycle:            sess.cycleCount.Load(),
			MarketsEvaluated: result.marketsFound,
			BetsRecommended:  result.placed,
			Decisions:        result.decisions,
		}
		if out.Decisions == nil {
			out.Decisions = []dryRunRecord{} // always emit array, not null
		}
		data, err := json.MarshalIndent(out, "", "  ")
		if err != nil {
			slog.Warn("dry-run-file: marshal failed", "err", err)
			return
		}
		if err := os.WriteFile(*dryRunFile, data, 0o644); err != nil {
			slog.Warn("dry-run-file: write failed", "path", *dryRunFile, "err", err)
			return
		}
		slog.Info("dry-run-file written", "path", *dryRunFile, "bets", result.placed)
	}

	// TASK-199: recordCycleStat appends per-cycle performance data to data/cycles.csv.
	recordCycleStat := func(res cycleResult, startT time.Time) {
		avgEdge := 0.0
		if len(res.decisions) > 0 {
			for _, dr := range res.decisions {
				avgEdge += dr.Edge
			}
			avgEdge /= float64(len(res.decisions))
		}
		avgConf := 0.0
		if res.confCount > 0 {
			avgConf = res.confSum / float64(res.confCount)
		}
		stat := calibration.CycleStat{
			Timestamp:        startT.UTC(),
			DurationMs:       time.Since(startT).Milliseconds(),
			MarketsEvaluated: res.marketsFound,
			BetsPlaced:       res.placed,
			AvgEdge:          avgEdge,
			AvgConfidence:    avgConf,
		}
		if err := calibration.AppendCycleStat(stat, cfg.DataRoot); err != nil {
			slog.Warn("cycle stats: append failed", "err", err)
		}
	}

	cycleStart := time.Now()
	result := run()
	recordCycleStat(result, cycleStart)
	writeDryRunFile(result)
	metrics.UpdateCycle(result.placed) // TASK-051: update /healthz state

	// TASK-113: record daily return snapshot and log Sharpe ratio.
	{
		startBankroll := calibration.LoadBankroll(cfg.DataRoot)
		_ = calibration.RecordDailyReturn(startBankroll, startBankroll, cfg.DataRoot)
		calibration.LogSharpe(cfg.DataRoot)
		if alert := calibration.SharpeAlertMessage(cfg.DataRoot); alert != "" {
			_ = notifier.NotifyError("sharpe alert", fmt.Errorf("%s", alert))
		}
	}

	if cfg.LoopSec > 0 {
		baseInterval := time.Duration(cfg.LoopSec) * time.Second
		slog.Info("loop mode (adaptive)", "base_interval", baseInterval)

		// TASK-051: inform /healthz about the configured loop interval so it can
		// compute degraded threshold (last_cycle_at > 2×loop_sec).
		metrics.SetLoopSec(cfg.LoopSec)

		// Start the auto-resolver goroutine: checks resolved markets every hour.
		// TASK-139: pass a callback that checks for losing streaks after each resolve cycle.
		calibration.StartResolver(cfg.DataRoot, ctx, func(dataRoot string) {
			records, err := calibration.LoadHistory(dataRoot)
			if err != nil {
				return
			}
			if alert, msg := calibration.StreakAlert(records, 4); alert {
				slog.Warn("streak alert", "message", msg)
				_ = notifier.NotifyError("streak alert", fmt.Errorf("%s", msg))
			}
			// TASK-147: check for Brier score drift after each resolve cycle.
			if alert, msg := calibration.DriftAlert(records, 14, 30, 0.15); alert {
				slog.Warn("calibration drift", "message", msg)
				_ = notifier.NotifyError("calibration drift", fmt.Errorf("%s", msg))
			}
			// TASK-181: keep the isotonic calibrator up to date after each resolve.
			if _, err := calibration.UpdateAndSaveIsotonic(dataRoot, records); err != nil {
				slog.Warn("isotonic calibrator save failed", "err", err)
			}
		})

		// TASK-111: start Telegram command poller (/status /positions /next /pause /resume).
		notifier.StartCommandPoller(ctx, notifier.BotConfig{
			DataRoot:         cfg.DataRoot,
			Bankroll:         calibration.LoadBankroll(cfg.DataRoot),
			MinEdge:          cfg.MinEdge,
			MaxBet:           cfg.MaxBet,
			StartTime:        sess.startTime,
			DryRun:           dryRun,
			KellyFraction:    cfg.KellyFraction,
			MaxKellyFraction: cfg.MaxKellyFraction,
			LoopSec:          cfg.LoopSec,
			ProtocolFeeRate:  cfg.ProtocolFeeRate,
		})

		// TASK-200: watchdog goroutine — alert if no successful cycle for > 2×loop_sec.
		go func() {
			watchdogTick := time.NewTicker(time.Duration(cfg.LoopSec) * time.Second)
			defer watchdogTick.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-watchdogTick.C:
					last := metrics.LastCycleAt()
					if last.IsZero() {
						continue // no cycle yet
					}
					threshold := time.Duration(cfg.LoopSec*2) * time.Second
					if time.Since(last) > threshold {
						msg := fmt.Sprintf("⚠️ Watchdog: no cycle in %.0fs (expected ≤%ds)",
							time.Since(last).Seconds(), cfg.LoopSec*2)
						slog.Warn("watchdog: cycle overdue", "since_last", time.Since(last))
						_ = notifier.NotifyError("watchdog", fmt.Errorf("%s", msg))
					}
				}
			}
		}()

		// TASK-047: adaptive loop interval.
		// nextInterval computes the delay before the next cycle based on results.
		adaptiveInterval := func(res cycleResult) time.Duration {
			const (
				highEdgeInterval  = 5 * time.Minute    // found hot market — check again soon
				thinLiqInterval   = 30 * time.Minute   // all markets illiquid — no rush
				maxBackoff        = 60 * time.Minute   // backoff ceiling
			)
			switch {
			case res.highEdgeBet:
				slog.Info("adaptive loop: high-edge bet found, next cycle in 5 min")
				return highEdgeInterval
			case res.thinLiquidityOnly && res.marketsFound > 0:
				slog.Info("adaptive loop: thin liquidity only, waiting 30 min")
				return thinLiqInterval
			case res.placed == 0 && res.marketsFound > 0:
				// No bets found — apply exponential backoff, capped.
				next := time.Duration(float64(baseInterval) * 1.5)
				if next > maxBackoff {
					next = maxBackoff
				}
				slog.Info("adaptive loop: nothing found, backing off", "next", next)
				return next
			default:
				// Found markets and placed bets — reset to base interval.
				slog.Info("adaptive loop: normal cycle, base interval", "next", baseInterval)
				return baseInterval
			}
		}

		lastDigest := time.Time{}
		timer := time.NewTimer(baseInterval)
		defer timer.Stop()

	loop:
		for {
			select {
			case <-ctx.Done():
				slog.Info("shutdown signal received — stopping loop")
				break loop

			case <-timer.C:
				loopCycleStart := time.Now()
				loopResult := run()
				recordCycleStat(loopResult, loopCycleStart) // TASK-199
				writeDryRunFile(loopResult)                  // TASK-049
				metrics.UpdateCycle(loopResult.placed)       // TASK-051: update /healthz state

				// TASK-113: record daily return and emit Sharpe log / alert.
				{
					currentBankroll := calibration.LoadBankroll(cfg.DataRoot)
					_ = calibration.RecordDailyReturn(currentBankroll, currentBankroll, cfg.DataRoot)
					calibration.LogSharpe(cfg.DataRoot)
					if alert := calibration.SharpeAlertMessage(cfg.DataRoot); alert != "" {
						_ = notifier.NotifyError("sharpe alert", fmt.Errorf("%s", alert))
					}
				}

				// TASK-075: append market opportunity heatmap CSV for today.
				if hmRows, hmErr := strategy.LoadTodayHeatmap(cfg.DataRoot); hmErr == nil && len(hmRows) == 0 {
					// File doesn't exist yet — LoadTodayHeatmap returns nil slice, heatmap
					// is written inside EvaluateFused via the prediction log. We load and
					// re-export from today's prediction JSONL instead.
					_ = exportHeatmapFromPredictions(cfg.DataRoot)
				}

				// Send daily digest at ~09:00 UTC
				now := time.Now().UTC()
				if now.Hour() == 9 && now.Sub(lastDigest) > 23*time.Hour {
					if err := notifier.DailyDigest(cfg.DataRoot); err != nil {
						slog.Warn("daily digest failed", "err", err)
					} else {
						lastDigest = now
					}
				}

				// TASK-070: weekly digest — tries every cycle, no-ops if sent within 7 days.
				if err := notifier.WeeklyDigest(cfg.DataRoot); err != nil {
					slog.Warn("weekly digest failed", "err", err)
				}

				// Schedule next cycle with adaptive interval.
				next := adaptiveInterval(loopResult)
				timer.Reset(next)

			// TASK-073: config hot-reload on SIGHUP.
			// Reloads config.yaml and updates the cfg pointer captured by the run closure.
			// In-flight runs are not affected — the new config takes effect on the next cycle.
			// CLI flag overrides (--loop, --metrics-port) are preserved.
			case <-sighupCh:
				slog.Info("SIGHUP received — reloading config", "path", cfgPath)
				newCfg, loadErr := config.Load(cfgPath)
				if loadErr != nil {
					slog.Warn("config reload failed, keeping current config", "err", loadErr)
					break
				}
				// Preserve CLI-flag overrides that were applied at startup.
				if *loopFlag > 0 {
					newCfg.LoopSec = *loopFlag
				}
				if *metricsPortFlag >= 0 {
					newCfg.MetricsPort = *metricsPortFlag
				}
				// Swap config (run() closure captures cfg by variable reference —
				// next cycle automatically picks up the updated pointer).
				*cfg = *newCfg
				slog.Info("config reloaded",
					"min_edge", cfg.MinEdge,
					"max_bet", cfg.MaxBet,
					"cities", cfg.Cities,
					"loop_sec", cfg.LoopSec,
				)
			}
		}
	}

	// ── Graceful shutdown ──────────────────────────────────────────────────

	summary := sess.summary(dryRun)
	slog.Info("session summary", "summary", summary)

	// Shut down the metrics HTTP server.
	if metricsSrv != nil {
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := metricsSrv.Shutdown(shutCtx); err != nil {
			slog.Warn("metrics server shutdown error", "err", err)
		} else {
			slog.Info("metrics server stopped")
		}
	}

	// Notify Telegram about the shutdown.
	if err := notifier.NotifyStop(summary); err != nil {
		slog.Warn("telegram stop notification failed", "err", err)
	}
}

// preOrderRefresh fetches the latest market prices immediately before placing
// a real order (TASK-036).
//
//   - On success it returns an updated Decision with fresh prices/edge and skip=false.
//   - If the refreshed edge drops below minEdge it logs a warning and returns
//     nil, skip=true — the caller should skip this bet.
//   - On API failure (timeout, 4xx, etc.) it logs a warning but returns nil,
//     skip=false so the bot continues with the original (possibly stale) price.
func preOrderRefresh(d *strategy.Decision, minEdge float64) (updated *strategy.Decision, skip bool) {
	freshMkt, refreshed, err := markets.RefreshPrices(d.Market)
	if err != nil {
		slog.Warn("price refresh failed, using stale price",
			"conditionID", d.Market.ConditionID, "err", err)
		return nil, false // proceed with original price
	}
	if !refreshed {
		return nil, false
	}

	// Compute new edge for our side.
	var oldPrice, newPrice, newEdge float64
	switch d.Side {
	case "YES":
		oldPrice = d.Market.YesPrice
		newPrice = freshMkt.YesPrice
		newEdge = d.OurProbability - newPrice
	case "NO":
		oldPrice = d.Market.NoPrice
		newPrice = freshMkt.NoPrice
		newEdge = (1 - d.OurProbability) - newPrice
	default:
		return nil, false
	}

	if newEdge < minEdge {
		slog.Info("price refresh: edge reduced, skipping bet",
			"side", d.Side,
			"old_price", fmt.Sprintf("%.4f", oldPrice),
			"new_price", fmt.Sprintf("%.4f", newPrice),
			"old_edge", fmt.Sprintf("%+.4f", d.Edge),
			"new_edge", fmt.Sprintf("%+.4f", newEdge),
			"conditionID", d.Market.ConditionID,
		)
		return nil, true
	}

	if oldPrice != newPrice {
		slog.Info("price refresh: price moved, proceeding",
			"side", d.Side,
			"old_price", fmt.Sprintf("%.4f", oldPrice),
			"new_price", fmt.Sprintf("%.4f", newPrice),
			"new_edge", fmt.Sprintf("%+.4f", newEdge),
			"conditionID", d.Market.ConditionID,
		)
	}

	// Return updated decision with fresh prices.
	newDecision := *d
	newDecision.Market = freshMkt
	newDecision.MarketPrice = newPrice
	newDecision.Edge = newEdge
	return &newDecision, false
}

// placeBet submits an order to Polymarket CLOB using EIP-712 signing.
func placeBet(d *strategy.Decision) error {
	orderID, err := polymarket.PlaceBet(d)
	if err != nil {
		return fmt.Errorf("placeBet: %w", err)
	}
	slog.Info("order submitted", "orderID", orderID, "token", d.TokenID, "side", d.Side)
	return nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// exportHeatmapFromPredictions loads today's prediction JSONL and appends
// all records to the daily heatmap CSV (TASK-075).
// Called once per loop cycle to ensure the heatmap stays current.
func exportHeatmapFromPredictions(dataRoot string) error {
	today := time.Now().UTC().Format("2006-01-02")
	recs, err := strategy.LoadPredictions(today, dataRoot)
	if err != nil || len(recs) == 0 {
		return err
	}
	rows := make([]strategy.HeatmapRow, 0, len(recs))
	for _, r := range recs {
		rows = append(rows, strategy.HeatmapRowFromPrediction(r))
	}
	return strategy.AppendHeatmap(rows, dataRoot)
}

// applyPlattCalibration adjusts d.OurProbability with the fitted Platt calibrator
// and recomputes Kelly sizing. Returns nil if calibrated edge < minEdge.
// Returns d unchanged when calibrator is not yet active (< 20 resolved bets).
func applyPlattCalibration(
	d *strategy.Decision,
	pc *calibration.PlattCalibrator,
	minEdge float64,
	bankroll float64,
) *strategy.Decision {
	if d == nil || pc == nil || !pc.IsActive() {
		return d
	}

	rawP := d.OurProbability
	calP := pc.Calibrate(rawP)
	if math.Abs(calP-rawP) < 1e-6 {
		return d
	}

	price := d.MarketPrice
	if price <= 0 || price >= 1 {
		return d
	}
	newEdge := calP - price

	if newEdge < minEdge {
		slog.Debug("platt: edge below threshold after calibration — skipping",
			"city", d.Market.City, "signal", d.Market.Signal,
			"raw_p", fmt.Sprintf("%.3f", rawP),
			"cal_p", fmt.Sprintf("%.3f", calP),
			"edge", fmt.Sprintf("%.3f", newEdge),
		)
		return nil
	}

	out := *d
	out.OurProbability = calP
	out.Edge = newEdge
	out.Reason = fmt.Sprintf("%s [platt:%.3f→%.3f]", d.Reason, rawP, calP)
	slog.Debug("platt calibration applied",
		"city", d.Market.City, "signal", d.Market.Signal,
		"raw_p", fmt.Sprintf("%.3f", rawP), "cal_p", fmt.Sprintf("%.3f", calP),
	)
	return &out
}

// applyIsotonicCalibration adjusts d.OurProbability using the fitted isotonic
// regression calibrator and recomputes Kelly sizing. (TASK-181)
// Returns nil if calibrated edge < minEdge. Returns d unchanged when
// the calibrator is not active.
func applyIsotonicCalibration(
	d *strategy.Decision,
	ic *calibration.IsotonicCalibrator,
	minEdge float64,
	bankroll float64,
) *strategy.Decision {
	if d == nil || ic == nil || !ic.IsActive() {
		return d
	}
	rawP := d.OurProbability
	calP := ic.Predict(rawP)
	if math.Abs(calP-rawP) < 1e-6 {
		return d
	}
	price := d.MarketPrice
	if price <= 0 || price >= 1 {
		return d
	}
	newEdge := calP - price
	if newEdge < minEdge {
		slog.Debug("isotonic: edge below threshold after calibration — skipping",
			"city", d.Market.City, "signal", d.Market.Signal,
			"raw_p", fmt.Sprintf("%.3f", rawP),
			"cal_p", fmt.Sprintf("%.3f", calP),
			"edge", fmt.Sprintf("%.3f", newEdge),
		)
		return nil
	}
	out := *d
	out.OurProbability = calP
	out.Edge = newEdge
	out.Reason = fmt.Sprintf("%s [isotonic:%.3f→%.3f]", d.Reason, rawP, calP)
	slog.Debug("isotonic calibration applied",
		"city", d.Market.City, "signal", d.Market.Signal,
		"raw_p", fmt.Sprintf("%.3f", rawP), "cal_p", fmt.Sprintf("%.3f", calP),
	)
	return &out
}

// runValidate performs pre-flight checks on config and external APIs, prints a
// status table, and exits 0 (all ok) or 1 (one or more failures). Designed for
// use as a Docker HEALTHCHECK: `./bot --validate` (TASK-143).
//
// Checks performed:
//  1. config.Validate — errors are fatal, warnings are informational.
//  2. Open-Meteo HTTP GET for the first configured city (3 s timeout).
//  3. Gamma API GET /markets?limit=1 (3 s timeout).
//  4. Telegram GET /getMe — only when TelegramBotToken is set.
//  5. Private key parse — only when PolyPrivateKey is set (no network call).
func runValidate(cfg *config.Config) {
	type check struct {
		name string
		ok   bool
		msg  string
	}
	var checks []check

	pass := func(name, msg string) { checks = append(checks, check{name, true, msg}) }
	fail := func(name, msg string) { checks = append(checks, check{name, false, msg}) }

	// ── 1. Config validation ─────────────────────────────────────────────────
	vr := config.Validate(cfg)
	if len(vr.Errors) > 0 {
		fail("config", strings.Join(vr.Errors, "; "))
	} else if len(vr.Warnings) > 0 {
		pass("config", "warnings: "+strings.Join(vr.Warnings, "; "))
	} else {
		pass("config", fmt.Sprintf("ok (cities=%d min_edge=%.3f)", len(cfg.Cities), cfg.MinEdge))
	}

	// ── 2. Open-Meteo connectivity ───────────────────────────────────────────
	{
		city := "new_york"
		if len(cfg.Cities) > 0 {
			city = cfg.Cities[0]
		}
		client := &http.Client{Timeout: 3 * time.Second}
		c, ok := weather.Cities[city]
		if !ok {
			fail("openmeteo", fmt.Sprintf("city %q not registered", city))
		} else {
			url := fmt.Sprintf(
				"https://api.open-meteo.com/v1/forecast?latitude=%.4f&longitude=%.4f"+
					"&daily=temperature_2m_max&forecast_days=1&timezone=UTC",
				c.Lat, c.Lon,
			)
			resp, err := client.Get(url)
			if err != nil {
				fail("openmeteo", err.Error())
			} else {
				resp.Body.Close()
				if resp.StatusCode == 200 {
					pass("openmeteo", fmt.Sprintf("HTTP %d for city=%s", resp.StatusCode, city))
				} else {
					fail("openmeteo", fmt.Sprintf("HTTP %d for city=%s", resp.StatusCode, city))
				}
			}
		}
	}

	// ── 3. Gamma API connectivity ────────────────────────────────────────────
	{
		client := &http.Client{Timeout: 3 * time.Second}
		req, _ := http.NewRequest("GET", "https://gamma-api.polymarket.com/markets?limit=1", nil)
		req.Header.Set("User-Agent", "polymarket-weather-bot/validate")
		resp, err := client.Do(req)
		if err != nil {
			fail("gamma_api", err.Error())
		} else {
			resp.Body.Close()
			if resp.StatusCode == 200 {
				pass("gamma_api", fmt.Sprintf("HTTP %d", resp.StatusCode))
			} else {
				fail("gamma_api", fmt.Sprintf("HTTP %d", resp.StatusCode))
			}
		}
	}

	// ── 4. Telegram bot token ────────────────────────────────────────────────
	if cfg.TelegramBotToken != "" {
		client := &http.Client{Timeout: 3 * time.Second}
		url := fmt.Sprintf("https://api.telegram.org/bot%s/getMe", cfg.TelegramBotToken)
		resp, err := client.Get(url)
		if err != nil {
			fail("telegram", err.Error())
		} else {
			resp.Body.Close()
			if resp.StatusCode == 200 {
				pass("telegram", "bot token valid")
			} else {
				fail("telegram", fmt.Sprintf("HTTP %d — token may be invalid", resp.StatusCode))
			}
		}
	} else {
		pass("telegram", "skipped (TELEGRAM_BOT_TOKEN not set)")
	}

	// ── 5. Private key parse ─────────────────────────────────────────────────
	if cfg.PolyPrivateKey != "" {
		key := strings.TrimPrefix(cfg.PolyPrivateKey, "0x")
		if len(key) != 64 {
			fail("private_key", fmt.Sprintf("unexpected length %d (expected 64 hex chars)", len(key)))
		} else {
			pass("private_key", "format ok (64 hex chars)")
		}
	} else {
		pass("private_key", "skipped (POLYMARKET_PRIVATE_KEY not set)")
	}

	// ── Print results ────────────────────────────────────────────────────────
	allOK := true
	for _, c := range checks {
		icon := "[OK]  "
		if !c.ok {
			icon = "[FAIL]"
			allOK = false
		}
		fmt.Printf("%s %s: %s\n", icon, c.name, c.msg)
	}

	if allOK {
		fmt.Println("\nAll checks passed.")
		os.Exit(0)
	} else {
		fmt.Println("\nOne or more checks failed.")
		os.Exit(1)
	}
}
