// exit_signal.go — TASK-188: market exit signal based on forecast drift.
//
// When our probability estimate for an open position changes significantly
// from the entry estimate (stored in BetRecord.OurProbability), it may be
// worth selling the position rather than holding to resolution.
//
// The forecast map is keyed by conditionID → latest ourP drawn from the
// prediction log or live evaluation.  Only open (unresolved) bets are examined.
package calibration

// ExitSignal holds the exit analysis for a single open position.
type ExitSignal struct {
	ConditionID    string
	Side           string  // "YES" or "NO"
	EntryP         float64 // OurProbability at bet time
	CurrentP       float64 // latest ourP from forecasts map
	Delta          float64 // CurrentP - EntryP (positive = improved, negative = deteriorated)
	CurrentMktPrice float64 // latest market price for the bet side (0 if unknown)
	// SuggestedAction is one of:
	//   "SELL"            — forecast worsened > 0.20 from entry; exit position
	//   "HOLD/REDUCE_SIZE" — forecast improved > 0.15; consider partial take-profit
	//   "HOLD"            — no significant change
	SuggestedAction string
}

const (
	exitSellThreshold   = -0.20 // delta < -0.20 → SELL
	exitReduceThreshold = +0.15 // delta > +0.15 → HOLD/REDUCE_SIZE
)

// ComputeExitSignals evaluates each open bet against the current forecast.
//
// forecasts maps conditionID → current estimated probability (ourP) from the
// latest prediction log or live strategy evaluation.  Bets whose conditionID
// is not in forecasts are included with CurrentP == EntryP (delta 0, HOLD).
//
// mktPrices (optional, may be nil) maps conditionID → current market price for
// the bet side, used to populate ExitSignal.CurrentMktPrice.
func ComputeExitSignals(openBets []BetRecord, forecasts map[string]float64, mktPrices map[string]float64) []ExitSignal {
	var out []ExitSignal
	for _, r := range openBets {
		if r.Outcome != nil {
			continue // already resolved
		}
		currentP := r.OurProbability // fallback: treat as unchanged
		if p, ok := forecasts[r.ConditionID]; ok {
			currentP = p
		}
		delta := currentP - r.OurProbability

		action := "HOLD"
		switch {
		case delta < exitSellThreshold:
			action = "SELL"
		case delta > exitReduceThreshold:
			action = "HOLD/REDUCE_SIZE"
		}

		sig := ExitSignal{
			ConditionID:    r.ConditionID,
			Side:           r.Side,
			EntryP:         r.OurProbability,
			CurrentP:       currentP,
			Delta:          delta,
			SuggestedAction: action,
		}
		if mktPrices != nil {
			sig.CurrentMktPrice = mktPrices[r.ConditionID]
		}
		out = append(out, sig)
	}
	return out
}

// SellSignals filters exit signals to only those recommending "SELL".
func SellSignals(signals []ExitSignal) []ExitSignal {
	var out []ExitSignal
	for _, s := range signals {
		if s.SuggestedAction == "SELL" {
			out = append(out, s)
		}
	}
	return out
}
