# Polymarket Weather Bot — Makefile
# ─────────────────────────────────────────────────────────────────────────────
.PHONY: build run live backtest dashboard history test lint docker docker-run clean help

BINARY     := bot
BACKTEST   := backtest
DASHBOARD  := dashboard
IMAGE      := polymarket-weather-bot
DATA_DIR   := ./data

## build: compile all binaries
build:
	@echo "→ building..."
	go build -trimpath ./cmd/bot ./cmd/backtest ./cmd/dashboard
	@echo "✓ built: bot, backtest, dashboard"

## run: dry-run (no real orders)
run: build
	@echo "→ dry run..."
	./${BINARY}

## live: real money mode (requires POLYMARKET_PRIVATE_KEY)
live: build
	@echo "⚠️  LIVE MODE — real money!"
	./${BINARY} --live

## loop: dry-run loop, check every hour
loop: build
	./${BINARY} --loop 3600

## live-loop: live mode with hourly loop
live-loop: build
	./${BINARY} --live --loop 3600

## history: download 90-day historical weather data
history: build
	@echo "→ collecting historical data..."
	./${BINARY} --collect-history

## backtest: run backtest over historical data
backtest: build
	@echo "→ running backtest..."
	./${BACKTEST} --verbose

## backtest-30: backtest with 30-day window
backtest-30: build
	./${BACKTEST} --days 30 --verbose

## dashboard: show all dashboard panels
dashboard: build
	./${DASHBOARD} all

## positions: show open positions
positions: build
	./${DASHBOARD} positions

## pnl: show P&L history
pnl: build
	./${DASHBOARD} pnl

## next: show top-5 bet candidates
next: build
	./${DASHBOARD} next

## test: run all tests
test:
	go test ./... -v -count=1

## test-short: run tests without integration (fast)
test-short:
	go test ./... -short -count=1

## lint: run golangci-lint (install separately)
lint:
	@which golangci-lint > /dev/null || (echo "golangci-lint not found; run: brew install golangci-lint" && exit 1)
	golangci-lint run ./...

## vet: go vet
vet:
	go vet ./...

## tidy: tidy go modules
tidy:
	go mod tidy

## docker: build Docker image
docker:
	docker build -t ${IMAGE}:latest .

## docker-run: run bot in Docker (dry-run, requires .env)
docker-run: docker
	docker run --rm --env-file .env -v $(PWD)/data:/app/data ${IMAGE}:latest

## docker-live: run bot in Docker (live mode)
docker-live: docker
	docker run --rm --env-file .env -v $(PWD)/data:/app/data ${IMAGE}:latest --live

## docker-backtest: run backtest in Docker
docker-backtest: docker
	docker run --rm --env-file .env -v $(PWD)/data:/app/data \
		--entrypoint ./backtest ${IMAGE}:latest --verbose

## clean: remove built binaries
clean:
	rm -f ${BINARY} ${BACKTEST} ${DASHBOARD}
	@echo "✓ cleaned"

## help: show this help
help:
	@grep -E '^## [a-z]' Makefile | sed 's/## /  make /g'
