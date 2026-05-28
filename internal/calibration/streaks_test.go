package calibration

import (
	"testing"
	"time"
)

// makeStreakRecord creates a minimal BetRecord with a specific resolved time and outcome.
func makeStreakRecord(ts time.Time, outcome *bool) BetRecord {
	r := makeRecord("new_york", "rain", 0.6, outcome)
	r.Timestamp = ts
	r.ResolvedAt = ts
	return r
}

// TestComputeStreak_Empty — no records at all.
func TestComputeStreak_Empty(t *testing.T) {
	n, kind := ComputeStreak(nil)
	if n != 0 || kind != "" {
		t.Fatalf("expected (0,''), got (%d,%q)", n, kind)
	}
}

// TestComputeStreak_AllUnresolved — no resolved bets.
func TestComputeStreak_AllUnresolved(t *testing.T) {
	records := []BetRecord{
		makeStreakRecord(time.Now(), nil),
		makeStreakRecord(time.Now().Add(-time.Hour), nil),
	}
	n, kind := ComputeStreak(records)
	if n != 0 || kind != "" {
		t.Fatalf("expected (0,''), got (%d,%q)", n, kind)
	}
}

// TestComputeStreak_WinsOnly — all resolved as wins.
func TestComputeStreak_WinsOnly(t *testing.T) {
	base := time.Now()
	win := true
	records := []BetRecord{
		makeStreakRecord(base.Add(-3*time.Hour), &win),
		makeStreakRecord(base.Add(-2*time.Hour), &win),
		makeStreakRecord(base.Add(-1*time.Hour), &win),
	}
	n, kind := ComputeStreak(records)
	if n != 3 || kind != "wins" {
		t.Fatalf("expected (3,wins), got (%d,%q)", n, kind)
	}
}

// TestComputeStreak_LossesOnly — all resolved as losses.
func TestComputeStreak_LossesOnly(t *testing.T) {
	base := time.Now()
	loss := false
	records := []BetRecord{
		makeStreakRecord(base.Add(-3*time.Hour), &loss),
		makeStreakRecord(base.Add(-2*time.Hour), &loss),
		makeStreakRecord(base.Add(-1*time.Hour), &loss),
	}
	n, kind := ComputeStreak(records)
	if n != 3 || kind != "losses" {
		t.Fatalf("expected (3,losses), got (%d,%q)", n, kind)
	}
}

// TestComputeStreak_MixedEndingInWin — tail is wins.
func TestComputeStreak_MixedEndingInWin(t *testing.T) {
	base := time.Now()
	win := true
	loss := false
	records := []BetRecord{
		makeStreakRecord(base.Add(-5*time.Hour), &loss),
		makeStreakRecord(base.Add(-4*time.Hour), &loss),
		makeStreakRecord(base.Add(-3*time.Hour), &win),
		makeStreakRecord(base.Add(-2*time.Hour), &win),
		makeStreakRecord(base.Add(-1*time.Hour), &win),
	}
	n, kind := ComputeStreak(records)
	if n != 3 || kind != "wins" {
		t.Fatalf("expected (3,wins), got (%d,%q)", n, kind)
	}
}

// TestStreakAlert_BelowThreshold — 3 losses, alert needs 4.
func TestStreakAlert_BelowThreshold(t *testing.T) {
	base := time.Now()
	loss := false
	records := []BetRecord{
		makeStreakRecord(base.Add(-3*time.Hour), &loss),
		makeStreakRecord(base.Add(-2*time.Hour), &loss),
		makeStreakRecord(base.Add(-1*time.Hour), &loss),
	}
	ok, msg := StreakAlert(records, 4)
	if ok || msg != "" {
		t.Fatalf("expected no alert, got ok=%v msg=%q", ok, msg)
	}
}

// TestStreakAlert_AtThreshold — exactly 4 consecutive losses.
func TestStreakAlert_AtThreshold(t *testing.T) {
	base := time.Now()
	loss := false
	records := []BetRecord{
		makeStreakRecord(base.Add(-4*time.Hour), &loss),
		makeStreakRecord(base.Add(-3*time.Hour), &loss),
		makeStreakRecord(base.Add(-2*time.Hour), &loss),
		makeStreakRecord(base.Add(-1*time.Hour), &loss),
	}
	ok, msg := StreakAlert(records, 4)
	if !ok {
		t.Fatalf("expected alert at threshold, got ok=%v", ok)
	}
	if msg == "" {
		t.Fatalf("expected non-empty alert message")
	}
}

// TestStreakAlert_WinStreak — wins should never trigger alert.
func TestStreakAlert_WinStreak(t *testing.T) {
	base := time.Now()
	win := true
	records := []BetRecord{
		makeStreakRecord(base.Add(-5*time.Hour), &win),
		makeStreakRecord(base.Add(-4*time.Hour), &win),
		makeStreakRecord(base.Add(-3*time.Hour), &win),
		makeStreakRecord(base.Add(-2*time.Hour), &win),
		makeStreakRecord(base.Add(-1*time.Hour), &win),
	}
	ok, _ := StreakAlert(records, 4)
	if ok {
		t.Fatalf("win streak should not trigger alert")
	}
}
