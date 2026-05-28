package markets

import (
	"testing"
	"time"
)

func makeMarket(condID, city, signal, endDate string, expiry time.Time) Market {
	return Market{
		ConditionID: condID,
		City:        city,
		Signal:      signal,
		EndDate:     endDate,
		ExpiryUTC:   expiry,
	}
}

func TestMarketFingerprint(t *testing.T) {
	expiry := time.Date(2026, 7, 4, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name string
		m    Market
		want string
	}{
		{
			name: "basic fingerprint",
			m:    makeMarket("c1", "new_york", "heat", "", expiry),
			want: "new_york/heat/2026-07-04",
		},
		{
			name: "city normalization — spaces",
			m:    makeMarket("c2", "New York", "rain", "", expiry),
			want: "new_york/rain/2026-07-04",
		},
		{
			name: "city normalization — dashes",
			m:    makeMarket("c3", "new-york", "heat", "", expiry),
			want: "new_york/heat/2026-07-04",
		},
		{
			name: "fallback to EndDate when ExpiryUTC is zero",
			m:    makeMarket("c4", "miami", "heat", "2026-08-01T00:00:00Z", time.Time{}),
			want: "miami/heat/2026-08-01",
		},
		{
			name: "zero expiry and no EndDate → unknown",
			m:    makeMarket("c5", "los_angeles", "rain", "", time.Time{}),
			want: "los_angeles/rain/unknown",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := MarketFingerprint(tc.m)
			if got != tc.want {
				t.Errorf("MarketFingerprint = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestFindDuplicates_NoDuplicates(t *testing.T) {
	expiry1 := time.Date(2026, 7, 4, 12, 0, 0, 0, time.UTC)
	expiry2 := time.Date(2026, 7, 5, 12, 0, 0, 0, time.UTC)
	mkts := []Market{
		makeMarket("c1", "new_york", "heat", "", expiry1),
		makeMarket("c2", "miami", "rain", "", expiry2),
	}
	dupes := FindDuplicates(mkts)
	if len(dupes) != 0 {
		t.Errorf("expected no duplicates, got %v", dupes)
	}
}

func TestFindDuplicates_ExactDuplicate(t *testing.T) {
	expiry := time.Date(2026, 7, 4, 12, 0, 0, 0, time.UTC)
	mkts := []Market{
		makeMarket("c1", "new_york", "heat", "", expiry),
		makeMarket("c2", "New York", "heat", "", expiry), // same event, different title
	}
	dupes := FindDuplicates(mkts)
	if len(dupes) != 1 {
		t.Fatalf("expected 1 duplicate fingerprint, got %d: %v", len(dupes), dupes)
	}
	fp := "new_york/heat/2026-07-04"
	ids, ok := dupes[fp]
	if !ok {
		t.Errorf("expected fingerprint %q in duplicates map", fp)
	}
	if len(ids) != 2 {
		t.Errorf("expected 2 condition IDs under fingerprint, got %d", len(ids))
	}
}

func TestFindDuplicates_DifferentDate(t *testing.T) {
	expiry1 := time.Date(2026, 7, 4, 12, 0, 0, 0, time.UTC)
	expiry2 := time.Date(2026, 7, 5, 12, 0, 0, 0, time.UTC)
	mkts := []Market{
		makeMarket("c1", "new_york", "heat", "", expiry1),
		makeMarket("c2", "new_york", "heat", "", expiry2), // different day → not duplicate
	}
	dupes := FindDuplicates(mkts)
	if len(dupes) != 0 {
		t.Errorf("different-date markets should not be duplicates, got %v", dupes)
	}
}

func TestFindDuplicates_DifferentSignal(t *testing.T) {
	expiry := time.Date(2026, 7, 4, 12, 0, 0, 0, time.UTC)
	mkts := []Market{
		makeMarket("c1", "new_york", "heat", "", expiry),
		makeMarket("c2", "new_york", "rain", "", expiry), // different signal → not duplicate
	}
	dupes := FindDuplicates(mkts)
	if len(dupes) != 0 {
		t.Errorf("different-signal markets should not be duplicates, got %v", dupes)
	}
}

func TestIsDuplicateOf_NoBets(t *testing.T) {
	m := makeMarket("c1", "new_york", "heat", "", time.Now().UTC().Add(24*time.Hour))
	if IsDuplicateOf(m, nil) {
		t.Error("expected false when no open bets exist")
	}
}

func TestIsDuplicateOf_OpenBetsCheck(t *testing.T) {
	now := time.Now().UTC()

	open := OpenBetInfo{
		City:     "new_york",
		Signal:   "heat",
		PlacedAt: now.Add(-2 * time.Hour),
		Resolved: false,
	}
	resolved := OpenBetInfo{
		City:     "new_york",
		Signal:   "heat",
		PlacedAt: now.Add(-2 * time.Hour),
		Resolved: true,
	}

	m := makeMarket("c1", "new_york", "heat", "", now.Add(24*time.Hour))

	// Open bet on same city/signal → duplicate.
	if !IsDuplicateOf(m, []OpenBetInfo{open}) {
		t.Error("expected duplicate detected for open bet on same city/signal")
	}

	// Resolved bet → not duplicate.
	if IsDuplicateOf(m, []OpenBetInfo{resolved}) {
		t.Error("expected no duplicate when only resolved bet exists")
	}

	// Different city → no duplicate.
	mMiami := makeMarket("c2", "miami", "heat", "", now.Add(24*time.Hour))
	if IsDuplicateOf(mMiami, []OpenBetInfo{open}) {
		t.Error("expected no duplicate for different city")
	}
}
