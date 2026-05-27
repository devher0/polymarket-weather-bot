// cmd/report — generate a self-contained HTML performance report from bets_history.csv.
//
// Usage:
//
//	go run ./cmd/report                           # writes data/report.html
//	go run ./cmd/report --out /tmp/report.html    # custom output path
//	go run ./cmd/report --data /path/to/data      # custom data root
//
// The HTML file contains embedded Chart.js charts (loaded from CDN) with:
//   - Cumulative P&L curve over time
//   - Win rate by signal type (bar chart)
//   - Rolling 10-bet Brier score trend
//   - City breakdown table
//   - Open positions list
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"math"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/devher0/polymarket-weather-bot/internal/calibration"
)

func main() {
	dataRoot := flag.String("data", ".", "data root directory")
	outPath := flag.String("out", "", "output HTML path (default: <data>/data/report.html)")
	flag.Parse()

	if *outPath == "" {
		*outPath = *dataRoot + "/data/report.html"
	}

	records, err := calibration.LoadHistory(*dataRoot)
	if err != nil && !os.IsNotExist(err) {
		slog.Error("load history", "err", err)
		os.Exit(1)
	}

	rpt := buildReport(records)

	html, err := renderHTML(rpt)
	if err != nil {
		slog.Error("render html", "err", err)
		os.Exit(1)
	}

	if err := os.WriteFile(*outPath, []byte(html), 0644); err != nil {
		slog.Error("write html", "err", err)
		os.Exit(1)
	}
	fmt.Printf("Report written to %s\n", *outPath)
}

// report holds all data needed to render the HTML dashboard.
type report struct {
	GeneratedAt    string          `json:"generated_at"`
	TotalBets      int             `json:"total_bets"`
	ResolvedBets   int             `json:"resolved_bets"`
	OpenBets       int             `json:"open_bets"`
	WinRate        float64         `json:"win_rate"`        // 0–100
	TotalPnL       float64         `json:"total_pnl"`       // USDC
	BrierScore     float64         `json:"brier_score"`     // 0–1 (lower = better)
	PnLCurve       pnlPoint        `json:"_"`               // placeholder — real data in PnLSeries
	PnLSeries      []pnlPoint      `json:"pnl_series"`
	BrierSeries    []brierPoint    `json:"brier_series"`
	SignalStats    []signalStat    `json:"signal_stats"`
	CityStats      []cityStat      `json:"city_stats"`
	OpenPositions  []openPosition  `json:"open_positions"`
}

type pnlPoint struct {
	Date string  `json:"date"`
	PnL  float64 `json:"pnl"`
}

type brierPoint struct {
	Label string  `json:"label"`
	Score float64 `json:"score"`
}

type signalStat struct {
	Signal  string  `json:"signal"`
	Count   int     `json:"count"`
	Wins    int     `json:"wins"`
	WinRate float64 `json:"win_rate"`
	AvgEdge float64 `json:"avg_edge"`
}

type cityStat struct {
	City    string  `json:"city"`
	Count   int     `json:"count"`
	Wins    int     `json:"wins"`
	WinRate float64 `json:"win_rate"`
	PnL     float64 `json:"pnl"`
}

type openPosition struct {
	ConditionID string    `json:"condition_id"`
	City        string    `json:"city"`
	Signal      string    `json:"signal"`
	Side        string    `json:"side"`
	SizeUSDC    float64   `json:"size_usdc"`
	OurProb     float64   `json:"our_prob"`
	PlacedAt    time.Time `json:"placed_at"`
}

func buildReport(records []calibration.BetRecord) report {
	rpt := report{
		GeneratedAt: time.Now().UTC().Format("2006-01-02 15:04 UTC"),
	}

	rpt.TotalBets = len(records)

	// Separate resolved vs open.
	var resolved []calibration.BetRecord
	for _, r := range records {
		if r.Outcome != nil {
			resolved = append(resolved, r)
		} else {
			rpt.OpenBets++
			rpt.OpenPositions = append(rpt.OpenPositions, openPosition{
				ConditionID: r.ConditionID,
				City:        r.City,
				Signal:      r.Signal,
				Side:        r.Side,
				SizeUSDC:    r.SizeUSDC,
				OurProb:     r.OurProbability,
				PlacedAt:    r.Timestamp,
			})
		}
	}
	rpt.ResolvedBets = len(resolved)

	// Sort resolved by timestamp for curve building.
	sort.Slice(resolved, func(i, j int) bool {
		return resolved[i].Timestamp.Before(resolved[j].Timestamp)
	})

	// P&L curve and overall stats.
	var cumPnL float64
	var wins int
	var brierSum float64

	for _, r := range resolved {
		won := *r.Outcome
		var pnl float64
		if won {
			wins++
			// profit = size × (1/price - 1) on winning side
			if r.MarketPrice > 0 && r.MarketPrice < 1 {
				pnl = r.SizeUSDC * (1/r.MarketPrice - 1)
			} else {
				pnl = r.SizeUSDC
			}
		} else {
			pnl = -r.SizeUSDC
		}
		cumPnL += pnl
		rpt.PnLSeries = append(rpt.PnLSeries, pnlPoint{
			Date: r.Timestamp.Format("01-02"),
			PnL:  math.Round(cumPnL*100) / 100,
		})

		// Brier contribution.
		outcome := 0.0
		if won {
			outcome = 1.0
		}
		brierSum += math.Pow(r.OurProbability-outcome, 2)
	}

	rpt.TotalPnL = math.Round(cumPnL*100) / 100
	if rpt.ResolvedBets > 0 {
		rpt.WinRate = math.Round(float64(wins)/float64(rpt.ResolvedBets)*1000) / 10
		rpt.BrierScore = math.Round(brierSum/float64(rpt.ResolvedBets)*1000) / 1000
	}

	// Rolling Brier score (window = 10).
	window := 10
	for i := 0; i < len(resolved); i++ {
		start := i - window + 1
		if start < 0 {
			start = 0
		}
		var bs float64
		for _, r := range resolved[start : i+1] {
			outcome := 0.0
			if *r.Outcome {
				outcome = 1.0
			}
			bs += math.Pow(r.OurProbability-outcome, 2)
		}
		bs /= float64(i - start + 1)
		rpt.BrierSeries = append(rpt.BrierSeries, brierPoint{
			Label: fmt.Sprintf("#%d", i+1),
			Score: math.Round(bs*1000) / 1000,
		})
	}

	// Signal breakdown.
	sigMap := map[string]*signalStat{}
	for _, r := range resolved {
		sig := r.Signal
		if sig == "" {
			sig = "unknown"
		}
		s := sigMap[sig]
		if s == nil {
			s = &signalStat{Signal: sig}
			sigMap[sig] = s
		}
		s.Count++
		s.AvgEdge += math.Abs(r.OurProbability - r.MarketPrice)
		if *r.Outcome {
			s.Wins++
		}
	}
	for _, s := range sigMap {
		if s.Count > 0 {
			s.WinRate = math.Round(float64(s.Wins)/float64(s.Count)*1000) / 10
			s.AvgEdge = math.Round(s.AvgEdge/float64(s.Count)*1000) / 1000
		}
		rpt.SignalStats = append(rpt.SignalStats, *s)
	}
	sort.Slice(rpt.SignalStats, func(i, j int) bool {
		return rpt.SignalStats[i].Count > rpt.SignalStats[j].Count
	})

	// City breakdown.
	cityPnL := map[string]float64{}
	cityMap := map[string]*cityStat{}
	for _, r := range resolved {
		city := r.City
		if city == "" {
			city = "unknown"
		}
		c := cityMap[city]
		if c == nil {
			c = &cityStat{City: city}
			cityMap[city] = c
		}
		c.Count++
		if *r.Outcome {
			c.Wins++
			if r.MarketPrice > 0 && r.MarketPrice < 1 {
				cityPnL[city] += r.SizeUSDC * (1/r.MarketPrice - 1)
			} else {
				cityPnL[city] += r.SizeUSDC
			}
		} else {
			cityPnL[city] -= r.SizeUSDC
		}
	}
	for city, c := range cityMap {
		if c.Count > 0 {
			c.WinRate = math.Round(float64(c.Wins)/float64(c.Count)*1000) / 10
		}
		c.PnL = math.Round(cityPnL[city]*100) / 100
		rpt.CityStats = append(rpt.CityStats, *c)
	}
	sort.Slice(rpt.CityStats, func(i, j int) bool {
		return rpt.CityStats[i].Count > rpt.CityStats[j].Count
	})

	return rpt
}

func renderHTML(rpt report) (string, error) {
	dataJSON, err := json.Marshal(rpt)
	if err != nil {
		return "", err
	}

	// Build city table rows.
	var cityRows strings.Builder
	for _, c := range rpt.CityStats {
		pnlClass := "neutral"
		if c.PnL > 0 {
			pnlClass = "positive"
		} else if c.PnL < 0 {
			pnlClass = "negative"
		}
		fmt.Fprintf(&cityRows,
			`<tr><td>%s</td><td>%d</td><td>%d</td><td>%.1f%%</td><td class="%s">%.2f</td></tr>`,
			c.City, c.Count, c.Wins, c.WinRate, pnlClass, c.PnL,
		)
	}

	// Build open positions rows.
	var openRows strings.Builder
	for _, p := range rpt.OpenPositions {
		fmt.Fprintf(&openRows,
			`<tr><td>%s</td><td>%s</td><td>%s</td><td>%.2f</td><td>%.0f%%</td><td>%s</td></tr>`,
			p.City, p.Signal, p.Side, p.SizeUSDC,
			p.OurProb*100,
			p.PlacedAt.Format("01-02 15:04"),
		)
	}

	pnlClass := "neutral"
	if rpt.TotalPnL > 0 {
		pnlClass = "positive"
	} else if rpt.TotalPnL < 0 {
		pnlClass = "negative"
	}

	html := fmt.Sprintf(`<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>Polymarket Weather Bot — Performance Report</title>
<script src="https://cdn.jsdelivr.net/npm/chart.js@4.4.0/dist/chart.umd.min.js"></script>
<style>
  * { box-sizing: border-box; margin: 0; padding: 0; }
  body { font-family: system-ui, sans-serif; background: #0f1117; color: #e0e0e0; padding: 24px; }
  h1 { font-size: 1.4rem; margin-bottom: 4px; color: #fff; }
  .subtitle { color: #888; font-size: 0.85rem; margin-bottom: 24px; }
  .cards { display: flex; flex-wrap: wrap; gap: 16px; margin-bottom: 24px; }
  .card { background: #1a1d27; border: 1px solid #2a2d3a; border-radius: 10px; padding: 18px 22px; min-width: 140px; }
  .card .label { font-size: 0.75rem; color: #888; text-transform: uppercase; letter-spacing: 0.05em; }
  .card .value { font-size: 1.8rem; font-weight: 700; margin-top: 4px; color: #fff; }
  .card .value.positive { color: #4ade80; }
  .card .value.negative { color: #f87171; }
  .card .value.neutral  { color: #e0e0e0; }
  .charts { display: grid; grid-template-columns: 1fr 1fr; gap: 16px; margin-bottom: 24px; }
  .chart-box { background: #1a1d27; border: 1px solid #2a2d3a; border-radius: 10px; padding: 16px; }
  .chart-box h2 { font-size: 0.85rem; color: #aaa; margin-bottom: 12px; }
  canvas { max-height: 220px; }
  table { width: 100%%; border-collapse: collapse; font-size: 0.85rem; }
  th { text-align: left; color: #888; font-weight: 600; padding: 8px 12px; border-bottom: 1px solid #2a2d3a; }
  td { padding: 7px 12px; border-bottom: 1px solid #1e2130; }
  tr:last-child td { border-bottom: none; }
  td.positive { color: #4ade80; }
  td.negative { color: #f87171; }
  td.neutral   { color: #e0e0e0; }
  .section { background: #1a1d27; border: 1px solid #2a2d3a; border-radius: 10px; padding: 16px; margin-bottom: 16px; }
  .section h2 { font-size: 0.85rem; color: #aaa; margin-bottom: 12px; }
  @media (max-width: 700px) { .charts { grid-template-columns: 1fr; } }
</style>
</head>
<body>
<h1>⛅ Polymarket Weather Bot</h1>
<p class="subtitle">Performance Report · %s</p>

<div class="cards">
  <div class="card"><div class="label">Total Bets</div><div class="value">%d</div></div>
  <div class="card"><div class="label">Resolved</div><div class="value">%d</div></div>
  <div class="card"><div class="label">Open</div><div class="value">%d</div></div>
  <div class="card"><div class="label">Win Rate</div><div class="value">%.1f%%</div></div>
  <div class="card"><div class="label">Total P&L (USDC)</div><div class="value %s">%+.2f</div></div>
  <div class="card"><div class="label">Brier Score</div><div class="value">%.3f</div></div>
</div>

<div class="charts">
  <div class="chart-box"><h2>Cumulative P&L (USDC)</h2><canvas id="pnlChart"></canvas></div>
  <div class="chart-box"><h2>Win Rate by Signal</h2><canvas id="signalChart"></canvas></div>
  <div class="chart-box"><h2>Rolling Brier Score (window=10)</h2><canvas id="brierChart"></canvas></div>
  <div class="chart-box"><h2>Bet Count by Signal</h2><canvas id="countChart"></canvas></div>
</div>

<div class="section">
  <h2>City Breakdown</h2>
  <table>
    <tr><th>City</th><th>Bets</th><th>Wins</th><th>Win Rate</th><th>P&L (USDC)</th></tr>
    %s
  </table>
</div>

<div class="section">
  <h2>Open Positions (%d)</h2>
  <table>
    <tr><th>City</th><th>Signal</th><th>Side</th><th>Size</th><th>Our P</th><th>Placed</th></tr>
    %s
  </table>
</div>

<script>
const data = %s;

const chartDefaults = {
  responsive: true,
  plugins: { legend: { display: false } },
  scales: {
    x: { ticks: { color: '#888', font: { size: 10 } }, grid: { color: '#1e2130' } },
    y: { ticks: { color: '#888', font: { size: 10 } }, grid: { color: '#1e2130' } },
  }
};

// P&L Curve
new Chart(document.getElementById('pnlChart'), {
  type: 'line',
  data: {
    labels: data.pnl_series.map(p => p.date),
    datasets: [{
      data: data.pnl_series.map(p => p.pnl),
      borderColor: '#60a5fa',
      backgroundColor: 'rgba(96,165,250,0.1)',
      fill: true,
      tension: 0.3,
      pointRadius: 0,
    }]
  },
  options: chartDefaults,
});

// Signal Win Rate
new Chart(document.getElementById('signalChart'), {
  type: 'bar',
  data: {
    labels: data.signal_stats.map(s => s.signal),
    datasets: [{
      data: data.signal_stats.map(s => s.win_rate),
      backgroundColor: data.signal_stats.map(s => s.win_rate >= 50 ? '#4ade80' : '#f87171'),
    }]
  },
  options: {
    ...chartDefaults,
    scales: {
      ...chartDefaults.scales,
      y: { ...chartDefaults.scales.y, max: 100, ticks: { ...chartDefaults.scales.y.ticks, callback: v => v + '%%' } }
    }
  }
});

// Rolling Brier
new Chart(document.getElementById('brierChart'), {
  type: 'line',
  data: {
    labels: data.brier_series.map(b => b.label),
    datasets: [{
      data: data.brier_series.map(b => b.score),
      borderColor: '#f59e0b',
      backgroundColor: 'rgba(245,158,11,0.1)',
      fill: true,
      tension: 0.3,
      pointRadius: 0,
    }]
  },
  options: chartDefaults,
});

// Bet Count
new Chart(document.getElementById('countChart'), {
  type: 'bar',
  data: {
    labels: data.signal_stats.map(s => s.signal),
    datasets: [{
      data: data.signal_stats.map(s => s.count),
      backgroundColor: '#818cf8',
    }]
  },
  options: chartDefaults,
});
</script>
</body>
</html>`,
		rpt.GeneratedAt,
		rpt.TotalBets,
		rpt.ResolvedBets,
		rpt.OpenBets,
		rpt.WinRate,
		pnlClass, rpt.TotalPnL,
		rpt.BrierScore,
		cityRows.String(),
		rpt.OpenBets,
		openRows.String(),
		string(dataJSON),
	)
	return html, nil
}
