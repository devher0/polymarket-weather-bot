# 🌦️ Polymarket Weather Bot

Автоматический бот на **Go** для ставок на погодные рынки [Polymarket](https://polymarket.com).

**Идея:** Open-Meteo + NASA POWER + NOAA NWS + GOES-19 satellite дают точный прогноз. Polymarket — prediction market, где цены часто неэффективны. Если наш агрегированный прогноз расходится с рынком на >5% при достаточной уверенности (confidence ≥ 0.4) — делаем ставку с half-Kelly sizing.

## Архитектура

```
cmd/
  bot/          — основной бинарник (dry-run / live / loop)
  backtest/     — бэктест 90 дней на исторических данных
  dashboard/    — CLI дашборд: позиции, P&L, top-5 ставок
internal/
  weather/      — Open-Meteo forecasts (free, no key)
  markets/      — Polymarket CLOB: поиск и классификация погодных рынков
  strategy/     — edge calculation + half-Kelly sizing + confidence gate
  collectors/   — NASA POWER, NOAA NWS, GOES-19 satellite, aggregator
  calibration/  — Brier score, история ставок (CSV)
  notifier/     — Telegram уведомления (bet alerts + daily digest)
  polymarket/   — EIP-712 order signing + CLOB API
data/           — кэш данных, история ставок
```

## Быстрый старт

### Требования

- Go 1.21+ (с CGO для go-ethereum)
- gcc / musl-dev (для CGO)

### Установка

```bash
git clone https://github.com/devher0/polymarket-weather-bot
cd polymarket-weather-bot
go mod download
cp .env.example .env
# Заполнить .env своими ключами
```

### Запуск

```bash
# Dry run (без реальных денег)
make run
# или: go run ./cmd/bot

# Боевой режим
make live
# или: go run ./cmd/bot --live

# Цикл каждый час
make live-loop
# или: go run ./cmd/bot --live --loop 3600

# Скачать исторические данные (для бэктеста)
make history

# Запустить бэктест
make backtest

# CLI дашборд
make dashboard       # всё сразу
make positions       # открытые позиции
make pnl             # история P&L
make next            # топ-5 ставок прямо сейчас
```

### Docker

```bash
# Собрать образ
make docker

# Dry-run в Docker
make docker-run

# Live-режим в Docker
make docker-live

# Бэктест в Docker
make docker-backtest
```

## Команды бота

| Флаг | Описание |
|------|----------|
| *(без флагов)* | Dry-run: показывает ставки без исполнения |
| `--live` | Реальный режим: подписывает и отправляет ордера |
| `--loop N` | Повторять каждые N секунд |
| `--collect-history` | Скачать 90 дней исторических данных |
| `--test-telegram` | Отправить тестовое сообщение в Telegram |

## Источники данных

| Источник | Покрытие | Вес | API ключ |
|----------|----------|-----|----------|
| [Open-Meteo](https://open-meteo.com) | Глобальный | 35% | Нет |
| [NASA POWER](https://power.larc.nasa.gov) | Глобальный | 30% | Нет |
| [NOAA NWS](https://api.weather.gov) | США | 25% | Нет |
| [GOES-19 / AWS](https://registry.opendata.aws/noaa-goes/) | США+, облачность | 10% | Нет |

Все источники бесплатны и не требуют API ключей.

## Настройки (.env)

| Параметр | По умолчанию | Описание |
|----------|-------------|----------|
| `POLYMARKET_PRIVATE_KEY` | — | Ethereum wallet private key |
| `POLYMARKET_ADDRESS` | — | Ethereum address (wallet) |
| `POLYMARKET_API_KEY` | — | CLOB API key |
| `POLYMARKET_API_SECRET` | — | CLOB API secret |
| `POLYMARKET_API_PASSPHRASE` | — | CLOB API passphrase |
| `MAX_BET_USDC` | `10.0` | Максимальная ставка (USDC) |
| `MIN_EDGE` | `0.05` | Минимальный edge (5%) |
| `TELEGRAM_BOT_TOKEN` | — | Telegram бот токен (опционально) |
| `TELEGRAM_CHAT_ID` | — | Telegram chat ID (опционально) |

## Стратегия

1. **Multi-source aggregation** — взвешенное среднее из 4 источников
2. **Confidence gate** — если источники расходятся (confidence < 0.4), пропускаем рынок
3. **Edge calculation** — `edge = our_probability - market_price`
4. **Half-Kelly sizing** — оптимальный размер ставки по Kelly criterion / 2
5. **Calibration** — Brier score для отслеживания качества прогнозов

## ⚠️ Дисклеймер

Образовательный проект. Торговля на Polymarket связана с риском потери средств. Используйте только средства, потерю которых можете позволить себе.
