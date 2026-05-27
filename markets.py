"""
Polymarket market discovery and filtering.
Finds weather-related markets and maps them to our forecast signals.
"""
import re
from dataclasses import dataclass
from loguru import logger
from py_clob_client.client import ClobClient
from py_clob_client.clob_types import ApiCreds


@dataclass
class WeatherMarket:
    condition_id: str
    question: str
    yes_token_id: str
    no_token_id: str
    yes_price: float   # current best ask for YES
    no_price: float    # current best ask for NO
    city: str | None
    signal: str | None  # "rain", "heat", "cold", "wind", etc.
    end_date: str | None


# Regex patterns to match weather markets
WEATHER_PATTERNS = [
    (r"rain|precipitation|rainfall|rainy", "rain"),
    (r"temperature.*above|above.*\d+.*degree|heat wave|heatwave|hot day", "heat"),
    (r"temperature.*below|below.*\d+.*degree|cold snap|freeze", "cold"),
    (r"snow|snowfall|blizzard", "snow"),
    (r"wind|hurricane|typhoon|storm", "wind"),
    (r"sunny|sunshine|clear sky", "sunny"),
]

CITY_PATTERNS = {
    "new_york": r"new york|nyc|manhattan",
    "london": r"london|uk weather",
    "tokyo": r"tokyo|japan weather",
    "miami": r"miami|florida weather",
    "paris": r"paris|france weather",
}


def classify_market(question: str) -> tuple[str | None, str | None]:
    """Returns (city, signal) for a weather market question, or (None, None)."""
    q = question.lower()

    signal = None
    for pattern, sig in WEATHER_PATTERNS:
        if re.search(pattern, q):
            signal = sig
            break

    if signal is None:
        return None, None  # not a weather market

    city = None
    for city_key, pattern in CITY_PATTERNS.items():
        if re.search(pattern, q):
            city = city_key
            break

    return city, signal


def get_weather_markets(client: ClobClient) -> list[WeatherMarket]:
    """Fetch open markets from Polymarket and filter for weather-related ones."""
    logger.info("Fetching open markets from Polymarket...")

    markets = []
    next_cursor = None

    while True:
        kwargs = {"next_cursor": next_cursor} if next_cursor else {}
        resp = client.get_markets(**kwargs)

        for m in resp.get("data", []):
            # Skip closed/resolved markets
            if m.get("closed") or m.get("archived"):
                continue

            question = m.get("question", "")
            city, signal = classify_market(question)

            if signal is None:
                continue  # not weather

            # Get token prices
            tokens = m.get("tokens", [])
            yes_token = next((t for t in tokens if t.get("outcome") == "Yes"), None)
            no_token = next((t for t in tokens if t.get("outcome") == "No"), None)

            if not yes_token or not no_token:
                continue

            yes_price = float(yes_token.get("price", 0.5))
            no_price = float(no_token.get("price", 0.5))

            markets.append(WeatherMarket(
                condition_id=m.get("condition_id", ""),
                question=question,
                yes_token_id=yes_token.get("token_id", ""),
                no_token_id=no_token.get("token_id", ""),
                yes_price=yes_price,
                no_price=no_price,
                city=city,
                signal=signal,
                end_date=m.get("end_date_iso"),
            ))

        next_cursor = resp.get("next_cursor")
        if not next_cursor or next_cursor == "LTE=":
            break

    logger.info(f"Found {len(markets)} weather markets")
    return markets
