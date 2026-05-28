package calibration

import (
	"testing"
	"time"
)

func makeDayRecord(day time.Weekday, won bool, size float64, mktPrice float64) BetRecord {
	// Find a time that lands on the target weekday (UTC).
	base := time.Date(2026, 5, 25, 12, 0, 0, 0, time.UTC) // Monday 2026-05-25
	offset := int(day) - int(base.Weekday())
	if offset < 0 {
		offset += 7
	}
	ts := base.AddDate(0, 0, offset)
	outcome := won
	return BetRecord{
		Timestamp:   ts,
		SizeUSDC:    size,
		MarketPrice: mktPrice,
		Outcome:     &outcome,
	}
}

func TestWeekdayBreakdown_Empty(t *testing.T) {
	stats := WeekdayBreakdown(nil)
	for _, s := range stats {
		if s.Bets != 0 {
			t.Errorf("day %s: want Bets=0, got %d", s.Day, s.Bets)
		}
	}
}

func TestWeekdayBreakdown_OneDay(t *testing.T) {
	records := []BetRecord{
		makeDayRecord(time.Monday, true, 2.0, 0.50),
		makeDayRecord(time.Monday, false, 1.0, 0.50),
	}
	stats := WeekdayBreakdown(records)
	mon := stats[time.Monday]
	if mon.Bets != 2 {
		t.Errorf("Monday: want 2 bets, got %d", mon.Bets)
	}
	if mon.Wins != 1 {
		t.Errorf("Monday: want 1 win, got %d", mon.Wins)
	}
}

func TestWeekdayBreakdown_AllDays(t *testing.T) {
	var records []BetRecord
	days := []time.Weekday{time.Sunday, time.Monday, time.Tuesday, time.Wednesday, time.Thursday, time.Friday, time.Saturday}
	for _, d := range days {
		records = append(records, makeDayRecord(d, true, 1.0, 0.50))
	}
	stats := WeekdayBreakdown(records)
	for i, s := range stats {
		if s.Bets != 1 {
			t.Errorf("day %d: want 1 bet, got %d", i, s.Bets)
		}
	}
}

func TestBestWorstWeekday(t *testing.T) {
	// Monday: win (ROI positive), Tuesday: loss (ROI negative)
	records := []BetRecord{
		makeDayRecord(time.Monday, true, 2.0, 0.50),
		makeDayRecord(time.Tuesday, false, 2.0, 0.50),
	}
	stats := WeekdayBreakdown(records)
	best := BestWeekday(stats, 1)
	worst := WorstWeekday(stats, 1)
	if best != int(time.Monday) {
		t.Errorf("want best=Monday, got day %d", best)
	}
	if worst != int(time.Tuesday) {
		t.Errorf("want worst=Tuesday, got day %d", worst)
	}
}
