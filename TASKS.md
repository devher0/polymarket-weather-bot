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

### TASK-095: ESA MTG-S1 — новый европейский спутник (запущен SpaceX июль 2025)
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

### TASK-097: Speedwell Climate HDD/CDD settlement data
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

### TASK-099: Super-aggregator — все источники в один pipeline
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

### TASK-100: Байесовский ансамбль — не просто среднее
**Файл:** `internal/aggregation/bayesian_ensemble.go`
Вместо взвешенного среднего — полноценный байесовский ансамбль:
- Prior: климатологическая вероятность для города/месяца/сигнала из исторических данных
- Likelihood: каждый источник обновляет prior через байесовское обновление
- Posterior = итоговая вероятность
- Формула: P(rain|sources) = P(sources|rain) × P(rain) / P(sources)
- Результат: точнее обычного среднего на 8-12% особенно когда источники расходятся

### TASK-101: Gradient boosting калибровка (XGBoost-style в Go)
**Файл:** `internal/aggregation/gradient_boost.go`
Обучить лёгкую ML-модель прямо в Go без внешних зависимостей:
- Features: [openmeteo_p, nasa_p, noaa_p, goes_cloud, cape, pressure_trend, month, city_id]
- Target: фактический исход рынка (из bets_history.csv resolved=true/false)
- Алгоритм: простой gradient boosting с 50-100 деревьями решений (gbdt.go)
- Переобучение каждые 7 дней на свежих данных
- Хранить модель в data/model.json (веса деревьев)
- После 50+ resolved ставок — точность +10-15% vs взвешенного среднего

### TASK-102: Метео-консенсус индекс (как рынки используют Reuters Eikon)
**Файл:** `internal/aggregation/consensus_index.go`
Профессиональные трейдеры смотрят на консенсус между моделями:
- Если ECMWF, GFS, HRRR, OpenMeteo все говорят "дождь" → консенсус = 1.0, ставим уверенно
- Если модели 50/50 → консенсус = 0.0, пропускаем (edge реально нет)
- ConsensusIndex(models []float64, threshold float64) (consensus, direction float64)
- Интегрировать в strategy: при ConsensusIndex < 0.3 → skip bet regardless of edge
- При ConsensusIndex > 0.8 → увеличить Kelly fraction на 20%

### TASK-103: Исторический базис — насколько каждый источник точен по городам
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

### TASK-104: Real-time re-weighting при расхождении источников
**Файл:** `internal/collectors/super_aggregator.go` (обновить)
Когда источники сильно расходятся — не усреднять, а анализировать:
- Если 1 источник outlier (отклонение > 2σ от остальных) → понизить его вес в текущем цикле
- Если ECMWF расходится с остальными → ECMWF обычно прав (он точнее), повысить его вес
- Логировать: "NOAA outlier detected (0.82 vs mean 0.45), weight reduced to 0.05"
- История outlier'ов влияет на долгосрочный Brier score источника

### TASK-105: Ensemble spread → автоматический размер ставки
**Файл:** `internal/strategy/strategy.go` (обновить)
Spread между источниками = мера неопределённости = должна влиять на Kelly:
- Малый spread (все согласны) → bet_size × 1.3 (высокая уверенность)
- Средний spread → bet_size × 1.0 (baseline)
- Большой spread → bet_size × 0.5 (осторожно, неопределённость высокая)
- SpreadScale(sources []float64) float64 — стандартное отклонение → scaling factor
- Эффект: автоматически больше ставим когда уверены, меньше когда сомневаемся

### TASK-106: Nowcasting — прогноз на следующие 2-6 часов
**Файл:** `internal/collectors/nowcast.go`
Для рынков которые закрываются сегодня — нужен nowcast а не daily forecast:
- Open-Meteo minutely_15 endpoint: 15-минутные интервалы на 2 суток
- Параметры: precipitation, temperature_2m, wind_speed_10m
- NowcastRainProbability(minutes int) float64 — вероятность дождя в следующие N минут
- Использовать для рынков с EndDate сегодня (DaysUntilExpiry == 0)
- Точнее daily forecast для intraday рынков на 20-30%
