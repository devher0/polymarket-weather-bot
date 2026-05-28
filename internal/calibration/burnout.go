// burnout.go — TASK-238: signal burnout detector.
//
// Detects when a specific signal is being traded too frequently relative to
// its recent win rate. Over-trading a single thesis can amplify losses when
// the model is wrong — "burnout" guards help prevent this.
//
// Algorithm:
//   - Look at resolved bets per signal in the last 14 days
//   - Compare bet frequency to overall average
//   - If frequency > FreqMultiplier×avg AND win rate < WinRateThreshold → burnout
//   - Also flag if a signal has 5+ bets in the last 48 hours (burst trading)
package calibration

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// BurnoutConfig controls detection thresholds.
type BurnoutConfig struct {
	WindowDays       int     // look-back window (default 14)
	FreqMultiplier   float64 // flag if signal freq > FreqMultiplier × global avg (default 2.0)
	WinRateThreshold float64 // flag if win rate below this (default 0.45)
	BurstWindow      int     // hours for burst detection (default 48)
	BurstLimit       int     // bets in BurstWindow to trigger burst alert (default 5)
	MinBets          int     // min resolved bets for win-rate analysis (default 4)
}

// DefaultBurnoutConfig returns sensible defaults.
func DefaultBurnoutConfig() BurnoutConfig {
	return BurnoutConfig{
		WindowDays:       14,
		FreqMultiplier:   2.0,
		WinRateThreshold: 0.45,
		BurstWindow:      48,
		BurstLimit:       5,
		MinBets:          4,
	}
}

// BurnoutResult holds the analysis for one signal.
type BurnoutResult struct {
	Signal       string
	BetsWindow   int     // resolved bets in look-back window
	WinRate      float64 // win rate in window (-1 if no data)
	Bursting     bool    // too many bets in last BurstWindow hours
	BurstCount   int     // bets in last BurstWindow hours (all, not just resolved)
	Overloaded   bool    // high freq + low win rate
	FreqRatio    float64 // freq / global avg (>FreqMultiplier = concerning)
	AlertMessage string  // non-empty if action recommended
}

// BurnoutReport holds results for all signals and overall stats.
type BurnoutReport struct {
	Results     []BurnoutResult
	GlobalAvg   float64 // avg bets per signal over window
	WindowDays  int
	GeneratedAt time.Time
}

// AnalyzeBurnout inspects bet history and returns a burnout report.
func AnalyzeBurnout(records []BetRecord, cfg BurnoutConfig) BurnoutReport {
	now := time.Now()
	windowStart := now.Add(-time.Duration(cfg.WindowDays) * 24 * time.Hour)
	burstStart := now.Add(-time.Duration(cfg.BurstWindow) * time.Hour)

	// Count resolved bets per signal in window and burst window.
	type signalStats struct {
		resolvedInWindow int
		winsInWindow     int
		burstCount       int // all bets (resolved or not) in burst window
	}
	bySig := map[string]*signalStats{}

	for i := range records {
		r := &records[i]
		sig := r.Signal
		if sig == "" {
			sig = "unknown"
		}
		if bySig[sig] == nil {
			bySig[sig] = &signalStats{}
		}
		// Burst: any bet (including unresolved) placed in burst window.
		if r.Timestamp.After(burstStart) {
			bySig[sig].burstCount++
		}
		// Window: only resolved bets.
		if r.Outcome == nil {
			continue
		}
		if r.Timestamp.After(windowStart) {
			bySig[sig].resolvedInWindow++
			if *r.Outcome {
				bySig[sig].winsInWindow++
			}
		}
	}

	if len(bySig) == 0 {
		return BurnoutReport{
			GeneratedAt: now,
			WindowDays:  cfg.WindowDays,
		}
	}

	// Compute global average resolved bets per signal over window.
	totalResolved := 0
	for _, s := range bySig {
		totalResolved += s.resolvedInWindow
	}
	globalAvg := float64(totalResolved) / float64(len(bySig))

	// Build results.
	var results []BurnoutResult
	for sig, s := range bySig {
		wr := -1.0
		if s.resolvedInWindow >= cfg.MinBets {
			wr = float64(s.winsInWindow) / float64(s.resolvedInWindow)
		}

		freqRatio := 0.0
		if globalAvg > 0 {
			freqRatio = float64(s.resolvedInWindow) / globalAvg
		}

		bursting := s.burstCount >= cfg.BurstLimit
		overloaded := freqRatio >= cfg.FreqMultiplier && wr >= 0 && wr < cfg.WinRateThreshold

		var alert string
		if overloaded {
			alert = fmt.Sprintf("over-trading %s with low win rate %.0f%% (freq=%.1fx avg)", sig, wr*100, freqRatio)
		} else if bursting {
			alert = fmt.Sprintf("burst on %s: %d bets in last %dh", sig, s.burstCount, cfg.BurstWindow)
		}

		results = append(results, BurnoutResult{
			Signal:       sig,
			BetsWindow:   s.resolvedInWindow,
			WinRate:      wr,
			Bursting:     bursting,
			BurstCount:   s.burstCount,
			Overloaded:   overloaded,
			FreqRatio:    freqRatio,
			AlertMessage: alert,
		})
	}

	// Sort: alerts first, then by signal name.
	sort.Slice(results, func(i, j int) bool {
		ai := results[i].AlertMessage != ""
		aj := results[j].AlertMessage != ""
		if ai != aj {
			return ai
		}
		return results[i].Signal < results[j].Signal
	})

	return BurnoutReport{
		Results:     results,
		GlobalAvg:   globalAvg,
		WindowDays:  cfg.WindowDays,
		GeneratedAt: now,
	}
}

// HasAlerts returns true if any signal has a burnout alert.
func (r BurnoutReport) HasAlerts() bool {
	for _, res := range r.Results {
		if res.AlertMessage != "" {
			return true
		}
	}
	return false
}

// FormatBurnout returns a Telegram-ready HTML string for the burnout report.
func FormatBurnout(report BurnoutReport) string {
	if len(report.Results) == 0 {
		return "📊 No signal data available yet."
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("<b>🔥 Signal Burnout Detector (%dd window)</b>\n", report.WindowDays))
	sb.WriteString(fmt.Sprintf("Global avg: %.1f bets/signal | Generated: %s\n\n",
		report.GlobalAvg, report.GeneratedAt.UTC().Format("15:04 UTC")))
	sb.WriteString("<pre>")
	sb.WriteString(fmt.Sprintf("%-8s %-5s %-6s %-5s %-6s\n", "Signal", "Bets", "WinR%", "Burst", "Status"))
	sb.WriteString(strings.Repeat("─", 38) + "\n")

	for _, r := range report.Results {
		wr := "N/A"
		if r.WinRate >= 0 {
			wr = fmt.Sprintf("%.0f%%", r.WinRate*100)
		}

		status := "✅ ok"
		if r.Overloaded {
			status = "🚨 OVER"
		} else if r.Bursting {
			status = "⚠️ BURST"
		}

		burst := fmt.Sprintf("%d", r.BurstCount)

		sb.WriteString(fmt.Sprintf("%-8s %-5d %-6s %-5s %s\n",
			r.Signal, r.BetsWindow, wr, burst, status))
	}
	sb.WriteString("</pre>")

	var alerts []string
	for _, r := range report.Results {
		if r.AlertMessage != "" {
			alerts = append(alerts, "⚠️ "+r.AlertMessage)
		}
	}
	if len(alerts) > 0 {
		sb.WriteString("\n<b>Alerts:</b>\n" + strings.Join(alerts, "\n"))
	} else {
		sb.WriteString("\n✅ No burnout detected across all signals.")
	}

	return sb.String()
}
