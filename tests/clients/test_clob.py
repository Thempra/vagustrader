# tests/clients/test_clob.py
import pytest
from unittest.mock import MagicMock, patch
from polywin.clients.clob import ClobClient, PaperClobClient, Orderbook, OrderbookLevel
from polywin.clients.creds import ClobCreds

FAKE_KEY = "0x" + "a" * 64
FAKE_CREDS = ClobCreds(api_key="k", api_secret="s", api_passphrase="p")


def _make_clob(mock_inner):
    with patch("polywin.clients.clob._ClobClient", return_value=mock_inner):
        return ClobClient(private_key=FAKE_KEY, creds=FAKE_CREDS)


def test_get_orderbook_parses_levels():
    mock = MagicMock()
    mock.get_order_book.return_value = MagicMock(
        bids=[{"price": "0.64", "size": "100"}],
        asks=[{"price": "0.66", "size": "200"}],
    )
    ob = _make_clob(mock).get_orderbook("tok-1")
    assert isinstance(ob, Orderbook)
    assert ob.bids[0] == OrderbookLevel(price=0.64, size=100.0)
    assert ob.asks[0] == OrderbookLevel(price=0.66, size=200.0)


def test_get_orderbook_empty():
    mock = MagicMock()
    mock.get_order_book.return_value = MagicMock(bids=None, asks=None)
    ob = _make_clob(mock).get_orderbook("tok-x")
    assert ob.bids == [] and ob.asks == []


def test_place_limit_order_validates_side():
    mock = MagicMock()
    with pytest.raises(ValueError, match="side must be BUY or SELL"):
        _make_clob(mock).place_limit_order("tok", "HOLD", 0.5, 10.0)


def test_place_limit_order_delegates():
    mock = MagicMock()
    mock.create_and_post_order.return_value = {"orderID": "ord-1"}
    result = _make_clob(mock).place_limit_order("tok", "BUY", 0.65, 50.0)
    assert result["orderID"] == "ord-1"


def test_paper_clob_returns_fake_order_id(tmp_path):
    import asyncio
    from polywin.db import init_db
    db_path = str(tmp_path / "test.db")
    asyncio.run(init_db(db_path))
    paper = PaperClobClient(db_path=db_path)
    result = paper.place_limit_order("tok", "BUY", 0.65, 50.0)
    assert "orderID" in result
    assert result["orderID"].startswith("paper-")


def test_paper_clob_writes_to_db(tmp_path):
    import asyncio
    from polywin.db import init_db, get_today_trades
    db_path = str(tmp_path / "test.db")
    asyncio.run(init_db(db_path))
    PaperClobClient(db_path=db_path).place_limit_order("tok", "BUY", 0.65, 50.0)
    trades = asyncio.run(get_today_trades("2000-01-01", path=db_path))
    assert len(trades) == 1
    assert trades[0]["paper"] == 1
    assert trades[0]["price"] == 0.65
