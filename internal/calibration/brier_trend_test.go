package calibration

import (
	"math"
	"testing"
	"time"
)

// makeBet creates a resolved BetRecord with a given OurProbability, win/loss,
// and resolved timestamp.
func makeBet(ourP float64, win bool, resolvedAt time.Time) BetRecord {
	outcome := win
	return BetRecord{
		OurProbability: ourP,
		Outcome:        &outcome,
		ResolvedAt:     resolvedAt,
	}
}

// isoWeekMiddle returns a time within the ISO week that was `weeksAgo` full
// ISO weeks before the current week. dayInWeek 0=Mon, 2=Wed, 4=Fri.
func isoWeekMiddle(weeksAgo, dayInWeek int) time.Time {
	now := time.Now().UTC()
	// Find Monday of this ISO week.
	wd := int(now.Weekday())
	if wd == 0 {
		wd = 7
	}
	monday := now.AddDate(0, 0, -(wd - 1))
	monday = time.Date(monday.Year(), monday.Month(), monday.Day(), 12, 0, 0, 0, time.UTC)
	return monday.AddDate(0, 0, -weeksAgo*7+dayInWeek)
}

func TestBrierTrend_InsufficientData(t *testing.T) {
	// Empty history → (0, 0)
	slope, r2 := BrierTrend(nil, 4)
	if slope != 0 || r2 != 0 {
		t.Errorf("expected (0,0) for empty records, got (%v, %v)", slope, r2)
	}

	// Only 2 qualifying weeks → need ≥3 qualifying weeks
	records := []BetRecord{}
	for d := 0; d < 5; d++ {
		records = append(records, makeBet(0.7, true, isoWeekMiddle(1, d%5)))
	}
	for d := 0; d < 5; d++ {
		records = append(records, makeBet(0.6, false, isoWeekMiddle(2, d%5)))
	}
	slope, r2 = BrierTrend(records, 4)
	if slope != 0 || r2 != 0 {
		t.Errorf("expected (0,0) for only 2 qualifying weeks, got (%v, %v)", slope, r2)
	}
}

func TestBrierTrend_NoTrend(t *testing.T) {
	// Stable Brier score across 4 weeks → slope ≈ 0.
	records := make([]BetRecord, 0, 20)
	for w := 1; w <= 4; w++ {
		for d := 0; d < 5; d++ {
			records = append(records, makeBet(0.65, true, isoWeekMiddle(w, d%5)))
		}
	}
	slope, _ := BrierTrend(records, 5)
	if math.Abs(slope) > 1e-6 {
		t.Errorf("expected ~0 slope for stable scores, got %v", slope)
	}
}

func TestBrierTrend_Worsening(t *testing.T) {
	// Build a worsening trend: week 4 Brier≈0.01, week 3 Brier≈0.25,
	// week 2 Brier≈0.36, week 1 Brier≈0.49.
	records := make([]BetRecord, 0, 20)
	for d := 0; d < 5; d++ {
		records = append(records, makeBet(0.90, true, isoWeekMiddle(4, d%5))) // (0.9-1)²=0.01
	}
	for d := 0; d < 5; d++ {
		records = append(records, makeBet(0.50, true, isoWeekMiddle(3, d%5))) // (0.5-1)²=0.25
	}
	for d := 0; d < 5; d++ {
		records = append(records, makeBet(0.40, true, isoWeekMiddle(2, d%5))) // (0.4-1)²=0.36
	}
	for d := 0; d < 5; d++ {
		records = append(records, makeBet(0.30, true, isoWeekMiddle(1, d%5))) // (0.3-1)²=0.49
	}

	slope, _ := BrierTrend(records, 5)
	if slope <= 0 {
		t.Errorf("expected positive slope for worsening Brier, got %v", slope)
	}
}

func TestBrierTrendAlert_HighSlopeHighR2(t *testing.T) {
	// Strongly worsening linear trend across 3 iso weeks (slope ≫ 0.015, R² = 1).
	// Week 3 ago: Brier ≈ 0.01 (very accurate).
	// Week 2 ago: Brier ≈ 0.25.
	// Week 1 ago: Brier ≈ 0.49.
	records := make([]BetRecord, 0, 15)
	for d := 0; d < 5; d++ {
		records = append(records, makeBet(0.90, true, isoWeekMiddle(3, d%5)))
	}
	for d := 0; d < 5; d++ {
		records = append(records, makeBet(0.50, true, isoWeekMiddle(2, d%5)))
	}
	for d := 0; d < 5; d++ {
		records = append(records, makeBet(0.30, true, isoWeekMiddle(1, d%5)))
	}

	alerted, msg := BrierTrendAlert(records)
	if !alerted {
		slope, r2 := BrierTrend(records, 3)
		t.Errorf("expected alert for strong worsening trend (slope=%.4f r2=%.4f)", slope, r2)
	}
	if msg == "" {
		t.Error("expected non-empty alert message")
	}
}
