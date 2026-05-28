package markets

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSaveDriftEntry(t *testing.T) {
	dir := t.TempDir()
	err := SaveDriftEntry("cond-001", "YES", 0.45, dir)
	if err != nil {
		t.Fatalf("SaveDriftEntry: %v", err)
	}
	records, err := loadDriftRecords(dir)
	if err != nil {
		t.Fatalf("loadDriftRecords: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("want 1 record, got %d", len(records))
	}
	if records[0].EntryPrice != 0.45 {
		t.Errorf("want EntryPrice=0.45, got %f", records[0].EntryPrice)
	}
	if records[0].Side != "YES" {
		t.Errorf("want Side=YES, got %s", records[0].Side)
	}
}

func TestSaveDriftEntry_NoDuplicate(t *testing.T) {
	dir := t.TempDir()
	_ = SaveDriftEntry("cond-001", "YES", 0.45, dir)
	_ = SaveDriftEntry("cond-001", "YES", 0.99, dir) // should NOT overwrite

	records, _ := loadDriftRecords(dir)
	if len(records) != 1 {
		t.Fatalf("want 1 record (no duplicate), got %d", len(records))
	}
	if records[0].EntryPrice != 0.45 {
		t.Errorf("entry price overwritten: got %f", records[0].EntryPrice)
	}
}

func TestUpdateDrift(t *testing.T) {
	dir := t.TempDir()
	_ = SaveDriftEntry("cond-002", "YES", 0.50, dir)
	err := UpdateDrift("cond-002", 0.58, dir)
	if err != nil {
		t.Fatalf("UpdateDrift: %v", err)
	}

	records, _ := loadDriftRecords(dir)
	if records[0].CurrentPrice != 0.58 {
		t.Errorf("want CurrentPrice=0.58, got %f", records[0].CurrentPrice)
	}
	// drift = (0.58 - 0.50) × 100 = 8pp
	if records[0].DriftPP < 7.9 || records[0].DriftPP > 8.1 {
		t.Errorf("want DriftPP≈8.0, got %f", records[0].DriftPP)
	}
}

func TestLoadDriftSummary_Empty(t *testing.T) {
	dir := t.TempDir()
	// No file at all.
	_, ok := LoadDriftSummary(dir)
	if ok {
		t.Error("want ok=false for empty data directory")
	}
}

func TestLoadDriftSummary_Mixed(t *testing.T) {
	dir := t.TempDir()
	_ = os.MkdirAll(filepath.Join(dir, "data"), 0o755)

	_ = SaveDriftEntry("cond-A", "YES", 0.40, dir)
	_ = UpdateDrift("cond-A", 0.50, dir) // +10pp positive

	_ = SaveDriftEntry("cond-B", "NO", 0.60, dir)
	_ = UpdateDrift("cond-B", 0.50, dir) // -10pp negative

	summary, ok := LoadDriftSummary(dir)
	if !ok {
		t.Fatal("want ok=true")
	}
	if summary.Count != 2 {
		t.Errorf("want Count=2, got %d", summary.Count)
	}
	if summary.Positive != 1 {
		t.Errorf("want Positive=1, got %d", summary.Positive)
	}
	if summary.Negative != 1 {
		t.Errorf("want Negative=1, got %d", summary.Negative)
	}
	// avg drift = (10 + -10) / 2 = 0
	if summary.AvgDrift < -0.1 || summary.AvgDrift > 0.1 {
		t.Errorf("want AvgDrift≈0, got %f", summary.AvgDrift)
	}
}
