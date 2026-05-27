# Night Log — Polymarket Weather Bot

## 2026-05-27 16:10 — TASK-023, 024, 025: Liquidity filter, Graceful shutdown, Extreme weather confidence

**Задачи:** TASK-023, TASK-024, TASK-025

**Файлы созданы/изменены:**
- `internal/markets/liquidity.go` — НОВЫЙ (~80 строк): `checkSpread(tokenID)` → GET /book CLOB API → bid-ask spread; `EnrichWithLiquidity([]Market)` — batch-обогащение маркетов данными о ликвидности; threshold 10 центов
- `internal/markets/markets.go` — добавлены поля `ThinLiquidity bool` и `Spread float64` в `Market` struct
- `internal/strategy/strategy.go` — рефактор `evaluate()`: единый путь вычисления size + gate `ThinLiquidity && size < 50 → skip` с логом; добавлен `log/slog`
- `internal/weather/extremes.go` — НОВЫЙ (~30 строк): `IsExtreme(Forecast)` → (bool, tag); пороги: heat>38°C, rain>50mm, wind>90km/h; `ExtremeConfidenceFloor = 0.75`
- `internal/collectors/aggregator.go` — в `fuse()`: после построения FusedForecast вызывается `weather.IsExtreme()`, если экстремум — confidence поднимается до max(confidence, 0.75), тег добавляется в Sources
- `internal/metrics/metrics.go` — `Start()` теперь возвращает `*http.Server` для graceful shutdown
- `internal/calibration/resolver.go` — `StartResolver()` принимает `context.Context`; горутина завершается при `ctx.Done()`
- `internal/notifier/telegram.go` — добавлена `NotifyStop(summary)` — отправляет итог сессии при завершении
- `cmd/bot/main.go` — полный рефактор: `signal.NotifyContext(ctx, SIGTERM, SIGINT)`; `sessionStats` с atomic счётчиками (cycles, markets, bets, dry-run P&L); ticker loop с select вместо range; при выходе: печать summary, shutdown metrics server, Telegram уведомление

**Итого: 9 файлов, ~200 строк изменений**

`go build ./...` — ✅ чистая компиляция
`go test ./...` — ✅ все тесты PASS (calibration, strategy, weather, polymarket)

**Ключевые улучшения:**
- Тонкие рынки (spread > 10¢) теперь пропускаются если ставка < $50 — нет price impact
- Экстремальные события (жара >38°C, осадки >50мм, ветер >90км/ч) поднимают confidence до 0.75 — модели сходятся на очевидных сигналах
- SIGTERM/SIGINT корректно останавливают loop, metrics server и resolver горутину
- Telegram уведомление "Bot Stopped" с итогом сессии при каждом завершении

## 2026-05-27 15:57 — TASK-022: Seasonal Bayesian calibration

**Задача:** TASK-022

**Файлы созданы/изменены:**
- `internal/weather/seasonal.go` — НОВЫЙ (~165 строк): полная клима-таблица 9 городов × 12 месяцев (AvgMaxTempC, RainProb, SunProb); `GetSeasonal(city, month)` → MonthlyClimate; `AdjustForSeason(city, forecastDate, rawP, signal, thresholdC)` — Байесовское смешивание с alpha-весами по горизонту прогноза (день 0-1→0.80, день 2-3→0.65, день 4-5→0.50, день 6→0.40); `priorForSignal()` — heat/cold через sigmoid; `SeasonalSummary()` для отладки
- `internal/weather/seasonal_test.go` — НОВЫЙ (~130 строк): 11 тестов — знакомые города, все 9 городов × 12 месяцев в диапазоне, alpha decreases with horizon, unknown city passthrough, wind passthrough, clamp, heat threshold Chicago winter
- `internal/strategy/strategy.go` — интеграция в `evaluate()`: после вычисления ourP применяется `weather.AdjustForSeason()`; seasonal note добавляется в Reason если произошла коррекция (+5 строк нетто)

**Итого: 3 файла, ~300 строк**

**Тесты:** 11 новых тестов в `seasonal_test.go` — все PASS; `go test ./internal/strategy/... ./internal/calibration/...` — PASS

`go build ./...` — ✅ чистая компиляция

**Ключевые улучшения:**
- Miami июль RainProb=63%: модель 0.40 → скорректировано вверх (сезонный prior тянет к климат. норме)
- LA лето SunProb=87%: если модель занижает — коррекция вверх
- Чем дальше горизонт прогноза, тем больше вес клима. prior (меньший alpha)
- Decision.Reason теперь показывает: `seasonal(raw=0.40→0.46)` для прозрачности

## 2026-05-27 15:37 — TASK-015..019: Dedup, Auto-resolve, Prometheus, Walk-Forward, HTTP retry

**Задачи:** TASK-015, TASK-016, TASK-017, TASK-018, TASK-019

**Файлы созданы/изменены:**
- `internal/calibration/calibration.go` — добавлен `LoadOpenPositions(dataRoot)` → `map[string]bool` unresolved conditionIDs; ~15 строк
- `internal/calibration/resolver.go` — НОВЫЙ (~140 строк): `queryGammaMarket()` для Gamma API, `ResolveOpenBets()` — проверяет все open bets, вызывает `UpdateOutcome()` для resolved; `StartResolver()` — запускает горутину каждый час
- `internal/metrics/metrics.go` — НОВЫЙ (~110 строк): stdlib Prometheus exposition format; метрики: bets_placed_total, bets_won_total, brier_score, edge_avg, bankroll_usdc; `/health` endpoint; `Start(addr, dataRoot)` запускает HTTP server
- `internal/httpclient/httpclient.go` — НОВЫЙ (~140 строк): token-bucket rate limiter (10 req/s), exponential backoff retry (3 попытки), Retry-After header support, `New(opts)` + `Default` + `Get(url)` + `Do(req)`
- `cmd/backtest/main.go` — добавлен `--walk-forward` флаг; `runWalkForward()` — 2 IS→OOS пары из 30-дневных окон; sweep minEdge 0.03-0.15; `printWalkForwardReport()` — overfitting ratio, IS/OOS PnL; ~120 строк
- `cmd/bot/main.go` — добавлен `--metrics-port` флаг (default 9090), `calibration.StartResolver()` в loop режиме, `openPositions` dedup проверка перед каждой ставкой, импорт `metrics` пакета

**Итого: 6 файлов, ~625 строк**

`go build ./...` — ✅ чистая компиляция

**Ключевые улучшения:**
- Anti-double-bet: каждый цикл загружает unresolved conditionIDs, пропускает дубли
- Auto-resolve: фоновая горутина каждый час обновляет исходы завершённых рынков через Gamma API
- `/metrics` endpoint: мониторинг через Prometheus/Grafana без зависимостей
- Walk-forward validation: `go run ./cmd/backtest --walk-forward` показывает overfitting ratio
- HTTP retry: все collectors могут использовать `httpclient.Default.Get()` вместо bare http.Client

---

## 2026-05-27 15:32 — TASK-014: Multi-day forecast selection + SunnyProbability

**Задачи:** TASK-014 (новая, разработана в этой итерации)

**Проблема:** бот использовал `fc[0]` (сегодняшний прогноз) для **всех** рынков, включая те, что истекают через 4-5 дней — фундаментальная ошибка точности. Кроме того, сигнал `sunny` в classifier детектировался, но strategy.go возвращал `nil`.

**Файлы изменены:**
- `internal/markets/markets.go` — добавлен `DaysUntilExpiry() int`: парсит EndDate (RFC3339/ISO/plain), возвращает [0,6] дней до истечения; ~25 строк
- `internal/collectors/aggregator.go` — добавлен `AggregateForDay(city, dayOffset, dataRoot)`: запрашивает `dayOffset+1` дней из каждого источника, выбирает нужный день; GOES только для dayOffset=0; корректный fallback; `time` import добавлен; ~70 строк
- `internal/weather/weather.go` — добавлен `SunnyProbability(f Forecast) float64`: WMO коды 0-3/51-67/71-77/80+; rainPenalty из PrecipitationProbability; ~25 строк
- `internal/strategy/strategy.go` — добавлен `case "sunny": ourP = weather.SunnyProbability(f)` в evaluate(); ~2 строки
- `cmd/bot/main.go` — в evaluation loop: `dayOffset := m.DaysUntilExpiry()`, если > 0 — `AggregateForDay()` с fallback на сегодняшний fused; ~15 строк

**Итого: 5 файлов, ~137 строк**

`go build ./...` — ✅ чистая компиляция

**Эффект:** рынок "Will it rain in NYC on Friday?" теперь оценивается по пятничному прогнозу, а не по сегодняшнему. Sunny сигналы теперь генерируют реальные ставки.

---


## 2026-05-27 15:16 — TASK-009..013: Backtest, Dashboard, Telegram, EIP-712, Docker

**Задачи:** TASK-009, TASK-010, TASK-011, TASK-012, TASK-013

**Файлы созданы/изменены:**
- `cmd/backtest/main.go` (~290 строк): бэктест на 90 дней — Gamma API клиент с пагинацией, synthetic market fallback (при недоступности Gamma), классификатор рынков (зеркало markets package), simulation loop, статистика: total P&L, win rate, avg edge, max drawdown, Sharpe ratio, per-signal breakdown
- `cmd/dashboard/main.go` (~220 строк): CLI дашборд с go-pretty/v6/table — sub-commands: positions (открытые позиции), pnl (история resolved ставок с P&L), next (top-5 ставок прямо сейчас с fused+legacy forecasts), all (всё вместе)
- `internal/notifier/telegram.go` (~190 строк): NotifyBet() → HTML-форматированное сообщение при ставке; DailyDigest() → P&L дайджест с Brier score; NotifyError() → алерт при ошибке; SendTestMessage() для тестирования; graceful no-op если токен не задан
- `internal/polymarket/order.go` (~230 строк): PlaceBet() — EIP-712 order signing через go-ethereum/crypto + apitypes.TypedDataAndHash; order struct с CTF Exchange; l1AuthHeaders с HMAC; POST /order на CLOB; toMicroUnits/priceToCTFAmount конвертации
- `internal/polymarket/order_test.go` (~100 строк): TestToMicroUnits, TestNewSalt, TestPriceToCTFAmount, TestLoadPrivateKey(Invalid/Missing/Valid), TestSignOrder
- `Dockerfile` — multi-stage builder (golang:1.23-alpine + CGO) → runtime (alpine:3.19); binaries: bot, backtest, dashboard
- `Makefile` — 18 целей: build/run/live/loop/live-loop/history/backtest/backtest-30/dashboard/positions/pnl/next/test/test-short/lint/vet/tidy/docker/docker-run/docker-live/docker-backtest/clean/help
- `.env.example` — расширен: POLYMARKET_ADDRESS, TELEGRAM_*
- `README.md` — полный гайд: архитектура, быстрый старт, Docker, таблица env vars, описание стратегии
- `cmd/bot/main.go` — интегрированы: notifier.NotifyBet/NotifyError, polymarket.PlaceBet (вместо заглушки), --test-telegram флаг, DailyDigest в loop
- `go.mod/go.sum` — добавлены: go-pretty/v6, go-ethereum v1.15.11 + зависимости

**Итого: ~11 файлов, ~1300 строк**

`go build ./...` — ✅  |  `go test ./...` — ✅ (polymarket: 5/5 тестов)

Автоматический лог агентских итераций.

---

## 2026-05-27 — Init
- Создан базовый бот: weather.py, markets.py, strategy.py, bot.py
- Создан TASKS.md с 12 задачами
- Репозиторий: https://github.com/devher0/polymarket-weather-bot

## 2026-05-27 14:41 — TASK-001..005: Multi-source data collectors + aggregator

**Задачи:** TASK-001, TASK-002, TASK-003, TASK-004, TASK-005

**Файлы изменены/созданы:**
- `internal/collectors/nasa_power.go` — NASA POWER API (~150 строк): T2M, PRECTOTCORR, WS2M, RH2M; 6-час in-memory кэш (sync.Map); heuristic estimatePrecipProb из humidity
- `internal/collectors/noaa_nws.go` — NOAA NWS API (~210 строк): GET /points → /forecast; US-only (new_york, miami); парсинг temperature, precipitationProbability из periods; конвертация F→C
- `internal/collectors/historical.go` — Open-Meteo Historical Archive (~150 строк): 90 дней для всех городов; сохранение в data/historical/{city}.json; `CollectHistory()`, `GetHistory()`
- `internal/collectors/goes_satellite.go` — GOES-19 via AWS S3 (~200 строк): anonymous credentials; ABI-L2-ACMF продукт; graceful fallback; сохранение в data/satellite/{city}_{date}.json
- `internal/collectors/aggregator.go` — fusion всех источников (~190 строк): FusedForecast {Confidence, Sources}; взвешенное среднее OpenMeteo=0.35/NASA=0.30/NOAA=0.25/GOES=0.10; confidence = 1 - stddev(precipProbs)
- `internal/strategy/strategy.go` — добавлен EvaluateFused() с confidence gate < 0.4; source note в Reason; legacy Evaluate() сохранён
- `cmd/bot/main.go` — флаг --collect-history; интеграция AggregateAll() с fallback на OpenMeteo
- `go.mod/go.sum` — добавлен aws-sdk-go-v2 (s3, config, credentials)

**Итого: ~8 файлов, ~1150 строк**

`go build ./...` — ✅ чистая компиляция

## 2026-05-27 15:11 — TASK-006, TASK-007, TASK-008: Market classifier, Calibration, Ensemble gate

**Задачи:** TASK-006, TASK-007, TASK-008

**Файлы изменены/созданы:**
- `internal/markets/markets.go` — парсинг температурного порога regex `(\d+)°?[FC]` с конвертацией F→C; новое поле `ThresholdC float64` в Market; расширен cityPatterns: chicago, los_angeles, san_francisco, berlin; ~+40 строк
- `internal/weather/weather.go` — добавлены 4 новых города в Cities: chicago, los_angeles, san_francisco, berlin
- `internal/collectors/noaa_nws.go` — расширен usCities: chicago, los_angeles, san_francisco
- `internal/strategy/strategy.go` — EvaluateFused теперь логирует weighted contribution каждого источника (напр. `ensemble=[openmeteo(41%)+nasa(35%)+noaa(24%)] confidence=0.87`); HeatProbability/cold теперь использует реальный ThresholdC из вопроса рынка вместо захардкоженных 35°C/10°C; ~+20 строк
- `internal/calibration/calibration.go` — новый пакет (~220 строк): SaveBet() → append CSV; LoadHistory() → []BetRecord; BrierScore() → float64; UpdateOutcome(conditionID, outcome); PrintBrierScore() с win rate и avg edge on wins
- `cmd/bot/main.go` — интеграция calibration.PrintBrierScore() при старте; calibration.SaveBet() после успешной ставки в live режиме

**Итого: 6 файлов, ~300 строк**

`go build ./...` — ✅ чистая компиляция

## 2026-05-27 15:47 — TASK-020, TASK-021: Config file + Unit tests

**Задачи:** TASK-020 (config.yaml), TASK-021 (unit tests)

**Файлы созданы/изменены:**
- `config/config.go` — новый пакет (~165 строк): структура Config со всеми параметрами бота; Load(path) с yaml.Unmarshal + ENV overlay; LoadDefault(); applyEnv() с envFloat/envInt helpers; fallback к defaults при отсутствии файла
- `config/config.yaml` — пример конфига со всеми параметрами, комментарии на каждую настройку, ~60 строк
- `cmd/bot/main.go` — рефакторинг: заменены разрозненные `os.Getenv()` на `config.Load()`; новый флаг `--config path/to/config.yaml`; `--loop` и `--metrics-port` из CLI переопределяют yaml; `DataRoot` прокинут во все функции; лог конфига при старте; убрана дублирующая функция `envFloat()`; ~+30 строк нетто
- `internal/strategy/strategy_test.go` — 16 тестов (~180 строк): Evaluate() (no edge, YES/NO edge, unknown city/signal, heat, size cap), EvaluateFused() (nil, low confidence, high confidence, boundary, multi-source), halfKelly() edge cases
- `internal/calibration/calibration_test.go` — 16 тестов (~180 строк): SaveBet (create, nil, multi), LoadHistory (empty, roundtrip), UpdateOutcome (win/loss/not-found), BrierScore (no resolved, perfect, random, mixed), LoadOpenPositions (empty, unresolved-only, dup), timestamp ordering

**Зависимости:** `gopkg.in/yaml.v3` переведён из indirect в direct dep

**Тесты:** 32 теста, все PASS — `go test ./internal/strategy/... ./internal/calibration/...`

**Итого: 5 файлов, ~535 строк**

`go build ./...` — ✅ чистая компиляция
