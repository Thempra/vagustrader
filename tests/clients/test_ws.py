# tests/clients/test_ws.py
import json
from unittest.mock import AsyncMock, MagicMock, patch
from polywin.clients.ws import MarketFeed, PriceUpdate

RAW = {"asset_id": "0xTOK", "market": "0xMKT", "price": "0.66",
       "side": "BUY", "size": "75.5", "timestamp": "1717200000"}


def test_parse_update():
    u = MarketFeed(["0xTOK"])._parse(RAW)
    assert isinstance(u, PriceUpdate)
    assert u.token_id == "0xTOK"
    assert u.price == 0.66
    assert u.size == 75.5
    assert u.timestamp == 1717200000


def test_parse_missing_fields():
    u = MarketFeed([])._parse({})
    assert u.token_id == "" and u.price == 0.0


async def test_stream_yields_updates():
    messages = [json.dumps([RAW]), json.dumps(RAW)]

    async def async_iterator():
        for msg in messages:
            yield msg

    mock_ws = AsyncMock()
    mock_ws.__aiter__ = MagicMock(return_value=async_iterator())
    mock_ws.__aenter__ = AsyncMock(return_value=mock_ws)
    mock_ws.__aexit__ = AsyncMock(return_value=False)

    with patch("polywin.clients.ws.websockets.connect", return_value=mock_ws):
        feed = MarketFeed(["0xTOK"])
        updates = []
        async for u in feed.stream():
            updates.append(u)
            if len(updates) >= 2:
                break

    assert len(updates) == 2
    assert all(isinstance(u, PriceUpdate) for u in updates)


async def test_stream_sends_subscription():
    from websockets.exceptions import ConnectionClosed

    class AsyncIteratorMock:
        def __aiter__(self):
            return self

        async def __anext__(self):
            raise ConnectionClosed(None, None)

    mock_ws = AsyncMock()
    mock_ws.__aiter__ = lambda: AsyncIteratorMock()
    mock_ws.__aenter__ = AsyncMock(return_value=mock_ws)
    mock_ws.__aexit__ = AsyncMock(return_value=False)

    with patch("polywin.clients.ws.websockets.connect", return_value=mock_ws):
        try:
            async for _ in MarketFeed(["tok-1", "tok-2"]).stream():
                break
        except Exception:
            pass

    sent = json.loads(mock_ws.send.call_args[0][0])
    assert sent["assets_ids"] == ["tok-1", "tok-2"]
    assert sent["type"] == "market"
