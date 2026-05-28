// Package risk — per-position stop-loss guard (TASK-225).
//
// CheckStopLoss monitors open positions and returns true when a position's
// unrealized loss exceeds the configured threshold. The bot main loop uses
// this to emit a Telegram alert so the operator can decide to exit manually.
//
// Polymarket binary markets do not support programmatic limit orders from this
// bot, so the stop-loss is advisory rather than automatic order submission.
package risk

import (
	"log/slog"

	"github.com/devher0/polymarket-weather-bot/internal/calibration"
)

// StopLossConfig holds stop-loss parameters.
type StopLossConfig struct {
	// Enabled toggles stop-loss checking. When false, CheckStopLoss always
	// returns false regardless of other fields.
	Enabled bool

	// MaxLossPct is the maximum acceptable unrealized loss as a fraction of the
	// original bet size (e.g. 0.50 = 50%). When the position has lost more than
	// this fraction of its entry value, CheckStopLoss returns true.
	//
	// A value of 0.50 means: if you bet 10 USDC and the position is currently
	// worth less than 5 USDC, stop-loss is triggered.
	MaxLossPct float64
}

// DefaultStopLossConfig returns a conservative default: enabled with 50% loss cap.
func DefaultStopLossConfig() StopLossConfig {
	return StopLossConfig{
		Enabled:    false, // opt-in; user must enable explicitly
		MaxLossPct: 0.50,
	}
}

// StopLossResult describes a single stop-loss evaluation.
type StopLossResult struct {
	ConditionID  string
	Side         string
	EntryPrice   float64 // MarketPrice at bet time
	CurrentPrice float64 // latest fetched price
	LossFraction float64 // (entryPrice - currentPrice) / entryPrice, positive = loss
	Triggered    bool
}

// CheckStopLoss evaluates whether an open position's unrealized loss has
// exceeded the threshold defined in cfg.
//
// Returns (result, true) if the stop-loss fires, (result, false) otherwise.
// When cfg.Enabled is false or cfg.MaxLossPct is 0, always returns false.
//
// The currentPrice argument is the up-to-date market price for the position's
// side (YES or NO). It is the caller's responsibility to fetch it (e.g. via
// calibration.FetchUnrealizedPnL).
func CheckStopLoss(rec calibration.BetRecord, currentPrice float64, cfg StopLossConfig) (StopLossResult, bool) {
	res := StopLossResult{
		ConditionID:  rec.ConditionID,
		Side:         rec.Side,
		EntryPrice:   rec.MarketPrice,
		CurrentPrice: currentPrice,
	}

	if !cfg.Enabled || cfg.MaxLossPct <= 0 {
		return res, false
	}

	// Already resolved — no stop-loss needed.
	if rec.Outcome != nil {
		return res, false
	}

	// If we have no entry price we cannot compute a loss fraction.
	if rec.MarketPrice <= 0 {
		return res, false
	}

	// Loss fraction: how much of the entry price has been lost.
	// Positive value = loss; negative = gain.
	lossFraction := (rec.MarketPrice - currentPrice) / rec.MarketPrice
	res.LossFraction = lossFraction

	if lossFraction >= cfg.MaxLossPct {
		res.Triggered = true
		slog.Warn("stop-loss triggered",
			"condition_id", rec.ConditionID,
			"side", rec.Side,
			"entry", rec.MarketPrice,
			"current", currentPrice,
			"loss_pct", lossFraction*100,
			"threshold_pct", cfg.MaxLossPct*100,
		)
	}

	return res, res.Triggered
}

// ScanStopLosses checks all open positions in records against the given config
// and returns all positions where stop-loss was triggered.
//
// positions must be the result of calibration.FetchUnrealizedPnL — it provides
// the up-to-date currentPrice for each open bet.
func ScanStopLosses(positions []calibration.UnrealizedPosition, cfg StopLossConfig) []StopLossResult {
	if !cfg.Enabled || cfg.MaxLossPct <= 0 {
		return nil
	}

	var triggered []StopLossResult
	for _, p := range positions {
		if p.FetchError != "" {
			// Cannot evaluate without a current price.
			continue
		}
		res, fired := CheckStopLoss(p.BetRecord, p.CurrentPrice, cfg)
		if fired {
			triggered = append(triggered, res)
		}
	}
	return triggered
}
