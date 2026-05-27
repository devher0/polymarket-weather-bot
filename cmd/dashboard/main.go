// cmd/dashboard — CLI dashboard for the Polymarket Weather Bot.
//
// Usage:
//
//	go run ./cmd/dashboard positions  — show open positions (from bets_history.csv)
//	go run ./cmd/dashboard pnl        — P&L summary from data/bets_history.csv
//	go run ./cmd/dashboard next       — top-5 bet candidates right now
//	go run ./cmd/dashboard all        — run all sub-commands
package main

import (
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

	t := newTable()
	t.AppendHeader(table.Row{
		"Condition ID", "Side", "Our P", "Mkt Price", "Size", "Opened",
	})

	for _, r := range open {
		age := time.Since(r.Timestamp).Round(time.Hour)
		t.AppendRow(table.Row{
			truncate(r.ConditionID, 16),
			r.Side,
			fmt.Sprintf("%.2f", r.OurProbability),
			fmt.Sprintf("%.2f", r.MarketPrice),
			fmt.Sprintf("$%.2f", r.SizeUSDC),
			fmt.Sprintf("%s ago", age),
		})
	}

	t.Render()
	fmt.Printf("\n  Total open: %d position(s)\n", len(open))
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
	fmt.Println("  positions   Show open (unresolved) positions")
	fmt.Println("  pnl         P&L history from data/bets_history.csv")
	fmt.Println("  next        Top-5 bet candidates right now")
	fmt.Println("  forecast    Fused weather forecast table for all cities")
	fmt.Println("  cache       Show forecast cache status (age of cached data)")
	fmt.Println("  all         Run all sub-commands")
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

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
