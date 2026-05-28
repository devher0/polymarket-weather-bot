package calibration

import (
	"testing"
)


func TestComputeEdgeBuckets_Empty(t *testing.T) {
	buckets := ComputeEdgeBuckets(nil)
	if len(buckets) != 4 {
		t.Fatalf("want 4 buckets, got %d", len(buckets))
	}
	for _, b := range buckets {
		if b.Count != 0 {
			t.Errorf("bucket %s: want Count=0, got %d", b.Label, b.Count)
		}
	}
}

func TestComputeEdgeBuckets_DistinctBuckets(t *testing.T) {
	records := []BetRecord{
		// <5% edge
		{OurProbability: 0.54, MarketPrice: 0.51, SizeUSDC: 1.0, Outcome: boolPtr(true)},
		// 5-10% edge
		{OurProbability: 0.60, MarketPrice: 0.53, SizeUSDC: 2.0, Outcome: boolPtr(false)},
		// 10-15% edge
		{OurProbability: 0.72, MarketPrice: 0.60, SizeUSDC: 3.0, Outcome: boolPtr(true)},
		// >15% edge
		{OurProbability: 0.80, MarketPrice: 0.60, SizeUSDC: 4.0, Outcome: boolPtr(true)},
	}

	buckets := ComputeEdgeBuckets(records)
	if len(buckets) != 4 {
		t.Fatalf("want 4 buckets, got %d", len(buckets))
	}

	if buckets[0].Count != 1 {
		t.Errorf("bucket <5%%: want Count=1, got %d", buckets[0].Count)
	}
	if buckets[1].Count != 1 {
		t.Errorf("bucket 5-10%%: want Count=1, got %d", buckets[1].Count)
	}
	if buckets[2].Count != 1 {
		t.Errorf("bucket 10-15%%: want Count=1, got %d", buckets[2].Count)
	}
	if buckets[3].Count != 1 {
		t.Errorf("bucket >15%%: want Count=1, got %d", buckets[3].Count)
	}
}

func TestComputeEdgeBuckets_SkipsUnresolved(t *testing.T) {
	records := []BetRecord{
		{OurProbability: 0.70, MarketPrice: 0.50, SizeUSDC: 5.0, Outcome: nil}, // unresolved
		{OurProbability: 0.70, MarketPrice: 0.50, SizeUSDC: 5.0, Outcome: boolPtr(true)},
	}

	buckets := ComputeEdgeBuckets(records)
	total := 0
	for _, b := range buckets {
		total += b.Count
	}
	if total != 1 {
		t.Errorf("want 1 resolved bet counted, got %d", total)
	}
}

func TestComputeEdgeBuckets_AllInOneBucket(t *testing.T) {
	var records []BetRecord
	// All with edge ~12% → bucket 10-15%
	for i := 0; i < 5; i++ {
		records = append(records, BetRecord{
			OurProbability: 0.72,
			MarketPrice:    0.60,
			SizeUSDC:       1.0,
			Outcome:        boolPtr(i%2 == 0),
		})
	}

	buckets := ComputeEdgeBuckets(records)
	if buckets[2].Count != 5 {
		t.Errorf("bucket 10-15%%: want Count=5, got %d", buckets[2].Count)
	}
	for i, b := range buckets {
		if i != 2 && b.Count != 0 {
			t.Errorf("bucket %s: want Count=0, got %d", b.Label, b.Count)
		}
	}
}

func TestEdgeValidation_NotEnoughData(t *testing.T) {
	buckets := ComputeEdgeBuckets(nil)
	msg, ok := EdgeValidation(buckets)
	if ok {
		t.Error("want validated=false for empty data")
	}
	if msg == "" {
		t.Error("want non-empty message")
	}
}
