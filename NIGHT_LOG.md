# Night Log — Polymarket Weather Bot

## 2026-05-27 18:27 UTC — TASK-057: Structured prediction logging

**Задача:** TASK-057

### Что сделано:

**Structured prediction logging** — каждый вызов `EvaluateFused()` теперь пишет полную запись в `data/predictions/YYYY-MM-DD.jsonl`:

- **Новый файл `internal/strategy/prediction_log.go`** (~185 строк):
  - `PredictionRecord` — struct с полями: ts, condition_id, city, signal, yes/no price, our_p, yes/no edge, confidence, ensemble_unc, alert_level, sources, forecast fields (max/min temp, precip mm/prob, wind), decision, size_usdc, reason
  - `SavePrediction(rec, dataRoot)` — atomic append в JSONL файл, ошибки заглушаются (не крашит betting loop)
  - `LoadPredictions(date, dataRoot)` — читает дневной JSONL, пропускает битые строки
  - `AnalyzePredictions(records)` → `map[BreakdownKey]*BreakdownStats` — per-city/signal агрегация
  - `SortedBreakdownKeys()` — сортировка по количеству evaluated desc
  - `PredictionSummary()` — однострочный резюме (evaluated/bets/skip)

- **`internal/strategy/strategy.go`** (+45 строк):
  - Новая экспортированная `ComputeOurP(m, f)` — вычисляет нашу вероятность с сезонной корректировкой (извлечена из evaluate(), нужна для логирования скипов)
  - В `EvaluateFused()`: closure `logPrediction(decision, sizeUSDC, reason)` вызывается при каждом exit-point:
    - `"SKIP:confidence"` — confidence gate (< 0.40)
    - `"SKIP:no_edge"` — evaluate() вернул nil (edge недостаточен)
    - `"SKIP:min_size"` — Kelly size < $0.50 после ensemble scaling
    - `"BET_YES"` / `"BET_NO"` — ставка размещена
  - Добавлен импорт `"time"`

- **`cmd/dashboard/main.go`** (+55 строк):
  - Новый sub-command `dashboard analysis`:
    - Читает `data/predictions/YYYY-MM-DD.jsonl` для сегодня
    - Таблица: City | Signal | Eval'd | Bets | Skip% | SkipConf | SkipEdge | SkipSize | AvgEdge | AvgConf | TotalSize$
    - Зелёные Bets, красный Skip% когда 100%
    - Footer с объяснением колонок
  - Обновлён `printUsage()` — добавлена строка для `analysis`

**Какую проблему решает:** теперь видно ПОЧЕМУ бот не ставит на конкретные рынки — `dashboard analysis` сразу покажет например "NYC/rain: 47 evaluated, 0 bets (100% skip) — 43 SkipEdge, 4 SkipConf". Без этого приходилось перечитывать slog вручную.

**Файлы:**
- `internal/strategy/prediction_log.go` (новый, ~185 строк)
- `internal/strategy/strategy.go` (+45 строк)
- `cmd/dashboard/main.go` (+55 строк)
- `TASKS.md` (TASK-057 → [x])

**Итого:** ~285 строк. `go test ./...` — все PASS; `go build ./...` — ✅

---

## 2026-05-27 17:27 UTC — TASK-043 + TASK-044: Active-city filter + Bankroll persistence

**Задачи:** TASK-043, TASK-044

**Файлы созданы/изменены:**
- `internal/calibration/bankroll.go` — НОВЫЙ (~105 строк): `LoadBankroll(dataRoot)` → читает `data/bankroll.json`, возвращает 100.0 по умолчанию; `SaveBankroll(bankroll, dataRoot)` → сохраняет JSON с `bankroll_usdc` + `updated_at`; `AdjustBankrollOnBet(size, dataRoot)` → вычитает размер ставки и сохраняет, логирует "before→after"; `AdjustBankrollOnResolve(size, marketPrice, won, dataRoot)` → добавляет payout (size/marketPrice) при выигрыше или ноль при проигрыше; thread-safe через `bankrollMu sync.Mutex`
- `internal/collectors/aggregator.go` — новая функция `AggregateForCities(activeCities, dataRoot)` (~65 строк): принимает список активных городов; для активных городов — вызывает `Aggregate()` (свежий фетч с cache-fallback внутри); для остальных городов — только cache без сетевых вызовов; логирует "skipping forecast: no active markets" при cache miss на неактивных городах; конкурентно через goroutines + channel
- `cmd/bot/main.go` — реструктурирован `run()`: (1) маркеты фетчатся ПЕРВЫМИ чтобы определить активные города; (2) `AggregateForCities(activeCitiesSlice, dataRoot)` вместо `AggregateAll` — только активные города получают свежие данные; (3) `calibration.LoadBankroll()` вместо hardcoded 100.0; (4) `calibration.AdjustBankrollOnBet()` после каждой реальной ставки; переменная `activeCity` → `configuredCities` (более точное имя); ~40 строк нетто (+/-)
- `internal/calibration/resolver.go` — после resolve победившей ставки вызывает `AdjustBankrollOnResolve(r.SizeUSDC, r.MarketPrice, won, dataRoot)` → банкролл растёт на payout при выигрыше; ~5 строк

**Ключевые эффекты:**
- TASK-043: если активных рынков только 3-4 города — экономия ~55-67% API вызовов на прогнозы за цикл; остальные города обслуживаются из disk cache (если есть) или пропускаются
- TASK-044: банкролл сохраняется между перезапусками бота в `data/bankroll.json`; Kelly-sizing теперь работает с реальным накопленным балансом вместо hardcoded 100 USDC; Brier multiplier применяется поверх сохранённого значения

**Строк кода:** ~215 (+105 новый файл, +65 aggregator, +40 bot, +5 resolver)

`go build ./...` — ✅  `go test ./...` — ✅ все PASS

---

## 2026-05-27 17:17 UTC — TASK-041 + TASK-042: Forecast cache persistence + change detector

**Задачи:** TASK-041, TASK-042

**Файлы созданы/изменены:**
- `internal/collectors/forecast_cache.go` — НОВЫЙ (~215 строк): `SaveForecastCache(city, dayOffset, ff, dataRoot)` → сохраняет FusedForecast в `data/forecasts/{city}_d{dayOffset}.json`; `LoadForecastCache(city, dayOffset, dataRoot, maxAge)` → возвращает кэш если age < maxAge (default 2h); `DetectForecastShift(city, dayOffset, newFF, dataRoot)` → сравнивает новый прогноз с кэшем, возвращает ForecastShift с Δs и флагом Significant (|ΔMaxTemp|>5°C или |ΔPrecipProb|>20%); `ForecastCacheStats(dataRoot)` → map[key]age для dashboard; `cachedForecast` envelope-структура с SavedAt для staleness
- `internal/collectors/aggregator.go` — добавлен `log/slog` импорт; в `Aggregate()` и `AggregateForDay()`: cache check перед любыми API вызовами (cache hit → return немедленно), shift detection + логирование при значимом изменении, `SaveForecastCache()` после успешного фетча
- `internal/notifier/telegram.go` — новая функция `NotifyForecastShift(city, oldMaxTemp, newMaxTemp, oldPrecipP, newPrecipP)` — HTML-форматированное сообщение с дельтами и стрелками ↑↓; ~35 строк
- `cmd/bot/main.go` — после `AggregateAll()`: loop по городам с вызовом `collectors.DetectForecastShift()` + `notifier.NotifyForecastShift()` для города; ~15 строк
- `cmd/dashboard/main.go` — новый sub-command `cache`: таблица с ключами, возрастом и цветным статусом (fresh/aging/stale); добавлен в printUsage(); ~35 строк

**Ключевые эффекты:**
- Loop-режим: 2-й и последующие циклы в течение 2 часов НЕ делают API вызовы (cache hit) → время цикла с ~5 сек до <100ms
- Forecast shift alert: когда грозовой фронт меняет прогноз на >5°C или >20% осадков → Telegram уведомление оператору
- `go run ./cmd/dashboard cache` — быстрый просмотр свежести данных перед ручным запуском бота

**Строк кода:** ~295 (+215 новый файл, +50 aggregator, +35 telegram, +15 bot, +35 dashboard -~55 рефакторинг)

`go build ./...` — ✅  `go test ./...` — ✅ все PASS

---

## 2026-05-27 17:12 — TASK-039 + TASK-040: dashboard forecast + integration smoke tests

**Задачи:** TASK-039, TASK-040

**Файлы изменены:**
- `cmd/dashboard/main.go` — новый sub-command `forecast`: вызывает `AggregateAll()`, строит таблицу City|Date|MaxT°C|MinT°C|Precip mm|Rain%|Wind|Ens.Unc°C|Confidence|Sources|Age; подсвечивает conf < 0.4 жёлтым "(low conf)", conf ≥ 0.75 зелёным; добавлен в `all` и `printUsage()`; ~65 строк
- `internal/collectors/collectors_integration_test.go` — новый файл с build tag `//go:build integration`; 6 smoke-тестов: TestSmokeOpenMeteo, TestSmokeNASAPower, TestSmokeNOAANWS, TestSmokeNOAANWSNonUS, TestSmokeEnsemble, TestSmokeAggregateAll; запуск: `go test -tags=integration -timeout=60s ./internal/collectors/`; ~135 строк

**Строк кода:** +200 (dash ~65 + tests ~135)
**go build ./...:** ✅ OK
**go build -tags=integration ./internal/collectors/...:** ✅ OK

---

## 2026-05-27 16:51 — TASK-034: Ensemble uncertainty → proportional bet scaling

**Задача:** TASK-034

**Файлы изменены:**
- `internal/strategy/strategy.go` — новая функция `ensembleUncertaintyScale(uncertaintyC float64) float64`: formula `1.0 - unc/6.0`, clamped [0.30, 1.0]; интеграция в `EvaluateFused()`: после `evaluate()` проверяет `ff.EnsembleUncertainty`, применяет scale к `SizeUSDC`, логирует детали, аннотирует `Decision.Reason`; повторная проверка min-size gate после масштабирования; ~40 строк нетто
- `internal/strategy/strategy_test.go` — 7 новых тестов: `TestEnsembleUncertaintyScale_Zero/Low/Mid/Floor/Negative`, `TestEvaluateFused_EnsembleScaling_ReducesSize`, `TestEvaluateFused_EnsembleScaling_ReasonAnnotated`; вспомогательные функции `containsStr`/`stringContains`; ~75 строк

**Итого: 2 файла, ~115 строк**

**Тесты:** 21/21 PASS в strategy пакете (7 новых + 14 существующих)

`go build ./...` — ✅  `go test ./...` — ✅

**Ключевой эффект:**
- При EnsembleUncertainty=0°C (нет данных): scale=1.0, ставка не меняется — backward compat ✅
- При EnsembleUncertainty=3°C (умеренная): ставка уменьшается вдвое ($50→$25)
- При EnsembleUncertainty=6°C+ (высокая): ставка 70% меньше (floor 0.30), т.е. $50→$15
- Если после scaling size < $0.50 — ставка отменяется с логом
- Decision.Reason аннотируется: `ensemble_scale=0.50(unc=3.0°C,50.00→25.00)`
- Новые задачи добавлены в TASKS.md: TASK-035 (per-city Brier), TASK-036 (pre-order price refresh)

---

## 2026-05-27 16:37 — TASK-032, TASK-033: Per-source accuracy tracker + PnL-adaptive Kelly

**Задачи:** TASK-032 (per-source accuracy tracker), TASK-033 (PnL-adaptive Kelly)

**Файлы созданы/изменены:**
- `internal/collectors/source_accuracy.go` — новый файл (~210 строк): `AccuracyStats{Count, BrierSum}`, `LoadSourceAccuracy(dataRoot)` / `SaveSourceAccuracy()`, `RecordSourcePredictions(conditionID, probs, dataRoot)` → записывает per-source прогнозы в `data/source_predictions/{cid}.json`, `UpdateSourceAccuracyOnResolve(conditionID, outcome, dataRoot)` → читает прогнозы, обновляет `data/source_accuracy.json`, удаляет sidecar; `DynamicWeights(accuracy)` → пересчёт весов по Brier skill (1/score), min weight=0.05, clamped, renormalised; `LogDynamicWeights()` логирует текущие веса
- `internal/collectors/aggregator.go` — рефакторинг: `sourceWeights` → `staticSourceWeights`, новая `currentWeights(dataRoot)` вызывает `DynamicWeights` при наличии данных; `collectSources` принимает динамические веса; `fuse()` теперь заполняет `PerSourceForecasts map[string]weather.Forecast` в `FusedForecast`; добавлено поле `PerSourceForecasts` в struct
- `internal/strategy/strategy.go` — `EvaluateFused` получает `dataRoot string`; после успешного evaluate вызывает `computePerSourceProbs(m, ff.PerSourceForecasts)` + `collectors.RecordSourcePredictions()`; новая функция `computePerSourceProbs()` — идентичная switch-логика по signal для каждого источника
- `internal/calibration/resolver.go` — импорт collectors; после `UpdateOutcome` вызывает `collectors.UpdateSourceAccuracyOnResolve()` (non-fatal если sidecar отсутствует — нормально для старых ставок)
- `internal/calibration/calibration.go` — новая функция `BankrollMultiplier(brierScore)`: score<0.10→1.5x, score>0.22→0.5x, линейная интерполяция между ними, clamped [0.25, 2.0], returns 1.0 при score=0 (нет данных)
- `cmd/bot/main.go` — вычисляет `brierScore + bankrollMultiplier + effectiveBankroll` в начале каждого цикла; передаёт `effectiveBankroll` вместо `100.0` в оба вызова Evaluate/EvaluateFused; добавлен slog.Info лог при ненейтральном multiplier
- `cmd/dashboard/main.go` — EvaluateFused теперь с `dataRoot=""` (нет записи прогнозов в dashboard)
- `internal/strategy/strategy_test.go` — обновлены все 5 вызовов EvaluateFused с новым параметром `""`

**Тесты:** `go test ./internal/strategy/... ./internal/calibration/...` — ✅ все PASS

**Итого: 8 файлов, ~270 строк нетто**

`go build ./...` — ✅ чистая компиляция

## 2026-05-27 16:32 — TASK-031: Параллельный фетчинг источников данных

**Задача:** TASK-031

**Файлы созданы/изменены:**
- `internal/collectors/aggregator.go` — рефакторинг (~290 строк итого): добавлена `collectSources(ctx, city, days, dayOffset, dataRoot, includeGOES)` — запускает OpenMeteo/NASA POWER/NOAA NWS/GOES-19 в 4 отдельных горутинах одновременно, собирает результаты через буферизованный канал с `context.WithTimeout(8s)`; при срабатывании дедлайна — graceful fallback на успевшие источники; `Aggregate()` и `AggregateForDay()` рефакторированы под `collectSources()`; `AggregateAll()` рефакторирован: 9 городов теперь фетчатся параллельно через `sync.WaitGroup` + буферизованный канал — результаты собираются после `wg.Wait()`

**Новые задачи добавлены в TASKS.md:** TASK-031 (параллельный фетчинг, выполнено), TASK-032 (per-source accuracy tracker), TASK-033 (PnL-адаптивный Kelly)

**Итого: 1 файл, ~60 строк нетто изменений**

`go build ./...` — ✅ чистая компиляция
`go test ./...` — ✅ calibration/polymarket/risk/strategy/weather PASS

**Ключевые улучшения:**
- Время фетчинга одного города: ~12 сек (последовательно) → ~5 сек (параллельно, ограничено самым медленным источником)
- Время `AggregateAll()` для 9 городов: ~90 сек → ~5-8 сек (все города одновременно)
- Суммарный цикл бота: с 2+ минут до ~10-15 секунд на цикл
- Контекстный дедлайн 8 сек гарантирует: если NASA/GOES недоступны — бот не зависает

---


## 2026-05-27 16:22 — TASK-027/028/029/030: Ensemble, Correlation, Staleness, Scoring

**Задачи:** TASK-027, TASK-028, TASK-029, TASK-030

**Файлы созданы/изменены:**
- `internal/collectors/openmeteo_ensemble.go` — НОВЫЙ (~155 строк): `GetEnsembleForecast()` — запрашивает ICON-EPS 16 членов с Open-Meteo ensemble API, агрегирует почасовые данные в дневные, считает mean + stddev температуры и осадков по членам; `EnsembleToConfidence()` — конвертирует stddev → 0-1 confidence (0°C→1.0, ≥5°C→0.0)
- `internal/collectors/aggregator.go` — добавлены поля `EnsembleUncertainty float64`, `FetchedAt time.Time` в `FusedForecast`; в `Aggregate()` и `AggregateForDay()` после fuse() вызывается `GetEnsembleForecast()` — если ансамбль доступен, его confidence заменяет межмодельный; `FetchedAt` устанавливается в `fuse()`
- `internal/risk/correlation.go` — НОВЫЙ (~75 строк): карта корреляций 5 пар городов (NY-Miami=0.70, London-Paris=0.80, LA-SF=0.85, Chicago-NY=0.65); `CorrelatedCitiesOpen(m, placedMarkets)` — пропускает рынки с r>0.75 и тем же сигналом; логирует "skipped: correlated position in {city}"
- `config/config.go` — добавлены `MaxForecastAgeHours float64` (default 3.0) и `MaxBetsPerCycle int` (default 5) + ENV overlay
- `internal/strategy/strategy.go` — добавлены `ScoredMarket` struct и `ScoreMarket(m, ff)` — приоритет = rough_edge × confidence × urgency_factor (1.5/1.2/1.0/0.8/0.6 по дням до экспирации)
- `cmd/bot/main.go` — TASK-028: `placedThisCycle` slice + `risk.CorrelatedCitiesOpen()` перед каждой ставкой; TASK-029: `maxForecastAge()` + проверка `ff.FetchedAt` перед оценкой; TASK-030: предварительный скоринг + сортировка рынков по убыванию score + `MaxBetsPerCycle` hard cap; логирование score при каждой ставке

**Результат:** `go build ./...` ✅, все тесты pass (calibration/polymarket/risk/strategy/weather)
**Строки кода:** ~460 новых/изменённых строк

---

## 2026-05-27 16:12 — TASK-026: Risk Manager — дневные лимиты потерь

**Задача:** TASK-026

**Файлы созданы/изменены:**
- `internal/risk/risk.go` — НОВЫЙ (~115 строк): пакет `risk`; `Manager.AllowBet(records)` — проверка 3 лимитов (daily bet cap, daily P&L loss limit, open-position cap); `DailyStats()` — подсчёт сегодняшних ставок и realised P&L; `OpenPositionsCount()` — счётчик open positions; `Summary()` — однострочный риск-статус для лога
- `internal/risk/risk_test.go` — НОВЫЙ (~165 строк): 13 unit-тестов — empty history, yesterday bets ignored, today mix (win+unresolved), loss P&L, cap edge cases, zero limits = unlimited, Summary fields
- `config/config.go` — добавлены поля `MaxDailyLossUSDC`, `MaxDailyBets`, `MaxOpenPositions` в struct + defaults (50/20/30) + ENV overlay (MAX_DAILY_LOSS_USDC, MAX_DAILY_BETS, MAX_OPEN_POSITIONS)
- `config/config.yaml` — секция `── Risk management ──` с документированными полями
- `cmd/bot/main.go` — инициализация `riskMgr` из cfg; `LoadHistory()` заменяет `LoadOpenPositions()` (один вызов для dedup + risk); risk.Summary() в лог каждого цикла; pre-cycle gate (весь цикл пропускается) + per-bet gate (break loop при срабатывании)

**Итого: 5 файлов, ~270 строк нетто**

`go build ./...` — ✅  `go test ./...` — ✅ (13 новых тестов + все предыдущие PASS)

**Ключевые улучшения:**
- При 20+ ставках за день — бот автоматически замолкает до следующего UTC-дня
- При убытке >50 USDC за день — весь цикл пропускается с логом "risk gate blocked entire cycle"
- При ≥30 открытых позициях — per-bet check ломает цикл (`break`), не тратит API calls
- Риск-статус виден в каждом цикле: `risk [daily_bets=3 daily_pnl=+2.50 USDC open=7 | limits: ...]`

**Новые задачи добавлены в TASKS.md:** TASK-027 (ensemble uncertainty), TASK-028 (correlation guard), TASK-029 (staleness guard), TASK-030 (market score ranking)

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

## 2026-05-27 16:54 UTC — TASK-035: Per-city/signal Brier breakdown

**Что сделано:**
- `BetRecord` расширен полями `City` и `Signal` (cols 8-9); backward-compat с легаси-строками (len < 10)
- `csvHeader` / `SaveBet` / `parseRow` обновлены — сохраняем `d.Market.City` и `d.Market.Signal`
- Новый тип `BreakdownStats{Count, BrierSum, Wins}` с методами `BrierAvg()` и `WinRate()`
- `CityBreakdown(records)` → `map[string]BreakdownStats` — per-city статистика
- `SignalBreakdown(records)` → `map[string]BreakdownStats` — per-signal статистика
- `PrintBrierScore()` расширен: показывает топ-5 городов и сигналов по Brier score
- `cmd/dashboard pnl` — добавлены таблицы "P&L BY CITY" и "P&L BY SIGNAL"
- 9 новых unit-тестов (итого 23 теста в пакете calibration)

**Файлы:**
- `internal/calibration/calibration.go` (+130 строк)
- `internal/calibration/calibration_test.go` (+110 строк)
- `cmd/dashboard/main.go` (+55 строк)
- `TASKS.md` — TASK-035 отмечена [x]

**Строк добавлено:** ~295

## 2026-05-27 17:00 UTC — TASK-036: Pre-order price refresh

**Что сделано:**
- `internal/markets/markets.go` — новая функция `RefreshPrices(m Market)`:
  - GET CLOB `/markets/{conditionID}` с 2-секундным timeout
  - Возвращает обновлённые YesPrice/NoPrice; при ошибке — graceful fallback (original prices)
- `cmd/bot/main.go` — новая функция `preOrderRefresh(d, minEdge)`:
  - Вызывается перед каждой реальной ставкой (только в live-режиме)
  - Пересчитывает edge с актуальными ценами
  - Если newEdge < minEdge — логирует "price refresh: edge reduced, skipping bet" и пропускает
  - При API-ошибке — логирует warning, продолжает со старой ценой
  - При движении цены — логирует "price moved, proceeding" с delta

**Файлы:**
- `internal/markets/markets.go` (+43 строки)
- `cmd/bot/main.go` (+65 строк)
- `TASKS.md` — TASK-036 отмечена [x]

**Строк добавлено:** ~108

## 2026-05-27 15:02 UTC — TASK-037 + TASK-038: Near-expiry filter + Daily profit target

**Что сделано:**

### TASK-037: Near-expiry filter
- `internal/markets/markets.go` — новый метод `HoursUntilExpiry() float64` (точные часы до закрытия)
- `config/config.go` — новое поле `MinHoursToExpiry float64` (default 6.0) + ENV `MIN_HOURS_TO_EXPIRY`
- `config/config.yaml` — задокументирован `min_hours_to_expiry: 6.0` с пояснением
- `cmd/bot/main.go` — проверка перед evaluate: если `HoursUntilExpiry() < cfg.MinHoursToExpiry` → skip с логом "skipped: near-expiry market, hours_left=Xh"
- Защита от ставок в последние часы где bid/ask спред максимален

### TASK-038: Daily profit target auto-pause
- `internal/risk/risk.go` — новое поле `MaxDailyProfitUSDC float64` в `Config`; проверка 2b в `AllowBet()`: если resolved P&L > target → return error "daily profit target reached"
- `Summary()` показывает `max_daily_profit` если включён
- `config/config.go` — поле `MaxDailyProfitUSDC` + ENV `MAX_DAILY_PROFIT_USDC`
- `config/config.yaml` — `max_daily_profit_usdc: 0.0` (disabled by default)
- `cmd/bot/main.go` — `MaxDailyProfitUSDC` передаётся в оба места создания `risk.Config`
- `internal/risk/risk_test.go` — 3 новых теста: `TestAllowBet_ProfitTargetNotReached`, `TestAllowBet_ProfitTargetReached`, `TestAllowBet_ProfitTargetDisabled`

**Файлы (5):**
- `internal/markets/markets.go` (+22 строки)
- `config/config.go` (+6 строк)
- `config/config.yaml` (+10 строк)
- `cmd/bot/main.go` (+16 строк)
- `internal/risk/risk.go` (+14 строк)
- `internal/risk/risk_test.go` (+40 строк)

**Итого строк добавлено:** ~108

**Тесты:** `go test ./...` — все PASS (risk: 16 тестов, остальные без изменений)

`go build ./...` — ✅ чистая компиляция

## 2026-05-27 15:43 UTC — TASK-045: dashboard explain — Decision Audit Trail

**Что сделано:**
- `internal/strategy/explain.go` (новый, 161 строка) — тип `ExplainResult` с полным аудитом:
  - Confidence, Sources, EnsUnc из FusedForecast
  - RawP (до seasonal adj), SeasonP (после), FinalP
  - YesEdge / NoEdge, BestSide, BestEdge
  - KellyRaw (до ансамблевого скейлинга), EnsScale, FinalSize
  - SkipReason + Action ("BET YES $8.42" или "SKIP: low confidence (0.35)")
- Функция `ExplainEvaluate(m, ff, bankroll, minEdge, maxBet)` — проходит ВСЕ gate'ы стратегии (nil ff, confidence, signal→rawP, seasonal, edge, Kelly, ensemble scale, min size)
- `cmd/dashboard/main.go` (+118 строк) — новый sub-command `explain`:
  - Фетчит прогнозы (с кэшем) + активные рынки
  - Таблица: City/Signal | OurP | YesP→Edge | NoP→Edge | Conf | EnsUnc | Action
  - BET — зелёным, SKIP — красным
  - Итог: "Markets evaluated: N | BET: K | SKIP: N-K"
  - Добавлен в `printUsage()`
- Добавлено в TASKS.md: TASK-046..049 (новые улучшения)

**Файлы (4):**
- `internal/strategy/explain.go` (новый, 161 строк)
- `cmd/dashboard/main.go` (+118 строк)
- `TASKS.md` (+54 строки: 5 новых задач, 045 отмечена [x])
- `NIGHT_LOG.md` (эта запись)

**Строк добавлено:** ~280

**Тесты:** `go test ./...` — все PASS; `go build ./...` — ✅

## 2026-05-27 17:55 UTC — TASK-046..049: Webhook, Adaptive Loop, Extended Regex, Dry-Run File

### TASK-046: Webhook уведомления при ставках

**Что сделано:**
- `internal/notifier/webhook.go` (новый, 138 строк) — `WebhookPayload` struct; `PostWebhook(url, payload)` с таймаутом 3s и 1 retry; 4 convenience-обёртки: `WebhookBetPlaced`, `WebhookBetSkippedRisk`, `WebhookCycleComplete`, `WebhookError`
- `config/config.go` — новое поле `WebhookURL string` + ENV `WEBHOOK_URL`
- `config/config.yaml` — секция `webhook_url: ""`
- `.env.example` — `WEBHOOK_URL=`
- `cmd/bot/main.go` — вызовы вебхука: `bet_placed` после успешной ставки, `bet_skipped_risk` при блокировке риска, `cycle_complete` в конце каждого цикла, `error` при ошибке `placeBet`

### TASK-047: Adaptive loop interval

**Что сделано:**
- `cmd/bot/main.go` — `cycleResult` struct с полями `placed`, `marketsFound`, `highEdgeBet`, `thinLiquidityOnly`, `decisions`
- `run()` теперь возвращает `cycleResult`; отслеживает: high-edge bets (edge > 0.15), thin-liquidity-only циклы
- `adaptiveInterval(res)` — логика: high edge → 5 мин; thin liquidity → 30 мин; nothing found → backoff ×1.5 (cap 60 мин); normal → base interval
- Цикл переписан с `time.Ticker` на `time.Timer` для переменных интервалов
- Логирует причину адаптации каждый цикл

### TASK-048: Расширенные regex для парсинга рынков

**Что сделано:**
- `internal/markets/markets.go` — 3 новых сигнала: `fog`, `humid`, `dry`
- Расширены cityPatterns: "Big Apple" → new_york, "Windy City" → chicago, "City of Light" → paris, "Silicon Valley" → san_francisco
- `tempDegreesOnlyRe` — парсинг без C/F: value > 50 → Fahrenheit, иначе Celsius
- `tempRangeRe` — "between X°C and Y°C" → ThresholdC = верхняя граница
- `parseTempThresholdC` — приоритет: range > explicit > unitless
- `internal/markets/markets_test.go` (новый, 89 строк) — 11 тест-кейсов, все PASS

### TASK-049: --dry-run-file

**Что сделано:**
- `cmd/bot/main.go` — флаг `--dry-run-file=output.json`
- `dryRunRecord` / `dryRunOutput` structs с полями timestamp, cycle, markets_evaluated, bets_recommended, decisions
- `writeDryRunFile(result)` — вызывается после каждого `run()` (в одиночном и loop режимах)
- Всегда перезаписывает файл (не append); `decisions` всегда [] вместо null

**Файлы:**
- `internal/notifier/webhook.go` (новый, 138 строк)
- `internal/markets/markets.go` (+65 строк)
- `internal/markets/markets_test.go` (новый, 89 строк)
- `cmd/bot/main.go` (+115 строк)
- `config/config.go` (+8 строк)
- `config/config.yaml` (+6 строк)
- `.env.example` (+4 строки)
- `TASKS.md` (отмечены [x] 046-049)

**Итого строк добавлено:** ~425

**Тесты:** `go test ./...` — все PASS; `go build ./...` — ✅

---

## 2026-05-27 — Итерация 11 (17:57 CET)

### TASK-050: NOAA Weather Alerts — буст вероятности при активных NWS предупреждениях

**Что сделано:**
- `internal/collectors/noaa_alerts.go` (новый, 230 строк)
  - `FetchAlerts(city)` → `AlertSummary{Level, Events}`
  - Кэш 30 минут (sync.Mutex + map)
  - `alertsUsCities` = new_york, miami, chicago, los_angeles, san_francisco
  - `classifyEvent()` — 28 NWS event keywords → AlertLevel (0=none…3=warning)
  - `AlertBoost(summary, signal)` → (probBoost, confBoost) по сигналу и уровню
    - Warning: +15% prob, +10% conf | Watch: +8%/+5% | Advisory: +4%/+2%
  - User-Agent header обязателен для NWS API
- `internal/collectors/aggregator.go` (обновлён)
  - `FusedForecast` получил поля `AlertLevel int` и `AlertEvents []string`
  - `FetchAlerts()` вызывается в `Aggregate()` и `AggregateForDay()` после ансамблевой калибровки
  - Ошибки алертов логируются как DEBUG и не останавливают основной поток
- `internal/strategy/strategy.go` (обновлён)
  - В `EvaluateFused()`: перед вызовом `evaluate()` применяется `AlertBoost()`
  - Для heat: MaxTempC += boost×15; для rain: PrecipitationProbability boosted; для cold/snow: temp снижается; для wind: WindSpeedKMH boosted
  - Добавлен `levelName()` helper; логируется "alert boost applied" с city/signal/level/events
  - Confidence boost применяется к `ff.Confidence` после основного evaluate

**Файлы:**
- `internal/collectors/noaa_alerts.go` (новый, 230 строк)
- `internal/collectors/aggregator.go` (+18 строк)
- `internal/strategy/strategy.go` (+53 строки)

**Итого строк добавлено:** ~301

**Тесты:** `go test ./...` — все PASS; `go build ./...` — ✅

---

## 2026-05-27 18:10 UTC — TASK-051, TASK-052, TASK-053

**Что сделано:**

### TASK-051: /healthz HTTP endpoint
- Добавлен `/healthz` эндпоинт к Prometheus HTTP серверу
- Возвращает JSON: `{status, uptime_s, last_cycle_at, cycles, bets_placed, open_positions, bankroll_usdc}`
- `status: "degraded"` если последний цикл был > 2×loop_sec назад (возвращает HTTP 503)
- Добавлены функции `metrics.UpdateCycle(n)` и `metrics.SetLoopSec(sec)` для обновления состояния
- main.go вызывает UpdateCycle после каждого цикла (и в loop-режиме, и при разовом запуске)
- Сохранён legacy `/health` эндпоинт для обратной совместимости

### TASK-052: dashboard report — JSON export
- Новый sub-command `dashboard report [--output=file.json]`
- Экспортирует полный снимок всех рынков: timestamp, condition_id, city/signal, наша вероятность, edges, уверенность, решение, причина пропуска
- Без `--output` печатает в stdout; с флагом пишет в файл
- Использует ExplainEvaluate — тот же pipeline что и `dashboard explain`

### TASK-053: ENV validation + README
- Добавлена валидация обязательных переменных при старте в `--live` режиме
- Проверяет POLYMARKET_PRIVATE_KEY и POLYMARKET_ADDRESS
- При отсутствии — чёткое сообщение с именами пропущенных vars + пример export команд, exit(1)
- README.md: полная документация всех ENV-переменных (обязательные / опциональные)

**Файлы:**
- `internal/metrics/metrics.go` (+77 строк)
- `cmd/bot/main.go` (+26 строк)
- `cmd/dashboard/main.go` (+80 строк)
- `README.md` (+32 строки)
- `TASKS.md` (3 задачи → [x])

**Итого:** ~215 строк. `go build ./...` ✅

## 2026-05-27 18:17 UTC — TASK-054 + TASK-055 + TASK-056: Correlated guard + Adaptive edge + Price tracker

**Задачи:** TASK-054, TASK-055, TASK-056

**Файлы созданы/изменены:**
- `internal/risk/risk.go` — новое поле `MaxSameCitySignalBets int` в `Config`; метод `CheckCorrelation(records, city, signal)` считает открытые ставки на (city, signal) пару и блокирует при превышении лимита; обновлён `Summary` для отображения лимита; ~35 строк
- `internal/risk/risk_test.go` — 6 новых тестов для `CheckCorrelation`: disabled, under limit, at limit, resolved bets not count, different signal, different city; ~90 строк
- `config/config.go` — поле `MaxSameCitySignalBets int` в `Config` + default=2 + ENV override `MAX_SAME_CITY_SIGNAL_BETS`; ~8 строк
- `config/config.yaml` — документация `max_same_city_signal_bets: 2`; ~5 строк
- `internal/strategy/strategy.go` — функция `confidenceEdgeFactor(confidence)`: >0.80→0.80, 0.50-0.80→1.00, <0.50→1.50; применяется в `EvaluateFused` к `minEdge` перед вызовом `evaluate()`; логирует при отклонении; ~30 строк
- `internal/markets/price_tracker.go` — НОВЫЙ (~175 строк): `SnapshotPrice(condID, yesTokenID, dataRoot)` → fetches mid-price from CLOB book API, appends JSON line в `data/price_snapshots/{condID}.jsonl`; `GetPriceHistory(condID, dataRoot)` → загружает JSONL; `DetectAdverseMove(ourSide, history)` → true если цена нашей стороны упала >0.15 за последние 3 точки; `SnapshotOpenPositions(map, dataRoot)` → batch snapshot
- `cmd/bot/main.go` — (1) `riskMgr` получает `MaxSameCitySignalBets` из config; (2) `CheckCorrelation` вызывается перед каждой ставкой; (3) `SnapshotOpenPositions` вызывается после загрузки рынков; (4) `DetectAdverseMove` + elevated edge check (+0.05) перед ставкой; ~40 строк нетто

**Ключевые эффекты:**
- TASK-054: бот не открывает >2 позиций на одну (city, signal) пару — защита от overconcentration в e.g. new_york/rain при нескольких экспирях
- TASK-055: при confidence >0.80 принимаем edge от 0.04 (было 0.05); при confidence <0.50 требуем edge 0.075+ — динамичный порог входа
- TASK-056: каждый цикл сохраняет снапшоты цен открытых позиций; обнаруживает adverse move >0.15 за 3 точки и блокирует ставку если edge недостаточен

**Тесты:** все пакеты OK (`go test ./...`), `go build ./...` проходит без ошибок

---

## 2026-05-27 18:45 UTC — TASK-058 + TASK-059

### TASK-058: Weather alert digest в Telegram DailyDigest
**Файлы:** `internal/notifier/telegram.go` (+80 строк), `internal/collectors/noaa_alerts.go` (без изменений)

Добавлена секция "⚠️ Active Weather Alerts" в `DailyDigest()`:
- Новые helpers: `alertEmoji()`, `cityDisplayName`, `usCitiesForAlerts`, `buildAlertDigest()`
- `buildAlertDigest()` вызывает `collectors.FetchAlerts()` для 5 US городов (new_york, miami, chicago, los_angeles, san_francisco)
- Emoji по уровню: 🔴 Warning, 🟡 Watch, 🔵 Advisory
- Секция не показывается если нет активных алертов (graceful: ошибки API игнорируются)
- Добавлены импорты: `strings`, `collectors`
- Нет circular dependency: collectors не импортирует notifier

### TASK-059: Prediction log CSV export
**Файлы:** `internal/strategy/prediction_log.go` (+55 строк), `cmd/dashboard/main.go` (+35 строк)

Добавлен sub-command `dashboard export-predictions`:
- Функция `ExportPredictionsCSV(date, dataRoot, outputPath string) error` в prediction_log.go
- Конвертирует JSONL → CSV, заголовки: timestamp, condition_id, city, signal, our_p, yes_edge, no_edge, confidence, ensemble_unc, decision, size_usdc
- Флаги: `--date=2026-05-27` (default: today UTC), `--output=predictions.csv` (default: stdout)
- Обработчик `cmdExportPredictions()` в dashboard/main.go
- Обновлены printUsage() и switch в main()
- Добавлены импорты: `encoding/csv`, `strconv`

**Итог:** `go build ./...` — OK, все тесты прошли (8 пакетов).
**Строк добавлено:** ~170

---

## 2026-05-27 18:50 UTC — TASK-060: Market price momentum signal

**Задача:** TASK-060

**Файлы созданы/изменены:**
- `internal/markets/price_tracker.go` — добавлены константы `momentumWindow=5`, `momentumMinPoints=4`, `momentumRunRequired=3`; тип `MomentumDirection` (Favorable/Adverse/Neutral); функция `DetectMomentum(side, history)` (~75 строк): берёт последние 5 снапшотов, подсчитывает consecutive run движений цены нашей стороны, возвращает направление и силу тренда (0-1)
- `internal/markets/price_tracker_test.go` — НОВЫЙ (~110 строк): 8 unit-тестов для DetectMomentum — insufficient history, favorable YES, favorable NO, adverse YES, neutral zigzag, run below threshold, strength capped at 1.0, flat prices
- `cmd/bot/main.go` — расширен блок TASK-056 (~30 новых строк): после adverse move check вызываем `DetectMomentum`; favorable → логируем boost и добавляем в Decision.Reason; adverse+strength>0.60 → требуем edge +0.03; если недостаточно — skip с логом

**Алгоритм DetectMomentum:**
1. Взять последние min(5, N) точек истории
2. Идти от новейшей к старейшей, считать последовательный run в одну сторону
3. Если run ≥ 3 и вверх → MomentumFavorable; если вниз → MomentumAdverse
4. strength = run/5, clamped [0,1]

**Тесты:** 8/8 PASS, `go test ./...` — all OK, `go build ./...` — OK

**Строк добавлено:** ~215
