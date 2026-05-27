# Polymarket Weather Bot — Task Queue

> Язык: **Go**. Автономный агент берёт задачи сверху вниз. Не более 5 файлов / 300 строк за итерацию.
> Выполненные задачи отмечаются [x] YYYY-MM-DD. Лог в NIGHT_LOG.md.

---

## 🔴 ПРИОРИТЕТ 1 — Данные (основа всего)

### [x] 2026-05-27 — TASK-001: NASA POWER API collector
**Файл:** `internal/collectors/nasa_power.go`
Подключить NASA POWER API (https://power.larc.nasa.gov/api/temporal/daily/point)
- Параметры: T2M (температура), PRECTOTCORR (осадки), WS2M (ветер), RH2M (влажность)
- Без API ключа, бесплатно, глобальный охват
- Возвращать `[]weather.Forecast` (тот же тип что в internal/weather/weather.go)
- In-memory кэш на 6 часов (sync.Map + time.Time)
- Проверка компиляции: `go build ./internal/collectors/...`

### [x] 2026-05-27 — TASK-002: NOAA NWS API collector (США)
**Файл:** `internal/collectors/noaa_nws.go`
Подключить National Weather Service API (https://api.weather.gov)
- Эндпоинт: GET /points/{lat},{lon} → достать gridId/gridX/gridY → GET /gridpoints/.../forecast
- Только США: new_york, miami (остальные города — возвращать error "not US")
- Парсить temperature, probabilityOfPrecipitation из periods[0]
- Без API ключа, нужен User-Agent заголовок
- Возвращать `[]weather.Forecast`

### [x] 2026-05-27 — TASK-003: ESA Copernicus / Open-Meteo Historical collector
**Файл:** `internal/collectors/historical.go`
Скачать исторические данные через Open-Meteo Historical API (https://archive-api.open-meteo.com)
- Параметры: те же что у forecast (temperature_2m_max, precipitation_sum, etc.)
- Диапазон: последние 90 дней для каждого города
- Сохранять в data/historical/{city}.json (создать папку если нет)
- Используется для калибровки и бэктеста
- Проверка: `go run ./cmd/bot --collect-history`

### [x] 2026-05-27 — TASK-004: GOES-19 satellite cloud cover
**Файл:** `internal/collectors/goes_satellite.go`
Получить данные об облачности из NOAA GOES-19 через AWS S3 (noaa-goes19, публичный)
- Использовать AWS SDK Go v2 с anonymous credentials
- Брать последний ABI-L2-ACM (облачность) продукт для нужного bbox
- Из данных извлекать среднюю долю облачного покрова (0-1) для каждого города
- Если AWS недоступен — graceful fallback, не крашить
- Сохранять в data/satellite/{city}_{date}.json

### [x] 2026-05-27 — TASK-005: Data aggregator — fusion всех источников
**Файл:** `internal/collectors/aggregator.go`
Объединить все источники в единый `FusedForecast`:
```go
type FusedForecast struct {
    weather.Forecast
    Confidence float64  // 0-1: насколько источники согласны
    Sources    []string // какие источники использованы
}
```
- Взвешенное среднее: OpenMeteo=0.35, NASA=0.30, NOAA=0.25, GOES=0.10
- Если источник недоступен — нормализовать веса
- Confidence = 1 - stddev(probabilities across sources)
- Обновить strategy.Evaluate() для приёма FusedForecast

---

## 🟡 ПРИОРИТЕТ 2 — Стратегия и сигналы

### [x] 2026-05-27 — TASK-006: Улучшить классификатор рынков
**Файл:** `internal/markets/markets.go` (обновить)
- Парсить температурный порог regex: `(\d+)\s*°?[FC]` из вопроса
- Добавить поле ThresholdC в Market struct, конвертировать F→C если нужно
- Передавать реальный порог в HeatProbability() вместо захардкоженных 35°C
- Расширить cityPatterns: NYC, Chi-town, LA, Chicago, San Francisco, Berlin

### [x] 2026-05-27 — TASK-007: Калибровка (Brier score)
**Файл:** `internal/calibration/calibration.go` + `data/bets_history.csv`
- Функция SaveBet(decision, timestamp) → append в CSV
- Функция LoadHistory() → []BetRecord
- Функция BrierScore(records) → float64
- После resolve рынка — функция UpdateOutcome(conditionID, outcome bool)
- Запускать при старте: вывести текущий Brier score если есть данные

### [x] 2026-05-27 — TASK-008: Ensemble + confidence gate
**Файл:** `internal/strategy/strategy.go` (обновить)
- Принимать FusedForecast из aggregator
- Если confidence < 0.4 — пропускать рынок (не ставить)
- Логировать contribution каждого источника в Decision.Reason

---

## 🟢 ПРИОРИТЕТ 3 — Инфраструктура

### [x] 2026-05-27 — TASK-009: Бэктест
**Файл:** `cmd/backtest/main.go`
- Скачать исторические данные (TASK-003)
- Получить исторические цены рынков через Polymarket Gamma API (https://gamma-api.polymarket.com)
- Симулировать все ставки за последние 90 дней
- Вывести: total P&L, win rate, avg edge, Sharpe ratio

### [x] 2026-05-27 — TASK-010: CLI Dashboard
**Файл:** `cmd/dashboard/main.go`
- `go run ./cmd/dashboard positions` — открытые позиции
- `go run ./cmd/dashboard pnl` — P&L из data/bets_history.csv
- `go run ./cmd/dashboard next` — топ-5 ставок прямо сейчас
- Использовать github.com/jedib0t/go-pretty/v6/table

### [x] 2026-05-27 — TASK-011: Telegram уведомления
**Файл:** `internal/notifier/telegram.go`
- Функция NotifyBet(decision) — сообщение при каждой реальной ставке
- Функция DailyDigest(bets []BetRecord) — P&L дайджест в 09:00
- TELEGRAM_BOT_TOKEN + TELEGRAM_CHAT_ID из .env
- Использовать https://api.telegram.org/bot{token}/sendMessage

### [x] 2026-05-27 — TASK-012: Polymarket CLOB order signing (EIP-712)
**Файл:** `internal/polymarket/order.go`
- Реализовать PlaceBet(decision Decision) из заглушки в cmd/bot/main.go
- EIP-712 подпись через go-ethereum/crypto
- POST /order на https://clob.polymarket.com
- L1 Auth headers (CLOB API key + signature)
- Тесты в order_test.go

### [x] 2026-05-27 — TASK-013: Docker + Makefile
**Файл:** `Dockerfile`, `Makefile`
- Multi-stage build: builder → alpine
- `make run` — dry run
- `make live` — реальный режим
- `make backtest` — запустить бэктест
- Обновить README.md: полный гайд

---

## ✅ ВЫПОЛНЕНО

- [x] 2026-05-27 — TASK-000: Базовый бот на Go (internal/weather, internal/markets, internal/strategy, cmd/bot)
- [x] 2026-05-27 — TASK-001: NASA POWER API collector (internal/collectors/nasa_power.go)
- [x] 2026-05-27 — TASK-002: NOAA NWS API collector (internal/collectors/noaa_nws.go)
- [x] 2026-05-27 — TASK-003: Open-Meteo Historical collector (internal/collectors/historical.go)
- [x] 2026-05-27 — TASK-004: GOES-19 satellite cloud cover (internal/collectors/goes_satellite.go)
- [x] 2026-05-27 — TASK-005: Data aggregator — fusion всех источников (internal/collectors/aggregator.go)
- [x] 2026-05-27 — TASK-009: Backtest (cmd/backtest/main.go) — Gamma API + synthetic fallback + P&L/Sharpe/drawdown
- [x] 2026-05-27 — TASK-010: CLI Dashboard (cmd/dashboard/main.go) — positions/pnl/next с go-pretty tables
- [x] 2026-05-27 — TASK-011: Telegram notifier (internal/notifier/telegram.go) — NotifyBet, DailyDigest, NotifyError
- [x] 2026-05-27 — TASK-012: EIP-712 order signing (internal/polymarket/order.go) — PlaceBet, L1 auth, order_test.go
- [x] 2026-05-27 — TASK-013: Docker + Makefile — multi-stage Dockerfile, Makefile с 12 целями, README обновлён
