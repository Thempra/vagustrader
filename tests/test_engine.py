# tests/test_engine.py
"""Tests for the Engine class."""
from __future__ import annotations
import asyncio
from unittest.mock import AsyncMock, MagicMock, patch

import pytest

from polywin.engine import Engine
from polywin.config import AppConfig, CopyTradingConfig, StrategiesConfig
from polywin.clients.creds import ClobCreds

FAKE_KEY = "0x" + "a" * 64


def _make_settings(*, with_clob_key: bool = False) -> MagicMock:
    s = MagicMock()
    s.private_key = FAKE_KEY
    s.gamma_api_url = "https://gamma-api.polymarket.com"
    s.data_api_url = "https://data-api.polymarket.com"
    s.clob_host = "https://clob.polymarket.com"
    s.chain_id = 137
    if with_clob_key:
        s.clob_api_key = "existing-key"
        s.clob_api_secret = "existing-secret"
        s.clob_api_passphrase = "existing-pass"
    else:
        s.clob_api_key = None
        s.clob_api_secret = None
        s.clob_api_passphrase = None
    return s


def _make_config(enabled: bool = True) -> AppConfig:
    return AppConfig(
        strategies=StrategiesConfig(
            copy_trading=CopyTradingConfig(
                enabled=enabled,
                poll_interval_seconds=9999,
                min_pnl=10_000,
            )
        )
    )


# ---------------------------------------------------------------------------
# register()
# ---------------------------------------------------------------------------

def test_register_adds_strategy():
    engine = Engine(settings=_make_settings(), config=_make_config())
    strat = MagicMock()
    engine.register(strat)
    assert strat in engine._strategies


def test_register_multiple_strategies():
    engine = Engine(settings=_make_settings(), config=_make_config())
    s1, s2 = MagicMock(), MagicMock()
    engine.register(s1)
    engine.register(s2)
    assert len(engine._strategies) == 2


# ---------------------------------------------------------------------------
# _resolve_creds()
# ---------------------------------------------------------------------------

def test_resolve_creds_uses_existing_key():
    """When clob_api_key is already set, derive_clob_creds should NOT be called."""
    engine = Engine(settings=_make_settings(with_clob_key=True), config=_make_config())
    with patch("polywin.engine.derive_clob_creds") as mock_derive:
        creds = engine._resolve_creds()
    mock_derive.assert_not_called()
    assert creds.api_key == "existing-key"


def test_resolve_creds_derives_when_no_key():
    """When clob_api_key is None, derive_clob_creds should be called."""
    engine = Engine(settings=_make_settings(with_clob_key=False), config=_make_config())
    fake_creds = ClobCreds(api_key="derived-k", api_secret="derived-s", api_passphrase="derived-p")
    with patch("polywin.engine.derive_clob_creds", return_value=fake_creds) as mock_derive:
        creds = engine._resolve_creds()
    mock_derive.assert_called_once()
    assert creds.api_key == "derived-k"


# ---------------------------------------------------------------------------
# start() — paper mode
# ---------------------------------------------------------------------------

async def test_start_paper_mode_uses_paper_clob(tmp_path):
    """Engine.start in paper mode should instantiate PaperClobClient."""
    # Use with_clob_key=True so _resolve_creds doesn't try to call the network
    engine = Engine(
        settings=_make_settings(with_clob_key=True),
        config=_make_config(enabled=False),
        paper=True,
    )

    mock_strategy = MagicMock()
    # run() coroutine that immediately returns so the engine can finish
    mock_strategy.run = AsyncMock(return_value=None)
    mock_strategy.name = "mock_strategy"
    engine.register(mock_strategy)

    with (
        patch("polywin.engine.init_db", new=AsyncMock()),
        patch("polywin.engine.PaperClobClient") as MockPaper,
        patch("polywin.engine.GammaClient") as MockGamma,
        patch("polywin.engine.DataApiClient") as MockData,
        patch("polywin.engine.get_today_trades", new=AsyncMock(return_value=[])),
        patch("polywin.engine.save_pnl_snapshot", new=AsyncMock()),
        patch("polywin.engine.Engine._daily_snapshot", new=AsyncMock()),
    ):
        MockGamma.return_value.close = MagicMock()
        MockData.return_value.close = MagicMock()

        await engine.start()

    MockPaper.assert_called_once()


async def test_start_live_mode_uses_real_clob(tmp_path):
    """Engine.start in live mode should instantiate ClobClient."""
    engine = Engine(
        settings=_make_settings(with_clob_key=True),
        config=_make_config(enabled=False),
        paper=False,
    )

    mock_strategy = MagicMock()
    mock_strategy.run = AsyncMock(return_value=None)
    mock_strategy.name = "mock_live"
    engine.register(mock_strategy)

    with (
        patch("polywin.engine.init_db", new=AsyncMock()),
        patch("polywin.engine.ClobClient") as MockClob,
        patch("polywin.engine.GammaClient") as MockGamma,
        patch("polywin.engine.DataApiClient") as MockData,
        patch("polywin.engine.get_today_trades", new=AsyncMock(return_value=[])),
        patch("polywin.engine.save_pnl_snapshot", new=AsyncMock()),
        patch("polywin.engine.Engine._daily_snapshot", new=AsyncMock()),
    ):
        MockGamma.return_value.close = MagicMock()
        MockData.return_value.close = MagicMock()

        await engine.start()

    MockClob.assert_called_once()


async def test_start_propagates_strategy_exception(tmp_path):
    """If a strategy raises, Engine.start should re-raise."""
    engine = Engine(
        settings=_make_settings(with_clob_key=True),
        config=_make_config(enabled=False),
        paper=True,
    )

    boom = RuntimeError("strategy exploded")
    mock_strategy = MagicMock()
    mock_strategy.run = AsyncMock(side_effect=boom)
    mock_strategy.name = "exploding_strategy"
    engine.register(mock_strategy)

    with (
        patch("polywin.engine.init_db", new=AsyncMock()),
        patch("polywin.engine.PaperClobClient"),
        patch("polywin.engine.GammaClient") as MockGamma,
        patch("polywin.engine.DataApiClient") as MockData,
        patch("polywin.engine.get_today_trades", new=AsyncMock(return_value=[])),
        patch("polywin.engine.save_pnl_snapshot", new=AsyncMock()),
        patch("polywin.engine.Engine._daily_snapshot", new=AsyncMock()),
    ):
        MockGamma.return_value.close = MagicMock()
        MockData.return_value.close = MagicMock()

        with pytest.raises(RuntimeError, match="strategy exploded"):
            await engine.start()
