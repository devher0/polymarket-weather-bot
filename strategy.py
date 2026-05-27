"""
Betting strategy: compare our weather model probability vs Polymarket price.
Bet when we have a meaningful edge.
"""
from dataclasses import dataclass
from loguru import logger
from weather import WeatherForecast, rain_probability, heat_probability
from markets import WeatherMarket


@dataclass
class BetDecision:
    market: WeatherMarket
    side: str          # "YES" or "NO"
    token_id: str
    our_probability: float
    market_probability: float  # = yes_price (Polymarket prices ≈ probabilities)
    edge: float
    size_usdc: float
    reason: str


def kelly_fraction(edge: float, odds: float, bankroll: float, max_fraction: float = 0.05) -> float:
    """
    Half-Kelly criterion for bet sizing.
    edge   = our_p - market_p
    odds   = 1/market_p  (decimal odds for YES)
    """
    if edge <= 0:
        return 0.0
    b = odds - 1  # net decimal odds
    p = edge + (1 / odds)  # our estimated win prob
    q = 1 - p
    kelly = (b * p - q) / b
    half_kelly = kelly / 2
    return min(max_fraction, max(0.0, half_kelly)) * bankroll


def evaluate_market(
    market: WeatherMarket,
    forecasts: dict[str, list[WeatherForecast]],
    bankroll: float,
    min_edge: float = 0.05,
    max_bet: float = 5.0,
) -> BetDecision | None:
    """
    Compare our weather forecast to Polymarket price.
    Returns a BetDecision if there's sufficient edge, else None.
    """
    if market.city is None or market.city not in forecasts:
        logger.debug(f"No forecast for city={market.city}, skipping: {market.question[:60]}")
        return None

    city_forecasts = forecasts[market.city]
    if not city_forecasts:
        return None

    # Use next-day forecast as primary signal
    forecast = city_forecasts[0]

    # Calculate our probability based on signal type
    if market.signal == "rain":
        our_p = rain_probability(forecast)
    elif market.signal == "heat":
        # Try to extract threshold from question
        our_p = heat_probability(forecast, threshold_c=35.0)
    elif market.signal == "cold":
        our_p = 1 - heat_probability(forecast, threshold_c=10.0)
    elif market.signal == "snow":
        # Snow needs cold + precip
        cold_p = 1 - heat_probability(forecast, threshold_c=2.0)
        our_p = cold_p * rain_probability(forecast) * 0.8
    elif market.signal == "wind":
        our_p = min(0.95, forecast.wind_speed_kmh / 80.0)
    else:
        logger.debug(f"Unhandled signal: {market.signal}")
        return None

    market_p = market.yes_price  # Polymarket YES price ≈ implied probability

    yes_edge = our_p - market_p
    no_edge = (1 - our_p) - market.no_price

    if abs(yes_edge) >= min_edge and yes_edge == max(yes_edge, no_edge):
        side = "YES"
        edge = yes_edge
        token_id = market.yes_token_id
        odds = 1 / market_p if market_p > 0 else 2.0
    elif abs(no_edge) >= min_edge and no_edge > yes_edge:
        side = "NO"
        edge = no_edge
        token_id = market.no_token_id
        odds = 1 / market.no_price if market.no_price > 0 else 2.0
    else:
        logger.debug(
            f"No edge on '{market.question[:50]}': yes_edge={yes_edge:.3f} no_edge={no_edge:.3f}"
        )
        return None

    size = kelly_fraction(edge, odds, bankroll)
    size = min(size, max_bet)

    if size < 0.5:
        logger.debug(f"Bet too small (${size:.2f}), skipping")
        return None

    reason = (
        f"{market.city}/{market.signal}: our_p={our_p:.2f} "
        f"market_p={market_p:.2f} edge={edge:+.2f}"
    )

    return BetDecision(
        market=market,
        side=side,
        token_id=token_id,
        our_probability=our_p,
        market_probability=market_p,
        edge=edge,
        size_usdc=size,
        reason=reason,
    )
