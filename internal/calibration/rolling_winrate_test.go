package calibration

import (
	"testing"
	"time"
)

// rBet builds a resolved BetRecord with the given outcome.
func rBet(won bool) BetRecord {
	outcome := won
	return BetRecord{
		ConditionID:    "cid",
		Timestamp:      time.Now().UTC(),
		Side:           "YES",
		OurProbability: 0.6,
		MarketPrice:    0.5,
		SizeUSDC:       5.0,
		Outcome:        &outcome,
		ResolvedAt:     time.Now().UTC(),
	}
}

// unresolved returns an unresolved BetRecord.
func unresolved() BetRecord {
	return BetRecord{
		ConditionID:    "cid",
		Timestamp:      time.Now().UTC(),
		Side:           "YES",
		OurProbability: 0.6,
		MarketPrice:    0.5,
		SizeUSDC:       5.0,
		Outcome:        nil,
	}
}

func TestComputeRollingWinRate_Empty(t *testing.T) {
	rate, n := ComputeRollingWinRate(nil, 20)
	if rate != -1 || n != 0 {
		t.Errorf("expected (-1,0) on empty, got (%v,%v)", rate, n)
	}
}

func TestComputeRollingWinRate_TooFewResolved(t *testing.T) {
	records := []BetRecord{rBet(true), rBet(false), rBet(true)}
	rate, n := ComputeRollingWinRate(records, 20)
	if rate != -1 || n != 0 {
		t.Errorf("expected (-1,0) for <5 resolved, got (%v,%v)", rate, n)
	}
}

func TestComputeRollingWinRate_AllWins(t *testing.T) {
	records := make([]BetRecord, 10)
	for i := range records {
		records[i] = rBet(true)
	}
	rate, n := ComputeRollingWinRate(records, 10)
	if rate != 1.0 {
		t.Errorf("expected 1.0, got %.2f", rate)
	}
	if n != 10 {
		t.Errorf("expected n=10, got %d", n)
	}
}

func TestComputeRollingWinRate_AllLosses(t *testing.T) {
	records := make([]BetRecord, 10)
	for i := range records {
		records[i] = rBet(false)
	}
	rate, n := ComputeRollingWinRate(records, 10)
	if rate != 0.0 {
		t.Errorf("expected 0.0, got %.2f", rate)
	}
	if n != 10 {
		t.Errorf("expected n=10, got %d", n)
	}
}

func TestComputeRollingWinRate_Mixed(t *testing.T) {
	// 6 wins, 4 losses over 10
	records := []BetRecord{
		rBet(true), rBet(true), rBet(false), rBet(true),
		rBet(false), rBet(true), rBet(false), rBet(true),
		rBet(false), rBet(true),
	}
	rate, n := ComputeRollingWinRate(records, 10)
	if n != 10 {
		t.Errorf("expected n=10, got %d", n)
	}
	if rate < 0.59 || rate > 0.61 {
		t.Errorf("expected ~0.60, got %.4f", rate)
	}
}

func TestComputeRollingWinRate_WindowLargerThanHistory(t *testing.T) {
	// 7 resolved bets, window=20 → use all 7
	records := make([]BetRecord, 7)
	for i := range records {
		records[i] = rBet(i%2 == 0) // alternating
	}
	rate, n := ComputeRollingWinRate(records, 20)
	if n != 7 {
		t.Errorf("expected n=7 (capped at history), got %d", n)
	}
	_ = rate
}

func TestComputeRollingWinRate_UnresolvedIgnored(t *testing.T) {
	// Mix of resolved and unresolved — only resolved count.
	records := []BetRecord{
		rBet(true), unresolved(), rBet(true), unresolved(),
		rBet(false), rBet(true), rBet(true), rBet(true),
	}
	rate, n := ComputeRollingWinRate(records, 10)
	if n != 6 {
		t.Errorf("expected 6 resolved, got %d", n)
	}
	if rate < 0.83 || rate > 0.84 { // 5/6 ≈ 0.833
		t.Errorf("expected ~0.833, got %.4f", rate)
	}
}

func TestWinRateAlert_AboveThreshold(t *testing.T) {
	records := make([]BetRecord, 10)
	for i := range records {
		records[i] = rBet(true) // 100% win rate
	}
	alert, msg := WinRateAlert(records, 10, 0.35)
	if alert {
		t.Errorf("expected no alert above threshold, got: %s", msg)
	}
}

func TestWinRateAlert_BelowThreshold(t *testing.T) {
	records := make([]BetRecord, 10)
	for i := range records {
		records[i] = rBet(false) // 0% win rate
	}
	alert, msg := WinRateAlert(records, 10, 0.35)
	if !alert {
		t.Error("expected alert below threshold")
	}
	if msg == "" {
		t.Error("expected non-empty message")
	}
}
