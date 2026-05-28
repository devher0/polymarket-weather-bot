package calibration

import (
	"testing"
	"time"
)

func boolPtr237(b bool) *bool { return &b }

func makeBestBet237(city, signal, side string, ourP, price, size float64, won bool) BetRecord {
	return BetRecord{
		ConditionID:    "test-" + city + "-" + signal,
		Timestamp:      time.Now().UTC(),
		City:           city,
		Signal:         signal,
		Side:           side,
		OurProbability: ourP,
		MarketPrice:    price,
		SizeUSDC:       size,
		Outcome:        boolPtr237(won),
		ResolvedAt:     time.Now().UTC(),
	}
}

func TestTopBottomBets_Empty(t *testing.T) {
	top, bottom := TopBottomBets(nil, 5)
	if len(top) != 0 || len(bottom) != 0 {
		t.Errorf("empty input should return empty slices")
	}
}

func TestTopBottomBets_BasicRanking(t *testing.T) {
	records := []BetRecord{
		makeBestBet237("nyc", "rain", "YES", 0.70, 0.50, 2.0, true),  // ROI = (2/0.5 - 2)/2*100 = 100%
		makeBestBet237("miami", "heat", "YES", 0.65, 0.60, 1.0, true),  // ROI = (1/0.6-1)/1*100 ≈ 67%
		makeBestBet237("chicago", "cold", "YES", 0.55, 0.50, 3.0, false), // ROI = -100%
	}
	top, bottom := TopBottomBets(records, 3)
	if len(top) == 0 {
		t.Fatal("expected non-empty top")
	}
	if top[0].City != "nyc" {
		t.Errorf("top[0] should be nyc (ROI 100%%), got %s", top[0].City)
	}
	if len(bottom) == 0 {
		t.Fatal("expected non-empty bottom")
	}
	if bottom[0].City != "chicago" {
		t.Errorf("bottom[0] should be chicago (loss), got %s", bottom[0].City)
	}
}

func TestTopBottomBets_FewerThanN(t *testing.T) {
	records := []BetRecord{
		makeBestBet237("nyc", "rain", "YES", 0.65, 0.55, 1.0, true),
	}
	top, bottom := TopBottomBets(records, 5)
	if len(top) != 1 {
		t.Errorf("want len(top)=1, got %d", len(top))
	}
	if len(bottom) != 1 {
		t.Errorf("want len(bottom)=1, got %d", len(bottom))
	}
}

func TestTopBottomBets_UnresolvedIgnored(t *testing.T) {
	records := []BetRecord{
		makeBestBet237("nyc", "rain", "YES", 0.65, 0.55, 1.0, true),
		{City: "miami", Signal: "heat", Outcome: nil}, // unresolved
	}
	top, _ := TopBottomBets(records, 5)
	if len(top) != 1 {
		t.Errorf("unresolved bets should be excluded, want 1 top bet, got %d", len(top))
	}
}

func TestFormatBestBets_NoData(t *testing.T) {
	out := FormatBestBets(nil, 5)
	if out == "" {
		t.Error("should return non-empty string even with no data")
	}
}

func TestBetSummaryROI_Win(t *testing.T) {
	// $2 at price 0.50 win → pnl = 2.0, ROI = 100%
	r := makeBestBet237("nyc", "rain", "YES", 0.70, 0.50, 2.0, true)
	summaries := computeBetSummaries([]BetRecord{r})
	if len(summaries) != 1 {
		t.Fatal("expect 1 summary")
	}
	if summaries[0].ROIPct < 99 || summaries[0].ROIPct > 101 {
		t.Errorf("ROI should be ~100%%, got %.1f", summaries[0].ROIPct)
	}
}

func TestBetSummaryROI_Loss(t *testing.T) {
	r := makeBestBet237("nyc", "rain", "YES", 0.70, 0.50, 2.0, false)
	summaries := computeBetSummaries([]BetRecord{r})
	if summaries[0].ROIPct != -100 {
		t.Errorf("loss ROI should be -100%%, got %.1f", summaries[0].ROIPct)
	}
}
