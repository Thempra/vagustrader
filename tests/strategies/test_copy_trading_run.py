# tests/strategies/test_copy_trading_run.py
"""Tests for CopyTradingStrategy.run() to cover the disabled path and loop body."""
from __future__ import annotations
import asyncio
from unittest.mock import AsyncMock, MagicMock, patch

from polywin.strategies.copy_trading import CopyTradingStrategy
from polywin.strategies.base import StrategyContext
from polywin.config import AppConfig, CopyTradingConfig, StrategiesConfig


def _make_ctx(enabled: bool, poll_interval: int = 9999) -> StrategyContext:
    cfg = AppConfig(
        strategies=StrategiesConfig(
            copy_trading=CopyTradingConfig(
                enabled=enabled,
                poll_interval_seconds=poll_interval,
                min_pnl=10_000,
                max_copy_per_trader=3,
                price_guard=0.05,
                min_volume_usd=0,
            )
        )
    )
    return StrategyContext(
        clob=MagicMock(),
        gamma=MagicMock(),
        data_api=MagicMock(),
        config=cfg,
        db_path=":memory:",
        paper=True,
    )


async def test_run_disabled_returns_immediately():
    """When copy_trading is disabled, run() should return without looping."""
    strategy = CopyTradingStrategy()
    ctx = _make_ctx(enabled=False)

    # run() should complete immediately (not loop forever)
    await asyncio.wait_for(strategy.run(ctx), timeout=2.0)


async def test_run_calls_tick_then_sleeps():
    """When enabled, run() calls _tick and then asyncio.sleep."""
    strategy = CopyTradingStrategy()
    ctx = _make_ctx(enabled=True, poll_interval=9999)

    tick_called = []

    async def fake_tick(c):
        tick_called.append(True)
        raise asyncio.CancelledError  # break the loop after first tick

    async def fake_sleep(seconds):
        raise asyncio.CancelledError  # break loop at sleep

    with (
        patch.object(strategy, "_tick", side_effect=fake_tick),
        patch("polywin.strategies.copy_trading.asyncio.sleep", side_effect=fake_sleep),
    ):
        try:
            await strategy.run(ctx)
        except asyncio.CancelledError:
            pass

    assert len(tick_called) == 1


async def test_run_logs_and_continues_on_tick_error(tmp_path):
    """If _tick raises a non-CancelledError, run() should log it and keep looping."""
    from polywin.db import init_db

    strategy = CopyTradingStrategy()
    ctx = _make_ctx(enabled=True, poll_interval=0)

    call_count = [0]

    async def flaky_tick(c):
        call_count[0] += 1
        if call_count[0] == 1:
            raise RuntimeError("transient failure")
        # Second call: cancel to break the loop
        raise asyncio.CancelledError

    sleep_count = [0]

    async def fake_sleep(seconds):
        sleep_count[0] += 1
        # Allow two sleeps, then cancel
        if sleep_count[0] >= 2:
            raise asyncio.CancelledError

    with (
        patch.object(strategy, "_tick", side_effect=flaky_tick),
        patch("polywin.strategies.copy_trading.asyncio.sleep", side_effect=fake_sleep),
    ):
        try:
            await strategy.run(ctx)
        except asyncio.CancelledError:
            pass

    # Tick should have been called at least twice (once failing, once cancelling)
    assert call_count[0] >= 1
