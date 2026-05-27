// Package strategy — heatmap.go
//
// TASK-075: Market opportunity heatmap CSV.
//
// After each cycle, the bot calls AppendHeatmap() with the evaluated
// prediction records. This appends rows to data/heatmap/YYYY-MM-DD.csv
// so operators can track edge/confidence distribution across city×signal
// combinations over time in Excel, pandas, or Grafana.
//
// Columns: timestamp, city, signal, our_p, yes_edge, no_edge, confidence,
//          ensemble_unc, decision, size_usdc
//
// If the file for today does not exist, a header row is written first.
package strategy

import (
	"encoding/csv"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

// HeatmapRow summarises one market evaluation for the heatmap.
type HeatmapRow struct {
	Timestamp           time.Time
	City                string
	Signal              string
	OurP                float64
	YesEdge             float64
	NoEdge              float64
	Confidence          float64
	EnsembleUncertainty float64
	Decision            string
	SizeUSDC            float64
}

var heatmapHeader = []string{
	"timestamp", "city", "signal",
	"our_p", "yes_edge", "no_edge",
	"confidence", "ensemble_unc",
	"decision", "size_usdc",
}

// AppendHeatmap writes the given rows to the daily heatmap CSV file.
// dataRoot is the root directory (same as cfg.DataRoot).
// If len(rows) == 0 the function is a no-op.
func AppendHeatmap(rows []HeatmapRow, dataRoot string) error {
	if len(rows) == 0 {
		return nil
	}

	dir := filepath.Join(dataRoot, "data", "heatmap")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("heatmap: mkdir: %w", err)
	}

	today := time.Now().UTC().Format("2006-01-02")
	path := filepath.Join(dir, today+".csv")

	// Determine whether the file is new (needs header).
	needsHeader := false
	if _, err := os.Stat(path); os.IsNotExist(err) {
		needsHeader = true
	}

	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("heatmap: open: %w", err)
	}
	defer f.Close()

	w := csv.NewWriter(f)
	defer w.Flush()

	if needsHeader {
		if err := w.Write(heatmapHeader); err != nil {
			return fmt.Errorf("heatmap: write header: %w", err)
		}
	}

	for _, r := range rows {
		row := []string{
			r.Timestamp.UTC().Format(time.RFC3339),
			r.City,
			r.Signal,
			strconv.FormatFloat(r.OurP, 'f', 4, 64),
			strconv.FormatFloat(r.YesEdge, 'f', 4, 64),
			strconv.FormatFloat(r.NoEdge, 'f', 4, 64),
			strconv.FormatFloat(r.Confidence, 'f', 4, 64),
			strconv.FormatFloat(r.EnsembleUncertainty, 'f', 4, 64),
			r.Decision,
			strconv.FormatFloat(r.SizeUSDC, 'f', 2, 64),
		}
		if err := w.Write(row); err != nil {
			return fmt.Errorf("heatmap: write row: %w", err)
		}
	}

	slog.Debug("heatmap updated", "path", path, "rows", len(rows))
	return nil
}

// HeatmapRowFromPrediction converts a PredictionRecord to a HeatmapRow.
func HeatmapRowFromPrediction(rec PredictionRecord) HeatmapRow {
	ts, _ := time.Parse(time.RFC3339, rec.Timestamp)
	if ts.IsZero() {
		ts = time.Now().UTC()
	}
	return HeatmapRow{
		Timestamp:           ts,
		City:                rec.City,
		Signal:              rec.Signal,
		OurP:                rec.OurP,
		YesEdge:             rec.YesEdge,
		NoEdge:              rec.NoEdge,
		Confidence:          rec.Confidence,
		EnsembleUncertainty: rec.EnsembleUncertainty,
		Decision:            rec.Decision,
		SizeUSDC:            rec.SizeUSDC,
	}
}

// LoadTodayHeatmap reads all rows from today's heatmap CSV.
// Returns an empty slice if the file doesn't exist.
func LoadTodayHeatmap(dataRoot string) ([]HeatmapRow, error) {
	today := time.Now().UTC().Format("2006-01-02")
	path := filepath.Join(dataRoot, "data", "heatmap", today+".csv")

	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("heatmap load: %w", err)
	}
	defer f.Close()

	r := csv.NewReader(f)
	records, err := r.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("heatmap read: %w", err)
	}

	var rows []HeatmapRow
	for i, rec := range records {
		if i == 0 {
			continue // skip header
		}
		if len(rec) < 10 {
			continue
		}
		ts, _ := time.Parse(time.RFC3339, rec[0])
		ourP, _ := strconv.ParseFloat(rec[3], 64)
		yesEdge, _ := strconv.ParseFloat(rec[4], 64)
		noEdge, _ := strconv.ParseFloat(rec[5], 64)
		conf, _ := strconv.ParseFloat(rec[6], 64)
		ensUnc, _ := strconv.ParseFloat(rec[7], 64)
		size, _ := strconv.ParseFloat(rec[9], 64)
		rows = append(rows, HeatmapRow{
			Timestamp:           ts,
			City:                rec[1],
			Signal:              rec[2],
			OurP:                ourP,
			YesEdge:             yesEdge,
			NoEdge:              noEdge,
			Confidence:          conf,
			EnsembleUncertainty: ensUnc,
			Decision:            rec[8],
			SizeUSDC:            size,
		})
	}
	return rows, nil
}
