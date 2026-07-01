from __future__ import annotations
import sqlite3
import time
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
        self, token_id: str, side: str, price: float, size_usdc: float, market_id: str = ""
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


class PaperClobClient:
    """Simulates ClobClient locally. Writes paper trades to DB via stdlib sqlite3."""

    def __init__(self, db_path: str = "data/polywin.db"):
        self._db_path = db_path

    def get_orderbook(self, token_id: str) -> Orderbook:
        return Orderbook(token_id=token_id, bids=[], asks=[])

    def place_limit_order(
        self, token_id: str, side: str, price: float, size_usdc: float, market_id: str = ""
    ) -> dict:
        if side not in ("BUY", "SELL"):
            raise ValueError("side must be BUY or SELL")
        order_id = f"paper-{int(time.time() * 1000)}"
        with sqlite3.connect(self._db_path) as conn:
            conn.execute(
                "INSERT INTO trades (ts,market_id,token_id,side,price,size_usdc,order_id,paper) "
                "VALUES (?,?,?,?,?,?,?,?)",
                (int(time.time()), market_id, token_id, side, price, size_usdc, order_id, 1),
            )
        return {"orderID": order_id, "paper": True}

    def get_open_orders(self) -> list[dict]:
        return []

    def cancel_order(self, order_id: str) -> dict:
        return {"success": True}
