// telegram_watchlist.go — TASK-153: /watchlist Telegram command.
//
// Operators can pin specific Polymarket conditionIDs that are always evaluated,
// even if they fall outside the normal discovery window.
//
//	/watchlist list           — show all pinned conditionIDs
//	/watchlist add <id>       — add a conditionID
//	/watchlist remove <id>    — remove a conditionID
//
// Persisted to data/watchlist.json.
package notifier

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const watchlistFile = "watchlist.json"

// LoadWatchlist reads the persisted watchlist from dataRoot/watchlist.json.
// Returns an empty slice if the file doesn't exist or is malformed.
func LoadWatchlist(dataRoot string) []string {
	path := watchlistPath(dataRoot)
	data, err := os.ReadFile(path)
	if err != nil {
		return []string{}
	}
	var ids []string
	if err := json.Unmarshal(data, &ids); err != nil {
		return []string{}
	}
	return ids
}

// SaveWatchlist writes the watchlist to dataRoot/watchlist.json, creating
// the data directory if needed.
func SaveWatchlist(dataRoot string, ids []string) error {
	path := watchlistPath(dataRoot)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("watchlist: mkdir: %w", err)
	}
	data, err := json.MarshalIndent(ids, "", "  ")
	if err != nil {
		return fmt.Errorf("watchlist: marshal: %w", err)
	}
	return os.WriteFile(path, data, 0o644)
}

func watchlistPath(dataRoot string) string {
	if dataRoot == "" || dataRoot == "." {
		return filepath.Join("data", watchlistFile)
	}
	return filepath.Join(dataRoot, "data", watchlistFile)
}

// handleWatchlist dispatches /watchlist sub-commands and returns a reply string.
// arg is the text after "/watchlist " (may be empty).
func handleWatchlist(bcfg BotConfig, arg string) string {
	parts := strings.Fields(arg)
	if len(parts) == 0 {
		return watchlistHelp()
	}

	switch parts[0] {
	case "list":
		return handleWatchlistList(bcfg)
	case "add":
		if len(parts) < 2 {
			return "❌ Usage: <code>/watchlist add &lt;conditionID&gt;</code>"
		}
		return handleWatchlistAdd(bcfg, parts[1])
	case "remove", "rm", "del":
		if len(parts) < 2 {
			return "❌ Usage: <code>/watchlist remove &lt;conditionID&gt;</code>"
		}
		return handleWatchlistRemove(bcfg, parts[1])
	default:
		return watchlistHelp()
	}
}

func handleWatchlistList(bcfg BotConfig) string {
	ids := LoadWatchlist(bcfg.DataRoot)
	if len(ids) == 0 {
		return "📋 Watchlist is empty.\nAdd markets with: <code>/watchlist add &lt;conditionID&gt;</code>"
	}
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("<b>📌 Watchlist (%d)</b>\n", len(ids)))
	for i, id := range ids {
		sb.WriteString(fmt.Sprintf("%d. <code>%s</code>\n", i+1, id))
	}
	return strings.TrimRight(sb.String(), "\n")
}

func handleWatchlistAdd(bcfg BotConfig, conditionID string) string {
	ids := LoadWatchlist(bcfg.DataRoot)
	// Deduplicate.
	for _, id := range ids {
		if id == conditionID {
			return fmt.Sprintf("ℹ️ <code>%s</code> is already in the watchlist.", conditionID)
		}
	}
	ids = append(ids, conditionID)
	if err := SaveWatchlist(bcfg.DataRoot, ids); err != nil {
		return fmt.Sprintf("❌ Failed to save watchlist: %v", err)
	}
	return fmt.Sprintf("✅ Added <code>%s</code> to watchlist (%d total).", conditionID, len(ids))
}

func handleWatchlistRemove(bcfg BotConfig, conditionID string) string {
	ids := LoadWatchlist(bcfg.DataRoot)
	newIDs := ids[:0]
	found := false
	for _, id := range ids {
		if id == conditionID {
			found = true
			continue
		}
		newIDs = append(newIDs, id)
	}
	if !found {
		return fmt.Sprintf("ℹ️ <code>%s</code> is not in the watchlist.", conditionID)
	}
	if err := SaveWatchlist(bcfg.DataRoot, newIDs); err != nil {
		return fmt.Sprintf("❌ Failed to save watchlist: %v", err)
	}
	return fmt.Sprintf("✅ Removed <code>%s</code> from watchlist (%d remaining).", conditionID, len(newIDs))
}

func watchlistHelp() string {
	return `📌 <b>Watchlist commands</b>
<code>/watchlist list</code>          — show pinned markets
<code>/watchlist add &lt;id&gt;</code>   — pin a conditionID
<code>/watchlist remove &lt;id&gt;</code> — unpin a conditionID

Watchlisted markets are always evaluated, even outside the normal discovery window.`
}
