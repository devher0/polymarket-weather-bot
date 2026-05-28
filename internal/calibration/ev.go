// ev.go — TASK-187: rolling expected-value capture ratio.
//
// Compares theoretical expected value (edge × size) against realized P&L.
// A capture ratio < 0.70 indicates calibration problems, market impact, or edge decay.
package calibration

import (
	"sort"
)

// EVResult holds the EV capture analysis for a window of resolved bets.
type EVResult struct {
	Count        int
	ExpectedEV   float64 // Σ (ourP - marketPrice) × size
	RealizedPnL  float64 // Σ actual P&L: win=(size/mktP - size), loss=-size
	CaptureRatio float64 // realizedPnL / expectedEV; 0 when expectedEV ≤ 0
}

// RollingEV computes EV capture for the last n resolved bets.
// If n == 0, all resolved bets are used.
// Returns a zero EVResult when there are no resolved bets.
func RollingEV(records []BetRecord, n int) EVResult {
	var resolved []BetRecord
	for _, r := range records {
		if r.Outcome != nil {
			resolved = append(resolved, r)
		}
	}
	if len(resolved) == 0 {
		return EVResult{}
	}

	sort.Slice(resolved, func(i, j int) bool {
		return resolved[i].Timestamp.Before(resolved[j].Timestamp)
	})
	if n > 0 && len(resolved) > n {
		resolved = resolved[len(resolved)-n:]
	}

	var expectedEV, realizedPnL float64
	for _, r := range resolved {
		edge := r.OurProbability - r.MarketPrice
		expectedEV += edge * r.SizeUSDC
		if *r.Outcome {
			if r.MarketPrice > 0 {
				realizedPnL += r.SizeUSDC*(1/r.MarketPrice-1)
			}
		} else {
			realizedPnL -= r.SizeUSDC
		}
	}

	var captureRatio float64
	if expectedEV > 0 {
		captureRatio = realizedPnL / expectedEV
	}

	return EVResult{
		Count:        len(resolved),
		ExpectedEV:   expectedEV,
		RealizedPnL:  realizedPnL,
		CaptureRatio: captureRatio,
	}
}

// RollingEVBySignal computes EV capture broken down by signal type.
// Only resolved bets with a non-empty Signal are included.
func RollingEVBySignal(records []BetRecord, n int) map[string]EVResult {
	// Group by signal.
	bySignal := make(map[string][]BetRecord)
	for _, r := range records {
		if r.Outcome != nil && r.Signal != "" {
			bySignal[r.Signal] = append(bySignal[r.Signal], r)
		}
	}
	results := make(map[string]EVResult, len(bySignal))
	for sig, recs := range bySignal {
		results[sig] = RollingEV(recs, n)
	}
	return results
}
