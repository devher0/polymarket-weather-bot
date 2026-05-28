package calibration

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"time"
)

// BankrollSnapshot records bankroll value at a point in time.
type BankrollSnapshot struct {
	Date          string  `json:"date"`  // YYYY-MM-DD
	BalanceUSDC   float64 `json:"balance_usdc"`
	Timestamp     int64   `json:"timestamp"` // Unix timestamp
	ResolvedBets  int     `json:"resolved_bets"`
	CumulativePnL float64 `json:"cumulative_pnl"`
}

// SaveBankrollSnapshot records the daily end-of-day bankroll balance.
// Should be called once per day (e.g., at 23:59 or when switching days).
func SaveBankrollSnapshot(balanceUSDC float64, dataRoot string) error {
	now := time.Now().UTC()
	dateStr := now.Format("2006-01-02")

	// Load existing history.
	path := bankrollHistoryPath(dataRoot)
	var history []BankrollSnapshot
	if data, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(data, &history)
	}

	// Compute cumulative P&L from resolved bets for this date.
	records, _ := LoadHistory(dataRoot)
	var cumulativePnL, resolvedBets int
	for _, r := range records {
		if r.Outcome == nil || r.Timestamp.UTC().Format("2006-01-02") != dateStr {
			continue
		}
		resolvedBets++
		if *r.Outcome {
			cumulativePnL += int(r.SizeUSDC/r.MarketPrice - r.SizeUSDC)
		} else {
			cumulativePnL -= int(r.SizeUSDC)
		}
	}

	// Check if we already have a snapshot for today (update rather than append).
	found := false
	for i, s := range history {
		if s.Date == dateStr {
			history[i] = BankrollSnapshot{
				Date:          dateStr,
				BalanceUSDC:   balanceUSDC,
				Timestamp:     now.Unix(),
				ResolvedBets:  resolvedBets,
				CumulativePnL: float64(cumulativePnL),
			}
			found = true
			break
		}
	}

	// If not found, append new snapshot.
	if !found {
		history = append(history, BankrollSnapshot{
			Date:          dateStr,
			BalanceUSDC:   balanceUSDC,
			Timestamp:     now.Unix(),
			ResolvedBets:  resolvedBets,
			CumulativePnL: float64(cumulativePnL),
		})
	}

	// Save back to disk.
	if err := os.MkdirAll(bankrollDir(dataRoot), 0o755); err != nil {
		return err
	}
	data, _ := json.MarshalIndent(history, "", "  ")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return err
	}

	return nil
}

// LoadBankrollHistory reads the bankroll history from disk.
func LoadBankrollHistory(dataRoot string) ([]BankrollSnapshot, error) {
	path := bankrollHistoryPath(dataRoot)
	data, err := os.ReadFile(path)
	if err != nil {
		return []BankrollSnapshot{}, nil // Return empty slice if file doesn't exist
	}

	var history []BankrollSnapshot
	if err := json.Unmarshal(data, &history); err != nil {
		return nil, err
	}

	// Sort by date ascending.
	sort.Slice(history, func(i, j int) bool {
		return history[i].Date < history[j].Date
	})

	return history, nil
}

// BankrollStats contains aggregated bankroll statistics.
type BankrollStats struct {
	CurrentBalance  float64
	DaysOfData      int
	CumulativeProfit float64
	DailyAverage    float64
	BestDay         string
	BestDayValue    float64
	WorstDay        string
	WorstDayValue   float64
	DaysUp          int
	DaysDown        int
	DaysFlat        int
	StartBalance    float64
}

// ComputeBankrollStats computes aggregate statistics from history.
func ComputeBankrollStats(history []BankrollSnapshot) BankrollStats {
	stats := BankrollStats{}

	if len(history) == 0 {
		return stats
	}

	stats.StartBalance = history[0].BalanceUSDC
	stats.CurrentBalance = history[len(history)-1].BalanceUSDC
	stats.CumulativeProfit = stats.CurrentBalance - stats.StartBalance
	stats.DaysOfData = len(history)

	if stats.DaysOfData > 0 {
		stats.DailyAverage = stats.CumulativeProfit / float64(stats.DaysOfData)
	}

	// Find best and worst days.
	stats.BestDayValue = history[0].BalanceUSDC
	stats.BestDay = history[0].Date
	stats.WorstDayValue = history[0].BalanceUSDC
	stats.WorstDay = history[0].Date

	for i, snap := range history {
		if snap.BalanceUSDC > stats.BestDayValue {
			stats.BestDayValue = snap.BalanceUSDC
			stats.BestDay = snap.Date
		}
		if snap.BalanceUSDC < stats.WorstDayValue {
			stats.WorstDayValue = snap.BalanceUSDC
			stats.WorstDay = snap.Date
		}

		// Count up/down/flat days.
		if i == 0 {
			continue
		}
		prev := history[i-1].BalanceUSDC
		if snap.BalanceUSDC > prev {
			stats.DaysUp++
		} else if snap.BalanceUSDC < prev {
			stats.DaysDown++
		} else {
			stats.DaysFlat++
		}
	}

	return stats
}

// bankrollHistoryPath returns the file path for bankroll history.
func bankrollHistoryPath(dataRoot string) string {
	dir := bankrollDir(dataRoot)
	return dir + "/history.json"
}

// bankrollDir returns the bankroll data directory.
func bankrollDir(dataRoot string) string {
	if dataRoot == "" || dataRoot == "." {
		return "data/bankroll"
	}
	return dataRoot + "/data/bankroll"
}

// FormatBankrollChart creates an ASCII bar chart of bankroll over days.
func FormatBankrollChart(history []BankrollSnapshot, maxDays int) string {
	if len(history) == 0 {
		return "No bankroll history."
	}

	// Take last N days.
	start := 0
	if len(history) > maxDays {
		start = len(history) - maxDays
	}
	subset := history[start:]

	// Find min/max for scaling.
	minBal, maxBal := subset[0].BalanceUSDC, subset[0].BalanceUSDC
	for _, s := range subset {
		if s.BalanceUSDC < minBal {
			minBal = s.BalanceUSDC
		}
		if s.BalanceUSDC > maxBal {
			maxBal = s.BalanceUSDC
		}
	}

	// Prevent division by zero.
	if minBal == maxBal {
		maxBal = minBal + 1
	}

	// Build chart.
	var chart string
	chartHeight := 10
	for row := chartHeight; row > 0; row-- {
		chart += "│ "
		threshold := minBal + (maxBal-minBal)*float64(row-1)/float64(chartHeight)
		for _, s := range subset {
			if s.BalanceUSDC >= threshold {
				chart += "█ "
			} else {
				chart += "  "
			}
		}
		chart += fmt.Sprintf("%.0f USDC\n", threshold)
	}
	chart += "└" + repeatStr("─", len(subset)*2+1) + "\n"
	chart += " "
	for _, s := range subset {
		chart += s.Date[8:] + " " // Show day of month (DD)
	}

	return chart
}

func repeatStr(s string, n int) string {
	out := ""
	for i := 0; i < n; i++ {
		out += s
	}
	return out
}
