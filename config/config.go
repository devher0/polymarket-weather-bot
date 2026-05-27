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

// Config holds all bot configuration.
type Config struct {
	// Trading parameters
	Cities  []string `yaml:"cities"`   // list of city slugs to trade (default: all)
	MinEdge float64  `yaml:"min_edge"` // minimum edge to place a bet (0.05 = 5%)
	MaxBet  float64  `yaml:"max_bet"`  // max bet per market in USDC

	// Loop / scheduling
	LoopSec int `yaml:"loop_sec"` // run interval in seconds (0 = run once)

	// Infrastructure
	MetricsPort int    `yaml:"metrics_port"` // Prometheus metrics port (0 = disabled)
	LogLevel    string `yaml:"log_level"`    // "debug" | "info" | "warn" | "error"
	DataRoot    string `yaml:"data_root"`    // root dir for data/ files (default ".")

	// Risk management
	MaxDailyLossUSDC   float64 `yaml:"max_daily_loss_usdc"`   // stop if today's P&L < -this (0 = disabled)
	MaxDailyProfitUSDC float64 `yaml:"max_daily_profit_usdc"` // stop if today's P&L > this (0 = disabled)
	MaxDailyBets       int     `yaml:"max_daily_bets"`        // max bets per UTC day (0 = disabled)
	MaxOpenPositions   int     `yaml:"max_open_positions"`    // max unresolved bets at once (0 = disabled)

	// Forecast quality
	MaxForecastAgeHours float64 `yaml:"max_forecast_age_hours"` // skip bets on forecasts older than this (0 = disabled)
	MaxBetsPerCycle     int     `yaml:"max_bets_per_cycle"`     // max bets per single run loop (0 = unlimited)
	MinHoursToExpiry    float64 `yaml:"min_hours_to_expiry"`    // skip markets closing within this many hours (0 = disabled)

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
		MaxDailyLossUSDC:    50.0,
		MaxDailyBets:        20,
		MaxOpenPositions:    30,
		MaxForecastAgeHours: 3.0,
		MaxBetsPerCycle:     5,
		MinHoursToExpiry:    6.0,
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
