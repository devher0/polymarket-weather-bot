// brier_history_test.go — TASK-198 unit tests.
package calibration

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func makeResolvedBet(ourP float64, won bool) BetRecord {
	return BetRecord{
		ConditionID:    "c1",
		Timestamp:      time.Now().Add(-time.Hour),
		Side:           "YES",
		OurProbability: ourP,
		MarketPrice:    0.5,
		SizeUSDC:       1.0,
		Outcome:        boolPtr(won),
		ResolvedAt:     time.Now(),
	}
}

// TestAppendBrierSnapshot_Idempotent verifies that calling AppendBrierSnapshot
// twice on the same UTC day does not duplicate the record.
func TestAppendBrierSnapshot_Idempotent(t *testing.T) {
	dir := t.TempDir()
	records := []BetRecord{makeResolvedBet(0.7, true)}

	if err := AppendBrierSnapshot(records, dir); err != nil {
		t.Fatalf("first append: %v", err)
	}
	if err := AppendBrierSnapshot(records, dir); err != nil {
		t.Fatalf("second append: %v", err)
	}

	snaps, err := LoadBrierSnapshots(dir)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(snaps) != 1 {
		t.Errorf("expected 1 snapshot, got %d", len(snaps))
	}
}

// TestLoadBrierSnapshots_MissingFile verifies that a missing file returns nil
// without error.
func TestLoadBrierSnapshots_MissingFile(t *testing.T) {
	dir := t.TempDir()
	snaps, err := LoadBrierSnapshots(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if snaps != nil {
		t.Errorf("expected nil, got %v", snaps)
	}
}

// TestAppendBrierSnapshot_LoadRoundtrip verifies persist + reload.
func TestAppendBrierSnapshot_LoadRoundtrip(t *testing.T) {
	dir := t.TempDir()
	records := []BetRecord{makeResolvedBet(0.8, false)}

	if err := AppendBrierSnapshot(records, dir); err != nil {
		t.Fatalf("append: %v", err)
	}
	snaps, err := LoadBrierSnapshots(dir)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(snaps) != 1 {
		t.Fatalf("expected 1 snapshot, got %d", len(snaps))
	}
	today := time.Now().UTC().Format("2006-01-02")
	if snaps[0].Date != today {
		t.Errorf("wrong date: got %q, want %q", snaps[0].Date, today)
	}
}

// TestBrierSparkline_Empty verifies that an empty slice returns "".
func TestBrierSparkline_Empty(t *testing.T) {
	if s := BrierSparkline(nil, 10); s != "" {
		t.Errorf("expected empty string, got %q", s)
	}
}

// TestBrierSparkline_Ascending verifies that a decreasing Brier (improving)
// sequence produces rising block characters (later chars >= earlier ones).
func TestBrierSparkline_Ascending(t *testing.T) {
	// Decreasing Brier = improving = higher blocks as time progresses.
	snaps := []BrierSnapshot{
		{Date: "2026-05-01", BrierAll: 0.25, Brier7d: 0.25},
		{Date: "2026-05-02", BrierAll: 0.20, Brier7d: 0.20},
		{Date: "2026-05-03", BrierAll: 0.15, Brier7d: 0.15},
		{Date: "2026-05-04", BrierAll: 0.10, Brier7d: 0.10},
	}
	spark := BrierSparkline(snaps, 10)
	if len([]rune(spark)) != len(snaps) {
		t.Errorf("expected %d chars, got %d (%q)", len(snaps), len([]rune(spark)), spark)
	}
	// Each rune should be >= the previous (improvement = taller blocks).
	runes := []rune(spark)
	for i := 1; i < len(runes); i++ {
		if runes[i] < runes[i-1] {
			t.Errorf("block at index %d (%c) lower than %d (%c) — expected improvement to show as rising",
				i, runes[i], i-1, runes[i-1])
		}
	}
}

// TestBrierTrendLabel verifies improving/stable/worsening classification.
func TestBrierTrendLabel_Improving(t *testing.T) {
	snaps := []BrierSnapshot{
		{Date: "2026-05-01", Brier7d: 0.22},
		{Date: "2026-05-08", Brier7d: 0.18},
		{Date: "2026-05-15", Brier7d: 0.12},
	}
	if got := BrierTrendLabel(snaps, 10); got != "improving" {
		t.Errorf("expected improving, got %q", got)
	}
}

// TestAppendBrierSnapshot_CreatesDataDir verifies the function creates the
// data/ directory if it does not exist.
func TestAppendBrierSnapshot_CreatesDataDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested_root")
	records := []BetRecord{makeResolvedBet(0.6, true)}
	if err := AppendBrierSnapshot(records, dir); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	path := filepath.Join(dir, brierSnapshotFile)
	if _, err := os.Stat(path); os.IsNotExist(err) {
		t.Errorf("expected file %s to exist", path)
	}
}
