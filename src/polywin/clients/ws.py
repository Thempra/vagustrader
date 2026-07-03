import asyncio
import json
import logging
from collections.abc import AsyncIterator
from dataclasses import dataclass
import websockets
from websockets.exceptions import ConnectionClosed

log = logging.getLogger("polywin.ws")
WS_MARKET_URL = "wss://ws-subscriptions-clob.polymarket.com/ws/market"


@dataclass
class PriceUpdate:
    token_id: str
    market_id: str
    price: float
    side: str
    size: float
    timestamp: int


class MarketFeed:
    def __init__(self, token_ids: list[str], reconnect_delay: float = 5.0):
        self.token_ids = token_ids
        self._reconnect_delay = reconnect_delay

    async def stream(self) -> AsyncIterator[PriceUpdate]:
        while True:
            try:
                async with websockets.connect(WS_MARKET_URL, ping_interval=5, ping_timeout=10) as ws:
                    await ws.send(json.dumps({"assets_ids": self.token_ids, "type": "market"}))
                    async for raw in ws:
                        payload = json.loads(raw)
                        items = payload if isinstance(payload, list) else [payload]
                        for item in items:
                            yield self._parse(item)
            except (ConnectionClosed, OSError) as exc:
                log.warning(f"WS disconnected ({exc}), retrying in {self._reconnect_delay}s")
                await asyncio.sleep(self._reconnect_delay)

    def _parse(self, d: dict) -> PriceUpdate:
        return PriceUpdate(
            token_id=d.get("asset_id", ""),
            market_id=d.get("market", ""),
            price=float(d.get("price", 0)),
            side=d.get("side", ""),
            size=float(d.get("size", 0)),
            timestamp=int(d.get("timestamp", 0)),
        )
