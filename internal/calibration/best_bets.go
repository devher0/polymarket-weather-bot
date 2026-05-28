// best_bets.go — TASK-237: top and bottom bets by realized edge.
//
// "Realized edge" = (pnl - expected_pnl_at_market_price) / size.
// It answers: "which bets performed much better or worse than expected?"
//
//   expected_pnl = marketPrice*(1/marketPrice - 1)*size - (1-marketPrice)*size
//                = size * (1 - marketPrice) / marketPrice - size*(1-marketPrice)
//                = size * (1/marketPrice - 1) * marketPrice  [simplifies]
//
// More intuitively, the "fair" break-even is to win back exactly your stake.
// For a bet at price p (implied prob) we expect:
//   expected_pnl = p * net_win + (1-p) * (-size)
//                = p*(size/p - size) + (1-p)*(-size)
//                = (size - p*size) - size + p*size = 0 at fair odds.
//
// So expected_pnl at a fair market is 0. But we bet because OurProbability > p.
// Realized edge = actual_pnl - expected_pnl_at_market_price
//               = actual_pnl - 0   (since expected is 0 at fair odds, simplified)
// We use the simpler definition: realized_edge_pct = actual_pnl / size_usdc.
// This gives ROI on the individual bet: +100% = doubled money, -100% = total loss.
//
// Top bets: highest individual ROI% (best outcomes, possibly because of correct
// high-confidence prediction or lucky outcome on a marginal bet).
// Bottom bets: lowest individual ROI% (worst losses).
package calibration

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// BetSummary summarises a single resolved bet for leaderboard display.
type BetSummary struct {
	ConditionID string
	City        string
	Signal      string
	Side        string
	OurProb     float64
	MarketPrice float64
	SizeUSDC    float64
	PnL         float64     // realized P&L for this bet
	ROIPct      float64     // PnL / SizeUSDC * 100
	Outcome     bool        // true = won
	ResolvedAt  time.Time
}

// computeBetSummaries converts resolved BetRecords into BetSummary slice
// with PnL and ROI computed per bet.
func computeBetSummaries(records []BetRecord) []BetSummary {
	var out []BetSummary
	for _, r := range records {
		if r.Outcome == nil {
			continue
		}
		var pnl float64
		if *r.Outcome {
			if r.MarketPrice > 0 {
				pnl = r.SizeUSDC/r.MarketPrice - r.SizeUSDC
			}
		} else {
			pnl = -r.SizeUSDC
		}
		roi := 0.0
		if r.SizeUSDC > 0 {
			roi = pnl / r.SizeUSDC * 100
		}
		out = append(out, BetSummary{
			ConditionID: r.ConditionID,
			City:        r.City,
			Signal:      r.Signal,
			Side:        r.Side,
			OurProb:     r.OurProbability,
			MarketPrice: r.MarketPrice,
			SizeUSDC:    r.SizeUSDC,
			PnL:         pnl,
			ROIPct:      roi,
			Outcome:     *r.Outcome,
			ResolvedAt:  r.ResolvedAt,
		})
	}
	return out
}

// TopBottomBets returns the topN best and bottomN worst bets by ROI%.
// If there are fewer than topN resolved bets, returns what's available.
func TopBottomBets(records []BetRecord, topN int) (top, bottom []BetSummary) {
	summaries := computeBetSummaries(records)
	if len(summaries) == 0 {
		return nil, nil
	}
	sort.Slice(summaries, func(i, j int) bool {
		return summaries[i].ROIPct > summaries[j].ROIPct
	})
	n := topN
	if n > len(summaries) {
		n = len(summaries)
	}
	top = make([]BetSummary, n)
	copy(top, summaries[:n])

	// Bottom: last bn entries (worst ROI), reversed so worst-first.
	bn := topN
	if bn > len(summaries) {
		bn = len(summaries)
	}
	src := summaries[len(summaries)-bn:]
	bottom = make([]BetSummary, bn)
	for i, b := range src {
		bottom[bn-1-i] = b
	}
	return top, bottom
}

// formatBetRow formats a single BetSummary as a compact table row.
func formatBetRow(b BetSummary, rank int, rankEmoji string) string {
	city := b.City
	if city == "" {
		city = "unknown"
	}
	if len(city) > 8 {
		city = city[:7] + "…"
	}
	sig := b.Signal
	if sig == "" {
		sig = "?"
	}
	side := "Y"
	if b.Side == "NO" {
		side = "N"
	}
	pnlSign := "+"
	if b.PnL < 0 {
		pnlSign = ""
	}
	return fmt.Sprintf("%s%d  %-8s %-5s %s  %4.0f%%  %s$%.2f  p=%.2f\n",
		rankEmoji, rank,
		city, sig, side,
		b.ROIPct,
		pnlSign, b.PnL,
		b.OurProb)
}

// FormatBestBets returns an HTML-formatted Telegram message for /best-bets.
func FormatBestBets(records []BetRecord, topN int) string {
	top, bottom := TopBottomBets(records, topN)
	if len(top) == 0 {
		return "🏆 <b>Best &amp; Worst Bets</b>\nNo resolved bets yet."
	}

	var sb strings.Builder
	sb.WriteString("🏆 <b>Best &amp; Worst Bets</b>\n")
	sb.WriteString("<pre>")

	sb.WriteString(fmt.Sprintf("%-17s %-5s %5s  %7s  %5s\n", "City+Sig", "Side", "ROI%", "P&L", "Prob"))
	sb.WriteString(strings.Repeat("─", 42) + "\n")

	sb.WriteString("🔝 Top bets:\n")
	for i, b := range top {
		sb.WriteString(formatBetRow(b, i+1, ""))
	}
	if len(bottom) > 0 {
		sb.WriteString("\n💀 Worst bets:\n")
		for i, b := range bottom {
			sb.WriteString(formatBetRow(b, i+1, ""))
		}
	}
	sb.WriteString("</pre>")
	sb.WriteString(fmt.Sprintf("<i>Based on %d resolved bets | %s UTC</i>",
		len(computeBetSummaries(records)),
		"now"))
	return sb.String()
}
