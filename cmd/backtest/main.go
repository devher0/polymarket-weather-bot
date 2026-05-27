// cmd/backtest — simulates weather bot bets over the last 90 days.
//
// Usage:
//
//	go run ./cmd/backtest                   — run with default settings
//	go run ./cmd/backtest --days 30         — limit backtest window
//	go run ./cmd/backtest --min-edge 0.05   — override edge threshold
//	go run ./cmd/backtest --bankroll 1000   — starting bankroll in USDC
//	go run ./cmd/backtest --verbose         — print each simulated bet
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/joho/godotenv"

	"github.com/devher0/polymarket-weather-bot/internal/collectors"
	"github.com/devher0/polymarket-weather-bot/internal/markets"
	"github.com/devher0/polymarket-weather-bot/internal/strategy"
	"github.com/devher0/polymarket-weather-bot/internal/weather"
)

// ── Flags ──────────────────────────────────────────────────────────────────

var (
	flagDays     = flag.Int("days", 90, "backtest window in days")
	flagMinEdge  = flag.Float64("min-edge", 0.05, "minimum edge to place bet")
	flagBankroll = flag.Float64("bankroll", 1000.0, "starting bankroll (USDC)")
	flagMaxBet   = flag.Float64("max-bet", 25.0, "maximum bet size per market (USDC)")
	flagVerbose  = flag.Bool("verbose", false, "print each simulated bet")
	flagDataRoot = flag.String("data", ".", "project root for historical data cache")
)

// ── Gamma API types ────────────────────────────────────────────────────────

const gammaBase = "https://gamma-api.polymarket.com"

// gammaMarket is a minimal struct for the Gamma API market object.
type gammaMarket struct {
	ConditionID    string  `json:"conditionId"`
	Question       string  `json:"question"`
	StartDate      string  `json:"startDate"`
	EndDate        string  `json:"endDate"`
	Closed         bool    `json:"closed"`
	Archived       bool    `json:"archived"`
	Resolved       bool    `json:"resolved"`
	OutcomePrices  string  `json:"outcomePrices"`  // JSON array: ["0","1"] or ["1","0"]
	Outcomes       string  `json:"outcomes"`        // JSON array: ["Yes","No"]
	Volume         float64 `json:"volume,string"`
}

type gammaResp struct {
	Data   []gammaMarket `json:"data"`
	Count  int           `json:"count"`
	Limit  int           `json:"limit"`
	Offset int           `json:"offset"`
}

var httpClient = &http.Client{Timeout: 30 * time.Second}

// ── Gamma helpers ──────────────────────────────────────────────────────────

// fetchResolvedMarkets returns resolved weather-related markets from Gamma API.
func fetchResolvedMarkets(since time.Time) ([]gammaMarket, error) {
	var all []gammaMarket
	limit := 100
	offset := 0

	for {
		url := fmt.Sprintf(
			"%s/markets?closed=true&limit=%d&offset=%d",
			gammaBase, limit, offset,
		)

		req, _ := http.NewRequest("GET", url, nil)
		req.Header.Set("User-Agent", "polymarket-weather-bot/backtest")
		resp, err := httpClient.Do(req)
		if err != nil {
			return nil, fmt.Errorf("gamma request: %w", err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("gamma status %d: %s", resp.StatusCode, string(body[:min(len(body), 200)]))
		}

		var page gammaResp
		if err := json.Unmarshal(body, &page); err != nil {
			// Gamma may return a plain array in some API versions
			var arr []gammaMarket
			if err2 := json.Unmarshal(body, &arr); err2 != nil {
				return nil, fmt.Errorf("gamma parse: %w", err)
			}
			page.Data = arr
		}

		for _, m := range page.Data {
			if !m.Resolved {
				continue
			}
			// Filter by end date
			if m.EndDate != "" {
				t, err := time.Parse(time.RFC3339, m.EndDate)
				if err != nil {
					// Try date-only format
					t, err = time.Parse("2006-01-02T15:04:05Z", m.EndDate)
					if err != nil {
						t, err = time.Parse("2006-01-02", m.EndDate)
					}
				}
				if err == nil && t.Before(since) {
					continue
				}
			}
			all = append(all, m)
		}

		if len(page.Data) < limit {
			break
		}
		offset += limit

		// Safety: don't page forever
		if offset > 5000 {
			break
		}
	}

	return all, nil
}

// parseOutcomePrices returns the final price for YES and NO outcomes.
// outcomePrices is a JSON array like ["0","1"] or ["1","0"].
// Outcome order matches the outcomes array (e.g., ["Yes","No"]).
func parseOutcomePrices(outcomePricesJSON, outcomesJSON string) (yesResolved, noResolved bool, err error) {
	var prices []string
	if err := json.Unmarshal([]byte(outcomePricesJSON), &prices); err != nil {
		return false, false, fmt.Errorf("parse prices: %w", err)
	}
	var outcomes []string
	if err := json.Unmarshal([]byte(outcomesJSON), &outcomes); err != nil {
		return false, false, fmt.Errorf("parse outcomes: %w", err)
	}

	if len(prices) != len(outcomes) {
		return false, false, fmt.Errorf("prices/outcomes length mismatch")
	}

	for i, outcome := range outcomes {
		if i >= len(prices) {
			break
		}
		price, _ := strconv.ParseFloat(prices[i], 64)
		switch strings.ToLower(outcome) {
		case "yes":
			yesResolved = price >= 0.9
		case "no":
			noResolved = price >= 0.9
		}
	}
	return yesResolved, noResolved, nil
}

// ── Market classifier (mirrors markets package) ────────────────────────────

type backtestMarket struct {
	markets.Market
	StartDate   time.Time
	EndDate     time.Time
	YesResolved bool
	NoResolved  bool
	Volume      float64
}

var tempThreshRe = regexp.MustCompile(`(\d+(?:\.\d+)?)\s*°?\s*([FC])\b`)

func parseTempThresholdC(q string) float64 {
	m := tempThreshRe.FindStringSubmatch(q)
	if len(m) < 3 {
		return 0
	}
	v, err := strconv.ParseFloat(m[1], 64)
	if err != nil {
		return 0
	}
	if strings.ToUpper(m[2]) == "F" {
		v = (v - 32) * 5 / 9
	}
	return v
}

var signalPatterns = []struct {
	re  *regexp.Regexp
	sig string
}{
	{regexp.MustCompile(`(?i)rain|precipitation|rainfall|rainy`), "rain"},
	{regexp.MustCompile(`(?i)temperature.{0,20}above|above.{0,10}\d+.{0,10}degree|heat.?wave|heatwave|hot day`), "heat"},
	{regexp.MustCompile(`(?i)temperature.{0,20}below|below.{0,10}\d+.{0,10}degree|cold snap|freeze`), "cold"},
	{regexp.MustCompile(`(?i)snow|snowfall|blizzard`), "snow"},
	{regexp.MustCompile(`(?i)wind|hurricane|typhoon|storm`), "wind"},
	{regexp.MustCompile(`(?i)sunny|sunshine|clear sky`), "sunny"},
}

var cityPats = map[string]*regexp.Regexp{
	"new_york":      regexp.MustCompile(`(?i)new york|nyc|manhattan|Chi-town`),
	"london":        regexp.MustCompile(`(?i)\blondon\b|uk weather`),
	"tokyo":         regexp.MustCompile(`(?i)\btokyo\b|japan weather`),
	"miami":         regexp.MustCompile(`(?i)\bmiami\b|florida weather`),
	"paris":         regexp.MustCompile(`(?i)\bparis\b|france weather`),
	"chicago":       regexp.MustCompile(`(?i)\bchicago\b|chi-town`),
	"los_angeles":   regexp.MustCompile(`(?i)los angeles|\bLA\b|l\.a\.|southern california`),
	"san_francisco": regexp.MustCompile(`(?i)san francisco|\bSF\b|bay area|s\.f\.|frisco`),
	"berlin":        regexp.MustCompile(`(?i)\bberlin\b|germany weather`),
}

func classifyGamma(gm gammaMarket) (city, sig string, threshC float64) {
	for _, s := range signalPatterns {
		if s.re.MatchString(gm.Question) {
			sig = s.sig
			break
		}
	}
	if sig == "" {
		return
	}
	for c, re := range cityPats {
		if re.MatchString(gm.Question) {
			city = c
			break
		}
	}
	if sig == "heat" || sig == "cold" {
		threshC = parseTempThresholdC(gm.Question)
	}
	return
}

func parseDate(s string) (time.Time, error) {
	for _, layout := range []string{
		time.RFC3339,
		"2006-01-02T15:04:05Z",
		"2006-01-02T15:04:05.000Z",
		"2006-01-02",
	} {
		if t, err := time.Parse(layout, s); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("cannot parse date %q", s)
}

// ── Backtest logic ─────────────────────────────────────────────────────────

// SimResult is a single simulated bet outcome.
type SimResult struct {
	Question    string
	City        string
	Signal      string
	Date        string
	Side        string
	Size        float64
	OurP        float64
	MarketPrice float64
	Edge        float64
	Won         bool
	PnL         float64 // +size if won, -size if lost (simplified fixed-odds)
}

func runBacktest(
	historicalData map[string]map[string]weather.Forecast, // city → date → Forecast
	gammaMarkets []gammaMarket,
	minEdge float64,
	bankroll float64,
	maxBet float64,
	verbose bool,
) []SimResult {
	var results []SimResult

	for _, gm := range gammaMarkets {
		if gm.OutcomePrices == "" || gm.Outcomes == "" {
			continue
		}

		city, sig, threshC := classifyGamma(gm)
		if sig == "" || city == "" {
			continue
		}

		// Parse dates
		endDate, err := parseDate(gm.EndDate)
		if err != nil {
			continue
		}

		// Look up historical weather for the day the market closes
		dateKey := endDate.Format("2006-01-02")
		cityData, ok := historicalData[city]
		if !ok {
			continue
		}
		fc, ok := cityData[dateKey]
		if !ok {
			// Try the day before (markets sometimes close at midnight)
			dateKey = endDate.AddDate(0, 0, -1).Format("2006-01-02")
			fc, ok = cityData[dateKey]
			if !ok {
				continue
			}
		}

		// Parse resolution: did YES win?
		yesWon, noWon, err := parseOutcomePrices(gm.OutcomePrices, gm.Outcomes)
		if err != nil {
			continue
		}
		if !yesWon && !noWon {
			continue // unresolved or ambiguous
		}

		// Estimate market prices (use 0.5/0.5 as fallback since we don't have entry prices)
		// Real backtest would need to fetch the historical prices at bet time.
		// We use a conservative 0.5 / 0.5 split.
		yesPrice := 0.50
		noPrice := 0.50

		m := markets.Market{
			ConditionID: gm.ConditionID,
			Question:    gm.Question,
			YesTokenID:  "yes-" + gm.ConditionID,
			NoTokenID:   "no-" + gm.ConditionID,
			YesPrice:    yesPrice,
			NoPrice:     noPrice,
			City:        city,
			Signal:      sig,
			ThresholdC:  threshC,
		}

		forecastMap := map[string][]weather.Forecast{
			city: {fc},
		}

		d := strategy.Evaluate(m, forecastMap, bankroll, minEdge, maxBet)
		if d == nil {
			continue
		}

		// Did our bet win?
		won := (d.Side == "YES" && yesWon) || (d.Side == "NO" && noWon)

		// Simplified PnL: binary outcome at ~2x odds (price ≈ 0.50)
		// Real PnL = size * (1/price - 1) if won, -size if lost
		odds := 1.0 / d.MarketPrice
		pnl := d.SizeUSDC * (odds - 1)
		if !won {
			pnl = -d.SizeUSDC
		}

		sr := SimResult{
			Question:    gm.Question,
			City:        city,
			Signal:      sig,
			Date:        dateKey,
			Side:        d.Side,
			Size:        d.SizeUSDC,
			OurP:        d.OurProbability,
			MarketPrice: d.MarketPrice,
			Edge:        d.Edge,
			Won:         won,
			PnL:         pnl,
		}
		results = append(results, sr)

		if verbose {
			icon := "✗"
			if won {
				icon = "✓"
			}
			fmt.Printf("  [%s] %s/%s %s @ %.2f | edge=%+.2f | PnL=%+.2f\n",
				icon, city, sig, d.Side, d.MarketPrice, d.Edge, pnl)
		}
	}

	return results
}

// ── Statistics ─────────────────────────────────────────────────────────────

type BacktestStats struct {
	TotalBets  int
	Wins       int
	Losses     int
	WinRate    float64
	TotalPnL   float64
	AvgEdge    float64
	MaxDrawdown float64
	SharpeRatio float64
}

func computeStats(results []SimResult) BacktestStats {
	if len(results) == 0 {
		return BacktestStats{}
	}

	wins := 0
	totalEdge := 0.0
	totalPnL := 0.0

	// Sort by date for drawdown/Sharpe calculation
	sort.Slice(results, func(i, j int) bool {
		return results[i].Date < results[j].Date
	})

	dailyPnL := make(map[string]float64)
	for _, r := range results {
		if r.Won {
			wins++
		}
		totalEdge += r.Edge
		totalPnL += r.PnL
		dailyPnL[r.Date] += r.PnL
	}

	winRate := float64(wins) / float64(len(results))
	avgEdge := totalEdge / float64(len(results))

	// Sharpe ratio: mean(daily PnL) / stddev(daily PnL) * sqrt(252)
	days := make([]float64, 0, len(dailyPnL))
	for _, pnl := range dailyPnL {
		days = append(days, pnl)
	}

	sharpe := 0.0
	if len(days) > 1 {
		mean := 0.0
		for _, d := range days {
			mean += d
		}
		mean /= float64(len(days))

		variance := 0.0
		for _, d := range days {
			diff := d - mean
			variance += diff * diff
		}
		variance /= float64(len(days))
		sd := math.Sqrt(variance)
		if sd > 0 {
			sharpe = (mean / sd) * math.Sqrt(252)
		}
	}

	// Max drawdown: peak-to-trough over cumulative PnL curve
	cumPnL := 0.0
	peak := 0.0
	maxDD := 0.0
	for _, r := range results {
		cumPnL += r.PnL
		if cumPnL > peak {
			peak = cumPnL
		}
		dd := peak - cumPnL
		if dd > maxDD {
			maxDD = dd
		}
	}

	return BacktestStats{
		TotalBets:   len(results),
		Wins:        wins,
		Losses:      len(results) - wins,
		WinRate:     winRate,
		TotalPnL:    totalPnL,
		AvgEdge:     avgEdge,
		MaxDrawdown: maxDD,
		SharpeRatio: sharpe,
	}
}

// ── Entry point ────────────────────────────────────────────────────────────

func main() {
	flag.Parse()
	_ = godotenv.Load()

	since := time.Now().UTC().AddDate(0, 0, -*flagDays)
	fmt.Printf("🔍 Polymarket Weather Bot — Backtest\n")
	fmt.Printf("   Window: last %d days (since %s)\n", *flagDays, since.Format("2006-01-02"))
	fmt.Printf("   Min edge: %.0f%%  |  Bankroll: $%.0f  |  Max bet: $%.0f\n\n",
		*flagMinEdge*100, *flagBankroll, *flagMaxBet)

	// 1. Load historical weather data
	fmt.Println("📥 Loading historical weather data…")
	historicalData, err := loadHistoricalData(*flagDataRoot)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ERROR: loading historical data: %v\n", err)
		fmt.Println("Tip: run `go run ./cmd/bot --collect-history` first")
		os.Exit(1)
	}
	totalRecords := 0
	for city, dates := range historicalData {
		n := len(dates)
		totalRecords += n
		_ = city
	}
	fmt.Printf("   Loaded %d city-day records across %d cities\n\n", totalRecords, len(historicalData))

	if totalRecords == 0 {
		fmt.Println("No historical data found. Run: go run ./cmd/bot --collect-history")
		os.Exit(1)
	}

	// 2. Fetch resolved markets from Gamma API
	fmt.Println("🌐 Fetching resolved markets from Polymarket Gamma API…")
	gammaMarkets, err := fetchResolvedMarkets(since)
	if err != nil {
		fmt.Fprintf(os.Stderr, "WARNING: gamma API error: %v\n", err)
		fmt.Println("Using synthetic market data based on historical weather…")
		gammaMarkets = synthesizeMarketsFromHistory(historicalData, since)
	}
	fmt.Printf("   Found %d resolved markets\n\n", len(gammaMarkets))

	if len(gammaMarkets) == 0 {
		fmt.Println("No resolved weather markets found in the window. Try increasing --days.")
		os.Exit(0)
	}

	// 3. Run simulation
	fmt.Println("⚡ Simulating bets…")
	if *flagVerbose {
		fmt.Println()
	}
	results := runBacktest(historicalData, gammaMarkets, *flagMinEdge, *flagBankroll, *flagMaxBet, *flagVerbose)
	if *flagVerbose && len(results) > 0 {
		fmt.Println()
	}

	// 4. Compute and print stats
	stats := computeStats(results)
	printReport(stats, results, *flagBankroll)
}

// loadHistoricalData reads all cities' historical JSON files into a nested map.
func loadHistoricalData(dataRoot string) (map[string]map[string]weather.Forecast, error) {
	out := make(map[string]map[string]weather.Forecast)

	for city := range weather.Cities {
		records, err := collectors.GetHistory(city, dataRoot)
		if err != nil {
			fmt.Fprintf(os.Stderr, "  warning: %s: %v\n", city, err)
			continue
		}
		if len(records) == 0 {
			continue
		}
		byDate := make(map[string]weather.Forecast, len(records))
		for _, r := range records {
			byDate[r.Date] = r.Forecast
		}
		out[city] = byDate
	}

	return out, nil
}

// synthesizeMarketsFromHistory creates synthetic resolved markets from historical
// weather data when Gamma API is unavailable. Useful for offline testing.
// We generate rain/heat/cold markets at 0.5 odds and check outcomes.
func synthesizeMarketsFromHistory(
	historicalData map[string]map[string]weather.Forecast,
	since time.Time,
) []gammaMarket {
	var synthetic []gammaMarket
	id := 0

	for city, dates := range historicalData {
		for dateStr, fc := range dates {
			d, err := time.Parse("2006-01-02", dateStr)
			if err != nil || d.Before(since) {
				continue
			}

			// Rain market
			rainProb := weather.RainProbability(fc)
			yesWon := "0"
			noWon := "1"
			if rainProb > 0.5 {
				yesWon = "1"
				noWon = "0"
			}
			question := fmt.Sprintf("Will it rain in %s on %s?", city, dateStr)
			synthetic = append(synthetic, gammaMarket{
				ConditionID:   fmt.Sprintf("syn-%d", id),
				Question:      question,
				EndDate:       d.Format(time.RFC3339),
				Closed:        true,
				Resolved:      true,
				OutcomePrices: fmt.Sprintf(`["%s","%s"]`, yesWon, noWon),
				Outcomes:      `["Yes","No"]`,
				Volume:        1000,
			})
			id++

			// Heat market (>30°C)
			heatProb := weather.HeatProbability(fc, 30.0)
			if heatProb > 0.5 {
				yesWon = "1"
				noWon = "0"
			} else {
				yesWon = "0"
				noWon = "1"
			}
			question = fmt.Sprintf("Will temperature exceed 30°C in %s on %s?", city, dateStr)
			synthetic = append(synthetic, gammaMarket{
				ConditionID:   fmt.Sprintf("syn-%d", id),
				Question:      question,
				EndDate:       d.Format(time.RFC3339),
				Closed:        true,
				Resolved:      true,
				OutcomePrices: fmt.Sprintf(`["%s","%s"]`, yesWon, noWon),
				Outcomes:      `["Yes","No"]`,
				Volume:        1000,
			})
			id++
		}
	}

	return synthetic
}

// printReport prints the formatted backtest report.
func printReport(stats BacktestStats, results []SimResult, startingBankroll float64) {
	sep := strings.Repeat("─", 56)

	fmt.Println(sep)
	fmt.Println("📊 BACKTEST RESULTS")
	fmt.Println(sep)

	if stats.TotalBets == 0 {
		fmt.Println("  No qualifying bets found (check edge threshold / data coverage)")
		fmt.Println(sep)
		return
	}

	endingBankroll := startingBankroll + stats.TotalPnL
	roi := stats.TotalPnL / startingBankroll * 100

	fmt.Printf("  Simulated bets : %d\n", stats.TotalBets)
	fmt.Printf("  Wins / Losses  : %d / %d\n", stats.Wins, stats.Losses)
	fmt.Printf("  Win rate       : %.1f%%\n", stats.WinRate*100)
	fmt.Printf("  Avg edge       : %+.2f%%\n", stats.AvgEdge*100)
	fmt.Println(sep)
	fmt.Printf("  Total P&L      : %+.2f USDC\n", stats.TotalPnL)
	fmt.Printf("  ROI            : %+.1f%%  ($%.2f → $%.2f)\n",
		roi, startingBankroll, endingBankroll)
	fmt.Printf("  Max drawdown   : -%.2f USDC\n", stats.MaxDrawdown)
	fmt.Printf("  Sharpe ratio   : %.2f\n", stats.SharpeRatio)
	fmt.Println(sep)

	// Quality labels
	if stats.SharpeRatio >= 2.0 {
		fmt.Println("  📈 Strategy quality: EXCELLENT (Sharpe ≥ 2)")
	} else if stats.SharpeRatio >= 1.0 {
		fmt.Println("  📈 Strategy quality: GOOD (Sharpe ≥ 1)")
	} else if stats.SharpeRatio >= 0 {
		fmt.Println("  ⚠️  Strategy quality: MARGINAL (Sharpe 0–1)")
	} else {
		fmt.Println("  ❌ Strategy quality: POOR (negative Sharpe)")
	}

	// Per-signal breakdown
	fmt.Println()
	fmt.Println("  Per-signal breakdown:")
	sigStats := make(map[string][2]int) // signal → [wins, total]
	for _, r := range results {
		cur := sigStats[r.Signal]
		cur[1]++
		if r.Won {
			cur[0]++
		}
		sigStats[r.Signal] = cur
	}
	for sig, st := range sigStats {
		wr := float64(st[0]) / float64(st[1]) * 100
		fmt.Printf("    %-14s %d/%d bets  (%.0f%% win rate)\n",
			sig+":", st[0], st[1], wr)
	}

	fmt.Println(sep)
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
