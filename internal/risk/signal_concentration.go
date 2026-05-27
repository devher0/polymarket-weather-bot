// signal_concentration.go — guards against over-concentration in a single
// signal type (rain, heat, cold, etc.) across all open positions.
//
// Motivation: if the rain-probability model is systematically biased, all
// "rain" bets lose simultaneously. Limiting any single signal type to at most
// MaxSignalExposurePct of total open exposure bounds the correlated loss.
//
// Usage:
//
//	err := mgr.CheckSignalConcentration(history, "rain", betSizeUSDC)
//	if err != nil { /* skip */ }
package risk

import (
	"fmt"
	"log/slog"

	"github.com/devher0/polymarket-weather-bot/internal/calibration"
)

// CheckSignalConcentration returns an error when placing a bet of newSizeUSDC
// on the given signal would cause that signal type to exceed MaxSignalExposurePct
// of total open exposure.
//
// Returns nil when MaxSignalExposurePct is 0 (disabled) or signal is empty.
func (m *Manager) CheckSignalConcentration(
	records []calibration.BetRecord,
	signal string,
	newSizeUSDC float64,
) error {
	if m.cfg.MaxSignalExposurePct <= 0 || signal == "" {
		return nil
	}

	var totalOpen, signalOpen float64
	for _, r := range records {
		if r.Outcome != nil {
			continue // only count open (unresolved) positions
		}
		totalOpen += r.SizeUSDC
		if r.Signal == signal {
			signalOpen += r.SizeUSDC
		}
	}

	newTotal := totalOpen + newSizeUSDC
	newSignal := signalOpen + newSizeUSDC

	if newTotal == 0 {
		return nil
	}

	pct := newSignal / newTotal
	if pct > m.cfg.MaxSignalExposurePct {
		return fmt.Errorf(
			"signal concentration: %s would reach %.0f%% of open exposure (max %.0f%%, current %.2f USDC / total %.2f USDC)",
			signal,
			pct*100,
			m.cfg.MaxSignalExposurePct*100,
			signalOpen,
			totalOpen,
		)
	}

	slog.Debug("signal concentration check: ok",
		"signal", signal,
		"signal_usdc", fmt.Sprintf("%.2f", signalOpen+newSizeUSDC),
		"total_usdc", fmt.Sprintf("%.2f", newTotal),
		"pct", fmt.Sprintf("%.1f%%", pct*100),
		"limit_pct", fmt.Sprintf("%.0f%%", m.cfg.MaxSignalExposurePct*100),
	)
	return nil
}

// SignalExposureBreakdown returns the total open USDC exposure per signal type
// across all unresolved positions. Useful for dashboard display and diagnostics.
// Signals without open positions are not included.
func SignalExposureBreakdown(records []calibration.BetRecord) map[string]float64 {
	out := make(map[string]float64)
	for _, r := range records {
		if r.Outcome == nil && r.Signal != "" {
			out[r.Signal] += r.SizeUSDC
		}
	}
	return out
}

// SignalConcentrationPct returns the fraction (0-1) of total open exposure
// occupied by the given signal. Returns 0 if there is no open exposure.
func SignalConcentrationPct(records []calibration.BetRecord, signal string) float64 {
	var totalOpen, signalOpen float64
	for _, r := range records {
		if r.Outcome != nil {
			continue
		}
		totalOpen += r.SizeUSDC
		if r.Signal == signal {
			signalOpen += r.SizeUSDC
		}
	}
	if totalOpen == 0 {
		return 0
	}
	return signalOpen / totalOpen
}
