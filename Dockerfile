# ── Stage 1: builder ─────────────────────────────────────────────────────────
FROM golang:1.23-alpine AS builder

WORKDIR /build

# Install C compiler for CGO dependencies (go-ethereum uses secp256k1 with C)
RUN apk add --no-cache gcc musl-dev git

# Cache module downloads separately from source
COPY go.mod go.sum ./
RUN go mod download

# Copy source and build
COPY . .
RUN CGO_ENABLED=1 GOOS=linux go build -trimpath -ldflags="-s -w" -o /bot ./cmd/bot
RUN CGO_ENABLED=1 GOOS=linux go build -trimpath -ldflags="-s -w" -o /backtest ./cmd/backtest
RUN CGO_ENABLED=1 GOOS=linux go build -trimpath -ldflags="-s -w" -o /dashboard ./cmd/dashboard

# ── Stage 2: runtime ──────────────────────────────────────────────────────────
FROM alpine:3.19

# ca-certificates for HTTPS calls
RUN apk add --no-cache ca-certificates tzdata

WORKDIR /app

# Copy binaries
COPY --from=builder /bot       ./bot
COPY --from=builder /backtest  ./backtest
COPY --from=builder /dashboard ./dashboard

# Create data directories
RUN mkdir -p data/historical data/satellite

# Default: dry-run mode. Mount .env file at runtime:
#   docker run --env-file .env polymarket-weather-bot
ENTRYPOINT ["./bot"]
CMD []

# Expose nothing — bot pulls, doesn't serve.
LABEL org.opencontainers.image.title="polymarket-weather-bot" \
      org.opencontainers.image.description="Automated weather prediction market bot for Polymarket" \
      org.opencontainers.image.source="https://github.com/devher0/polymarket-weather-bot"
