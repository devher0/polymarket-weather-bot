// bankroll.go — persistent bankroll state between bot sessions.
//
// The bankroll JSON file lives at data/bankroll.json and tracks the USDC
// balance available for betting:
//
//	Open bet    → bankroll -= SizeUSDC   (capital is committed)
//	Resolve WIN → bankroll += SizeUSDC / MarketPrice  (full payout returned)
//	Resolve LOSS → no change (amount was already deducted at open)
//
// When no file exists the default bankroll of 100.0 USDC is returned.
package calibration

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const (
	DefaultBankroll = 100.0
	bankrollFile    = "data/bankroll.json"
)

type bankrollState struct {
	BankrollUSDC float64   `json:"bankroll_usdc"`
	UpdatedAt    time.Time `json:"updated_at"`
}

var bankrollMu sync.Mutex

// LoadBankroll reads the persisted bankroll from data/bankroll.json.
// Returns DefaultBankroll (100.0 USDC) when the file doesn't exist or is unreadable.
func LoadBankroll(dataRoot string) float64 {
	if dataRoot == "" {
		dataRoot = "."
	}
	path := filepath.Join(dataRoot, bankrollFile)

	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return DefaultBankroll
	}
	if err != nil {
		slog.Warn("bankroll: read failed, using default", "err", err, "path", path)
		return DefaultBankroll
	}

	var state bankrollState
	if err := json.Unmarshal(data, &state); err != nil {
		slog.Warn("bankroll: parse failed, using default", "err", err)
		return DefaultBankroll
	}
	if state.BankrollUSDC <= 0 {
		slog.Warn("bankroll: file has non-positive value, using default",
			"stored", state.BankrollUSDC)
		return DefaultBankroll
	}
	return state.BankrollUSDC
}

// SaveBankroll persists the current bankroll to data/bankroll.json.
// Thread-safe: uses a package-level mutex so concurrent callers don't race.
func SaveBankroll(bankroll float64, dataRoot string) error {
	if dataRoot == "" {
		dataRoot = "."
	}
	path := filepath.Join(dataRoot, bankrollFile)

	bankrollMu.Lock()
	defer bankrollMu.Unlock()

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("bankroll: mkdir: %w", err)
	}
	state := bankrollState{
		BankrollUSDC: bankroll,
		UpdatedAt:    time.Now().UTC(),
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("bankroll: marshal: %w", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("bankroll: write: %w", err)
	}
	return nil
}

// AdjustBankrollOnBet deducts the bet size from the bankroll and saves it.
// Call this when a real (non-dry-run) bet is placed.
// Returns the updated bankroll.
func AdjustBankrollOnBet(sizeUSDC float64, dataRoot string) (float64, error) {
	current := LoadBankroll(dataRoot)
	updated := current - sizeUSDC
	slog.Info("bankroll update: bet opened",
		"before", fmt.Sprintf("%.2f", current),
		"size", fmt.Sprintf("%.2f", sizeUSDC),
		"after", fmt.Sprintf("%.2f", updated),
	)
	return updated, SaveBankroll(updated, dataRoot)
}

// AdjustBankrollOnResolve adds the payout to the bankroll when a bet is won,
// or logs a zero-change when the bet is lost (money was already deducted on open).
// payout = sizeUSDC / marketPrice  (binary prediction market: 1.0 per contract)
func AdjustBankrollOnResolve(sizeUSDC, marketPrice float64, won bool, dataRoot string) (float64, error) {
	current := LoadBankroll(dataRoot)
	if !won {
		slog.Info("bankroll update: bet lost",
			"bankroll", fmt.Sprintf("%.2f", current),
			"size", fmt.Sprintf("%.2f", sizeUSDC),
		)
		return current, nil // already deducted at open
	}
	payout := sizeUSDC
	if marketPrice > 0 && marketPrice < 1 {
		payout = sizeUSDC / marketPrice
	}
	updated := current + payout
	slog.Info("bankroll update: bet won",
		"before", fmt.Sprintf("%.2f", current),
		"payout", fmt.Sprintf("%.2f", payout),
		"after", fmt.Sprintf("%.2f", updated),
	)
	return updated, SaveBankroll(updated, dataRoot)
}
