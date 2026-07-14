# tests/strategies/test_copy_trading.py
import asyncio
from unittest.mock import MagicMock
from polywin.strategies.copy_trading import CopyTradingStrategy
from polywin.strategies.base import StrategyContext
from polywin.config import AppConfig, CopyTradingConfig, StrategiesConfig
from polywin.clients.data_api import LeaderboardEntry, Position
from polywin.clients.clob import PaperClobClient


async def _make_ctx(tmp_path, min_pnl=10_000):
    from polywin.db import init_db
    db_path = str(tmp_path / "test.db")
    await init_db(db_path)

    cfg = AppConfig(
        strategies=StrategiesConfig(
            copy_trading=CopyTradingConfig(
                enabled=True,
                poll_interval_seconds=9999,
                min_pnl=min_pnl,
                max_copy_per_trader=3,
                price_guard=0.05,
                min_volume_usd=0,
            )
        )
    )
    data_api = MagicMock()
    data_api.get_top_traders.return_value = [
        LeaderboardEntry("0xTRADER", "alpha", 50000.0, 1e6)
    ]
    data_api.get_positions.return_value = [
        Position("0xTOK_YES", "0xMKT", "Yes", 100.0, 0.65, 70.0)
    ]
    clob = PaperClobClient(db_path=db_path)

    return StrategyContext(
        clob=clob,
        gamma=MagicMock(),
        data_api=data_api,
        config=cfg,
        db_path=db_path,
        paper=True,
    ), db_path


async def test_tick_places_order_for_new_position(tmp_path):
    from polywin.db import get_today_trades
    ctx, db_path = await _make_ctx(tmp_path)
    strategy = CopyTradingStrategy()
    await strategy._tick(ctx)
    trades = await get_today_trades("2000-01-01", path=db_path)
    assert len(trades) == 1
    assert trades[0]["token_id"] == "0xTOK_YES"
    assert trades[0]["paper"] == 1


async def test_tick_skips_if_no_traders(tmp_path):
    from polywin.db import get_today_trades
    ctx, db_path = await _make_ctx(tmp_path)
    ctx.data_api.get_top_traders.return_value = []
    await CopyTradingStrategy()._tick(ctx)
    trades = await get_today_trades("2000-01-01", path=db_path)
    assert len(trades) == 0


async def test_tick_skips_already_held_token(tmp_path):
    from polywin.db import get_today_trades, upsert_position
    import time
    ctx, db_path = await _make_ctx(tmp_path)
    await upsert_position(
        token_id="0xTOK_YES", market_id="0xMKT", outcome="Yes",
        size=50.0, avg_price=0.65, updated_ts=int(time.time()), path=db_path,
    )
    await CopyTradingStrategy()._tick(ctx)
    trades = await get_today_trades("2000-01-01", path=db_path)
    assert len(trades) == 0
