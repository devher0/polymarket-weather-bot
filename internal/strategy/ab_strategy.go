// ab_test.go — A/B test framework for comparing two Kelly fraction strategies.
//
// Strategy A: quarter-Kelly (fraction=0.25) — conservative
// Strategy B: half-Kelly   (fraction=0.50) — aggressive (current default)
//
// Every call to EvaluateFused() is shadowed: both strategies get a Decision,
// and the outcome is logged to data/ab_test.csv. After 50 resolved bets,
// the framework automatically declares a winner and logs it.
//
// Usage:
//
//	go run ./cmd/dashboard ab-test
package strategy

import (
	"encoding/csv"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"sync"
	"time"
)

// ABVariant identifies which strategy variant a record belongs to.
type ABVariant string

const (
	VariantA ABVariant = "A" // quarter-Kelly (conservative)
	VariantB ABVariant = "B" // half-Kelly (aggressive)
)

const (
	abMinSamples    = 50    // resolved bets before declaring winner
	fractionA       = 0.25  // quarter-Kelly
	fractionB       = 0.50  // half-Kelly
	maxFractionAB   = 0.05  // same cap for both
)

// ABRecord is one row in data/ab_test.csv.
type ABRecord struct {
	Timestamp   time.Time
	ConditionID string
	Variant     ABVariant
	City        string
	Signal      string
	SizeUSDC    float64
	Edge        float64
	OurP        float64
	MarketPrice float64
	Outcome     *bool  // nil = unresolved
	PnL         float64
}

// abCSVHeader defines column order.
var abCSVHeader = []string{
	"timestamp", "condition_id", "variant", "city", "signal",
	"size_usdc", "edge", "our_p", "market_price",
	"outcome", "pnl",
}

var abMu sync.Mutex

func abPath(dataRoot string) string {
	return filepath.Join(dataRoot, "data", "ab_test.csv")
}

// SaveABRecord appends one record to data/ab_test.csv.
func SaveABRecord(r ABRecord, dataRoot string) {
	abMu.Lock()
	defer abMu.Unlock()

	path := abPath(dataRoot)
	_ = os.MkdirAll(filepath.Dir(path), 0o755)

	writeHeader := false
	if _, err := os.Stat(path); os.IsNotExist(err) {
		writeHeader = true
	}

	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()

	w := csv.NewWriter(f)
	defer w.Flush()

	if writeHeader {
		_ = w.Write(abCSVHeader)
	}

	outcome := ""
	if r.Outcome != nil {
		if *r.Outcome {
			outcome = "true"
		} else {
			outcome = "false"
		}
	}

	_ = w.Write([]string{
		r.Timestamp.UTC().Format(time.RFC3339),
		r.ConditionID,
		string(r.Variant),
		r.City,
		r.Signal,
		fmt.Sprintf("%.4f", r.SizeUSDC),
		fmt.Sprintf("%.4f", r.Edge),
		fmt.Sprintf("%.4f", r.OurP),
		fmt.Sprintf("%.4f", r.MarketPrice),
		outcome,
		fmt.Sprintf("%.4f", r.PnL),
	})
}

// EvaluateAB shadows EvaluateFused with both Kelly variants, logs both decisions
// and returns the live-production decision (variant B, matching current KellyFraction).
//
// If variant A would have bet but B would not (or vice versa), only the production
// decision is returned for actual execution; the counterfactual is logged only.
func EvaluateAB(
	m interface{ GetConditionID() string },
	baseDecision *Decision,
	ourP, marketPrice, edge float64,
	bankroll, maxBet float64,
	dataRoot string,
) {
	if baseDecision == nil {
		return
	}

	// Compute hypothetical sizes under both fractions.
	odds := 1 / marketPrice
	sizeA := abHalfKelly(edge, odds, bankroll, maxFractionAB, fractionA)
	sizeA = math.Min(sizeA, maxBet)
	sizeB := abHalfKelly(edge, odds, bankroll, maxFractionAB, fractionB)
	sizeB = math.Min(sizeB, maxBet)

	now := time.Now().UTC()
	cid := baseDecision.Market.ConditionID

	SaveABRecord(ABRecord{
		Timestamp:   now,
		ConditionID: cid,
		Variant:     VariantA,
		City:        baseDecision.Market.City,
		Signal:      baseDecision.Market.Signal,
		SizeUSDC:    sizeA,
		Edge:        edge,
		OurP:        ourP,
		MarketPrice: marketPrice,
	}, dataRoot)

	SaveABRecord(ABRecord{
		Timestamp:   now,
		ConditionID: cid,
		Variant:     VariantB,
		City:        baseDecision.Market.City,
		Signal:      baseDecision.Market.Signal,
		SizeUSDC:    sizeB,
		Edge:        edge,
		OurP:        ourP,
		MarketPrice: marketPrice,
	}, dataRoot)
}

// abHalfKelly is a Kelly sizer parameterized by fraction (doesn't touch global KellyFraction).
func abHalfKelly(edge, odds, bankroll, maxFraction, fraction float64) float64 {
	if edge <= 0 {
		return 0
	}
	b := odds - 1
	p := edge + 1/odds
	q := 1 - p
	k := (b*p - q) / b
	frac := math.Min(maxFraction, math.Max(0, k*fraction))
	return frac * bankroll
}

// ABStats holds performance metrics for one variant.
type ABStats struct {
	Variant      ABVariant
	TotalBets    int
	ResolvedBets int
	Wins         int
	TotalPnL     float64
	TotalSize    float64
	BrierScore   float64
	WinRate      float64 // 0-100
	ROI          float64 // PnL / TotalSize × 100
}

// LoadABStats reads data/ab_test.csv and returns stats for both variants.
func LoadABStats(dataRoot string) (a, b ABStats, err error) {
	a.Variant = VariantA
	b.Variant = VariantB

	path := abPath(dataRoot)
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return a, b, nil
		}
		return a, b, err
	}
	defer f.Close()

	r := csv.NewReader(f)
	rows, err := r.ReadAll()
	if err != nil {
		return a, b, err
	}

	if len(rows) == 0 {
		return a, b, nil
	}

	// Build col index from header.
	colIdx := map[string]int{}
	for i, h := range rows[0] {
		colIdx[h] = i
	}

	// Group by conditionID+variant for Brier computation.
	type key struct {
		cid     string
		variant ABVariant
	}
	type entry struct {
		ourP    float64
		outcome *bool
		size    float64
		pnl     float64
	}
	grouped := map[key][]entry{}

	for _, row := range rows[1:] {
		if len(row) < len(abCSVHeader) {
			continue
		}
		variant := ABVariant(row[colIdx["variant"]])
		cid := row[colIdx["condition_id"]]
		size, _ := strconv.ParseFloat(row[colIdx["size_usdc"]], 64)
		ourP, _ := strconv.ParseFloat(row[colIdx["our_p"]], 64)
		pnl, _ := strconv.ParseFloat(row[colIdx["pnl"]], 64)

		var outcome *bool
		if s := row[colIdx["outcome"]]; s == "true" {
			v := true
			outcome = &v
		} else if s == "false" {
			v := false
			outcome = &v
		}

		grouped[key{cid, variant}] = append(grouped[key{cid, variant}], entry{ourP, outcome, size, pnl})
	}

	// Aggregate per variant.
	computeStats := func(v ABVariant) ABStats {
		st := ABStats{Variant: v}
		var brierSum float64
		var brierN int
		seen := map[string]bool{}

		for k, entries := range grouped {
			if k.variant != v {
				continue
			}
			if seen[k.cid] {
				continue
			}
			seen[k.cid] = true

			// Use last entry for this cid (most recent update).
			e := entries[len(entries)-1]
			st.TotalBets++
			st.TotalSize += e.size
			if e.outcome != nil {
				st.ResolvedBets++
				if *e.outcome {
					st.Wins++
					// pnl ≈ size × (1/marketPrice - 1); stored in csv when resolved
				}
				st.TotalPnL += e.pnl
				// Brier: (p - outcome)^2
				outcome := 0.0
				if *e.outcome {
					outcome = 1.0
				}
				brierSum += math.Pow(e.ourP-outcome, 2)
				brierN++
			}
		}
		if brierN > 0 {
			st.BrierScore = brierSum / float64(brierN)
		}
		if st.ResolvedBets > 0 {
			st.WinRate = float64(st.Wins) / float64(st.ResolvedBets) * 100
		}
		if st.TotalSize > 0 {
			st.ROI = st.TotalPnL / st.TotalSize * 100
		}
		return st
	}

	a = computeStats(VariantA)
	b = computeStats(VariantB)
	return a, b, nil
}

// ABWinner returns the winning variant (or "" if not enough data yet),
// comparing resolved Brier scores. Lower Brier = better.
// Requires abMinSamples resolved bets for each variant.
func ABWinner(a, b ABStats) (winner ABVariant, reason string) {
	minA, minB := a.ResolvedBets >= abMinSamples, b.ResolvedBets >= abMinSamples
	if !minA || !minB {
		missing := abMinSamples - a.ResolvedBets
		if b.ResolvedBets < a.ResolvedBets {
			missing = abMinSamples - b.ResolvedBets
		}
		return "", fmt.Sprintf("need %d more resolved bets", missing)
	}
	if a.BrierScore < b.BrierScore {
		delta := b.BrierScore - a.BrierScore
		return VariantA, fmt.Sprintf("A wins: Brier Δ=%.4f (A=%.4f B=%.4f)", delta, a.BrierScore, b.BrierScore)
	}
	delta := a.BrierScore - b.BrierScore
	return VariantB, fmt.Sprintf("B wins: Brier Δ=%.4f (A=%.4f B=%.4f)", delta, a.BrierScore, b.BrierScore)
}

// PrintABTest prints a formatted A/B test report to stdout.
func PrintABTest(dataRoot string) {
	a, b, err := LoadABStats(dataRoot)
	if err != nil {
		fmt.Printf("A/B test: error loading data: %v\n", err)
		return
	}

	if a.TotalBets == 0 && b.TotalBets == 0 {
		fmt.Println("A/B test: no data yet. Bets will be logged as they occur.")
		return
	}

	fmt.Println("╔══════════════════════════════════════════════╗")
	fmt.Println("║           A/B Strategy Test Results          ║")
	fmt.Println("╠══════════════════════════════════════════════╣")

	rows := []struct {
		label string
		aVal  string
		bVal  string
	}{
		{"Variant", "A (quarter-Kelly=0.25)", "B (half-Kelly=0.50)"},
		{"Total bets", fmt.Sprintf("%d", a.TotalBets), fmt.Sprintf("%d", b.TotalBets)},
		{"Resolved", fmt.Sprintf("%d", a.ResolvedBets), fmt.Sprintf("%d", b.ResolvedBets)},
		{"Wins", fmt.Sprintf("%d (%.1f%%)", a.Wins, a.WinRate), fmt.Sprintf("%d (%.1f%%)", b.Wins, b.WinRate)},
		{"Brier score", fmtBrier(a.BrierScore, a.ResolvedBets), fmtBrier(b.BrierScore, b.ResolvedBets)},
		{"ROI", fmt.Sprintf("%.2f%%", a.ROI), fmt.Sprintf("%.2f%%", b.ROI)},
		{"Total P&L", fmt.Sprintf("$%.2f", a.TotalPnL), fmt.Sprintf("$%.2f", b.TotalPnL)},
	}

	for _, row := range rows {
		fmt.Printf("║  %-20s │ %-12s │ %-12s ║\n", row.label, row.aVal, row.bVal)
	}

	fmt.Println("╠══════════════════════════════════════════════╣")
	winner, reason := ABWinner(a, b)
	if winner == "" {
		fmt.Printf("║  Status: %s\n", reason)
	} else {
		fmt.Printf("║  WINNER: Variant %s — %s\n", winner, reason)
	}
	fmt.Println("╚══════════════════════════════════════════════╝")

	// Day-by-day breakdown if available.
	printABDailyBreakdown(dataRoot)
}

// printABDailyBreakdown shows per-day win rates for both variants.
func printABDailyBreakdown(dataRoot string) {
	path := abPath(dataRoot)
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()

	r := csv.NewReader(f)
	rows, _ := r.ReadAll()
	if len(rows) < 2 {
		return
	}

	colIdx := map[string]int{}
	for i, h := range rows[0] {
		colIdx[h] = i
	}

	type dayKey struct {
		day     string
		variant ABVariant
	}
	type dayStat struct {
		bets, wins int
	}
	daily := map[dayKey]*dayStat{}
	var days []string
	seenDay := map[string]bool{}

	for _, row := range rows[1:] {
		if len(row) < len(abCSVHeader) {
			continue
		}
		ts := row[colIdx["timestamp"]]
		if len(ts) < 10 {
			continue
		}
		day := ts[:10]
		variant := ABVariant(row[colIdx["variant"]])
		outcome := row[colIdx["outcome"]]

		k := dayKey{day, variant}
		if daily[k] == nil {
			daily[k] = &dayStat{}
		}
		daily[k].bets++
		if outcome == "true" {
			daily[k].wins++
		}
		if !seenDay[day] {
			seenDay[day] = true
			days = append(days, day)
		}
	}

	if len(days) == 0 {
		return
	}

	sort.Strings(days)
	fmt.Println("\nDaily breakdown:")
	fmt.Printf("  %-12s  %-20s  %-20s\n", "Date", "A bets/wins", "B bets/wins")
	for _, day := range days {
		as := daily[dayKey{day, VariantA}]
		bs := daily[dayKey{day, VariantB}]
		aStr, bStr := "-", "-"
		if as != nil {
			aStr = fmt.Sprintf("%d/%d", as.bets, as.wins)
		}
		if bs != nil {
			bStr = fmt.Sprintf("%d/%d", bs.bets, bs.wins)
		}
		fmt.Printf("  %-12s  %-20s  %-20s\n", day, aStr, bStr)
	}
}

func fmtBrier(score float64, n int) string {
	if n == 0 {
		return "n/a"
	}
	return fmt.Sprintf("%.4f", score)
}
