"""
Weather data provider using Open-Meteo (free, no API key needed).
Returns forecast probabilities for use in Polymarket betting.
"""
import openmeteo_requests
import requests_cache
from retry_requests import retry
import pandas as pd
from dataclasses import dataclass
from loguru import logger


@dataclass
class WeatherForecast:
    city: str
    lat: float
    lon: float
    date: str  # YYYY-MM-DD
    max_temp_c: float
    min_temp_c: float
    precipitation_mm: float
    precipitation_probability: float  # 0-100
    wind_speed_kmh: float
    weather_code: int  # WMO code


# Cities to monitor (add/remove as needed)
CITIES = {
    "new_york":  {"lat": 40.71, "lon": -74.01},
    "london":    {"lat": 51.51, "lon": -0.13},
    "tokyo":     {"lat": 35.68, "lon": 139.69},
    "miami":     {"lat": 25.77, "lon": -80.19},
    "paris":     {"lat": 48.85, "lon": 2.35},
}


def get_forecast(city: str, days: int = 7) -> list[WeatherForecast]:
    """Fetch weather forecast for a city, returns list of daily forecasts."""
    if city not in CITIES:
        raise ValueError(f"Unknown city: {city}. Available: {list(CITIES.keys())}")

    coords = CITIES[city]

    # Setup cached session with retry
    cache_session = requests_cache.CachedSession(".weather_cache", expire_after=3600)
    retry_session = retry(cache_session, retries=5, backoff_factor=0.2)
    om = openmeteo_requests.Client(session=retry_session)

    params = {
        "latitude": coords["lat"],
        "longitude": coords["lon"],
        "daily": [
            "temperature_2m_max",
            "temperature_2m_min",
            "precipitation_sum",
            "precipitation_probability_max",
            "wind_speed_10m_max",
            "weather_code",
        ],
        "forecast_days": days,
        "timezone": "UTC",
    }

    logger.debug(f"Fetching forecast for {city} ({coords['lat']}, {coords['lon']})")
    responses = om.weather_api("https://api.open-meteo.com/v1/forecast", params=params)
    response = responses[0]

    daily = response.Daily()
    dates = pd.date_range(
        start=pd.to_datetime(daily.Time(), unit="s", utc=True),
        end=pd.to_datetime(daily.TimeEnd(), unit="s", utc=True),
        freq=pd.Timedelta(seconds=daily.Interval()),
        inclusive="left",
    )

    forecasts = []
    for i, date in enumerate(dates):
        forecasts.append(WeatherForecast(
            city=city,
            lat=coords["lat"],
            lon=coords["lon"],
            date=date.strftime("%Y-%m-%d"),
            max_temp_c=float(daily.Variables(0).ValuesAsNumpy()[i]),
            min_temp_c=float(daily.Variables(1).ValuesAsNumpy()[i]),
            precipitation_mm=float(daily.Variables(2).ValuesAsNumpy()[i]),
            precipitation_probability=float(daily.Variables(3).ValuesAsNumpy()[i]),
            wind_speed_kmh=float(daily.Variables(4).ValuesAsNumpy()[i]),
            weather_code=int(daily.Variables(5).ValuesAsNumpy()[i]),
        ))

    return forecasts


def rain_probability(forecast: WeatherForecast) -> float:
    """Returns 0-1 probability of meaningful rain (>2mm)."""
    # Blend model precipitation probability with actual mm forecast
    if forecast.precipitation_mm > 10:
        return min(0.97, forecast.precipitation_probability / 100 + 0.1)
    elif forecast.precipitation_mm > 2:
        return forecast.precipitation_probability / 100
    else:
        # Low precip forecast lowers raw probability
        return max(0.03, forecast.precipitation_probability / 100 - 0.15)


def heat_probability(forecast: WeatherForecast, threshold_c: float = 35.0) -> float:
    """Returns 0-1 probability of extreme heat above threshold."""
    diff = forecast.max_temp_c - threshold_c
    if diff >= 3:
        return 0.93
    elif diff >= 0:
        return 0.70 + diff * 0.077
    elif diff >= -5:
        return max(0.05, 0.50 + diff * 0.09)
    else:
        return 0.03


if __name__ == "__main__":
    logger.info("Testing weather module...")
    forecasts = get_forecast("new_york", days=3)
    for f in forecasts:
        print(f"  {f.date}: max={f.max_temp_c:.1f}°C  precip={f.precipitation_mm:.1f}mm  rain_p={rain_probability(f):.2f}")
