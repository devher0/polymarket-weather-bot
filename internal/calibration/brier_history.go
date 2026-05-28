// brier_history.go — TASK-198: daily Brier score snapshots.
//
// AppendBrierSnapshot writes one record per calendar day (UTC) to
// data/brier_snapshots.json.  The file is a JSON array; idempotent re-runs on
// the same day are silently ignored.  LoadBrierSnapshots reads the array back.
// BrierSparkline converts the last N snapshots into an ASCII spark-line where
// taller blocks represent better (lower) Brier scores.
package calibration

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// BrierSnapshot is one daily Brier score record.
type BrierSnapshot struct {
	Date     string  `json:"date"`      // "YYYY-MM-DD" UTC
	BrierAll float64 `json:"brier_all"` // overall Brier (all resolved)
	Brier7d  float64 `json:"brier_7d"`  // last 7-day window
	Brier30d float64 `json:"brier_30d"` // last 30-day window
	BetsAll  int     `json:"bets_all"`  // total resolved bets
}

const brierSnapshotFile = "data/brier_snapshots.json"

// AppendBrierSnapshot computes today's Brier score from records and appends a
// snapshot to data/brier_snapshots.json.  It is idempotent: if a record for
// today already exists the function returns nil without writing.
func AppendBrierSnapshot(records []BetRecord, dataRoot string) error {
	today := time.Now().UTC().Format("2006-01-02")

	existing, _ := LoadBrierSnapshots(dataRoot)
	for _, s := range existing {
		if s.Date == today {
			return nil // already recorded today
		}
	}

	brierAll, betsAll, _ := BrierScore(records)
	brier7d, _ := BrierWindow(records, 7)
	brier30d, _ := BrierWindow(records, 30)

	snap := BrierSnapshot{
		Date:     today,
		BrierAll: brierAll,
		Brier7d:  brier7d,
		Brier30d: brier30d,
		BetsAll:  betsAll,
	}
	existing = append(existing, snap)

	// Sort chronologically (oldest first) before writing.
	sort.Slice(existing, func(i, j int) bool {
		return existing[i].Date < existing[j].Date
	})

	path := filepath.Join(dataRoot, brierSnapshotFile)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(existing, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

// LoadBrierSnapshots reads all stored Brier snapshots from disk.
// Returns an empty slice (not error) when the file does not exist yet.
func LoadBrierSnapshots(dataRoot string) ([]BrierSnapshot, error) {
	path := filepath.Join(dataRoot, brierSnapshotFile)
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var snaps []BrierSnapshot
	if err := json.Unmarshal(data, &snaps); err != nil {
		return nil, err
	}
	return snaps, nil
}

// BrierSparkline builds a compact Unicode bar string representing the last n
// Brier snapshots (by brier_7d, falling back to brier_all when 7d is zero).
//
// Lower Brier = better, so we invert the scale: a lower score maps to a taller
// block.  We use the 8-level Unicode block set ▁▂▃▄▅▆▇█.
// Returns "" when fewer than 2 snapshots are available.
func BrierSparkline(snapshots []BrierSnapshot, n int) string {
	if len(snapshots) == 0 || n <= 0 {
		return ""
	}
	// Take the last n entries.
	if len(snapshots) > n {
		snapshots = snapshots[len(snapshots)-n:]
	}
	if len(snapshots) < 2 {
		return ""
	}

	vals := make([]float64, len(snapshots))
	for i, s := range snapshots {
		v := s.Brier7d
		if v <= 0 {
			v = s.BrierAll
		}
		vals[i] = v
	}

	// Find range.
	minV, maxV := vals[0], vals[0]
	for _, v := range vals[1:] {
		if v < minV {
			minV = v
		}
		if v > maxV {
			maxV = v
		}
	}

	blocks := []rune{'▁', '▂', '▃', '▄', '▅', '▆', '▇', '█'}
	rng := maxV - minV
	result := make([]rune, len(vals))
	for i, v := range vals {
		var idx int
		if rng < 1e-9 {
			idx = 3 // mid-level if all values identical
		} else {
			// Invert: lower Brier → higher block.
			norm := (maxV - v) / rng // 0=worst(low block) 1=best(high block)
			idx = int(math.Floor(norm * float64(len(blocks)-1)))
			idx = max0(min8(idx, len(blocks)-1), 0)
		}
		result[i] = blocks[idx]
	}
	return string(result)
}

func min8(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func max0(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// BrierTrendLabel returns a short human-readable trend label based on the
// difference between the most recent and oldest snapshot in the window.
// Returns "improving", "stable", or "worsening".
func BrierTrendLabel(snapshots []BrierSnapshot, n int) string {
	if len(snapshots) < 2 {
		return "stable"
	}
	if len(snapshots) > n {
		snapshots = snapshots[len(snapshots)-n:]
	}
	first := snapshots[0].Brier7d
	if first <= 0 {
		first = snapshots[0].BrierAll
	}
	last := snapshots[len(snapshots)-1].Brier7d
	if last <= 0 {
		last = snapshots[len(snapshots)-1].BrierAll
	}
	delta := last - first
	switch {
	case delta < -0.005:
		return "improving"
	case delta > 0.005:
		return "worsening"
	default:
		return "stable"
	}
}
