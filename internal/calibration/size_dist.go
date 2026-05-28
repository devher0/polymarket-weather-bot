// size_dist.go — TASK-236: bet-size tier performance analysis.
//
// Groups resolved bets into four size buckets and reports win rate, P&L,
// and ROI% per bucket. The hypothesis is that the Kelly formula produces
// larger bets for higher-confidence situations — so larger bets should
// have a better win rate if the confidence model is working correctly.
//
// Buckets: <$1 | $1–$2 | $2–$5 | $5+
package calibration

import (
	"fmt"
	"strings"
)

// SizeBucket holds aggregated stats for one bet-size tier.
type SizeBucket struct {
	Label       string  // e.g. "$1–$2"
	MaxSize     float64 // exclusive upper bound; 0 = unbounded (last bucket)
	Count       int     // resolved bets
	Wins        int
	PnL         float64
	TotalRisked float64
}

// WinPct returns win percentage (0–100), or -1 when Count == 0.
func (s SizeBucket) WinPct() float64 {
	if s.Count == 0 {
		return -1
	}
	return float64(s.Wins) / float64(s.Count) * 100
}

// ROIPct returns ROI as a percentage, or 0 when TotalRisked == 0.
func (s SizeBucket) ROIPct() float64 {
	if s.TotalRisked == 0 {
		return 0
	}
	return s.PnL / s.TotalRisked * 100
}

// AvgSize returns the average bet size for this bucket.
func (s SizeBucket) AvgSize() float64 {
	if s.Count == 0 {
		return 0
	}
	return s.TotalRisked / float64(s.Count)
}

var sizeBucketDefs = []struct {
	label   string
	minSize float64
	maxSize float64 // 0 means unbounded
}{
	{"<$1", 0, 1.0},
	{"$1–$2", 1.0, 2.0},
	{"$2–$5", 2.0, 5.0},
	{"$5+", 5.0, 0},
}

// ComputeSizeBuckets groups resolved bets by SizeUSDC and returns one
// SizeBucket per tier. Unresolved bets are ignored.
func ComputeSizeBuckets(records []BetRecord) []SizeBucket {
	buckets := make([]SizeBucket, len(sizeBucketDefs))
	for i, d := range sizeBucketDefs {
		buckets[i] = SizeBucket{Label: d.label, MaxSize: d.maxSize}
	}

	for _, r := range records {
		if r.Outcome == nil {
			continue
		}
		sz := r.SizeUSDC
		idx := -1
		for i, d := range sizeBucketDefs {
			if sz >= d.minSize && (d.maxSize == 0 || sz < d.maxSize) {
				idx = i
				break
			}
		}
		if idx < 0 {
			continue
		}
		buckets[idx].Count++
		buckets[idx].TotalRisked += sz
		if *r.Outcome {
			buckets[idx].Wins++
			if r.MarketPrice > 0 {
				buckets[idx].PnL += sz/r.MarketPrice - sz
			}
		} else {
			buckets[idx].PnL -= sz
		}
	}
	return buckets
}

// SizeValidation checks whether larger bets produce better win rates —
// i.e. the Kelly formula is correctly sizing up on high-confidence bets.
// Returns (message, isValid). isValid is true when win rate is non-decreasing
// across buckets with ≥3 bets.
func SizeValidation(buckets []SizeBucket) (string, bool) {
	// Collect non-empty buckets
	var filled []SizeBucket
	for _, b := range buckets {
		if b.Count >= 3 {
			filled = append(filled, b)
		}
	}
	if len(filled) < 2 {
		return "⏳ Need ≥3 bets in at least 2 tiers for validation", false
	}
	for i := 1; i < len(filled); i++ {
		if filled[i].WinPct() < filled[i-1].WinPct()-5 {
			return fmt.Sprintf("⚠️ Larger bets NOT winning more (%s: %.0f%% < %s: %.0f%%)",
				filled[i].Label, filled[i].WinPct(),
				filled[i-1].Label, filled[i-1].WinPct()), false
		}
	}
	return "✅ Larger bets → higher win rate (Kelly sizing working)", true
}

// FormatSizeDist returns an HTML-formatted Telegram string for /size-dist.
func FormatSizeDist(buckets []SizeBucket) string {
	total := 0
	for _, b := range buckets {
		total += b.Count
	}
	if total == 0 {
		return "💰 <b>Bet Size Distribution</b>\nNo resolved bets yet."
	}

	msg, _ := SizeValidation(buckets)
	var sb strings.Builder
	sb.WriteString("💰 <b>Bet Size Distribution</b>\n")
	sb.WriteString("<pre>")
	sb.WriteString(fmt.Sprintf("%-7s %4s %5s %5s %7s %6s\n",
		"Size", "N", "Avg$", "Win%", "P&L", "ROI%"))
	sb.WriteString(strings.Repeat("─", 43) + "\n")

	for _, b := range buckets {
		if b.Count == 0 {
			sb.WriteString(fmt.Sprintf("%-7s %4s %5s %5s %7s %6s\n",
				b.Label, "—", "—", "—", "—", "—"))
			continue
		}
		pnlSign := "+"
		if b.PnL < 0 {
			pnlSign = ""
		}
		roiSign := "+"
		if b.ROIPct() < 0 {
			roiSign = ""
		}
		sb.WriteString(fmt.Sprintf("%-7s %4d %4.2f %4.0f%% %s%6.2f %s%.0f%%\n",
			b.Label, b.Count, b.AvgSize(), b.WinPct(),
			pnlSign, b.PnL,
			roiSign, b.ROIPct()))
	}
	sb.WriteString("</pre>")
	sb.WriteString(msg + "\n")
	return sb.String()
}
