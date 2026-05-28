// cache.go — disk-based cache for GetWeatherMarkets() with 1-hour TTL.
// Reduces Polymarket API calls on restarts and keeps rate-limit headroom.
package markets

import (
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const marketCacheTTL = 1 * time.Hour

type marketsCache struct {
	Markets   []Market  `json:"markets"`
	FetchedAt time.Time `json:"fetched_at"`
}

var (
	cacheDataRoot string
	cacheMu       sync.Mutex
)

// SetCacheDataRoot sets the directory root where markets_cache.json is stored.
// Call once at bot startup with cfg.DataRoot before any GetWeatherMarkets call.
func SetCacheDataRoot(root string) {
	cacheMu.Lock()
	defer cacheMu.Unlock()
	cacheDataRoot = root
}

func marketCachePath() string {
	cacheMu.Lock()
	root := cacheDataRoot
	cacheMu.Unlock()
	if root == "" {
		root = "."
	}
	return filepath.Join(root, "data", "markets_cache.json")
}

// loadMarketCache returns cached markets when the cache file is present and not
// expired. Returns nil, false on any miss (no file, parse error, or TTL exceeded).
func loadMarketCache() ([]Market, bool) {
	path := marketCachePath()
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, false
	}

	var c marketsCache
	if err := json.Unmarshal(data, &c); err != nil {
		slog.Debug("markets cache parse error, will re-fetch", "err", err)
		return nil, false
	}

	age := time.Since(c.FetchedAt)
	if age > marketCacheTTL {
		slog.Debug("markets cache expired", "age", age.Round(time.Second))
		return nil, false
	}

	slog.Info("markets cache hit", "markets", len(c.Markets), "age", age.Round(time.Second))
	return c.Markets, true
}

// saveMarketCache writes markets to disk. Errors are logged but not returned
// because a cache write failure is non-fatal.
func saveMarketCache(mkts []Market) {
	path := marketCachePath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		slog.Warn("markets cache mkdir failed", "err", err)
		return
	}

	data, err := json.Marshal(marketsCache{Markets: mkts, FetchedAt: time.Now()})
	if err != nil {
		slog.Warn("markets cache marshal failed", "err", err)
		return
	}

	if err := os.WriteFile(path, data, 0o644); err != nil {
		slog.Warn("markets cache write failed", "err", err)
		return
	}
	slog.Debug("markets cache saved", "markets", len(mkts))
}
