// auto_blacklist.go — automatic city+signal blacklist based on loss history.
//
// If a (city, signal) pair systematically loses money, it is automatically
// blacklisted for a configurable number of days so the bot stops re-entering
// losing positions without operator intervention.
//
// Persistence: data/auto_blacklist.json
// (separate from the per-conditionID blacklist in blacklist.go)
package markets

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"
)

// AutoBetRecord is a minimal bet summary consumed by AutoBlacklistCheck.
// cmd/bot converts []calibration.BetRecord into []AutoBetRecord to avoid
// an import cycle (markets → calibration → strategy → markets).
type AutoBetRecord struct {
	City           string
	Signal         string
	Outcome        *bool   // nil = unresolved
	SizeUSDC       float64
	OurProbability float64
	MarketPrice    float64
}

// AutoBlacklistCfg controls when a (city, signal) pair is auto-blacklisted.
type AutoBlacklistCfg struct {
	// MinBets is the minimum number of resolved bets on a (city, signal) pair
	// before the auto-blacklist logic is considered (default: 8).
	MinBets int
	// LossThresholdUSDC is the cumulative net PnL below which the pair is
	// blacklisted (negative; default: -3.0).
	LossThresholdUSDC float64
	// BlacklistDays is how many days to blacklist the pair (default: 3).
	BlacklistDays int
}

// AutoBlacklistEntry is one active auto-blacklist record.
type AutoBlacklistEntry struct {
	City      string    `json:"city"`
	Signal    string    `json:"signal"`
	NetPnL    float64   `json:"net_pnl_usdc"` // cumulative PnL when blacklisted
	AddedAt   time.Time `json:"added_at"`
	ExpiresAt time.Time `json:"expires_at"`
}

func autoBlacklistPath(dataRoot string) string {
	if dataRoot == "" {
		dataRoot = "."
	}
	return filepath.Join(dataRoot, "data", "auto_blacklist.json")
}

// loadAutoBlacklist reads and returns active (non-expired) auto-blacklist entries.
// Returns nil on any error or when the file does not exist.
func loadAutoBlacklist(dataRoot string) []AutoBlacklistEntry {
	data, err := os.ReadFile(autoBlacklistPath(dataRoot))
	if err != nil {
		return nil
	}
	var entries []AutoBlacklistEntry
	if err := json.Unmarshal(data, &entries); err != nil {
		slog.Warn("auto_blacklist: parse error", "err", err)
		return nil
	}
	// Prune expired entries.
	now := time.Now().UTC()
	active := make([]AutoBlacklistEntry, 0, len(entries))
	for _, e := range entries {
		if e.ExpiresAt.After(now) {
			active = append(active, e)
		}
	}
	return active
}

func saveAutoBlacklist(entries []AutoBlacklistEntry, dataRoot string) error {
	path := autoBlacklistPath(dataRoot)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("auto_blacklist: mkdir: %w", err)
	}
	data, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

// IsAutoBlacklisted returns true if the (city, signal) pair is currently
// auto-blacklisted (i.e., a non-expired entry exists in auto_blacklist.json).
func IsAutoBlacklisted(city, signal, dataRoot string) bool {
	if city == "" || signal == "" {
		return false
	}
	entries := loadAutoBlacklist(dataRoot)
	now := time.Now().UTC()
	for _, e := range entries {
		if e.City == city && e.Signal == signal && e.ExpiresAt.After(now) {
			return true
		}
	}
	return false
}

// AutoBlacklistStatus returns all active (non-expired) auto-blacklist entries.
func AutoBlacklistStatus(dataRoot string) []AutoBlacklistEntry {
	return loadAutoBlacklist(dataRoot)
}

// AutoBlacklistCheck inspects the recent bet history for a (city, signal) pair.
// If there are at least cfg.MinBets resolved bets and the cumulative net P&L is
// below cfg.LossThresholdUSDC (negative), the pair is added to the auto-blacklist.
//
// Returns nil when the check passes (no action taken), or an error describing
// what triggered the blacklisting.
func AutoBlacklistCheck(records []AutoBetRecord, city, signal, dataRoot string, cfg AutoBlacklistCfg) error {
	if cfg.MinBets <= 0 {
		cfg.MinBets = 8
	}
	if cfg.LossThresholdUSDC >= 0 {
		cfg.LossThresholdUSDC = -3.0
	}
	if cfg.BlacklistDays <= 0 {
		cfg.BlacklistDays = 3
	}

	if city == "" || signal == "" {
		return nil
	}

	// Collect resolved bets for this (city, signal) pair.
	var netPnL float64
	count := 0
	for _, r := range records {
		if r.City != city || r.Signal != signal {
			continue
		}
		if r.Outcome == nil {
			continue // skip unresolved
		}
		count++
		if *r.Outcome {
			// Win: gain ≈ size × (1/marketPrice - 1) — simplified as size × edge proxy.
			// For blacklist purposes we use size as a positive contribution.
			netPnL += r.SizeUSDC * (r.OurProbability/r.MarketPrice - 1.0)
		} else {
			// Loss: lose the entire size.
			netPnL -= r.SizeUSDC
		}
	}

	if count < cfg.MinBets {
		return nil // not enough data
	}

	if netPnL >= cfg.LossThresholdUSDC {
		return nil // within acceptable range
	}

	// Pair is systematically losing — add to blacklist.
	existing := loadAutoBlacklist(dataRoot)

	// Check if already blacklisted to avoid duplicate entries.
	now := time.Now().UTC()
	for _, e := range existing {
		if e.City == city && e.Signal == signal && e.ExpiresAt.After(now) {
			return fmt.Errorf("auto-blacklisted (already active, expires %s)", e.ExpiresAt.Format(time.DateOnly))
		}
	}

	// Remove any expired entry for this pair and add a fresh one.
	fresh := make([]AutoBlacklistEntry, 0, len(existing)+1)
	for _, e := range existing {
		if !(e.City == city && e.Signal == signal) {
			fresh = append(fresh, e)
		}
	}
	expiry := now.Add(time.Duration(cfg.BlacklistDays) * 24 * time.Hour)
	fresh = append(fresh, AutoBlacklistEntry{
		City:      city,
		Signal:    signal,
		NetPnL:    netPnL,
		AddedAt:   now,
		ExpiresAt: expiry,
	})

	if err := saveAutoBlacklist(fresh, dataRoot); err != nil {
		slog.Warn("auto_blacklist: failed to save", "err", err)
	}

	msg := fmt.Sprintf("auto-blacklisted: city=%s signal=%s net_pnl=%.2f USDC (threshold=%.2f), expires %s",
		city, signal, netPnL, cfg.LossThresholdUSDC, expiry.Format(time.DateOnly))
	slog.Warn(msg,
		"city", city,
		"signal", signal,
		"net_pnl_usdc", fmt.Sprintf("%.2f", netPnL),
		"bets", count,
		"expires", expiry.Format(time.DateOnly),
	)
	return fmt.Errorf("%s", msg)
}
