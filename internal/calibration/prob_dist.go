// prob_dist.go — TASK-235: betting-range probability accuracy distribution.
//
// Groups resolved bets into six probability buckets covering the actual
// betting range (0.40–1.00) and compares the mean predicted probability
// to the empirical win rate within each bucket.
//
// The existing calibration_curve.go covers the full [0,1] range in 5 equal
// buckets; this file targets the narrower range where the bot actually bets,
// using finer granularity in the high-confidence zone.
//
// Buckets:
//   [0.40, 0.50) — marginal confidence
//   [0.50, 0.55) — low-mid confidence
//   [0.55, 0.60) — mid confidence
//   [0.60, 0.70) — good confidence
//   [0.70, 0.80) — high confidence
//   [0.80, 1.00] — very high confidence
package calibration

import (
	"fmt"
	"math"
	"strings"
)

// ProbBucket holds stats for one probability tier in the betting range.
type ProbBucket struct {
	Label      string  // human-readable range, e.g. "0.60–0.70"
	Lo, Hi     float64 // inclusive lower, exclusive upper (except last: inclusive)
	Count      int     // resolved bets in this bucket
	Wins       int
	PredSum    float64 // sum of OurProbability values (for AvgPred)
	PnL        float64
	TotalRisked float64
}

// AvgPred returns the mean predicted probability for bets in this bucket.
func (b ProbBucket) AvgPred() float64 {
	if b.Count == 0 {
		return 0
	}
	return b.PredSum / float64(b.Count)
}

// WinPct returns empirical win rate (0–100) or -1 when Count == 0.
func (b ProbBucket) WinPct() float64 {
	if b.Count == 0 {
		return -1
	}
	return float64(b.Wins) / float64(b.Count) * 100
}

// ROIPct returns ROI percentage, or 0 when TotalRisked == 0.
func (b ProbBucket) ROIPct() float64 {
	if b.TotalRisked == 0 {
		return 0
	}
	return b.PnL / b.TotalRisked * 100
}

// CalibrationGap returns AvgPred − WinRate (fraction), indicating over- or
// under-confidence. Positive = overconfident; negative = underconfident.
// Returns 0 when Count == 0.
func (b ProbBucket) CalibrationGap() float64 {
	if b.Count == 0 {
		return 0
	}
	return b.AvgPred() - b.WinPct()/100
}

var probBucketDefs = []struct {
	label  string
	lo, hi float64
}{
	{"0.40–0.50", 0.40, 0.50},
	{"0.50–0.55", 0.50, 0.55},
	{"0.55–0.60", 0.55, 0.60},
	{"0.60–0.70", 0.60, 0.70},
	{"0.70–0.80", 0.70, 0.80},
	{"0.80–1.00", 0.80, 1.01},
}

// BuildProbDist groups resolved bets by our predicted probability and returns
// one ProbBucket per tier (always 6 entries). Unresolved bets are ignored.
func BuildProbDist(records []BetRecord) []ProbBucket {
	buckets := make([]ProbBucket, len(probBucketDefs))
	for i, d := range probBucketDefs {
		buckets[i] = ProbBucket{Label: d.label, Lo: d.lo, Hi: d.hi}
	}

	for _, r := range records {
		if r.Outcome == nil {
			continue
		}
		p := r.OurProbability
		if p < 0.40 {
			continue // below betting threshold — skip
		}
		idx := -1
		for i, d := range probBucketDefs {
			if p >= d.lo && p < d.hi {
				idx = i
				break
			}
		}
		if idx < 0 {
			continue
		}
		buckets[idx].Count++
		buckets[idx].PredSum += p
		buckets[idx].TotalRisked += r.SizeUSDC
		if *r.Outcome {
			buckets[idx].Wins++
			if r.MarketPrice > 0 {
				buckets[idx].PnL += r.SizeUSDC/r.MarketPrice - r.SizeUSDC
			}
		} else {
			buckets[idx].PnL -= r.SizeUSDC
		}
	}
	return buckets
}

// ProbDistECE returns the Expected Calibration Error across all non-empty
// buckets: weighted mean of |AvgPred − WinRate|.
func ProbDistECE(buckets []ProbBucket) float64 {
	var totalWeight, weightedErr float64
	for _, b := range buckets {
		if b.Count == 0 {
			continue
		}
		totalWeight += float64(b.Count)
		weightedErr += float64(b.Count) * math.Abs(b.CalibrationGap())
	}
	if totalWeight == 0 {
		return 0
	}
	return weightedErr / totalWeight
}

// FormatProbDist returns an HTML-formatted Telegram string for /prob-dist.
func FormatProbDist(buckets []ProbBucket) string {
	total := 0
	for _, b := range buckets {
		total += b.Count
	}
	if total == 0 {
		return "📊 <b>Probability Distribution</b>\nNo resolved bets yet."
	}

	ece := ProbDistECE(buckets)
	var sb strings.Builder
	sb.WriteString("📊 <b>Probability Distribution</b>\n")
	sb.WriteString("<pre>")
	sb.WriteString(fmt.Sprintf("%-11s %4s %5s %5s %5s %5s\n",
		"Range", "N", "Pred", "Win%", "Gap", "ROI%"))
	sb.WriteString(strings.Repeat("─", 44) + "\n")

	for _, b := range buckets {
		if b.Count == 0 {
			sb.WriteString(fmt.Sprintf("%-11s %4s %5s %5s %5s %5s\n",
				b.Label, "—", "—", "—", "—", "—"))
			continue
		}
		gap := b.CalibrationGap() * 100
		gapSign := "+"
		if gap < 0 {
			gapSign = ""
		}
		roiSign := "+"
		if b.ROIPct() < 0 {
			roiSign = ""
		}
		var calibIcon string
		switch {
		case math.Abs(gap) < 3:
			calibIcon = "✅"
		case math.Abs(gap) < 7:
			calibIcon = "🟡"
		default:
			calibIcon = "❌"
		}
		sb.WriteString(fmt.Sprintf("%-11s %4d %4.0f%% %4.0f%% %s%3.0f%% %s%.0f%% %s\n",
			b.Label, b.Count,
			b.AvgPred()*100, b.WinPct(),
			gapSign, gap,
			roiSign, b.ROIPct(),
			calibIcon))
	}
	sb.WriteString("</pre>")
	sb.WriteString(fmt.Sprintf("ECE: <b>%.3f</b> — %s\n",
		ece, eceLabel(ece)))
	return sb.String()
}

func eceLabel(ece float64) string {
	switch {
	case ece < 0.03:
		return "✅ excellent calibration"
	case ece < 0.07:
		return "🟡 moderate calibration"
	default:
		return "❌ poor calibration — review model"
	}
}
