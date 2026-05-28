package calibration

import (
	"testing"
	"time"
)

func boolPtr236(b bool) *bool { return &b }

func makeSize236(size float64, won bool) BetRecord {
	return BetRecord{
		Timestamp:      time.Now().UTC(),
		OurProbability: 0.60,
		MarketPrice:    0.55,
		SizeUSDC:       size,
		Outcome:        boolPtr236(won),
		ResolvedAt:     time.Now().UTC(),
	}
}

func TestComputeSizeBuckets_Empty(t *testing.T) {
	buckets := ComputeSizeBuckets(nil)
	if len(buckets) != 4 {
		t.Fatalf("want 4 buckets, got %d", len(buckets))
	}
	for _, b := range buckets {
		if b.Count != 0 {
			t.Errorf("bucket %s: want Count=0, got %d", b.Label, b.Count)
		}
	}
}

func TestComputeSizeBuckets_CorrectBins(t *testing.T) {
	records := []BetRecord{
		makeSize236(0.50, true),  // <$1
		makeSize236(1.50, false), // $1–$2
		makeSize236(3.00, true),  // $2–$5
		makeSize236(7.00, false), // $5+
	}
	buckets := ComputeSizeBuckets(records)
	wantCounts := []int{1, 1, 1, 1}
	for i, want := range wantCounts {
		if buckets[i].Count != want {
			t.Errorf("bucket[%d] %s: want Count=%d, got %d", i, buckets[i].Label, want, buckets[i].Count)
		}
	}
}

func TestComputeSizeBuckets_PnL(t *testing.T) {
	// $2 bet at price 0.50, win → pnl = 2/0.5 - 2 = 2.0
	records := []BetRecord{
		makeSize236(2.0, true),
	}
	records[0].MarketPrice = 0.50
	buckets := ComputeSizeBuckets(records)
	b := buckets[2] // $2–$5
	if b.Count != 1 {
		t.Fatalf("want Count=1 in $2-$5 bucket")
	}
	wantPnL := 2.0/0.5 - 2.0
	if b.PnL < wantPnL-0.001 || b.PnL > wantPnL+0.001 {
		t.Errorf("PnL: want %.2f, got %.2f", wantPnL, b.PnL)
	}
}

func TestComputeSizeBuckets_Loss(t *testing.T) {
	records := []BetRecord{makeSize236(3.50, false)}
	buckets := ComputeSizeBuckets(records)
	b := buckets[2] // $2–$5
	if b.PnL != -3.50 {
		t.Errorf("loss PnL: want -3.50, got %.2f", b.PnL)
	}
}

func TestSizeValidation_Monotone(t *testing.T) {
	buckets := []SizeBucket{
		{Label: "<$1", Count: 5, Wins: 2},     // 40%
		{Label: "$1–$2", Count: 5, Wins: 3},   // 60%
		{Label: "$2–$5", Count: 5, Wins: 4},   // 80%
		{Label: "$5+", Count: 5, Wins: 5},     // 100%
	}
	_, ok := SizeValidation(buckets)
	if !ok {
		t.Error("monotone buckets should be valid")
	}
}

func TestSizeValidation_NotMonotone(t *testing.T) {
	buckets := []SizeBucket{
		{Label: "<$1", Count: 5, Wins: 5},    // 100%
		{Label: "$1–$2", Count: 5, Wins: 1},  // 20% — big drop
		{Label: "$2–$5", Count: 5, Wins: 4},
		{Label: "$5+", Count: 5, Wins: 5},
	}
	_, ok := SizeValidation(buckets)
	if ok {
		t.Error("non-monotone buckets should not be valid")
	}
}

func TestSizeValidation_NotEnoughData(t *testing.T) {
	buckets := []SizeBucket{
		{Label: "<$1", Count: 2, Wins: 1},
		{Label: "$1–$2", Count: 1, Wins: 0},
	}
	_, ok := SizeValidation(buckets)
	if ok {
		t.Error("fewer than 2 buckets with >=3 bets should return false")
	}
}
