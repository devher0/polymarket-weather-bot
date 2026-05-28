// milestone.go — profit milestone tracking and detection.
//
// Milestones fire once when the bankroll crosses 125%, 150%, 200%, or 300% of
// the initial bankroll (DefaultBankroll = 100 USDC by default). Reached
// milestones are persisted to data/milestones.json so the alert is sent only
// once even across bot restarts.
package calibration

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
)

// Milestone describes a single profit-milestone threshold.
type Milestone struct {
	Pct   float64 // multiplier vs initial bankroll, e.g. 1.25 for 125%
	Label string  // human-readable label, e.g. "+25% ROI"
}

// predefinedMilestones are the four default profit targets.
var predefinedMilestones = []Milestone{
	{1.25, "+25% ROI"},
	{1.50, "+50% ROI"},
	{2.00, "+100% ROI (2×)"},
	{3.00, "+200% ROI (3×)"},
}

const milestonesFile = "data/milestones.json"

var milestoneMu sync.Mutex

// milestonesState is the persisted file format.
type milestonesState struct {
	Reached map[string]bool `json:"reached"` // key: formatted pct string
}

func milestoneKey(pct float64) string {
	return fmt.Sprintf("%.4f", pct)
}

// LoadMilestones reads the set of already-reached milestone percentages from disk.
// Returns an empty map on any error (graceful degradation).
func LoadMilestones(dataRoot string) map[float64]bool {
	if dataRoot == "" {
		dataRoot = "."
	}
	path := filepath.Join(dataRoot, milestonesFile)

	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return map[float64]bool{}
	}
	if err != nil {
		slog.Warn("milestones: read failed", "err", err)
		return map[float64]bool{}
	}

	var state milestonesState
	if err := json.Unmarshal(data, &state); err != nil {
		slog.Warn("milestones: parse failed", "err", err)
		return map[float64]bool{}
	}

	result := make(map[float64]bool)
	for _, m := range predefinedMilestones {
		if state.Reached[milestoneKey(m.Pct)] {
			result[m.Pct] = true
		}
	}
	return result
}

// MarkMilestone records that a milestone has been reached so it won't alert again.
func MarkMilestone(pct float64, dataRoot string) error {
	if dataRoot == "" {
		dataRoot = "."
	}
	path := filepath.Join(dataRoot, milestonesFile)

	milestoneMu.Lock()
	defer milestoneMu.Unlock()

	// Load current state.
	state := milestonesState{Reached: map[string]bool{}}
	if data, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(data, &state)
		if state.Reached == nil {
			state.Reached = map[string]bool{}
		}
	}

	state.Reached[milestoneKey(pct)] = true

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("milestones: mkdir: %w", err)
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("milestones: marshal: %w", err)
	}
	return os.WriteFile(path, data, 0o644)
}

// CheckMilestones compares the current bankroll against the initial bankroll and
// returns any milestones that have been crossed for the first time. It does NOT
// automatically persist the results — callers must call MarkMilestone for each
// returned entry.
//
// Returns an empty slice when current <= initial or all milestones already reached.
func CheckMilestones(current, initial float64, dataRoot string) []Milestone {
	if initial <= 0 || current <= initial {
		return nil
	}

	reached := LoadMilestones(dataRoot)
	ratio := current / initial

	var newMilestones []Milestone
	for _, m := range predefinedMilestones {
		if ratio >= m.Pct && !reached[m.Pct] {
			newMilestones = append(newMilestones, m)
		}
	}
	return newMilestones
}
