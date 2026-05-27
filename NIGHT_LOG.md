# Night Log — Polymarket Weather Bot

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
