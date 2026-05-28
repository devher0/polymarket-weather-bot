package calibration

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCheckMilestones_NoAchievement(t *testing.T) {
	dir := t.TempDir()
	milestones := CheckMilestones(100.0, 100.0, dir)
	if len(milestones) != 0 {
		t.Errorf("expected 0 milestones, got %d", len(milestones))
	}

	// Below initial.
	milestones = CheckMilestones(90.0, 100.0, dir)
	if len(milestones) != 0 {
		t.Errorf("expected 0 milestones for below-initial, got %d", len(milestones))
	}
}

func TestCheckMilestones_FirstAchievement(t *testing.T) {
	dir := t.TempDir()
	// 125% reached.
	milestones := CheckMilestones(125.0, 100.0, dir)
	if len(milestones) != 1 {
		t.Fatalf("expected 1 milestone, got %d", len(milestones))
	}
	if milestones[0].Pct != 1.25 {
		t.Errorf("expected Pct=1.25, got %v", milestones[0].Pct)
	}
}

func TestCheckMilestones_AlreadyReached(t *testing.T) {
	dir := t.TempDir()
	// Mark 125% as already reached.
	if err := MarkMilestone(1.25, dir); err != nil {
		t.Fatal(err)
	}
	milestones := CheckMilestones(125.0, 100.0, dir)
	if len(milestones) != 0 {
		t.Errorf("already-reached milestone should not appear again, got %d", len(milestones))
	}
}

func TestCheckMilestones_MultipleSameTime(t *testing.T) {
	dir := t.TempDir()
	// Jump straight to 210% — crosses 125%, 150%, and 200%.
	milestones := CheckMilestones(210.0, 100.0, dir)
	if len(milestones) != 3 {
		t.Errorf("expected 3 milestones, got %d", len(milestones))
	}
}

func TestMarkAndLoadMilestone_Roundtrip(t *testing.T) {
	dir := t.TempDir()

	reached := LoadMilestones(dir)
	if len(reached) != 0 {
		t.Error("expected empty milestones on fresh dir")
	}

	if err := MarkMilestone(1.50, dir); err != nil {
		t.Fatal(err)
	}

	reached = LoadMilestones(dir)
	if !reached[1.50] {
		t.Error("1.50 should be marked as reached")
	}
	if reached[1.25] {
		t.Error("1.25 should not be marked")
	}
}

func TestLoadMilestones_MissingFile(t *testing.T) {
	dir := t.TempDir()
	// Ensure missing file returns empty map, not panic.
	reached := LoadMilestones(filepath.Join(dir, "nonexistent"))
	if len(reached) != 0 {
		t.Errorf("expected empty map for missing dir, got %v", reached)
	}
}

func TestLoadMilestones_CorruptFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "data", "milestones.json")
	_ = os.MkdirAll(filepath.Dir(path), 0o755)
	_ = os.WriteFile(path, []byte("not-json{{{"), 0o644)

	reached := LoadMilestones(dir)
	if len(reached) != 0 {
		t.Errorf("expected empty map for corrupt file, got %v", reached)
	}
}
