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
