package calibration

import (
	"testing"
	"time"
)

func TestCurrentStreak_Empty(t *testing.T) {
	s := CurrentStreak(nil)
	if s.Count != 0 {
		t.Errorf("expected Count=0, got %d", s.Count)
	}
}

func TestCurrentStreak_OnlyUnresolved(t *testing.T) {
	records := []BetRecord{
		{ConditionID: "a", Outcome: nil},
		{ConditionID: "b", Outcome: nil},
	}
	s := CurrentStreak(records)
	if s.Count != 0 {
		t.Errorf("expected Count=0 for all-unresolved, got %d", s.Count)
	}
}

func TestCurrentStreak_SingleWin(t *testing.T) {
	records := []BetRecord{
		{ConditionID: "a", Outcome: boolPtr(true), ResolvedAt: time.Unix(100, 0)},
	}
	s := CurrentStreak(records)
	if s.Count != 1 || !s.IsWin {
		t.Errorf("expected 1 win streak, got Count=%d IsWin=%v", s.Count, s.IsWin)
	}
}

func TestCurrentStreak_TwoLosses(t *testing.T) {
	records := []BetRecord{
		{ConditionID: "a", Outcome: boolPtr(true), ResolvedAt: time.Unix(100, 0)},
		{ConditionID: "b", Outcome: boolPtr(false), ResolvedAt: time.Unix(200, 0)},
		{ConditionID: "c", Outcome: boolPtr(false), ResolvedAt: time.Unix(300, 0)},
	}
	s := CurrentStreak(records)
	if s.Count != 2 || s.IsWin {
		t.Errorf("expected 2-loss streak, got Count=%d IsWin=%v", s.Count, s.IsWin)
	}
}

func TestCurrentStreak_ThreeLosses(t *testing.T) {
	records := []BetRecord{
		{ConditionID: "a", Outcome: boolPtr(false), ResolvedAt: time.Unix(100, 0)},
		{ConditionID: "b", Outcome: boolPtr(false), ResolvedAt: time.Unix(200, 0)},
		{ConditionID: "c", Outcome: boolPtr(false), ResolvedAt: time.Unix(300, 0)},
	}
	s := CurrentStreak(records)
	if s.Count != 3 || s.IsWin {
		t.Errorf("expected 3-loss streak, got Count=%d IsWin=%v", s.Count, s.IsWin)
	}
}

func TestCurrentStreak_ResetAfterWin(t *testing.T) {
	// Three losses followed by a win — streak should be 1 win.
	records := []BetRecord{
		{ConditionID: "a", Outcome: boolPtr(false), ResolvedAt: time.Unix(100, 0)},
		{ConditionID: "b", Outcome: boolPtr(false), ResolvedAt: time.Unix(200, 0)},
		{ConditionID: "c", Outcome: boolPtr(false), ResolvedAt: time.Unix(300, 0)},
		{ConditionID: "d", Outcome: boolPtr(true), ResolvedAt: time.Unix(400, 0)},
	}
	s := CurrentStreak(records)
	if s.Count != 1 || !s.IsWin {
		t.Errorf("expected 1-win streak after recovery, got Count=%d IsWin=%v", s.Count, s.IsWin)
	}
}

func TestStreakKellyFactor(t *testing.T) {
	tests := []struct {
		name   string
		streak StreakResult
		want   float64
	}{
		{"empty", StreakResult{Count: 0}, 1.0},
		{"1 loss", StreakResult{Count: 1, IsWin: false}, 1.0},
		{"2 losses", StreakResult{Count: 2, IsWin: false}, 0.85},
		{"3 losses", StreakResult{Count: 3, IsWin: false}, 0.70},
		{"5 losses", StreakResult{Count: 5, IsWin: false}, 0.70},
		{"3 wins", StreakResult{Count: 3, IsWin: true}, 1.0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := StreakKellyFactor(tc.streak)
			if got != tc.want {
				t.Errorf("StreakKellyFactor(%+v) = %.2f, want %.2f", tc.streak, got, tc.want)
			}
		})
	}
}
