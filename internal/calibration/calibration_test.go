package calibration

import (
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/devher0/polymarket-weather-bot/internal/markets"
	"github.com/devher0/polymarket-weather-bot/internal/strategy"
)

// ─── test helpers ─────────────────────────────────────────────────────────────

// tempDir creates a temporary directory for isolation and returns a cleanup func.
func tempDir(t *testing.T) (string, func()) {
	t.Helper()
	dir, err := os.MkdirTemp("", "calibration_test_*")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	return dir, func() { os.RemoveAll(dir) }
}

func makeDecision(condID string, ourP, mktP, size float64) *strategy.Decision {
	return &strategy.Decision{
		Market: markets.Market{
			ConditionID: condID,
			City:        "new_york",
			Signal:      "rain",
		},
		Side:           "YES",
		TokenID:        "tok-yes",
		OurProbability: ourP,
		MarketPrice:    mktP,
		Edge:           ourP - mktP,
		SizeUSDC:       size,
		Reason:         "test",
	}
}

// ─── SaveBet ──────────────────────────────────────────────────────────────────

func TestSaveBet_CreateFile(t *testing.T) {
	dir, cleanup := tempDir(t)
	defer cleanup()

	d := makeDecision("cond-001", 0.70, 0.50, 5.0)
	if err := SaveBet(d, dir); err != nil {
		t.Fatalf("SaveBet: %v", err)
	}

	path := filepath.Join(dir, csvFileName)
	if _, err := os.Stat(path); os.IsNotExist(err) {
		t.Error("expected CSV file to be created")
	}
}

func TestSaveBet_NilDecision(t *testing.T) {
	dir, cleanup := tempDir(t)
	defer cleanup()

	if err := SaveBet(nil, dir); err == nil {
		t.Error("expected error for nil decision, got nil")
	}
}

func TestSaveBet_MultipleRows(t *testing.T) {
	dir, cleanup := tempDir(t)
	defer cleanup()

	for i := 0; i < 5; i++ {
		d := makeDecision("cond-multi", 0.60, 0.50, 3.0)
		if err := SaveBet(d, dir); err != nil {
			t.Fatalf("SaveBet[%d]: %v", i, err)
		}
	}

	records, err := LoadHistory(dir)
	if err != nil {
		t.Fatalf("LoadHistory: %v", err)
	}
	if len(records) != 5 {
		t.Errorf("expected 5 records, got %d", len(records))
	}
}

// ─── LoadHistory ─────────────────────────────────────────────────────────────

func TestLoadHistory_Empty(t *testing.T) {
	dir, cleanup := tempDir(t)
	defer cleanup()

	records, err := LoadHistory(dir)
	if err != nil {
		t.Fatalf("LoadHistory on empty dir: %v", err)
	}
	if records != nil {
		t.Errorf("expected nil records for missing file, got %v", records)
	}
}

func TestLoadHistory_RoundTrip(t *testing.T) {
	dir, cleanup := tempDir(t)
	defer cleanup()

	d := makeDecision("cond-rt", 0.75, 0.50, 7.50)
	if err := SaveBet(d, dir); err != nil {
		t.Fatalf("SaveBet: %v", err)
	}

	records, err := LoadHistory(dir)
	if err != nil {
		t.Fatalf("LoadHistory: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("expected 1 record, got %d", len(records))
	}

	r := records[0]
	if r.ConditionID != "cond-rt" {
		t.Errorf("ConditionID: want %q, got %q", "cond-rt", r.ConditionID)
	}
	if math.Abs(r.OurProbability-0.75) > 1e-4 {
		t.Errorf("OurProbability: want 0.75, got %.6f", r.OurProbability)
	}
	if math.Abs(r.MarketPrice-0.50) > 1e-4 {
		t.Errorf("MarketPrice: want 0.50, got %.6f", r.MarketPrice)
	}
	if math.Abs(r.SizeUSDC-7.50) > 1e-2 {
		t.Errorf("SizeUSDC: want 7.50, got %.2f", r.SizeUSDC)
	}
	if r.Outcome != nil {
		t.Errorf("expected unresolved (nil) outcome, got %v", r.Outcome)
	}
}

// ─── UpdateOutcome ────────────────────────────────────────────────────────────

func TestUpdateOutcome_Win(t *testing.T) {
	dir, cleanup := tempDir(t)
	defer cleanup()

	d := makeDecision("cond-win", 0.70, 0.45, 10.0)
	if err := SaveBet(d, dir); err != nil {
		t.Fatalf("SaveBet: %v", err)
	}

	if err := UpdateOutcome("cond-win", true, dir); err != nil {
		t.Fatalf("UpdateOutcome: %v", err)
	}

	records, err := LoadHistory(dir)
	if err != nil {
		t.Fatalf("LoadHistory: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("expected 1 record, got %d", len(records))
	}
	if records[0].Outcome == nil {
		t.Fatal("expected resolved outcome, got nil")
	}
	if !*records[0].Outcome {
		t.Errorf("expected outcome=true, got false")
	}
	if records[0].ResolvedAt.IsZero() {
		t.Error("expected non-zero ResolvedAt")
	}
}

func TestUpdateOutcome_Loss(t *testing.T) {
	dir, cleanup := tempDir(t)
	defer cleanup()

	d := makeDecision("cond-loss", 0.30, 0.55, 4.0)
	if err := SaveBet(d, dir); err != nil {
		t.Fatalf("SaveBet: %v", err)
	}
	if err := UpdateOutcome("cond-loss", false, dir); err != nil {
		t.Fatalf("UpdateOutcome: %v", err)
	}

	records, _ := LoadHistory(dir)
	if *records[0].Outcome != false {
		t.Errorf("expected outcome=false, got true")
	}
}

func TestUpdateOutcome_NotFound(t *testing.T) {
	dir, cleanup := tempDir(t)
	defer cleanup()

	d := makeDecision("cond-exists", 0.60, 0.40, 5.0)
	_ = SaveBet(d, dir)

	err := UpdateOutcome("cond-missing", true, dir)
	if err == nil {
		t.Error("expected error for missing conditionID, got nil")
	}
}

// ─── BrierScore ──────────────────────────────────────────────────────────────

func TestBrierScore_NoResolved(t *testing.T) {
	records := []BetRecord{
		{OurProbability: 0.7, MarketPrice: 0.5, Outcome: nil},
	}
	score, count, err := BrierScore(records)
	if err != nil {
		t.Fatalf("BrierScore: %v", err)
	}
	if count != 0 {
		t.Errorf("expected count=0, got %d", count)
	}
	if score != 0 {
		t.Errorf("expected score=0, got %g", score)
	}
}

func TestBrierScore_PerfectPredictions(t *testing.T) {
	win := true
	loss := false
	records := []BetRecord{
		// Win and we predicted 1.0
		{OurProbability: 1.0, Outcome: &win},
		// Loss and we predicted 0.0
		{OurProbability: 0.0, Outcome: &loss},
	}
	score, count, err := BrierScore(records)
	if err != nil {
		t.Fatalf("BrierScore: %v", err)
	}
	if count != 2 {
		t.Errorf("expected count=2, got %d", count)
	}
	if score > 1e-9 {
		t.Errorf("expected perfect score~0, got %g", score)
	}
}

func TestBrierScore_Random(t *testing.T) {
	// 0.5 probability on all outcomes → Brier score = 0.25.
	win := true
	loss := false
	records := []BetRecord{
		{OurProbability: 0.5, Outcome: &win},
		{OurProbability: 0.5, Outcome: &loss},
	}
	score, _, _ := BrierScore(records)
	if math.Abs(score-0.25) > 1e-9 {
		t.Errorf("expected Brier score=0.25 for random predictor, got %g", score)
	}
}

func TestBrierScore_MixedResolved(t *testing.T) {
	// Only resolved bets contribute to the score.
	win := true
	records := []BetRecord{
		{OurProbability: 0.8, Outcome: &win},
		{OurProbability: 0.6, Outcome: nil}, // unresolved — ignored
	}
	_, count, _ := BrierScore(records)
	if count != 1 {
		t.Errorf("expected count=1 (only resolved), got %d", count)
	}
}

// ─── LoadOpenPositions ────────────────────────────────────────────────────────

func TestLoadOpenPositions_Empty(t *testing.T) {
	dir, cleanup := tempDir(t)
	defer cleanup()

	pos, err := LoadOpenPositions(dir)
	if err != nil {
		t.Fatalf("LoadOpenPositions: %v", err)
	}
	if len(pos) != 0 {
		t.Errorf("expected empty map, got %v", pos)
	}
}

func TestLoadOpenPositions_UnresolvedOnly(t *testing.T) {
	dir, cleanup := tempDir(t)
	defer cleanup()

	d1 := makeDecision("cond-open", 0.65, 0.45, 8.0)
	d2 := makeDecision("cond-closed", 0.55, 0.40, 5.0)
	_ = SaveBet(d1, dir)
	_ = SaveBet(d2, dir)
	_ = UpdateOutcome("cond-closed", true, dir)

	pos, err := LoadOpenPositions(dir)
	if err != nil {
		t.Fatalf("LoadOpenPositions: %v", err)
	}
	if !pos["cond-open"] {
		t.Error("expected cond-open to be in open positions")
	}
	if pos["cond-closed"] {
		t.Error("expected cond-closed to NOT be in open positions (already resolved)")
	}
}

func TestLoadOpenPositions_MultipleOpenSameID(t *testing.T) {
	dir, cleanup := tempDir(t)
	defer cleanup()

	d := makeDecision("cond-dup", 0.60, 0.45, 5.0)
	_ = SaveBet(d, dir)
	_ = SaveBet(d, dir) // duplicate (bot would normally skip this)

	pos, err := LoadOpenPositions(dir)
	if err != nil {
		t.Fatalf("LoadOpenPositions: %v", err)
	}
	if !pos["cond-dup"] {
		t.Error("expected cond-dup to be in open positions")
	}
}

// ─── SaveBet preserves timestamp order ───────────────────────────────────────

func TestSaveBet_TimestampOrder(t *testing.T) {
	dir, cleanup := tempDir(t)
	defer cleanup()

	// Truncate to second precision because SaveBet stores RFC3339 (no sub-second).
	start := time.Now().UTC().Truncate(time.Second)
	_ = SaveBet(makeDecision("cond-a", 0.6, 0.4, 5), dir)
	_ = SaveBet(makeDecision("cond-b", 0.7, 0.5, 3), dir)

	records, _ := LoadHistory(dir)
	if len(records) != 2 {
		t.Fatalf("expected 2 records, got %d", len(records))
	}
	// Timestamp should be >= start (RFC3339 precision = 1s).
	if records[0].Timestamp.Before(start) {
		t.Errorf("first timestamp %v should be >= start %v", records[0].Timestamp, start)
	}
	// Second bet is appended after the first — its timestamp must not precede it.
	if records[1].Timestamp.Before(records[0].Timestamp) {
		t.Error("second timestamp should not be before first")
	}
}
