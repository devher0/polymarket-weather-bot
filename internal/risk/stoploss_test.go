package risk

import (
	"testing"

	"github.com/devher0/polymarket-weather-bot/internal/calibration"
)

func boolPtr(b bool) *bool { return &b }

func openRec(condID, side string, entry float64) calibration.BetRecord {
	return calibration.BetRecord{
		ConditionID: condID,
		Side:        side,
		MarketPrice: entry,
		SizeUSDC:    10.0,
	}
}

func resolvedRec(condID, side string, entry float64, won bool) calibration.BetRecord {
	r := openRec(condID, side, entry)
	r.Outcome = boolPtr(won)
	return r
}

// TestCheckStopLoss_Triggered verifies that a 60% loss on a 50% threshold fires.
func TestCheckStopLoss_Triggered(t *testing.T) {
	rec := openRec("0xabc", "YES", 0.70)
	cfg := StopLossConfig{Enabled: true, MaxLossPct: 0.50}

	// currentPrice = 0.20 → loss = (0.70 - 0.20) / 0.70 ≈ 0.714 (71.4% loss)
	res, fired := CheckStopLoss(rec, 0.20, cfg)
	if !fired {
		t.Fatalf("expected stop-loss to trigger, got false; loss_fraction=%.3f", res.LossFraction)
	}
	if !res.Triggered {
		t.Error("result.Triggered should be true")
	}
}

// TestCheckStopLoss_NotTriggered verifies a small loss does not fire.
func TestCheckStopLoss_NotTriggered(t *testing.T) {
	rec := openRec("0xabc", "YES", 0.60)
	cfg := StopLossConfig{Enabled: true, MaxLossPct: 0.50}

	// currentPrice = 0.50 → loss = (0.60 - 0.50) / 0.60 ≈ 0.167 (16.7% loss)
	res, fired := CheckStopLoss(rec, 0.50, cfg)
	if fired {
		t.Fatalf("expected no trigger, got fired=true; loss_fraction=%.3f", res.LossFraction)
	}
	if res.Triggered {
		t.Error("result.Triggered should be false")
	}
}

// TestCheckStopLoss_Disabled verifies that disabled config never triggers.
func TestCheckStopLoss_Disabled(t *testing.T) {
	rec := openRec("0xabc", "YES", 0.80)
	cfg := StopLossConfig{Enabled: false, MaxLossPct: 0.50}

	_, fired := CheckStopLoss(rec, 0.01, cfg)
	if fired {
		t.Error("stop-loss should not fire when disabled")
	}
}

// TestCheckStopLoss_ExactlyAtThreshold verifies that a loss exactly at MaxLossPct triggers.
func TestCheckStopLoss_ExactlyAtThreshold(t *testing.T) {
	rec := openRec("0xabc", "YES", 0.80)
	cfg := StopLossConfig{Enabled: true, MaxLossPct: 0.50}

	// currentPrice = 0.40 → loss = (0.80 - 0.40) / 0.80 = 0.50 (exactly 50%)
	_, fired := CheckStopLoss(rec, 0.40, cfg)
	if !fired {
		t.Error("stop-loss should fire when loss equals threshold exactly")
	}
}

// TestCheckStopLoss_AlreadyResolved verifies resolved positions are skipped.
func TestCheckStopLoss_AlreadyResolved(t *testing.T) {
	rec := resolvedRec("0xabc", "YES", 0.80, false)
	cfg := StopLossConfig{Enabled: true, MaxLossPct: 0.50}

	_, fired := CheckStopLoss(rec, 0.01, cfg)
	if fired {
		t.Error("should not trigger stop-loss on already-resolved position")
	}
}

// TestScanStopLosses_MultiplePositions verifies ScanStopLosses returns only triggered positions.
func TestScanStopLosses_MultiplePositions(t *testing.T) {
	cfg := StopLossConfig{Enabled: true, MaxLossPct: 0.50}

	positions := []calibration.UnrealizedPosition{
		// 75% loss — should trigger
		{BetRecord: openRec("0x001", "YES", 0.80), CurrentPrice: 0.20},
		// 10% loss — should not trigger
		{BetRecord: openRec("0x002", "NO", 0.60), CurrentPrice: 0.54},
		// fetch error — skip
		{BetRecord: openRec("0x003", "YES", 0.70), FetchError: "timeout"},
	}

	results := ScanStopLosses(positions, cfg)
	if len(results) != 1 {
		t.Fatalf("expected 1 triggered, got %d", len(results))
	}
	if results[0].ConditionID != "0x001" {
		t.Errorf("expected triggered on 0x001, got %s", results[0].ConditionID)
	}
}

// TestScanStopLosses_Disabled returns nil when disabled.
func TestScanStopLosses_Disabled(t *testing.T) {
	cfg := StopLossConfig{Enabled: false, MaxLossPct: 0.50}

	positions := []calibration.UnrealizedPosition{
		{BetRecord: openRec("0x001", "YES", 0.80), CurrentPrice: 0.01},
	}

	results := ScanStopLosses(positions, cfg)
	if results != nil {
		t.Errorf("expected nil results when disabled, got %v", results)
	}
}
