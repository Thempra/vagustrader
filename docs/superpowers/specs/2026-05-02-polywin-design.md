# Polywin — System Design

**Date:** 2026-05-02  
**Status:** Approved

## Goal

A production-grade Python trading service for Polymarket. Runs as a long-lived daemon on a VPS, operated via CLI, with pluggable strategies, SQLite persistence, and a paper-trading mode for safe validation.

---

## Architecture

```
┌─────────────────────────────────────────────────┐
│                  polywin CLI                    │
│   run | run --paper | status | markets | ...    │
└────────────────────┬────────────────────────────┘
                     │
┌────────────────────▼────────────────────────────┐
│                 Engine (asyncio)                │
│  - loads strategies from config.yml             │
│  - schedules: market scan, WS feed, strategies  │
│  - intercepts orders in paper mode              │
└──┬──────────────┬──────────────┬────────────────┘
   │              │              │
┌──▼──┐      ┌───▼───┐    ┌─────▼──────┐
│ DB  │      │Clients│    │ Strategies │
│SQLi-│      │Gamma  │    │ CopyTrading│
│ te  │      │DataAPI│    │ (ABC base) │
│     │      │CLOB   │    └────────────┘
│     │      │WS feed│
└─────┘      │Creds  │
             └───────┘
```

---

## Components

### CLI (`src/polywin/cli.py`)
Entry point installed as `polywin` command via `pyproject.toml`. Built with `typer`.

Commands:
- `polywin run [--paper]` — start the trading engine
- `polywin status` — open positions + today's P&L from DB
- `polywin markets` — list tradeable markets right now
- `polywin top-traders [--min-wr 0.70]` — leaderboard filter
- `polywin derive-creds` — derive + print CLOB API keys from private key

### Engine (`src/polywin/engine.py`)
Async orchestrator. On startup: derives CLOB creds if absent, loads enabled strategies from `config.yml`, starts the WebSocket market feed, and runs each strategy as a concurrent asyncio task. In paper mode, wraps the CLOB client with `PaperClobClient` which logs orders to DB without submitting them.

### Configuration (`src/polywin/config.py`)
Two sources merged at startup:
- `.env` — secrets only: `PRIVATE_KEY`, optionally `CLOB_API_KEY/SECRET/PASSPHRASE`
- `config.yml` — all non-secret parameters: strategy settings, thresholds, risk limits

`Settings` (pydantic-settings) handles `.env`. `AppConfig` (pydantic BaseModel) handles `config.yml`. Both available as singletons after `load_config()`.

### Database (`src/polywin/db.py`)
SQLite via `aiosqlite`. Three tables:

```sql
trades (
    id INTEGER PRIMARY KEY,
    ts INTEGER NOT NULL,          -- unix timestamp
    market_id TEXT NOT NULL,
    token_id TEXT NOT NULL,
    side TEXT NOT NULL,           -- BUY | SELL
    price REAL NOT NULL,
    size_usdc REAL NOT NULL,
    order_id TEXT,                -- null if paper
    paper INTEGER NOT NULL        -- 0 | 1
)

positions (
    token_id TEXT PRIMARY KEY,
    market_id TEXT NOT NULL,
    outcome TEXT NOT NULL,
    size REAL NOT NULL,
    avg_price REAL NOT NULL,
    updated_ts INTEGER NOT NULL
)

pnl_snapshots (
    date TEXT PRIMARY KEY,        -- YYYY-MM-DD
    realized REAL NOT NULL,
    unrealized REAL NOT NULL,
    total_trades INTEGER NOT NULL
)
```

### Logger (`src/polywin/logger.py`)
Structured JSON output to stdout + rotating file (`logs/polywin.log`, 10 MB, 5 backups). Each log line includes `ts`, `level`, `logger`, `msg`, and any extra fields passed as kwargs.

### Clients (`src/polywin/clients/`)
Unchanged from Plan 1 design: `gamma.py`, `data_api.py`, `clob.py`, `creds.py`, `ws.py`.

`PaperClobClient` wraps `ClobClient` with the same interface — `place_limit_order` writes to DB and returns a fake order ID instead of hitting the network.

### Strategies (`src/polywin/strategies/`)

**`base.py`** — Abstract base class:
```python
class Strategy(ABC):
    name: str
    @abstractmethod
    async def run(self, ctx: StrategyContext) -> None: ...
```

`StrategyContext` carries: `clob`, `gamma`, `data_api`, `db`, `config`, `logger`.

**`copy_trading.py`** — First live strategy:
- Polls `data_api.get_top_traders()` every N minutes (configurable)
- Fetches positions of each top trader
- For any position not yet held locally and above min_edge threshold: places a limit order at best ask
- Position sizing: configurable % of bankroll, capped at max_position_usdc

### Deployment
`Dockerfile` — single-stage Python 3.11 slim image.  
`docker-compose.yml` — mounts `.env` and `config.yml` as volumes, maps `logs/` and `data/` for persistence.  
`Makefile` — `make run`, `make paper`, `make test`, `make shell`, `make logs`.

---

## Paper Trading Mode

`polywin run --paper` passes `paper=True` to the engine. The engine swaps `ClobClient` → `PaperClobClient`. All strategy logic runs unchanged. Orders land in the DB with `paper=1`. `polywin status` shows paper vs. live P&L separately.

---

## config.yml Structure

```yaml
risk:
  max_position_usdc: 50.0       # max per trade
  max_open_positions: 10
  daily_loss_limit_usdc: 200.0

strategies:
  copy_trading:
    enabled: true
    poll_interval_seconds: 300
    min_win_rate: 0.70
    min_volume_usd: 10000
    price_guard: 0.05            # ignore markets priced < 5% or > 95%
    max_copy_per_trader: 3       # max concurrent positions per followed wallet

markets:
  min_liquidity_usdc: 5000
```

---

## Testing Strategy

- Unit tests mock HTTP (pytest-httpx) and DB (in-memory SQLite)
- `PaperClobClient` is the integration test harness for strategies — no real orders
- One smoke script (`scripts/smoke_test.py`) hits live read-only APIs

---

## Self-Review

- No TBD or placeholders ✓
- Architecture matches feature descriptions ✓  
- Scope is focused: one service, one CLI, one DB ✓
- Paper mode clearly specified ✓
- config.yml schema fully defined ✓
- Strategy interface defined ✓
