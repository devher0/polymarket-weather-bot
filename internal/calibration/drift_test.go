package calibration

import (
	"testing"
	"time"
)

// makeResolvedAt creates a BetRecord with a specific ResolvedAt time and known outcome.
func makeResolvedAt(daysAgo int, won bool, ourProb float64) BetRecord {
	o := won
	return BetRecord{
		OurProbability: ourProb,
		Outcome:        &o,
		ResolvedAt:     time.Now().UTC().AddDate(0, 0, -daysAgo),
	}
}

// TestBrierWindow_Empty verifies that an empty slice returns (0, 0).
func TestBrierWindow_Empty(t *testing.T) {
	score, count := BrierWindow(nil, 14)
	if score != 0 || count != 0 {
		t.Errorf("empty: got (%v, %v) want (0, 0)", score, count)
	}
}

// TestBrierWindow_AllUnresolved checks that unresolved bets are ignored.
func TestBrierWindow_AllUnresolved(t *testing.T) {
	records := []BetRecord{
		{OurProbability: 0.8, Outcome: nil, ResolvedAt: time.Now().AddDate(0, 0, -5)},
	}
	score, count := BrierWindow(records, 14)
	if score != 0 || count != 0 {
		t.Errorf("unresolved: got (%v, %v) want (0, 0)", score, count)
	}
}

// TestBrierWindow_OutsideWindow checks that bets older than days are excluded.
func TestBrierWindow_OutsideWindow(t *testing.T) {
	// 20-day-old bet — outside 14d window.
	r := makeResolvedAt(20, true, 0.9)
	score, count := BrierWindow([]BetRecord{r}, 14)
	if count != 0 {
		t.Errorf("outside window: got count=%d want 0, score=%v", count, score)
	}
}

// TestBrierWindow_Correct verifies Brier computation for a simple case.
// Bet: ourProb=0.8, won=true → brier=(0.8-1)²=0.04
func TestBrierWindow_Correct(t *testing.T) {
	r := makeResolvedAt(3, true, 0.8)
	score, count := BrierWindow([]BetRecord{r}, 14)
	if count != 1 {
		t.Fatalf("count: got %d want 1", count)
	}
	want := 0.04
	if diff := score - want; diff < -0.0001 || diff > 0.0001 {
		t.Errorf("score: got %.6f want %.6f", score, want)
	}
}

// TestDriftAlert_NoDrift checks that no alert fires when performance is stable.
func TestDriftAlert_NoDrift(t *testing.T) {
	// Generate 30 bets with consistent performance.
	var records []BetRecord
	for i := 0; i < 30; i++ {
		records = append(records, makeResolvedAt(i, true, 0.75))
	}
	alert, msg := DriftAlert(records, 14, 30, 0.15)
	if alert {
		t.Errorf("unexpected alert: %s", msg)
	}
}

// TestDriftAlert_DriftDetected checks that an alert fires when recent bets are worse.
func TestDriftAlert_DriftDetected(t *testing.T) {
	var records []BetRecord
	// Older bets (15–29 days ago): good performance (prob≈outcome).
	for i := 15; i < 30; i++ {
		records = append(records, makeResolvedAt(i, true, 0.95)) // Brier≈0.0025
	}
	// Recent bets (0–13 days ago): bad performance.
	for i := 0; i < 14; i++ {
		records = append(records, makeResolvedAt(i, false, 0.9)) // Brier≈0.81
	}
	alert, msg := DriftAlert(records, 14, 30, 0.15)
	if !alert {
		t.Error("expected drift alert, got none")
	}
	if msg == "" {
		t.Error("expected non-empty message")
	}
}

// TestDriftAlert_InsufficientData checks that no alert fires with < 5 recent bets.
func TestDriftAlert_InsufficientData(t *testing.T) {
	var records []BetRecord
	// Only 3 recent bets — below minimum.
	for i := 0; i < 3; i++ {
		records = append(records, makeResolvedAt(i, false, 0.9))
	}
	// Plenty of base data.
	for i := 15; i < 30; i++ {
		records = append(records, makeResolvedAt(i, true, 0.9))
	}
	alert, _ := DriftAlert(records, 14, 30, 0.15)
	if alert {
		t.Error("should not alert with < 5 recent resolved bets")
	}
}
