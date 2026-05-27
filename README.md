# 🌦️ Polymarket Weather Bot

Автоматический бот на **Go** для ставок на погодные рынки [Polymarket](https://polymarket.com).

Идея: Open-Meteo (+ NASA POWER, NOAA NWS, GOES-19) даёт точный прогноз погоды. Polymarket — prediction market, где цены часто неэффективны. Если наш прогноз расходится с ценой рынка на >5% — делаем ставку с Kelly-sizing.

## Структура

```
cmd/
  bot/          — основной бинарник
  backtest/     — бэктест на исторических данных
  dashboard/    — CLI дашборд (позиции, P&L)
internal/
  weather/      — Open-Meteo forecasts (free, no key)
  markets/      — Polymarket CLOB: поиск погодных рынков
  strategy/     — edge calculation + half-Kelly sizing
  collectors/   — NASA POWER, NOAA NWS, GOES-19 satellite (WIP)
  calibration/  — Brier score, калибровка (WIP)
  notifier/     — Telegram уведомления (WIP)
  polymarket/   — EIP-712 order signing (WIP)
data/           — кэш данных, история ставок
```

## Быстрый старт

```bash
# Установить Go 1.21+
go build ./...

# Настроить окружение
cp .env.example .env

# Dry run (без реальных денег)
go run ./cmd/bot

# Боевой режим
go run ./cmd/bot --live

# Цикл каждый час
go run ./cmd/bot --live --loop 3600
```

## Источники данных

| Источник | Покрытие | API ключ |
|----------|----------|----------|
| [Open-Meteo](https://open-meteo.com) | Глобальный, прогноз | Нет |
| [NASA POWER](https://power.larc.nasa.gov) | Глобальный, спутник | Нет |
| [NOAA NWS](https://api.weather.gov) | США | Нет |
| [GOES-19 / AWS](https://registry.opendata.aws/noaa-goes/) | США+, спутник | Нет |
| [ESA Copernicus ERA5](https://cds.climate.copernicus.eu) | Глобальный, история | Бесплатно, нужна регистрация |

## Настройки (.env)

| Параметр | По умолчанию | Описание |
|----------|-------------|----------|
| `POLYMARKET_PRIVATE_KEY` | — | Wallet private key |
| `DRY_RUN` | `true` | Симуляция без денег |
| `MAX_BET_USDC` | `5` | Максимальная ставка |
| `MIN_EDGE` | `0.05` | Минимальный edge (5%) |
| `TELEGRAM_BOT_TOKEN` | — | Для уведомлений |
| `TELEGRAM_CHAT_ID` | — | Для уведомлений |

## ⚠️ Дисклеймер

Образовательный проект. Торговля связана с риском потери средств.
