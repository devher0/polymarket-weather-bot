// Package strategy evaluates edge and computes bet sizing.
package strategy

import (
	"fmt"
	"math"
	"strings"

	"github.com/devher0/polymarket-weather-bot/internal/collectors"
	"github.com/devher0/polymarket-weather-bot/internal/markets"
	"github.com/devher0/polymarket-weather-bot/internal/weather"
)

// Decision is a bet recommendation.
type Decision struct {
	Market         markets.Market
	Side           string  // "YES" or "NO"
	TokenID        string
	OurProbability float64
	MarketPrice    float64
	Edge           float64
	SizeUSDC       float64
	Reason         string
}

// halfKelly returns the half-Kelly bet size given edge and decimal odds.
func halfKelly(edge, odds, bankroll, maxFraction float64) float64 {
	if edge <= 0 {
		return 0
	}
	b := odds - 1
	p := edge + 1/odds
	q := 1 - p
	k := (b*p - q) / b
	frac := math.Min(maxFraction, math.Max(0, k/2))
	return frac * bankroll
}

// minConfidence is the threshold below which we skip the market.
const minConfidence = 0.4

// EvaluateFused evaluates a market using a FusedForecast from the aggregator.
// Returns nil when confidence is too low or there is no sufficient edge.
func EvaluateFused(
	m markets.Market,
	ff *collectors.FusedForecast,
	bankroll float64,
	minEdge float64,
	maxBet float64,
) *Decision {
	if ff == nil {
		return nil
	}

	// Confidence gate: skip if sources disagree too much
	if ff.Confidence < minConfidence {
		return nil
	}

	return evaluate(m, ff.Forecast, bankroll, minEdge, maxBet,
		fmt.Sprintf("sources=[%s] confidence=%.2f", strings.Join(ff.Sources, ","), ff.Confidence))
}

// Evaluate compares our weather forecast to a Polymarket price.
// Returns a Decision when edge ≥ minEdge, otherwise nil.
// Deprecated: prefer EvaluateFused when aggregator data is available.
func Evaluate(
	m markets.Market,
	forecasts map[string][]weather.Forecast,
	bankroll float64,
	minEdge float64,
	maxBet float64,
) *Decision {
	if m.City == "" {
		return nil
	}
	flist, ok := forecasts[m.City]
	if !ok || len(flist) == 0 {
		return nil
	}
	return evaluate(m, flist[0], bankroll, minEdge, maxBet, "source=openmeteo")
}

// evaluate is the core logic shared by Evaluate and EvaluateFused.
func evaluate(
	m markets.Market,
	f weather.Forecast,
	bankroll float64,
	minEdge float64,
	maxBet float64,
	sourceNote string,
) *Decision {
	if m.City == "" {
		return nil
	}

	var ourP float64
	switch m.Signal {
	case "rain":
		ourP = weather.RainProbability(f)
	case "heat":
		ourP = weather.HeatProbability(f, 35.0)
	case "cold":
		ourP = 1 - weather.HeatProbability(f, 10.0)
	case "snow":
		coldP := 1 - weather.HeatProbability(f, 2.0)
		ourP = coldP * weather.RainProbability(f) * 0.8
	case "wind":
		ourP = math.Min(0.95, f.WindSpeedKMH/80.0)
	default:
		return nil
	}

	yesEdge := ourP - m.YesPrice
	noEdge := (1 - ourP) - m.NoPrice

	baseReason := fmt.Sprintf("%s/%s: our=%.2f mkt=%.2f edge=%+.2f [%s]",
		m.City, m.Signal, ourP, 0.0, 0.0, sourceNote)

	if yesEdge >= noEdge && math.Abs(yesEdge) >= minEdge {
		size := halfKelly(yesEdge, 1/m.YesPrice, bankroll, 0.05)
		size = math.Min(size, maxBet)
		if size < 0.5 {
			return nil
		}
		return &Decision{
			Market:         m,
			Side:           "YES",
			TokenID:        m.YesTokenID,
			OurProbability: ourP,
			MarketPrice:    m.YesPrice,
			Edge:           yesEdge,
			SizeUSDC:       size,
			Reason: fmt.Sprintf("%s/%s: our=%.2f mkt=%.2f edge=%+.2f [%s]",
				m.City, m.Signal, ourP, m.YesPrice, yesEdge, sourceNote),
		}
	} else if noEdge > yesEdge && math.Abs(noEdge) >= minEdge {
		_ = baseReason
		size := halfKelly(noEdge, 1/m.NoPrice, bankroll, 0.05)
		size = math.Min(size, maxBet)
		if size < 0.5 {
			return nil
		}
		return &Decision{
			Market:         m,
			Side:           "NO",
			TokenID:        m.NoTokenID,
			OurProbability: ourP,
			MarketPrice:    m.NoPrice,
			Edge:           noEdge,
			SizeUSDC:       size,
			Reason: fmt.Sprintf("%s/%s: our=%.2f mkt=%.2f edge=%+.2f [%s]",
				m.City, m.Signal, ourP, m.NoPrice, noEdge, sourceNote),
		}
	}

	return nil
}
