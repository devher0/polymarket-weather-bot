// Package config loads bot configuration from config.yaml with ENV override.
//
// Priority (highest wins):
//  1. Environment variables (for secrets and CI/CD overrides)
//  2. config.yaml (or the file specified by --config flag / CONFIG_FILE env)
//  3. Built-in defaults
//
// Example usage:
//
//	cfg, err := config.Load("config/config.yaml")
//	if err != nil { log.Fatal(err) }
//	fmt.Println(cfg.MinEdge)
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// CityEntry allows specifying a city with custom lat/lon in config.yaml.
// Example:
//
//	city_defs:
//	  - name: dubai
//	    lat: 25.20
//	    lon: 55.27
//
// When city_defs is non-empty, each entry is registered in weather.Cities so
// the rest of the pipeline can fetch forecasts for it.
type CityEntry struct {
	Name string  `yaml:"name"`
	Lat  float64 `yaml:"lat"`
	Lon  float64 `yaml:"lon"`
}

// Config holds all bot configuration.
type Config struct {
	// Trading parameters
	Cities    []string    `yaml:"cities"`     // list of city slugs to trade (default: all)
	CityDefs  []CityEntry `yaml:"city_defs"`  // optional custom city definitions with lat/lon
	MinEdge float64  `yaml:"min_edge"` // minimum edge to place a bet (0.05 = 5%)
	MaxBet  float64  `yaml:"max_bet"`  // max bet per market in USDC

	// Loop / scheduling
	LoopSec int `yaml:"loop_sec"` // run interval in seconds (0 = run once)

	// Infrastructure
	MetricsPort int    `yaml:"metrics_port"` // Prometheus metrics port (0 = disabled)
	LogLevel    string `yaml:"log_level"`    // "debug" | "info" | "warn" | "error"
	DataRoot    string `yaml:"data_root"`    // root dir for data/ files (default ".")

	// Risk management
	MaxDailyLossUSDC      float64 `yaml:"max_daily_loss_usdc"`        // stop if today's P&L < -this (0 = disabled)
	MaxDailyProfitUSDC    float64 `yaml:"max_daily_profit_usdc"`      // stop if today's P&L > this (0 = disabled)
	MaxDailyBets          int     `yaml:"max_daily_bets"`             // max bets per UTC day (0 = disabled)
	MaxOpenPositions      int     `yaml:"max_open_positions"`         // max unresolved bets at once (0 = disabled)
	MaxSameCitySignalBets int     `yaml:"max_same_city_signal_bets"`  // max open bets on same (city,signal) pair (0 = disabled)
	LossBlacklistDays    int     `yaml:"loss_blacklist_days"`         // days to blacklist a market after a loss (0 = disabled)
	MaxDrawdownFraction  float64 `yaml:"max_drawdown_fraction"`       // circuit-breaker: reduce bet size when drawdown > this fraction (0 = disabled)
	MaxExposureUSDC      float64 `yaml:"max_exposure_usdc"`           // hard cap on total USDC at risk across all open positions (0 = disabled)

	// Forecast quality
	MaxForecastAgeHours float64 `yaml:"max_forecast_age_hours"` // skip bets on forecasts older than this (0 = disabled)
	MaxBetsPerCycle     int     `yaml:"max_bets_per_cycle"`     // max bets per single run loop (0 = unlimited)
	MinHoursToExpiry    float64 `yaml:"min_hours_to_expiry"`    // skip markets closing within this many hours (0 = disabled)

	// Kelly sizing (TASK-080)
	KellyFraction    float64 `yaml:"kelly_fraction"`     // fraction of full Kelly (0.25=quarter, 0.5=half, 1.0=full; default 0.5)
	MaxKellyFraction float64 `yaml:"max_kelly_fraction"` // hard cap on bankroll fraction per bet (default 0.05 = 5%)

	// Protocol fee (TASK-141): Polymarket charges this fraction of net profit on wins.
	// Kelly formula uses fee-adjusted odds so sizing accounts for the real payout.
	ProtocolFeeRate float64 `yaml:"protocol_fee_rate"` // default 0.02 (2%)

	// Auto-blacklist (TASK-131): automatically suppress (city, signal) pairs that
	// show systematic losses. 0/default values use the AutoBlacklistCfg defaults.
	AutoBlacklistMinBets    int     `yaml:"auto_blacklist_min_bets"`    // min resolved bets before check (default 8)
	AutoBlacklistLossUSDC   float64 `yaml:"auto_blacklist_loss_usdc"`   // cumulative loss threshold, negative (default -3.0)
	AutoBlacklistDays       int     `yaml:"auto_blacklist_days"`        // days to suppress the pair (default 3)

	// Rolling win-rate alert (TASK-132): Telegram warning when rolling win rate
	// falls below threshold. 0 = disabled.
	RollingWinRateWindow    int     `yaml:"rolling_winrate_window"`    // rolling window size (default 20)
	RollingWinRateThreshold float64 `yaml:"rolling_winrate_threshold"` // alert threshold (default 0.35)

	// Anti-detection / rate limiting
	BetJitterEnabled    bool    `yaml:"bet_jitter_enabled"`     // sleep 30s–3min before each live bet (default: true)
	MinBetIntervalHours float64 `yaml:"min_bet_interval_hours"` // cooldown per conditionID in hours (default: 4.0)

	// Polymarket CLOB credentials (usually via ENV, not yaml)
	PolyPrivateKey   string `yaml:"poly_private_key"`
	PolyAddress      string `yaml:"poly_address"`
	PolyAPIKey       string `yaml:"poly_api_key"`
	PolyAPISecret    string `yaml:"poly_api_secret"`
	PolyAPIPassphrase string `yaml:"poly_api_passphrase"`

	// Telegram notifications
	TelegramBotToken string `yaml:"telegram_bot_token"`
	TelegramChatID   string `yaml:"telegram_chat_id"`

	// Webhook notifications (TASK-046)
	WebhookURL string `yaml:"webhook_url"` // POST JSON events to this URL (empty = disabled)

	// Per-signal minimum edge overrides (TASK-118).
	// When a signal is listed here its threshold replaces MinEdge.
	// Example: signal_min_edge: {rain: 0.06, heat: 0.04, snow: 0.08}
	SignalMinEdge map[string]float64 `yaml:"signal_min_edge"`
}

// defaults returns a Config with sensible built-in defaults.
func defaults() Config {
	return Config{
		Cities: []string{
			"new_york", "london", "tokyo", "miami", "paris",
			"chicago", "los_angeles", "san_francisco", "berlin",
		},
		MinEdge:          0.05,
		MaxBet:           10.0,
		LoopSec:          0,
		MetricsPort:      9090,
		LogLevel:         "info",
		DataRoot:         ".",
		MaxDailyLossUSDC:      50.0,
		MaxDailyBets:          20,
		MaxOpenPositions:      30,
		MaxSameCitySignalBets: 2,
		LossBlacklistDays:    5,
		MaxDrawdownFraction:  0.30,
		MaxForecastAgeHours:   3.0,
		MaxBetsPerCycle:     5,
		MinHoursToExpiry:    6.0,
		KellyFraction:           0.5,
		MaxKellyFraction:        0.05,
		ProtocolFeeRate:         0.02,
		BetJitterEnabled:        true,
		MinBetIntervalHours:     4.0,
		AutoBlacklistMinBets:    8,
		AutoBlacklistLossUSDC:   -3.0,
		AutoBlacklistDays:       3,
		RollingWinRateWindow:    20,
		RollingWinRateThreshold: 0.35,
	}
}

// Load reads configuration from the given YAML file (may be empty string to
// skip file loading), then overlays environment-variable overrides.
// It never returns nil — if the file is missing it uses defaults + ENV.
func Load(path string) (*Config, error) {
	cfg := defaults()

	if path != "" {
		data, err := os.ReadFile(path)
		if err != nil {
			if !os.IsNotExist(err) {
				return nil, fmt.Errorf("config: read %s: %w", path, err)
			}
			// File doesn't exist — that's fine, use defaults.
		} else {
			if err := yaml.Unmarshal(data, &cfg); err != nil {
				return nil, fmt.Errorf("config: parse %s: %w", path, err)
			}
		}
	}

	// ENV overlay — environment variables win over file values.
	applyEnv(&cfg)

	return &cfg, nil
}

// LoadDefault loads from CONFIG_FILE env var, then "config/config.yaml",
// falling back to defaults if neither exists.
func LoadDefault() (*Config, error) {
	path := os.Getenv("CONFIG_FILE")
	if path == "" {
		path = "config/config.yaml"
	}
	return Load(path)
}

// applyEnv overlays environment variables onto the config.
// ENV variable names mirror the YAML keys in SCREAMING_SNAKE_CASE.
func applyEnv(cfg *Config) {
	// Trading
	if v := os.Getenv("CITIES"); v != "" {
		parts := strings.Split(v, ",")
		cities := make([]string, 0, len(parts))
		for _, p := range parts {
			if s := strings.TrimSpace(p); s != "" {
				cities = append(cities, s)
			}
		}
		if len(cities) > 0 {
			cfg.Cities = cities
		}
	}
	if v := envFloat("MIN_EDGE"); v != nil {
		cfg.MinEdge = *v
	}
	if v := envFloat("MAX_BET_USDC"); v != nil {
		cfg.MaxBet = *v
	}

	// Loop / scheduling
	if v := envInt("LOOP_SEC"); v != nil {
		cfg.LoopSec = *v
	}

	// Infrastructure
	if v := envInt("METRICS_PORT"); v != nil {
		cfg.MetricsPort = *v
	}
	if v := os.Getenv("LOG_LEVEL"); v != "" {
		cfg.LogLevel = v
	}
	if v := os.Getenv("DATA_ROOT"); v != "" {
		cfg.DataRoot = v
	}

	// Polymarket credentials
	if v := os.Getenv("POLYMARKET_PRIVATE_KEY"); v != "" {
		cfg.PolyPrivateKey = v
	}
	if v := os.Getenv("POLYMARKET_ADDRESS"); v != "" {
		cfg.PolyAddress = v
	}
	if v := os.Getenv("POLYMARKET_API_KEY"); v != "" {
		cfg.PolyAPIKey = v
	}
	if v := os.Getenv("POLYMARKET_API_SECRET"); v != "" {
		cfg.PolyAPISecret = v
	}
	if v := os.Getenv("POLYMARKET_API_PASSPHRASE"); v != "" {
		cfg.PolyAPIPassphrase = v
	}

	// Risk management
	if v := envFloat("MAX_DAILY_LOSS_USDC"); v != nil {
		cfg.MaxDailyLossUSDC = *v
	}
	if v := envFloat("MAX_DAILY_PROFIT_USDC"); v != nil {
		cfg.MaxDailyProfitUSDC = *v
	}
	if v := envInt("MAX_DAILY_BETS"); v != nil {
		cfg.MaxDailyBets = *v
	}
	if v := envInt("MAX_OPEN_POSITIONS"); v != nil {
		cfg.MaxOpenPositions = *v
	}
	if v := envInt("MAX_SAME_CITY_SIGNAL_BETS"); v != nil {
		cfg.MaxSameCitySignalBets = *v
	}
	if v := envInt("LOSS_BLACKLIST_DAYS"); v != nil {
		cfg.LossBlacklistDays = *v
	}
	if v := envFloat("MAX_DRAWDOWN_FRACTION"); v != nil {
		cfg.MaxDrawdownFraction = *v
	}
	if v := envFloat("MAX_EXPOSURE_USDC"); v != nil {
		cfg.MaxExposureUSDC = *v
	}

	// Forecast quality
	if v := envFloat("MAX_FORECAST_AGE_HOURS"); v != nil {
		cfg.MaxForecastAgeHours = *v
	}
	if v := envInt("MAX_BETS_PER_CYCLE"); v != nil {
		cfg.MaxBetsPerCycle = *v
	}
	if v := envFloat("MIN_HOURS_TO_EXPIRY"); v != nil {
		cfg.MinHoursToExpiry = *v
	}

	// Kelly sizing (TASK-080)
	if v := envFloat("KELLY_FRACTION"); v != nil {
		cfg.KellyFraction = *v
	}
	if v := envFloat("MAX_KELLY_FRACTION"); v != nil {
		cfg.MaxKellyFraction = *v
	}
	// Protocol fee (TASK-141)
	if v := envFloat("PROTOCOL_FEE_RATE"); v != nil {
		cfg.ProtocolFeeRate = *v
	}

	// Anti-detection / rate limiting
	if v := os.Getenv("BET_JITTER_ENABLED"); v != "" {
		cfg.BetJitterEnabled = v == "true" || v == "1"
	}
	if v := envFloat("MIN_BET_INTERVAL_HOURS"); v != nil {
		cfg.MinBetIntervalHours = *v
	}

	// Telegram
	if v := os.Getenv("TELEGRAM_BOT_TOKEN"); v != "" {
		cfg.TelegramBotToken = v
	}
	if v := os.Getenv("TELEGRAM_CHAT_ID"); v != "" {
		cfg.TelegramChatID = v
	}

	// Webhook (TASK-046)
	if v := os.Getenv("WEBHOOK_URL"); v != "" {
		cfg.WebhookURL = v
	}
}

// GetMinEdgeForSignal returns the signal-specific minimum edge when configured,
// falling back to cfg.MinEdge for unknown signals (TASK-118).
func GetMinEdgeForSignal(cfg *Config, signal string) float64 {
	if cfg.SignalMinEdge != nil {
		if v, ok := cfg.SignalMinEdge[signal]; ok {
			return v
		}
	}
	return cfg.MinEdge
}

// envFloat returns a pointer to the parsed float64, or nil if the env var is
// unset or unparseable.
func envFloat(key string) *float64 {
	v := os.Getenv(key)
	if v == "" {
		return nil
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return nil
	}
	return &f
}

// envInt returns a pointer to the parsed int, or nil.
func envInt(key string) *int {
	v := os.Getenv(key)
	if v == "" {
		return nil
	}
	i, err := strconv.Atoi(v)
	if err != nil {
		return nil
	}
	return &i
}

// ValidationResult holds the result of a config validation pass.
type ValidationResult struct {
	Errors   []string // fatal: bot should not start
	Warnings []string // non-fatal: logged but bot continues
}

// Validate checks the config for invalid or suspicious values.
// Returns errors (must fix) and warnings (advisories only).
// Call at startup: if len(r.Errors) > 0 → exit(1).
//
// TASK-082: Config validation + sanitization.
func Validate(cfg *Config) ValidationResult {
	var r ValidationResult

	// ── Fatal errors ────────────────────────────────────────────────────────────

	if len(cfg.Cities) == 0 {
		r.Errors = append(r.Errors, "cities: list is empty — bot has no cities to trade")
	}

	if cfg.MinEdge <= 0 {
		r.Errors = append(r.Errors, fmt.Sprintf("min_edge: must be > 0, got %.4f", cfg.MinEdge))
	} else if cfg.MinEdge > 0.50 {
		r.Errors = append(r.Errors, fmt.Sprintf("min_edge: %.2f exceeds 0.50 — no market will ever meet this threshold", cfg.MinEdge))
	}

	if cfg.MaxBet <= 0 {
		r.Errors = append(r.Errors, fmt.Sprintf("max_bet: must be > 0, got %.4f", cfg.MaxBet))
	}

	if cfg.KellyFraction <= 0 || cfg.KellyFraction > 1.0 {
		r.Errors = append(r.Errors, fmt.Sprintf("kelly_fraction: must be in (0, 1.0], got %.2f", cfg.KellyFraction))
	}

	if cfg.MaxKellyFraction <= 0 || cfg.MaxKellyFraction > 1.0 {
		r.Errors = append(r.Errors, fmt.Sprintf("max_kelly_fraction: must be in (0, 1.0], got %.4f", cfg.MaxKellyFraction))
	}

	// ── Warnings ────────────────────────────────────────────────────────────────

	if cfg.MinEdge < 0.03 {
		r.Warnings = append(r.Warnings, fmt.Sprintf("min_edge=%.3f is very aggressive (< 3%%) — expect many false positives and lower average P&L", cfg.MinEdge))
	}

	if cfg.MaxBet > 100 {
		r.Warnings = append(r.Warnings, fmt.Sprintf("max_bet=%.0f USDC is large — ensure bankroll supports this bet size", cfg.MaxBet))
	}

	if cfg.KellyFraction > 0.75 {
		r.Warnings = append(r.Warnings, fmt.Sprintf("kelly_fraction=%.2f is aggressive (> 0.75) — consider half-Kelly (0.50) for lower variance", cfg.KellyFraction))
	}

	if cfg.MaxKellyFraction > 0.15 {
		r.Warnings = append(r.Warnings, fmt.Sprintf("max_kelly_fraction=%.2f caps at %.0f%% of bankroll per bet — very high single-bet exposure", cfg.MaxKellyFraction, cfg.MaxKellyFraction*100))
	}

	if cfg.LoopSec > 0 && cfg.LoopSec < 60 {
		r.Warnings = append(r.Warnings, fmt.Sprintf("loop_sec=%d is very tight — risk of API rate-limiting; recommend ≥60s", cfg.LoopSec))
	}

	if cfg.MaxForecastAgeHours > 0 && cfg.MaxForecastAgeHours > 6 {
		r.Warnings = append(r.Warnings, fmt.Sprintf("max_forecast_age_hours=%.1f is high — forecasts >6h old may be significantly stale", cfg.MaxForecastAgeHours))
	}

	if cfg.MaxDailyLossUSDC > 0 && cfg.MaxDailyLossUSDC > 200 {
		r.Warnings = append(r.Warnings, fmt.Sprintf("max_daily_loss_usdc=%.0f is high — this is a large daily risk tolerance", cfg.MaxDailyLossUSDC))
	}

	return r
}
