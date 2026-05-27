"""
Polymarket Weather Bot
======================
Main entry point. Runs on a schedule, finds weather markets,
compares our forecast vs market price, and places bets with edge.

Usage:
    python bot.py              # run once (dry run by default)
    python bot.py --live       # real money mode (careful!)
    python bot.py --loop       # run every hour

Configure via .env file (see .env.example).
"""
import os
import sys
import time
import argparse
from datetime import datetime

from dotenv import load_dotenv
from loguru import logger
from py_clob_client.client import ClobClient
from py_clob_client.clob_types import ApiCreds, OrderArgs, OrderType
from py_clob_client.constants import POLYGON

from weather import get_forecast, CITIES
from markets import get_weather_markets
from strategy import evaluate_market, BetDecision

load_dotenv()

# ── Config ─────────────────────────────────────────────────────────────────────
PRIVATE_KEY    = os.getenv("POLYMARKET_PRIVATE_KEY", "")
API_KEY        = os.getenv("POLYMARKET_API_KEY", "")
API_SECRET     = os.getenv("POLYMARKET_API_SECRET", "")
API_PASSPHRASE = os.getenv("POLYMARKET_API_PASSPHRASE", "")
DRY_RUN        = os.getenv("DRY_RUN", "true").lower() == "true"
MAX_BET_USDC   = float(os.getenv("MAX_BET_USDC", "5"))
MIN_EDGE       = float(os.getenv("MIN_EDGE", "0.05"))
LOG_LEVEL      = os.getenv("LOG_LEVEL", "info").upper()

logger.remove()
logger.add(sys.stderr, level=LOG_LEVEL, format="<green>{time:HH:mm:ss}</green> | <level>{level: <8}</level> | {message}")
logger.add("logs/bot_{time:YYYY-MM-DD}.log", level="DEBUG", rotation="1 day", retention="7 days")

POLYMARKET_HOST = "https://clob.polymarket.com"
CHAIN_ID = POLYGON


def build_client() -> ClobClient:
    if not PRIVATE_KEY:
        raise ValueError("POLYMARKET_PRIVATE_KEY not set in .env")

    creds = None
    if API_KEY:
        creds = ApiCreds(
            api_key=API_KEY,
            api_secret=API_SECRET,
            api_passphrase=API_PASSPHRASE,
        )

    client = ClobClient(
        host=POLYMARKET_HOST,
        key=PRIVATE_KEY,
        chain_id=CHAIN_ID,
        creds=creds,
    )

    if not API_KEY:
        logger.info("No API credentials found, creating new ones...")
        new_creds = client.create_or_derive_api_creds()
        logger.info(f"API Key: {new_creds.api_key}")
        logger.info("Save these to your .env file!")

    return client


def place_bet(client: ClobClient, decision: BetDecision) -> dict | None:
    """Place a market order on Polymarket. Returns order result or None."""
    logger.info(
        f"{'[DRY RUN] ' if DRY_RUN else ''}Placing {decision.side} bet: "
        f"${decision.size_usdc:.2f} on '{decision.market.question[:60]}'"
        f" | {decision.reason}"
    )

    if DRY_RUN:
        return {"status": "dry_run", "decision": decision}

    try:
        order_args = OrderArgs(
            price=decision.market_probability,
            size=decision.size_usdc,
            side=decision.side,
            token_id=decision.token_id,
        )
        result = client.create_and_post_order(order_args)
        logger.success(f"Order placed: {result}")
        return result
    except Exception as e:
        logger.error(f"Failed to place order: {e}")
        return None


def run_once(client: ClobClient) -> list[dict]:
    """Single bot cycle: fetch forecasts, find markets, place bets."""
    logger.info(f"=== Bot cycle starting {datetime.now().isoformat()} ===")

    # 1. Fetch weather forecasts for all cities
    forecasts = {}
    for city in CITIES:
        try:
            forecasts[city] = get_forecast(city, days=3)
            f = forecasts[city][0]
            logger.info(f"  {city}: {f.max_temp_c:.0f}°C max, {f.precipitation_mm:.1f}mm precip, rain_prob={f.precipitation_probability:.0f}%")
        except Exception as e:
            logger.warning(f"Failed to fetch forecast for {city}: {e}")

    # 2. Get weather markets from Polymarket
    try:
        markets = get_weather_markets(client)
    except Exception as e:
        logger.error(f"Failed to fetch markets: {e}")
        return []

    if not markets:
        logger.warning("No weather markets found — check if Polymarket has active weather questions")
        return []

    # 3. Evaluate each market and collect bets
    results = []
    for market in markets:
        decision = evaluate_market(
            market=market,
            forecasts=forecasts,
            bankroll=100.0,  # assume $100 bankroll for Kelly sizing
            min_edge=MIN_EDGE,
            max_bet=MAX_BET_USDC,
        )

        if decision:
            result = place_bet(client, decision)
            if result:
                results.append(result)

    if not results:
        logger.info("No bets placed this cycle (no sufficient edge found)")
    else:
        logger.success(f"Placed {len(results)} bet(s) this cycle")

    return results


def main():
    parser = argparse.ArgumentParser(description="Polymarket Weather Betting Bot")
    parser.add_argument("--live", action="store_true", help="Disable dry run (real money!)")
    parser.add_argument("--loop", action="store_true", help="Run every hour continuously")
    parser.add_argument("--interval", type=int, default=3600, help="Loop interval in seconds (default: 3600)")
    args = parser.parse_args()

    global DRY_RUN
    if args.live:
        DRY_RUN = False
        logger.warning("⚠️  LIVE MODE — real money will be used!")
    else:
        logger.info("DRY RUN mode (set DRY_RUN=false or --live to trade for real)")

    os.makedirs("logs", exist_ok=True)

    try:
        client = build_client()
    except ValueError as e:
        logger.error(str(e))
        sys.exit(1)

    if args.loop:
        logger.info(f"Running in loop mode every {args.interval}s")
        while True:
            try:
                run_once(client)
            except Exception as e:
                logger.error(f"Cycle error: {e}")
            logger.info(f"Sleeping {args.interval}s...")
            time.sleep(args.interval)
    else:
        run_once(client)


if __name__ == "__main__":
    main()
