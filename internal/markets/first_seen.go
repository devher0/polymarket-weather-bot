// first_seen.go — TASK-124: track when each market was first seen.
//
// New markets (< 2 hours old) often have worse price discovery and can offer
// larger edges. We record the first time a conditionID is encountered and use
// that timestamp to flag markets as "new" during evaluation.
package markets

import (
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const firstSeenFile = "data/market_first_seen.json"

// NewMarketWindowHours is the window during which a market is considered "new".
const NewMarketWindowHours = 2.0

// firstSeenStore is a thread-safe in-memory cache of conditionID → first-seen time.
type firstSeenStore struct {
	mu   sync.RWMutex
	data map[string]time.Time
	path string
}

var globalFirstSeen *firstSeenStore
var globalFirstSeenOnce sync.Once

func getFirstSeenStore(dataRoot string) *firstSeenStore {
	globalFirstSeenOnce.Do(func() {
		path := firstSeenPath(dataRoot)
		store := &firstSeenStore{
			data: make(map[string]time.Time),
			path: path,
		}
		store.load()
		globalFirstSeen = store
	})
	return globalFirstSeen
}

func firstSeenPath(dataRoot string) string {
	if dataRoot == "" || dataRoot == "." {
		return firstSeenFile
	}
	return filepath.Join(dataRoot, firstSeenFile)
}

func (s *firstSeenStore) load() {
	data, err := os.ReadFile(s.path)
	if err != nil {
		return
	}
	var raw map[string]time.Time
	if err := json.Unmarshal(data, &raw); err != nil {
		return
	}
	s.mu.Lock()
	s.data = raw
	s.mu.Unlock()
}

func (s *firstSeenStore) save() {
	s.mu.RLock()
	data, err := json.Marshal(s.data)
	s.mu.RUnlock()
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return
	}
	if err := os.WriteFile(s.path, data, 0o644); err != nil {
		slog.Warn("first_seen: save failed", "err", err)
	}
}

// RecordFirstSeen records the current time as the first-seen timestamp for
// conditionID. If the ID was already recorded, this is a no-op. Returns true
// when a new entry was created (i.e., the market is brand new to us).
func RecordFirstSeen(conditionID string, dataRoot string) bool {
	s := getFirstSeenStore(dataRoot)
	s.mu.Lock()
	if _, exists := s.data[conditionID]; exists {
		s.mu.Unlock()
		return false
	}
	s.data[conditionID] = time.Now().UTC()
	s.mu.Unlock()
	s.save()
	return true
}

// IsNew returns true when conditionID was first seen within NewMarketWindowHours.
// Returns false for unknown markets (they should be registered via RecordFirstSeen first).
func IsNew(conditionID string, dataRoot string) bool {
	s := getFirstSeenStore(dataRoot)
	s.mu.RLock()
	t, ok := s.data[conditionID]
	s.mu.RUnlock()
	if !ok {
		return false
	}
	return time.Since(t).Hours() < NewMarketWindowHours
}

// FirstSeenAt returns when conditionID was first recorded, and whether it was found.
func FirstSeenAt(conditionID string, dataRoot string) (time.Time, bool) {
	s := getFirstSeenStore(dataRoot)
	s.mu.RLock()
	t, ok := s.data[conditionID]
	s.mu.RUnlock()
	return t, ok
}

// RecentMarkets returns all conditionIDs first seen within the last maxAgeDays days.
func RecentMarkets(dataRoot string, maxAgeDays float64) []string {
	s := getFirstSeenStore(dataRoot)
	s.mu.RLock()
	defer s.mu.RUnlock()
	cutoff := time.Now().UTC().Add(-time.Duration(maxAgeDays * 24 * float64(time.Hour)))
	var result []string
	for id, t := range s.data {
		if t.After(cutoff) {
			result = append(result, id)
		}
	}
	return result
}
