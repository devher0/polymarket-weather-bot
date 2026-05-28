// pnl_city_test.go — unit tests for CityPnL and SignalPnL.
package calibration

import (
	"testing"
)

func TestCityPnL_Empty(t *testing.T) {
	stats := CityPnL(nil)
	if len(stats) != 0 {
		t.Fatalf("expected empty, got %d entries", len(stats))
	}
}

func TestCityPnL_SkipsUnresolved(t *testing.T) {
	records := []BetRecord{
		{City: "tokyo", SizeUSDC: 1.0, MarketPrice: 0.5, Outcome: nil},
	}
	stats := CityPnL(records)
	if len(stats) != 0 {
		t.Fatalf("expected 0, got %d", len(stats))
	}
}

func TestCityPnL_SkipsEmptyCity(t *testing.T) {
	won := true
	records := []BetRecord{
		{City: "", SizeUSDC: 2.0, MarketPrice: 0.5, Outcome: &won},
	}
	stats := CityPnL(records)
	if len(stats) != 0 {
		t.Fatalf("expected 0 (no city), got %d", len(stats))
	}
}

func TestCityPnL_WinLoss(t *testing.T) {
	won := true
	lost := false
	records := []BetRecord{
		// new_york: 1 win at price 0.5 → profit = 2.0/0.5 - 2.0 = 2.0
		{City: "new_york", SizeUSDC: 2.0, MarketPrice: 0.5, Outcome: &won},
		// new_york: 1 loss → -1.0
		{City: "new_york", SizeUSDC: 1.0, MarketPrice: 0.6, Outcome: &lost},
		// london: 1 win at price 0.4 → profit = 1.0/0.4 - 1.0 = 1.5
		{City: "london", SizeUSDC: 1.0, MarketPrice: 0.4, Outcome: &won},
	}

	stats := CityPnL(records)
	if len(stats) != 2 {
		t.Fatalf("expected 2 cities, got %d", len(stats))
	}

	// Sorted by PnL desc: new_york (+1.0) first, london (+1.5) wait...
	// new_york: 2.0/0.5 - 2.0 - 1.0 = 4.0 - 2.0 - 1.0 = 1.0
	// london: 1.0/0.4 - 1.0 = 2.5 - 1.0 = 1.5 → london first
	if stats[0].City != "london" {
		t.Errorf("expected london first (higher PnL), got %s", stats[0].City)
	}

	var ny CityPnLStats
	for _, s := range stats {
		if s.City == "new_york" {
			ny = s
		}
	}
	if ny.Bets != 2 {
		t.Errorf("new_york: expected 2 bets, got %d", ny.Bets)
	}
	if ny.Wins != 1 {
		t.Errorf("new_york: expected 1 win, got %d", ny.Wins)
	}
	const epsilon = 0.001
	expected := 2.0/0.5 - 2.0 - 1.0
	if ny.PnLUSDC < expected-epsilon || ny.PnLUSDC > expected+epsilon {
		t.Errorf("new_york PnL: expected %.3f, got %.3f", expected, ny.PnLUSDC)
	}
}

func TestCityPnL_WinRateROI(t *testing.T) {
	won := true
	lost := false
	records := []BetRecord{
		{City: "paris", SizeUSDC: 2.0, MarketPrice: 0.5, Outcome: &won},
		{City: "paris", SizeUSDC: 2.0, MarketPrice: 0.5, Outcome: &lost},
	}
	stats := CityPnL(records)
	if len(stats) != 1 {
		t.Fatalf("expected 1, got %d", len(stats))
	}
	s := stats[0]
	if s.WinRate() != 50.0 {
		t.Errorf("expected 50%% win rate, got %.1f", s.WinRate())
	}
	// PnL = (2/0.5 - 2) - 2 = 2.0 - 2.0 = 0
	// ROI = 0/4.0 = 0%
	if s.ROI() != 0.0 {
		t.Errorf("expected 0%% ROI, got %.2f", s.ROI())
	}
}

func TestSignalPnL_Basic(t *testing.T) {
	won := true
	records := []BetRecord{
		{Signal: "rain", SizeUSDC: 1.0, MarketPrice: 0.5, Outcome: &won},
		{Signal: "heat", SizeUSDC: 2.0, MarketPrice: 0.5, Outcome: boolPtr(false)},
		{Signal: "rain", SizeUSDC: 1.0, MarketPrice: 0.5, Outcome: &won},
	}
	stats := SignalPnL(records)
	if len(stats) != 2 {
		t.Fatalf("expected 2 signals, got %d", len(stats))
	}
	// rain: 2 wins, pnl = 2*(1/0.5-1) = 2.0; heat: 1 loss = -2.0
	// sorted by pnl desc → rain first
	if stats[0].City != "rain" {
		t.Errorf("expected rain first, got %s", stats[0].City)
	}
}
