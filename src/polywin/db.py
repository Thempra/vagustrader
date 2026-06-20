from __future__ import annotations
from pathlib import Path
import aiosqlite

_SCHEMA = """
CREATE TABLE IF NOT EXISTS trades (
    id        INTEGER PRIMARY KEY AUTOINCREMENT,
    ts        INTEGER NOT NULL,
    market_id TEXT NOT NULL,
    token_id  TEXT NOT NULL,
    side      TEXT NOT NULL,
    price     REAL NOT NULL,
    size_usdc REAL NOT NULL,
    order_id  TEXT,
    paper     INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS positions (
    token_id   TEXT PRIMARY KEY,
    market_id  TEXT NOT NULL,
    outcome    TEXT NOT NULL,
    size       REAL NOT NULL,
    avg_price  REAL NOT NULL,
    updated_ts INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS pnl_snapshots (
    date         TEXT PRIMARY KEY,
    realized     REAL NOT NULL,
    unrealized   REAL NOT NULL,
    total_trades INTEGER NOT NULL
);
"""


async def init_db(path: str = "data/polywin.db") -> None:
    Path(path).parent.mkdir(parents=True, exist_ok=True)
    async with aiosqlite.connect(path) as db:
        await db.executescript(_SCHEMA)
        await db.commit()


async def record_trade(
    *,
    ts: int,
    market_id: str,
    token_id: str,
    side: str,
    price: float,
    size_usdc: float,
    order_id: str | None,
    paper: bool,
    path: str = "data/polywin.db",
) -> int:
    async with aiosqlite.connect(path) as db:
        cur = await db.execute(
            "INSERT INTO trades (ts,market_id,token_id,side,price,size_usdc,order_id,paper) "
            "VALUES (?,?,?,?,?,?,?,?)",
            (ts, market_id, token_id, side, price, size_usdc, order_id, int(paper)),
        )
        await db.commit()
        return cur.lastrowid  # type: ignore[return-value]


async def upsert_position(
    *,
    token_id: str,
    market_id: str,
    outcome: str,
    size: float,
    avg_price: float,
    updated_ts: int,
    path: str = "data/polywin.db",
) -> None:
    async with aiosqlite.connect(path) as db:
        await db.execute(
            "INSERT INTO positions (token_id,market_id,outcome,size,avg_price,updated_ts) "
            "VALUES (?,?,?,?,?,?) "
            "ON CONFLICT(token_id) DO UPDATE SET "
            "size=excluded.size, avg_price=excluded.avg_price, updated_ts=excluded.updated_ts",
            (token_id, market_id, outcome, size, avg_price, updated_ts),
        )
        await db.commit()


async def get_positions(path: str = "data/polywin.db") -> list[dict]:
    async with aiosqlite.connect(path) as db:
        db.row_factory = aiosqlite.Row
        cur = await db.execute("SELECT * FROM positions ORDER BY updated_ts DESC")
        return [dict(r) for r in await cur.fetchall()]


async def get_today_trades(date_str: str, path: str = "data/polywin.db") -> list[dict]:
    """Pass '2000-01-01' to get all trades ever (for tests)."""
    from datetime import datetime
    if date_str == "2000-01-01":
        start_ts, end_ts = 0, 9_999_999_999
    else:
        dt = datetime.strptime(date_str, "%Y-%m-%d")
        start_ts = int(dt.timestamp())
        end_ts = start_ts + 86_400
    async with aiosqlite.connect(path) as db:
        db.row_factory = aiosqlite.Row
        cur = await db.execute(
            "SELECT * FROM trades WHERE ts >= ? AND ts < ? ORDER BY ts DESC",
            (start_ts, end_ts),
        )
        return [dict(r) for r in await cur.fetchall()]


async def save_pnl_snapshot(
    *, date: str, realized: float, unrealized: float, total_trades: int,
    path: str = "data/polywin.db",
) -> None:
    async with aiosqlite.connect(path) as db:
        await db.execute(
            "INSERT INTO pnl_snapshots (date,realized,unrealized,total_trades) VALUES (?,?,?,?) "
            "ON CONFLICT(date) DO UPDATE SET "
            "realized=excluded.realized, unrealized=excluded.unrealized, total_trades=excluded.total_trades",
            (date, realized, unrealized, total_trades),
        )
        await db.commit()
