# Polywin — Plan 1: Core Infrastructure

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build the foundational Python package for the Polywin trading system — project scaffolding, configuration, CLOB credential derivation, Polymarket API clients (Gamma, Data API, CLOB), real-time WebSocket feed, wallet management, and structured logging — that all future strategies build upon.

**Architecture:** A `src/polywin/` package with one module per responsibility. REST clients wrap `httpx.Client`. The CLOB client wraps `py-clob-client`. CLOB API credentials are derived deterministically from the private key at startup (no manual setup needed). The WebSocket client streams real-time price updates with automatic reconnection. Configuration via `pydantic-settings`; CLOB creds are optional and auto-derived if absent. Tests mock HTTP via `pytest-httpx` and WebSocket connections via `unittest.mock`.

**Tech Stack:** Python 3.11, httpx, websockets, pydantic-settings, py-clob-client, eth-account, pytest, pytest-httpx, pytest-asyncio

---

## Project Breakdown (Multi-Plan Overview)

This spec covers 5 independent subsystems. This is Plan 1 of 5:

| Plan | Subsystem | Depends On |
|------|-----------|------------|
| **1 — Core Infrastructure** (this plan) | API clients, credential derivation, WebSocket feed, wallet | — |
| 2 — Copy Trading Strategy | Leaderboard tracker, wallet mirroring, order execution | Plan 1 |
| 3 — Arbitrage Strategy | Binance/Coinbase websocket, latency arb engine | Plan 1 |
| 4 — Risk Management | Kelly criterion sizing, stop-loss, drawdown limits | Plan 1 |
| 5 — Monitoring & Alerts | Telegram bot, P&L dashboard, on-chain fill verification | Plans 1–4 |

---

## File Map

```
polywin/
├── pyproject.toml
├── .env.example
├── .gitignore
├── pytest.ini
├── scripts/
│   └── smoke_test.py                     # live read-only verification
├── src/
│   └── polywin/
│       ├── __init__.py
│       ├── config.py                     # Settings via pydantic-settings (CLOB creds optional)
│       ├── logger.py                     # structured logger factory
│       ├── wallet.py                     # key loading, address derivation, message signing
│       └── clients/
│           ├── __init__.py
│           ├── creds.py                  # CLOB API key derivation from private key
│           ├── gamma.py                  # Gamma API: markets + token_ids, volume filter
│           ├── data_api.py               # Data API: leaderboard, positions, activity
│           ├── clob.py                   # CLOB: orderbook, place/cancel limit orders
│           └── ws.py                     # WebSocket: real-time price feed, auto-reconnect
└── tests/
    ├── __init__.py
    ├── conftest.py
    ├── test_config.py
    ├── test_wallet.py
    └── clients/
        ├── __init__.py
        ├── test_creds.py
        ├── test_gamma.py
        ├── test_data_api.py
        ├── test_clob.py
        └── test_ws.py
```

---

## Task 1: Project Scaffolding

**Files:**
- Create: `pyproject.toml`
- Create: `.env.example`
- Create: `.gitignore`
- Create: `pytest.ini`
- Create: all `__init__.py` and directory markers

- [ ] **Step 1: Create `pyproject.toml`**

```toml
[project]
name = "polywin"
version = "0.1.0"
requires-python = ">=3.11"
dependencies = [
    "httpx>=0.27",
    "websockets>=12.0",
    "pydantic-settings>=2.0",
    "py-clob-client>=0.19",
    "eth-account>=0.11",
    "python-dotenv>=1.0",
]

[project.optional-dependencies]
dev = [
    "pytest>=8.0",
    "pytest-httpx>=0.30",
    "pytest-asyncio>=0.23",
    "pytest-cov>=5.0",
]

[build-system]
requires = ["hatchling"]
build-backend = "hatchling.build"

[tool.hatch.build.targets.wheel]
packages = ["src/polywin"]

[tool.pytest.ini_options]
asyncio_mode = "auto"
```

- [ ] **Step 2: Create `.env.example`**

```
# Copy to .env — NEVER commit .env
# Only PRIVATE_KEY is required; CLOB creds are derived automatically on startup.
PRIVATE_KEY=0x0000000000000000000000000000000000000000000000000000000000000001

# Optional: pre-fill derived CLOB creds to skip on-chain derivation at startup.
# Run scripts/derive_creds.py once to get these values.
CLOB_API_KEY=
CLOB_API_SECRET=
CLOB_API_PASSPHRASE=

POLYGON_RPC=https://polygon-rpc.com
```

- [ ] **Step 3: Create `.gitignore`**

```
.env
.venv/
__pycache__/
*.pyc
.pytest_cache/
.coverage
dist/
*.egg-info/
```

- [ ] **Step 4: Create `pytest.ini`**

```ini
[pytest]
testpaths = tests
addopts = -v --tb=short
```

- [ ] **Step 5: Create directory structure and package markers**

```bash
mkdir -p src/polywin/clients tests/clients scripts
touch src/polywin/__init__.py src/polywin/clients/__init__.py
touch tests/__init__.py tests/clients/__init__.py
touch tests/conftest.py
```

- [ ] **Step 6: Install in dev mode**

```bash
python -m venv .venv && source .venv/bin/activate
pip install -e ".[dev]"
```

Expected: `Successfully installed polywin-0.1.0`

- [ ] **Step 7: Verify import**

```bash
python -c "import polywin; print('ok')"
```

Expected: `ok`

- [ ] **Step 8: Commit**

```bash
git init
git add pyproject.toml .env.example .gitignore pytest.ini src/ tests/ scripts/
git commit -m "chore: initialize polywin project structure"
```

---

## Task 2: Configuration Module

**Files:**
- Create: `src/polywin/config.py`
- Create: `tests/test_config.py`

CLOB credentials (`api_key`, `api_secret`, `api_passphrase`) are `Optional` — if absent from `.env`, they are derived at runtime by Task 6 (`creds.py`). Only `PRIVATE_KEY` is mandatory.

- [ ] **Step 1: Write the failing tests**

```python
# tests/test_config.py
import pytest


def test_settings_loads_private_key_and_defaults(monkeypatch):
    monkeypatch.setenv("PRIVATE_KEY", "0x" + "ab" * 32)
    monkeypatch.delenv("CLOB_API_KEY", raising=False)
    monkeypatch.delenv("CLOB_API_SECRET", raising=False)
    monkeypatch.delenv("CLOB_API_PASSPHRASE", raising=False)

    from polywin.config import Settings
    s = Settings()

    assert s.private_key == "0x" + "ab" * 32
    assert s.clob_api_key is None
    assert s.clob_api_secret is None
    assert s.clob_api_passphrase is None
    assert s.gamma_api_url == "https://gamma-api.polymarket.com"
    assert s.data_api_url == "https://data-api.polymarket.com"
    assert s.clob_host == "https://clob.polymarket.com"
    assert s.chain_id == 137


def test_settings_loads_optional_clob_creds(monkeypatch):
    monkeypatch.setenv("PRIVATE_KEY", "0x" + "ab" * 32)
    monkeypatch.setenv("CLOB_API_KEY", "key123")
    monkeypatch.setenv("CLOB_API_SECRET", "secret456")
    monkeypatch.setenv("CLOB_API_PASSPHRASE", "pass789")

    from polywin.config import Settings
    s = Settings()

    assert s.clob_api_key == "key123"
    assert s.clob_api_secret == "secret456"
    assert s.clob_api_passphrase == "pass789"


def test_settings_missing_private_key_raises(monkeypatch):
    monkeypatch.delenv("PRIVATE_KEY", raising=False)

    from polywin.config import Settings
    with pytest.raises(Exception):
        Settings()
```

- [ ] **Step 2: Run tests to confirm they fail**

```bash
pytest tests/test_config.py -v
```

Expected: `ERROR` — `ModuleNotFoundError: No module named 'polywin.config'`

- [ ] **Step 3: Implement `src/polywin/config.py`**

```python
from typing import Optional
from pydantic_settings import BaseSettings, SettingsConfigDict


class Settings(BaseSettings):
    model_config = SettingsConfigDict(env_file=".env", env_file_encoding="utf-8")

    private_key: str

    clob_api_key: Optional[str] = None
    clob_api_secret: Optional[str] = None
    clob_api_passphrase: Optional[str] = None

    gamma_api_url: str = "https://gamma-api.polymarket.com"
    data_api_url: str = "https://data-api.polymarket.com"
    clob_host: str = "https://clob.polymarket.com"
    polygon_rpc: str = "https://polygon-rpc.com"
    chain_id: int = 137
```

- [ ] **Step 4: Run tests to confirm they pass**

```bash
pytest tests/test_config.py -v
```

Expected: `3 passed`

- [ ] **Step 5: Commit**

```bash
git add src/polywin/config.py tests/test_config.py
git commit -m "feat: add configuration module (CLOB creds optional, auto-derived)"
```

---

## Task 3: Logger Module

**Files:**
- Create: `src/polywin/logger.py`
- Create: `tests/test_logger.py`

- [ ] **Step 1: Write the failing tests**

```python
# tests/test_logger.py
import logging
from polywin.logger import get_logger


def test_get_logger_returns_named_logger():
    logger = get_logger("polywin.test")
    assert isinstance(logger, logging.Logger)
    assert logger.name == "polywin.test"


def test_get_logger_same_instance_on_repeat_call():
    a = get_logger("polywin.x")
    b = get_logger("polywin.x")
    assert a is b
```

- [ ] **Step 2: Run tests to confirm they fail**

```bash
pytest tests/test_logger.py -v
```

Expected: `ERROR` — `ModuleNotFoundError: No module named 'polywin.logger'`

- [ ] **Step 3: Implement `src/polywin/logger.py`**

```python
import logging
import sys


def get_logger(name: str) -> logging.Logger:
    logger = logging.getLogger(name)
    if not logger.handlers:
        handler = logging.StreamHandler(sys.stdout)
        handler.setFormatter(logging.Formatter(
            "%(asctime)s | %(levelname)-8s | %(name)s | %(message)s",
            datefmt="%Y-%m-%d %H:%M:%S",
        ))
        logger.addHandler(handler)
        logger.setLevel(logging.INFO)
    return logger
```

- [ ] **Step 4: Run tests to confirm they pass**

```bash
pytest tests/test_logger.py -v
```

Expected: `2 passed`

- [ ] **Step 5: Commit**

```bash
git add src/polywin/logger.py tests/test_logger.py
git commit -m "feat: add structured logger factory"
```

---

## Task 4: Gamma API Client

**Files:**
- Create: `src/polywin/clients/gamma.py`
- Create: `tests/clients/test_gamma.py`

Key changes vs. naive implementation:
- `outcomes` and `outcomePrices` come as JSON-encoded strings — parser handles both formats.
- `clobTokenIds` is parsed into `token_ids: list[str]` — one token ID per outcome, required for WebSocket subscriptions and CLOB order placement.
- `get_tradeable_markets()` filters to markets with volume > threshold and price in (0.05, 0.95) — avoids near-resolved markets where edge is gone.

- [ ] **Step 1: Write the failing tests**

```python
# tests/clients/test_gamma.py
import pytest
from polywin.clients.gamma import GammaClient, Market


MARKET_FIXTURE = {
    "id": "market-1",
    "question": "Will BTC hit $100k?",
    "outcomes": '["Yes", "No"]',
    "outcomePrices": '["0.65", "0.35"]',
    "clobTokenIds": '[{"token_id": "0xTOKEN_YES", "outcome": "Yes"}, {"token_id": "0xTOKEN_NO", "outcome": "No"}]',
    "volume": "123456.78",
    "liquidity": "45678.90",
    "active": True,
    "endDate": "2026-06-01T00:00:00Z",
    "conditionId": "0xabc123",
}

NEAR_RESOLVED_FIXTURE = {**MARKET_FIXTURE, "outcomePrices": '["0.97", "0.03"]', "id": "market-nr"}


def test_get_markets_returns_list(httpx_mock):
    httpx_mock.add_response(
        url="https://gamma-api.polymarket.com/markets?limit=100&offset=0&active=true",
        json={"data": [MARKET_FIXTURE], "next_cursor": None},
    )
    markets = GammaClient().get_markets()

    assert len(markets) == 1
    m = markets[0]
    assert isinstance(m, Market)
    assert m.id == "market-1"
    assert m.outcomes == ["Yes", "No"]
    assert m.outcome_prices == [0.65, 0.35]
    assert m.volume == 123456.78
    assert m.active is True
    assert m.condition_id == "0xabc123"
    assert m.token_ids == ["0xTOKEN_YES", "0xTOKEN_NO"]


def test_get_market_by_condition_id(httpx_mock):
    httpx_mock.add_response(
        url="https://gamma-api.polymarket.com/markets/0xabc123",
        json=MARKET_FIXTURE,
    )
    m = GammaClient().get_market("0xabc123")
    assert m.condition_id == "0xabc123"
    assert m.token_ids == ["0xTOKEN_YES", "0xTOKEN_NO"]


def test_get_tradeable_markets_filters_low_volume(httpx_mock):
    low_vol = {**MARKET_FIXTURE, "volume": "500.00", "id": "market-low"}
    httpx_mock.add_response(
        url="https://gamma-api.polymarket.com/markets?limit=100&offset=0&active=true",
        json={"data": [MARKET_FIXTURE, low_vol], "next_cursor": None},
    )
    markets = GammaClient().get_tradeable_markets(min_volume=10_000)
    assert len(markets) == 1
    assert markets[0].id == "market-1"


def test_get_tradeable_markets_filters_near_resolved(httpx_mock):
    httpx_mock.add_response(
        url="https://gamma-api.polymarket.com/markets?limit=100&offset=0&active=true",
        json={"data": [MARKET_FIXTURE, NEAR_RESOLVED_FIXTURE], "next_cursor": None},
    )
    markets = GammaClient().get_tradeable_markets(min_volume=0, price_guard=0.05)
    assert len(markets) == 1
    assert markets[0].id == "market-1"


def test_get_markets_raises_on_http_error(httpx_mock):
    httpx_mock.add_response(
        url="https://gamma-api.polymarket.com/markets?limit=100&offset=0&active=true",
        status_code=503,
    )
    with pytest.raises(Exception):
        GammaClient().get_markets()
```

- [ ] **Step 2: Run tests to confirm they fail**

```bash
pytest tests/clients/test_gamma.py -v
```

Expected: `ERROR` — `ModuleNotFoundError: No module named 'polywin.clients.gamma'`

- [ ] **Step 3: Implement `src/polywin/clients/gamma.py`**

```python
import json
from dataclasses import dataclass, field
import httpx


@dataclass
class Market:
    id: str
    question: str
    outcomes: list[str]
    outcome_prices: list[float]
    token_ids: list[str]          # CLOB token IDs — one per outcome, needed for WS + orders
    volume: float
    liquidity: float
    active: bool
    end_date: str
    condition_id: str


class GammaClient:
    def __init__(self, base_url: str = "https://gamma-api.polymarket.com"):
        self._client = httpx.Client(base_url=base_url, timeout=10.0)

    def get_markets(self, limit: int = 100, offset: int = 0, active: bool = True) -> list[Market]:
        resp = self._client.get(
            "/markets",
            params={"limit": limit, "offset": offset, "active": str(active).lower()},
        )
        resp.raise_for_status()
        body = resp.json()
        raw = body.get("data", body) if isinstance(body, dict) else body
        return [self._parse(m) for m in raw]

    def get_market(self, condition_id: str) -> Market:
        resp = self._client.get(f"/markets/{condition_id}")
        resp.raise_for_status()
        return self._parse(resp.json())

    def get_tradeable_markets(
        self,
        min_volume: float = 10_000,
        price_guard: float = 0.05,
        limit: int = 100,
    ) -> list[Market]:
        """Return active markets filtered to min_volume and prices not near resolution."""
        markets = self.get_markets(limit=limit)
        return [
            m for m in markets
            if m.volume >= min_volume
            and m.outcome_prices
            and all(price_guard < p < (1 - price_guard) for p in m.outcome_prices)
        ]

    def _parse(self, data: dict) -> Market:
        outcomes = data.get("outcomes", [])
        if isinstance(outcomes, str):
            outcomes = json.loads(outcomes)

        prices = data.get("outcomePrices", [])
        if isinstance(prices, str):
            prices = json.loads(prices)

        clob_tokens = data.get("clobTokenIds", [])
        if isinstance(clob_tokens, str):
            clob_tokens = json.loads(clob_tokens)
        token_ids = [t["token_id"] for t in clob_tokens if isinstance(t, dict)]

        return Market(
            id=data["id"],
            question=data["question"],
            outcomes=outcomes,
            outcome_prices=[float(p) for p in prices],
            token_ids=token_ids,
            volume=float(data.get("volume", 0)),
            liquidity=float(data.get("liquidity", 0)),
            active=bool(data.get("active", False)),
            end_date=data.get("endDate", ""),
            condition_id=data.get("conditionId", data["id"]),
        )

    def close(self) -> None:
        self._client.close()

    def __enter__(self) -> "GammaClient":
        return self

    def __exit__(self, *_) -> None:
        self.close()
```

- [ ] **Step 4: Run tests to confirm they pass**

```bash
pytest tests/clients/test_gamma.py -v
```

Expected: `5 passed`

- [ ] **Step 5: Commit**

```bash
git add src/polywin/clients/gamma.py tests/clients/test_gamma.py
git commit -m "feat: add Gamma API client with token_ids and tradeable market filter"
```

---

## Task 5: Data API Client

**Files:**
- Create: `src/polywin/clients/data_api.py`
- Create: `tests/clients/test_data_api.py`

- [ ] **Step 1: Write the failing tests**

```python
# tests/clients/test_data_api.py
from polywin.clients.data_api import DataApiClient, LeaderboardEntry, Position


LEADERBOARD_FIXTURE = [
    {"address": "0xAAA", "name": "trader_a", "pnl": 50000.0, "volume": 1000000.0, "winRate": 0.75},
    {"address": "0xBBB", "name": "trader_b", "pnl": 30000.0, "volume": 500000.0, "winRate": 0.68},
]

POSITIONS_FIXTURE = [
    {"market": "market-1", "outcome": "Yes", "size": 200.0, "avgPrice": 0.60, "currentValue": 130.0},
]


def test_get_leaderboard_returns_entries(httpx_mock):
    httpx_mock.add_response(
        url="https://data-api.polymarket.com/leaderboard?limit=100&offset=0",
        json=LEADERBOARD_FIXTURE,
    )
    entries = DataApiClient().get_leaderboard()

    assert len(entries) == 2
    assert isinstance(entries[0], LeaderboardEntry)
    assert entries[0].address == "0xAAA"
    assert entries[0].pnl == 50000.0
    assert entries[0].win_rate == 0.75


def test_get_leaderboard_custom_limit(httpx_mock):
    httpx_mock.add_response(
        url="https://data-api.polymarket.com/leaderboard?limit=10&offset=0",
        json=LEADERBOARD_FIXTURE[:1],
    )
    assert len(DataApiClient().get_leaderboard(limit=10)) == 1


def test_get_positions_returns_positions(httpx_mock):
    httpx_mock.add_response(
        url="https://data-api.polymarket.com/positions?user=0xAAA",
        json=POSITIONS_FIXTURE,
    )
    positions = DataApiClient().get_positions("0xAAA")

    assert len(positions) == 1
    p = positions[0]
    assert isinstance(p, Position)
    assert p.market_id == "market-1"
    assert p.outcome == "Yes"
    assert p.size == 200.0
    assert p.avg_price == 0.60


def test_get_activity_returns_raw_list(httpx_mock):
    activity = [{"type": "BUY", "market": "market-1", "size": 100.0, "price": 0.6}]
    httpx_mock.add_response(
        url="https://data-api.polymarket.com/activity?user=0xAAA&limit=100",
        json=activity,
    )
    assert DataApiClient().get_activity("0xAAA") == activity


def test_get_top_traders_filters_by_win_rate(httpx_mock):
    httpx_mock.add_response(
        url="https://data-api.polymarket.com/leaderboard?limit=100&offset=0",
        json=LEADERBOARD_FIXTURE,
    )
    top = DataApiClient().get_top_traders(min_win_rate=0.70)
    assert len(top) == 1
    assert top[0].address == "0xAAA"
```

- [ ] **Step 2: Run tests to confirm they fail**

```bash
pytest tests/clients/test_data_api.py -v
```

Expected: `ERROR` — `ModuleNotFoundError: No module named 'polywin.clients.data_api'`

- [ ] **Step 3: Implement `src/polywin/clients/data_api.py`**

```python
from dataclasses import dataclass
import httpx


@dataclass
class LeaderboardEntry:
    address: str
    name: str
    pnl: float
    volume: float
    win_rate: float


@dataclass
class Position:
    market_id: str
    outcome: str
    size: float
    avg_price: float
    current_value: float


class DataApiClient:
    def __init__(self, base_url: str = "https://data-api.polymarket.com"):
        self._client = httpx.Client(base_url=base_url, timeout=10.0)

    def get_leaderboard(self, limit: int = 100, offset: int = 0) -> list[LeaderboardEntry]:
        resp = self._client.get("/leaderboard", params={"limit": limit, "offset": offset})
        resp.raise_for_status()
        return [self._parse_entry(e) for e in resp.json()]

    def get_top_traders(self, min_win_rate: float = 0.70, limit: int = 100) -> list[LeaderboardEntry]:
        """Return leaderboard entries filtered to consistent winners — primary input for copy trading."""
        entries = self.get_leaderboard(limit=limit)
        return [e for e in entries if e.win_rate >= min_win_rate]

    def get_positions(self, user_address: str) -> list[Position]:
        resp = self._client.get("/positions", params={"user": user_address})
        resp.raise_for_status()
        return [self._parse_position(p) for p in resp.json()]

    def get_activity(self, user_address: str, limit: int = 100) -> list[dict]:
        resp = self._client.get("/activity", params={"user": user_address, "limit": limit})
        resp.raise_for_status()
        return resp.json()

    def _parse_entry(self, data: dict) -> LeaderboardEntry:
        return LeaderboardEntry(
            address=data.get("address", ""),
            name=data.get("name", ""),
            pnl=float(data.get("pnl", 0)),
            volume=float(data.get("volume", 0)),
            win_rate=float(data.get("winRate", 0)),
        )

    def _parse_position(self, data: dict) -> Position:
        return Position(
            market_id=data.get("market", ""),
            outcome=data.get("outcome", ""),
            size=float(data.get("size", 0)),
            avg_price=float(data.get("avgPrice", 0)),
            current_value=float(data.get("currentValue", 0)),
        )

    def close(self) -> None:
        self._client.close()

    def __enter__(self) -> "DataApiClient":
        return self

    def __exit__(self, *_) -> None:
        self.close()
```

- [ ] **Step 4: Run tests to confirm they pass**

```bash
pytest tests/clients/test_data_api.py -v
```

Expected: `5 passed`

- [ ] **Step 5: Commit**

```bash
git add src/polywin/clients/data_api.py tests/clients/test_data_api.py
git commit -m "feat: add Data API client with top-trader filter"
```

---

## Task 6: CLOB Credential Derivation

**Files:**
- Create: `src/polywin/clients/creds.py`
- Create: `tests/clients/test_creds.py`

**Why this exists:** Polymarket's CLOB API requires `api_key`, `api_secret`, and `api_passphrase`. These are NOT arbitrary passwords — they are derived deterministically from your private key by signing a challenge on-chain. `py-clob-client` does this via `derive_api_key()`. The result is always the same for a given key, so credentials can be cached in `.env` to skip the derivation call on subsequent startups.

- [ ] **Step 1: Write the failing tests**

```python
# tests/clients/test_creds.py
from unittest.mock import MagicMock, patch
from polywin.clients.creds import derive_clob_creds, ClobCreds


def test_derive_clob_creds_returns_typed_creds():
    mock_creds = MagicMock()
    mock_creds.api_key = "key-abc"
    mock_creds.api_secret = "secret-xyz"
    mock_creds.api_passphrase = "pass-123"

    mock_client = MagicMock()
    mock_client.derive_api_key.return_value = mock_creds

    with patch("polywin.clients.creds._ClobClient", return_value=mock_client):
        result = derive_clob_creds(
            private_key="0x" + "a" * 64,
            host="https://clob.polymarket.com",
            chain_id=137,
        )

    assert isinstance(result, ClobCreds)
    assert result.api_key == "key-abc"
    assert result.api_secret == "secret-xyz"
    assert result.api_passphrase == "pass-123"
    mock_client.derive_api_key.assert_called_once()


def test_derive_clob_creds_uses_correct_chain_id():
    mock_creds = MagicMock(api_key="k", api_secret="s", api_passphrase="p")
    mock_client = MagicMock()
    mock_client.derive_api_key.return_value = mock_creds

    with patch("polywin.clients.creds._ClobClient", return_value=mock_client) as MockClobClient:
        derive_clob_creds("0x" + "b" * 64, chain_id=137)
        _, kwargs = MockClobClient.call_args
        assert kwargs.get("chain_id") == 137 or MockClobClient.call_args[0][2] == 137
```

- [ ] **Step 2: Run tests to confirm they fail**

```bash
pytest tests/clients/test_creds.py -v
```

Expected: `ERROR` — `ModuleNotFoundError: No module named 'polywin.clients.creds'`

- [ ] **Step 3: Implement `src/polywin/clients/creds.py`**

```python
from dataclasses import dataclass
from py_clob_client.client import ClobClient as _ClobClient


@dataclass
class ClobCreds:
    api_key: str
    api_secret: str
    api_passphrase: str


def derive_clob_creds(
    private_key: str,
    host: str = "https://clob.polymarket.com",
    chain_id: int = 137,
) -> ClobCreds:
    """Derive CLOB API credentials deterministically from private key.

    Result is always the same for a given key — safe to cache in .env.
    Makes one HTTPS call to clob.polymarket.com; no on-chain transaction.
    """
    client = _ClobClient(host=host, key=private_key, chain_id=chain_id)
    raw = client.derive_api_key()
    return ClobCreds(
        api_key=raw.api_key,
        api_secret=raw.api_secret,
        api_passphrase=raw.api_passphrase,
    )
```

- [ ] **Step 4: Create `scripts/derive_creds.py` (run once to populate .env)**

```python
#!/usr/bin/env python3
# scripts/derive_creds.py
"""Run once to derive CLOB API credentials from your private key.
Paste the output into your .env file.

Usage: python scripts/derive_creds.py
"""
from polywin.config import Settings
from polywin.clients.creds import derive_clob_creds

settings = Settings()
creds = derive_clob_creds(settings.private_key, settings.clob_host, settings.chain_id)

print("Add these to your .env:")
print(f"CLOB_API_KEY={creds.api_key}")
print(f"CLOB_API_SECRET={creds.api_secret}")
print(f"CLOB_API_PASSPHRASE={creds.api_passphrase}")
```

- [ ] **Step 5: Run tests to confirm they pass**

```bash
pytest tests/clients/test_creds.py -v
```

Expected: `2 passed`

- [ ] **Step 6: Commit**

```bash
git add src/polywin/clients/creds.py tests/clients/test_creds.py scripts/derive_creds.py
git commit -m "feat: add deterministic CLOB credential derivation from private key"
```

---

## Task 7: CLOB Client

**Files:**
- Create: `src/polywin/clients/clob.py`
- Create: `tests/clients/test_clob.py`

Wraps `py-clob-client` for orderbook reads and limit order execution. Accepts `ClobCreds` from Task 6.

- [ ] **Step 1: Write the failing tests**

```python
# tests/clients/test_clob.py
from unittest.mock import MagicMock, patch
import pytest
from polywin.clients.clob import ClobClient, Orderbook, OrderbookLevel
from polywin.clients.creds import ClobCreds


FAKE_CREDS = ClobCreds(api_key="key", api_secret="secret", api_passphrase="pass")
FAKE_KEY = "0x" + "a" * 64


def _make_client(mock_inner):
    with patch("polywin.clients.clob._ClobClient", return_value=mock_inner):
        return ClobClient(private_key=FAKE_KEY, creds=FAKE_CREDS)


def test_get_orderbook_returns_bids_and_asks():
    mock_inner = MagicMock()
    mock_inner.get_order_book.return_value = MagicMock(
        bids=[{"price": "0.64", "size": "100"}, {"price": "0.63", "size": "200"}],
        asks=[{"price": "0.66", "size": "150"}],
    )
    ob = _make_client(mock_inner).get_orderbook("token-abc")

    assert isinstance(ob, Orderbook)
    assert ob.token_id == "token-abc"
    assert ob.bids[0] == OrderbookLevel(price=0.64, size=100.0)
    assert ob.asks[0] == OrderbookLevel(price=0.66, size=150.0)


def test_get_orderbook_empty_book():
    mock_inner = MagicMock()
    mock_inner.get_order_book.return_value = MagicMock(bids=None, asks=None)
    ob = _make_client(mock_inner).get_orderbook("token-xyz")
    assert ob.bids == []
    assert ob.asks == []


def test_get_open_orders_delegates():
    mock_inner = MagicMock()
    mock_inner.get_orders.return_value = [{"id": "order-1"}]
    orders = _make_client(mock_inner).get_open_orders()
    assert orders == [{"id": "order-1"}]


def test_cancel_order_delegates():
    mock_inner = MagicMock()
    mock_inner.cancel.return_value = {"success": True}
    result = _make_client(mock_inner).cancel_order("order-999")
    assert result == {"success": True}
    mock_inner.cancel.assert_called_once_with("order-999")


def test_place_limit_order_buy():
    mock_inner = MagicMock()
    mock_inner.create_and_post_order.return_value = {"orderID": "new-order-1"}
    result = _make_client(mock_inner).place_limit_order(
        token_id="token-abc", side="BUY", price=0.64, size_usdc=50.0
    )
    assert result["orderID"] == "new-order-1"
    mock_inner.create_and_post_order.assert_called_once()


def test_place_limit_order_rejects_invalid_side():
    mock_inner = MagicMock()
    with pytest.raises(ValueError, match="side must be BUY or SELL"):
        _make_client(mock_inner).place_limit_order(
            token_id="token-abc", side="HOLD", price=0.64, size_usdc=50.0
        )
```

- [ ] **Step 2: Run tests to confirm they fail**

```bash
pytest tests/clients/test_clob.py -v
```

Expected: `ERROR` — `ModuleNotFoundError: No module named 'polywin.clients.clob'`

- [ ] **Step 3: Implement `src/polywin/clients/clob.py`**

```python
from dataclasses import dataclass
from py_clob_client.client import ClobClient as _ClobClient
from py_clob_client.clob_types import ApiCreds, OrderArgs
from polywin.clients.creds import ClobCreds


@dataclass
class OrderbookLevel:
    price: float
    size: float


@dataclass
class Orderbook:
    token_id: str
    bids: list[OrderbookLevel]
    asks: list[OrderbookLevel]


class ClobClient:
    def __init__(
        self,
        private_key: str,
        creds: ClobCreds,
        host: str = "https://clob.polymarket.com",
        chain_id: int = 137,
    ):
        self._client = _ClobClient(
            host=host,
            key=private_key,
            chain_id=chain_id,
            creds=ApiCreds(
                api_key=creds.api_key,
                api_secret=creds.api_secret,
                api_passphrase=creds.api_passphrase,
            ),
        )

    def get_orderbook(self, token_id: str) -> Orderbook:
        resp = self._client.get_order_book(token_id)
        bids = [OrderbookLevel(float(b["price"]), float(b["size"])) for b in (resp.bids or [])]
        asks = [OrderbookLevel(float(a["price"]), float(a["size"])) for a in (resp.asks or [])]
        return Orderbook(token_id=token_id, bids=bids, asks=asks)

    def place_limit_order(
        self, token_id: str, side: str, price: float, size_usdc: float
    ) -> dict:
        if side not in ("BUY", "SELL"):
            raise ValueError("side must be BUY or SELL")
        return self._client.create_and_post_order(
            OrderArgs(token_id=token_id, price=price, size=size_usdc, side=side)
        )

    def get_open_orders(self) -> list[dict]:
        return self._client.get_orders()

    def cancel_order(self, order_id: str) -> dict:
        return self._client.cancel(order_id)
```

- [ ] **Step 4: Run tests to confirm they pass**

```bash
pytest tests/clients/test_clob.py -v
```

Expected: `6 passed`

- [ ] **Step 5: Commit**

```bash
git add src/polywin/clients/clob.py tests/clients/test_clob.py
git commit -m "feat: add CLOB client with limit order placement and ClobCreds integration"
```

---

## Task 8: Wallet Manager

**Files:**
- Create: `src/polywin/wallet.py`
- Create: `tests/test_wallet.py`

- [ ] **Step 1: Write the failing tests**

```python
# tests/test_wallet.py
import pytest
from polywin.wallet import Wallet


# Hardhat/Foundry default account 0 — well-known dev key, never use for real funds
TEST_KEY = "0xac0974bec39a17e36ba4a6b4d238ff944bacb478cbed5efcae784d7bf4f2ff80"
TEST_ADDRESS = "0xf39Fd6e51aad88F6F4ce6aB8827279cffFb92266"


def test_wallet_derives_correct_address():
    assert Wallet.from_private_key(TEST_KEY).address == TEST_ADDRESS


def test_wallet_address_is_checksummed():
    addr = Wallet.from_private_key(TEST_KEY).address
    assert addr != addr.lower()
    assert addr != addr.upper()


def test_wallet_signs_message_returns_hex():
    sig = Wallet.from_private_key(TEST_KEY).sign_message("hello polywin")
    assert sig.startswith("0x")
    assert len(sig) == 132  # 65 bytes = 130 hex + "0x"


def test_wallet_sign_is_deterministic():
    w = Wallet.from_private_key(TEST_KEY)
    assert w.sign_message("test") == w.sign_message("test")


def test_wallet_raises_on_invalid_key():
    with pytest.raises(Exception):
        Wallet.from_private_key("not-a-key")
```

- [ ] **Step 2: Run tests to confirm they fail**

```bash
pytest tests/test_wallet.py -v
```

Expected: `ERROR` — `ModuleNotFoundError: No module named 'polywin.wallet'`

- [ ] **Step 3: Implement `src/polywin/wallet.py`**

```python
from eth_account import Account
from eth_account.messages import encode_defunct


class Wallet:
    def __init__(self, private_key: str):
        self._account = Account.from_key(private_key)

    @property
    def address(self) -> str:
        return self._account.address

    def sign_message(self, message: str) -> str:
        signed = self._account.sign_message(encode_defunct(text=message))
        return "0x" + signed.signature.hex()

    @classmethod
    def from_private_key(cls, private_key: str) -> "Wallet":
        return cls(private_key)
```

- [ ] **Step 4: Run tests to confirm they pass**

```bash
pytest tests/test_wallet.py -v
```

Expected: `5 passed`

- [ ] **Step 5: Commit**

```bash
git add src/polywin/wallet.py tests/test_wallet.py
git commit -m "feat: add Wallet manager with deterministic EIP-191 signing"
```

---

## Task 9: WebSocket Market Feed

**Files:**
- Create: `src/polywin/clients/ws.py`
- Create: `tests/clients/test_ws.py`

The WebSocket feed at `wss://ws-subscriptions-clob.polymarket.com/ws/market` delivers real-time trade events for any set of CLOB token IDs. This is the lowest-latency data source — critical for copy trading and arbitrage. The client auto-reconnects on disconnect (pattern proven in poly-maker).

- [ ] **Step 1: Write the failing tests**

```python
# tests/clients/test_ws.py
import json
import pytest
from unittest.mock import AsyncMock, MagicMock, patch, call
from polywin.clients.ws import MarketFeed, PriceUpdate


RAW_UPDATE = {
    "asset_id": "0xTOKEN_YES",
    "market": "0xabc123",
    "price": "0.66",
    "side": "BUY",
    "size": "75.5",
    "timestamp": "1717200000",
}


def test_parse_price_update():
    feed = MarketFeed(token_ids=["0xTOKEN_YES"])
    update = feed._parse(RAW_UPDATE)

    assert isinstance(update, PriceUpdate)
    assert update.token_id == "0xTOKEN_YES"
    assert update.market_id == "0xabc123"
    assert update.price == 0.66
    assert update.side == "BUY"
    assert update.size == 75.5
    assert update.timestamp == 1717200000


def test_parse_price_update_missing_fields():
    feed = MarketFeed(token_ids=[])
    update = feed._parse({})
    assert update.token_id == ""
    assert update.price == 0.0
    assert update.size == 0.0


async def test_stream_yields_parsed_updates():
    messages = [json.dumps([RAW_UPDATE]), json.dumps(RAW_UPDATE)]

    mock_ws = AsyncMock()
    mock_ws.__aiter__ = MagicMock(return_value=iter(messages))
    mock_ws.__aenter__ = AsyncMock(return_value=mock_ws)
    mock_ws.__aexit__ = AsyncMock(return_value=False)

    with patch("polywin.clients.ws.websockets.connect", return_value=mock_ws):
        feed = MarketFeed(token_ids=["0xTOKEN_YES"])
        updates = []
        async for update in feed.stream():
            updates.append(update)
            if len(updates) >= 2:
                break

    assert len(updates) == 2
    assert all(isinstance(u, PriceUpdate) for u in updates)
    assert updates[0].token_id == "0xTOKEN_YES"


async def test_stream_sends_subscription_message():
    messages = []

    mock_ws = AsyncMock()
    mock_ws.__aiter__ = MagicMock(return_value=iter(messages))
    mock_ws.__aenter__ = AsyncMock(return_value=mock_ws)
    mock_ws.__aexit__ = AsyncMock(return_value=False)

    with patch("polywin.clients.ws.websockets.connect", return_value=mock_ws):
        feed = MarketFeed(token_ids=["tok-1", "tok-2"])
        async for _ in feed.stream():
            break

    sent = json.loads(mock_ws.send.call_args[0][0])
    assert sent["assets_ids"] == ["tok-1", "tok-2"]
    assert sent["type"] == "market"
```

- [ ] **Step 2: Run tests to confirm they fail**

```bash
pytest tests/clients/test_ws.py -v
```

Expected: `ERROR` — `ModuleNotFoundError: No module named 'polywin.clients.ws'`

- [ ] **Step 3: Implement `src/polywin/clients/ws.py`**

```python
import asyncio
import json
from collections.abc import AsyncIterator
from dataclasses import dataclass
import websockets
from websockets.exceptions import ConnectionClosed

from polywin.logger import get_logger

WS_MARKET_URL = "wss://ws-subscriptions-clob.polymarket.com/ws/market"

log = get_logger("polywin.ws")


@dataclass
class PriceUpdate:
    token_id: str
    market_id: str
    price: float
    side: str        # "BUY" | "SELL"
    size: float
    timestamp: int


class MarketFeed:
    def __init__(self, token_ids: list[str], reconnect_delay: float = 5.0):
        self.token_ids = token_ids
        self._reconnect_delay = reconnect_delay

    async def stream(self) -> AsyncIterator[PriceUpdate]:
        """Yield PriceUpdates forever, reconnecting silently on disconnect."""
        while True:
            try:
                async with websockets.connect(
                    WS_MARKET_URL,
                    ping_interval=5,
                    ping_timeout=10,
                ) as ws:
                    await ws.send(json.dumps({
                        "assets_ids": self.token_ids,
                        "type": "market",
                    }))
                    async for raw in ws:
                        payload = json.loads(raw)
                        if isinstance(payload, list):
                            for item in payload:
                                yield self._parse(item)
                        elif isinstance(payload, dict):
                            yield self._parse(payload)
            except (ConnectionClosed, OSError, Exception) as exc:
                log.warning(f"WS disconnected ({exc}), reconnecting in {self._reconnect_delay}s")
                await asyncio.sleep(self._reconnect_delay)

    def _parse(self, data: dict) -> PriceUpdate:
        return PriceUpdate(
            token_id=data.get("asset_id", ""),
            market_id=data.get("market", ""),
            price=float(data.get("price", 0)),
            side=data.get("side", ""),
            size=float(data.get("size", 0)),
            timestamp=int(data.get("timestamp", 0)),
        )
```

- [ ] **Step 4: Run tests to confirm they pass**

```bash
pytest tests/clients/test_ws.py -v
```

Expected: `4 passed`

- [ ] **Step 5: Commit**

```bash
git add src/polywin/clients/ws.py tests/clients/test_ws.py
git commit -m "feat: add async WebSocket market feed with auto-reconnect"
```

---

## Task 10: Full Suite + Smoke Script

**Files:**
- Create: `scripts/smoke_test.py`

- [ ] **Step 1: Run the full test suite**

```bash
pytest --cov=polywin --cov-report=term-missing
```

Expected: all tests pass, coverage ≥ 80%

- [ ] **Step 2: Create `scripts/smoke_test.py`**

Hits live APIs in read-only mode. Requires a populated `.env`.

```python
#!/usr/bin/env python3
# scripts/smoke_test.py
"""
Read-only smoke test against live Polymarket APIs.
Requires .env with PRIVATE_KEY. Run derive_creds.py first if CLOB creds are absent.
Usage: python scripts/smoke_test.py
"""
import asyncio
from polywin.config import Settings
from polywin.clients.creds import derive_clob_creds
from polywin.clients.gamma import GammaClient
from polywin.clients.data_api import DataApiClient
from polywin.clients.ws import MarketFeed
from polywin.wallet import Wallet
from polywin.logger import get_logger

log = get_logger("smoke")


async def main():
    settings = Settings()
    wallet = Wallet.from_private_key(settings.private_key)
    log.info(f"Wallet: {wallet.address}")

    # Derive CLOB creds if not in .env
    if not settings.clob_api_key:
        log.info("Deriving CLOB creds from private key...")
        creds = derive_clob_creds(settings.private_key, settings.clob_host, settings.chain_id)
        log.info(f"  api_key={creds.api_key[:8]}...")
    else:
        log.info(f"Using cached CLOB creds: {settings.clob_api_key[:8]}...")

    log.info("--- Tradeable markets (vol > $10k, price 5-95%) ---")
    with GammaClient(settings.gamma_api_url) as gamma:
        markets = gamma.get_tradeable_markets(min_volume=10_000, limit=5)
        for m in markets:
            log.info(
                f"  {m.question[:55]:55s}  "
                f"prices={[f'{p:.2f}' for p in m.outcome_prices]}  "
                f"vol=${m.volume:,.0f}  tokens={len(m.token_ids)}"
            )

    log.info("--- Top traders (win rate ≥ 70%) ---")
    with DataApiClient(settings.data_api_url) as data:
        traders = data.get_top_traders(min_win_rate=0.70, limit=20)
        for t in traders[:5]:
            log.info(f"  {t.name or t.address[:10]:15s}  pnl=${t.pnl:>10,.0f}  wr={t.win_rate:.0%}")

    if markets and markets[0].token_ids:
        token_id = markets[0].token_ids[0]
        log.info(f"--- WebSocket: first 3 updates for token {token_id[:12]}... ---")
        feed = MarketFeed(token_ids=[token_id])
        count = 0
        async for update in feed.stream():
            log.info(f"  price={update.price:.3f}  side={update.side}  size={update.size}")
            count += 1
            if count >= 3:
                break

    log.info("Smoke test complete.")


if __name__ == "__main__":
    asyncio.run(main())
```

- [ ] **Step 3: Run smoke test**

```bash
# Populate .env first:
#   cp .env.example .env
#   # add your PRIVATE_KEY
#   python scripts/derive_creds.py   # paste output into .env
python scripts/smoke_test.py
```

Expected: markets listed, top traders listed, 3 live price updates from WebSocket printed.

- [ ] **Step 4: Commit**

```bash
git add scripts/smoke_test.py
git commit -m "chore: add live smoke test covering Gamma, Data API, CLOB creds, and WebSocket"
```

---

## Self-Review Checklist

**Spec coverage:**
- [x] Python backend — `pyproject.toml` (Task 1)
- [x] Configuration, no hardcoded secrets — Task 2
- [x] Logging — Task 3
- [x] Gamma API client — Task 4
- [x] Data API client + top-trader filter — Task 5
- [x] CLOB API credential derivation — Task 6
- [x] CLOB client with limit orders — Task 7
- [x] Wallet / key management — Task 8
- [x] Real-time WebSocket feed — Task 9
- [ ] Polygon RPC / on-chain balance reads — deferred to Plan 4 (Risk Management)
- [ ] Position merging (YES+NO → USDC) — deferred to Plan 4

**Type consistency:**
- `GammaClient.get_tradeable_markets()` → `list[Market]` ✓ — `Market.token_ids` used in `MarketFeed` constructor ✓
- `DataApiClient.get_top_traders()` → `list[LeaderboardEntry]` ✓ — used in Plan 2 copy trading
- `derive_clob_creds()` → `ClobCreds` ✓ — accepted by `ClobClient.__init__(creds=)` ✓
- `ClobClient.place_limit_order()` → `dict` ✓
- `MarketFeed.stream()` → `AsyncIterator[PriceUpdate]` ✓
- `Wallet.from_private_key()` → `Wallet` ✓

**Profitability improvements included:**
- `get_tradeable_markets(min_volume, price_guard)` — avoids illiquid and near-resolved markets
- `get_top_traders(min_win_rate)` — primary signal source for Plan 2 copy trading
- `token_ids` on `Market` — enables direct WebSocket subscription without a secondary lookup
- WebSocket auto-reconnect — prevents blind spots in live data during strategy execution

**No placeholders:** All steps have actual code and exact commands. ✓
