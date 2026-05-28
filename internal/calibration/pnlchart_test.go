package calibration

import (
	"testing"
	"time"
)

func makePnLRecord(ts time.Time, size, price float64, won bool) BetRecord {
	outcome := won
	return BetRecord{
		ConditionID:    "test",
		Timestamp:      ts,
		ResolvedAt:     ts,
		Side:           "YES",
		OurProbability: 0.6,
		MarketPrice:    price,
		SizeUSDC:       size,
		Outcome:        &outcome,
	}
}

func TestDailyPnLBars_Empty(t *testing.T) {
	bars, total := DailyPnLBars(nil, 14)
	if len([]rune(bars)) != 14 {
		t.Errorf("expected 14 chars for empty records, got %d: %q", len([]rune(bars)), bars)
	}
	if total != 0 {
		t.Errorf("expected total=0, got %.2f", total)
	}
}

func TestDailyPnLBars_WinToday(t *testing.T) {
	now := time.Now().UTC()
	records := []BetRecord{
		makePnLRecord(now, 5.0, 0.5, true), // win: profit = 5
	}
	bars, total := DailyPnLBars(records, 14)
	runes := []rune(bars)
	if len(runes) != 14 {
		t.Fatalf("expected 14 chars, got %d", len(runes))
	}
	last := runes[13] // today = last index
	if last == '·' || last == '▼' {
		t.Errorf("expected a positive block for today's win, got %q", last)
	}
	if total <= 0 {
		t.Errorf("expected positive total for a win, got %.2f", total)
	}
}

func TestDailyPnLBars_LossToday(t *testing.T) {
	now := time.Now().UTC()
	records := []BetRecord{
		makePnLRecord(now, 5.0, 0.5, false), // loss: -5
	}
	bars, total := DailyPnLBars(records, 14)
	runes := []rune(bars)
	if runes[13] != '▼' {
		t.Errorf("expected ▼ for today's loss, got %q", runes[13])
	}
	if total >= 0 {
		t.Errorf("expected negative total for a loss, got %.2f", total)
	}
}

func TestDailyPnLLine_Format(t *testing.T) {
	now := time.Now().UTC()
	records := []BetRecord{
		makePnLRecord(now, 5.0, 0.5, true),
	}
	line := DailyPnLLine(records, 14)
	if line == "" {
		t.Error("expected non-empty line")
	}
	// Should contain "P&L 14d:"
	if len(line) < 10 {
		t.Errorf("line too short: %q", line)
	}
}
