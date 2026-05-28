// weekly_test.go — unit tests for WeeklyBreakdown, BestWeek, WorstWeek.
// TASK-166
package calibration

import (
	"testing"
	"time"
)

// weeklyRec creates a minimal BetRecord resolved on the given date.
func weeklyRec(resolvedDate time.Time, outcome bool, sizeUSDC, marketPrice, ourP float64) BetRecord {
	o := outcome
	return BetRecord{
		ResolvedAt:     resolvedDate,
		Timestamp:      resolvedDate,
		Outcome:        &o,
		SizeUSDC:       sizeUSDC,
		MarketPrice:    marketPrice,
		OurProbability: ourP,
	}
}

func TestWeeklyBreakdown_Empty(t *testing.T) {
	got := WeeklyBreakdown(nil, 4)
	if len(got) != 4 {
		t.Fatalf("expected 4 weeks, got %d", len(got))
	}
	for _, w := range got {
		if w.Bets != 0 || w.PnLUSDC != 0 {
			t.Errorf("expected empty week, got %+v", w)
		}
	}
}

func TestWeeklyBreakdown_SingleBet(t *testing.T) {
	now := time.Now().UTC()
	ws := weekStart(now)
	rec := weeklyRec(ws.Add(time.Hour), true, 5.0, 0.50, 0.70)

	got := WeeklyBreakdown([]BetRecord{rec}, 2)
	if len(got) != 2 {
		t.Fatalf("want 2 weeks, got %d", len(got))
	}
	thisWeek := got[1] // last entry = current week
	if thisWeek.Bets != 1 {
		t.Errorf("bets: want 1, got %d", thisWeek.Bets)
	}
	if thisWeek.Wins != 1 {
		t.Errorf("wins: want 1, got %d", thisWeek.Wins)
	}
	expectedPnL := 5.0/0.50 - 5.0 // = 5.0
	if thisWeek.PnLUSDC < expectedPnL-0.001 || thisWeek.PnLUSDC > expectedPnL+0.001 {
		t.Errorf("pnl: want %.2f, got %.2f", expectedPnL, thisWeek.PnLUSDC)
	}
}

func TestWeeklyBreakdown_CrossWeekBoundary(t *testing.T) {
	now := time.Now().UTC()
	thisWS := weekStart(now)
	lastWS := thisWS.AddDate(0, 0, -7)

	recs := []BetRecord{
		weeklyRec(lastWS.Add(time.Hour), false, 3.0, 0.60, 0.40), // loss last week
		weeklyRec(thisWS.Add(time.Hour), true, 2.0, 0.40, 0.70),  // win this week
	}

	got := WeeklyBreakdown(recs, 2)
	if len(got) != 2 {
		t.Fatalf("want 2 weeks, got %d", len(got))
	}
	if got[0].Bets != 1 || got[0].Wins != 0 {
		t.Errorf("last week: want 1 bet, 0 wins; got %+v", got[0])
	}
	if got[1].Bets != 1 || got[1].Wins != 1 {
		t.Errorf("this week: want 1 bet, 1 win; got %+v", got[1])
	}
}

func TestBestWorstWeek(t *testing.T) {
	make3 := func(pnl float64) WeeklyStats {
		return WeeklyStats{PnLUSDC: pnl, Bets: 1}
	}
	stats := []WeeklyStats{make3(-5), make3(10), make3(3)}

	best := BestWeek(stats)
	if best.PnLUSDC != 10 {
		t.Errorf("best: want 10, got %.2f", best.PnLUSDC)
	}

	worst := WorstWeek(stats)
	if worst.PnLUSDC != -5 {
		t.Errorf("worst: want -5, got %.2f", worst.PnLUSDC)
	}
}

func TestBestWorstWeek_Empty(t *testing.T) {
	if got := BestWeek(nil); got.Bets != 0 {
		t.Errorf("BestWeek(nil) should return zero value")
	}
	if got := WorstWeek(nil); got.Bets != 0 {
		t.Errorf("WorstWeek(nil) should return zero value")
	}
}
