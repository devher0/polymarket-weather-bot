// blacklist.go — market loss blacklist.
//
// When a bet resolves as a loss, the condition ID is added to the blacklist
// for a configurable number of days (default 5). Blacklisted markets are
// skipped during evaluation to avoid re-entering positions where we've
// recently been wrong.
//
// Persistence: data/blacklist.json (JSON array of BlacklistEntry objects).
// Expired entries are pruned on every load.
package markets

import (
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"time"
)

// BlacklistEntry marks a condition ID as off-limits for a cooldown period
// after a loss on that market.
type BlacklistEntry struct {
	ConditionID string    `json:"condition_id"`
	City        string    `json:"city"`
	Signal      string    `json:"signal"`
	LostAt      time.Time `json:"lost_at"`
	ExpiresAt   time.Time `json:"expires_at"`
}

const defaultBlacklistDays = 5

func blacklistPath(dataRoot string) string {
	if dataRoot == "" {
		dataRoot = "."
	}
	return filepath.Join(dataRoot, "blacklist.json")
}

// LoadBlacklist reads the persisted blacklist from disk, pruning expired entries.
// Returns a nil slice (not an error) when the file does not exist yet.
func LoadBlacklist(dataRoot string) []BlacklistEntry {
	data, err := os.ReadFile(blacklistPath(dataRoot))
	if err != nil {
		return nil
	}
	var entries []BlacklistEntry
	if err := json.Unmarshal(data, &entries); err != nil {
		slog.Warn("blacklist: failed to parse", "err", err)
		return nil
	}
	return PurgeExpired(entries)
}

// PurgeExpired returns a new slice with all expired entries removed.
func PurgeExpired(entries []BlacklistEntry) []BlacklistEntry {
	now := time.Now().UTC()
	out := make([]BlacklistEntry, 0, len(entries))
	for _, e := range entries {
		if e.ExpiresAt.After(now) {
			out = append(out, e)
		}
	}
	return out
}

// IsBlacklisted reports whether conditionID appears in the active (non-expired)
// blacklist. The second return value is the expiry time (zero if not blacklisted).
func IsBlacklisted(conditionID string, blacklist []BlacklistEntry) (bool, time.Time) {
	now := time.Now().UTC()
	for _, e := range blacklist {
		if e.ConditionID == conditionID && e.ExpiresAt.After(now) {
			return true, e.ExpiresAt
		}
	}
	return false, time.Time{}
}

// AddToBlacklist adds conditionID to the blacklist for `days` days (replacing
// any existing entry for the same condition ID) and saves the updated list.
// If days <= 0 the default of 5 days is used.
func AddToBlacklist(conditionID, city, signal string, days int, dataRoot string) error {
	if days <= 0 {
		days = defaultBlacklistDays
	}
	entries := LoadBlacklist(dataRoot)
	// Remove any pre-existing entry for this condition ID so we don't duplicate.
	fresh := make([]BlacklistEntry, 0, len(entries)+1)
	for _, e := range entries {
		if e.ConditionID != conditionID {
			fresh = append(fresh, e)
		}
	}
	now := time.Now().UTC()
	fresh = append(fresh, BlacklistEntry{
		ConditionID: conditionID,
		City:        city,
		Signal:      signal,
		LostAt:      now,
		ExpiresAt:   now.Add(time.Duration(days) * 24 * time.Hour),
	})
	slog.Info("blacklist: added market after loss",
		"conditionID", conditionID,
		"city", city,
		"signal", signal,
		"expires_at", fresh[len(fresh)-1].ExpiresAt.Format(time.DateOnly),
	)
	return SaveBlacklist(fresh, dataRoot)
}

// SaveBlacklist writes the blacklist slice to disk as formatted JSON.
func SaveBlacklist(entries []BlacklistEntry, dataRoot string) error {
	path := blacklistPath(dataRoot)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}
