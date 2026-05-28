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

## 🔵 ПРИОРИТЕТ 4 — Точность и надёжность

### [x] 2026-05-27 — TASK-014: Выбор дня прогноза по дате истечения рынка
**Файлы:** `internal/markets/markets.go`, `internal/collectors/aggregator.go`, `internal/weather/weather.go`, `internal/strategy/strategy.go`, `cmd/bot/main.go`
- Добавить `Market.DaysUntilExpiry() int` — сколько дней до закрытия рынка (0-6)
- Добавить `AggregateForDay(city, dayOffset, dataRoot)` — прогноз для конкретного дня
- Добавить `SunnyProbability(f Forecast) float64` в weather пакет (WMO коды 0-3, осадки)
- Добавить `case "sunny"` в strategy.evaluate()
- В cmd/bot: для каждого рынка вычислять dayOffset и запрашивать прогноз нужного дня

### [x] 2026-05-27 — TASK-015: Позиция-дедупликация (anti-double-bet)
**Файл:** `internal/calibration/calibration.go` (обновить), `cmd/bot/main.go`
- При старте цикла загружать множество conditionID из bets_history.csv
- Перед ставкой проверять: уже есть открытая ставка на этот conditionID?
- Если да — пропускать (не дублировать позицию)
- Логировать "skipped: already have position on <conditionID>"

### [x] 2026-05-27 — TASK-016: Auto-resolve позиций
**Файл:** `internal/calibration/resolver.go` (новый)
- После EndDate рынка — опросить Gamma API `/markets/{conditionID}` на статус resolved
- Вызывать `UpdateOutcome(conditionID, outcome)` автоматически
- Запускать как отдельная горутина в loop режиме раз в час
- Если рынок still open — skip

### [x] 2026-05-27 — TASK-017: Prometheus /metrics endpoint
**Файл:** `internal/metrics/metrics.go` (новый), `cmd/bot/main.go` (обновить)
- Добавить `--metrics-port` флаг (default 9090)
- Экспортировать: bets_placed_total, bets_won_total, brier_score, edge_avg, bankroll_usdc
- Использовать только stdlib (net/http + text/plain формат Prometheus exposition)
- Endpoint: GET /metrics

### [x] 2026-05-27 — TASK-018: Расширенный backtest — Walk-Forward Validation
**Файл:** `cmd/backtest/main.go` (обновить)
- Разбить 90 дней на 3 окна по 30 дней (train/validate/test)
- Для каждого окна оптимизировать minEdge (0.03-0.15 с шагом 0.01)
- Вывести: best minEdge per window, out-of-sample P&L, overfitting ratio
- Добавить `--walk-forward` флаг

---

## 🟣 ПРИОРИТЕТ 6 — Следующие улучшения

### [x] 2026-05-27 — TASK-022: Сезонная калибровка (Bayesian priors по месяцам)
**Файл:** `internal/weather/seasonal.go` (новый), `internal/strategy/strategy.go` (обновить)
- Клима-таблица: 9 городов × 12 месяцев → AvgMaxTempC, RainProbability, SunProbability
- `AdjustForSeason(city, forecastDate, rawP, signal)` — Байесовское смешивание прогноза с климат. приором
- alpha зависит от горизонта прогноза: день 0-1→0.80, день 2-3→0.65, день 4-5→0.50, день 6→0.40
- Интеграция в `evaluate()`: применять коррекцию после вычисления ourP
- Тест в `seasonal_test.go`: проверить summer/winter смещения

### [x] 2026-05-27 — TASK-023: Market liquidity depth filter
**Файл:** `internal/markets/liquidity.go` (новый), `internal/markets/markets.go` (обновить)
- GET /book?token_id=... из CLOB API → проверить top-of-book bid/ask spread
- Если spread > 0.10 (10 cents) — помечать Market.ThinLiquidity = true
- В strategy.go: пропускать рынки с ThinLiquidity и SizeUSDC < 50 USDC — нет смысла мувить цену
- Логировать "skipped: thin liquidity, spread=X"

### [x] 2026-05-27 — TASK-024: Graceful shutdown с итоговым отчётом
**Файл:** `cmd/bot/main.go` (обновить)
- Перехватывать SIGTERM/SIGINT через signal.NotifyContext
- При завершении: вывести итог сессии (сколько рынков, ставок, dry-run P&L)
- Отправить Telegram-уведомление "Bot stopped, session summary: ..."
- Корректно завершать metrics server и resolver горутину

### [x] 2026-05-27 — TASK-025: Аномальные погодные события → повышенный confidence
**Файл:** `internal/weather/extremes.go` (новый), `internal/collectors/aggregator.go` (обновить)
- `IsExtreme(f Forecast) (bool, string)` — выявлять экстремальные значения: MaxTemp>38°C, PrecipMM>50, Wind>90kmh
- При экстремальном событии — автоматически повышать Confidence до max(confidence, 0.75)
- Причина: при очевидных экстремумах все модели обычно соглашаются, даже если у нас только 1-2 источника
- Добавить тег "extreme: heat_wave|heavy_rain|storm" в FusedForecast.Sources

## 🔴 ПРИОРИТЕТ 7 — Новые улучшения (добавлено 2026-05-27)

### [x] 2026-05-27 — TASK-026: Risk Manager — дневные лимиты потерь
**Файл:** `internal/risk/risk.go` (новый), `internal/risk/risk_test.go` (новый), `config/config.go` (обновить), `config/config.yaml` (обновить), `cmd/bot/main.go` (обновить)
- `Manager.AllowBet(records)` — проверка 3 лимитов: дневной cap ставок, дневной лимит потерь, cap открытых позиций
- `DailyStats(records)` → (count, netPnL) — считает только сегодняшние resolved ставки
- `Summary(records, cfg)` — однострочный риск-статус в лог
- Интеграция в bot: pre-cycle check + per-bet check, break loop при срабатывании
- Новые поля в Config: max_daily_loss_usdc, max_daily_bets, max_open_positions
- 13 unit-тестов в risk_test.go

### [x] 2026-05-27 — TASK-027: Open-Meteo Ensemble (16 members) для точной неопределённости
**Файл:** `internal/collectors/openmeteo_ensemble.go` (новый), `internal/collectors/aggregator.go` (обновить)
- Endpoint: https://ensemble-api.open-meteo.com/v1/ensemble с models=icon_seamless
- Парсить 16 членов ансамбля → stddev температуры и осадков
- Использовать stddev как более точный сигнал для Confidence (вместо межмодельного разброса)
- Добавить EnsembleUncertainty float64 в FusedForecast
- Обновить aggregator: если ensemble доступен, заменить confidence на ensemble-based

### [x] 2026-05-27 — TASK-028: Portfolio correlation guard — не ставить на коррелированные города
**Файл:** `internal/risk/correlation.go` (новый), `cmd/bot/main.go` (обновить)
- Карта корреляций: (new_york, miami)=0.7, (london, paris)=0.8, (los_angeles, san_francisco)=0.85
- `CorrelatedCitiesOpen(market, openBets)` — есть ли открытая ставка в коррелированном городе?
- Если correlation > 0.75 И тот же сигнал — пропускать второй рынок
- Логировать "skipped: correlated position in {city} (r=X)"

### [x] 2026-05-27 — TASK-029: Forecast staleness guard — пропускать ставки по старым данным
**Файл:** `internal/collectors/aggregator.go` (обновить), `internal/collectors/openmeteo.go` (проверить)
- Добавить FetchedAt time.Time в FusedForecast
- Если age > 3 часов — логировать "stale forecast, skipping market" и return nil из EvaluateFused
- Порог настраивается через config: max_forecast_age_hours (default: 3)

### [x] 2026-05-27 — TASK-030: Market score ranking — сортировка рынков перед оценкой
**Файл:** `internal/markets/markets.go` (обновить), `internal/strategy/strategy.go` (обновить)
- `ScoreMarket(m Market, ff FusedForecast)` → float64 = edge × confidence × daysUntilExpiry_factor
- Сортировать рынки по Score desc перед циклом ставок
- Лимит: ставить не более TopN (config: max_bets_per_cycle, default 5) лучших рынков за цикл
- Предотвращает ситуацию когда утренний цикл "съедает" весь дневной лимит на плохих рынках

## 🟣 ПРИОРИТЕТ 5 — Новые улучшения

### [x] 2026-05-27 — TASK-019: Rate limiting + retry для HTTP-клиентов
**Файл:** `internal/httpclient/httpclient.go` (новый)
- Общий HTTP-клиент с exponential backoff (max 3 попытки, 429/503 → retry)
- Встроенный rate limiter через `golang.org/x/time/rate` (10 req/s по умолчанию)
- Заменить все `&http.Client{}` в collectors/ на этот клиент

### [x] 2026-05-27 — TASK-020: Конфигурационный файл config.yaml
**Файл:** `config/config.go` (новый), `config/config.yaml` (пример)
- Структура Config: Cities []string, MinEdge, MaxBet, LoopSec, MetricsPort, ...
- Загрузка из config.yaml через gopkg.in/yaml.v3, с fallback на ENV
- Вся конфигурация бота через один файл вместо разрозненных env-переменных

### [x] 2026-05-27 — TASK-021: Юнит-тесты для strategy и calibration
**Файлы:** `internal/strategy/strategy_test.go`, `internal/calibration/calibration_test.go`
- Тесты для Evaluate(), EvaluateFused() — edge cases: no edge, confidence < 0.4
- Тесты для BrierScore(), LoadHistory(), SaveBet(), LoadOpenPositions()
- Мок CSV через os.CreateTemp для изоляции

---

## 🔴 ПРИОРИТЕТ 8 — Новые улучшения (добавлено 2026-05-27)

### [x] 2026-05-27 — TASK-031: Параллельный фетчинг источников данных в aggregator
**Файл:** `internal/collectors/aggregator.go` (обновить)
- Сейчас: 4 HTTP-вызова последовательные → 8-12 сек на цикл
- Рефакторить: `collectSources()` запускает OpenMeteo/NASA/NOAA/GOES в отдельных горутинах одновременно
- Context timeout 8 сек: если источник не ответил — graceful fallback на доступные
- `AggregateAll()` тоже параллелизовать: все 9 городов одновременно (с `errgroup`)
- Результат: цикл бота должен занимать ~3-5 сек вместо 30-60 сек

### [x] 2026-05-27 — TASK-032: Per-source accuracy tracker — динамические веса по точности
**Файл:** `internal/collectors/source_accuracy.go` (новый), `internal/collectors/aggregator.go` (обновить)
- После resolve рынка — сохранять какой источник был ближе к исходу в `data/source_accuracy.json`
- `LoadSourceAccuracy(dataRoot)` → map[source]AccuracyStats{Count, BrierSum}
- `DynamicWeights(accuracy)` → пересчитывать веса: если NASA стабильно точнее OpenMeteo — поднять его вес
- Минимальный вес = 0.05 (не выключать источник полностью при недостатке данных < 10 бетов)
- Обновлять веса раз в цикл, логировать "dynamic weights: openmeteo=0.38 nasa=0.31 ..."

### [x] 2026-05-27 — TASK-033: PnL-адаптивный Kelly — масштабировать bankroll по Brier score
**Файл:** `internal/calibration/calibration.go` (обновить), `internal/strategy/strategy.go` (обновить)
- `BankrollMultiplier(brierScore float64) float64` — если score < 0.10 → 1.5x, если > 0.22 → 0.5x, иначе линейно
- Передавать скорректированный bankroll в EvaluateFused/Evaluate вместо фиксированного
- Загружать Brier score при старте и передавать через config или параметр
- Лимит: multiplier clamped [0.25, 2.0]

---

## 🔴 ПРИОРИТЕТ 9 — Новые улучшения (добавлено 2026-05-27)

### [x] 2026-05-27 — TASK-034: Ensemble uncertainty → proportional bet scaling
**Файл:** `internal/strategy/strategy.go` (обновить)
- Использовать `FusedForecast.EnsembleUncertainty` (°C stddev) для масштабирования ставки
- `ensembleScale = clamp(1.0 - uncertainty/6.0, 0.30, 1.0)`: 0°C→1.0x, 3°C→0.5x, 6°C+→0.3x
- Применять scale к `size` перед min-size-gate в `EvaluateFused()`
- Добавить в Decision.Reason: `ensemble_scale=0.65 (unc=2.1°C)`
- Тест: TestEnsembleScaling в strategy_test.go

### [x] 2026-05-27 — TASK-035: Per-city/signal Brier breakdown
**Файл:** `internal/calibration/calibration.go` (обновить), `cmd/dashboard/main.go` (обновить)
- Добавить поля `City` и `Signal` в BetRecord (cols 8-9, backward-compat: пустые для старых записей)
- Обновить `csvHeader`, `SaveBet`, `parseRow` — сохранять `d.Market.City`, `d.Market.Signal`
- Новая функция `CityBreakdown(records)` → `map[string]BreakdownStats{Count, BrierSum, Wins}`
- Новая функция `SignalBreakdown(records)` → `map[string]BreakdownStats`
- Обновить `PrintBrierScore()` — показывать топ-5 городов и сигналов по точности
- Обновить `cmd/dashboard/main.go` pnl sub-command — таблица per-city

### [x] 2026-05-27 — TASK-036: Pre-order price refresh — свежие цены перед ставкой
**Файл:** `cmd/bot/main.go` (обновить), `internal/markets/markets.go` (обновить)
- Перед каждой реальной ставкой: GET Gamma API `/markets?condition_id={id}` → обновить YesPrice/NoPrice
- Пересчитать edge с актуальными ценами; если edge упал ниже minEdge — пропустить ставку
- Логировать "price refresh: yes=0.42→0.51 (stale by Xmin), edge reduced, skipped"
- Timeout 2s: если Gamma недоступен — использовать старую цену с предупреждением
- Защита от торговли на несвежих ценах в волатильных рынках

---

## 🔴 ПРИОРИТЕТ 10 — Новые улучшения (добавлено 2026-05-27)

### [x] 2026-05-27 — TASK-037: Near-expiry filter — пропускать рынки < MinHoursToExpiry
**Файлы:** `internal/markets/markets.go` (обновить), `config/config.go` (обновить), `config/config.yaml` (обновить), `cmd/bot/main.go` (обновить)
- Добавить `HoursUntilExpiry() float64` в Market — точное время до закрытия в часах
- Добавить `MinHoursToExpiry float64` в Config (default 6.0) + env `MIN_HOURS_TO_EXPIRY`
- В bot loop перед evaluate: если `HoursUntilExpiry() < cfg.MinHoursToExpiry` — пропускать с логом
- Логировать "skipped: market expires in Xh (min=6h), conditionID=..."
- Защита от ставок в последние часы где спред максимален и ликвидность минимальна

### [x] 2026-05-27 — TASK-038: Daily profit target auto-pause
**Файлы:** `internal/risk/risk.go` (обновить), `config/config.go` (обновить), `config/config.yaml` (обновить)
- Добавить `MaxDailyProfitUSDC float64` в Config (default 0 = disabled)
- В `risk.AllowBet()` добавить проверку: если resolved P&L сегодня > MaxDailyProfitUSDC → стоп
- Логировать "daily profit target reached: pnl=+X USDC (target=Y), pausing for the day"
- Защита от ситуации «overtrading после удачного утра»
- Тест в risk_test.go: `TestAllowBetProfitTarget`

### [x] 2026-05-27 — TASK-039: `dashboard forecast` — таблица прогнозов по всем городам
**Файл:** `cmd/dashboard/main.go` (обновить)
- Новый sub-command: `go run ./cmd/dashboard forecast`
- Загружает fusedForecasts для всех городов из data/forecasts/ (если нет — вызывает AggregateAll)
- Таблица: City | MaxTemp°C | Precip mm | Rain% | Cloud% | Confidence | Sources | Age
- Подсвечивает строки с confidence < 0.4 суффиксом "(low conf)"
- Позволяет оператору быстро видеть качество данных перед запуском бота

### [x] 2026-05-27 — TASK-040: Collector smoke-test с реальными HTTP вызовами
**Файл:** `internal/collectors/collectors_integration_test.go` (новый)
- Build tag `//go:build integration` — не запускается в обычном `go test ./...`
- `TestSmokeOpenMeteo` — реальный HTTP запрос, проверяет что возвращается > 0 прогнозов
- `TestSmokeNASAPower` — реальный HTTP, не nil forecast
- `TestSmokeNOAANWS` — только для new_york, проверяет хотя бы 1 период
- Запуск: `go test -tags=integration -timeout=30s ./internal/collectors/`
- Помогает быстро проверить не сломался ли upstream API

---

---

## 🔴 ПРИОРИТЕТ 11 — Новые улучшения (добавлено 2026-05-27)

### [x] 2026-05-27 — TASK-041: Forecast cache persistence — экономия API-вызовов в loop-режиме
**Файл:** `internal/collectors/forecast_cache.go` (новый), `internal/collectors/aggregator.go` (обновить)
- Сохранять FusedForecast на диск в `data/forecasts/{city}_d{dayOffset}.json` после каждого успешного фетча
- При старте Aggregate()/AggregateForDay(): проверять кэш, если возраст < 2 часа — возвращать кэш без API-вызовов
- `SaveForecastCache(city, dayOffset, ff, dataRoot)` и `LoadForecastCache(city, dayOffset, dataRoot, maxAge)` → (*FusedForecast, bool)
- Логировать "forecast cache hit" vs "forecast cache miss" для каждого города
- В loop-режиме: первый цикл фетчит свежие данные (cache miss), следующие 2 часа используют кэш → экономия ~95% API calls

### [x] 2026-05-27 — TASK-042: Forecast change detector — алерт при резком изменении прогноза
**Файл:** `internal/collectors/forecast_cache.go` (обновить), `internal/notifier/telegram.go` (обновить)
- При сохранении нового прогноза: сравнить с предыдущим кэшем
- Если ΔMaxTemp > 5°C ИЛИ ΔPrecipProb > 20% — логировать "forecast shift detected"
- Отправлять Telegram-уведомление `NotifyForecastShift(city, old, new FusedForecast)` при значимом изменении
- Помогает оператору заметить внезапные погодные события (фронты, шторма) которые могут открыть новые ставки

### [x] 2026-05-27 — TASK-043: Active-city filter — фетчить прогнозы только для городов с активными рынками
**Файл:** `cmd/bot/main.go` (обновить), `internal/collectors/aggregator.go` (обновить)
- Перед AggregateAll(): сделать быстрый запрос рынков, собрать уникальные города из активных маркетов
- Передавать `activeCities []string` в новую функцию `AggregateForCities(cities, dataRoot)`
- Для городов без активных рынков — только кэш (не фетчить свежее)
- Логировать "skipping forecast for {city}: no active markets"
- Экономия CPU/API: в тихие дни может быть 3-4 активных города вместо 9

### [x] 2026-05-27 — TASK-044: Bankroll history — сохранение состояния bankroll между сессиями
**Файл:** `internal/calibration/bankroll.go` (новый), `cmd/bot/main.go` (обновить)
- Сохранять текущий bankroll в `data/bankroll.json` после каждого цикла: {bankroll_usdc, updated_at}
- При старте: загружать сохранённый bankroll вместо фиксированных 100.0 USDC
- `LoadBankroll(dataRoot) float64` — возвращает сохранённый bankroll или 100.0 по умолчанию
- `SaveBankroll(bankroll float64, dataRoot string) error`
- Обновлять bankroll: +SizeUSDC при открытии ставки, +/-исход при resolve (через resolver)
- Логировать "bankroll: 100.00 → 103.45 USDC" при изменении

---

## 🔴 ПРИОРИТЕТ 12 — Новые улучшения (добавлено 2026-05-27)

### [x] 2026-05-27 — TASK-045: `dashboard explain` — полный аудит-трейл решений
**Файл:** `internal/strategy/explain.go` (новый), `cmd/dashboard/main.go` (обновить)
- Новый тип `ExplainResult` с полями: RawP, SeasonP, FinalP, YesEdge, NoEdge, BestSide, BestEdge, KellyRaw, EnsScale, FinalSize, SkipReason, Action
- Функция `ExplainEvaluate(m, ff, bankroll, minEdge, maxBet)` → `*ExplainResult`
  - Проходит ВСЕ шаги стратегии: confidence gate, rawP, seasonal adj, edge check, Kelly, ensemble scale
  - При каждом шаге фиксирует почему пропускает / продолжает
- Новый sub-command `dashboard explain`:
  - Фетчит рынки + прогнозы
  - Для каждого рынка выводит полную таблицу: City/Sig | OurP | YesP | NoP | YesEdge | NoEdge | Conf | EnsUnc | Action
  - BET — зелёным, SKIP — красным с причиной
  - Итог: "Evaluated: N | Bets: K | Skipped: N-K"
- Полезно для дебаггинга: понять почему бот не ставит на конкретный рынок

### [x] 2026-05-27 — TASK-046: Webhook уведомления при ставках
**Файл:** `internal/notifier/webhook.go` (новый), `config/config.go` (обновить), `config/config.yaml` (обновить), `cmd/bot/main.go` (обновить)
- `PostWebhook(url string, payload any) error` — POST JSON с таймаутом 3s, retry 1 раз
- Payload: `{event, conditionID, side, size, edge, ourP, marketP, city, signal, timestamp}`
- Events: "bet_placed", "bet_skipped_risk", "cycle_complete", "error"
- Настройка через `webhook_url: ""` в config.yaml и `WEBHOOK_URL` env
- В bot/main.go: вызывать PostWebhook после каждой ставки и в конце цикла
- Позволяет интегрировать бота с внешними системами (алерты, trading journal, Zapier)

### [x] 2026-05-27 — TASK-047: Adaptive loop interval — динамический интервал цикла
**Файл:** `cmd/bot/main.go` (обновить)
- После каждого цикла вычислять следующий интервал на основе результатов:
  - Нашли ≥1 ставку с edge > 0.15 → следующий цикл через 5 мин (проверить остались ли открытые рынки)
  - Ничего не нашли → ждать min(loop_sec × 1.5, 3600) — exponential backoff
  - Нашли только рынки с thin liquidity → ждать 30 мин
- Сбрасывать backoff до base loop_sec если находим новые рынки
- Логировать "next cycle in Xs (adaptive: found N bets)"
- Экономия API вызовов и compute в тихие периоды

### [x] 2026-05-27 — TASK-048: Расширенные regex для парсинга рынков
**Файл:** `internal/markets/markets.go` (обновить)
- Добавить температурный regex без явного C/F: `(\d+)\s*degrees?` → попытаться угадать единицу по контексту (если >50 → скорее F)
- Добавить сигналы: "fog" (туман), "humid" (влажность), "dry" (засуха)
- Расширить cityPatterns: "Big Apple" (new_york), "Windy City" (chicago), "City of Light" (paris), "Eternal City" → skip, "Silicon Valley" (san_francisco)
- Добавить парсинг диапазона температур: "between 20°C and 30°C" → для heat сигнала ThresholdC = верхняя граница
- Тест: `markets_test.go` с 10 новыми тест-кейсами

### [x] 2026-05-27 — TASK-049: `--dry-run-file` — экспорт результатов цикла в JSON
**Файл:** `cmd/bot/main.go` (обновить)
- Флаг `--dry-run-file=output.json` — после каждого цикла записывать результаты в JSON файл
- Структура: `{timestamp, cycle, markets_evaluated, bets_recommended, decisions: [{market, side, ourP, edge, size, reason}]}`
- Создавать/перезаписывать файл после каждого цикла (не append)
- Позволяет внешним инструментам (скрипты, мониторинг) читать результаты последнего цикла
- Работает в dry-run И live режиме

---

## 🔴 ПРИОРИТЕТ 15 — Новые улучшения (добавлено 2026-05-27)

### [x] 2026-05-27 — TASK-057: Structured prediction logging — полный журнал каждой оценки рынка
**Файлы:** `internal/strategy/prediction_log.go` (новый), `internal/strategy/strategy.go` (обновить), `cmd/dashboard/main.go` (обновить)
- Каждый вызов `EvaluateFused()` сохраняет `PredictionRecord` в `data/predictions/YYYY-MM-DD.jsonl`
- Запись как BET (YES/NO), так и SKIP (с причиной: confidence/no_edge/min_size)
- Поля: ts, condition_id, city, signal, our_p, yes_edge, no_edge, confidence, ens_unc, sources, forecast_fields, decision, size_usdc, reason
- Новая helper-функция `ComputeOurP(m, f)` — экспортированная для логирования скипов
- Новый sub-command `dashboard analysis` — таблица: City/Signal | Evaluated | Bets | Skip% | AvgEdge | AvgConf | TotalSize
- Используется для пост-анализа: "почему бот не ставил на NYC/rain сегодня?"

### [x] 2026-05-27 — TASK-058: Weather alert digest в Telegram DailyDigest
**Файлы:** `internal/collectors/noaa_alerts.go` (обновить), `internal/notifier/telegram.go` (обновить)
- В `DailyDigest()` добавить секцию "⚠️ Active Weather Alerts"
- Для каждого US города с AlertLevel > 0 добавлять строку: "🔴 New York: Excessive Heat Warning"
- Emoji по уровню: 🔴 Warning, 🟡 Watch, 🔵 Advisory
- Вызывать `FetchAlerts()` для каждого US города (new_york, miami, chicago, los_angeles, san_francisco)
- Если нет алертов — секция не показывается

### [x] 2026-05-27 — TASK-059: Prediction log CSV export
**Файлы:** `internal/strategy/prediction_log.go` (обновить), `cmd/dashboard/main.go` (обновить)
- Новый sub-command `dashboard export-predictions --date=2026-05-27 --output=predictions.csv`
- Конвертирует JSONL → CSV формат совместимый с Excel/pandas
- Заголовки: timestamp, condition_id, city, signal, our_p, yes_edge, no_edge, confidence, ensemble_unc, decision, size_usdc
- По умолчанию экспортирует сегодня; с `--date` — конкретный день
- Позволяет быстро анализировать данные во внешних инструментах

---

## ✅ ВЫПОЛНЕНО

- [x] 2026-05-27 — TASK-026: Risk Manager (internal/risk/risk.go + risk_test.go) — дневной лимит ставок, P&L лимит, cap открытых позиций; интеграция в bot и config

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

---

## 🔴 ПРИОРИТЕТ 13 — Новые улучшения (добавлено 2026-05-27)

### [x] 2026-05-27 — TASK-050: NOAA Weather Alerts — буст вероятности при активных предупреждениях
**Файлы:** `internal/collectors/noaa_alerts.go` (новый), `internal/collectors/aggregator.go` (обновить), `internal/strategy/strategy.go` (обновить)
- Fetch active NWS alerts: `GET https://api.weather.gov/alerts/active?point={lat},{lon}`
- Парсить severity ("Extreme/Severe/Moderate") и event type ("Tornado Warning", "Excessive Heat Warning", etc.)
- AlertLevel: 0=None, 1=Advisory, 2=Watch, 3=Warning
- Добавить в FusedForecast: `AlertLevel int`, `AlertEvents []string`
- В EvaluateFused(): boost probability на 15%/8%/4% (Warning/Watch/Advisory) для релевантного сигнала
  - Excessive Heat Warning → heat, cold сигналы
  - Winter Storm Warning → snow, cold сигналы
  - Tornado/Severe Thunderstorm → wind, rain сигналы
  - Flood Warning → rain сигнал
- Boost confidence на +0.10 при Warning уровне
- Кэш 30 минут (не чаще NWS rate limit)
- Только для US городов: new_york, miami, chicago, los_angeles, san_francisco
- Логировать "alert boost applied: city=X level=Warning event=Y boost=+0.15"

### [x] 2026-05-27 — TASK-051: /healthz HTTP endpoint
**Файлы:** `cmd/bot/main.go` (обновить), `internal/metrics/metrics.go` (обновить)
- Добавить `/healthz` к существующему Prometheus HTTP серверу
- Возвращает JSON: `{status, uptime_s, last_cycle_at, cycles, bets_placed, open_positions, bankroll_usdc}`
- `status`: "ok" / "degraded" (если last_cycle_at > 2×loop_sec назад)
- Полезно для Docker/k8s health checks и внешнего мониторинга

### [x] 2026-05-27 — TASK-052: Batch market evaluation report — JSON export
**Файлы:** `cmd/dashboard/main.go` (обновить)
- Новый sub-command: `dashboard report --output=report.json`
- Экспортирует полный снимок оценок рынков: timestamp, все рынки с нашей вероятностью, edge, решением
- Удобно для post-hoc анализа и интеграции с внешними системами

### [x] 2026-05-27 — TASK-053: Конфигурация через переменные окружения без .env файла
**Файлы:** `cmd/bot/main.go` (обновить), README.md (обновить)
- Документировать все ENV vars в README секции "Environment Variables"
- Добавить валидацию обязательных ENV vars при старте в live-режиме: POLYMARKET_PRIVATE_KEY, POLYMARKET_ADDRESS
- Выводить чёткое сообщение об ошибке с именами пропущенных vars вместо generic panic

---

## 🔴 ПРИОРИТЕТ 14 — Новые улучшения (добавлено 2026-05-27)

### [x] 2026-05-27 — TASK-054: Correlated positions guard — защита от over-концентрации по city+signal
**Файлы:** `internal/risk/risk.go` (обновить), `internal/risk/risk_test.go` (обновить), `config/config.go` (обновить), `config/config.yaml` (обновить), `cmd/bot/main.go` (обновить)
- Добавить `MaxSameCitySignalBets int` в `risk.Config` и `config.Config`
- Метод `CheckCorrelation(records []BetRecord, city, signal string) error`:
  - Считает открытые (unresolved) ставки на данную (city, signal) пару
  - Возвращает ошибку если count ≥ MaxSameCitySignalBets (default: 2)
- Вызывать перед каждой ставкой в `cmd/bot/main.go`
- Логировать "corr guard: skip (city=X signal=Y open=N max=2)"
- Тесты в risk_test.go

### [x] 2026-05-27 — TASK-055: Confidence-adaptive min_edge — меньший порог для уверенных прогнозов
**Файл:** `internal/strategy/strategy.go` (обновить)
- В `EvaluateFused()` вычислять `adjustedMinEdge = minEdge × confidenceFactor(ff.Confidence)`:
  - confidence > 0.80 → factor 0.80 (высокая уверенность → принимать меньший edge)
  - confidence 0.50-0.80 → factor 1.00 (baseline)
  - confidence < 0.50 → factor 1.50 (низкая уверенность → требовать больший edge)
- Логировать "min_edge adjusted: base=X factor=Y adj=Z conf=W"
- Не менять логику Kelly sizing — только порог входа

### [x] 2026-05-27 — TASK-056: Market price snapshot tracker — отслеживание движения цен рынков
**Файл:** `internal/markets/price_tracker.go` (новый), `cmd/bot/main.go` (обновить)
- `SnapshotPrice(conditionID, yesTokenID, dataRoot string) error` — сохранить текущую YesPrice в `data/price_snapshots/{conditionID}.jsonl` (append JSON lines)
- `GetPriceHistory(conditionID, dataRoot string) ([]PricePoint, error)` — загрузить историю
- Структура: `{timestamp, yes_price, no_price}`
- `DetectAdverseMove(conditionID, ourSide string, history []PricePoint) (bool, float64)` — true если цена нашей стороны упала >0.15 за последние 3 точки (признак информированного движения против нас)
- В bot/main.go: перед каждой ставкой проверять adverse move — если true, логировать предупреждение и увеличить требуемый edge на +0.05

---

## 🔴 ПРИОРИТЕТ 16 — Новые улучшения (добавлено 2026-05-27)

### [x] 2026-05-27 — TASK-060: Market price momentum signal — использовать тренд цены как дополнительный сигнал
**Файл:** `internal/markets/price_tracker.go` (обновить), `cmd/bot/main.go` (обновить)
- `DetectMomentum(side string, history []PricePoint) (direction string, strength float64)` — анализ тренда
- Алгоритм: взять последние 5 точек (или сколько есть ≥4), посчитать последовательные движения в одну сторону
- Если 3+ последовательных роста цены нашей стороны → direction="favorable", strength = consecutive/5
- Если 3+ последовательных падений → direction="adverse", strength пропорционально
- Иначе → direction="neutral", strength=0
- В cmd/bot/main.go: если favorable → логировать "momentum boost: side moving in our favor"; если adverse+strong (>0.6) → требовать edge +0.03
- Добавить в Decision.Reason: "momentum=favorable(0.8)" или "momentum=adverse(0.7, +0.03 edge req)"

### [x] 2026-05-27 — TASK-061: Position profit-taking alert — Telegram алерт когда позиция в хорошем плюсе
**Файл:** `cmd/bot/main.go` (обновить), `internal/notifier/telegram.go` (обновить)
- После SnapshotOpenPositions: для каждой открытой позиции сравнить текущую цену с entry price (market_price из BetRecord)
- Если текущая цена нашей стороны выросла на ≥0.25 от entry → отправить Telegram-алерт: "💰 Profit opportunity: {condID} side={side} entry={entry:.2f} now={now:.2f} (+{pnl:.0f}% implied P&L)"
- Не спамить: сохранять set notified conditionIDs в data/profit_alerts.json, алертить один раз
- Добавить `NotifyProfitOpportunity(condID, side string, entry, current float64) error` в telegram.go

### [x] 2026-05-27 — TASK-062: Forecast confidence time-decay — снижать уверенность для дальних прогнозов
**Файл:** `internal/collectors/aggregator.go` (обновить)
- После fuse(): применять time-decay к Confidence на основе dayOffset параметра
- Decay table: dayOffset 0-1→×1.00, 2→×0.95, 3→×0.88, 4→×0.78, 5→×0.65, 6→×0.55
- Логировать "confidence decay: raw=0.72 → decay_factor=0.78 → adj=0.56 (day=4)"
- Реализовать в AggregateForDay(): после получения FusedForecast применять `applyConfidenceDecay(ff, dayOffset)`
- Рационал: 6-дневный прогноз значительно менее надёжен чем сегодняшний

### [x] 2026-05-27 — TASK-063: Market stale detector — пропускать рынки без торгов >24h
**Файл:** `internal/markets/markets.go` (обновить)
- Парсить `last_trade_price` и предположительно дату последнего трейда из Gamma API ответа
- Если рынок не торговался >24h И spread > 0.08 → помечать Market.Stale = true
- В strategy.go: логировать "skipped: stale market (no trades >24h)" и return nil для стейл рынков
- Помогает избежать неликвидных рынков которые просто "висят"

### [x] 2026-05-27 — TASK-064: Per-city climate anomaly score — буст уверенности при экстремальных отклонениях от нормы
**Файл:** `internal/weather/seasonal.go` (обновить), `internal/collectors/aggregator.go` (обновить)
- Использовать данные из data/historical/{city}.json для вычисления rolling avg и stddev за последние 30 дней
- `ClimateAnomalyScore(city, forecastDate, maxTemp float64, dataRoot string) float64` → 0-1
- Score = clamp((maxTemp - rolling_avg) / (2 * rolling_stddev), 0, 1) для heat
- В aggregator: если anomalyScore > 0.7 → boost confidence: ff.Confidence = max(ff.Confidence, 0.70)
- Логировать "climate anomaly: city=X maxTemp=39°C norm=28°C score=0.85 → confidence boosted"

---

## 🔴 ПРИОРИТЕТ 17 — Новые улучшения (добавлено 2026-05-27)

### [x] 2026-05-27 — TASK-065: Market loss blacklist — не входить повторно в рынки где недавно проиграли
**Файлы:** `internal/markets/blacklist.go` (новый), `cmd/bot/main.go` (обновить), `config/config.go` (обновить)
- После резолва проигрышной ставки — добавить conditionID в `data/blacklist.json` на N дней (default=5)
- `LoadBlacklist`, `IsBlacklisted`, `AddToBlacklist`, `PurgeExpired`, `SaveBlacklist`
- В cmd/bot/main.go: после загрузки истории — автообновление blacklist из lost bets; перед оценкой рынка — проверить IsBlacklisted
- Конфиг: `loss_blacklist_days` (yaml) / `LOSS_BLACKLIST_DAYS` (env), default=5
- Логировать "blacklisted: market {conditionID} until {date}"

### [x] 2026-05-27 — TASK-066: Adaptive min_edge — динамический порог входа по rolling Brier score
**Файлы:** `internal/calibration/adaptive_edge.go` (новый), `cmd/bot/main.go` (обновить)
- Rolling window: последние 20 разрешённых ставок
- Brier < 0.10 → factor 0.90 (расслабить на 10%); Brier > 0.22 → factor 1.20 (ужесточить на 20%)
- Линейная интерполяция между крайними значениями
- Результат зажат в [base × 0.75, base × 1.50]
- Нужно минимум 5 разрешённых ставок — иначе возвращать base без изменений
- Логировать "adaptive min_edge: base=0.05 rolling_brier=0.09 factor=0.90 adjusted=0.045"

### [x] 2026-05-27 — TASK-069: Peak drawdown circuit-breaker — снижать ставки при просадке
**Файлы:** `internal/calibration/drawdown.go` (новый), `cmd/bot/main.go` (обновить), `config/config.go` (обновить)
- Отслеживать максимальный bankroll за всё время в `data/bankroll_peak.json`
- Просадка < 10% → multiplier 1.00 (без изменений)
- Просадка 10–30% → линейно от 1.00 до 0.20
- Просадка > 30% → multiplier 0.20 (защитный минимум)
- Конфиг: `max_drawdown_fraction` (yaml) / `MAX_DRAWDOWN_FRACTION` (env), default=0.30
- Логировать "drawdown guard: peak=X current=Y drawdown=Z% mult=M"

---

## 🔴 ПРИОРИТЕТ 18 — Новые улучшения (добавлено 2026-05-27)

### [x] 2026-05-27 — TASK-070: Weekly Telegram digest — еженедельный отчёт с разбивкой по city/signal
**Файлы:** `internal/notifier/telegram.go` (обновить), `cmd/bot/main.go` (обновить)
- `WeeklyDigest(dataRoot string) error` — отправляет Telegram-сообщение каждое воскресенье
- Содержимое: ставки за последние 7 дней, win rate, rolling Brier, топ прибыльный city/signal, суммарный P&L
- В cmd/bot/main.go: трекать `data/last_weekly_digest.txt` (RFC3339 timestamp); отправлять если прошло ≥7 дней
- Формат: emoji + таблица → как DailyDigest, но за неделю
- Логировать "weekly digest sent" / "weekly digest: skipped (sent X days ago)"

### [x] 2026-05-27 — TASK-071: Total USDC exposure cap — ограничение общей суммы в открытых позициях
**Файлы:** `config/config.go` (обновить), `config/config.yaml` (обновить), `internal/risk/risk.go` (обновить), `cmd/bot/main.go` (обновить)
- Добавить `MaxExposureUSDC float64` в `Config` (yaml: `max_exposure_usdc`, env: `MAX_EXPOSURE_USDC`, default=0 = отключено)
- Метод `CheckExposure(records []BetRecord, maxExposure float64) error`:
  - Суммирует `SizeUSDC` всех открытых (unresolved) ставок
  - Возвращает ошибку если sum ≥ maxExposure
- Добавить в `Config.MaxExposureUSDC` и в `risk.Config`
- В cmd/bot/main.go: вызывать перед каждой ставкой (после AllowBet, перед ExecuteBet)
- Логировать "exposure guard: total=X max=Y — skip" или "exposure guard: total=X max=Y — ok"

### [x] 2026-05-27 — TASK-072: Signal win-rate breakdown по типу сигнала — Telegram алерт при ухудшении
**Файлы:** `internal/calibration/calibration.go` (обновить), `cmd/bot/main.go` (обновить)
- `SignalBreakdown(records []BetRecord) map[string]BreakdownStats` — уже есть, использовать
- `WeakSignalAlert(breakdown map[string]BreakdownStats, minSamples int) []string` — возвращает список сигналов с win rate <40% (≥minSamples=10 ставок)
- В cmd/bot/main.go: проверять при старте, если есть слабые сигналы — Telegram предупреждение "⚠️ Weak signal detected: rain win_rate=32% (n=15) — consider raising min_edge"
- Логировать все слабые сигналы

---

## 🔴 ПРИОРИТЕТ 19 — Новые улучшения (добавлено 2026-05-27)

### [x] 2026-05-27 — TASK-073: Config hot-reload via SIGHUP
**Файлы:** `cmd/bot/main.go` (обновить)
- Добавить `sighupCh := make(chan os.Signal, 1)` + `signal.Notify(sighupCh, syscall.SIGHUP)`
- В select loop: `case <-sighupCh:` → перечитать config.yaml, обновить `*cfg` на месте
- Сохранять CLI-flag overrides (--loop, --metrics-port) при reload
- In-flight run() не затрагивается; следующий цикл получит новый конфиг
- Логировать "SIGHUP received — reloading config" + key fields после reload

### [x] 2026-05-27 — TASK-074: Calibration model snapshot export — JSON-дамп состояния модели
**Файлы:** `internal/calibration/snapshot.go` (новый), `cmd/bot/main.go` (обновить), `cmd/dashboard/main.go` (обновить)
- `ExportSnapshot(records, baseMinEdge, maxDrawdownFraction, dataRoot)` → `data/calibration_snapshot.json`
- Содержимое: overall_brier, win_rate, resolved/open bets, adaptive_edge_factor, drawdown_pct/mult, bankroll/peak, city/signal breakdown
- Вызывать при старте бота (после PrintBrierScore)
- Новый dashboard subcommand `snapshot` → `calibration.PrintSnapshot(dataRoot)` — форматированный вывод в терминал
- Позволяет внешним инструментам (Grafana, скрипты) читать текущее состояние модели без парсинга логов

### [x] 2026-05-27 — TASK-075: Market opportunity heatmap CSV — накопительный CSV с edge/confidence
**Файлы:** `internal/strategy/heatmap.go` (новый), `cmd/bot/main.go` (обновить), `cmd/dashboard/main.go` (обновить)
- `AppendHeatmap(rows []HeatmapRow, dataRoot string)` → append в `data/heatmap/YYYY-MM-DD.csv`
- `HeatmapRowFromPrediction(PredictionRecord) HeatmapRow` — конвертация из prediction log
- `LoadTodayHeatmap(dataRoot string)` — загрузить сегодняшний файл
- Колонки: timestamp, city, signal, our_p, yes_edge, no_edge, confidence, ensemble_unc, decision, size_usdc
- В cmd/bot/main.go: после каждого цикла вызывать `exportHeatmapFromPredictions()` — экспортирует все сегодняшние predictions
- Новый dashboard subcommand `heatmap` — агрегированная таблица city×signal с avg_edge и avg_conf

---

## 🔴 ПРИОРИТЕТ 20 — Новые улучшения (добавлено 2026-05-27)

### [x] 2026-05-27 — TASK-076: OpenMeteo hourly forecast — intraday точность для day-0/day-1 рынков
**Файл:** `internal/collectors/openmeteo_hourly.go` (новый), `internal/collectors/aggregator.go` (обновить)
- `FetchHourlyForecast(city, days)` → `[]HourlyPoint` — 24× точки в сутки (temp, precip, precipProb, wind, cloud, WMO)
- `FilterHourlyByDate(points, date)` — фильтр по дате (UTC)
- `RefineWithHourly(ff *FusedForecast, points []HourlyPoint)` — перезаписывает MaxTemp/MinTemp/PrecipProb/PrecipMM/Wind более точными hourly-значениями; буст confidence +0.05; добавляет "hourly" в Sources
- `hourlyRainProbability` — "at-some-point" вероятность: maxHourlyProb + буст при накоплении осадков (1.5мм→+5%, 5мм→+15%)
- Интеграция в `Aggregate()` (dayOffset=0) и `AggregateForDay()` (dayOffset 0-1): вызов перед сохранением в кэш
- Эффект: rain probability теперь отражает реальный пик часа, а не smoothed daily max

### [x] 2026-05-27 — TASK-077: Unit-тесты для openmeteo_hourly
**Файл:** `internal/collectors/hourly_test.go` (новый)
- `TestFilterHourlyByDate` — проверить фильтрацию по дате
- `TestHourlyRainProbability` — тест без осадков, с малыми осадками (1мм), с большими (6мм)
- `TestHourlyMaxMinTemp` — проверить max/min из набора точек
- `TestRefineWithHourly` — mock FusedForecast + проверить все поля после refinement
- `TestRefineWithHourlyEmpty` — не паниковать на пустом slice
- Запуск: `go test ./internal/collectors/ -run TestHourly`

### [x] 2026-05-27 — TASK-078: Dashboard `hourly` sub-command — почасовой прогноз для города
**Файл:** `cmd/dashboard/main.go` (обновить)
- `go run ./cmd/dashboard hourly new_york` — таблица по часам: Hour UTC | Temp°C | Precip mm | Rain% | Wind km/h | Cloud% | WMO
- Загружать через `FetchHourlyForecast(city, 2)` — сегодня + завтра
- Выделять строки с PrecipProb>50% суффиксом "(rain likely)"
- Строки с TempC выше климат-нормы → `!` маркер
- Полезно для ручной проверки прогноза перед запуском бота

### [x] 2026-05-27 — TASK-079: Probabilistic rain window — уточнить вероятность дождя по часам до экспирации рынка
**Файл:** `internal/collectors/openmeteo_hourly.go` (обновить), `internal/collectors/aggregator.go` (обновить)
- `RainWindowProbability(points []HourlyPoint, fromUTC, toUTC time.Time)` — вероятность дождя в конкретном окне часов
- В `AggregateForDay()`: если знаем время экспирации рынка — передавать временное окно и использовать более точную вероятность
- Например: рынок "дождь в NYC до 18:00" → считать только часы 00-18, не 00-24
- Добавить поле `Market.ExpiryUTC time.Time` — парсить из Gamma API ответа
- Логировать "rain window [06-18 UTC]: prob=0.73 (full day: 0.45)"

---

## 🔴 ПРИОРИТЕТ 21 — Новые улучшения (добавлено 2026-05-27)

### [x] 2026-05-27 — TASK-080: Configurable Kelly fraction + max Kelly cap
**Файлы:** `config/config.go` (обновить), `config/config.yaml` (обновить), `internal/strategy/strategy.go` (обновить), `cmd/bot/main.go` (обновить)
- Добавить `KellyFraction float64` в Config (yaml: `kelly_fraction`, env: `KELLY_FRACTION`, default: 0.5)
- Добавить `MaxKellyFraction float64` в Config (yaml: `max_kelly_fraction`, env: `MAX_KELLY_FRACTION`, default: 0.05 — max 5% of bankroll per bet)
- Обновить `halfKelly(edge, odds, bankroll, maxFraction float64)` — уже принимает maxFraction; передавать cfg.MaxKellyFraction
- В `evaluate()` и `EvaluateFused()`: `k/2` → `k * cfg.KellyFraction`; обновить сигнатуры для передачи KellyFraction
- Альтернатива: передавать оба параметра в Evaluate/EvaluateFused или упаковать в `StrategyParams` struct
- Позволяет операторам настраивать агрессивность (0.25 = quarter-Kelly, 1.0 = full-Kelly)

### [x] 2026-05-27 — TASK-081: Source health tracker — per-source up/down stats
**Файлы:** `internal/collectors/source_health.go` (новый), `internal/collectors/aggregator.go` (обновить), `cmd/dashboard/main.go` (обновить)
- Тип `SourceHealth{LastSuccess, LastError time.Time, ConsecFails int, TotalCalls, TotalSuccess int64}`
- `RecordSourceCall(source string, err error, dataRoot string)` — atomic update + persist в `data/source_health.json`
- `LoadSourceHealth(dataRoot)` → `map[string]SourceHealth`
- В `collectSources()` aggregator.go: вызывать `RecordSourceCall` для каждого источника
- Новый sub-command `dashboard health` — таблица: Source | Status | LastSuccess | LastError | ConsecFails | UpRate%
- Цвет: зелёный если последний успех < 1ч назад, жёлтый < 6ч, красный > 6ч

### [x] 2026-05-27 — TASK-082: Config validation + sanitization
**Файлы:** `config/config.go` (обновить), `cmd/bot/main.go` (обновить)
- Функция `Validate(cfg *Config) []string` → возвращает список предупреждений (warnings); ошибки fatal
- Проверки: MinEdge [0.01, 0.50], MaxBet [0.10, 1000], KellyFraction [0.10, 1.0], MaxKellyFraction [0.01, 0.25]
- Предупреждения: MinEdge < 0.03 ("very aggressive"), MaxBet > 100 ("large bets"), LoopSec > 0 && LoopSec < 60 ("tight loop")
- Ошибки: Cities пустой, MinEdge <= 0, MaxBet <= 0
- В cmd/bot/main.go: вызывать Validate при старте; если warnings — логировать slog.Warn; если errors — exit(1)

---

## 🔴 ПРИОРИТЕТ 22 — Новые улучшения (добавлено 2026-05-27)

### [x] 2026-05-27 — TASK-083: UV Index signal — новый тип сигнала для UV-рынков
**Файлы:** `internal/weather/weather.go`, `internal/markets/markets.go`, `internal/strategy/strategy.go`
- Добавить `UVIndexMax float64` в `weather.Forecast`
- Фетчить `uv_index_max` в `GetForecast()` из Open-Meteo daily API
- `UVProbability(f Forecast, threshold float64) float64` — вероятность превышения UV порога (threshold=8 = "very high UV")
- В `markets.go`: regex `(?i)\buv.?index\b|ultraviolet` → signal "uv"; парсинг UV threshold из вопроса (числа 1-12)
- В `strategy.go ComputeOurP`: case "uv" → `weather.UVProbability(f, threshold)`
- В `strategy.go ScoreMarket`: case "uv" → аналогично
- Экспандирует рынки, которые бот может оценивать (пример: "Will UV index exceed 8 in Miami today?")

### [x] 2026-05-27 — TASK-084: Apparent temperature — heat index / wind chill для heat/cold рынков
**Файлы:** `internal/weather/apparent.go` (новый), `internal/collectors/aggregator.go` (обновить), `internal/collectors/nasa_power.go` (обновить)
- `HeatIndexC(tempC, relHumidityPct float64) float64` — формула Steadman/Rothfusz (корректна при tempC>27, humidity>40%)
- `WindChillC(tempC, windKMH float64) float64` — формула NOAA (корректна при tempC<10, wind>4.8 km/h)
- `ApparentTempC(tempC, relHumidityPct, windKMH float64) float64` — выбор правильной формулы по условиям
- NASA POWER уже возвращает RH2M (влажность); добавить `HumidityPct` в `weather.Forecast`
- В aggregator: после fusion вычислять `ff.Forecast.ApparentMaxTempC` как отдельное поле
- В `HeatProbability` / `ComputeOurP`: для сигнала "heat" использовать ApparentMaxTempC если humidity > 50%
- Эффект: +5-10% точность для жаркихи/холодных рынков

### [x] 2026-05-27 — TASK-085: Barometric pressure trend — физический сигнал для rain-рынков
**Файлы:** `internal/collectors/openmeteo_hourly.go` (обновить), `internal/collectors/aggregator.go` (обновить)
- Добавить `PressureHPa float64` в `HourlyPoint`
- Фетчить `surface_pressure` в `FetchHourlyForecast()` из Open-Meteo hourly
- `PressureTrendBoost(points []HourlyPoint) float64` — вычислить средний тренд давления за последние 6 точек (hPa/3h); если падение >2 hPa/3h → возвращать +0.08 буст дождя; если рост >2 → −0.05
- В `RefineWithHourly()`: применять `PrecipP += PressureTrendBoost(points)` (clamped to 0.97)
- Логировать "pressure trend: Δ=-3.2 hPa/3h → rain boost +0.08"
- Физическое обоснование: падение давления = приближение фронта = дождь; рост = прояснение

---

## 🚀 ПРИОРИТЕТ 5 — Аэрокосмические источники (SpaceX-level данные)

### [x] 2026-05-27 — TASK-086: NOAA HRRR — высокоточная модель (3км, обновление каждый час)
**Файл:** `internal/collectors/hrrr.go`
Подключить NOAA High-Resolution Rapid Refresh через Open-Meteo HRRR endpoint:
- URL: https://api.open-meteo.com/v1/forecast?models=gfs_seamless (или hrrr)
- Параметры: temperature_2m, precipitation_probability, wind_speed_10m, cape (конвективная энергия)
- Только США (new_york, miami) — HRRR покрывает только Северную Америку
- Обновляется каждый час → кэш не более 60 минут
- Добавить как 5-й источник в aggregator с весом 0.15 (перераспределить остальные)
- HRRR особенно точен для storm/wind рынков (конвективные события)

### [x] 2026-05-27 — TASK-087: Радиозонды RAOB — профиль атмосферы по высотам
**Файл:** `internal/collectors/raob.go`
Подключить данные метеозондов через rucsoundings.noaa.gov:
- URL: https://rucsoundings.noaa.gov/get_soundings.cgi?data_source=GFS&airport={lat},{lon}
- Парсить: ветер на высотах 850/700/500 hPa (≈1.5/3/5.5 км)
- Возвращать AtmosphericProfile{WindKMH850hPa, WindKMH700hPa, WindKMH500hPa, MaxWindShear}
- Использовать в wind-рынках: если ветер на высоте 850hPa > 50km/h → boost wind probability
- Graceful fallback если данные недоступны

### [x] 2026-05-27 — TASK-088: Blitzortung — детекция молний в реальном времени
**Файл:** `internal/collectors/lightning.go`
Подключить глобальную сеть детекции молний blitzortung.org:
- WebSocket: wss://ws8.blitzortung.org (публичный, без ключа)
- Считать количество ударов молний за последние 30 минут в radius 200км от города
- LightningRisk(city, strikes30min) float64 — высокий риск при >50 ударов
- Использовать как сигнал для storm/wind рынков: lightning risk → boost storm probability
- Хранить в data/lightning/{city}_{hour}.json

### [x] 2026-05-27 — TASK-089: CAPE индекс — конвективная энергия (storm predictor)
**Файл:** `internal/collectors/aggregator.go` (обновить), `internal/weather/weather.go` (обновить)
CAPE (Convective Available Potential Energy) — лучший физический предиктор гроз:
- Добавить поле `CapeJkg float64` в `weather.Forecast`
- Фетчить `cape` из Open-Meteo (доступен в hourly параметрах)
- CAPEStormProbability(cape float64) float64:
  - cape < 500  → 0.05 (слабый риск)
  - cape 500-1500 → 0.25 (умеренный)
  - cape 1500-3000 → 0.60 (высокий)
  - cape > 3000  → 0.90 (очень высокий, торнадо-опасность)
- Интегрировать в storm/wind strategy как дополнительный буст

### [x] 2026-05-27 — TASK-090: 45th Weather Squadron launch forecasts парсер
**Файл:** `internal/collectors/launch_weather.go`
45th Weather Squadron (Patrick AFB) публикует GO/NO-GO критерии для запусков — те же данные что использует SpaceX:
- URL: https://www.patrick.spaceforce.mil/About/Weather/ (публичная страница)
- Парсить: облачность на высотах, электрические поля, вероятность выполнения правил
- LaunchRules compliance — 11 официальных Launch Commit Criteria
- Использовать для storm/wind/rain рынков: низкий compliance % → высокий риск плохой погоды
- Если парсинг недоступен — graceful skip

### [x] 2026-05-27 — TASK-091: ECMWF AIFS — лучшая мировая AI-модель прогноза
**Файл:** `internal/collectors/ecmwf_aifs.go`
С октября 2025 ECMWF открыл полный доступ к данным IFS + AIFS (AI Forecasting System).
AIFS обгоняет классические модели на 5-15% по точности, лучше предсказывает тропические циклоны:
- URL: https://data.ecmwf.int/forecasts/ (open data, без ключа)
- Параметры: 2m_temperature, total_precipitation, 10m_wind_speed, mean_sea_level_pressure
- Горизонт: до 10 дней, 6-часовые шаги
- Добавить как источник с весом 0.25, повысить до первого места по весу
- Graceful fallback если ECMWF API недоступен

### [x] 2026-05-27 — TASK-092: NOAA GFS — глобальный прогноз 16 дней
**Файл:** `internal/collectors/gfs.go`
GFS (Global Forecast System) — базовая модель всех профессиональных weather трейдеров:
- Через Open-Meteo: `?models=gfs_seamless` — уже агрегирует GFS
- Или напрямую: NOAA NOMADS https://nomads.ncep.noaa.gov/
- Параметры: temperature, precipitation, wind, convective_inhibition
- Горизонт: до 16 дней (уникально — больше всех остальных источников)
- Добавить поле `Forecast16Days []Forecast` для долгосрочных рынков

### [x] 2026-05-27 — TASK-093: CME HDD/CDD индексы — стандарт weather derivatives
**Файл:** `internal/collectors/cme_degree_days.go`
Chicago Mercantile Exchange публикует Heating/Cooling Degree Day индексы — именно по ним торгуются погодные деривативы на $4.6 млрд рынке:
- HDD = max(0, 65°F - средняя_температура) — heating degree days
- CDD = max(0, средняя_температура - 65°F) — cooling degree days
- Использовать для рынков с формулировкой "average temperature above/below X"
- Данные: https://www.cmegroup.com/markets/weather.html (парсить публичную страницу)
- Или вычислять самостоятельно из наших прогнозов и сравнивать с CME settlement

### [x] 2026-05-27 — TASK-094: NLDN + Vaisala — National Lightning Detection Network
**Файл:** `internal/collectors/lightning_nldn.go`
Vaisala NLDN — профессиональная сеть детекции молний, которую использует 45th Weather Squadron:
- Публичный доступ через: https://www.weather.gov/lmk/lightning (NWS отображение)
- Или через Blitzortung API как альтернатива (TASK-088)
- Cloud-to-ground strikes в radius 300км, за последний час
- Lightning30min, Lightning1h, LightningTrend (растёт/падает)
- Storm probability: >100 ударов/час = 0.90, >50 = 0.70, >10 = 0.40

### [x] 2026-05-27 — TASK-095: ESA MTG-S1 — новый европейский спутник (запущен SpaceX июль 2025)
**Файл:** `internal/collectors/esa_mtg.go`
MTG-Sounder запущен SpaceX в июле 2025 — даёт 3D карты атмосферы над Европой и Африкой:
- Покрытие: Лондон, Париж, Берлин — наши европейские города
- Данные через Copernicus EUMETSAT: https://data.eumetsat.int
- Атмосферный профиль: температура и влажность на 100+ уровнях высоты
- Особенно ценно для winter/storm рынков в Европе
- Регистрация на EUMETSAT бесплатна

### [x] 2026-05-27 — TASK-096: Wind shear профиль — ветер на разных высотах
**Файл:** `internal/collectors/wind_shear.go`
Вертикальный сдвиг ветра — ключевой параметр для storm/hurricane рынков:
- Фетчить из Open-Meteo: wind_speed_80m, wind_speed_120m, wind_speed_180m (hourly)
- WindShear(low, high float64) float64 — разница скоростей между слоями
- Высокий shear (>30 km/h между 10m и 180m) → подавляет торнадо, но усиливает обычные шторма
- Добавить в wind/storm signal как модификатор вероятности
- Критично для: "Will wind speed exceed X in Chicago?" (город у озера, сильный shear)

### [x] 2026-05-27 — TASK-097: Speedwell Climate HDD/CDD settlement data
**Файл:** `internal/collectors/speedwell.go`
Speedwell Climate — институциональный провайдер для weather derivatives трейдеров:
- Публичные исторические HDD/CDD индексы: https://portal.speedwellclimate.com
- Парсить исторические значения для калибровки наших temperature рынков
- Использовать как ground truth для бэктеста temperature-based рынков
- Бесплатный доступ к историческим данным через их портал

### [x] 2026-05-27 — TASK-098: Apparent temperature — feels-like для всех городов (расширение TASK-084)
**Файл:** `internal/collectors/aggregator.go` (обновить)
Дополнить apparent temperature данными из нескольких источников:
- Open-Meteo: apparent_temperature (уже есть в API, просто добавить)
- NASA POWER: уже есть RH2M для расчёта heat index
- Сравнивать apparent_temperature из разных источников для confidence
- Рынки типа "feels like above 105°F in Phoenix" — точность +15%

---

## 🧠 ПРИОРИТЕТ 6 — Мега-агрегатор (поднять % точности)

### [x] 2026-05-27 — TASK-099: Super-aggregator — все источники в один pipeline
**Файл:** `internal/collectors/super_aggregator.go`
Центральный агрегатор который принимает ВСЕ источники и выдаёт единый SuperForecast:
```go
type SuperForecast struct {
    weather.Forecast
    Sources        []SourceResult   // каждый источник с весом и значением
    Confidence     float64          // 0-1: консенсус источников
    Uncertainty    float64          // стандартное отклонение между источниками
    ModelAgreement float64          // % источников согласных с majority vote
    SignalStrength float64          // насколько сильный сигнал (для Kelly scaling)
}
```
- Принимать результаты от: OpenMeteo, NASA, NOAA, GOES, HRRR, ECMWF, GFS, RAOB, Lightning, CAPE, MTG
- Динамические веса: вес источника = его исторический Brier score за последние 30 дней
- Источники с плохим Brier score → автоматически понижаются
- Источники с хорошим Brier score → автоматически повышаются
- Параллельный фетчинг всех источников через goroutines с timeout 10s per source
- Timeout → источник пропускается без блокировки остальных

### [x] 2026-05-27 — TASK-100: Байесовский ансамбль — не просто среднее
**Файл:** `internal/aggregation/bayesian_ensemble.go`
Вместо взвешенного среднего — полноценный байесовский ансамбль:
- Prior: климатологическая вероятность для города/месяца/сигнала из исторических данных
- Likelihood: каждый источник обновляет prior через байесовское обновление
- Posterior = итоговая вероятность
- Формула: P(rain|sources) = P(sources|rain) × P(rain) / P(sources)
- Результат: точнее обычного среднего на 8-12% особенно когда источники расходятся

### [x] 2026-05-27 — TASK-101: Gradient boosting калибровка (XGBoost-style в Go)
**Файл:** `internal/aggregation/gradient_boost.go`
Обучить лёгкую ML-модель прямо в Go без внешних зависимостей:
- Features: [openmeteo_p, nasa_p, noaa_p, goes_cloud, cape, pressure_trend, month, city_id]
- Target: фактический исход рынка (из bets_history.csv resolved=true/false)
- Алгоритм: простой gradient boosting с 50-100 деревьями решений (gbdt.go)
- Переобучение каждые 7 дней на свежих данных
- Хранить модель в data/model.json (веса деревьев)
- После 50+ resolved ставок — точность +10-15% vs взвешенного среднего

### [x] 2026-05-27 — TASK-102: Метео-консенсус индекс (как рынки используют Reuters Eikon)
**Файл:** `internal/aggregation/consensus_index.go`
Профессиональные трейдеры смотрят на консенсус между моделями:
- Если ECMWF, GFS, HRRR, OpenMeteo все говорят "дождь" → консенсус = 1.0, ставим уверенно
- Если модели 50/50 → консенсус = 0.0, пропускаем (edge реально нет)
- ConsensusIndex(models []float64, threshold float64) (consensus, direction float64)
- Интегрировать в strategy: при ConsensusIndex < 0.3 → skip bet regardless of edge
- При ConsensusIndex > 0.8 → увеличить Kelly fraction на 20%

### [x] 2026-05-27 — TASK-103: Исторический базис — насколько каждый источник точен по городам
**Файл:** `internal/aggregation/source_accuracy.go` (расширить)
Трекать точность каждого источника отдельно по каждому городу и сигналу:
```
OpenMeteo / new_york / rain → Brier: 0.12, N=45 (хороший)
NASA      / london   / rain → Brier: 0.21, N=30 (средний)
NOAA      / miami    / heat → Brier: 0.08, N=20 (отличный)
```
- При агрегации: использовать city+signal специфичные веса
- NOAA хорош для США тепла → вес 0.40 для miami/heat
- ECMWF хорош для Европы → вес 0.45 для london/rain
- Экспортировать в dashboard и Prometheus /metrics

### [x] 2026-05-27 — TASK-104: Real-time re-weighting при расхождении источников
**Файл:** `internal/collectors/super_aggregator.go` (обновить)
Когда источники сильно расходятся — не усреднять, а анализировать:
- Если 1 источник outlier (отклонение > 2σ от остальных) → понизить его вес в текущем цикле
- Если ECMWF расходится с остальными → ECMWF обычно прав (он точнее), повысить его вес
- Логировать: "NOAA outlier detected (0.82 vs mean 0.45), weight reduced to 0.05"
- История outlier'ов влияет на долгосрочный Brier score источника

### [x] 2026-05-27 — TASK-105: Ensemble spread → автоматический размер ставки
**Файл:** `internal/strategy/strategy.go` (обновить)
Spread между источниками = мера неопределённости = должна влиять на Kelly:
- Малый spread (все согласны) → bet_size × 1.3 (высокая уверенность)
- Средний spread → bet_size × 1.0 (baseline)
- Большой spread → bet_size × 0.5 (осторожно, неопределённость высокая)
- SpreadScale(sources []float64) float64 — стандартное отклонение → scaling factor
- Эффект: автоматически больше ставим когда уверены, меньше когда сомневаемся

### [x] 2026-05-27 — TASK-106: Nowcasting — прогноз на следующие 2-6 часов
**Файл:** `internal/collectors/nowcast.go`
Для рынков которые закрываются сегодня — нужен nowcast а не daily forecast:
- Open-Meteo minutely_15 endpoint: 15-минутные интервалы на 2 суток
- Параметры: precipitation, temperature_2m, wind_speed_10m
- NowcastRainProbability(minutes int) float64 — вероятность дождя в следующие N минут
- Использовать для рынков с EndDate сегодня (DaysUntilExpiry == 0)
- Точнее daily forecast для intraday рынков на 20-30%

---

## Фаза 4 — Качество, мониторинг и live-trading

### [x] 2026-05-28 — TASK-107: Integration test — сквозной dry-run без реальных ставок
**Файл:** `tests/integration_test.go`
Написать сквозной тест: GetWeatherMarkets → GetForecast → EvaluateFused → логируем решение. Мокаем HTTP через httptest. Проверяем что бот не паникует на пустом рынке, на рынке без города, на рынке с thin liquidity. `go test ./tests/... -tags integration`

### [x] 2026-05-28 — TASK-108: Healthcheck endpoint — расширенный статус
**Файл:** `internal/metrics/metrics.go` (обновить)
Расширить `/healthz` до JSON: `{"status":"ok","sources":{"openmeteo":true,"nasa":true,...},"last_bet_at":"...","brier_score":0.18,"open_positions":3}`. Каждый источник ping-ует свой API с таймаутом 3с. Если источник недоступен — статус degraded, не error.

### [x] 2026-05-28 — TASK-109: Авто-добавление новых городов через конфиг
**Файл:** `config/config.yaml` + `internal/weather/weather.go`
Сейчас города захардкожены в `var Cities`. Читать их из `config.yaml` секция `cities:` — имя, lat, lon. Добавить 5 новых городов: dubai, sydney, singapore, toronto, moscow. Backward-compatible: если конфиг пустой — использовать встроенные.

### [x] 2026-05-28 — TASK-110: Авто-резолв ставок через Polymarket Gamma API
**Файл:** `internal/calibration/resolver.go` (обновить)
Текущий resolver использует заглушку. Подключить реальный Polymarket Gamma API: `GET https://gamma-api.polymarket.com/markets/{conditionId}` → поле `resolved`, `resolutionPrice`. Если рынок resolved=true и resolutionPrice=1.0 → YES выиграл. Обновлять `bets_history.csv` автоматически раз в час.

### [x] 2026-05-28 — TASK-111: Telegram команды — /status /positions /next
**Файл:** `internal/notifier/telegram_commands.go` (новый файл)
Добавить polling Telegram updates (long-poll, не webhook). Команды:
- `/status` — текущий Brier score, открытые позиции, P&L за день
- `/positions` — список открытых ставок
- `/next` — топ-3 лучших ставки прямо сейчас (dry-run)
- `/pause` и `/resume` — приостановить/возобновить торговлю

---

## 🔬 ПРИОРИТЕТ 7 — Эксперименты и ML-улучшения

### [x] 2026-05-28 — TASK-112: A/B тест стратегий — сравнение Kelly вариантов
**Файл:** `internal/strategy/ab_test.go` (новый)
Реализовать A/B тест двух стратегий ставок с автоматическим сравнением:
- Стратегия A: quarter-Kelly (fraction=0.25) — консервативная
- Стратегия B: half-Kelly (fraction=0.50) — агрессивная
- `ABTest` struct с двумя независимыми bankroll, Brier score, win rate
- Каждый рынок получает обе оценки — решения логируются в data/ab_test.csv
- После N=50 resolved ставок — автоматически выбрать лучшую стратегию
- `dashboard ab-test` субкоманда — показать текущие результаты A/B теста
- Переключение в production на winner без перезапуска

### [x] 2026-05-28 — TASK-113: Sharpe ratio трекер — risk-adjusted return
**Файл:** `internal/calibration/sharpe.go` (новый)
Считать Sharpe ratio нашего P&L для оценки стратегии как профессионального трейдера:
- Sharpe = mean(daily_returns) / std(daily_returns) × sqrt(365)
- Daily returns: (end_bankroll - start_bankroll) / start_bankroll
- Сохранять daily snapshots в data/daily_returns.json
- Sharpe > 1.0 = хорошо, > 2.0 = отлично (hedge fund benchmark)
- Показывать в `/status` Telegram команде и healthz endpoint
- Alert в Telegram если Sharpe < 0.5 за последние 30 дней

### [x] 2026-05-28 — TASK-114: Market sentiment — используем order flow imbalance
**Файл:** `internal/markets/sentiment.go` (новый)
Анализировать дисбаланс ордеров для детекции "умных денег":
- CLOB API: GET /book?token_id=... → считать суммарный объём bid vs ask
- OrderFlowImbalance = (bid_vol - ask_vol) / (bid_vol + ask_vol) → [-1, 1]
- Положительный imbalance + наш прогноз YES → +5% к edge
- Отрицательный imbalance → осторожнее
- Логировать в prediction_log: поле `order_flow_imbalance`

### [x] 2026-05-28 — TASK-115: Seasonal CLOB patterns — торговые паттерны по дням недели
**Файл:** `internal/strategy/seasonal.go` (новый)
Анализировать наши исторические ставки на предмет паттернов:
- Win rate по дню недели (Mon-Sun)
- Win rate по времени суток (morning/afternoon/evening)
- Win rate по сезону (месяц)
- Если в пятницу вечером win rate < 40% → снизить max_bet на 30%
- Данные из bets_history.csv, обновлять каждую итерацию
- Экспортировать как JSON в data/seasonal_patterns.json

### [x] 2026-05-28 — TASK-116: ML фича инженерия — автогенерация признаков для gradient boost
**Файл:** `internal/aggregation/feature_engineering.go` (новый)
Расширить признаки для gradient_boost.go с текущих ~8 до ~25:
- Взаимодействия: openmeteo_p × nasa_p, temp_rank_vs_historical
- Лаговые признаки: yesterday_rain_prob, 3day_rain_trend
- Агрегации: mean_7d_precip, max_7d_temp, std_7d_wind
- City embeddings: one-hot по 15 городам
- Signal embeddings: rain=0, heat=1, cold=2, snow=3, wind=4, sunny=5
- Сохранять feature importance в data/feature_importance.json

---

## 🔴 ПРИОРИТЕТ 23 — Новые улучшения (добавлено 2026-05-28)

### [x] 2026-05-28 — TASK-117: Live unrealized P&L — текущий нереализованный PnL по открытым позициям
**Файлы:** `internal/calibration/unrealized.go` (новый), `cmd/dashboard/main.go` (обновить), `internal/metrics/metrics.go` (обновить)
- `UnrealizedPosition` struct: BetRecord + CurrentPrice, UnrealizedPnL, PriceChange, FetchedAt, FetchError
- `FetchUnrealizedPnL(records []BetRecord) []UnrealizedPosition` — для каждой открытой ставки GET Gamma API `/markets/{conditionID}`, парсит текущую YES/NO цену
- Расчёт: shares = SizeUSDC / entryPrice; unrealizedPnL = shares × (currentPrice - entryPrice)
- Обновить `cmdPositions()` в dashboard: добавить колонки "Current", "Δ Price", "Unreal PnL"
- Добавить Prometheus metric `unrealized_pnl_usdc` в `/metrics`
- Timeout 2s per position; при ошибке — показывать "N/A" без краша

### [x] 2026-05-28 — TASK-118: Per-signal min_edge config — разные пороги edge для разных сигналов
**Файлы:** `config/config.go` (обновить), `config/config.yaml` (обновить), `internal/strategy/strategy.go` (обновить)
- Добавить `SignalMinEdge map[string]float64` в Config (yaml: `signal_min_edge:`, env не нужен)
- `GetMinEdgeForSignal(cfg *Config, signal string) float64` — возвращает signal-specific или default MinEdge
- В `EvaluateFused()`: использовать GetMinEdgeForSignal вместо фиксированного minEdge
- Пример config: rain=0.06, heat=0.04, snow=0.08 (сложнее предсказать)
- Логировать "using signal min_edge=0.06 for signal=rain"

### [x] 2026-05-28 — TASK-119: API downtime alert — Telegram уведомление при сбое Polymarket API
**Файлы:** `cmd/bot/main.go` (обновить), `internal/notifier/telegram.go` (обновить)
- Трекать `consecutiveAPIFails int` при ошибке GetWeatherMarkets()
- При consecutiveAPIFails >= 3 → отправить Telegram: "⚠️ Polymarket API down: N consecutive failures"
- Сбрасывать счётчик при успехе; не спамить — слать уведомление только при переходе 2→3
- Логировать "api_fail_streak=N" каждую итерацию при ошибках

---

## 🔴 ПРИОРИТЕТ 24 — Новые улучшения (добавлено 2026-05-28)

### [x] 2026-05-28 — TASK-120: Fog / Humid / Dry signal support
**Файлы:** `internal/weather/weather.go`, `internal/weather/seasonal.go`, `internal/strategy/strategy.go`
- `FogProbability(f)` — WMO коды 45/48 (fog) + humidity/wind proxy (0.08–0.92)
- `HumidProbability(f, threshold)` — относительная влажность vs порога (default 75%), fallback через rain
- `DryProbability(f)` — 1-rain + WMO код бонусы/штрафы + осадки > 5 мм
- Добавлены case "fog"|"humid"|"dry" в `ScoreMarket()`, `evaluate()`, `EvaluateFused()`
- Сезонные прiors в `priorForSignal()`: fog≈rain×0.30, humid≈rain×0.80+0.10, dry≈1-rain
- Рынки по туману/влажности/засухе теперь торгуются (раньше — default: return nil)

### [x] 2026-05-28 — TASK-121: HTML performance report generator
**Файл:** `cmd/report/main.go` (новый)
- `go run ./cmd/report` → пишет `data/report.html` (самодостаточный HTML)
- Флаги: `--data` (корень данных), `--out` (путь к файлу)
- 4 Chart.js графика: кумулятивный P&L, win rate по сигналу, rolling Brier score (окно 10), кол-во ставок по сигналу
- Таблица городов: бетов / побед / win rate / P&L
- Таблица открытых позиций: город, сигнал, сторона, размер, вероятность, дата
- Тёмная тема, responsive 2-колоночная сетка

---

## 🔴 ПРИОРИТЕТ 25 — Новые улучшения (добавлено 2026-05-28)

### [x] 2026-05-28 — TASK-122: Platt scaling — байесовская калибровка вероятностей
**Файл:** `internal/calibration/platt.go` (новый)
Обучать сигмоидальный калибратор по истории ставок (Platt scaling):
- `PlattCalibrator` struct: slope A, intercept B, N (число обучающих примеров)
- `Fit(predictions, outcomes []float64)` — SGD минимизация log-loss, 200 итераций, lr=0.1
- `Calibrate(p float64) float64` — применить σ(A*p + B) к сырой вероятности
- `SaveCalibrator(path)` / `LoadCalibrator(path)` — JSON персистенция
- Автоматически обновлять после каждого resolved исхода в bot loop
- Показывать calibrated P vs raw P в prediction_log и explain вывод
- Если N < 20 — возвращать raw p без калибровки (недостаточно данных)

### [x] 2026-05-28 — TASK-123: ASCII sparkline P&L в Telegram /status
**Файл:** `internal/notifier/telegram_commands.go` (обновить)
Добавить мини-график P&L за последние 14 дней в ответ /status:
- `asciiSparkline(values []float64) string` — нормировать, выбрать символ из "▁▂▃▄▅▆▇█"
- Считать daily P&L из bets_history.csv (grouped by date)
- Формат: `P&L 14d: ▁▁▃▄▆▇██▆▃▁▂▄▅  (+4.20 USDC total)`
- Показывать только если есть хотя бы 3 дня данных

### [x] 2026-05-28 — TASK-124: New market early-entry detector
**Файл:** `internal/markets/first_seen.go` (новый)
Отслеживать когда каждый conditionID впервые появился:
- `RecordFirstSeen(conditionID string, dataRoot string)` — сохранять в data/market_first_seen.json
- `IsNew(conditionID string, dataRoot string) bool` — вернуть true если рынок появился < 2 часов назад
- В bot loop: если IsNew → уменьшить min_edge на 30% (больше шансов найти edge на неэффективном рынке)
- Логировать "new_market detected, reduced min_edge" при обнаружении
- `dashboard new-markets` субкоманда: список рынков появившихся за последние 24ч

---

## 🔴 ПРИОРИТЕТ 26 — Новые улучшения (добавлено 2026-05-28)

### [x] 2026-05-28 — TASK-125: Forecast stability tracker — снижение confidence при нестабильных прогнозах
**Файлы:** `internal/collectors/forecast_drift.go` (новый), `internal/collectors/forecast_drift_test.go` (новый), `internal/collectors/aggregator.go` (обновить)
Метеорологический факт: прогноз, который меняется от цикла к циклу — менее надёжен.
- `DriftRecord` struct: Timestamp, AbsDeltaTempC, AbsDeltaPrecipProb
- `RecordDrift(city, dayOffset, shift, dataRoot)` — append до 10 последних записей в `data/drift/{city}_d{dayOffset}.json`
- `ComputeDriftFactor(records []DriftRecord) float64` — экспоненциальное взвешенное среднее нестабильности:
  instability_i = clamp(|ΔTemp|/10 + |ΔPrecip%|/40, 0, 1); DriftFactor = clamp(1.0 - 0.30 × avg, 0.70, 1.00)
- `DriftFactor(city, dayOffset, dataRoot) float64` — загружает историю и вычисляет фактор
- В AggregateForDay(): после DetectForecastShift → RecordDrift + ff.Confidence *= DriftFactor
- 9 unit-тестов: TestComputeDriftFactor_AllStable/HighDrift/Empty/FloorRespected/SingleRecord/RecentWeightedMore, TestRecordDrift_PersistsAndCaps/NilShiftNoOp, TestLoadDriftSummary_Empty

### [x] 2026-05-28 — TASK-126: `dashboard drift` — таблица стабильности прогнозов
**Файл:** `cmd/dashboard/main.go` (обновить)
- Новый sub-command: `go run ./cmd/dashboard drift`
- Для каждого города показывает drift factor по day-0 и day-1: City | D+0 Factor | D+1 Factor | Last ΔTemp | Last ΔPrecip% | Stability
- Stability label: "stable" (factor ≥ 0.95), "moderate" (0.85–0.95), "unstable" (<0.85)
- Помогает оператору понять какие города сейчас в состоянии метеорологической неопределённости

---

## 🔴 ПРИОРИТЕТ 27 — Новые улучшения (добавлено 2026-05-28)

### [x] 2026-05-28 — TASK-127: Signal-type exposure concentration guard
**Файлы:** `internal/risk/signal_concentration.go` (новый), `internal/risk/signal_concentration_test.go` (новый), `internal/risk/risk.go` (обновить), `cmd/bot/main.go` (обновить)
Текущий риск-менеджер не контролирует суммарную экспозицию по типу сигнала. Если у нас 70% USDC в "rain" ставках, а модель дождя систематически ошибается — все ставки проигрывают одновременно.
- `CheckSignalConcentration(records, signal, newSize) error` — считает (sигнал_exposure + newSize) / (total_exposure + newSize); если > MaxSignalExposurePct → error
- `SignalExposureBreakdown(records) map[string]float64` — суммарный USDC по сигналам для аналитики
- `MaxSignalExposurePct float64` в Config (default: 0.40, т.е. не более 40% в одном типе)
- Вызывать в cmd/bot после CheckCorrelation
- 8 unit-тестов: пустая история, один сигнал, несколько сигналов, граничный случай, disabled (0), breakdown функция

### [x] 2026-05-28 — TASK-128: CLOB depth-weighted fair-value enrichment
**Файлы:** `internal/markets/fair_value.go` (новый), `internal/markets/fair_value_test.go` (новый), `internal/markets/markets.go` (обновить), `internal/markets/liquidity.go` (обновить)
Текущий `m.YesPrice` берётся из Gamma API как последняя цена — может быть stale. VWAP по лучшим N уровням CLOB даёт более точную оценку справедливой цены и улучшает расчёт edge.
- `DepthWeightedPrice(levels []bookLevel, topN int) float64` — VWAP по topN=5 уровням
- `FetchFairValue(tokenID string) (fairYes, fairNo float64, err error)` — запрос CLOB, вычисление VWAP для bid/ask, mid = (vwap_bid + vwap_ask) / 2
- Добавить `FairYesPrice, FairNoPrice float64` в Market struct
- В `EnrichWithLiquidity()` — вызывать FetchFairValue и заполнять поля
- В `strategy.EvaluateFused()` — использовать FairYesPrice если != 0
- Тесты с mock httptest.Server

### [x] 2026-05-28 — TASK-129: Dead-heat resolver — ставки на ничью в погодных рынках
**Файл:** `internal/strategy/deadheat.go` (новый)
Некоторые рынки сформулированы как "exactly X°C" или "between X and Y mm". Если наш прогноз близко к boundary — вероятность should быть ближе к 50% (dead-heat zone).
- `IsNearBoundary(ff *FusedForecast, m Market) bool` — true если прогноз в ±σ от порога
- `DeadHeatAdjust(p float64, distanceToThreshold, sigma float64) float64` — сжать вероятность к 0.5 пропорционально близости к порогу
- Вызывать в `evaluate()` перед seasonal correction
- Предотвращает ставки когда "coin flip" — снижает bankroll drain

---

## 🔴 ПРИОРИТЕТ 28 — Новые улучшения (добавлено 2026-05-28)

### [x] 2026-05-28 — TASK-130: Source consensus spread indicator — масштабирование confidence по межисточниковому согласию
**Файлы:** `internal/collectors/consensus.go` (новый), `internal/collectors/consensus_test.go` (новый), `internal/collectors/aggregator.go` (обновить)
Текущий confidence считается только по stddev осадков. Если 4 источника дают 20/25/22/23°C макс — они согласны. Если 15/25/35/20°C — значительное расхождение снижает достоверность прогноза.
- `ConsensusScore(values []float64) float64` — 1 - clamp(stddev/range, 0, 1); range = max-min. Возвращает 1.0 для 0-1 значений, 0.0 для максимального разброса
- `MultiDimConsensus(temps, precips, winds []float64) float64` — взвешенное среднее consensus по трём измерениям (temp weight 0.5, precip 0.3, wind 0.2)
- Добавить `ConsensusScore float64` в FusedForecast struct
- В `fuse()` — вычислять consensus по per-source MaxTempC/PrecipProb/WindSpeed; `ff.Confidence *= math.Sqrt(consensus)` (смягчающая функция чтобы не слишком агрессивно снижать)
- В `dashboard explain` — выводить "consensus: X.XX (temp±Xσ, precip±Xσ)"
- 7 unit-тестов: perfect consensus, total disagreement, two sources, empty, single, mixed dims, floor at 0

### [x] 2026-05-28 — TASK-131: Auto-blacklist city+signal по убыточной истории
**Файлы:** `internal/markets/auto_blacklist.go` (новый), `internal/markets/auto_blacklist_test.go` (новый), `cmd/bot/main.go` (обновить), `config/config.go` (обновить)
Если пара (city, signal) систематически теряет деньги — автоматически добавлять в blacklist на N дней.
- `AutoBlacklistCheck(records []calibration.BetRecord, city, signal, dataRoot string, cfg AutoBlacklistCfg) error` — анализирует последние MinBets resolved ставок для city+signal; если net PnL < -LossThreshold USDC → записывает в data/auto_blacklist.json с timestamp+expiry
- `IsAutoBlacklisted(city, signal, dataRoot string) bool` — проверяет активную запись (не истёкшую)
- `AutoBlacklistStatus(dataRoot string) []AutoBlacklistEntry` — список активных auto-blacklist записей для dashboard
- `AutoBlacklistCfg` struct: MinBets int (default 8), LossThresholdUSDC float64 (default -3.0), BlacklistDays int (default 3)
- Добавить в config.yaml: `auto_blacklist_min_bets`, `auto_blacklist_loss_usdc`, `auto_blacklist_days`
- Вызывать в cmd/bot перед CheckRisk — если IsAutoBlacklisted → skip с логом "auto-blacklisted"
- `dashboard blacklist` — показывать manual + auto blacklist вместе

### [x] 2026-05-28 — TASK-132: Rolling win rate monitor — алерт при деградации метрик
**Файлы:** `internal/calibration/rolling_winrate.go` (новый), `internal/calibration/rolling_winrate_test.go` (новый), `cmd/bot/main.go` (обновить)
Brier score хорош для долгосрочного трекинга, но медленно реагирует на резкую деградацию. Rolling win rate за последние 20 ставок — быстрый сигнал тревоги.
- `ComputeRollingWinRate(records []BetRecord, window int) (winRate float64, sampleSize int)` — берёт последние `window` resolved ставок, считает процент победных; возвращает (-1, 0) если resolved < 5
- `WinRateAlert(records []BetRecord, window int, threshold float64) (bool, string)` — возвращает (true, message) если winRate < threshold
- В cmd/bot после каждого resolved update: вызывать WinRateAlert(records, 20, 0.35); если true — слать Telegram warning
- В Telegram /status: добавить строку `Win Rate 20: X% (N bets)` рядом с Brier score
- В dashboard stats: показывать rolling win rate таблицу по окнам [10, 20, 50]
- 8 unit-тестов: empty, too few resolved, all wins, all losses, mixed, exactly at threshold, window larger than history, negative P&L detection

---

## 🔵 НОВЫЕ ЗАДАЧИ — авто-добавлены 2026-05-28

### [x] 2026-05-28 — TASK-133: Time-of-day win rate tracker — timing multiplier для bet-size
**Файлы:** `internal/calibration/timing.go` (новый), `internal/calibration/timing_test.go` (новый), `cmd/bot/main.go` (обновить), `cmd/dashboard/main.go` (обновить)
Prediction-market liquidity и order-flow паттерны существенно различаются по времени суток UTC. Трекинг побед/поражений по UTC-часу позволяет масштабировать bet-size в зависимости от исторической производительности в данный час.
- `HourBucket` struct: Hour int, Wins int, Losses int — данные по одному UTC-часу
- `LoadHourlyStats(dataRoot)` / `RebuildHourlyStats(records, dataRoot)` / `UpdateHourlyStats(rec, dataRoot)` — CRUD для `data/hourly_winrate.json`
- `TimingMultiplier(buckets, hour) float64` — 1.0 + clamp(hourWR/globalWR - 1, -0.5, 0.2); диапазон [0.5, 1.2]; 1.0 если < 5 ставок в часу
- `TimingMultiplierNow(dataRoot) float64` — мультипликатор для текущего UTC-часа
- `HourlyTable(buckets)` — срез из 24 HourlyRow для отображения
- В cmd/bot: применять `timingMult` к `d.SizeUSDC` после Platt калибровки; RebuildHourlyStats при старте
- `dashboard timing` — таблица 24 часов: hour/wins/losses/total/win_rate/multiplier/signal; текущий час отмечен ▶
- 8 unit-тестов: no data, hour below min samples, average hour, bad hour, good hour, invalid hour, rebuild/load roundtrip, unresolved ignored

### [x] 2026-05-28 — TASK-134: Forecast horizon confidence decay — снижение confidence для дальних прогнозов
**Файлы:** `internal/collectors/horizon.go` (новый), `internal/collectors/horizon_test.go` (новый), `internal/collectors/aggregator.go` (обновить)
1-дневные прогнозы значительно точнее 5-дневных. Сейчас confidence считается только по межисточниковому согласию, но не учитывает горизонт прогноза (сколько часов до целевой даты). Добавить decay-функцию которая снижает confidence для дальних горизонтов.
- `HorizonDecay(targetDate time.Time, forecastedAt time.Time) float64` — decay factor ∈ [0.65, 1.0]. 0-24h → 1.0, 24-48h → 0.92, 48-72h → 0.84, 72-96h → 0.76, 96-120h → 0.70, >120h → 0.65
- `HorizonDecayLinear(horizonHours float64) float64` — упрощённая версия: max(0.65, 1.0 - horizonHours/400)
- Добавить `ForecastHorizonHours float64` в FusedForecast (вычислять в AggregateAll как time.Until(targetDate).Hours())
- В `fuse()`: `ff.Confidence *= HorizonDecayLinear(horizonHours)` после consensus correction
- В `dashboard explain`: колонка "Horizon" с значением вроде "+36h" и цвет-кодировка (зелёный ≤ 24h, жёлтый 24-72h, красный > 72h)
- 6 unit-тестов: same-day, 24h, 48h, 96h, 120h+, boundary check

### [x] 2026-05-28 — TASK-135: Market duplicate guard — предотвращение ставок на одно и то же погодное событие
**Файлы:** `internal/markets/duplicate_guard.go` (новый), `internal/markets/duplicate_guard_test.go` (новый), `cmd/bot/main.go` (обновить)
Иногда Polymarket создаёт несколько рынков на одно погодное событие (e.g. "Will NYC hit 90°F on July 4?" и "Will New York reach 90 degrees on the 4th of July?"). Ставки на оба — это двойная экспозиция без дополнительного edge.
- `MarketFingerprint(m Market) string` — canonical key: normalize(city) + "/" + signal + "/" + date(expiry). Например "new_york/heat/2026-07-04"
- `FindDuplicates(markets []Market) map[string][]string` — map[fingerprint][]conditionID с 2+ рынками
- `IsDuplicateOf(m Market, openBets []calibration.BetRecord) bool` — true если уже есть открытая ставка с тем же fingerprint
- В cmd/bot: вызывать IsDuplicateOf перед EvaluateFused; skip с логом "duplicate-market: already bet on same event"
- 6 unit-тестов: no duplicates, exact duplicate, different date, different signal, fingerprint normalization, open bets check

---

## 🔵 НОВЫЕ ЗАДАЧИ — авто-добавлены 2026-05-28 (ночная итерация)

### [x] 2026-05-28 — TASK-136: Duplicate-market Telegram alert — уведомление при обнаружении дублей
**Файлы:** `cmd/bot/main.go` (обновить), `internal/markets/duplicate_guard.go` (обновить)
Когда `FindDuplicates` находит рынки-дубликаты, бот молча их пропускает. Добавить Telegram-уведомление раз в день со списком обнаруженных дублей.
- Вызывать `FindDuplicates(mkt)` в начале каждого цикла
- Если найдены дубли — один раз в сутки слать в Telegram: "⚠️ Duplicate markets detected: new_york/heat/2026-07-04 (c1, c2)"
- Хранить `lastDuplicateAlert time.Time` в сессии и отправлять не чаще раза в 24ч
- 2 unit-теста: нет дублей → нет алерта, есть дубли → текст алерта

### [x] 2026-05-28 — TASK-137: Horizon decay в dashboard timing — отображение decay в timing table
**Файлы:** `cmd/dashboard/main.go` (обновить), `internal/calibration/timing.go` (обновить)
`dashboard timing` показывает hourly win rate, но не учитывает horizon decay. Добавить колонку HorizonDecay в timing table, чтобы видеть как decay влияет на effective confidence в разное время суток.
- В HourlyRow добавить `AvgHorizonHours float64` — средний горизонт ставок в этот час
- `TimingTable(buckets)` — добавить колонку "HorizonDecay" с `HorizonDecayLinear(avgHorizon)`
- В dashboard timing: отображать с цветом (зелёный ≥ 0.90, жёлтый 0.75–0.90, красный < 0.75)
- В dashboard timing: отображать с цветом (зелёный ≥ 0.90, жёлтый 0.75–0.90, красный < 0.75)

---

## 🔵 НОВЫЕ ЗАДАЧИ — авто-добавлены 2026-05-28 (утренняя итерация)

### [x] 2026-05-28 — TASK-138: Telegram /forecast команда — быстрый просмотр прогноза из Telegram
**Файлы:** `internal/notifier/telegram_commands.go` (обновить)
- Команда `/forecast [city]` — возвращает текущий FusedForecast из кэша (или fallback OpenMeteo) для указанного города
- Без аргумента — показывает сводку по 5 ключевым городам (new_york, london, paris, miami, berlin) одним сообщением
- Формат для одного города: MaxTemp/MinTemp°C, Precip mm (X%), Wind km/h, Cloud%, Confidence X%, Sources, Age (updated Xmin ago)
- Если AlertLevel > 0 для US городов — добавлять строку "⚠️ NWS: <event_name>"
- Добавить `/forecast` в help-текст команды `/status` (подсказка для пользователя)
- Fallback: если нет кэша — вызывать `weather.GetForecast(city, 1)` напрямую
- 3 unit-теста: `TestForecastMsg_OneCity`, `TestForecastMsg_AllCities`, `TestForecastMsg_InvalidCity`

### [x] 2026-05-28 — TASK-139: Win/loss streak detector — алерт при серии поражений
**Файлы:** `internal/calibration/streaks.go` (новый), `internal/calibration/streaks_test.go` (новый), `cmd/bot/main.go` (обновить), `internal/notifier/telegram_commands.go` (обновить)
- `ComputeStreak(records []BetRecord) (current int, kind string)` — текущая серия: +N (wins) или -N (losses)
- `StreakAlert(records []BetRecord, alertLen int) (bool, string)` — true если losing streak ≥ alertLen (default 4)
- В cmd/bot: после каждого resolved update — вызывать StreakAlert; если true → Telegram предупреждение "🚨 Loss streak: N consecutive losses — consider pausing"
- В /status Telegram response: добавить строку "Streak: +3 wins" или "Streak: -2 losses"
- Метрика помогает быстрее замечать деградацию стратегии чем Brier score
- 6 unit-тестов: empty, all unresolved, wins only, losses only, mixed ending in win, streak alert threshold

### [x] 2026-05-28 — TASK-140: `dashboard freshness` — таблица свежести прогнозов
**Файл:** `cmd/dashboard/main.go` (обновить)
- Новый sub-command: `go run ./cmd/dashboard freshness`
- Использует `collectors.ForecastCacheStats(dataRoot)` для получения возраста каждого кэш-файла
- Таблица: City+Day | Last Updated | Age | Status
- Status: "fresh" (< 1h), "ok" (1-3h), "stale" (> 3h), "missing" (нет файла)
- Цвет: зелёный / жёлтый / красный
- Итог: "N cities fresh, M stale, K missing"
- Быстрый способ проверить состояние прогнозных данных перед запуском бота

---

## 🔴 ПРИОРИТЕТ 29 — Новые улучшения (добавлено 2026-05-28)

### [x] 2026-05-28 — TASK-141: Fee-adjusted Kelly sizing — учёт протокольных комиссий Polymarket
**Файлы:** `config/config.go` (обновить), `config/config.yaml` (обновить), `internal/strategy/strategy.go` (обновить), `cmd/bot/main.go` (обновить), `internal/strategy/strategy_test.go` (обновить)
Polymarket берёт 2% комиссии с прибыли при выигрышном исходе. Текущий Kelly не учитывает это — размер ставок завышен на marginal-edge рынках. При edge=0.05 fee уменьшает реальный EV на ~10–15%, что при систематической торговле выражается в реальных потерях.
- Добавить `ProtocolFeeRate float64` в Config (yaml: `protocol_fee_rate`, env: `PROTOCOL_FEE_RATE`, default: 0.02)
- Добавить `ProtocolFeeRate = 0.02` как package-level var в `internal/strategy/strategy.go` (аналогично KellyFraction)
- В `halfKelly()`: `b := (odds - 1)` → `b := (odds - 1) * (1 - ProtocolFeeRate)` — fee-adjusted net profit per dollar
- В `cmd/bot/main.go`: `strategy.ProtocolFeeRate = cfg.ProtocolFeeRate`
- Добавить `strategy.ProtocolFeeRate = 0` в `strategy_test.go` TestMain/setUp чтобы тесты не зависели от global state
- Добавить тест `TestHalfKellyFeeAdjusted`: при fee=0.02 размер позиции должен быть < чем при fee=0
- Логировать в Decision.Reason: `fee_rate=2%` когда fee > 0
- Эффект: Kelly корректно уменьшает размер ставки пропорционально реальной комиссии

### [x] 2026-05-28 — TASK-142: OpenMeteo snowfall direct signal — точный снежный прогноз через snowfall_sum
**Файлы:** `internal/collectors/openmeteo.go` (обновить), `internal/weather/weather.go` (обновить), `internal/strategy/strategy.go` (обновить)
Текущий snow signal: `(1 - HeatProbability(2°C)) × RainProbability × 0.8` — это грубая оценка. Open-Meteo daily API уже возвращает `snowfall_sum` (cm) напрямую. Использовать его для точного вычисления SnowProbability.
- Добавить `SnowfallCM float64` в `weather.Forecast`
- Фетчить `snowfall_sum` в `GetForecast()` из Open-Meteo daily API (уже запрашивается, но не парсится)
- `SnowProbability(f Forecast) float64` — вероятность снегопада: 0cm→0.02, 0-2cm→0.25, 2-5cm→0.60, >5cm→0.85, >10cm→0.95
- В strategy.go case "snow": использовать `weather.SnowProbability(f)` вместо `coldP * rainP * 0.8`
- Сохраняем fallback: если SnowfallCM == 0 и данные неполные — используем старую формулу
- Тест: `TestSnowProbability` — проверить все диапазоны + fallback

### [x] 2026-05-28 — TASK-143: `bot --validate` флаг — проверка конфига и API connectivity перед запуском
**Файл:** `cmd/bot/main.go` (обновить)
Операторы тратят время на отладку запусков которые ломаются из-за неправильного конфига или недоступных API. `--validate` проверяет всё это быстро, без real-side effects.
- Флаг `--validate` (bool) — если задан, проверить и выйти с кодом 0 (всё ок) или 1 (есть ошибки)
- Проверки:
  1. `config.Validate(cfg)` — существующая функция (errors = fatal, warnings = info)
  2. HTTP GET Open-Meteo для первого города с timeout 3s — проверка weather API
  3. HTTP GET Gamma API `/markets?limit=1` — проверка Polymarket connectivity
  4. Если `cfg.TelegramBotToken != ""` — GET /getMe — проверка Telegram bot token
  5. Если `cfg.PolyPrivateKey != ""` — проверить что ключ парсируется (не делать реальный запрос)
- Вывод: `[OK] config`, `[OK] openmeteo`, `[FAIL] gamma_api: connection refused`
- Позволяет в Dockerfile HEALTHCHECK использовать `./bot --validate`

---

## 🔴 ПРИОРИТЕТ 24 — Новые улучшения (добавлено 2026-05-28)

### [x] 2026-05-28 — TASK-144: `dashboard summary` — единая страница состояния бота
**Файл:** `cmd/dashboard/main.go` (обновить)
- Новый sub-command `dashboard summary` — одним взглядом видно всё
- Секции:
  1. **Bankroll**: текущий / пик / просадка% / мультипликатор
  2. **Performance**: Brier score (+ оценка), win rate, Sharpe (30d), resolved/open bets
  3. **Today**: ставок сегодня, realized P&L сегодня, unrealized PnL открытых позиций
  4. **Streak**: текущая серия побед/поражений
  5. **Top cities** (топ-3 по win rate, ≥3 bets)
  6. **Top signals** (топ-3 по win rate, ≥3 bets)
  7. **Source health** (краткая строка: ✅/⚠️/❌ per source)
  8. **Recent bets** (последние 5 с исходом)
- Цветовое кодирование через fatih/color (уже используется в dashboard)
- `go run ./cmd/dashboard summary`

### [x] 2026-05-28 — TASK-145: `dashboard compare` — сравнение двух периодов
**Файл:** `cmd/dashboard/main.go` (обновить)
- `dashboard compare --days=7` — сравнивает текущие N дней с предыдущими N днями
- Метрики: win rate, avg edge, total bets, P&L, Brier score per period
- Символы: ▲/▼/= показывают улучшение/ухудшение/без изменений
- Помогает понять тренд: стратегия улучшается или ухудшается?

### [x] 2026-05-28 — TASK-146: Telegram `/summary` команда — быстрый обзор прямо из чата
**Файл:** `internal/notifier/telegram_commands.go` (обновить)
- Команда `/summary` — краткая версия `dashboard summary` в Telegram (≤4096 символов)
- Показывает: bankroll, Brier, win rate, Sharpe, streak, today's bets
- Форматирование: `<pre>` блок для моноширинного выравнивания
- Переиспользует функции из calibration (LoadHistory, LoadBankroll, etc.)

---

## 🔴 ПРИОРИТЕТ 30 — Новые улучшения (добавлено 2026-05-28)

### [x] 2026-05-28 — TASK-147: Calibration drift detector — алерт при деградации Brier score
**Файлы:** `internal/calibration/drift.go` (новый), `internal/calibration/drift_test.go` (новый), `cmd/bot/main.go` (обновить)
Brier score отражает среднюю точность за всё время — он медленно реагирует на деградацию. Нужен детектор дрейфа: сравнить Brier за последние 14 дней vs. предыдущие 14 дней. Если ухудшение > 15% — алерт.
- `BrierWindow(records []BetRecord, days int) (score float64, count int)` — Brier за последние N дней (по `ResolvedAt`)
- `DriftAlert(records []BetRecord, recentDays, baseDays int, threshold float64) (bool, string)` — true если recent Brier > base Brier * (1+threshold)
- В `cmd/bot/main.go`: вызывать `DriftAlert` после каждого resolve-цикла; при true → `notifier.NotifyError`
- 5 unit-тестов: empty, all unresolved, no drift, drift detected, insufficient recent data
- Логировать: "[calibration] drift check: recent=0.1234 base=0.0987 ratio=1.25 → ALERT"

### [x] 2026-05-28 — TASK-148: Multi-source consensus confidence band — visual spread indicator in dashboard
**Файлы:** `cmd/dashboard/main.go` (обновить), `internal/aggregation/aggregation.go` (проверить наличие поля Spread/Stddev)
Когда источники прогноза сильно расходятся (NASA даёт 18°C, OpenMeteo 28°C), confidence должен отражать это расхождение. Сейчас есть `SourceConsensusSpread` — покажем его в dashboard forecast.
- В `dashboard forecast`: добавить колонку "Spread" — межисточниковое стд. отклонение по temperature
- Цвет: зелёный (spread < 2°C), жёлтый (2-5°C), красный (> 5°C)
- Показывать сколько источников участвовало в агрегации (N sources)

### [x] 2026-05-28 — TASK-149: Adaptive bet cooldown per city — пауза после ставки
**Файлы:** `internal/risk/risk.go` (обновить), `config/config.go` (обновить)
После размещения ставки на city+signal не ставить туда же в течение `BetCooldownHours` (default: 4h).
- `IsCoolingDown(city, signal string, positions []Position, cooldownHours int) bool`
- Проверять в risk.go перед ApproveOrder
- Конфиг: `bet_cooldown_hours: 4` (yaml + env)
- 3 unit-теста: no previous bet, just placed, past cooldown

### [x] 2026-05-28 — TASK-150: P&L chart ASCII — гистограмма ежедневного P&L в Telegram /summary
**Файлы:** `internal/notifier/telegram_commands.go` (обновить), `internal/calibration/pnlchart.go` (новый)
Покажем историю последних 14 дней P&L как ASCII chart (sparkline → bar chart).
- `DailyPnLBars(records []BetRecord, days int) string` — строка типа "▂▄█▆▃▁▅▇▂▄▆▃▁▃" (14 символов)
- Нормализовать по max(|PnL|) в диапазон. Положительные дни = ▲, отрицательные = ▼ или пустые
- В `/summary` добавить строку "P&L 14d: ▂▄█▆▃▁▅▇▂▄ +$12.30"

---

## 🔴 ПРИОРИТЕТ 40 — Новые улучшения (добавлено 2026-05-28)

### [x] 2026-05-28 — TASK-151: `/signals` Telegram команда — breakdown производительности по типу сигнала
**Файл:** `internal/notifier/telegram_commands.go` (обновить)
Операторы не видят какие сигналы работают, а какие нет. Нужна команда `/signals` — таблица win rate + Brier по каждому сигналу (rain, heat, cold, snow, sunny, wind, fog, uv, humid, dry).
- `handleSignals(bcfg BotConfig) string` — загружает историю, вызывает `calibration.SignalBreakdown(records)`, форматирует таблицу
- Колонки: сигнал | N бет | win% | Brier | P&L USDC
- Сортировка по win% descending (лучшие сигналы сверху)
- Цветовые эмодзи: 🟢 win>55%, 🟡 45-55%, 🔴 <45%
- Требует ≥3 resolved бет для строки (иначе "–")
- P&L считается из SizeUSDC и Outcome: win → +SizeUSDC*(1/MarketPrice-1), loss → -SizeUSDC
- Добавить `/signals` в help-текст бота и обработчик в `StartCommandPoller`

### [x] 2026-05-28 — TASK-152: Dynamic bankroll Kelly scaling — размер ставки растёт с банкроллом
**Файл:** `internal/calibration/bankroll_kelly.go` (новый), `cmd/bot/main.go` (обновить)
Текущий MaxKellyFraction (5%) фиксирован. Когда банкролл вырастает в 2x+, стоит разрешить немного больший Kelly. Когда банкролл падает, уменьшить его.
- `BankrollKellyScale(current, initial float64) float64` — возвращает multiplicative factor
  - ratio = current/initial
  - ratio < 0.70 → scale = 0.70 (защита при просадке)
  - ratio 0.70-1.00 → linear interpolation к 1.0
  - ratio 1.00-2.00 → linear interpolation к 1.20 (умеренный рост)
  - ratio > 2.00 → scale = 1.20 (не более)
- Применять к MaxKellyFraction каждый цикл
- `InitialBankroll float64` в Config (yaml: `initial_bankroll`, env: `INITIAL_BANKROLL`, default: 0 → использовать текущий при первом запуске)
- 4 unit-теста: deep drawdown, slight drawdown, no change, 2x growth

### [x] 2026-05-28 — TASK-153: `/watchlist add|remove|list` Telegram команда — пинить конкретные рынки
**Файл:** `internal/notifier/telegram_watchlist.go` (новый), `cmd/bot/main.go` (обновить)
Операторы хотят пинить конкретные Polymarket conditionID для всегда-evaluate, даже если они вне обычного discovery window.
- `data/watchlist.json` — список conditionID строк
- `LoadWatchlist(dataRoot) []string`, `SaveWatchlist(dataRoot, ids) error`
- Telegram команды: `/watchlist list`, `/watchlist add <conditionID>`, `/watchlist remove <conditionID>`
- В bot main loop: объединять watchlist conditionID с обычными discovered markets (без дублей)
- 3 unit-теста: load empty, add/save/load roundtrip, remove existing

### [x] 2026-05-28 — TASK-154: Bet export CSV через `/export` Telegram команда
**Файл:** `internal/notifier/telegram_commands.go` (обновить)
- `/export [days]` — отправляет CSV файл с историей ставок за последние N дней (default 30)
- Использует Telegram sendDocument API
- Формат: те же колонки что в bets_history.csv + дополнительный столбец "pnl_usdc"
- Фильтрует только resolved bets для экспорта
- Если ≥ 50 строк — сжимает через gzip

### [x] 2026-05-28 — TASK-155: Forecast freshness guard — пропуск ставки если прогноз устарел >N часов
**Файл:** `internal/collectors/freshness.go` (новый), `cmd/bot/main.go` (обновить)
- `ForecastAge(ff *FusedForecast) time.Duration` — время с момента последнего обновления прогноза
- `IsForecastStale(ff *FusedForecast, maxAgeHours float64) bool`
- `MaxForecastAgeHours` уже есть в Config — подключить логику проверки перед каждой ставкой в main loop
- Если прогноз устарел: логировать warning, пропускать ставку, инкрементировать метрику `stale_forecasts_skipped`
- 3 unit-теста: fresh, exactly on boundary, stale

---

## 🔴 ПРИОРИТЕТ 50 — Новые улучшения (добавлено 2026-05-28)

### [x] 2026-05-28 — TASK-156: Per-signal adaptive Kelly multiplier
**Файлы:** `internal/calibration/signal_kelly.go` (новый), `internal/calibration/signal_kelly_test.go` (новый), `cmd/bot/main.go` (обновить)
Сигналы с хорошим историческим Brier score получают более высокий Kelly cap; плохо откалиброванные — снижаются автоматически.
- `SignalKellyMultipliers(records []BetRecord) map[string]SignalKellyInfo` — вычислить мультипликатор для каждого сигнала из истории
- `SignalKellyInfo{Multiplier, BrierScore, Count}` — структура с мультипликатором и статистикой
- Таблица Brier→multiplier: <0.10→1.50x, <0.14→1.20x, <0.18→1.0x, <0.22→0.75x, ≥0.22→0.50x
- Мин. 10 resolved bets для активации (иначе 1.0x)
- В main.go: вычислять раз в цикл; применять после Platt calibration + timing multiplier
- Добавить в Decision.Reason: "signal_kelly=1.20x(rain,brier=0.09,n=42)"
- 7 unit-тестов в signal_kelly_test.go

### [x] 2026-05-28 — TASK-157: `/healthcheck` Telegram команда — комплексный обзор системы
**Файлы:** `internal/notifier/telegram_commands.go` (обновить)
Оператор отправляет `/healthcheck` и получает полный health-отчёт прямо в Telegram:
- Uptime и статус (running/PAUSED)
- Статус источников данных из source_health.json (✅/⚠️/❌, возраст последнего успешного fetch, uptime%)
- Brier score + drift alert + streak alert
- Ежедневные риск-показатели: bets today, daily P&L, open positions, bankroll
- Per-signal Kelly мультипликаторы (только те, что отличаются от 1.0x)
- Добавить `StartTime time.Time` в BotConfig; заполнять из `sess.startTime` в bot/main.go

---

## 🔴 ПРИОРИТЕТ 60 — Новые улучшения (добавлено 2026-05-28)

### [x] 2026-05-28 — TASK-158: Precipitation climatological z-score — аномальные осадки → boost confidence
**Файлы:** `internal/weather/seasonal.go` (добавить), `internal/collectors/aggregator.go` (добавить поле + применить)
Аналог TASK-064 (ClimateAnomalyScore для температуры), но для осадков: измеряем насколько прогнозируемые осадки отличаются от исторической нормы города. Когда z > 2.0 (сильная дождевая аномалия), confidence повышается до 0.72 floor.
- `precipHistShape` struct для десериализации data/historical/{city}.json (поле PrecipitationMM)
- `PrecipitationZScore(city, precipMM, dataRoot) float64` — возвращает знаковый z-score, zажат в [-5, +5], 0 когда данных < 7 или sigma < 0.5mm
- Поле `PrecipZScore float64` в `FusedForecast`
- В `AggregateForDay`: вычислить и сохранить PrecipZScore; если > 2.0 → confidence floor 0.72
- 4 unit-теста в seasonal_test.go: missing file, too few records, dry city (sigma < 0.5), heavy rain anomaly

### [x] 2026-05-28 — TASK-159: `dashboard markets` — таблица активных Polymarket рынков
**Файл:** `cmd/dashboard/main.go` (добавить `cmdMarkets`, case в switch, printUsage)
Операторам нужен быстрый обзор всех обнаруженных погодных рынков: какие активны, какие тонкие/стейл, спред, время истечения.
- `cmdMarkets(dataRoot)` — вызывает `markets.GetWeatherMarkets()` + `markets.EnrichWithLiquidity(mks)`
- Таблица: City | Signal | YES | NO | Spread | Status | Expiry (hours left)
- Status: 🟢 Active / 🟡 Thin / 🔴 Stale
- Сортировка: сначала Active, по City+Signal
- Счётчик внизу: N total, K active, M thin, J stale

### [x] 2026-05-28 — TASK-160: `/source-weights` Telegram команда — текущие динамические веса источников
**Файл:** `internal/notifier/telegram_commands.go` (добавить `handleSourceWeights`, case в switch)
Показать какой вес сейчас у каждого источника данных (dynamic vs static), Brier score источника, кол-во измерений.
- `handleSourceWeights(bcfg BotConfig) string` — загружает source_accuracy.json → `DynamicWeights()` → форматирует таблицу
- Колонки: источник | текущий вес | базовый вес | Brier | N прогнозов
- Если динамические веса не активны (< минимума данных) — показать "using static weights"
- Добавить `/source-weights` в docstring и поллер

---

## 🔴 ПРИОРИТЕТ 70 — Новые улучшения (добавлено 2026-05-28)

### [x] 2026-05-28 — TASK-161: `CityPnL` + `dashboard pnl-city` — P&L breakdown по городам
**Файлы:** `internal/calibration/pnl_city.go` (новый), `internal/calibration/pnl_city_test.go` (новый), `cmd/dashboard/main.go` (обновить)
Добавить функции `CityPnL(records)` и `SignalPnL(records)` → `[]CityPnLStats{City, Bets, Wins, PnLUSDC, TotalRisked}`. Subcommand `dashboard pnl-city` — таблица по городам, сортировка по P&L descending, итоговая строка TOTAL.
- `WinRate() float64`, `ROI() float64` методы на `CityPnLStats`
- 6 unit-тестов: empty, skip unresolved, skip no city, win/loss sort, winrate/ROI, signal basic
- `go run ./cmd/dashboard pnl-city`

### [x] 2026-05-28 — TASK-162: `MinBetUSDC` конфиг — настраиваемый минимальный размер ставки
**Файлы:** `config/config.go` (обновить), `internal/strategy/strategy.go` (обновить), `cmd/bot/main.go` (обновить)
Добавить `min_bet_usdc: 0.50` в Config (yaml + env `MIN_BET_USDC`). Package-level var `strategy.MinBetUSDC = 0.50`. Заменить 4 хардкода `< 0.5` в strategy.go на `< MinBetUSDC`. Инициализировать из cfg в cmd/bot/main.go.
- Конфиг: `min_bet_usdc`, ENV: `MIN_BET_USDC`, default: 0.50

### [x] 2026-05-28 — TASK-163: Telegram `/pnl-city` команда — P&L по городам из чата
**Файл:** `internal/notifier/telegram_commands.go` (обновить)
Команда `/pnl-city` — таблица `handlePnLCity(bcfg)` в `<pre>` блоке: city | bets | wins | win% | pnl | roi%. Переиспользует `calibration.CityPnL()`. Итоговая строка TOTAL. Добавить в docstring и поллер.

---

## 🔴 ПРИОРИТЕТ 80 — Новые улучшения (добавлено 2026-05-28)

### [x] 2026-05-28 — TASK-164: `dashboard week` — 7-дневная таблица P&L по дням
**Файл:** `cmd/dashboard/main.go` (добавить `cmdWeek`, case в switch)
Операторам нужна детальная таблица по каждому дню: сколько ставок, побед, P&L, накопленный итог.
- `DailyPnLTable(records []BetRecord, nDays int) []DailyStats` в `internal/calibration/pnlchart.go`
- `DailyStats{Date, Bets, Wins, PnLUSDC, CumulativePnL}`
- `cmdWeek(dataRoot)` — таблица: Date | Bets | Win% | P&L | Running
- Цвет: зелёный для прибыльных дней, красный для убыточных
- Итоговая строка TOTAL + sparkline внизу
- `go run ./cmd/dashboard week`

### [x] 2026-05-28 — TASK-165: Proactive opportunity alert — Telegram уведомление при сильном сигнале
**Файлы:** `internal/notifier/telegram_commands.go` (новая функция), `cmd/bot/main.go` (интеграция), `config/config.go` (новое поле)
Когда edge рынка > `OpportunityAlertThreshold` (по умолчанию 2x min_edge), немедленно уведомлять в Telegram даже без ставки — полезно в semi-manual режиме или dry-run.
- `OpportunityAlertThreshold float64` в Config (yaml `opportunity_alert_threshold`, ENV `OPPORTUNITY_ALERT_THRESHOLD`, default 0.0=disabled)
- `notifiedOpportunities sync.Map` — дедуплицировать по conditionID (один алерт на рынок за сессию)
- `SendOpportunityAlert(token, chatID, d Decision)` — форматированное сообщение: 🔔 Market/city/signal/edge/confidence/price
- В main loop: после EvaluateFused, если edge > threshold и !dry-run-only → SendOpportunityAlert
- Добавить в .env.example: `OPPORTUNITY_ALERT_THRESHOLD=0.15`

### [x] 2026-05-28 — TASK-166: `internal/calibration/weekly.go` — недельная агрегация P&L + тест
**Файл:** `internal/calibration/weekly.go` (новый), `internal/calibration/weekly_test.go` (новый)
Логика вычисления недельных метрик отдельно от dashboard для переиспользования.
- `WeeklyStats{WeekStart, Bets, Wins, PnLUSDC, BrierScore}` struct
- `WeeklyBreakdown(records []BetRecord, nWeeks int) []WeeklyStats` — группировка по ISO неделям
- `BestWeek(stats []WeeklyStats) WeeklyStats`, `WorstWeek(stats []WeeklyStats) WeeklyStats`
- 5 unit-тестов: empty, single bet, cross-week boundary, best/worst week selection

---

## 🔴 ПРИОРИТЕТ 90 — Новые улучшения (добавлено 2026-05-28)

### [x] 2026-05-28 — TASK-167: `/explain <conditionID>` Telegram команда — аудит конкретного рынка
**Файл:** `internal/notifier/telegram_commands.go` (добавить `handleExplainMarket`, case в switch)
Оператор отправляет `/explain 0xabc123` и получает полный пайплайн решения: уверенность, вероятности, edge, Kelly-размер, причина BET/SKIP. Переиспользует существующий `strategy.ExplainEvaluate`.
- `handleExplainMarket(bcfg BotConfig, conditionID string) string` — ищет рынок среди GetWeatherMarkets(), берёт кэшированный прогноз, запускает ExplainEvaluate, форматирует в HTML
- Если рынок не найден → "❌ Market <id> not found in current discovery window"
- Формат: город/сигнал/цена → confidence, RawP→SeasonP→FinalP, YES/NO edge, Kelly raw, ens_scale, action
- Добавить `/explain <conditionID>` в docstring и poller switch
- Обновить printUsage в cmd/dashboard/main.go нет, только Telegram

### [x] 2026-05-28 — TASK-168: `dashboard signals-trend` — тренд win rate по сигналам (7/14/30 дней)
**Файл:** `cmd/dashboard/main.go` (добавить `cmdSignalsTrend`, case в switch)
Показать как изменился win rate каждого сигнала за три окна: 7/14/30 дней.
- `SignalBreakdownForPeriod(records []BetRecord, days int) map[string]BreakdownStats` в `internal/calibration/calibration.go` — фильтрует resolved ставки за последние N дней
- `cmdSignalsTrend(dataRoot)` — таблица: Signal | 7d WR | 14d WR | 30d WR | 30d Brier | Bets
- Стрелка ↑/↓ если 7d > 30d WR (тренд улучшения/ухудшения)
- Цвет: зелёный если WR > 55%, красный если WR < 45%
- `go run ./cmd/dashboard signals-trend`

### [x] 2026-05-28 — TASK-169: `/winrate` Telegram команда — rolling win rate по сигналам
**Файл:** `internal/notifier/telegram_commands.go` (добавить `handleWinRate`, case в switch)
Telegram команда `/winrate` показывает rolling win rate за последние 20 бетов для каждого сигнала в виде таблицы `<pre>`.
- `handleWinRate(bcfg BotConfig) string` — загружает историю, группирует по Signal, вычисляет WinRate/Count/PnL за последние 20 resolved бетов каждого сигнала
- Колонки: Signal | N | Win% | PnL
- Итог: total N, overall Win%, total PnL
- Добавить `/winrate` в docstring и поллер

---

## 🔴 ПРИОРИТЕТ 100 — Новые улучшения (добавлено 2026-05-28)

### [x] 2026-05-28 — TASK-170: Market discovery caching — кэш рынков на диск с TTL 1h
**Файлы:** `internal/markets/cache.go` (новый), `internal/markets/markets.go` (обновить), `cmd/bot/main.go` (обновить)
Кэшировать результат `GetWeatherMarkets()` в `data/markets_cache.json` с TTL 1 час. Ускоряет рестарты (обычно 10-30 секунд API calls) и снижает нагрузку на Polymarket API.
- `SetCacheDataRoot(root string)` — вызвать при старте бота с cfg.DataRoot
- `loadCache() ([]Market, bool)` — читать, проверять TTL, возвращать false если просрочен
- `saveCache(markets []Market)` — записывать после успешного фетча
- Вставить вызовы в начало/конец `GetWeatherMarkets()`

### [x] 2026-05-28 — TASK-171: Losing streak Kelly reducer — снижать ставки при серии проигрышей
**Файлы:** `internal/calibration/streak.go` (новый), `internal/calibration/streak_test.go` (новый), `cmd/bot/main.go` (обновить)
Детектировать серию consecutive проигрышей в истории ставок. При 2 проигрышах подряд → Kelly ×0.85, при 3+ → Kelly ×0.70. Сбрасывать до ×1.0 при первой победе.
- `StreakResult{Count int, IsWin bool}` struct
- `CurrentStreak(records []BetRecord) StreakResult`
- `StreakKellyFactor(s StreakResult) float64` → 0.70 / 0.85 / 1.0
- В `cmd/bot/main.go`: применять `streakKelly * strategy.KellyFraction` аналогично drawdownMult
- 5 unit-тестов: пустая история, одна победа, 2 поражения, 3 поражения, сброс после победы

### [x] 2026-05-28 — TASK-172: `/config` Telegram команда — показать текущий конфиг бота
**Файл:** `internal/notifier/telegram_commands.go` (обновить)
Оператор отправляет `/config` и видит ключевые параметры текущего запущенного бота: min_edge, max_bet, kelly, bankroll, dry_run и пр.
- `handleConfig(bcfg BotConfig) string` — форматирует `<pre>` таблицу с ключевыми полями из BotConfig
- Добавить `/config` в docstring и поллер

### [x] 2026-05-28 — TASK-173: `dashboard positions` — таблица открытых позиций
**Файл:** `cmd/dashboard/main.go` (обновить)
`go run ./cmd/dashboard positions` — показать все открытые (unresolved) ставки из истории.
- `cmdPositions(dataRoot)` — фильтрует `calibration.LoadHistory()` где Resolved=false
- Таблица: Time | Market/City/Signal | Side | Size | Price | Hours Left
- Итог: N open, total exposure USDC
- Сортировка по времени истечения (ближайшие сначала)

---

## 🔴 ПРИОРИТЕТ 110 — Новые улучшения (добавлено 2026-05-28)

### [x] 2026-05-28 — TASK-174: Weather model bias tracker — систематическая коррекция смещения вероятностей
**Файлы:** `internal/calibration/bias.go` (новый), `internal/calibration/bias_test.go` (новый), `internal/calibration/resolver.go` (обновить), `cmd/bot/main.go` (обновить), `cmd/dashboard/main.go` (обновить)
После каждого resolved бета: сохранять (ourP, outcome) в `data/bias/{city}_{signal}.json` (rolling window 30).
- `RecordBiasOutcome(city, signal string, ourP float64, won bool, dataRoot string) error`
- `ComputeBias(city, signal, dataRoot string) float64` — mean(ourP - outcome); + = переоцениваем; 0 если < 5 записей
- `CorrectProbability(city, signal, ourP, dataRoot) (correctedP, bias float64)` — clamp к [0.02, 0.98]
- `LoadBiasSummary(dataRoot) []BiasSummaryRow` — для всех (city, signal) в data/bias/
- `splitCitySignal(name) (city, signal)` — разбор имени файла через известные суффиксы сигналов
- В resolver.go: вызывать `RecordBiasOutcome` после `UpdateOutcome`
- В bot/main.go: после `applyPlattCalibration` применять `calibration.CorrectProbability`; если новый edge < minEdge → skip; логировать "bias correction: ourP=X→Y (bias=Z)"
- `dashboard bias` — таблица: City | Signal | Bias | N | Status (over/under/ok) | Interpretation
- 6 unit-тестов: нет данных→bias=0, 5 записей→правильный bias, clamp нижней границы, пустой LoadBiasSummary, splitCitySignal, rolling cap

### [x] 2026-05-28 — TASK-175: Market volume filter — пропускать рынки с суммарным объёмом < MinVolumeUSDC
**Файлы:** `internal/markets/markets.go` (обновить), `config/config.go` (обновить), `config/config.yaml` (обновить)
Рынки с объёмом $10 USDC — это неликвидная игрушка. Добавить фильтр по totalVolume из Gamma API.
- Парсить `volume` (строка USD) из Gamma API ответа → `Market.VolumeUSDC float64`
- `MinVolumeUSDC float64` в Config (yaml: `min_volume_usdc`, env: `MIN_VOLUME_USDC`, default: 500.0)
- В bot loop: если `m.VolumeUSDC > 0 && m.VolumeUSDC < cfg.MinVolumeUSDC` → skip с логом "skipped: low volume {V} USDC (min={min})"
- В `dashboard markets`: добавить колонку "Vol USDC" рядом с Spread

### [x] 2026-05-28 — TASK-176: Pre-trade slippage guard — проверка что наш ордер не двигает цену >5%
**Файлы:** `internal/markets/slippage.go` (новый), `cmd/bot/main.go` (обновить)
При малой ликвидности наш ордер может сам сдвинуть цену и сделать ставку невыгодной.
- `EstimateSlippage(sizeUSDC, yesPrice float64, book []bookLevel) float64` — симулирует исполнение ордера по стакану, возвращает avg исполненную цену vs best bid
- Если slippage > 0.03 (3 цента) → логировать "high slippage: {X} — reducing size" и обрезать size до 50% max
- Если slippage > 0.07 → skip полностью

### [x] 2026-05-28 — TASK-177: Per-source timeout config — настраиваемый timeout для каждого источника данных
**Файлы:** `config/config.go` (обновить), `internal/collectors/aggregator.go` (обновить)
Сейчас timeout фиксирован. GOES/NASA иногда медленнее OpenMeteo. Добавить per-source таймауты.
- `SourceTimeouts map[string]int` в Config (yaml: `source_timeouts:`, в секундах)
- Default timeouts: openmeteo=8, nasa=10, noaa=8, goes=15, hrrr=8, ensemble=10
- В `collectSources()`: использовать `context.WithTimeout(ctx, time.Duration(timeout)*time.Second)` для каждого источника

### [x] 2026-05-28 — TASK-178: Brier score trend alert — Telegram уведомление при устойчивом ухудшении
**Файлы:** `internal/calibration/brier_trend.go` (новый), `cmd/bot/main.go` (обновить)
3-недельный тренд Brier score — более устойчивый сигнал чем 14-дневное drift detection (TASK-147).
- `BrierTrend(records []BetRecord, weeks int) (slope float64, r2 float64)` — линейная регрессия недельного Brier
- Если slope > 0.015/неделю И r2 > 0.7 → устойчивое ухудшение → Telegram: "📉 Calibration trend: Brier worsening +0.02/week (R²=0.78)"
- Минимум 3 недели с хотя бы 5 бетами каждая
- Проверять при старте бота (после PrintBrierScore)
- 4 unit-теста: пусто, нет тренда, улучшение, ухудшение

---

## 🔴 ПРИОРИТЕТ 120 — Новые улучшения (добавлено 2026-05-28)

### [x] 2026-05-28 — TASK-179: `/markets` Telegram команда — топ активных рынков в чате
**Файл:** `internal/notifier/telegram_commands.go` (обновить)
Оператор пишет `/markets` и видит топ-5 активных погодных рынков: город, сигнал, цены YES/NO, спред, часы до истечения.
- `handleMarkets(bcfg BotConfig) string` — вызывает `markets.GetWeatherMarkets()`, фильтрует non-empty City+Signal, сортирует по spread ASC (лучшие возможности первыми), берёт top-5
- Колонки в `<pre>`: City | Signal | YES | NO | Spr | Expiry
- Если рынков нет → "No active weather markets found"
- Добавить `/markets` в docstring и поллер switch

### [x] 2026-05-28 — TASK-180: Hour-of-day win rate — анализ когда мы ставим лучше
**Файлы:** `internal/calibration/pnlchart.go` (добавить), `cmd/dashboard/main.go` (добавить `cmdHourlyWinRate`)
Анализ побед по часу размещения ставки: в какое UTC время суток стратегия точнее?
- `HourlyStats{Hour int, Bets int, Wins int, PnLUSDC float64}`
- `HourlyWinRate(records []BetRecord) [24]HourlyStats` — группировка resolved бетов по UTC часу Timestamp
- `cmdHourlyWinRate(dataRoot)` — bar-таблица 0..23, █ пропорционально win rate, цвет зелёный/красный
- Итог: best hour, worst hour, summary
- `go run ./cmd/dashboard hourly-winrate`

### [x] 2026-05-28 — TASK-181: Isotonic regression calibration — монотонная калибровка вместо Platt scaling
**Файлы:** `internal/calibration/isotonic.go` (новый), `internal/calibration/isotonic_test.go` (новый), `cmd/bot/main.go` (добавить опциональный выбор калибратора)
Platt scaling (sigmoid) предполагает гладкую S-кривую. Isotonic regression не делает допущений о форме и часто точнее.
- Pool adjacent violators algorithm: `IsotonicRegression(x, y []float64) []float64`
- `FitIsotonic(records []BetRecord) *IsotonicCalibrator` — обучает на resolved бетах
- `IsotonicCalibrator.Predict(rawP float64) float64` — кусочно-линейная интерполяция
- `UseIsotonicCalibration bool` в Config (yaml: `use_isotonic`, default: false)
- В bot/main.go: если включено и данных ≥ 20 → `applyIsotonicCalibration` вместо Platt
- 5 unit-тестов: пустой список, уже монотонный, нарушение монотонности, интерполяция, клампинг

---

## 🔴 ПРИОРИТЕТ 130 — Новые улучшения (добавлено 2026-05-28)

### [x] 2026-05-28 — TASK-182: `/top-edge` Telegram команда — рынки по edge×confidence
**Файл:** `internal/notifier/telegram_commands.go` (обновить)
`/markets` сортирует по spread, но лучшие возможности — по edge×confidence.
- `handleTopEdge(bcfg BotConfig) string` — получает рынки, агрегирует прогноз для каждого, вычисляет edge×confidence score, top-5
- Запускает `collectors.AggregateForCity()` из кэша (ForecastCache, не живой запрос)
- Колонки в `<pre>`: City | Signal | Side | Edge | Conf | Score | Price
- Score = bestEdge × confidence (честная мера качества возможности)
- Добавить `/top-edge` в docstring и поллер switch

### [x] 2026-05-28 — TASK-183: Kelly fraction auto-tuner — эмпирический оптимальный Kelly из истории
**Файл:** `internal/calibration/kelly_opt.go` (новый), `cmd/dashboard/main.go` (обновить)
Текущий KellyFraction=0.5 фиксирован. Оптимальный можно найти эмпирически по истории ставок.
- `OptimalKelly(records []BetRecord, start, end float64, steps int) (bestK float64, bestEV float64)`
  - Grid search: simulate каждый исторический бет с Kelly k → cumulative log-EV
  - Вернуть k с максимальным geometric growth rate
- `KellyOptResult{BestK, BestEV, BestPnL, Steps []KellyStep}` для вывода
- `cmdKellyOpt(dataRoot)` в dashboard: таблица k | sim_pnl | log_ev, лучший выделен
- Минимум 10 resolved бетов для запуска, иначе "insufficient data"
- `go run ./cmd/dashboard kelly-opt`

### [x] 2026-05-28 — TASK-184: Forecast stability tracker — обнаружение flip-flop прогнозов
**Файл:** `internal/collectors/stability.go` (новый), `internal/collectors/aggregator.go` (обновить), `cmd/dashboard/main.go` (обновить)
Если наша вероятность для рынка скачет между циклами (0.7→0.3→0.7), сигнал ненадёжен.
- `StabilityRecord{ConditionID, City, Signal string, OurP float64, Timestamp time.Time}`
- `StabilityTracker` — rolling window 10 последних оценок на conditionID, хранит в памяти (sync.Map)
- `Track(conditionID, city, signal string, ourP float64)` — добавляет запись
- `Stability(conditionID string) float64` — stddev последних N оценок (0=стабильно, 0.5=хаос)
- `IsUnstable(conditionID string) bool` — stddev > 0.15
- В `EvaluateFused`: если `IsUnstable(conditionID)` → добавить `"SKIP:unstable"` или снизить confidence на 20%
- `dashboard stability` — таблица conditionID | city | signal | stability | N | last_p

---

## 🔴 ПРИОРИТЕТ 28 — Новые улучшения (добавлено 2026-05-28)

### [x] 2026-05-28 — TASK-185: Cross-day signal persistence — буст уверенности при согласии смежных дней
**Файлы:** `internal/collectors/cross_day.go` (новый), `internal/collectors/cross_day_test.go` (новый), `internal/collectors/aggregator.go` (обновить поле FusedForecast), `internal/strategy/strategy.go` (интегрировать), `cmd/dashboard/main.go` (новый subcommand)
Метеорологический факт: если rain/heat/cold одинаково предсказывается на 3 последовательных дня, сигнал намного надёжнее чем для одного дня. "Персистентные" погодные системы лучше предсказываются.
- `CrossDayResult{City, Signal, DaysChecked, DaysConsistent, AgreementFraction, ConfidenceBoost}`
- `CheckCrossDay(city, signal, targetDayOffset, threshold, dataRoot)` — загружает кэш d+0/d+1/d+2, считает направленное согласие
- `ApplyCrossDay(ff, res)` — применяет boost к ff.Confidence (cap 0.97), добавляет "cross_day" в Sources, записывает ff.CrossDayScore
- Boost: 3/3 дней согласны → +0.08; 2/3 → +0.04; иначе → 0
- `signalProbFromForecast(f, signal, threshold)` — вычисляет сигнальную вероятность из weather.Forecast (все 9 типов)
- В `EvaluateFused()`: вызывать CheckCrossDay + ApplyCrossDay до confidence gate
- `FusedForecast.CrossDayScore float64` — сохраняет AgreementFraction для анализа
- `dashboard crossday` — таблица city×signal: Days Checked | Days Agree | Agreement% | Boost | Persistence label
- 11 unit-тестов: FullAgreement/PartialAgreement/NoAgreement/NoCache/OnlyTargetDay/HeatSignal/AllSignals/UnknownSignal/BoostApplied/CapAt097/Noop

---

## 🔴 ПРИОРИТЕТ 29 — Новые улучшения (добавлено 2026-05-28)

### [x] 2026-05-28 — TASK-186: Size-weighted Brier score — точность взвешенная по размеру ставки
**Файлы:** `internal/calibration/calibration.go` (обновить), `cmd/dashboard/main.go` (обновить)
Текущий Brier score считает каждую ставку равнозначной. Ставка на $20 важнее ставки на $0.50 — используем размер как вес.
- `WeightedBrierScore(records []BetRecord) (score float64, count int, err error)` — вес = SizeUSDC / meanSize среди resolved
- Показывать рядом с обычным Brier в `cmdPnL`: "Brier: 0.230 (weighted: 0.215)"
- Также логировать при старте бота рядом с обычным `PrintBrierScore`
- Тест: пустые записи, одна запись (вес=1.0), разные размеры, все проигрыши

### [x] 2026-05-28 — TASK-187: Telegram `/ev` команда — EV capture ratio за последние 50 ставок
**Файлы:** `internal/calibration/ev.go` (новый), `internal/notifier/telegram_commands.go` (обновить)
Сколько от нашего теоретического edge мы реально захватываем? EV capture < 70% — признак проблемы с калибровкой или market impact.
- `EVResult{Count, ExpectedEV, RealizedPnL, CaptureRatio float64}` в `internal/calibration/ev.go`
- `RollingEV(records []BetRecord, n int) EVResult` — последние n resolved бетов (n=0 → все)
  - ExpectedEV = Σ(edge × size) где edge = ourP - marketPrice
  - RealizedPnL = Σ(size×(1/mktP-1) для побед, -size для проигрышей)
  - CaptureRatio = realizedPnL / expectedEV (бесконечность если expectedEV≤0)
- `handleEV(dataRoot string) string` в telegram_commands.go
  - Показывает: Count, ExpectedEV, RealizedPnL, CaptureRatio (с эмодзи ✅>0.7 / ⚠️>0.5 / 🚨≤0.5)
  - Добавить `/ev` в docstring и switch поллера
- `dashboard ev-track` subcommand — та же таблица, но с breakdown по сигналу (rain/heat/etc.)
- 4 unit-теста: пустой список, все победы, все поражения, смешанный

### [x] 2026-05-28 — TASK-188: Market exit signal — когда продавать позицию
**Файлы:** `internal/calibration/exit_signal.go` (новый), `cmd/bot/main.go` (обновить)
Если наш прогноз существенно изменился с момента ставки, возможно позицию выгоднее продать.
- `ExitSignal{ConditionID, Side, EntryP, CurrentP, Delta, CurrentMktPrice, SuggestedAction string}`
- `ComputeExitSignals(openBets []BetRecord, forecasts map[string]float64) []ExitSignal`
  - forecasts: conditionID → текущий ourP (из последнего prediction log)
  - Delta = currentP - entryP
  - Если delta < -0.20 (прогноз сильно ухудшился): SuggestedAction = "SELL"
  - Если delta > 0.15 (прогноз улучшился, цена скорее всего выросла): SuggestedAction = "HOLD/REDUCE_SIZE"
  - Иначе: "HOLD"
- В bot main loop: после фетча forecasts → вычислять exit signals → Telegram уведомление если SELL
- `dashboard exit-signals` subcommand — таблица открытых позиций с рекомендациями

---

## 🔴 ПРИОРИТЕТ 30 — Новые улучшения (добавлено 2026-05-28)

### [x] 2026-05-28 — TASK-189: `/help` Telegram команда — список всех доступных команд
**Файл:** `internal/notifier/telegram_commands.go` (обновить)
Операторы часто забывают какие команды доступны. Добавить `/help` который выводит полный список с описаниями.
- `handleHelp() string` — статическая строка с таблицей всех команд в формате `<pre>/cmd — описание</pre>`
- Команды сгруппировать по категориям: 📊 Аналитика, 🌤 Прогнозы, ⚙️ Управление, 📁 Экспорт
- Добавить case "/help" в switch поллера
- Обновить docstring файла (строки 1-22)

### [x] 2026-05-28 — TASK-190: `/daily` Telegram команда — хронология сегодняшних ставок
**Файл:** `internal/notifier/telegram_commands.go` (обновить)
Показать что произошло сегодня: все ставки с временной меткой, исходом, и нарастающий P&L.
- `handleDaily(bcfg BotConfig) string` — фильтрует records по сегодняшней дате UTC
- Таблица: Time | City/Sig | Side | Size | Entry | Outcome | RunningPnL
- Нарастающий P&L по строкам (running total, + prefix для прибыли)
- Если нет ставок — "📭 No bets today"
- Итоговая строка: "Today: N bets | +X resolved | pnl=$Y.YY"
- Добавить case "/daily" в switch поллера

### [x] 2026-05-28 — TASK-191: `/forecast-quality` Telegram команда — средняя уверенность прогнозов по городам
**Файл:** `internal/notifier/telegram_commands.go` (обновить)
Показывает насколько хорошие у нас сейчас прогнозы — помогает решить стоит ли запускать бота.
- Загружает кэшированные FusedForecast для всех 9 городов через `collectors.LoadForecastCache`
- Таблица: City | Confidence | Sources | Age | Status
- Status: ✅ если confidence ≥ 0.5 и age < 3h; ⚠️ если confidence 0.35-0.5 или age 3-6h; ❌ иначе
- Итог: "Ready: N/9 cities"
- Добавить case "/forecast-quality" в switch поллера

---

## 🔴 ПРИОРИТЕТ 31 — Новые улучшения (добавлено 2026-05-28)

### [x] 2026-05-28 — TASK-192: `/compare` Telegram команда — сравнение сегодня vs вчера
**Файл:** `internal/notifier/telegram_commands.go` (обновить)
Показать как мы делали вчера относительно сегодня — быстро понять в тренде ли стратегия.
- `handleCompare(bcfg BotConfig) string` — фильтрует records на две даты: сегодня (UTC) и вчера (UTC)
- Таблица сравнения: Metric | Today | Yesterday | Δ%
  - Metrics: Total Bets | Resolved | Win Rate | Avg Edge | PnL USDC | ROI%
  - Win Rate = Wins / Resolved
  - Avg Edge = mean(edge) для размещённых ставок
  - ROI = PnL / (avg size × count) ×100%
- Формат: `<pre>СРАВНЕНИЕ СЕГОДНЯ VS ВЧЕРА\n...\nТренд: ↑ (улучш) / ↓ (ухудш) / → (стабильно)`
- Если одного дня нет → "insufficient data for X"
- Добавить case "/compare" в switch поллера

### [ ] TASK-193: Signal heatmap в dashboard — матрица вероятностей по городам и сигналам
**Файлы:** `cmd/dashboard/main.go` (добавить `cmdHeatmap`), `internal/collectors/aggregator.go` (утилита)
Визуализация: какие города×сигналы сейчас наиболее вероятны для нашей стратегии.
- `LoadForecastCache()` → загружает кэшированные FusedForecast для всех 9 городов
- Матрица 9×9 (города × 9 сигналов: rain, heat, cold, snow, wind, hail, storm, sunny, fog)
- Ячейка = наша вероятность (colorized: 🟢 >0.6, 🟡 0.4-0.6, 🔴 <0.4, ⚫ нет данных)
- `go run ./cmd/dashboard heatmap` → выводит ASCII-матрицу с обозначениями
- Итоговые статистики: Max prob (город/сигнал), Min prob, Avg confidence

### [x] 2026-05-28 — TASK-194: `/trend` Telegram команда — 7-дневный тренд выбранного города
**Файл:** `internal/notifier/telegram_commands.go` (обновить)
Оператор пишет `/trend new_york` и видит как менялось наше edge/confidence за неделю для этого города.
- `handleTrend(city string, bcfg BotConfig) string` — фильтрует resolved bets для города за 7 дней
- Группировка по дням (UTC) → для каждого дня: count | win_rate | avg_edge | pnl
- ASCII bar chart edge по дням (█ пропорционально)
- Итог: trend (↑↓→), best day, worst day
- Команда: `/trend new_york` или `/trend` → список доступных городов

### [x] 2026-05-28 — TASK-195: Market price spread tracker — распределение спредов по типам рынков
**Файлы:** `cmd/dashboard/main.go` (добавить `cmdSpreadAnalysis`)
Анализ ликвидности: каких рынков спред узкий (хорошо), каких широкий (плохо).
- `go run ./cmd/dashboard spread-analysis` → группировка рынков по spread ranges
  - ≤0.01 (узкий, отлично), 0.01-0.03, 0.03-0.05, 0.05-0.10, >0.10 (широкий, плохо)
- Таблица: Range | Count | % | Avg Vol | Status
- Бум-диаграмма: средний спред по городам

### [ ] TASK-196: Forecast accuracy per-city — какой город прогнозируется лучше
**Файлы:** `internal/calibration/accuracy.go` (новый), `cmd/dashboard/main.go` (обновить)
Точность предсказаний различается по городам — Берлин может быть стабильнее Майами.
- После resolve рынка: сохранять в `data/city_accuracy/{city}.json` — (pred_prob, outcome)
- `CityAccuracy(city, dataRoot) (brier float64, count int)` — считает per-city Brier
- `LoadCityAccuracies(dataRoot) map[string]CityStats` — для всех городов
- `cmdCityAccuracy(dataRoot)` — таблица: City | Brier | Bets | Status (good/ok/poor)
- Сортировка по Brier ASC (лучшие первыми)

### [ ] TASK-197: Bankroll tracking — график баланса по дням
**Файлы:** `internal/calibration/bankroll.go` (обновить), `cmd/dashboard/main.go` (обновить)
Показать как растёт/падает наш bankroll — ключевой метрик долгосрочного выживания.
- `LoadBankrollHistory(dataRoot)` → [](timestamp, balance_usdc)
- Минимум 1 запись в сутки (end-of-day snapshot)
- `cmdBankrollChart(dataRoot)` — ASCII line chart 30 дней (или максимум имеющихся)
- Итог: current balance, cumulative profit, daily avg, best day, worst day, days up/down

---
