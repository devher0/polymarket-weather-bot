// duplicate_guard.go — prevents double-betting on the same weather event (TASK-135).
//
// Polymarket occasionally lists multiple markets for the same underlying event
// (e.g. "Will NYC hit 90°F on July 4?" and "Will New York reach 90 degrees on
// the 4th of July?"). Betting on both creates double exposure with no additional
// edge.  This guard detects and skips duplicate markets using a canonical
// fingerprint derived from city, signal, and expiry date.
package markets

import (
	"strings"
	"time"
)

// MarketFingerprint returns a canonical key for a market that captures the
// underlying weather event: normalize(city) + "/" + signal + "/" + date(expiry).
//
// Example: "new_york/heat/2026-07-04"
//
// When ExpiryUTC is zero the EndDate string is used as-is (already a date).
// The city is lower-cased and spaces/dashes normalised to underscores so that
// "New York", "new-york", and "new_york" all produce the same token.
func MarketFingerprint(m Market) string {
	city := normalizeCity(m.City)
	signal := strings.ToLower(strings.TrimSpace(m.Signal))

	var dateStr string
	if !m.ExpiryUTC.IsZero() {
		dateStr = m.ExpiryUTC.UTC().Format("2006-01-02")
	} else if m.EndDate != "" {
		// EndDate is already a date string (may include time); take prefix only.
		if len(m.EndDate) >= 10 {
			dateStr = m.EndDate[:10]
		} else {
			dateStr = m.EndDate
		}
	} else {
		dateStr = "unknown"
	}

	return city + "/" + signal + "/" + dateStr
}

// normalizeCity lower-cases the city string and replaces spaces and hyphens
// with underscores so that "New York", "new-york", and "new_york" are equal.
func normalizeCity(city string) string {
	city = strings.ToLower(strings.TrimSpace(city))
	city = strings.ReplaceAll(city, " ", "_")
	city = strings.ReplaceAll(city, "-", "_")
	return city
}

// FindDuplicates groups markets by fingerprint and returns a map of
// fingerprint → []conditionID for any fingerprint that has two or more
// markets (i.e. the actual duplicates).
func FindDuplicates(mkts []Market) map[string][]string {
	byFingerprint := make(map[string][]string, len(mkts))
	for _, m := range mkts {
		fp := MarketFingerprint(m)
		byFingerprint[fp] = append(byFingerprint[fp], m.ConditionID)
	}

	dupes := make(map[string][]string)
	for fp, ids := range byFingerprint {
		if len(ids) >= 2 {
			dupes[fp] = ids
		}
	}
	return dupes
}

// OpenBetInfo is a lightweight summary of an open (unresolved) bet used by
// IsDuplicateOf.  Callers populate this from their calibration.BetRecord slice
// to avoid an import-cycle between the markets and calibration packages.
type OpenBetInfo struct {
	City      string
	Signal    string
	PlacedAt  time.Time
	Resolved  bool // true when the bet has already been resolved
}

// IsDuplicateOf returns true when there is already an open (unresolved) bet
// in openBets on the same city+signal as m AND the bet was placed within the
// last 14 days (to avoid spurious matches from old, forgotten positions).
//
// NOTE: BetRecord does not store the market expiry date, so deduplication
// against open bets uses city+signal matching.  Expiry-date deduplication
// across the live market list is available via FindDuplicates.
func IsDuplicateOf(m Market, openBets []OpenBetInfo) bool {
	city := normalizeCity(m.City)
	signal := strings.ToLower(strings.TrimSpace(m.Signal))
	cutoff := time.Now().UTC().AddDate(0, 0, -14)

	for _, b := range openBets {
		if b.Resolved {
			continue
		}
		if b.PlacedAt.Before(cutoff) {
			continue // too old to be the same event window
		}
		if normalizeCity(b.City) == city && strings.ToLower(b.Signal) == signal {
			return true
		}
	}
	return false
}
