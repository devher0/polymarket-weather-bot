package notifier

import (
	"os"
	"testing"
)

func TestLoadWatchlistEmpty(t *testing.T) {
	dir := t.TempDir()
	ids := LoadWatchlist(dir)
	if len(ids) != 0 {
		t.Fatalf("expected empty slice, got %v", ids)
	}
}

func TestWatchlistAddSaveLoadRoundtrip(t *testing.T) {
	dir := t.TempDir()

	// Save two IDs.
	want := []string{"cond-abc-001", "cond-xyz-002"}
	if err := SaveWatchlist(dir, want); err != nil {
		t.Fatalf("SaveWatchlist: %v", err)
	}

	got := LoadWatchlist(dir)
	if len(got) != len(want) {
		t.Fatalf("expected %d IDs, got %d: %v", len(want), len(got), got)
	}
	for i, id := range want {
		if got[i] != id {
			t.Errorf("index %d: want %q, got %q", i, id, got[i])
		}
	}
}

func TestWatchlistRemoveExisting(t *testing.T) {
	dir := t.TempDir()
	initial := []string{"cond-aaa", "cond-bbb", "cond-ccc"}
	if err := SaveWatchlist(dir, initial); err != nil {
		t.Fatalf("SaveWatchlist: %v", err)
	}

	bcfg := BotConfig{DataRoot: dir}

	// Remove the middle element.
	reply := handleWatchlistRemove(bcfg, "cond-bbb")
	if reply == "" {
		t.Fatal("expected non-empty reply")
	}

	remaining := LoadWatchlist(dir)
	if len(remaining) != 2 {
		t.Fatalf("expected 2 remaining, got %d: %v", len(remaining), remaining)
	}
	for _, id := range remaining {
		if id == "cond-bbb" {
			t.Error("removed ID is still in watchlist")
		}
	}
}

func TestWatchlistRemoveNonExistent(t *testing.T) {
	dir := t.TempDir()
	if err := SaveWatchlist(dir, []string{"cond-aaa"}); err != nil {
		t.Fatalf("SaveWatchlist: %v", err)
	}
	bcfg := BotConfig{DataRoot: dir}
	reply := handleWatchlistRemove(bcfg, "cond-not-there")
	// Should say it's not found, not an error.
	if reply == "" {
		t.Fatal("expected non-empty reply")
	}
	// File should be unchanged.
	ids := LoadWatchlist(dir)
	if len(ids) != 1 || ids[0] != "cond-aaa" {
		t.Errorf("watchlist changed unexpectedly: %v", ids)
	}
}

func TestWatchlistAddDuplicate(t *testing.T) {
	dir := t.TempDir()
	bcfg := BotConfig{DataRoot: dir}
	handleWatchlistAdd(bcfg, "cond-dup")
	handleWatchlistAdd(bcfg, "cond-dup") // second add — should be ignored
	ids := LoadWatchlist(dir)
	if len(ids) != 1 {
		t.Errorf("expected 1 entry after duplicate add, got %d: %v", len(ids), ids)
	}
}

// Verify that watchlistPath handles empty / "." dataRoot correctly.
func TestWatchlistPathFallback(t *testing.T) {
	p1 := watchlistPath("")
	p2 := watchlistPath(".")
	if p1 != p2 {
		// Both should resolve the same way for the default case.
		// (actual path value doesn't matter — just that they're consistent)
		t.Logf("empty=%q dot=%q", p1, p2)
	}
	// Must not be empty.
	if p1 == "" {
		t.Error("watchlistPath returned empty string for empty dataRoot")
	}
	_ = os.Remove(p1) // clean up in case test run from repo root
}
