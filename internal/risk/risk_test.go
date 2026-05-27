package risk_test

import (
	"testing"
	"time"

	"github.com/devher0/polymarket-weather-bot/internal/calibration"
	"github.com/devher0/polymarket-weather-bot/internal/risk"
)

// helpers ─────────────────────────────────────────────────────────────────────

func boolPtr(b bool) *bool { return &b }

// newRecord builds a BetRecord with the given outcome and timestamp.
// Pass outcome=nil for an unresolved bet.
func newRecord(condID string, ts time.Time, size, mktPrice float64, outcome *bool) calibration.BetRecord {
	return calibration.BetRecord{
		ConditionID:    condID,
		Timestamp:      ts,
		Side:           "YES",
		OurProbability: 0.6,
		MarketPrice:    mktPrice,
		SizeUSDC:       size,
		Outcome:        outcome,
	}
}

// todayAt returns a time in today's UTC day at the given hour.
func todayAt(hour int) time.Time {
	now := time.Now().UTC().Truncate(24 * time.Hour)
	return now.Add(time.Duration(hour) * time.Hour)
}

// yesterdayAt returns a time in yesterday's UTC day.
func yesterdayAt(hour int) time.Time {
	return todayAt(hour).Add(-24 * time.Hour)
}

// DailyStats tests ─────────────────────────────────────────────────────────────

func TestDailyStats_EmptyHistory(t *testing.T) {
	count, pnl := risk.DailyStats(nil)
	if count != 0 || pnl != 0 {
		t.Fatalf("expected (0, 0), got (%d, %.2f)", count, pnl)
	}
}

func TestDailyStats_OnlyYesterdayBets(t *testing.T) {
	records := []calibration.BetRecord{
		newRecord("a", yesterdayAt(10), 10, 0.5, boolPtr(true)),
	}
	count, pnl := risk.DailyStats(records)
	if count != 0 || pnl != 0 {
		t.Fatalf("expected (0, 0) for yesterday's bets, got (%d, %.2f)", count, pnl)
	}
}

func TestDailyStats_TodayMix(t *testing.T) {
	// 2 bets today: one won, one unresolved
	records := []calibration.BetRecord{
		newRecord("win",  todayAt(8),  10, 0.5, boolPtr(true)),  // pnl = +10
		newRecord("open", todayAt(9),  5,  0.6, nil),            // unresolved
	}
	count, pnl := risk.DailyStats(records)
	if count != 2 {
		t.Fatalf("expected count=2, got %d", count)
	}
	// Won: 10 * (1/0.5 - 1) = 10 * 1 = +10
	wantPnL := 10.0
	if pnl != wantPnL {
		t.Fatalf("expected pnl=%.2f, got %.2f", wantPnL, pnl)
	}
}

func TestDailyStats_TodayLoss(t *testing.T) {
	records := []calibration.BetRecord{
		newRecord("loss", todayAt(8), 20, 0.4, boolPtr(false)),
	}
	count, pnl := risk.DailyStats(records)
	if count != 1 {
		t.Fatalf("expected count=1, got %d", count)
	}
	if pnl != -20 {
		t.Fatalf("expected pnl=-20, got %.2f", pnl)
	}
}

// OpenPositionsCount tests ────────────────────────────────────────────────────

func TestOpenPositionsCount(t *testing.T) {
	records := []calibration.BetRecord{
		newRecord("a", todayAt(1),     10, 0.5, nil),           // open
		newRecord("b", yesterdayAt(1), 10, 0.5, boolPtr(true)), // resolved
		newRecord("c", todayAt(2),     10, 0.5, nil),           // open
		newRecord("d", todayAt(3),     10, 0.5, boolPtr(false)),// resolved
	}
	n := risk.OpenPositionsCount(records)
	if n != 2 {
		t.Fatalf("expected 2 open positions, got %d", n)
	}
}

// AllowBet tests ──────────────────────────────────────────────────────────────

func TestAllowBet_EmptyHistory(t *testing.T) {
	m := risk.New(risk.DefaultConfig())
	if err := m.AllowBet(nil); err != nil {
		t.Fatalf("expected nil, got %v", err)
	}
}

func TestAllowBet_DailyBetCapExceeded(t *testing.T) {
	cfg := risk.Config{MaxDailyBets: 2, MaxDailyLossUSDC: 0, MaxOpenPositions: 0}
	m := risk.New(cfg)

	records := []calibration.BetRecord{
		newRecord("a", todayAt(1), 5, 0.5, nil),
		newRecord("b", todayAt(2), 5, 0.5, nil),
	}
	err := m.AllowBet(records)
	if err == nil {
		t.Fatal("expected error for daily bet cap, got nil")
	}
}

func TestAllowBet_DailyBetCapNotYetExceeded(t *testing.T) {
	cfg := risk.Config{MaxDailyBets: 3, MaxDailyLossUSDC: 0, MaxOpenPositions: 0}
	m := risk.New(cfg)

	records := []calibration.BetRecord{
		newRecord("a", todayAt(1), 5, 0.5, nil),
		newRecord("b", todayAt(2), 5, 0.5, nil),
	}
	if err := m.AllowBet(records); err != nil {
		t.Fatalf("expected nil, got %v", err)
	}
}

func TestAllowBet_DailyLossLimitHit(t *testing.T) {
	cfg := risk.Config{MaxDailyBets: 0, MaxDailyLossUSDC: 30, MaxOpenPositions: 0}
	m := risk.New(cfg)

	records := []calibration.BetRecord{
		newRecord("a", todayAt(1), 40, 0.5, boolPtr(false)), // -40 today
	}
	err := m.AllowBet(records)
	if err == nil {
		t.Fatal("expected error for daily loss limit, got nil")
	}
}

func TestAllowBet_DailyLossLimitNotHit(t *testing.T) {
	cfg := risk.Config{MaxDailyBets: 0, MaxDailyLossUSDC: 100, MaxOpenPositions: 0}
	m := risk.New(cfg)

	records := []calibration.BetRecord{
		newRecord("a", todayAt(1), 20, 0.5, boolPtr(false)), // -20 today
	}
	if err := m.AllowBet(records); err != nil {
		t.Fatalf("expected nil, got %v", err)
	}
}

func TestAllowBet_OpenPositionCapHit(t *testing.T) {
	cfg := risk.Config{MaxDailyBets: 0, MaxDailyLossUSDC: 0, MaxOpenPositions: 2}
	m := risk.New(cfg)

	records := []calibration.BetRecord{
		newRecord("a", yesterdayAt(1), 5, 0.5, nil), // old open
		newRecord("b", todayAt(1),     5, 0.5, nil), // today open
	}
	err := m.AllowBet(records)
	if err == nil {
		t.Fatal("expected error for open-position cap, got nil")
	}
}

func TestAllowBet_AllLimitsZeroMeansUnlimited(t *testing.T) {
	m := risk.New(risk.Config{}) // all zeros → no limits

	// 100 open unresolved bets today
	records := make([]calibration.BetRecord, 100)
	for i := range records {
		records[i] = newRecord("x", todayAt(1), 10, 0.5, nil)
	}
	if err := m.AllowBet(records); err != nil {
		t.Fatalf("expected nil with no limits configured, got %v", err)
	}
}

// Profit target tests ─────────────────────────────────────────────────────────

func TestAllowBet_ProfitTargetNotReached(t *testing.T) {
	cfg := risk.Config{MaxDailyProfitUSDC: 100.0}
	m := risk.New(cfg)

	// Won bet with 10 USDC at market price 0.5 → profit = 10*(1/0.5-1) = 10 USDC
	records := []calibration.BetRecord{
		newRecord("a", todayAt(1), 10, 0.5, boolPtr(true)),
	}
	if err := m.AllowBet(records); err != nil {
		t.Fatalf("expected nil (profit 10 < target 100), got %v", err)
	}
}

func TestAllowBet_ProfitTargetReached(t *testing.T) {
	cfg := risk.Config{MaxDailyProfitUSDC: 5.0}
	m := risk.New(cfg)

	// Won bet: 10 USDC at 0.5 → profit = 10 USDC > target 5
	records := []calibration.BetRecord{
		newRecord("a", todayAt(1), 10, 0.5, boolPtr(true)),
	}
	err := m.AllowBet(records)
	if err == nil {
		t.Fatal("expected error for profit target, got nil")
	}
}

func TestAllowBet_ProfitTargetDisabled(t *testing.T) {
	cfg := risk.Config{MaxDailyProfitUSDC: 0} // 0 = disabled
	m := risk.New(cfg)

	// Even huge profits should not block when target is 0
	records := make([]calibration.BetRecord, 10)
	for i := range records {
		records[i] = newRecord("x", todayAt(1), 100, 0.1, boolPtr(true)) // massive wins
	}
	if err := m.AllowBet(records); err != nil {
		t.Fatalf("expected nil (target disabled), got %v", err)
	}
}

// Summary test ────────────────────────────────────────────────────────────────

func TestSummary_ContainsKeyFields(t *testing.T) {
	cfg := risk.DefaultConfig()
	records := []calibration.BetRecord{
		newRecord("a", todayAt(1), 10, 0.5, boolPtr(true)),
	}
	s := risk.Summary(records, cfg)
	for _, want := range []string{"daily_bets=", "daily_pnl=", "open=", "max_daily_bets="} {
		if !contains(s, want) {
			t.Errorf("Summary missing %q in %q", want, s)
		}
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(s) > 0 && containsStr(s, sub))
}

func containsStr(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
