// sharpe.go — TASK-113: Sharpe ratio tracker.
//
// Tracks daily P&L returns and computes an annualised Sharpe ratio.
//
// Daily returns are stored as JSON in data/daily_returns.json:
//
//	{"returns": [{"date": "2026-05-27", "return": 0.015}, ...]}
//
// Sharpe = mean(daily_returns) / stddev(daily_returns) × sqrt(365)
//
// Quality thresholds:
//
//	> 2.0  — excellent (hedge-fund benchmark)
//	> 1.0  — good
//	> 0.5  — acceptable
//	≤ 0.5  — poor (triggers Telegram alert)
//
// SharpeAlert should be called once per bot cycle to emit warnings when
// the 30-day rolling Sharpe drops below 0.5.
package calibration

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"sort"
	"time"
)

const dailyReturnsFile = "data/daily_returns.json"

// DailyReturn captures the P&L return for one calendar date.
type DailyReturn struct {
	Date   string  `json:"date"`   // "YYYY-MM-DD" (UTC)
	Return float64 `json:"return"` // (end_bankroll - start_bankroll) / start_bankroll
}

type dailyReturnsStore struct {
	Returns []DailyReturn `json:"returns"`
}

func dailyReturnsPath(dataRoot string) string {
	if dataRoot == "" {
		dataRoot = "."
	}
	return filepath.Join(dataRoot, dailyReturnsFile)
}

// LoadDailyReturns reads the persisted daily-return series.
// Returns an empty slice if the file does not exist.
func LoadDailyReturns(dataRoot string) ([]DailyReturn, error) {
	data, err := os.ReadFile(dailyReturnsPath(dataRoot))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("sharpe: read daily returns: %w", err)
	}
	var store dailyReturnsStore
	if err := json.Unmarshal(data, &store); err != nil {
		return nil, fmt.Errorf("sharpe: parse daily returns: %w", err)
	}
	return store.Returns, nil
}

// RecordDailyReturn appends or updates today's daily return entry.
// startBankroll is the bankroll at the start of today; endBankroll is the
// current bankroll.  If startBankroll <= 0 the entry is skipped.
// An existing entry for today is overwritten so the last call per day wins.
func RecordDailyReturn(startBankroll, endBankroll float64, dataRoot string) error {
	if startBankroll <= 0 {
		return nil
	}
	today := time.Now().UTC().Format("2006-01-02")
	ret := (endBankroll - startBankroll) / startBankroll

	returns, err := LoadDailyReturns(dataRoot)
	if err != nil {
		returns = nil
	}

	// Update existing entry for today or append.
	updated := false
	for i, r := range returns {
		if r.Date == today {
			returns[i].Return = ret
			updated = true
			break
		}
	}
	if !updated {
		returns = append(returns, DailyReturn{Date: today, Return: ret})
	}

	// Keep sorted by date ascending.
	sort.Slice(returns, func(i, j int) bool {
		return returns[i].Date < returns[j].Date
	})

	store := dailyReturnsStore{Returns: returns}
	data, err := json.MarshalIndent(store, "", "  ")
	if err != nil {
		return fmt.Errorf("sharpe: marshal: %w", err)
	}
	path := dailyReturnsPath(dataRoot)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("sharpe: mkdir: %w", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("sharpe: write: %w", err)
	}
	return nil
}

// ComputeSharpe calculates the annualised Sharpe ratio from a slice of daily
// returns.  Returns (sharpe, count, error).
//
//   - Requires at least 2 observations; otherwise returns (0, 0, nil).
//   - If all returns are identical (stddev == 0) returns (0, count, nil).
func ComputeSharpe(returns []DailyReturn) (sharpe float64, count int, err error) {
	if len(returns) < 2 {
		return 0, len(returns), nil
	}
	n := float64(len(returns))
	sum := 0.0
	for _, r := range returns {
		sum += r.Return
	}
	mean := sum / n

	varSum := 0.0
	for _, r := range returns {
		d := r.Return - mean
		varSum += d * d
	}
	stddev := math.Sqrt(varSum / (n - 1)) // sample stddev
	if stddev == 0 {
		return 0, len(returns), nil
	}
	sharpe = mean / stddev * math.Sqrt(365)
	return sharpe, len(returns), nil
}

// RollingSharpe computes the Sharpe ratio over the last windowDays calendar
// days.  Days without an entry are excluded (sparse data is fine).
func RollingSharpe(dataRoot string, windowDays int) (sharpe float64, count int, err error) {
	all, err := LoadDailyReturns(dataRoot)
	if err != nil {
		return 0, 0, err
	}
	cutoff := time.Now().UTC().AddDate(0, 0, -windowDays).Format("2006-01-02")
	var window []DailyReturn
	for _, r := range all {
		if r.Date >= cutoff {
			window = append(window, r)
		}
	}
	return ComputeSharpe(window)
}

// SharpeQuality returns a human-readable label for a Sharpe ratio value.
func SharpeQuality(sharpe float64) string {
	switch {
	case sharpe > 2.0:
		return "excellent"
	case sharpe > 1.0:
		return "good"
	case sharpe > 0.5:
		return "acceptable"
	default:
		return "poor"
	}
}

// LogSharpe emits a structured log line with the current 30-day Sharpe ratio.
// Call once per bot cycle after RecordDailyReturn.
func LogSharpe(dataRoot string) {
	sharpe, count, err := RollingSharpe(dataRoot, 30)
	if err != nil {
		slog.Warn("sharpe: failed to compute", "err", err)
		return
	}
	if count < 2 {
		slog.Info("sharpe: not enough data", "days", count)
		return
	}
	slog.Info("sharpe ratio (30d)",
		"sharpe", fmt.Sprintf("%.3f", sharpe),
		"quality", SharpeQuality(sharpe),
		"days", count,
	)
}

// SharpeAlertMessage returns a non-empty string when the 30-day rolling Sharpe
// is below 0.5 and there are at least 5 data points.  The caller should send
// this as a Telegram alert.  Returns "" when no alert is needed.
func SharpeAlertMessage(dataRoot string) string {
	sharpe, count, err := RollingSharpe(dataRoot, 30)
	if err != nil || count < 5 {
		return ""
	}
	if sharpe >= 0.5 {
		return ""
	}
	return fmt.Sprintf(
		"⚠️ <b>Low Sharpe alert</b>\n30-day Sharpe ratio: <code>%.3f</code> [%s]\nDays tracked: %d\nConsider reviewing strategy or raising min_edge.",
		sharpe, SharpeQuality(sharpe), count,
	)
}
