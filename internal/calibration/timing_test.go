package calibration

import (
	"os"
	"testing"
	"time"
)

// ptr is a test helper to get *bool.
func boolPtr(v bool) *bool { return &v }

func TestTimingMultiplier_NotEnoughGlobalData(t *testing.T) {
	// All buckets empty → should return 1.0.
	var buckets [24]HourBucket
	for i := range buckets {
		buckets[i].Hour = i
	}
	got := TimingMultiplier(buckets, 14)
	if got != 1.0 {
		t.Errorf("expected 1.0, got %.4f", got)
	}
}

func TestTimingMultiplier_HourBelowMinSamples(t *testing.T) {
	// Global has data but target hour has < 5 bets.
	var buckets [24]HourBucket
	for i := range buckets {
		buckets[i].Hour = i
		// Fill all hours except 10 with enough data.
		if i != 10 {
			buckets[i].Wins = 6
			buckets[i].Losses = 4
		}
	}
	buckets[10].Wins = 2
	buckets[10].Losses = 1

	got := TimingMultiplier(buckets, 10)
	if got != 1.0 {
		t.Errorf("expected 1.0 (insufficient hour data), got %.4f", got)
	}
}

func TestTimingMultiplier_AverageHour(t *testing.T) {
	// All hours identical → multiplier should be ~1.0.
	var buckets [24]HourBucket
	for i := range buckets {
		buckets[i].Hour = i
		buckets[i].Wins = 7
		buckets[i].Losses = 3
	}
	got := TimingMultiplier(buckets, 12)
	if got < 0.99 || got > 1.01 {
		t.Errorf("expected ~1.0, got %.4f", got)
	}
}

func TestTimingMultiplier_BadHour(t *testing.T) {
	// Hour 3 has very poor win rate → multiplier should be < 1.0.
	var buckets [24]HourBucket
	for i := range buckets {
		buckets[i].Hour = i
		buckets[i].Wins = 7
		buckets[i].Losses = 3
	}
	// Hour 3: 1/10 = 10% vs global ~70%
	buckets[3].Wins = 1
	buckets[3].Losses = 9

	got := TimingMultiplier(buckets, 3)
	if got >= 1.0 {
		t.Errorf("bad hour should have multiplier < 1.0, got %.4f", got)
	}
	if got < 0.5 {
		t.Errorf("multiplier should not go below 0.5, got %.4f", got)
	}
}

func TestTimingMultiplier_GoodHour(t *testing.T) {
	// Hour 15 has excellent win rate → multiplier should be > 1.0 (capped at 1.2).
	var buckets [24]HourBucket
	for i := range buckets {
		buckets[i].Hour = i
		buckets[i].Wins = 5
		buckets[i].Losses = 5
	}
	// Hour 15: 10/10 = 100% vs global ~50%
	buckets[15].Wins = 10
	buckets[15].Losses = 0

	got := TimingMultiplier(buckets, 15)
	if got <= 1.0 {
		t.Errorf("good hour should have multiplier > 1.0, got %.4f", got)
	}
	if got > 1.21 {
		t.Errorf("multiplier should be capped at 1.2, got %.4f", got)
	}
}

func TestTimingMultiplier_InvalidHour(t *testing.T) {
	var buckets [24]HourBucket
	if got := TimingMultiplier(buckets, -1); got != 1.0 {
		t.Errorf("invalid hour -1: expected 1.0, got %.4f", got)
	}
	if got := TimingMultiplier(buckets, 24); got != 1.0 {
		t.Errorf("invalid hour 24: expected 1.0, got %.4f", got)
	}
}

func TestUpdateAndRebuildHourlyStats(t *testing.T) {
	dir := t.TempDir()

	win := true
	loss := false
	ts := time.Date(2026, 5, 28, 14, 30, 0, 0, time.UTC) // hour 14

	recs := []BetRecord{
		{Timestamp: ts, Outcome: &win},
		{Timestamp: ts, Outcome: &win},
		{Timestamp: ts, Outcome: &loss},
	}

	if err := RebuildHourlyStats(recs, dir); err != nil {
		t.Fatalf("rebuild: %v", err)
	}

	buckets, err := LoadHourlyStats(dir)
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	if buckets[14].Wins != 2 || buckets[14].Losses != 1 {
		t.Errorf("hour 14: expected 2W/1L, got %dW/%dL", buckets[14].Wins, buckets[14].Losses)
	}
	if buckets[0].Total() != 0 {
		t.Errorf("hour 0 should be empty, got total=%d", buckets[0].Total())
	}
}

func TestUpdateHourlyStats_UnresolvedIgnored(t *testing.T) {
	dir := t.TempDir()
	rec := BetRecord{
		Timestamp: time.Date(2026, 5, 28, 8, 0, 0, 0, time.UTC),
		Outcome:   nil, // unresolved
	}
	if err := UpdateHourlyStats(rec, dir); err != nil {
		t.Fatalf("update: %v", err)
	}
	// File should not exist (no data written).
	path := hourlyWinRatePath(dir)
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		// File was created — check that the bucket is empty.
		buckets, _ := LoadHourlyStats(dir)
		if buckets[8].Total() != 0 {
			t.Errorf("unresolved bet should not increment bucket, got total=%d", buckets[8].Total())
		}
	}
}

func TestHourlyTable_Length(t *testing.T) {
	var buckets [24]HourBucket
	for i := range buckets {
		buckets[i].Hour = i
		buckets[i].Wins = i
		buckets[i].Losses = 24 - i
	}
	rows := HourlyTable(buckets)
	if len(rows) != 24 {
		t.Errorf("expected 24 rows, got %d", len(rows))
	}
	for i, r := range rows {
		if r.Hour != i {
			t.Errorf("row[%d].Hour = %d, want %d", i, r.Hour, i)
		}
	}
}
