// pnl_city.go — per-city and per-signal P&L breakdown.
// TASK-161
package calibration

import "sort"

// CityPnLStats holds resolved-bet P&L metrics for one city or signal.
type CityPnLStats struct {
	City     string
	Bets     int
	Wins     int
	PnLUSDC  float64 // cumulative realised P&L (positive = profit)
	TotalRisked float64 // sum of SizeUSDC across all bets
}

// WinRate returns win percentage (0–100), or 0 when Bets == 0.
func (s CityPnLStats) WinRate() float64 {
	if s.Bets == 0 {
		return 0
	}
	return float64(s.Wins) / float64(s.Bets) * 100
}

// ROI returns P&L / TotalRisked as a percentage, or 0 when TotalRisked == 0.
func (s CityPnLStats) ROI() float64 {
	if s.TotalRisked == 0 {
		return 0
	}
	return s.PnLUSDC / s.TotalRisked * 100
}

// CityPnL returns per-city P&L stats computed from resolved bet records.
// Unresolved records and records with empty City are skipped (not bucketed).
func CityPnL(records []BetRecord) []CityPnLStats {
	m := make(map[string]*CityPnLStats)
	for _, r := range records {
		if r.Outcome == nil || r.City == "" {
			continue
		}
		s, ok := m[r.City]
		if !ok {
			s = &CityPnLStats{City: r.City}
			m[r.City] = s
		}
		s.Bets++
		s.TotalRisked += r.SizeUSDC
		if *r.Outcome {
			s.Wins++
			s.PnLUSDC += r.SizeUSDC/r.MarketPrice - r.SizeUSDC
		} else {
			s.PnLUSDC -= r.SizeUSDC
		}
	}

	out := make([]CityPnLStats, 0, len(m))
	for _, v := range m {
		out = append(out, *v)
	}
	// Sort by P&L descending (most profitable first).
	sort.Slice(out, func(i, j int) bool {
		if out[i].PnLUSDC != out[j].PnLUSDC {
			return out[i].PnLUSDC > out[j].PnLUSDC
		}
		return out[i].City < out[j].City
	})
	return out
}

// SignalPnL returns per-signal P&L stats computed from resolved bet records.
// Records with empty Signal are skipped.
func SignalPnL(records []BetRecord) []CityPnLStats {
	m := make(map[string]*CityPnLStats)
	for _, r := range records {
		if r.Outcome == nil || r.Signal == "" {
			continue
		}
		s, ok := m[r.Signal]
		if !ok {
			s = &CityPnLStats{City: r.Signal}
			m[r.Signal] = s
		}
		s.Bets++
		s.TotalRisked += r.SizeUSDC
		if *r.Outcome {
			s.Wins++
			s.PnLUSDC += r.SizeUSDC/r.MarketPrice - r.SizeUSDC
		} else {
			s.PnLUSDC -= r.SizeUSDC
		}
	}

	out := make([]CityPnLStats, 0, len(m))
	for _, v := range m {
		out = append(out, *v)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].PnLUSDC != out[j].PnLUSDC {
			return out[i].PnLUSDC > out[j].PnLUSDC
		}
		return out[i].City < out[j].City
	})
	return out
}
