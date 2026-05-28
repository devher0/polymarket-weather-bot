package calibration

import (
	"testing"
	"time"
)

func burnoutBoolPtr(b bool) *bool { return &b }

func makeRec(sig string, hoursAgo float64, won bool, resolved bool) BetRecord {
	ts := time.Now().Add(-time.Duration(hoursAgo*float64(time.Hour)))
	r := BetRecord{
		Signal:         sig,
		Timestamp:      ts,
		SizeUSDC:       1.0,
		OurProbability: 0.6,
		MarketPrice:    0.5,
	}
	if resolved {
		r.Outcome = burnoutBoolPtr(won)
		r.ResolvedAt = ts.Add(time.Hour)
	}
	return r
}

// TestBurnout_Empty — no records → no results.
func TestBurnout_Empty(t *testing.T) {
	report := AnalyzeBurnout(nil, DefaultBurnoutConfig())
	if len(report.Results) != 0 {
		t.Errorf("expected 0 results, got %d", len(report.Results))
	}
	if report.HasAlerts() {
		t.Error("expected no alerts on empty input")
	}
}

// TestBurnout_NoAlert — low frequency, good win rate → no alert.
func TestBurnout_NoAlert(t *testing.T) {
	var records []BetRecord
	for i := 0; i < 3; i++ {
		records = append(records, makeRec("rain", float64(i*24), true, true))
	}
	report := AnalyzeBurnout(records, DefaultBurnoutConfig())
	if report.HasAlerts() {
		t.Errorf("expected no alerts, got alerts: %+v", report.Results)
	}
}

// TestBurnout_Overloaded — high frequency + poor win rate → overloaded alert.
func TestBurnout_Overloaded(t *testing.T) {
	var records []BetRecord
	// rain: 12 resolved bets in 14d window with 16% win rate → overloaded
	for i := 0; i < 12; i++ {
		won := i < 2 // only 2 wins out of 12
		records = append(records, makeRec("rain", float64(i*24+1), won, true))
	}
	// heat: 1 resolved bet → global avg = (12+1)/2 = 6.5; rain freq = 12/6.5 = 1.85
	// Use only one other signal with 1 bet → avg = (12+1)/2 = 6.5; rain = 1.85 (below 2x)
	// Actually need avg to be low. Use 1 bet for heat:
	// avg = (12+1)/2 = 6.5. rain freqRatio = 12/6.5 = 1.85. Still below 2.0.
	// Better: use cfg.FreqMultiplier = 1.5
	records = append(records, makeRec("heat", 300, true, true))

	cfg := DefaultBurnoutConfig()
	cfg.MinBets = 4
	cfg.FreqMultiplier = 1.5 // lower threshold for test clarity
	report := AnalyzeBurnout(records, cfg)

	var found bool
	for _, r := range report.Results {
		if r.Signal == "rain" && r.Overloaded {
			found = true
		}
	}
	if !found {
		t.Errorf("expected rain to be overloaded, results: %+v", report.Results)
	}
}

// TestBurnout_Burst — many recent bets regardless of win rate → burst alert.
func TestBurnout_Burst(t *testing.T) {
	var records []BetRecord
	// 6 bets in last 47 hours (within burst window of 48h)
	for i := 0; i < 6; i++ {
		records = append(records, makeRec("wind", float64(i*7), true, false))
	}

	cfg := DefaultBurnoutConfig()
	cfg.BurstWindow = 48
	cfg.BurstLimit = 5
	report := AnalyzeBurnout(records, cfg)

	var found bool
	for _, r := range report.Results {
		if r.Signal == "wind" && r.Bursting {
			found = true
		}
	}
	if !found {
		t.Errorf("expected wind to be bursting, results: %+v", report.Results)
	}
}

// TestBurnout_HighFreqGoodRate — high frequency but good win rate → no overload.
func TestBurnout_HighFreqGoodRate(t *testing.T) {
	var records []BetRecord
	// rain: 12 resolved bets, 80% win rate → no overload (good performance)
	for i := 0; i < 12; i++ {
		won := i < 10
		records = append(records, makeRec("rain", float64(i*24+1), won, true))
	}
	// heat: 2 bets → baseline
	records = append(records, makeRec("heat", 100, true, true))
	records = append(records, makeRec("heat", 200, true, true))

	report := AnalyzeBurnout(records, DefaultBurnoutConfig())

	for _, r := range report.Results {
		if r.Signal == "rain" && r.Overloaded {
			t.Error("rain should NOT be overloaded when win rate is high")
		}
	}
}

// TestBurnout_UnresolvedIgnoredForWinRate — unresolved bets don't affect win rate calc.
func TestBurnout_UnresolvedIgnoredForWinRate(t *testing.T) {
	var records []BetRecord
	// 3 resolved losses for rain
	for i := 0; i < 3; i++ {
		records = append(records, makeRec("rain", float64(i*24+1), false, true))
	}
	// 5 unresolved rain bets → should not make it "MinBets" for win rate
	for i := 0; i < 5; i++ {
		records = append(records, makeRec("rain", float64(i*5+1), false, false))
	}

	cfg := DefaultBurnoutConfig()
	cfg.MinBets = 4 // need 4 resolved bets for win-rate analysis
	report := AnalyzeBurnout(records, cfg)

	for _, r := range report.Results {
		if r.Signal == "rain" {
			if r.WinRate != -1 {
				t.Errorf("expected WinRate=-1 (below MinBets), got %f", r.WinRate)
			}
		}
	}
}

// TestFormatBurnout_NoAlerts — format with no alerts shows green checkmark.
func TestFormatBurnout_NoAlerts(t *testing.T) {
	report := BurnoutReport{
		Results:    []BurnoutResult{{Signal: "rain", BetsWindow: 2, WinRate: 0.6}},
		GlobalAvg:  2.0,
		WindowDays: 14,
	}
	out := FormatBurnout(report)
	if out == "" {
		t.Error("expected non-empty output")
	}
	if len(out) < 20 {
		t.Errorf("output too short: %q", out)
	}
}
