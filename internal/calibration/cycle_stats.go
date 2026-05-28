// cycle_stats.go — per-cycle performance journal (TASK-199).
//
// Each bot cycle appends one CSV row to data/cycles.csv so operators can
// analyse throughput, latency, and edge quality over time.
package calibration

import (
	"encoding/csv"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

// CycleStat holds summary data from a single bot cycle.
type CycleStat struct {
	Timestamp        time.Time
	DurationMs       int64
	MarketsEvaluated int
	BetsPlaced       int
	AvgEdge          float64
	AvgConfidence    float64
}

const cycleStatsFile = "data/cycles.csv"

var cycleStatsHeader = []string{
	"timestamp", "duration_ms", "markets_evaluated", "bets_placed", "avg_edge", "avg_confidence",
}

func cycleStatsPath(dataRoot string) string {
	if dataRoot != "" && dataRoot != "." {
		return filepath.Join(dataRoot, cycleStatsFile)
	}
	return cycleStatsFile
}

// AppendCycleStat appends a single CycleStat row to data/cycles.csv.
// The file and its parent directory are created if they don't exist.
func AppendCycleStat(stat CycleStat, dataRoot string) error {
	path := cycleStatsPath(dataRoot)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}

	needHeader := false
	if _, err := os.Stat(path); os.IsNotExist(err) {
		needHeader = true
	}

	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()

	w := csv.NewWriter(f)
	if needHeader {
		if err := w.Write(cycleStatsHeader); err != nil {
			return err
		}
	}
	row := []string{
		stat.Timestamp.UTC().Format(time.RFC3339),
		strconv.FormatInt(stat.DurationMs, 10),
		strconv.Itoa(stat.MarketsEvaluated),
		strconv.Itoa(stat.BetsPlaced),
		fmt.Sprintf("%.4f", stat.AvgEdge),
		fmt.Sprintf("%.4f", stat.AvgConfidence),
	}
	if err := w.Write(row); err != nil {
		return err
	}
	w.Flush()
	return w.Error()
}

// LoadCycleStats reads all rows from data/cycles.csv.
// Returns an empty slice (not an error) if the file doesn't exist yet.
func LoadCycleStats(dataRoot string) ([]CycleStat, error) {
	path := cycleStatsPath(dataRoot)
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()

	r := csv.NewReader(f)
	rows, err := r.ReadAll()
	if err != nil {
		return nil, err
	}

	var stats []CycleStat
	for i, row := range rows {
		if i == 0 && len(row) > 0 && row[0] == "timestamp" {
			continue // skip header
		}
		if len(row) < 6 {
			continue
		}
		ts, err := time.Parse(time.RFC3339, row[0])
		if err != nil {
			continue
		}
		durMs, _ := strconv.ParseInt(row[1], 10, 64)
		mktEval, _ := strconv.Atoi(row[2])
		betsPlaced, _ := strconv.Atoi(row[3])
		avgEdge, _ := strconv.ParseFloat(row[4], 64)
		avgConf, _ := strconv.ParseFloat(row[5], 64)
		stats = append(stats, CycleStat{
			Timestamp:        ts,
			DurationMs:       durMs,
			MarketsEvaluated: mktEval,
			BetsPlaced:       betsPlaced,
			AvgEdge:          avgEdge,
			AvgConfidence:    avgConf,
		})
	}
	return stats, nil
}
