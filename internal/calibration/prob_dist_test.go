package calibration

import (
	"math"
	"testing"
	"time"
)

func boolPtr235(b bool) *bool { return &b }

func makeRecord235(p, price, size float64, won bool) BetRecord {
	return BetRecord{
		Timestamp:      time.Now().UTC(),
		OurProbability: p,
		MarketPrice:    price,
		SizeUSDC:       size,
		Outcome:        boolPtr235(won),
		ResolvedAt:     time.Now().UTC(),
	}
}

func TestBuildProbDist_Empty(t *testing.T) {
	buckets := BuildProbDist(nil)
	if len(buckets) != 6 {
		t.Fatalf("want 6 buckets, got %d", len(buckets))
	}
	for _, b := range buckets {
		if b.Count != 0 {
			t.Errorf("bucket %s: want Count=0, got %d", b.Label, b.Count)
		}
	}
}

func TestBuildProbDist_BelowThreshold(t *testing.T) {
	records := []BetRecord{
		makeRecord235(0.30, 0.35, 1.0, true),
		makeRecord235(0.38, 0.40, 1.0, false),
	}
	buckets := BuildProbDist(records)
	total := 0
	for _, b := range buckets {
		total += b.Count
	}
	if total != 0 {
		t.Errorf("bets below 0.40 should be excluded, got total=%d", total)
	}
}

func TestBuildProbDist_CorrectBuckets(t *testing.T) {
	records := []BetRecord{
		makeRecord235(0.42, 0.40, 2.0, true),  // bucket 0: 0.40–0.50
		makeRecord235(0.52, 0.50, 1.0, false), // bucket 1: 0.50–0.55
		makeRecord235(0.57, 0.55, 1.0, true),  // bucket 2: 0.55–0.60
		makeRecord235(0.65, 0.60, 1.0, true),  // bucket 3: 0.60–0.70
		makeRecord235(0.75, 0.70, 1.0, false), // bucket 4: 0.70–0.80
		makeRecord235(0.85, 0.80, 1.0, true),  // bucket 5: 0.80–1.00
	}
	buckets := BuildProbDist(records)
	for i, want := range []int{1, 1, 1, 1, 1, 1} {
		if buckets[i].Count != want {
			t.Errorf("bucket[%d] Count: want %d, got %d", i, want, buckets[i].Count)
		}
	}
	// Bucket 0 has a win (42% prob, price 40%, size 2.0)
	if buckets[0].Wins != 1 {
		t.Errorf("bucket[0] Wins: want 1, got %d", buckets[0].Wins)
	}
	if buckets[1].Wins != 0 {
		t.Errorf("bucket[1] Wins: want 0, got %d", buckets[1].Wins)
	}
}

func TestBuildProbDist_AvgPred(t *testing.T) {
	records := []BetRecord{
		makeRecord235(0.62, 0.60, 1.0, true),
		makeRecord235(0.68, 0.65, 1.0, false),
	}
	buckets := BuildProbDist(records) // both in bucket 3: 0.60–0.70
	b := buckets[3]
	if b.Count != 2 {
		t.Fatalf("want Count=2, got %d", b.Count)
	}
	want := (0.62 + 0.68) / 2
	if math.Abs(b.AvgPred()-want) > 1e-9 {
		t.Errorf("AvgPred: want %.4f, got %.4f", want, b.AvgPred())
	}
}

func TestProbDistECE_PerfectCalibration(t *testing.T) {
	// Each bucket has exactly AvgPred ≈ WinRate → ECE ≈ 0
	// Bucket 3: 0.60–0.70, 10 bets, 6 wins (60% win rate), avg pred 0.60
	var records []BetRecord
	for i := 0; i < 6; i++ {
		records = append(records, makeRecord235(0.60, 0.55, 1.0, true))
	}
	for i := 0; i < 4; i++ {
		records = append(records, makeRecord235(0.60, 0.55, 1.0, false))
	}
	buckets := BuildProbDist(records)
	ece := ProbDistECE(buckets)
	if ece > 0.05 {
		t.Errorf("ECE for near-perfect calibration should be small, got %.4f", ece)
	}
}

func TestFormatProbDist_NoData(t *testing.T) {
	out := FormatProbDist(BuildProbDist(nil))
	if out == "" {
		t.Error("FormatProbDist should return non-empty string even with no data")
	}
}
