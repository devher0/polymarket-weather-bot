// pnlchart.go — ASCII bar chart of daily P&L for Telegram /summary.
// TASK-150
package calibration

import (
	"fmt"
	"math"
	"time"
)

// DailyPnLBars returns a compact 14-character bar string representing the daily
// resolved P&L over the last nDays UTC days, plus the total P&L for the period.
//
// Each character represents one day (oldest → newest):
//   - positive day → block from ▁ to █ (height proportional to |pnl|)
//   - negative day → ▼ (any loss, regardless of magnitude)
//   - zero / no data → · (dot)
//
// The returned total sums only resolved P&L over the window.
func DailyPnLBars(records []BetRecord, nDays int) (bars string, total float64) {
	if nDays <= 0 {
		return "", 0
	}

	now := time.Now().UTC().Truncate(24 * time.Hour)
	daily := make([]float64, nDays) // index 0 = oldest day
	hasData := make([]bool, nDays)

	for _, r := range records {
		if r.Outcome == nil {
			continue
		}
		day := r.ResolvedAt
		if day.IsZero() {
			day = r.Timestamp
		}
		day = day.UTC().Truncate(24 * time.Hour)

		daysAgo := int(now.Sub(day).Hours() / 24)
		if daysAgo < 0 || daysAgo >= nDays {
			continue
		}
		revIdx := nDays - 1 - daysAgo // index 0 = oldest

		var pnl float64
		if *r.Outcome {
			pnl = r.SizeUSDC/r.MarketPrice - r.SizeUSDC
		} else {
			pnl = -r.SizeUSDC
		}
		daily[revIdx] += pnl
		hasData[revIdx] = true
		total += pnl
	}

	// Find maximum positive P&L for normalising bar heights.
	maxPos := 0.0
	for i, v := range daily {
		if hasData[i] && v > maxPos {
			maxPos = v
		}
	}

	posBlocks := []rune{'▁', '▂', '▃', '▄', '▅', '▆', '▇', '█'}

	runes := make([]rune, nDays)
	for i := 0; i < nDays; i++ {
		if !hasData[i] {
			runes[i] = '·'
			continue
		}
		v := daily[i]
		if v < 0 {
			runes[i] = '▼'
		} else if v == 0 {
			runes[i] = '·'
		} else {
			// Map positive P&L onto 8 block heights.
			frac := v / math.Max(maxPos, 0.01)
			idx := int(math.Round(frac * float64(len(posBlocks)-1)))
			idx = max150(0, min150(len(posBlocks)-1, idx))
			runes[i] = posBlocks[idx]
		}
	}
	return string(runes), total
}

// DailyPnLLine returns a formatted one-liner for embedding in Telegram /summary.
// Example: "P&L 14d: ▁▂▼·▄▂▼▁▅▇▂·▁▃  +$12.30"
func DailyPnLLine(records []BetRecord, nDays int) string {
	bars, total := DailyPnLBars(records, nDays)
	if bars == "" {
		return ""
	}
	sign := "+"
	if total < 0 {
		sign = ""
	}
	return fmt.Sprintf("P&L %dd: %s  %s$%.2f", nDays, bars, sign, total)
}

// DailyStats holds aggregated resolved-bet metrics for a single UTC day.
type DailyStats struct {
	Date          time.Time // midnight UTC
	Bets          int
	Wins          int
	PnLUSDC       float64
	CumulativePnL float64 // filled in by DailyPnLTable
}

// WinPct returns win percentage (0–100), or -1 when no bets.
func (d DailyStats) WinPct() float64 {
	if d.Bets == 0 {
		return -1
	}
	return float64(d.Wins) / float64(d.Bets) * 100
}

// DailyPnLTable returns per-day stats for the last nDays UTC days (oldest first).
// Days with no resolved bets still appear with zero values so the table is
// always exactly nDays rows.
func DailyPnLTable(records []BetRecord, nDays int) []DailyStats {
	if nDays <= 0 {
		return nil
	}

	now := time.Now().UTC().Truncate(24 * time.Hour)
	rows := make([]DailyStats, nDays)
	for i := range rows {
		rows[i].Date = now.AddDate(0, 0, -(nDays - 1 - i))
	}

	for _, r := range records {
		if r.Outcome == nil {
			continue
		}
		day := r.ResolvedAt
		if day.IsZero() {
			day = r.Timestamp
		}
		day = day.UTC().Truncate(24 * time.Hour)

		daysAgo := int(now.Sub(day).Hours() / 24)
		if daysAgo < 0 || daysAgo >= nDays {
			continue
		}
		idx := nDays - 1 - daysAgo

		var pnl float64
		if *r.Outcome {
			pnl = r.SizeUSDC/r.MarketPrice - r.SizeUSDC
			rows[idx].Wins++
		} else {
			pnl = -r.SizeUSDC
		}
		rows[idx].Bets++
		rows[idx].PnLUSDC += pnl
	}

	// Fill cumulative P&L (oldest to newest).
	var cum float64
	for i := range rows {
		cum += rows[i].PnLUSDC
		rows[i].CumulativePnL = cum
	}

	return rows
}

// HourlyStats holds aggregated resolved-bet metrics for one UTC hour of day.
type HourlyStats struct {
	Hour    int // 0–23 UTC
	Bets    int
	Wins    int
	PnLUSDC float64
}

// WinPct returns win percentage (0–100), or -1 when no bets.
func (h HourlyStats) WinPct() float64 {
	if h.Bets == 0 {
		return -1
	}
	return float64(h.Wins) / float64(h.Bets) * 100
}

// HourlyWinRate groups resolved bets by the UTC hour of their Timestamp and
// returns a 24-element slice (index == hour). (TASK-180)
func HourlyWinRate(records []BetRecord) [24]HourlyStats {
	var stats [24]HourlyStats
	for i := range stats {
		stats[i].Hour = i
	}
	for _, r := range records {
		if r.Outcome == nil {
			continue
		}
		h := r.Timestamp.UTC().Hour()
		stats[h].Bets++
		var pnl float64
		if *r.Outcome {
			stats[h].Wins++
			pnl = r.SizeUSDC/r.MarketPrice - r.SizeUSDC
		} else {
			pnl = -r.SizeUSDC
		}
		stats[h].PnLUSDC += pnl
	}
	return stats
}

func max150(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func min150(a, b int) int {
	if a < b {
		return a
	}
	return b
}
