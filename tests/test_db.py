import pytest
import time
from polywin.db import init_db, record_trade, upsert_position, get_positions, get_today_trades


@pytest.fixture
async def db_path(tmp_path):
    path = str(tmp_path / "test.db")
    await init_db(path)
    return path


async def test_init_creates_tables(db_path):
    import aiosqlite
    async with aiosqlite.connect(db_path) as db:
        cur = await db.execute("SELECT name FROM sqlite_master WHERE type='table'")
        tables = {row[0] for row in await cur.fetchall()}
    assert {"trades", "positions", "pnl_snapshots"} <= tables


async def test_record_trade_inserts_row(db_path):
    ts = int(time.time())
    row_id = await record_trade(
        ts=ts, market_id="mkt-1", token_id="tok-1",
        side="BUY", price=0.65, size_usdc=50.0,
        order_id="order-abc", paper=False, path=db_path,
    )
    assert row_id == 1
    trades = await get_today_trades("2000-01-01", path=db_path)
    assert len(trades) == 1
    assert trades[0]["side"] == "BUY"
    assert trades[0]["paper"] == 0


async def test_record_trade_paper_flag(db_path):
    await record_trade(
        ts=int(time.time()), market_id="m", token_id="t",
        side="BUY", price=0.5, size_usdc=10.0,
        order_id=None, paper=True, path=db_path,
    )
    trades = await get_today_trades("2000-01-01", path=db_path)
    assert trades[0]["paper"] == 1
    assert trades[0]["order_id"] is None


async def test_upsert_position_creates_and_updates(db_path):
    ts = int(time.time())
    await upsert_position(
        token_id="tok-1", market_id="mkt-1", outcome="Yes",
        size=100.0, avg_price=0.65, updated_ts=ts, path=db_path,
    )
    positions = await get_positions(path=db_path)
    assert len(positions) == 1
    assert positions[0]["avg_price"] == 0.65

    await upsert_position(
        token_id="tok-1", market_id="mkt-1", outcome="Yes",
        size=200.0, avg_price=0.70, updated_ts=ts + 1, path=db_path,
    )
    positions = await get_positions(path=db_path)
    assert len(positions) == 1
    assert positions[0]["size"] == 200.0
    assert positions[0]["avg_price"] == 0.70
