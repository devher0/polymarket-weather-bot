// kelly_opt.go — TASK-183: empirical Kelly fraction optimizer.
//
// Runs a grid search over Kelly fractions (0.05 to 1.0) on the historical
// bet record, simulating cumulative log-growth for each fraction.  The
// fraction that maximises geometric growth rate (log of final bankroll) is
// returned as the recommendation.
//
// Usage:
//
//	result := calibration.OptimalKelly(records, 0.05, 1.0, 20)
//	fmt.Printf("optimal Kelly fraction: %.2f  (simulated PnL: %+.2f)\n", result.BestK, result.BestPnL)
package calibration

import (
	"fmt"
	"math"
)

// KellyStep holds the simulation result for one Kelly fraction value.
type KellyStep struct {
	K       float64 // fraction tested
	LogGrowth float64 // sum of log(1 + k*edge) over all bets (theoretical)
	SimPnL  float64 // simulated cumulative PnL starting from bankroll=100 USDC
}

// KellyOptResult holds the full grid-search result.
type KellyOptResult struct {
	BestK    float64
	BestLogG float64
	BestPnL  float64
	Steps    []KellyStep
}

// OptimalKelly performs a grid search over Kelly fractions in [start, end]
// using the given number of steps.
//
// Each step simulates reinvesting k * edge of the current bankroll on each
// resolved historical bet.  The fraction that maximises the final bankroll
// (equivalent to maximising log-growth) is returned.
//
// Requires at least minBets resolved records; returns zero BestK and an
// error message if there is insufficient data.
//
// Errors:
//   - returns err if len(resolved) < minBets
func OptimalKelly(records []BetRecord, start, end float64, steps int) (KellyOptResult, error) {
	const minBets = 10

	// Only consider resolved bets with meaningful data.
	var resolved []BetRecord
	for _, r := range records {
		if r.Outcome == nil {
			continue
		}
		if r.MarketPrice <= 0 || r.MarketPrice >= 1 {
			continue
		}
		if r.OurProbability <= 0 || r.OurProbability >= 1 {
			continue
		}
		resolved = append(resolved, r)
	}

	if len(resolved) < minBets {
		return KellyOptResult{}, fmt.Errorf("insufficient data: %d resolved bets (need %d)", len(resolved), minBets)
	}

	if steps < 1 {
		steps = 20
	}
	if end <= start {
		end = 1.0
	}

	result := KellyOptResult{}
	stepSize := (end - start) / float64(steps)

	bestBankroll := -math.MaxFloat64

	for i := 0; i <= steps; i++ {
		k := start + float64(i)*stepSize
		if k < 0.001 {
			k = 0.001
		}
		bankroll := 100.0 // start with 100 USDC
		logGrowth := 0.0

		for _, r := range resolved {
			// Kelly edge for this bet.
			p := r.OurProbability
			q := 1 - p
			b := (1 - r.MarketPrice) / r.MarketPrice // net odds if win

			// Full-Kelly fraction: (p*b - q) / b.
			// We scale by k (0.0 = skip, 1.0 = full Kelly).
			fullKelly := (p*b - q) / b
			if fullKelly <= 0 {
				// No positive edge on this bet.
				continue
			}
			fraction := k * fullKelly
			if fraction > 0.20 { // hard cap at 20% per bet for simulation safety
				fraction = 0.20
			}

			betSize := bankroll * fraction
			if *r.Outcome {
				bankroll += betSize * b
				logGrowth += math.Log(1 + fraction*b)
			} else {
				bankroll -= betSize
				if bankroll <= 0 {
					bankroll = 0.01 // ruin: stop simulation
					logGrowth = -math.MaxFloat64
					break
				}
				logGrowth += math.Log(1 - fraction)
			}
		}

		step := KellyStep{
			K:         k,
			LogGrowth: logGrowth,
			SimPnL:    bankroll - 100.0,
		}
		result.Steps = append(result.Steps, step)

		if bankroll > bestBankroll {
			bestBankroll = bankroll
			result.BestK = k
			result.BestLogG = logGrowth
			result.BestPnL = bankroll - 100.0
		}
	}

	return result, nil
}
