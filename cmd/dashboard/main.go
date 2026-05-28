// cmd/dashboard — CLI dashboard for the Polymarket Weather Bot.
//
// Usage:
//
//	go run ./cmd/dashboard positions                          — show open positions (from bets_history.csv)
//	go run ./cmd/dashboard pnl                               — P&L summary from data/bets_history.csv
//	go run ./cmd/dashboard next                              — top-5 bet candidates right now
//	go run ./cmd/dashboard explain                           — full decision audit table for all current markets
//	go run ./cmd/dashboard report                            — export market evaluation snapshot to JSON (stdout)
//	go run ./cmd/dashboard report --output=r.json            — write to file instead of stdout
//	go run ./cmd/dashboard export-predictions                — export today's prediction log to CSV (stdout)
//	go run ./cmd/dashboard export-predictions --date=2026-05-27 --output=predictions.csv — specific date, to file
//	go run ./cmd/dashboard drift                             — forecast stability table (TASK-126)
//	go run ./cmd/dashboard timing                            — hourly win-rate and timing multiplier table (TASK-133)
//	go run ./cmd/dashboard all                               — run all sub-commands
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sort"
	"time"

	"github.com/jedib0t/go-pretty/v6/table"
	"github.com/jedib0t/go-pretty/v6/text"
	"github.com/joho/godotenv"

	"github.com/devher0/polymarket-weather-bot/internal/calibration"
	"github.com/devher0/polymarket-weather-bot/internal/collectors"
	"github.com/devher0/polymarket-weather-bot/internal/markets"
	"github.com/devher0/polymarket-weather-bot/internal/strategy"
	"github.com/devher0/polymarket-weather-bot/internal/weather"
)

// ── styling helpers ────────────────────────────────────────────────────────

var (
	styleHeader = text.Colors{text.Bold, text.FgCyan}
	styleWin    = text.Colors{text.FgGreen}
	styleLoss   = text.Colors{text.FgRed}
	styleNeutral = text.Colors{text.FgYellow}
)

func header(title string) {
	fmt.Printf("\n%s\n", styleHeader.Sprint("  "+title))
	fmt.Println("  " + repeatStr("─", 60))
}

func newTable() table.Writer {
	t := table.NewWriter()
	t.SetOutputMirror(os.Stdout)
	t.SetStyle(table.StyleLight)
	t.Style().Options.SeparateRows = false
	t.Style().Title.Align = text.AlignCenter
	return t
}

func repeatStr(s string, n int) string {
	out := ""
	for i := 0; i < n; i++ {
		out += s
	}
	return out
}

// ── positions ──────────────────────────────────────────────────────────────

func cmdPositions(dataRoot string) {
	header("📋 OPEN POSITIONS")

	records, err := calibration.LoadHistory(dataRoot)
	if err != nil {
		fmt.Fprintf(os.Stderr, "  error: %v\n", err)
		return
	}

	open := make([]calibration.BetRecord, 0)
	for _, r := range records {
		if r.Outcome == nil {
			open = append(open, r)
		}
	}

	if len(open) == 0 {
		fmt.Println("  No open positions (all bets resolved or no history yet).")
		return
	}

	fmt.Println("  Fetching live prices...")
	positions := calibration.FetchUnrealizedPnL(records)

	// Build map conditionID -> position for quick lookup.
	posMap := make(map[string]calibration.UnrealizedPosition, len(positions))
	for _, p := range positions {
		posMap[p.ConditionID] = p
	}

	t := newTable()
	t.AppendHeader(table.Row{
		"Condition ID", "Side", "Entry P", "Current P", "Size", "Unreal PnL", "Opened",
	})

	var totalUnreal float64
	for _, r := range open {
		age := time.Since(r.Timestamp).Round(time.Hour)

		curStr := "N/A"
		pnlStr := "N/A"
		if pos, ok := posMap[r.ConditionID]; ok && pos.FetchError == "" {
			curStr = fmt.Sprintf("%.3f", pos.CurrentPrice)
			if pos.UnrealizedPnL >= 0 {
				pnlStr = styleWin.Sprintf("+$%.2f", pos.UnrealizedPnL)
			} else {
				pnlStr = styleLoss.Sprintf("-$%.2f", -pos.UnrealizedPnL)
			}
			totalUnreal += pos.UnrealizedPnL
		}

		t.AppendRow(table.Row{
			truncate(r.ConditionID, 16),
			r.Side,
			fmt.Sprintf("%.3f", r.MarketPrice),
			curStr,
			fmt.Sprintf("$%.2f", r.SizeUSDC),
			pnlStr,
			fmt.Sprintf("%s ago", age),
		})
	}

	t.Render()
	totalStr := fmt.Sprintf("+$%.2f", totalUnreal)
	if totalUnreal < 0 {
		totalStr = fmt.Sprintf("-$%.2f", -totalUnreal)
	}
	fmt.Printf("\n  Total open: %d position(s)  |  Unrealized PnL: %s\n", len(open), totalStr)
}

// ── PnL summary ────────────────────────────────────────────────────────────

func cmdPnL(dataRoot string) {
	header("💰 P&L SUMMARY")

	records, err := calibration.LoadHistory(dataRoot)
	if err != nil {
		fmt.Fprintf(os.Stderr, "  error: %v\n", err)
		return
	}

	resolved := make([]calibration.BetRecord, 0)
	for _, r := range records {
		if r.Outcome != nil {
			resolved = append(resolved, r)
		}
	}

	if len(resolved) == 0 {
		fmt.Println("  No resolved bets yet.")
		calibration.PrintBrierScore(dataRoot)
		return
	}

	// Sort by timestamp
	sort.Slice(resolved, func(i, j int) bool {
		return resolved[i].Timestamp.Before(resolved[j].Timestamp)
	})

	wins := 0
	totalPnL := 0.0
	totalEdge := 0.0

	t := newTable()
	t.AppendHeader(table.Row{
		"#", "Date", "Side", "Our P", "Mkt P", "Edge", "Size", "Result", "PnL",
	})

	for i, r := range resolved {
		won := *r.Outcome
		if won {
			wins++
		}

		edge := r.OurProbability - r.MarketPrice
		totalEdge += edge

		odds := 1.0 / r.MarketPrice
		pnl := r.SizeUSDC * (odds - 1)
		if !won {
			pnl = -r.SizeUSDC
		}
		totalPnL += pnl

		result := styleWin.Sprint("WIN")
		pnlStr := styleWin.Sprintf("+$%.2f", pnl)
		if !won {
			result = styleLoss.Sprint("LOSS")
			pnlStr = styleLoss.Sprintf("-$%.2f", r.SizeUSDC)
		}

		t.AppendRow(table.Row{
			i + 1,
			r.Timestamp.Format("01-02"),
			r.Side,
			fmt.Sprintf("%.2f", r.OurProbability),
			fmt.Sprintf("%.2f", r.MarketPrice),
			fmt.Sprintf("%+.2f", edge),
			fmt.Sprintf("$%.2f", r.SizeUSDC),
			result,
			pnlStr,
		})
	}

	t.Render()

	pnlColor := styleWin
	if totalPnL < 0 {
		pnlColor = styleLoss
	}

	fmt.Printf("\n  Resolved bets : %d\n", len(resolved))
	fmt.Printf("  Win rate      : %.1f%%  (%d/%d)\n",
		float64(wins)/float64(len(resolved))*100, wins, len(resolved))
	fmt.Printf("  Avg edge      : %+.2f%%\n", totalEdge/float64(len(resolved))*100)
	fmt.Printf("  Total P&L     : %s\n", pnlColor.Sprintf("%+.2f USDC", totalPnL))

	// Brier score
	score, count, _ := calibration.BrierScore(records)
	if count > 0 {
		fmt.Printf("  Brier score   : %.4f (%d bets)\n", score, count)
	}

	// Per-city breakdown table
	cityMap := calibration.CityBreakdown(records)
	if len(cityMap) > 0 {
		header("🏙️  P&L BY CITY")
		tc := newTable()
		tc.AppendHeader(table.Row{"City", "Bets", "Wins", "Win %", "Brier"})

		type cityRow struct {
			name  string
			stats calibration.BreakdownStats
		}
		var cityRows []cityRow
		for k, v := range cityMap {
			cityRows = append(cityRows, cityRow{k, v})
		}
		// Sort by Brier ascending (best first)
		sort.Slice(cityRows, func(i, j int) bool {
			return cityRows[i].stats.BrierAvg() < cityRows[j].stats.BrierAvg()
		})
		for _, cr := range cityRows {
			brierStr := fmt.Sprintf("%.4f", cr.stats.BrierAvg())
			tc.AppendRow(table.Row{
				cr.name,
				cr.stats.Count,
				cr.stats.Wins,
				fmt.Sprintf("%.1f%%", cr.stats.WinRate()),
				brierStr,
			})
		}
		tc.Render()
	}

	// Per-signal breakdown table
	sigMap := calibration.SignalBreakdown(records)
	if len(sigMap) > 0 {
		header("📡 P&L BY SIGNAL")
		ts2 := newTable()
		ts2.AppendHeader(table.Row{"Signal", "Bets", "Wins", "Win %", "Brier"})

		type sigRow struct {
			name  string
			stats calibration.BreakdownStats
		}
		var sigRows []sigRow
		for k, v := range sigMap {
			sigRows = append(sigRows, sigRow{k, v})
		}
		sort.Slice(sigRows, func(i, j int) bool {
			return sigRows[i].stats.BrierAvg() < sigRows[j].stats.BrierAvg()
		})
		for _, sr := range sigRows {
			ts2.AppendRow(table.Row{
				sr.name,
				sr.stats.Count,
				sr.stats.Wins,
				fmt.Sprintf("%.1f%%", sr.stats.WinRate()),
				fmt.Sprintf("%.4f", sr.stats.BrierAvg()),
			})
		}
		ts2.Render()
	}
}

// ── next bets ──────────────────────────────────────────────────────────────

// betCandidate holds a decision plus source meta.
type betCandidate struct {
	Decision *strategy.Decision
	Source   string // "fused" or "openmeteo"
	City     string
}

func cmdNext(dataRoot string) {
	header("🎯 TOP-5 BET CANDIDATES (right now)")

	// Fetch fused forecasts (best effort)
	fmt.Print("  Fetching weather data…")
	fusedForecasts, _ := collectors.AggregateAll(dataRoot)
	fmt.Println(" done")

	// Also fetch plain OpenMeteo as fallback
	legacyForecasts := make(map[string][]weather.Forecast)
	for city := range weather.Cities {
		fc, err := weather.GetForecast(city, 3)
		if err == nil && len(fc) > 0 {
			legacyForecasts[city] = fc
		}
	}

	fmt.Print("  Fetching Polymarket markets…")
	mkt, err := markets.GetWeatherMarkets()
	if err != nil {
		fmt.Fprintf(os.Stderr, "\n  error fetching markets: %v\n", err)
		return
	}
	fmt.Printf(" found %d weather markets\n", len(mkt))

	bankroll := 1000.0
	minEdge := 0.05
	maxBet := 25.0

	var candidates []betCandidate
	for _, m := range mkt {
		var d *strategy.Decision

		if ff, ok := fusedForecasts[m.City]; ok {
			d = strategy.EvaluateFused(m, ff, bankroll, minEdge, maxBet, "")
			if d != nil {
				candidates = append(candidates, betCandidate{Decision: d, Source: "fused", City: m.City})
				continue
			}
		}

		d = strategy.Evaluate(m, legacyForecasts, bankroll, minEdge, maxBet)
		if d != nil {
			candidates = append(candidates, betCandidate{Decision: d, Source: "openmeteo", City: m.City})
		}
	}

	// Sort by edge descending
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].Decision.Edge > candidates[j].Decision.Edge
	})

	if len(candidates) == 0 {
		fmt.Println("\n  No qualifying bets found right now (edge < min threshold).")
		return
	}

	// Show top 5
	top := candidates
	if len(top) > 5 {
		top = top[:5]
	}

	t := newTable()
	t.AppendHeader(table.Row{
		"#", "City/Signal", "Side", "Our P", "Mkt P", "Edge", "Size", "Source", "Question",
	})

	for i, c := range top {
		d := c.Decision
		edgeStr := styleWin.Sprintf("%+.2f%%", d.Edge*100)
		t.AppendRow(table.Row{
			i + 1,
			fmt.Sprintf("%s/%s", c.City, d.Market.Signal),
			d.Side,
			fmt.Sprintf("%.2f", d.OurProbability),
			fmt.Sprintf("%.2f", d.MarketPrice),
			edgeStr,
			fmt.Sprintf("$%.2f", d.SizeUSDC),
			c.Source,
			truncate(d.Market.Question, 40),
		})
	}

	t.Render()
	fmt.Printf("\n  Total qualifying candidates: %d\n", len(candidates))
}

// ── forecast ───────────────────────────────────────────────────────────────

// cmdForecast fetches fused forecasts for all cities and renders a summary
// table with quality indicators. Rows with confidence < 0.4 are annotated.
func cmdForecast(dataRoot string) {
	header("🌤️  FUSED FORECASTS — ALL CITIES")

	fmt.Print("  Fetching weather data (all sources, parallel)…")
	fusedForecasts, err := collectors.AggregateAll(dataRoot)
	if err != nil && len(fusedForecasts) == 0 {
		fmt.Fprintf(os.Stderr, "\n  error: %v\n", err)
		return
	}
	fmt.Println(" done")

	if len(fusedForecasts) == 0 {
		fmt.Println("  No forecast data available.")
		return
	}

	// Sort city names for deterministic output.
	cities := make([]string, 0, len(fusedForecasts))
	for c := range fusedForecasts {
		cities = append(cities, c)
	}
	sort.Strings(cities)

	t := newTable()
	t.AppendHeader(table.Row{
		"City",
		"Date",
		"MaxT°C",
		"MinT°C",
		"Precip mm",
		"Rain %",
		"Wind km/h",
		"Ens.Unc °C",
		"Confidence",
		"Sources",
		"Age",
	})

	now := time.Now()
	for _, city := range cities {
		ff := fusedForecasts[city]

		age := now.Sub(ff.FetchedAt).Round(time.Second)
		ageStr := age.String()

		confStr := fmt.Sprintf("%.2f", ff.Confidence)
		note := ""
		if ff.Confidence < 0.4 {
			note = " (low conf)"
			confStr = styleNeutral.Sprint(confStr + note)
		} else if ff.Confidence >= 0.75 {
			confStr = styleWin.Sprint(confStr)
		}

		ensStr := "—"
		if ff.EnsembleUncertainty > 0 {
			ensStr = fmt.Sprintf("%.1f", ff.EnsembleUncertainty)
		}

		srcStr := ""
		for i, s := range ff.Sources {
			if i > 0 {
				srcStr += ","
			}
			srcStr += s
		}

		t.AppendRow(table.Row{
			city,
			ff.Forecast.Date,
			fmt.Sprintf("%.1f", ff.Forecast.MaxTempC),
			fmt.Sprintf("%.1f", ff.Forecast.MinTempC),
			fmt.Sprintf("%.1f", ff.Forecast.PrecipitationMM),
			fmt.Sprintf("%.0f%%", ff.Forecast.PrecipitationProbability),
			fmt.Sprintf("%.0f", ff.Forecast.WindSpeedKMH),
			ensStr,
			confStr,
			truncate(srcStr, 32),
			ageStr,
		})
	}

	t.Render()
	fmt.Printf("\n  Cities loaded: %d / %d\n", len(fusedForecasts), len(weather.Cities))

	// Legend
	fmt.Println()
	fmt.Println("  Legend: " +
		styleWin.Sprint("conf ≥ 0.75 = high") + "  " +
		styleNeutral.Sprint("conf < 0.40 = low") + "  " +
		"Ens.Unc = ensemble temperature stddev (°C)")
}

// ── main ───────────────────────────────────────────────────────────────────

func main() {
	_ = godotenv.Load()

	dataRoot := "."
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	cmd := os.Args[1]
	switch cmd {
	case "positions":
		cmdPositions(dataRoot)
	case "pnl":
		cmdPnL(dataRoot)
	case "next":
		cmdNext(dataRoot)
	case "forecast":
		cmdForecast(dataRoot)
	case "cache":
		cmdCacheStats(dataRoot)
	case "explain":
		cmdExplain(dataRoot)
	case "analysis":
		cmdAnalysis(dataRoot)
	case "report":
		// Parse --output flag from remaining args (os.Args[2:]).
		rFlags := flag.NewFlagSet("report", flag.ExitOnError)
		outputFile := rFlags.String("output", "", "Write JSON report to this file (default: stdout)")
		_ = rFlags.Parse(os.Args[2:])
		cmdReport(dataRoot, *outputFile)
	case "export-predictions":
		// Parse --date and --output flags.
		epFlags := flag.NewFlagSet("export-predictions", flag.ExitOnError)
		epDate := epFlags.String("date", "", "Date to export (default: today, format: 2006-01-02)")
		epOutput := epFlags.String("output", "", "Write CSV to this file (default: stdout)")
		_ = epFlags.Parse(os.Args[2:])
		cmdExportPredictions(dataRoot, *epDate, *epOutput)
	case "snapshot":
		// TASK-074: print calibration model snapshot.
		calibration.PrintSnapshot(dataRoot)
	case "heatmap":
		// TASK-075: show today's heatmap summary.
		cmdHeatmap(dataRoot)
	case "hourly":
		// TASK-078: show hourly forecast table for a city.
		city := ""
		if len(os.Args) >= 3 {
			city = os.Args[2]
		}
		cmdHourly(city)
	case "health":
		// TASK-081: show per-source data availability stats.
		cmdSourceHealth(dataRoot)
	case "ab-test":
		// TASK-112: A/B test comparison between Kelly fraction strategies.
		strategy.PrintABTest(dataRoot)
	case "drift":
		// TASK-126: show forecast stability drift table.
		cmdDrift(dataRoot)
	case "timing":
		// TASK-133: hourly win-rate and timing multiplier table.
		cmdTiming(dataRoot)
	case "freshness":
		// TASK-140: forecast freshness table.
		cmdFreshness(dataRoot)
	case "summary":
		// TASK-144: single-page health overview.
		cmdSummary(dataRoot)
	case "all":
		cmdPositions(dataRoot)
		cmdPnL(dataRoot)
		cmdForecast(dataRoot)
		cmdNext(dataRoot)
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %q\n\n", cmd)
		printUsage()
		os.Exit(1)
	}
	fmt.Println()
}

func printUsage() {
	fmt.Println("Usage: go run ./cmd/dashboard <command>")
	fmt.Println()
	fmt.Println("Commands:")
	fmt.Println("  positions                          Show open (unresolved) positions")
	fmt.Println("  pnl                               P&L history from data/bets_history.csv")
	fmt.Println("  next                              Top-5 bet candidates right now")
	fmt.Println("  forecast                          Fused weather forecast table for all cities")
	fmt.Println("  cache                             Show forecast cache status (age of cached data)")
	fmt.Println("  explain                           Full decision audit: why each market is BET or SKIP")
	fmt.Println("  analysis                          Per-city/signal breakdown of today's prediction log")
	fmt.Println("  report [--output=f]               Export market evaluation snapshot to JSON")
	fmt.Println("  export-predictions [--date=D]     Export prediction log to CSV (stdout or --output=f)")
	fmt.Println("  snapshot                          Print calibration model snapshot (TASK-074)")
	fmt.Println("  heatmap                           Show today's market opportunity heatmap summary (TASK-075)")
	fmt.Println("  hourly <city>                     Hourly weather table for city (today + tomorrow) (TASK-078)")
	fmt.Println("  health                            Per-source data availability stats (TASK-081)")
	fmt.Println("  timing                            Hourly win-rate and bet-size timing multiplier (TASK-133)")
	fmt.Println("  freshness                         Forecast freshness table: age/status per city (TASK-140)")
	fmt.Println("  summary                           Single-page health overview: bankroll, perf, streak, sources (TASK-144)")
	fmt.Println("  all                               Run all sub-commands")
}

// ── export-predictions (TASK-059) ─────────────────────────────────────────────

// cmdExportPredictions converts the JSONL prediction log for date (or today) to
// CSV and writes it to outputPath (or stdout when outputPath is "").
func cmdExportPredictions(dataRoot, date, outputPath string) {
	if date == "" {
		date = time.Now().UTC().Format("2006-01-02")
	}

	if outputPath != "" {
		fmt.Printf("Exporting predictions for %s → %s\n", date, outputPath)
	}

	if err := strategy.ExportPredictionsCSV(date, dataRoot, outputPath); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	if outputPath != "" {
		fmt.Printf("Done. Headers: timestamp, condition_id, city, signal, our_p, yes_edge, no_edge, confidence, ensemble_unc, decision, size_usdc\n")
	}
}

// cmdExplain fetches all active markets and runs ExplainEvaluate on each,
// printing a full audit table of BET vs SKIP decisions with intermediate values.
// Useful for debugging why the bot is or is not placing bets on specific markets.
func cmdExplain(dataRoot string) {
	header("🔍 DECISION AUDIT — Why each market is BET or SKIP")

	fmt.Print("  Fetching weather data (uses cache if fresh)…")
	fusedForecasts, _ := collectors.AggregateAll(dataRoot)
	fmt.Printf(" %d cities loaded\n", len(fusedForecasts))

	fmt.Print("  Fetching Polymarket markets…")
	mkt, err := markets.GetWeatherMarkets()
	if err != nil {
		fmt.Fprintf(os.Stderr, "\n  error fetching markets: %v\n", err)
		return
	}
	fmt.Printf(" %d weather markets found\n", len(mkt))

	const bankroll = 1000.0
	const minEdge = 0.05
	const maxBet = 25.0

	type row struct {
		r    *strategy.ExplainResult
		m    markets.Market
	}

	var rows []row
	for _, m := range mkt {
		if m.City == "" || m.Signal == "" {
			// Unclassified: report with no forecast
			rows = append(rows, row{
				r: &strategy.ExplainResult{
					Market: m,
					Action: "SKIP: unclassified (no city/signal match)",
				},
				m: m,
			})
			continue
		}
		ff := fusedForecasts[m.City] // nil when city has no forecast
		r := strategy.ExplainEvaluate(m, ff, bankroll, minEdge, maxBet)
		rows = append(rows, row{r: r, m: m})
	}

	// Sort: BET first (by edge desc), then SKIP (by best edge desc)
	sort.Slice(rows, func(i, j int) bool {
		ri, rj := rows[i].r, rows[j].r
		bi, bj := ri.IsBet(), rj.IsBet()
		if bi != bj {
			return bi // bets before skips
		}
		return ri.BestEdge > rj.BestEdge
	})

	t := newTable()
	t.AppendHeader(table.Row{
		"City/Signal", "OurP", "YesP→Edge", "NoP→Edge", "Conf", "Consensus", "EnsUnc", "Horizon", "Action",
	})

	betCount := 0
	for _, row := range rows {
		r := row.r
		m := row.m

		yesEdgeStr := fmt.Sprintf("%.2f→%+.3f", m.YesPrice, r.YesEdge)
		noEdgeStr := fmt.Sprintf("%.2f→%+.3f", m.NoPrice, r.NoEdge)
		confStr := fmt.Sprintf("%.2f", r.Confidence)
		encStr := fmt.Sprintf("%.1f°C", r.EnsUnc)

		// TASK-130: consensus score column — color-code by agreement level.
		consensusStr := "—"
		if r.ConsensusScore > 0 {
			consensusStr = fmt.Sprintf("%.2f", r.ConsensusScore)
			if r.ConsensusScore < 0.6 {
				consensusStr = styleLoss.Sprint(consensusStr)
			} else if r.ConsensusScore < 0.80 {
				consensusStr = styleNeutral.Sprint(consensusStr)
			}
		}

		// TASK-134: horizon column — hours to target date with color-coding.
		// Green ≤ 24 h, yellow 24–72 h, red > 72 h.
		horizonStr := "—"
		if r.ForecastHorizonHours > 0 {
			horizonStr = fmt.Sprintf("+%.0fh", r.ForecastHorizonHours)
			if r.ForecastHorizonHours <= 24 {
				horizonStr = styleWin.Sprint(horizonStr)
			} else if r.ForecastHorizonHours <= 72 {
				horizonStr = styleNeutral.Sprint(horizonStr)
			} else {
				horizonStr = styleLoss.Sprint(horizonStr)
			}
		}

		actionStr := r.Action
		if r.IsBet() {
			actionStr = styleWin.Sprint(r.Action)
			betCount++
		} else {
			actionStr = styleLoss.Sprint(r.Action)
		}

		if r.Confidence > 0 && r.Confidence < 0.4 {
			confStr = styleNeutral.Sprint(confStr)
		}

		ourPStr := "—"
		if r.FinalP > 0 {
			ourPStr = fmt.Sprintf("%.3f", r.FinalP)
		}
		if r.RawP != r.SeasonP && r.RawP > 0 {
			ourPStr += fmt.Sprintf("(raw=%.3f)", r.RawP)
		}

		t.AppendRow(table.Row{
			fmt.Sprintf("%s/%s", m.City, m.Signal),
			ourPStr,
			yesEdgeStr,
			noEdgeStr,
			confStr,
			consensusStr,
			encStr,
			horizonStr,
			actionStr,
		})
	}

	t.Render()

	skipCount := len(rows) - betCount
	fmt.Printf("\n  Markets evaluated: %d | %s | %s\n",
		len(rows),
		styleWin.Sprintf("BET: %d", betCount),
		styleLoss.Sprintf("SKIP: %d", skipCount),
	)
	if betCount > 0 {
		fmt.Println("\n  Tip: run `go run ./cmd/dashboard next` to see top-5 candidates with full sizing.")
	}
}

// cmdCacheStats shows the age of each cached fused forecast in data/forecasts/.
func cmdCacheStats(dataRoot string) {
	header("FORECAST CACHE STATUS")

	ages := collectors.ForecastCacheStats(dataRoot)
	if len(ages) == 0 {
		fmt.Println("  No cached forecasts found in", dataRoot+"/data/forecasts/")
		fmt.Println("  Run: go run ./cmd/bot --collect-history   or let the bot run once.")
		return
	}

	t := newTable()
	t.AppendHeader(table.Row{"Key", "Age", "Status"})

	keys := make([]string, 0, len(ages))
	for k := range ages {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	for _, k := range keys {
		age := ages[k]
		status := styleWin.Sprint("fresh")
		if age > 2*time.Hour {
			status = styleLoss.Sprint("stale (>2h)")
		} else if age > time.Hour {
			status = styleNeutral.Sprint("aging (>1h)")
		}
		t.AppendRow(table.Row{k, age.Round(time.Second).String(), status})
	}
	t.Render()
	fmt.Printf("\n  Cache directory: %s/data/forecasts/\n", dataRoot)
	fmt.Printf("  Total entries: %d\n", len(ages))
}

// ── report (TASK-052) ──────────────────────────────────────────────────────

// reportMarketEntry is one market's evaluation in the JSON report.
type reportMarketEntry struct {
	ConditionID string  `json:"condition_id"`
	Question    string  `json:"question"`
	City        string  `json:"city"`
	Signal      string  `json:"signal"`
	YesPrice    float64 `json:"yes_price"`
	NoPrice     float64 `json:"no_price"`
	OurP        float64 `json:"our_probability"`
	YesEdge     float64 `json:"yes_edge"`
	NoEdge      float64 `json:"no_edge"`
	Confidence  float64 `json:"confidence"`
	EnsUnc      float64 `json:"ensemble_uncertainty"`
	BestSide    string  `json:"best_side"`  // "YES", "NO", or ""
	BestEdge    float64 `json:"best_edge"`
	FinalSize   float64 `json:"final_size_usdc"`
	Action      string  `json:"action"`
	SkipReason  string  `json:"skip_reason,omitempty"`
}

// reportOutput is the full JSON document written by cmdReport.
type reportOutput struct {
	Timestamp       string              `json:"timestamp"`
	MarketsTotal    int                 `json:"markets_total"`
	BetsRecommended int                 `json:"bets_recommended"`
	Markets         []reportMarketEntry `json:"markets"`
}

// cmdReport fetches current markets + forecasts, evaluates each via
// ExplainEvaluate, and writes a JSON snapshot to outputPath (or stdout when
// outputPath is "").
func cmdReport(dataRoot, outputPath string) {
	fusedForecasts, _ := collectors.AggregateAll(dataRoot)
	mkt, err := markets.GetWeatherMarkets()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error fetching markets: %v\n", err)
		os.Exit(1)
	}

	const bankroll = 1000.0
	const minEdge = 0.05
	const maxBet = 25.0

	var entries []reportMarketEntry
	betCount := 0

	for _, m := range mkt {
		ff := fusedForecasts[m.City] // nil if city not present (pointer map)
		r := strategy.ExplainEvaluate(m, ff, bankroll, minEdge, maxBet)
		if r == nil {
			continue
		}
		entry := reportMarketEntry{
			ConditionID: m.ConditionID,
			Question:    m.Question,
			City:        m.City,
			Signal:      m.Signal,
			YesPrice:    m.YesPrice,
			NoPrice:     m.NoPrice,
			OurP:        r.FinalP,
			YesEdge:     r.YesEdge,
			NoEdge:      r.NoEdge,
			Confidence:  r.Confidence,
			EnsUnc:      r.EnsUnc,
			BestSide:    r.BestSide,
			BestEdge:    r.BestEdge,
			FinalSize:   r.FinalSize,
			Action:      r.Action,
			SkipReason:  r.SkipReason,
		}
		entries = append(entries, entry)
		if r.IsBet() {
			betCount++
		}
	}

	out := reportOutput{
		Timestamp:       time.Now().UTC().Format(time.RFC3339),
		MarketsTotal:    len(entries),
		BetsRecommended: betCount,
		Markets:         entries,
	}

	data, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "json marshal error: %v\n", err)
		os.Exit(1)
	}

	if outputPath == "" {
		fmt.Println(string(data))
		return
	}

	if err := os.WriteFile(outputPath, data, 0644); err != nil {
		fmt.Fprintf(os.Stderr, "error writing report to %s: %v\n", outputPath, err)
		os.Exit(1)
	}
	fmt.Printf("Report written to %s (%d markets, %d bets recommended)\n", outputPath, len(entries), betCount)
}

// ── analysis (TASK-057) ────────────────────────────────────────────────────

// cmdAnalysis reads today's prediction JSONL log and shows per-city/signal
// breakdown: how many markets were evaluated, how many generated bets, and
// why the rest were skipped.
func cmdAnalysis(dataRoot string) {
	header("🔬 PREDICTION LOG ANALYSIS")

	date := time.Now().UTC().Format("2006-01-02")
	records, err := strategy.LoadPredictions(date, dataRoot)
	if err != nil {
		fmt.Printf("  No prediction log for %s yet.\n", date)
		fmt.Printf("  Log is written to data/predictions/%s.jsonl when the bot runs.\n", date)
		return
	}

	if len(records) == 0 {
		fmt.Println("  Prediction log is empty.")
		return
	}

	fmt.Printf("  Date: %s   %s\n\n", date, strategy.PredictionSummary(records))

	breakdown := strategy.AnalyzePredictions(records)
	keys := strategy.SortedBreakdownKeys(breakdown)

	t := newTable()
	t.AppendHeader(table.Row{
		"City", "Signal", "Eval'd", "Bets", "Skip%",
		"SkipConf", "SkipEdge", "SkipSize",
		"AvgEdge", "AvgConf", "TotalSize$",
	})

	for _, k := range keys {
		s := breakdown[k]
		skipPct := fmt.Sprintf("%.0f%%", s.SkipPct())
		betStr := fmt.Sprintf("%d", s.Bets)
		if s.Bets > 0 {
			betStr = styleWin.Sprint(betStr)
		}
		if s.SkipPct() >= 100 {
			skipPct = styleLoss.Sprint(skipPct)
		}
		avgEdgeStr := "—"
		if s.AvgEdge() > 0 {
			avgEdgeStr = fmt.Sprintf("%+.3f", s.AvgEdge())
		}
		t.AppendRow(table.Row{
			k.City,
			k.Signal,
			s.Evaluated,
			betStr,
			skipPct,
			s.SkipConf,
			s.SkipEdge,
			s.SkipSize,
			avgEdgeStr,
			fmt.Sprintf("%.2f", s.AvgConf()),
			fmt.Sprintf("%.2f", s.TotalSize),
		})
	}
	t.Render()

	fmt.Printf("\n  Prediction log: data/predictions/%s.jsonl\n", date)
	fmt.Println("  SkipConf=confidence<0.40  SkipEdge=insufficient edge  SkipSize=Kelly<$0.50")
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// ── hourly (TASK-078) ─────────────────────────────────────────────────────────

// cmdHourly fetches and displays an hourly weather table for a single city.
// Usage: go run ./cmd/dashboard hourly <city>
//
// Columns: Hour UTC | Temp°C | Precip mm | Rain% | Wind km/h | Cloud% | WMO
// Rows with Rain% > 50 are annotated "(rain likely)".
// Rows with TempC above the monthly climatological normal are annotated "!".
func cmdHourly(city string) {
	if city == "" {
		fmt.Fprintln(os.Stderr, "  usage: dashboard hourly <city>")
		fmt.Fprintln(os.Stderr, "  available cities: new_york, london, tokyo, miami, paris, chicago, los_angeles, san_francisco, berlin, ...")
		os.Exit(1)
	}

	header(fmt.Sprintf("🕐 HOURLY FORECAST — %s", city))

	fmt.Print("  Fetching hourly data (today + tomorrow)…")
	points, err := collectors.FetchHourlyForecast(city, 2)
	if err != nil {
		fmt.Fprintf(os.Stderr, "\n  error: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf(" %d hourly points loaded\n\n", len(points))

	// Get current month's climate norm (AvgMaxTempC) to flag above-norm temps.
	now := time.Now().UTC()
	norm, hasNorm := weather.GetSeasonal(city, now.Month())

	// Separate today and tomorrow for visual grouping.
	todayStr := now.Format("2006-01-02")
	tomorrowStr := now.AddDate(0, 0, 1).Format("2006-01-02")

	t := newTable()
	t.AppendHeader(table.Row{
		"Date", "Hour UTC", "Temp°C", "Precip mm", "Rain%", "Wind km/h", "Cloud%", "WMO", "Note",
	})

	for _, p := range points {
		dateStr := p.Time.UTC().Format("2006-01-02")
		if dateStr != todayStr && dateStr != tomorrowStr {
			continue // only show today + tomorrow
		}

		hourStr := fmt.Sprintf("%02d:00", p.Time.UTC().Hour())
		tempStr := fmt.Sprintf("%.1f", p.TempC)
		precipStr := fmt.Sprintf("%.1f", p.PrecipMM)
		rainPctStr := fmt.Sprintf("%.0f%%", p.PrecipProb)
		windStr := fmt.Sprintf("%.0f", p.WindKMH)
		cloudStr := fmt.Sprintf("%.0f%%", p.CloudCover)
		wmoStr := fmt.Sprintf("%d", p.WeatherCode)

		note := ""
		// Mark above-climate-norm temperature with !
		if hasNorm && p.TempC > norm.AvgMaxTempC {
			tempStr = styleNeutral.Sprint(tempStr + "!")
		}
		// Mark high rain probability rows.
		if p.PrecipProb > 50 {
			rainPctStr = styleWin.Sprint(rainPctStr)
			note = "(rain likely)"
		}
		// Highlight heavy precip.
		if p.PrecipMM >= 5 {
			precipStr = styleLoss.Sprint(precipStr)
		}

		dayLabel := "today"
		if dateStr == tomorrowStr {
			dayLabel = "tmrw"
		}

		t.AppendRow(table.Row{
			dayLabel, hourStr, tempStr, precipStr, rainPctStr, windStr, cloudStr, wmoStr, note,
		})
	}

	t.Render()

	// Summary: full-day rain probability for today and tomorrow.
	todayPts := collectors.FilterHourlyByDate(points, todayStr)
	tomorrowPts := collectors.FilterHourlyByDate(points, tomorrowStr)

	fmt.Println()
	if len(todayPts) > 0 {
		// Compute business-hours window prob (06–18 UTC) via RainWindowProbability.
		refDate := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
		windowFrom := refDate.Add(6 * time.Hour)
		windowTo := refDate.Add(18 * time.Hour)
		fullDayP := collectors.HourlyRainProbabilityPublic(todayPts)
		windowP := collectors.RainWindowProbability(todayPts, windowFrom, windowTo)
		fmt.Printf("  Today     → full-day rain prob: %.0f%%  |  window [06-18 UTC]: %.0f%%\n",
			fullDayP*100, windowP*100)
	}
	if len(tomorrowPts) > 0 {
		refDate := time.Date(now.Year(), now.Month(), now.Day()+1, 0, 0, 0, 0, time.UTC)
		windowFrom := refDate.Add(6 * time.Hour)
		windowTo := refDate.Add(18 * time.Hour)
		fullDayP := collectors.HourlyRainProbabilityPublic(tomorrowPts)
		windowP := collectors.RainWindowProbability(tomorrowPts, windowFrom, windowTo)
		fmt.Printf("  Tomorrow  → full-day rain prob: %.0f%%  |  window [06-18 UTC]: %.0f%%\n",
			fullDayP*100, windowP*100)
	}

	if hasNorm {
		fmt.Printf("\n  Climate norm (month %d): AvgMaxTemp %.1f°C   RainDays %.0f%%   SunDays %.0f%%\n",
			int(now.Month()), norm.AvgMaxTempC, norm.RainProb*100, norm.SunProb*100)
		fmt.Printf("  Temp rows marked '!' are above this month's historical average maximum.\n")
	}
}

// ── heatmap (TASK-075) ────────────────────────────────────────────────────────

// cmdHeatmap prints a summary of today's market opportunity heatmap.
// The heatmap CSV accumulates all market evaluations across cycles.
func cmdHeatmap(dataRoot string) {
	rows, err := strategy.LoadTodayHeatmap(dataRoot)
	if err != nil {
		fmt.Printf("Heatmap error: %v\n", err)
		return
	}
	if len(rows) == 0 {
		fmt.Println("No heatmap data for today. Run the bot at least once in loop mode.")
		return
	}

	fmt.Printf("\n=== Market Opportunity Heatmap (%s, %d evaluations) ===\n\n",
		time.Now().UTC().Format("2006-01-02"), len(rows))

	// Aggregate by city+signal
	type cellKey struct{ city, signal string }
	type cellStats struct {
		count     int
		totalEdge float64
		totalConf float64
		bets      int
	}
	cells := make(map[cellKey]*cellStats)

	for _, r := range rows {
		k := cellKey{r.City, r.Signal}
		s := cells[k]
		if s == nil {
			s = &cellStats{}
			cells[k] = s
		}
		s.count++
		edge := r.YesEdge
		if r.NoEdge > edge {
			edge = r.NoEdge
		}
		s.totalEdge += edge
		s.totalConf += r.Confidence
		if r.Decision == "BET_YES" || r.Decision == "BET_NO" {
			s.bets++
		}
	}

	// Print table
	t := table.NewWriter()
	t.SetOutputMirror(os.Stdout)
	t.SetStyle(table.StyleLight)
	t.AppendHeader(table.Row{"City", "Signal", "Evals", "Bets", "Avg Edge", "Avg Conf"})

	for k, s := range cells {
		avgEdge := s.totalEdge / float64(s.count)
		avgConf := s.totalConf / float64(s.count)
		t.AppendRow(table.Row{
			k.city,
			k.signal,
			s.count,
			s.bets,
			fmt.Sprintf("%+.3f", avgEdge),
			fmt.Sprintf("%.3f", avgConf),
		})
	}
	t.SortBy([]table.SortBy{{Name: "Avg Edge", Mode: table.DscNumeric}})
	t.Render()

	fmt.Printf("\nHeatmap file: data/heatmap/%s.csv\n", time.Now().UTC().Format("2006-01-02"))
}

// ── source health (TASK-081) ──────────────────────────────────────────────────

// cmdSourceHealth displays per-source availability statistics loaded from
// data/source_health.json. The table is colour-coded: green = ok (< 1h ago),
// yellow = degraded (< 6h), red = down (> 6h or never seen).
func cmdSourceHealth(dataRoot string) {
	header("SOURCE HEALTH")

	health := collectors.LoadSourceHealth(dataRoot)

	// Ensure all known sources appear, even if they've never been called.
	knownSources := []string{"openmeteo", "nasa", "noaa", "goes"}
	for _, s := range knownSources {
		if _, ok := health[s]; !ok {
			health[s] = collectors.SourceHealth{}
		}
	}

	now := time.Now().UTC()

	t := newTable()
	t.AppendHeader(table.Row{
		"Source", "Status", "Last Success", "Last Error", "ConsecFails", "Total Calls", "Up Rate%",
	})

	// Keep knownSources order, then any extras.
	seen := map[string]bool{}
	ordered := make([]string, 0, len(health))
	for _, s := range knownSources {
		ordered = append(ordered, s)
		seen[s] = true
	}
	for s := range health {
		if !seen[s] {
			ordered = append(ordered, s)
		}
	}

	for _, src := range ordered {
		h := health[src]
		status := h.Status(now)

		// Colour the status cell.
		var statusStr string
		switch status {
		case "ok":
			statusStr = styleWin.Sprint("ok")
		case "degraded":
			statusStr = styleNeutral.Sprint("degraded")
		case "down":
			statusStr = styleLoss.Sprint("down")
		default:
			statusStr = "unknown"
		}

		lastOk := "-"
		if !h.LastSuccess.IsZero() {
			age := now.Sub(h.LastSuccess)
			if age < time.Minute {
				lastOk = fmt.Sprintf("%ds ago", int(age.Seconds()))
			} else if age < time.Hour {
				lastOk = fmt.Sprintf("%dm ago", int(age.Minutes()))
			} else {
				lastOk = fmt.Sprintf("%.1fh ago", age.Hours())
			}
		}

		lastErr := "-"
		if !h.LastError.IsZero() {
			age := now.Sub(h.LastError)
			if age < time.Minute {
				lastErr = fmt.Sprintf("%ds ago", int(age.Seconds()))
			} else if age < time.Hour {
				lastErr = fmt.Sprintf("%dm ago", int(age.Minutes()))
			} else {
				lastErr = fmt.Sprintf("%.1fh ago", age.Hours())
			}
			if h.LastErrorMsg != "" && len(h.LastErrorMsg) < 30 {
				lastErr += " (" + h.LastErrorMsg + ")"
			}
		}

		upRate := "-"
		if h.TotalCalls > 0 {
			upRate = fmt.Sprintf("%.0f%%", h.UpRatePct())
		}

		consecStr := fmt.Sprintf("%d", h.ConsecFails)
		if h.ConsecFails >= 3 {
			consecStr = styleLoss.Sprint(consecStr)
		}

		t.AppendRow(table.Row{
			src,
			statusStr,
			lastOk,
			lastErr,
			consecStr,
			h.TotalCalls,
			upRate,
		})
	}

	t.SetColumnConfigs([]table.ColumnConfig{
		{Name: "Source", Align: text.AlignLeft},
		{Name: "Status", Align: text.AlignCenter},
		{Name: "Up Rate%", Align: text.AlignRight},
	})
	t.Render()

	fmt.Printf("\nSource health data: %s/data/source_health.json\n", dataRoot)
	fmt.Println("Status: ok=<1h  degraded=<6h  down=>6h since last success")
}

// cmdDrift shows the forecast stability table for all cities (TASK-126).
// Displays per-city drift factors for day-0 and day-1, helping operators
// identify which cities are in a meteorologically uncertain state.
func cmdDrift(dataRoot string) {
	header("Forecast Stability (Drift Monitor)")

	cities := make([]string, 0, len(weather.Cities))
	for city := range weather.Cities {
		cities = append(cities, city)
	}
	sort.Strings(cities)

	t := newTable()
	t.AppendHeader(table.Row{
		"City", "D+0 Factor", "D+1 Factor",
		"Last ΔTemp°C", "Last ΔPrecip%", "D+0 Stability",
	})

	for _, city := range cities {
		rec0, f0 := collectors.LoadDriftSummary(city, 0, dataRoot)
		_, f1 := collectors.LoadDriftSummary(city, 1, dataRoot)

		// Format drift factor cells with colour coding.
		fmtFactor := func(f float64, hasHistory bool) string {
			if !hasHistory {
				return styleNeutral.Sprint("no data")
			}
			s := fmt.Sprintf("%.3f", f)
			switch {
			case f >= 0.95:
				return styleWin.Sprint(s)
			case f >= 0.85:
				return styleNeutral.Sprint(s)
			default:
				return styleLoss.Sprint(s)
			}
		}

		stabilityLabel := func(f float64, hasHistory bool) string {
			if !hasHistory {
				return "-"
			}
			switch {
			case f >= 0.95:
				return styleWin.Sprint("stable")
			case f >= 0.85:
				return styleNeutral.Sprint("moderate")
			default:
				return styleLoss.Sprint("unstable")
			}
		}

		hasD0 := !rec0.Timestamp.IsZero()
		hasD1 := f1 < 1.0 // if factor < 1 then history exists

		dTempStr := "-"
		dPrecipStr := "-"
		if hasD0 {
			dTempStr = fmt.Sprintf("%+.1f", rec0.AbsDeltaTempC)
			dPrecipStr = fmt.Sprintf("%+.1f%%", rec0.AbsDeltaPrecipPt)
		}

		t.AppendRow(table.Row{
			city,
			fmtFactor(f0, hasD0),
			fmtFactor(f1, hasD1),
			dTempStr,
			dPrecipStr,
			stabilityLabel(f0, hasD0),
		})
	}

	t.SetColumnConfigs([]table.ColumnConfig{
		{Name: "City", Align: text.AlignLeft},
		{Name: "D+0 Factor", Align: text.AlignCenter},
		{Name: "D+1 Factor", Align: text.AlignCenter},
		{Name: "D+0 Stability", Align: text.AlignCenter},
	})
	t.Render()

	fmt.Println()
	fmt.Println("  Drift factor: 1.000 = stable, 0.700 = maximally unstable (confidence floored)")
	fmt.Println("  Source: data/drift/{city}_d{N}.json — populated automatically when forecasts change")
}

// ── timing (TASK-133) ─────────────────────────────────────────────────────────

// cmdTiming prints the hourly win-rate table and timing multiplier per UTC hour.
// Operators can use this to understand at which hours the bot historically
// performs best, and adjust strategies accordingly.
func cmdTiming(dataRoot string) {
	buckets, err := calibration.LoadHourlyStats(dataRoot)
	if err != nil {
		fmt.Fprintf(os.Stderr, "timing: load error: %v\n", err)
		return
	}

	rows := calibration.HourlyTable(buckets)

	// Count hours with enough data.
	dataHours := 0
	for _, r := range rows {
		if r.WinRate >= 0 {
			dataHours++
		}
	}

	currentHour := time.Now().UTC().Hour()
	currentMult := calibration.TimingMultiplier(buckets, currentHour)

	fmt.Printf("\n  ── Hourly Win-Rate & Timing Multiplier (UTC) ──\n\n")
	fmt.Printf("  Current hour: %02d:xx UTC  |  Current multiplier: %.3f\n\n", currentHour, currentMult)

	t := table.NewWriter()
	t.SetOutputMirror(os.Stdout)
	t.SetStyle(table.StyleLight)
	t.AppendHeader(table.Row{"Hour (UTC)", "Wins", "Losses", "Total", "Win Rate", "Multiplier", "HorizonDecay", "Signal"})

	for _, r := range rows {
		var wrStr, multStr, decayStr, signal string

		if r.WinRate < 0 {
			wrStr = "—"
			multStr = "—"
			decayStr = "—"
			signal = "no data"
		} else {
			wrStr = fmt.Sprintf("%.0f%%", r.WinRate*100)
			multStr = fmt.Sprintf("%.3f", r.Multiplier)
			switch {
			case r.Multiplier >= 1.15:
				signal = "▲ great"
			case r.Multiplier >= 1.05:
				signal = "▲ good"
			case r.Multiplier <= 0.60:
				signal = "▼ avoid"
			case r.Multiplier <= 0.80:
				signal = "▼ weak"
			default:
				signal = "→ neutral"
			}

			// TASK-137: horizon decay column with colour coding.
			// Green ≥ 0.90, Yellow 0.75–0.90, Red < 0.75.
			decay := r.HorizonDecay
			switch {
			case decay >= 0.90:
				decayStr = fmt.Sprintf("🟢 %.3f", decay)
			case decay >= 0.75:
				decayStr = fmt.Sprintf("🟡 %.3f", decay)
			default:
				decayStr = fmt.Sprintf("🔴 %.3f", decay)
			}
			if r.AvgHorizonHours > 0 {
				decayStr += fmt.Sprintf(" (%.0fh)", r.AvgHorizonHours)
			}
		}

		marker := "  "
		if r.Hour == currentHour {
			marker = "▶ "
		}

		t.AppendRow(table.Row{
			fmt.Sprintf("%s%02d:00", marker, r.Hour),
			r.Wins,
			r.Losses,
			r.Wins + r.Losses,
			wrStr,
			multStr,
			decayStr,
			signal,
		})
	}

	t.SetColumnConfigs([]table.ColumnConfig{
		{Name: "Hour (UTC)", Align: text.AlignLeft},
		{Name: "Win Rate", Align: text.AlignRight},
		{Name: "Multiplier", Align: text.AlignRight},
		{Name: "HorizonDecay", Align: text.AlignRight},
		{Name: "Signal", Align: text.AlignLeft},
	})
	t.Render()

	fmt.Println()
	fmt.Printf("  Hours with data: %d/24\n", dataHours)
	fmt.Println("  Multiplier range: 0.50 (worst) → 1.20 (best)  |  1.000 = neutral (< 5 bets)")
	fmt.Println("  HorizonDecay: 🟢 ≥0.90 (fresh)  🟡 0.75–0.90  🔴 <0.75 (stale)  |  — = no horizon data yet")
	fmt.Println("  Source: data/hourly_winrate.json")
}

// ── summary (TASK-144) ────────────────────────────────────────────────────

// cmdSummary prints a single-page health overview combining bankroll, performance,
// today's activity, streak, top performers, and source health.
func cmdSummary(dataRoot string) {
	header("BOT HEALTH SUMMARY")
	now := time.Now()
	today := now.Format("2006-01-02")

	// ── Load base data ──────────────────────────────────────────────────────
	records, err := calibration.LoadHistory(dataRoot)
	if err != nil {
		fmt.Fprintf(os.Stderr, "  warning: could not load history: %v\n", err)
		records = nil
	}

	bankroll := calibration.LoadBankroll(dataRoot)
	peak := calibration.LoadPeakBankroll(dataRoot)

	// ── 1. Bankroll ─────────────────────────────────────────────────────────
	drawdownFraction := calibration.DrawdownFraction(peak, bankroll)
	drawdownPct := drawdownFraction * 100
	ddMult := calibration.DrawdownMultiplier(drawdownFraction, 0.30)

	ddColor := styleWin
	if drawdownPct > 20 {
		ddColor = styleLoss
	} else if drawdownPct > 10 {
		ddColor = styleNeutral
	}

	fmt.Printf("\n  %-20s %s\n", "Bankroll:", styleWin.Sprintf("$%.2f USDC", bankroll))
	fmt.Printf("  %-20s $%.2f USDC\n", "Peak:", peak)
	fmt.Printf("  %-20s %s  (mult: %.2f×)\n", "Drawdown:",
		ddColor.Sprintf("%.1f%%", drawdownPct), ddMult)

	// ── 2. Performance ──────────────────────────────────────────────────────
	fmt.Println()
	resolved := make([]calibration.BetRecord, 0, len(records))
	open := make([]calibration.BetRecord, 0, len(records))
	wins := 0
	for _, r := range records {
		if r.Outcome == nil {
			open = append(open, r)
		} else {
			resolved = append(resolved, r)
			if *r.Outcome {
				wins++
			}
		}
	}

	brierScore, brierCount, _ := calibration.BrierScore(records)
	winRate := 0.0
	if len(resolved) > 0 {
		winRate = float64(wins) / float64(len(resolved)) * 100
	}

	brierStr := "—"
	if brierCount > 0 {
		brierStr = fmt.Sprintf("%.4f (%s)", brierScore, brierQuality(brierScore))
	}
	winStr := "—"
	if len(resolved) > 0 {
		winStr = fmt.Sprintf("%.1f%%  (%d/%d)", winRate, wins, len(resolved))
	}

	fmt.Printf("  %-20s %s\n", "Brier Score:", brierStr)
	fmt.Printf("  %-20s %s\n", "Win Rate:", winStr)
	fmt.Printf("  %-20s %d open  /  %d resolved  /  %d total\n", "Positions:",
		len(open), len(resolved), len(records))

	sharpe, sharpeCount, _ := calibration.RollingSharpe(dataRoot, 30)
	if sharpeCount >= 2 {
		fmt.Printf("  %-20s %.3f (%s, %dd)\n", "Sharpe (30d):",
			sharpe, calibration.SharpeQuality(sharpe), sharpeCount)
	}

	// ── 3. Today ────────────────────────────────────────────────────────────
	fmt.Println()
	todayBets := 0
	todayPnL := 0.0
	todayUnresolved := 0
	for _, r := range records {
		if r.Timestamp.Format("2006-01-02") != today {
			continue
		}
		todayBets++
		if r.Outcome == nil {
			todayUnresolved++
		} else {
			odds := 1.0 / r.MarketPrice
			pnl := r.SizeUSDC * (odds - 1)
			if !*r.Outcome {
				pnl = -r.SizeUSDC
			}
			todayPnL += pnl
		}
	}
	pnlColor := styleWin
	if todayPnL < 0 {
		pnlColor = styleLoss
	} else if todayPnL == 0 {
		pnlColor = styleNeutral
	}
	fmt.Printf("  %-20s %d bets  (%d unresolved)\n", "Today:", todayBets, todayUnresolved)
	fmt.Printf("  %-20s %s\n", "Today P&L:", pnlColor.Sprintf("%+.2f USDC", todayPnL))

	// ── 4. Streak ───────────────────────────────────────────────────────────
	streakLine := calibration.StreakStatusLine(records)
	fmt.Printf("  %-20s %s\n", "Streak:", streakLine)

	// ── 5. Top cities ───────────────────────────────────────────────────────
	fmt.Println()
	cityBD := calibration.CityBreakdown(records)
	type bdEntry struct {
		name string
		stats calibration.BreakdownStats
	}
	var cities []bdEntry
	for k, v := range cityBD {
		if v.Count >= 3 {
			cities = append(cities, bdEntry{k, v})
		}
	}
	sort.Slice(cities, func(i, j int) bool {
		return cities[i].stats.WinRate() > cities[j].stats.WinRate()
	})
	if len(cities) > 0 {
		fmt.Printf("  %-20s", "Top Cities:")
		limit := 3
		if len(cities) < limit {
			limit = len(cities)
		}
		for i := 0; i < limit; i++ {
			c := cities[i]
			fmt.Printf("  %s %.0f%%(%d)", c.name, c.stats.WinRate(), c.stats.Count)
		}
		fmt.Println()
	}

	// ── 6. Top signals ──────────────────────────────────────────────────────
	sigBD := calibration.SignalBreakdown(records)
	var sigs []bdEntry
	for k, v := range sigBD {
		if v.Count >= 3 {
			sigs = append(sigs, bdEntry{k, v})
		}
	}
	sort.Slice(sigs, func(i, j int) bool {
		return sigs[i].stats.WinRate() > sigs[j].stats.WinRate()
	})
	if len(sigs) > 0 {
		fmt.Printf("  %-20s", "Top Signals:")
		limit := 3
		if len(sigs) < limit {
			limit = len(sigs)
		}
		for i := 0; i < limit; i++ {
			s := sigs[i]
			fmt.Printf("  %s %.0f%%(%d)", s.name, s.stats.WinRate(), s.stats.Count)
		}
		fmt.Println()
	}

	// ── 7. Source health summary ─────────────────────────────────────────────
	health := collectors.LoadSourceHealth(dataRoot)
	if len(health) > 0 {
		fmt.Println()
		fmt.Printf("  %-20s", "Sources:")
		sourceOrder := []string{"openmeteo", "nasa", "noaa", "goes", "hrrr", "ecmwf"}
		for _, src := range sourceOrder {
			h, ok := health[src]
			if !ok {
				continue
			}
			status := h.Status(now)
			icon := "✅"
			if status == "degraded" {
				icon = "⚠️"
			} else if status == "down" || status == "unknown" {
				icon = "❌"
			}
			fmt.Printf("  %s%s", icon, src)
		}
		fmt.Println()
	}

	// ── 8. Recent bets ──────────────────────────────────────────────────────
	fmt.Println()
	fmt.Println(styleHeader.Sprint("  Recent Bets"))
	// Sort resolved by time desc, show last 5
	allResolved := make([]calibration.BetRecord, 0, len(resolved))
	allResolved = append(allResolved, resolved...)
	sort.Slice(allResolved, func(i, j int) bool {
		return allResolved[i].Timestamp.After(allResolved[j].Timestamp)
	})
	if len(allResolved) == 0 {
		fmt.Println("  (no resolved bets yet)")
	} else {
		limit := 5
		if len(allResolved) < limit {
			limit = len(allResolved)
		}
		for i := 0; i < limit; i++ {
			r := allResolved[i]
			label := "WIN"
			col := styleWin
			if !*r.Outcome {
				label = "LOSS"
				col = styleLoss
			}
			cityStr := r.City
			if cityStr == "" {
				cityStr = "?"
			}
			sigStr := r.Signal
			if sigStr == "" {
				sigStr = "?"
			}
			odds := 1.0 / r.MarketPrice
			pnl := r.SizeUSDC * (odds - 1)
			if !*r.Outcome {
				pnl = -r.SizeUSDC
			}
			fmt.Printf("  %s  %s  %s/%s  %s  %+.2f USDC\n",
				r.Timestamp.Format("01-02 15:04"),
				col.Sprint(label),
				cityStr, sigStr,
				r.Side,
				pnl,
			)
		}
	}

	fmt.Printf("\n  Generated at: %s\n", now.Format("2006-01-02 15:04:05 UTC"))
}

// brierQuality is duplicated here for dashboard-internal use; the canonical
// version lives in calibration package.
func brierQuality(score float64) string {
	switch {
	case score <= 0.08:
		return "excellent"
	case score <= 0.12:
		return "good"
	case score <= 0.18:
		return "acceptable"
	default:
		return "poor"
	}
}

// ── freshness (TASK-140) ───────────────────────────────────────────────────

// cmdFreshness shows a per-city freshness table for cached forecasts.
// Status thresholds: fresh (<1h), ok (1–3h), stale (>3h), missing (no file).
func cmdFreshness(dataRoot string) {
	header("FORECAST FRESHNESS")

	// Load all cached ages from disk.
	ages := collectors.ForecastCacheStats(dataRoot)

	t := newTable()
	t.AppendHeader(table.Row{"City+Day", "Age", "Status"})

	// Collect all keys: known cities × day0, plus any extra keys from cache.
	allKeys := make(map[string]bool)
	for city := range weather.Cities {
		key := city + "_d0"
		allKeys[key] = true
	}
	for k := range ages {
		allKeys[k] = true
	}

	// Sort keys for stable output.
	sorted := make([]string, 0, len(allKeys))
	for k := range allKeys {
		sorted = append(sorted, k)
	}
	sort.Strings(sorted)

	fresh, ok, stale, missing := 0, 0, 0, 0
	for _, k := range sorted {
		age, cached := ages[k]
		if !cached {
			t.AppendRow(table.Row{k, "—", styleLoss.Sprint("missing")})
			missing++
			continue
		}
		ageStr := age.Round(time.Second).String()
		var status string
		switch {
		case age < time.Hour:
			status = styleWin.Sprint("fresh")
			fresh++
		case age <= 3*time.Hour:
			status = styleNeutral.Sprint("ok")
			ok++
		default:
			status = styleLoss.Sprint("stale")
			stale++
		}
		t.AppendRow(table.Row{k, ageStr, status})
	}
	t.Render()

	fmt.Printf("\n  %d fresh  |  %d ok  |  %d stale  |  %d missing\n",
		fresh, ok, stale, missing)
	fmt.Printf("  Cache directory: %s/data/forecasts/\n", dataRoot)
}
