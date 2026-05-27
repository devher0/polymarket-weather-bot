// prediction_log.go — structured logging of every market evaluation.
//
// Every call to EvaluateFused() appends a PredictionRecord to
// data/predictions/YYYY-MM-DD.jsonl so that operators can later audit:
//   - Why the bot did NOT bet on a specific market (SKIP with reason)
//   - Distribution of edges, confidence, and ensemble uncertainty
//   - Per-city / per-signal breakdown of evaluated vs bet markets
//
// Usage (dashboard):
//
//	go run ./cmd/dashboard analysis
package strategy

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// PredictionRecord captures the full evaluation context for one market.
// Decision values:
//
//	"BET_YES"          — placed (or would place) a YES bet
//	"BET_NO"           — placed (or would place) a NO bet
//	"SKIP:confidence"  — ff.Confidence < 0.40, sources disagree
//	"SKIP:no_edge"     — edge insufficient after confidence / seasonal adjustment
//	"SKIP:min_size"    — Kelly size too small (< $0.50) or post-ensemble scaling
//	"SKIP:stale"       — forecast older than max_forecast_age_hours
type PredictionRecord struct {
	Timestamp           string   `json:"ts"`
	ConditionID         string   `json:"condition_id"`
	City                string   `json:"city"`
	Signal              string   `json:"signal"`
	YesPrice            float64  `json:"yes_price"`
	NoPrice             float64  `json:"no_price"`
	OurP                float64  `json:"our_p"`
	YesEdge             float64  `json:"yes_edge"`
	NoEdge              float64  `json:"no_edge"`
	Confidence          float64  `json:"confidence"`
	EnsembleUncertainty float64  `json:"ensemble_unc,omitempty"`
	AlertLevel          int      `json:"alert_level,omitempty"`
	Sources             []string `json:"sources,omitempty"`
	MaxTempC            float64  `json:"max_temp_c"`
	MinTempC            float64  `json:"min_temp_c"`
	PrecipMM            float64  `json:"precip_mm"`
	PrecipProb          float64  `json:"precip_prob"`
	WindKPH             float64  `json:"wind_kph"`
	Decision            string   `json:"decision"` // see constants above
	SizeUSDC            float64  `json:"size_usdc,omitempty"`
	Reason              string   `json:"reason,omitempty"`
}

// SavePrediction appends rec as a JSON line to
// data/predictions/YYYY-MM-DD.jsonl.  Errors are silently swallowed so that
// a disk-full or permission problem never crashes the betting loop.
func SavePrediction(rec PredictionRecord, dataRoot string) {
	if dataRoot == "" {
		dataRoot = "."
	}
	dir := filepath.Join(dataRoot, "predictions")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return
	}
	date := time.Now().UTC().Format("2006-01-02")
	path := filepath.Join(dir, date+".jsonl")

	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()

	b, err := json.Marshal(rec)
	if err != nil {
		return
	}
	_, _ = f.Write(b)
	_, _ = f.WriteString("\n")
}

// LoadPredictions reads all PredictionRecords from the JSONL file for the
// given date string ("2006-01-02").  If dataRoot is "", "." is used.
// Malformed lines are silently skipped.
func LoadPredictions(date, dataRoot string) ([]PredictionRecord, error) {
	if dataRoot == "" {
		dataRoot = "."
	}
	path := filepath.Join(dataRoot, "predictions", date+".jsonl")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var out []PredictionRecord
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var r PredictionRecord
		if json.Unmarshal([]byte(line), &r) == nil {
			out = append(out, r)
		}
	}
	return out, nil
}

// BreakdownKey groups evaluation records by city+signal for the analysis view.
type BreakdownKey struct {
	City   string
	Signal string
}

// BreakdownStats holds aggregate counts for one city/signal pair.
type BreakdownStats struct {
	Evaluated int
	Bets      int
	SkipConf  int
	SkipEdge  int
	SkipSize  int
	EdgeSum   float64
	ConfSum   float64
	TotalSize float64
}

func (s BreakdownStats) AvgEdge() float64 {
	if s.Bets == 0 {
		return 0
	}
	return s.EdgeSum / float64(s.Bets)
}

func (s BreakdownStats) AvgConf() float64 {
	if s.Evaluated == 0 {
		return 0
	}
	return s.ConfSum / float64(s.Evaluated)
}

func (s BreakdownStats) SkipPct() float64 {
	if s.Evaluated == 0 {
		return 0
	}
	return float64(s.Evaluated-s.Bets) / float64(s.Evaluated) * 100
}

// AnalyzePredictions builds per-city/signal breakdown from a slice of records.
func AnalyzePredictions(records []PredictionRecord) map[BreakdownKey]*BreakdownStats {
	result := make(map[BreakdownKey]*BreakdownStats)
	for _, r := range records {
		k := BreakdownKey{City: r.City, Signal: r.Signal}
		s := result[k]
		if s == nil {
			s = &BreakdownStats{}
			result[k] = s
		}
		s.Evaluated++
		s.ConfSum += r.Confidence
		bestEdge := math.Max(r.YesEdge, r.NoEdge)
		switch {
		case strings.HasPrefix(r.Decision, "BET"):
			s.Bets++
			s.EdgeSum += bestEdge
			s.TotalSize += r.SizeUSDC
		case r.Decision == "SKIP:confidence":
			s.SkipConf++
		case r.Decision == "SKIP:min_size":
			s.SkipSize++
		default: // SKIP:no_edge, SKIP:stale, etc.
			s.SkipEdge++
		}
	}
	return result
}

// SortedBreakdownKeys returns keys from the analysis map sorted by evaluated count desc.
func SortedBreakdownKeys(m map[BreakdownKey]*BreakdownStats) []BreakdownKey {
	keys := make([]BreakdownKey, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if m[keys[i]].Evaluated != m[keys[j]].Evaluated {
			return m[keys[i]].Evaluated > m[keys[j]].Evaluated
		}
		if keys[i].City != keys[j].City {
			return keys[i].City < keys[j].City
		}
		return keys[i].Signal < keys[j].Signal
	})
	return keys
}

// PredictionSummary returns a one-line summary of today's prediction log.
func PredictionSummary(records []PredictionRecord) string {
	total := len(records)
	bets := 0
	for _, r := range records {
		if strings.HasPrefix(r.Decision, "BET") {
			bets++
		}
	}
	return fmt.Sprintf("evaluated=%d bets=%d skip=%d", total, bets, total-bets)
}
