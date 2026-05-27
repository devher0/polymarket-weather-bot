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
	"net/http"
	"os"
	"os/signal"
	"sort"
	"strings"
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
	"github.com/devher0/polymarket-weather-bot/internal/risk"
	"github.com/devher0/polymarket-weather-bot/internal/strategy"
	"github.com/devher0/polymarket-weather-bot/internal/weather"
)

// maxForecastAge converts MaxForecastAgeHours from config to a Duration.
// Returns 0 (no limit) when cfg.MaxForecastAgeHours is 0.
func maxForecastAge(cfg *config.Config) time.Duration {
	if cfg.MaxForecastAgeHours <= 0 {
		return 0
	}
	return time.Duration(cfg.MaxForecastAgeHours * float64(time.Hour))
}

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
	flag.Parse()

	// Set up graceful shutdown context — cancelled on SIGTERM or SIGINT.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

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

	// TASK-072: weak signal alert — warn if any signal type has <40% win rate (≥10 samples).
	if startupHistory, err := calibration.LoadHistory(cfg.DataRoot); err == nil {
		sigBreakdown := calibration.SignalBreakdown(startupHistory)
		weakSignals := calibration.WeakSignalAlert(sigBreakdown, 10, 40.0)
		for _, warn := range weakSignals {
			slog.Warn("weak signal detected: "+warn, "action", "consider raising min_edge")
			_ = notifier.NotifyError("weak signal", fmt.Errorf("%s", warn))
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

	// cycleResult holds summary data from one run() call used for adaptive
	// loop scheduling (TASK-047) and dry-run-file output (TASK-049).
	type cycleResult struct {
		placed            int
		marketsFound      int
		highEdgeBet       bool           // at least one bet with edge > 0.15
		thinLiquidityOnly bool           // all candidate markets had thin liquidity
		decisions         []dryRunRecord // populated for dry-run-file (TASK-049)
	}

	run := func() cycleResult {
		var res cycleResult
		sess.cycleCount.Add(1)
		slog.Info("=== cycle start", "time", time.Now().Format(time.RFC3339))

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

		// 1. Discover weather markets FIRST so we know which cities are active.
		// TASK-043: only fetch fresh forecasts for cities that have live markets.
		mkt, err := markets.GetWeatherMarkets()
		if err != nil {
			slog.Error("markets fetch failed", "err", err)
			return res
		}
		slog.Info("weather markets found", "count", len(mkt))

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

		// 4. Enrich markets with liquidity data (order book depth).
		markets.EnrichWithLiquidity(mkt)

		// 4b. TASK-030: score and sort markets before evaluation.
		// This ensures the highest-value opportunities are evaluated first,
		// preventing the daily cap from being consumed by marginal markets.
		type scored struct {
			m     markets.Market
			ff    *collectors.FusedForecast
			score float64
		}
		scoredList := make([]scored, 0, len(mkt))

		staleThreshold := maxForecastAge(cfg)

		for _, m := range mkt {
			if m.City != "" && !configuredCities[m.City] {
				continue
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

			// TASK-029: skip stale forecasts.
			if ff != nil && staleThreshold > 0 && !ff.FetchedAt.IsZero() {
				age := time.Since(ff.FetchedAt)
				if age > staleThreshold {
					slog.Info("stale forecast, skipping market",
						"city", m.City,
						"age", age.Round(time.Minute).String(),
						"max_age", staleThreshold.String(),
					)
					ff = nil
				}
			}

			sc := strategy.ScoreMarket(m, ff)
			scoredList = append(scoredList, scored{m, ff, sc})
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

			var d *strategy.Decision
			if ff != nil {
				d = strategy.EvaluateFused(m, ff, effectiveBankroll, adaptiveMinEdge, cfg.MaxBet, cfg.DataRoot)
			}
			if d == nil {
				d = strategy.Evaluate(m, legacyForecasts, effectiveBankroll, cfg.MinEdge, cfg.MaxBet)
			}
			if d == nil {
				continue
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

	result := run()
	writeDryRunFile(result)
	metrics.UpdateCycle(result.placed) // TASK-051: update /healthz state

	if cfg.LoopSec > 0 {
		baseInterval := time.Duration(cfg.LoopSec) * time.Second
		slog.Info("loop mode (adaptive)", "base_interval", baseInterval)

		// TASK-051: inform /healthz about the configured loop interval so it can
		// compute degraded threshold (last_cycle_at > 2×loop_sec).
		metrics.SetLoopSec(cfg.LoopSec)

		// Start the auto-resolver goroutine: checks resolved markets every hour.
		calibration.StartResolver(cfg.DataRoot, ctx)

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
				loopResult := run()
				writeDryRunFile(loopResult) // TASK-049
				metrics.UpdateCycle(loopResult.placed) // TASK-051: update /healthz state

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
