// drawdown.go — peak-to-trough bankroll drawdown guard.
//
// Tracks the highest bankroll value ever observed (peak) and computes the
// current drawdown fraction. When the drawdown exceeds a configured threshold,
// the effective bankroll used for bet sizing is scaled down proportionally.
// This acts as an automatic circuit-breaker: the bot keeps running but with
// progressively smaller stakes until the bankroll recovers.
//
// Persistence: data/bankroll_peak.json
//
// Drawdown thresholds:
//   < 10%           → no reduction (multiplier 1.00)
//   10% – threshold → linear reduction from 1.00 → 0.20
//   > threshold     → minimum multiplier 0.20 (safety floor)
package calibration

import (
	"encoding/json"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"time"
)

type bankrollPeak struct {
	PeakUSDC  float64   `json:"peak_usdc"`
	UpdatedAt time.Time `json:"updated_at"`
}

func peakPath(dataRoot string) string {
	if dataRoot == "" {
		dataRoot = "."
	}
	return filepath.Join(dataRoot, "bankroll_peak.json")
}

// LoadPeakBankroll returns the persisted all-time peak bankroll (USDC).
// Returns 0 if the file does not exist.
func LoadPeakBankroll(dataRoot string) float64 {
	data, err := os.ReadFile(peakPath(dataRoot))
	if err != nil {
		return 0
	}
	var bp bankrollPeak
	if err := json.Unmarshal(data, &bp); err != nil {
		return 0
	}
	return bp.PeakUSDC
}

// UpdatePeakBankroll writes a new peak to disk if current > saved peak.
// Returns the current peak value (whether updated or not).
func UpdatePeakBankroll(current float64, dataRoot string) (float64, error) {
	peak := LoadPeakBankroll(dataRoot)
	if current <= peak {
		return peak, nil // no new peak
	}
	bp := bankrollPeak{PeakUSDC: current, UpdatedAt: time.Now().UTC()}
	data, err := json.MarshalIndent(bp, "", "  ")
	if err != nil {
		return peak, err
	}
	path := peakPath(dataRoot)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return peak, err
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return peak, err
	}
	slog.Info("bankroll: new peak recorded", "peak_usdc", current)
	return current, nil
}

// DrawdownFraction returns the fraction of peak bankroll that has been lost.
//
//	peak=1000, current=700 → 0.30 (30% drawdown)
//	current >= peak         → 0.0  (no drawdown)
func DrawdownFraction(peakBankroll, currentBankroll float64) float64 {
	if peakBankroll <= 0 || currentBankroll >= peakBankroll {
		return 0
	}
	return (peakBankroll - currentBankroll) / peakBankroll
}

// DrawdownMultiplier returns the fraction by which to scale effective bankroll
// given the current drawdown and the configured maximum-drawdown threshold.
//
//   - drawdown < 10%                     → 1.00 (no reduction)
//   - 10% ≤ drawdown < maxDrawdown       → linear 1.00 → 0.20
//   - drawdown ≥ maxDrawdown             → 0.20 (minimum, safety floor)
//   - maxDrawdownFraction == 0           → 1.00 (feature disabled)
func DrawdownMultiplier(drawdownFraction, maxDrawdownFraction float64) float64 {
	if maxDrawdownFraction <= 0 {
		return 1.0
	}
	const (
		noReductionThreshold = 0.10
		minMultiplier        = 0.20
	)
	if drawdownFraction <= noReductionThreshold {
		return 1.0
	}
	if drawdownFraction >= maxDrawdownFraction {
		return minMultiplier
	}
	// Linear interpolation: 1.00 at noReductionThreshold, minMultiplier at maxDrawdownFraction.
	t := (drawdownFraction - noReductionThreshold) / (maxDrawdownFraction - noReductionThreshold)
	m := 1.0 - t*(1.0-minMultiplier)
	return math.Max(minMultiplier, m)
}

// LogDrawdown emits a warning log if the drawdown is noteworthy (> 5%).
func LogDrawdown(peak, current float64) {
	fraction := DrawdownFraction(peak, current)
	if fraction < 0.05 {
		return
	}
	slog.Warn("bankroll drawdown detected",
		"peak_usdc", math.Round(peak*100)/100,
		"current_usdc", math.Round(current*100)/100,
		"drawdown_pct", math.Round(fraction*1000)/10,
	)
}
