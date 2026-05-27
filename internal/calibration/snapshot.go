// Package calibration — snapshot.go
//
// TASK-074: Calibration model snapshot export.
//
// ExportSnapshot writes a JSON file capturing the current model state:
// overall Brier score, per-city and per-signal breakdown, adaptive edge
// factor, drawdown multiplier, and open positions count.
//
// The snapshot is useful for auditing model health, comparing across
// days, and feeding external dashboards.
//
// File location: data/calibration_snapshot.json (overwritten each call).
package calibration

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"
)

// SnapshotBreakdown captures performance for one dimension bucket (city or signal).
type SnapshotBreakdown struct {
	Count    int     `json:"count"`
	Wins     int     `json:"wins"`
	WinRate  float64 `json:"win_rate"`
	BrierAvg float64 `json:"brier_avg"`
}

// CalibrationSnapshot is the full model-state snapshot.
type CalibrationSnapshot struct {
	GeneratedAt string `json:"generated_at"` // RFC3339

	// Overall metrics
	TotalBets     int     `json:"total_bets"`
	ResolvedBets  int     `json:"resolved_bets"`
	OpenBets      int     `json:"open_bets"`
	OverallBrier  float64 `json:"overall_brier"`
	BrierQuality  string  `json:"brier_quality"`
	OverallWinRate float64 `json:"overall_win_rate"`

	// Adaptive parameters
	AdaptiveEdgeFactor float64 `json:"adaptive_edge_factor"` // factor relative to base (0.75–1.50)
	DrawdownPct        float64 `json:"drawdown_pct"`         // current drawdown vs peak (0–100)
	DrawdownMultiplier float64 `json:"drawdown_multiplier"`  // bet size multiplier (0.20–1.00)

	// Bankroll
	Bankroll     float64 `json:"bankroll_usdc"`
	PeakBankroll float64 `json:"peak_bankroll_usdc"`

	// Per-city breakdown (top cities by bet count)
	CityBreakdown map[string]SnapshotBreakdown `json:"city_breakdown"`

	// Per-signal breakdown
	SignalBreakdown map[string]SnapshotBreakdown `json:"signal_breakdown"`
}

// ExportSnapshot generates and writes a CalibrationSnapshot to
// data/calibration_snapshot.json inside dataRoot.
//
// Parameters:
//   - records: loaded bet history (nil is treated as empty)
//   - baseMinEdge: the configured minimum edge (used to show adaptive factor)
//   - maxDrawdownFraction: from config (for drawdown multiplier calc)
//   - dataRoot: root directory for data/ files
func ExportSnapshot(records []BetRecord, baseMinEdge, maxDrawdownFraction float64, dataRoot string) error {
	snap := CalibrationSnapshot{
		GeneratedAt: time.Now().UTC().Format(time.RFC3339),
	}

	if records == nil {
		records = []BetRecord{}
	}

	// Counts
	snap.TotalBets = len(records)
	open := 0
	wins := 0
	resolved := 0
	for _, r := range records {
		if r.Outcome == nil {
			open++
		} else {
			resolved++
			if *r.Outcome {
				wins++
			}
		}
	}
	snap.OpenBets = open
	snap.ResolvedBets = resolved
	if resolved > 0 {
		snap.OverallWinRate = float64(wins) / float64(resolved)
	}

	// Brier score
	brier, _, err := BrierScore(records)
	if err == nil {
		snap.OverallBrier = brier
		snap.BrierQuality = brierQuality(brier)
	}

	// Adaptive edge: compute effective factor vs base
	adaptiveEdge := AdaptiveMinEdge(records, baseMinEdge)
	if baseMinEdge > 0 {
		snap.AdaptiveEdgeFactor = adaptiveEdge / baseMinEdge
	} else {
		snap.AdaptiveEdgeFactor = 1.0
	}

	// Bankroll + drawdown
	bankroll := LoadBankroll(dataRoot)
	peak := LoadPeakBankroll(dataRoot)
	snap.Bankroll = bankroll
	snap.PeakBankroll = peak
	if peak > 0 {
		snap.DrawdownPct = DrawdownFraction(peak, bankroll) * 100
	}
	snap.DrawdownMultiplier = DrawdownMultiplier(DrawdownFraction(peak, bankroll), maxDrawdownFraction)

	// City breakdown
	cityRaw := CityBreakdown(records)
	snap.CityBreakdown = make(map[string]SnapshotBreakdown, len(cityRaw))
	for k, v := range cityRaw {
		snap.CityBreakdown[k] = SnapshotBreakdown{
			Count:    v.Count,
			Wins:     v.Wins,
			WinRate:  v.WinRate(),
			BrierAvg: v.BrierAvg(),
		}
	}

	// Signal breakdown
	sigRaw := SignalBreakdown(records)
	snap.SignalBreakdown = make(map[string]SnapshotBreakdown, len(sigRaw))
	for k, v := range sigRaw {
		snap.SignalBreakdown[k] = SnapshotBreakdown{
			Count:    v.Count,
			Wins:     v.Wins,
			WinRate:  v.WinRate(),
			BrierAvg: v.BrierAvg(),
		}
	}

	// Write to file
	outDir := filepath.Join(dataRoot, "data")
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return fmt.Errorf("calibration snapshot: mkdir: %w", err)
	}
	outPath := filepath.Join(outDir, "calibration_snapshot.json")

	data, err := json.MarshalIndent(snap, "", "  ")
	if err != nil {
		return fmt.Errorf("calibration snapshot: marshal: %w", err)
	}
	if err := os.WriteFile(outPath, data, 0o644); err != nil {
		return fmt.Errorf("calibration snapshot: write: %w", err)
	}

	slog.Info("calibration snapshot exported",
		"path", outPath,
		"resolved", snap.ResolvedBets,
		"brier", fmt.Sprintf("%.4f", snap.OverallBrier),
		"win_rate", fmt.Sprintf("%.1f%%", snap.OverallWinRate*100),
		"adaptive_edge_factor", fmt.Sprintf("%.2f", snap.AdaptiveEdgeFactor),
	)
	return nil
}

// PrintSnapshot loads and pretty-prints the last saved snapshot.
// Used by the dashboard command.
func PrintSnapshot(dataRoot string) {
	path := filepath.Join(dataRoot, "data", "calibration_snapshot.json")
	data, err := os.ReadFile(path)
	if err != nil {
		fmt.Println("No calibration snapshot found. Run the bot at least once.")
		return
	}
	var snap CalibrationSnapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		fmt.Printf("Error reading snapshot: %v\n", err)
		return
	}

	fmt.Printf("\n=== Calibration Snapshot (%s) ===\n\n", snap.GeneratedAt)
	fmt.Printf("Overall:    Brier=%.4f [%s]  WinRate=%.1f%%  Resolved=%d  Open=%d\n",
		snap.OverallBrier, snap.BrierQuality, snap.OverallWinRate*100,
		snap.ResolvedBets, snap.OpenBets)
	fmt.Printf("Bankroll:   %.2f USDC  (peak=%.2f, drawdown=%.1f%%, mult=%.2f)\n",
		snap.Bankroll, snap.PeakBankroll, snap.DrawdownPct, snap.DrawdownMultiplier)
	fmt.Printf("AdaptEdge:  factor=%.2f vs base\n\n", snap.AdaptiveEdgeFactor)

	if len(snap.CityBreakdown) > 0 {
		fmt.Println("By City:")
		for city, s := range snap.CityBreakdown {
			fmt.Printf("  %-18s  n=%-4d  wins=%-4d  WR=%.0f%%  Brier=%.3f\n",
				city, s.Count, s.Wins, s.WinRate*100, s.BrierAvg)
		}
		fmt.Println()
	}

	if len(snap.SignalBreakdown) > 0 {
		fmt.Println("By Signal:")
		for sig, s := range snap.SignalBreakdown {
			fmt.Printf("  %-10s  n=%-4d  wins=%-4d  WR=%.0f%%  Brier=%.3f\n",
				sig, s.Count, s.Wins, s.WinRate*100, s.BrierAvg)
		}
		fmt.Println()
	}
}
