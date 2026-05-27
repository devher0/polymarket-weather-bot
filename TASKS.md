# Polymarket Weather Bot — Task Queue

> Автономный агент берёт задачи сверху вниз. Не более 5 файлов / 300 строк за итерацию.
> Выполненные задачи отмечаются [x] и дата. Лог в NIGHT_LOG.md.

---

## 🔴 ПРИОРИТЕТ 1 — Данные (основа всего)

### TASK-001: NASA POWER API collector
**Файл:** `collectors/nasa_power.py`
Подключить NASA POWER API (https://power.larc.nasa.gov/api/temporal/daily/point)
- Параметры: T2M (температура), PRECTOTCORR (осадки), WS2M (ветер), RH2M (влажность)
- Без API ключа, бесплатно
- Возвращать те же dataclass WeatherForecast что и weather.py
- Кэшировать ответы на 6 часов
- Тестировать: python collectors/nasa_power.py

### TASK-002: NOAA NWS API collector (США)
**Файл:** `collectors/noaa_nws.py`
Подключить National Weather Service API (https://api.weather.gov)
- Эндпоинт: /points/{lat},{lon} → /gridpoints/.../forecast
- Только США (New York, Miami) — это ограничение NWS
- Извлекать: температура, вероятность осадков, описание погоды
- Без API ключа
- Тестировать: python collectors/noaa_nws.py

### TASK-003: ESA Copernicus ERA5 collector
**Файл:** `collectors/copernicus_era5.py`
Подключить Copernicus Climate Data Store (https://cds.climate.copernicus.eu)
- Библиотека: cdsapi (pip install cdsapi)
- Данные: реанализ ERA5 — исторические данные для калибровки моделей
- Нужна регистрация и ~/.cdsapirc (добавить в README)
- Сохранять данные в data/era5/ как parquet
- Тестировать: python collectors/copernicus_era5.py

### TASK-004: GOES-19 satellite imagery (NOAA AWS)
**Файл:** `collectors/goes_satellite.py`
Подключить GOES-19 через NOAA Open Data на AWS S3 (noaa-goes19 bucket)
- Без авторизации, публичный bucket
- Библиотека: boto3 (anonymous access)
- Брать последний Cloud Top Temperature (Band 13) снимок
- Извлекать среднюю облачность над нашими городами (lat/lon bbox)
- Сохранять статистику в data/satellite/
- Тестировать: python collectors/goes_satellite.py

### TASK-005: Data aggregator — fusion всех источников
**Файл:** `collectors/aggregator.py`
Объединить все источники в единый FusedForecast:
- Взвешенное среднее вероятностей (NASA: 0.3, Open-Meteo: 0.4, NOAA: 0.2, GOES: 0.1)
- Если источник недоступен — пересчитать веса на оставшихся
- Добавить поле confidence (0-1): насколько источники согласны между собой
- Тестировать: python collectors/aggregator.py

---

## 🟡 ПРИОРИТЕТ 2 — Стратегия и сигналы

### TASK-006: Улучшить классификатор рынков
**Файл:** `markets.py`
- Добавить regex для температурных порогов: "above 30°F", "below 0°C", "exceed 95°F"
- Парсить температурный порог из вопроса и передавать в heat_probability()
- Добавить маппинг городов: больше синонимов (NYC → new_york, etc.)
- Добавить поддержку humidity, UV index рынков

### TASK-007: Калибровка вероятностей (Brier score)
**Файл:** `calibration.py`
- Сохранять каждую нашу ставку с нашей_prob и исходом (resolved YES/NO)
- Считать Brier score и reliability diagram
- Применять Platt scaling для калибровки
- Данные хранить в data/bets_history.csv

### TASK-008: Ensemble model
**Файл:** `strategy.py` (обновить)
- Использовать FusedForecast из aggregator.py вместо одного источника
- Если confidence < 0.4 — не ставить (данные расходятся)
- Логировать contribution каждого источника в решение

---

## 🟢 ПРИОРИТЕТ 3 — Инфраструктура

### TASK-009: Исторические данные для бэктеста
**Файл:** `backtest.py`
- Скачать Open-Meteo historical data за последние 6 месяцев
- Симулировать ставки на исторических ценах Polymarket (gamma API)
- Вывести: total P&L, win rate, avg edge, Sharpe ratio

### TASK-010: Dashboard (CLI)
**Файл:** `dashboard.py`
- Вывод в терминал: текущие открытые позиции, P&L, следующие ставки
- Команды: python dashboard.py positions / pnl / next-bets
- Использовать rich или tabulate

### TASK-011: Telegram уведомления
**Файл:** `notifier.py`
- Отправлять сообщение в Telegram при каждой ставке (реальной)
- Отправлять ежедневный дайджест P&L в 09:00
- Использовать TELEGRAM_BOT_TOKEN из .env

### TASK-012: Docker + README финальный
**Файл:** `Dockerfile`, `docker-compose.yml`, `README.md`
- Dockerize проект
- docker-compose с cron для запуска каждый час
- Обновить README: полный гайд с нуля

---

## ✅ ВЫПОЛНЕНО

- [x] 2026-05-27 — TASK-000: Базовый бот (weather.py, markets.py, strategy.py, bot.py)
