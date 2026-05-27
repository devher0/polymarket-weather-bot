// Package risk implements position-level and session-level risk controls.
//
// The RiskManager enforces three guards before every bet:
//
//  1. Daily bet count cap  — stop after MaxDailyBets per UTC calendar day.
//  2. Daily loss limit     — stop when today's resolved net P&L falls below
//     -MaxDailyLossUSDC.
//  3. Open-position cap    — stop when there are ≥ MaxOpenPositions unresolved
//     bets (avoids overexposure while waiting for resolution).
//
// All thresholds can be tuned via config.yaml or ENV variables.
package risk

import (
	"fmt"
	"strings"
	"time"

	"github.com/devher0/polymarket-weather-bot/internal/calibration"
)

// Config holds risk-management thresholds.
// Zero values disable the corresponding check.
type Config struct {
	// MaxDailyLossUSDC is the maximum allowable realised loss in a single UTC
	// calendar day (e.g. 50.0 means stop when today's P&L < -50 USDC).
	MaxDailyLossUSDC float64

	// MaxDailyProfitUSDC is the daily profit target: stop betting when today's
	// realised P&L exceeds this value (0 = disabled). Prevents overtrading
	// after a lucky morning run.
	MaxDailyProfitUSDC float64

	// MaxDailyBets is the maximum number of bets that may be placed in a single
	// UTC calendar day (0 = unlimited).
	MaxDailyBets int

	// MaxOpenPositions is the maximum number of unresolved bets allowed at any
	// time (0 = unlimited).
	MaxOpenPositions int
}

// DefaultConfig returns conservative risk limits suitable for a small bankroll.
func DefaultConfig() Config {
	return Config{
		MaxDailyLossUSDC:   50.0,
		MaxDailyProfitUSDC: 0, // disabled by default
		MaxDailyBets:       20,
		MaxOpenPositions:   30,
	}
}

// Manager enforces risk limits.
type Manager struct {
	cfg Config
}

// New creates a Manager with the given Config.
func New(cfg Config) *Manager {
	return &Manager{cfg: cfg}
}

// ── Analytics helpers ─────────────────────────────────────────────────────────

// DailyStats returns the number of bets placed today (UTC) and the net
// realised P&L for those bets.
//
// Realised P&L:
//   - Won bet: +sizeUSDC × (1/marketPrice − 1)  (payout minus stake)
//   - Lost bet: −sizeUSDC
//   - Unresolved bet: 0 (not counted in P&L; counted in bet count)
func DailyStats(records []calibration.BetRecord) (count int, netPnL float64) {
	today := time.Now().UTC().Truncate(24 * time.Hour)
	for _, r := range records {
		if r.Timestamp.UTC().Before(today) {
			continue
		}
		count++
		if r.Outcome == nil {
			continue // unresolved — skip from P&L
		}
		if *r.Outcome {
			// payout = size / marketPrice; profit = payout - size
			netPnL += r.SizeUSDC * (1.0/r.MarketPrice - 1.0)
		} else {
			netPnL -= r.SizeUSDC
		}
	}
	return
}

// OpenPositionsCount returns the number of unresolved (open) bets.
func OpenPositionsCount(records []calibration.BetRecord) int {
	n := 0
	for _, r := range records {
		if r.Outcome == nil {
			n++
		}
	}
	return n
}

// ── Core check ────────────────────────────────────────────────────────────────

// AllowBet checks all active risk limits.
//
// Returns nil if a new bet of betSize USDC is allowed, or a non-nil error
// explaining which limit blocks it.  The caller should log the error and skip
// the bet.
func (m *Manager) AllowBet(records []calibration.BetRecord) error {
	dailyCount, dailyPnL := DailyStats(records)

	// 1. Daily bet count cap.
	if m.cfg.MaxDailyBets > 0 && dailyCount >= m.cfg.MaxDailyBets {
		return fmt.Errorf("daily bet cap reached (%d/%d bets today)",
			dailyCount, m.cfg.MaxDailyBets)
	}

	// 2. Daily loss limit.
	if m.cfg.MaxDailyLossUSDC > 0 && dailyPnL < -m.cfg.MaxDailyLossUSDC {
		return fmt.Errorf("daily loss limit hit (%.2f USDC loss today, limit %.2f USDC)",
			-dailyPnL, m.cfg.MaxDailyLossUSDC)
	}

	// 2b. Daily profit target — lock in gains, prevent overtrading.
	if m.cfg.MaxDailyProfitUSDC > 0 && dailyPnL >= m.cfg.MaxDailyProfitUSDC {
		return fmt.Errorf("daily profit target reached (+%.2f USDC today, target %.2f USDC) — locking in gains",
			dailyPnL, m.cfg.MaxDailyProfitUSDC)
	}

	// 3. Open-position cap.
	open := OpenPositionsCount(records)
	if m.cfg.MaxOpenPositions > 0 && open >= m.cfg.MaxOpenPositions {
		return fmt.Errorf("open-position cap reached (%d/%d positions open)",
			open, m.cfg.MaxOpenPositions)
	}

	return nil
}

// ── Reporting ─────────────────────────────────────────────────────────────────

// Summary returns a one-line human-readable status string showing the current
// risk counters and their configured limits.
func Summary(records []calibration.BetRecord, cfg Config) string {
	dailyCount, dailyPnL := DailyStats(records)
	open := OpenPositionsCount(records)

	pnlStr := fmt.Sprintf("%+.2f", dailyPnL)

	parts := []string{
		fmt.Sprintf("daily_bets=%d", dailyCount),
		fmt.Sprintf("daily_pnl=%s USDC", pnlStr),
		fmt.Sprintf("open=%d", open),
	}

	limits := []string{}
	if cfg.MaxDailyBets > 0 {
		limits = append(limits, fmt.Sprintf("max_daily_bets=%d", cfg.MaxDailyBets))
	}
	if cfg.MaxDailyLossUSDC > 0 {
		limits = append(limits, fmt.Sprintf("max_daily_loss=%.0f USDC", cfg.MaxDailyLossUSDC))
	}
	if cfg.MaxDailyProfitUSDC > 0 {
		limits = append(limits, fmt.Sprintf("max_daily_profit=%.0f USDC", cfg.MaxDailyProfitUSDC))
	}
	if cfg.MaxOpenPositions > 0 {
		limits = append(limits, fmt.Sprintf("max_open=%d", cfg.MaxOpenPositions))
	}

	if len(limits) > 0 {
		return fmt.Sprintf("risk [%s | limits: %s]",
			strings.Join(parts, " "),
			strings.Join(limits, " "),
		)
	}
	return fmt.Sprintf("risk [%s]", strings.Join(parts, " "))
}
