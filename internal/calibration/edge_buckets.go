// edge_buckets.go — TASK-232: edge-tier win-rate validator.
//
// Splits resolved bets into four edge buckets based on how large our
// predicted edge was (OurProbability - MarketPrice). The goal is to verify
// that bets with larger predicted edge actually produce better outcomes —
// a fundamental sanity-check for the prediction pipeline.
//
// Buckets:
//   <5%   — marginal edge (should barely be profitable)
//   5-10% — moderate edge
//   10-15%— strong edge
//   >15%  — high edge (should be our most profitable tier)
package calibration

import "fmt"

// EdgeBucket holds aggregated stats for one edge-size tier.
type EdgeBucket struct {
	Label       string  // e.g. "5-10%"
	MinEdge     float64 // inclusive lower bound (fraction, e.g. 0.05)
	MaxEdge     float64 // exclusive upper bound (fraction, e.g. 0.10); -1 = unbounded
	Count       int
	Wins        int
	PnL         float64
	TotalRisked float64
	BrierSum    float64
}

// WinPct returns win percentage (0–100), or -1 when Count == 0.
func (b EdgeBucket) WinPct() float64 {
	if b.Count == 0 {
		return -1
	}
	return float64(b.Wins) / float64(b.Count) * 100
}

// ROIPct returns ROI as a percentage, or math.NaN when TotalRisked == 0.
func (b EdgeBucket) ROIPct() float64 {
	if b.TotalRisked == 0 {
		return 0
	}
	return b.PnL / b.TotalRisked * 100
}

// AvgBrier returns the mean Brier score for this bucket, or -1 when Count == 0.
func (b EdgeBucket) AvgBrier() float64 {
	if b.Count == 0 {
		return -1
	}
	return b.BrierSum / float64(b.Count)
}

// edgeBucketDefs defines the four canonical tiers.
var edgeBucketDefs = []struct {
	label   string
	minEdge float64
	maxEdge float64 // -1 = no upper bound
}{
	{"<5%", 0.00, 0.05},
	{"5-10%", 0.05, 0.10},
	{"10-15%", 0.10, 0.15},
	{">15%", 0.15, -1},
}

// ComputeEdgeBuckets returns four EdgeBucket values (always four, even if empty)
// based on all resolved bets where edge > 0.
//
// Only positive-edge bets are included (bets placed because we expected
// OurProbability > MarketPrice). Bets with zero or negative edge are skipped
// since they violate the bot's entry rule.
func ComputeEdgeBuckets(records []BetRecord) []EdgeBucket {
	buckets := make([]EdgeBucket, len(edgeBucketDefs))
	for i, def := range edgeBucketDefs {
		buckets[i] = EdgeBucket{
			Label:   def.label,
			MinEdge: def.minEdge,
			MaxEdge: def.maxEdge,
		}
	}

	for _, r := range records {
		if r.Outcome == nil {
			continue
		}
		edge := r.OurProbability - r.MarketPrice
		if edge <= 0 {
			continue
		}

		idx := edgeBucketIndex(edge)
		if idx < 0 {
			continue
		}

		buckets[idx].Count++
		buckets[idx].TotalRisked += r.SizeUSDC

		brier := (r.OurProbability - outcomeToFloat(*r.Outcome)) * (r.OurProbability - outcomeToFloat(*r.Outcome))
		buckets[idx].BrierSum += brier

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

// edgeBucketIndex returns the index (0–3) for the given edge fraction.
func edgeBucketIndex(edge float64) int {
	for i, def := range edgeBucketDefs {
		if edge >= def.minEdge && (def.maxEdge < 0 || edge < def.maxEdge) {
			return i
		}
	}
	return -1
}

// outcomeToFloat converts a boolean outcome to 0.0 or 1.0.
func outcomeToFloat(b bool) float64 {
	if b {
		return 1.0
	}
	return 0.0
}

// EdgeValidation returns a short summary line confirming (or warning about)
// whether larger-edge bets actually outperform smaller-edge bets.
//
// Returns (message, validated). validated is true when the data supports the
// hypothesis that edge predicts outcome quality.
func EdgeValidation(buckets []EdgeBucket) (string, bool) {
	// Collect non-empty buckets.
	type point struct {
		midEdge float64
		winPct  float64
	}
	var pts []point
	for _, b := range buckets {
		if b.Count < 3 {
			continue
		}
		mid := b.MinEdge
		if b.MaxEdge > 0 {
			mid = (b.MinEdge + b.MaxEdge) / 2
		} else {
			mid = b.MinEdge + 0.10
		}
		pts = append(pts, point{mid, b.WinPct()})
	}

	totalBets := 0
	for _, b := range buckets {
		totalBets += b.Count
	}
	if totalBets < 10 {
		return fmt.Sprintf("⏳ Need more data (%d resolved bets, target 10+)", totalBets), false
	}

	// Check if win rate is monotonically increasing with edge.
	// A simple check: first non-empty vs last non-empty bucket win rate.
	if len(pts) < 2 {
		return "⏳ Too few distinct edge buckets to validate", false
	}
	first, last := pts[0], pts[len(pts)-1]
	if last.winPct > first.winPct {
		return fmt.Sprintf("✅ Edge validated: larger edge → higher win rate (%.0f%% → %.0f%%)", first.winPct, last.winPct), true
	}
	return fmt.Sprintf("⚠️ Edge not validated: win rate does not increase with edge (%.0f%% → %.0f%%)", first.winPct, last.winPct), false
}
