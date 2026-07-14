# tests/test_cli.py
"""CLI tests using typer's CliRunner — all external deps mocked at their source module."""
from __future__ import annotations
from unittest.mock import AsyncMock, MagicMock, patch

import pytest
from typer.testing import CliRunner

from polywin.cli import app

runner = CliRunner()

FAKE_KEY = "0x" + "a" * 64


def _fake_settings(**kwargs):
    """Return a MagicMock that looks enough like Settings for CLI tests."""
    s = MagicMock()
    s.private_key = FAKE_KEY
    s.gamma_api_url = "https://gamma-api.polymarket.com"
    s.data_api_url = "https://data-api.polymarket.com"
    s.clob_host = "https://clob.polymarket.com"
    s.chain_id = 137
    s.clob_api_key = None
    s.clob_api_secret = None
    s.clob_api_passphrase = None
    for k, v in kwargs.items():
        setattr(s, k, v)
    return s


# ---------------------------------------------------------------------------
# `run` command  — all imports are lazy inside the function body, so we patch
# at the *source* module locations rather than on polywin.cli
# ---------------------------------------------------------------------------

def test_run_paper_mode_prints_paper():
    """run --paper should print PAPER and invoke engine.start."""
    mock_engine = MagicMock()
    mock_engine.start = AsyncMock()

    with (
        patch("polywin.config.Settings", return_value=_fake_settings()),
        patch("polywin.config.load_app_config") as mock_cfg,
        patch("polywin.logger.setup_logging"),
        patch("polywin.engine.Engine", return_value=mock_engine),
        patch("asyncio.run"),  # prevent real event loop in tests
    ):
        app_cfg = MagicMock()
        app_cfg.strategies.copy_trading.enabled = False
        mock_cfg.return_value = app_cfg

        result = runner.invoke(app, ["run", "--paper"])

    assert result.exit_code == 0, result.output
    assert "PAPER" in result.output


def test_run_live_mode_prints_live():
    """run without --paper should print LIVE."""
    mock_engine = MagicMock()

    with (
        patch("polywin.config.Settings", return_value=_fake_settings()),
        patch("polywin.config.load_app_config") as mock_cfg,
        patch("polywin.logger.setup_logging"),
        patch("polywin.engine.Engine", return_value=mock_engine),
        patch("asyncio.run"),
    ):
        app_cfg = MagicMock()
        app_cfg.strategies.copy_trading.enabled = False
        mock_cfg.return_value = app_cfg

        result = runner.invoke(app, ["run"])

    assert result.exit_code == 0, result.output
    assert "LIVE" in result.output


def test_run_registers_copy_trading_when_enabled():
    """When copy_trading.enabled is True, engine.register should be called once."""
    mock_engine = MagicMock()

    with (
        patch("polywin.config.Settings", return_value=_fake_settings()),
        patch("polywin.config.load_app_config") as mock_cfg,
        patch("polywin.logger.setup_logging"),
        patch("polywin.engine.Engine", return_value=mock_engine),
        patch("asyncio.run"),
        patch("polywin.strategies.copy_trading.CopyTradingStrategy"),
    ):
        app_cfg = MagicMock()
        app_cfg.strategies.copy_trading.enabled = True
        mock_cfg.return_value = app_cfg

        runner.invoke(app, ["run"])

    mock_engine.register.assert_called_once()


# ---------------------------------------------------------------------------
# `status` command
# ---------------------------------------------------------------------------

def test_status_shows_positions():
    """status should print the positions table."""
    fake_positions = [
        {"token_id": "0xTOK_YES_FAKE", "outcome": "Yes", "size": 50.0, "avg_price": 0.65}
    ]
    fake_trades = [{"paper": False}, {"paper": True}]

    with (
        patch("polywin.config.Settings", return_value=_fake_settings()),
        patch("polywin.db.get_positions", new=AsyncMock(return_value=fake_positions)),
        patch("polywin.db.get_today_trades", new=AsyncMock(return_value=fake_trades)),
    ):
        result = runner.invoke(app, ["status"])

    assert result.exit_code == 0, result.output
    assert "Open Positions" in result.output


def test_status_empty_positions():
    """status with no positions should still exit cleanly."""
    with (
        patch("polywin.config.Settings", return_value=_fake_settings()),
        patch("polywin.db.get_positions", new=AsyncMock(return_value=[])),
        patch("polywin.db.get_today_trades", new=AsyncMock(return_value=[])),
    ):
        result = runner.invoke(app, ["status"])

    assert result.exit_code == 0, result.output
    assert "Open Positions (0)" in result.output


# ---------------------------------------------------------------------------
# `markets` command
# ---------------------------------------------------------------------------

def test_markets_lists_results():
    """markets should print a table with at least the question column."""
    from polywin.clients.gamma import Market

    fake_market = Market(
        id="mkt-1",
        question="Will ETH hit $5k?",
        outcomes=["Yes", "No"],
        outcome_prices=[0.60, 0.40],
        token_ids=["0xYES", "0xNO"],
        volume=50_000.0,
        liquidity=10_000.0,
        active=True,
        end_date="2026-12-01T00:00:00Z",
        condition_id="0xcond1",
    )

    with (
        patch("polywin.config.Settings", return_value=_fake_settings()),
        patch("polywin.clients.gamma.GammaClient") as MockGamma,
    ):
        instance = MockGamma.return_value.__enter__.return_value
        instance.get_tradeable_markets.return_value = [fake_market]
        result = runner.invoke(app, ["markets", "--min-vol", "10000", "--limit", "5"])

    assert result.exit_code == 0, result.output
    assert "Will ETH hit" in result.output


# ---------------------------------------------------------------------------
# `top-traders` command
# ---------------------------------------------------------------------------

def test_top_traders_lists_results():
    """top-traders should print a table with trader info."""
    from polywin.clients.data_api import LeaderboardEntry

    fake_trader = LeaderboardEntry(
        address="0xTRADERLONG", name="alpha", pnl=12345.0, volume=999_000.0
    )

    with (
        patch("polywin.config.Settings", return_value=_fake_settings()),
        patch("polywin.clients.data_api.DataApiClient") as MockData,
    ):
        instance = MockData.return_value.__enter__.return_value
        instance.get_top_traders.return_value = [fake_trader]
        result = runner.invoke(app, ["top-traders", "--min-pnl", "10000", "--limit", "5"])

    assert result.exit_code == 0, result.output
    assert "alpha" in result.output


def test_top_traders_address_truncated_when_no_name():
    """When trader.name is empty the address should appear truncated."""
    from polywin.clients.data_api import LeaderboardEntry

    fake_trader = LeaderboardEntry(
        address="0xADDRESSLONG123456", name="", pnl=1000.0, volume=50_000.0
    )

    with (
        patch("polywin.config.Settings", return_value=_fake_settings()),
        patch("polywin.clients.data_api.DataApiClient") as MockData,
    ):
        instance = MockData.return_value.__enter__.return_value
        instance.get_top_traders.return_value = [fake_trader]
        result = runner.invoke(app, ["top-traders"])

    assert result.exit_code == 0, result.output
    assert "…" in result.output


# ---------------------------------------------------------------------------
# `derive-creds` command
# ---------------------------------------------------------------------------

def test_derive_creds_prints_keys():
    """derive-creds should print CLOB_API_KEY to stdout."""
    from polywin.clients.creds import ClobCreds

    fake_creds = ClobCreds(api_key="key-abc", api_secret="secret-xyz", api_passphrase="pass-123")

    with (
        patch("polywin.config.Settings", return_value=_fake_settings()),
        patch("polywin.clients.creds.derive_clob_creds", return_value=fake_creds),
    ):
        result = runner.invoke(app, ["derive-creds"])

    assert result.exit_code == 0, result.output
    assert "CLOB_API_KEY" in result.output
    assert "key-abc" in result.output


# ---------------------------------------------------------------------------
# --help sanity check
# ---------------------------------------------------------------------------

def test_help_lists_all_commands():
    """polywin --help should list all 5 sub-commands."""
    result = runner.invoke(app, ["--help"])
    assert result.exit_code == 0
    for cmd in ("run", "status", "markets", "top-traders", "derive-creds"):
        assert cmd in result.output
