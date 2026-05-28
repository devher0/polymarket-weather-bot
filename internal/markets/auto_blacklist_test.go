package markets

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// helper to build an AutoBetRecord with a given outcome.
func mkBet(city, signal string, won bool, size, ourP, mktP float64) AutoBetRecord {
	outcome := won
	return AutoBetRecord{
		City:           city,
		Signal:         signal,
		OurProbability: ourP,
		MarketPrice:    mktP,
		SizeUSDC:       size,
		Outcome:        &outcome,
	}
}

// writeAutoBlacklist serialises entries directly to the expected path (test helper).
func writeAutoBlacklist(t *testing.T, dataRoot string, entries []AutoBlacklistEntry) {
	t.Helper()
	path := autoBlacklistPath(dataRoot)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	data, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestAutoBlacklistCheck_EmptyHistory(t *testing.T) {
	dir := t.TempDir()
	err := AutoBlacklistCheck(nil, "new_york", "rain", dir, AutoBlacklistCfg{MinBets: 8})
	if err != nil {
		t.Errorf("expected nil on empty history, got: %v", err)
	}
}

func TestAutoBlacklistCheck_TooFewBets(t *testing.T) {
	dir := t.TempDir()
	records := []AutoBetRecord{
		mkBet("new_york", "rain", false, 5.0, 0.6, 0.5),
		mkBet("new_york", "rain", false, 5.0, 0.6, 0.5),
	}
	err := AutoBlacklistCheck(records, "new_york", "rain", dir, AutoBlacklistCfg{MinBets: 8})
	if err != nil {
		t.Errorf("expected nil (too few bets), got: %v", err)
	}
}

func TestAutoBlacklistCheck_ProfitablePair(t *testing.T) {
	dir := t.TempDir()
	// 10 winning bets — should NOT trigger blacklist.
	records := make([]AutoBetRecord, 10)
	for i := range records {
		records[i] = mkBet("new_york", "rain", true, 5.0, 0.7, 0.5)
	}
	err := AutoBlacklistCheck(records, "new_york", "rain", dir, AutoBlacklistCfg{
		MinBets: 8, LossThresholdUSDC: -3.0, BlacklistDays: 3,
	})
	if err != nil {
		t.Errorf("expected nil on profitable pair, got: %v", err)
	}
}

func TestAutoBlacklistCheck_LosingPair_Blacklisted(t *testing.T) {
	dir := t.TempDir()
	// 10 losing bets → cumulative PnL deeply negative.
	records := make([]AutoBetRecord, 10)
	for i := range records {
		records[i] = mkBet("miami", "heat", false, 5.0, 0.6, 0.5)
	}
	err := AutoBlacklistCheck(records, "miami", "heat", dir, AutoBlacklistCfg{
		MinBets: 8, LossThresholdUSDC: -3.0, BlacklistDays: 3,
	})
	if err == nil {
		t.Error("expected error (blacklisted), got nil")
	}
	if !IsAutoBlacklisted("miami", "heat", dir) {
		t.Error("expected miami/heat to be auto-blacklisted")
	}
}

func TestIsAutoBlacklisted_NotPresent(t *testing.T) {
	dir := t.TempDir()
	if IsAutoBlacklisted("london", "rain", dir) {
		t.Error("expected false for non-existent entry")
	}
}

func TestIsAutoBlacklisted_EmptyPair(t *testing.T) {
	dir := t.TempDir()
	if IsAutoBlacklisted("", "rain", dir) {
		t.Error("expected false for empty city")
	}
	if IsAutoBlacklisted("london", "", dir) {
		t.Error("expected false for empty signal")
	}
}

func TestAutoBlacklistStatus_Empty(t *testing.T) {
	dir := t.TempDir()
	entries := AutoBlacklistStatus(dir)
	if len(entries) != 0 {
		t.Errorf("expected empty status, got %d entries", len(entries))
	}
}

func TestAutoBlacklistStatus_ShowsActiveEntries(t *testing.T) {
	dir := t.TempDir()
	records := make([]AutoBetRecord, 10)
	for i := range records {
		records[i] = mkBet("paris", "rain", false, 5.0, 0.6, 0.5)
	}
	if err := AutoBlacklistCheck(records, "paris", "rain", dir, AutoBlacklistCfg{
		MinBets: 8, LossThresholdUSDC: -3.0, BlacklistDays: 3,
	}); err == nil {
		t.Fatal("expected blacklist trigger")
	}
	status := AutoBlacklistStatus(dir)
	if len(status) != 1 {
		t.Errorf("expected 1 status entry, got %d", len(status))
	}
	if status[0].City != "paris" || status[0].Signal != "rain" {
		t.Errorf("unexpected entry: %+v", status[0])
	}
}

func TestAutoBlacklistCheck_ExpiredEntryRenewed(t *testing.T) {
	dir := t.TempDir()
	// Write an already-expired entry.
	writeAutoBlacklist(t, dir, []AutoBlacklistEntry{{
		City:      "tokyo",
		Signal:    "rain",
		NetPnL:    -10.0,
		AddedAt:   time.Now().UTC().Add(-10 * 24 * time.Hour),
		ExpiresAt: time.Now().UTC().Add(-1 * time.Hour),
	}})

	// Expired entry should not block the IsAutoBlacklisted check.
	if IsAutoBlacklisted("tokyo", "rain", dir) {
		t.Error("expired entry should not count as blacklisted")
	}

	// Now trigger blacklist again with fresh losses.
	records := make([]AutoBetRecord, 10)
	for i := range records {
		records[i] = mkBet("tokyo", "rain", false, 5.0, 0.6, 0.5)
	}
	err := AutoBlacklistCheck(records, "tokyo", "rain", dir, AutoBlacklistCfg{
		MinBets: 8, LossThresholdUSDC: -3.0, BlacklistDays: 3,
	})
	if err == nil {
		t.Error("expected blacklist to be re-applied after expiry")
	}
	if !IsAutoBlacklisted("tokyo", "rain", dir) {
		t.Error("expected tokyo/rain to be re-blacklisted")
	}
}
