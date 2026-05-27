package risk

import (
	"testing"

	"github.com/devher0/polymarket-weather-bot/internal/calibration"
)

// makeOpen builds a minimal unresolved BetRecord with a signal and size.
func makeOpen(signal string, size float64) calibration.BetRecord {
	return calibration.BetRecord{Signal: signal, SizeUSDC: size, Outcome: nil}
}

// makeResolved builds a resolved (won) BetRecord — should be excluded from checks.
func makeResolved(signal string, size float64) calibration.BetRecord {
	won := true
	return calibration.BetRecord{Signal: signal, SizeUSDC: size, Outcome: &won}
}

func TestCheckSignalConcentration_Disabled(t *testing.T) {
	mgr := New(Config{MaxSignalExposurePct: 0}) // disabled
	records := []calibration.BetRecord{makeOpen("rain", 50), makeOpen("rain", 50)}
	// Even 100% concentration should be allowed when disabled.
	if err := mgr.CheckSignalConcentration(records, "rain", 10); err != nil {
		t.Fatalf("expected nil, got %v", err)
	}
}

func TestCheckSignalConcentration_EmptySignal(t *testing.T) {
	mgr := New(Config{MaxSignalExposurePct: 0.40})
	if err := mgr.CheckSignalConcentration(nil, "", 10); err != nil {
		t.Fatalf("empty signal should always pass, got %v", err)
	}
}

func TestCheckSignalConcentration_NoHistory(t *testing.T) {
	mgr := New(Config{MaxSignalExposurePct: 0.40})
	// First bet: 100% of total exposure goes to "rain" but that's the only bet.
	// (10 / 10 = 100%) → exceeds 40%.
	err := mgr.CheckSignalConcentration(nil, "rain", 10)
	if err == nil {
		t.Fatal("expected concentration error for 100% rain with 40% limit")
	}
}

func TestCheckSignalConcentration_UnderLimit(t *testing.T) {
	mgr := New(Config{MaxSignalExposurePct: 0.40})
	records := []calibration.BetRecord{
		makeOpen("heat", 40),
		makeOpen("cold", 30),
		makeOpen("rain", 10), // 10/80 = 12.5%
	}
	// Adding 10 USDC rain → (20) / (90) = 22.2% — still under 40%.
	if err := mgr.CheckSignalConcentration(records, "rain", 10); err != nil {
		t.Fatalf("expected nil, got %v", err)
	}
}

func TestCheckSignalConcentration_ExactlyAtLimit(t *testing.T) {
	mgr := New(Config{MaxSignalExposurePct: 0.40})
	// 40 rain out of 100 total = exactly 40% — should pass (not strictly greater).
	records := []calibration.BetRecord{
		makeOpen("heat", 60),
		makeOpen("rain", 30),
	}
	// Adding 10 rain → 40 / 100 = 40.0% exactly — should pass.
	if err := mgr.CheckSignalConcentration(records, "rain", 10); err != nil {
		t.Fatalf("exactly at limit should pass, got %v", err)
	}
}

func TestCheckSignalConcentration_OverLimit(t *testing.T) {
	mgr := New(Config{MaxSignalExposurePct: 0.40})
	records := []calibration.BetRecord{
		makeOpen("heat", 60),
		makeOpen("rain", 31),
	}
	// Adding 10 rain → 41 / 101 = 40.6% — exceeds 40%.
	err := mgr.CheckSignalConcentration(records, "rain", 10)
	if err == nil {
		t.Fatal("expected concentration error, got nil")
	}
}

func TestCheckSignalConcentration_ResolvedExcluded(t *testing.T) {
	mgr := New(Config{MaxSignalExposurePct: 0.40})
	records := []calibration.BetRecord{
		makeResolved("rain", 200), // large resolved rain bet — must NOT count
		makeOpen("heat", 60),
	}
	// Only 60 USDC open (heat). Adding 10 rain → 10/70 = 14.3% — fine.
	if err := mgr.CheckSignalConcentration(records, "rain", 10); err != nil {
		t.Fatalf("resolved bets must be excluded, got %v", err)
	}
}

func TestSignalExposureBreakdown_Basic(t *testing.T) {
	records := []calibration.BetRecord{
		makeOpen("rain", 20),
		makeOpen("rain", 15),
		makeOpen("heat", 30),
		makeResolved("cold", 100), // excluded
	}
	bd := SignalExposureBreakdown(records)

	if bd["rain"] != 35 {
		t.Errorf("rain: want 35, got %.2f", bd["rain"])
	}
	if bd["heat"] != 30 {
		t.Errorf("heat: want 30, got %.2f", bd["heat"])
	}
	if _, ok := bd["cold"]; ok {
		t.Error("resolved cold bet should not appear in breakdown")
	}
}

func TestSignalConcentrationPct_Empty(t *testing.T) {
	pct := SignalConcentrationPct(nil, "rain")
	if pct != 0 {
		t.Errorf("empty records: want 0, got %.4f", pct)
	}
}

func TestSignalConcentrationPct_Values(t *testing.T) {
	records := []calibration.BetRecord{
		makeOpen("rain", 25),
		makeOpen("heat", 75),
	}
	pct := SignalConcentrationPct(records, "rain")
	// 25 / 100 = 0.25
	if pct < 0.249 || pct > 0.251 {
		t.Errorf("want ~0.25, got %.4f", pct)
	}
}
