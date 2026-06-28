import json
from dataclasses import dataclass
import httpx


@dataclass
class Market:
    id: str
    question: str
    outcomes: list[str]
    outcome_prices: list[float]
    token_ids: list[str]
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
            "/markets", params={"limit": limit, "offset": offset, "active": str(active).lower()}
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
        self, min_volume: float = 10_000, price_guard: float = 0.05, limit: int = 100
    ) -> list[Market]:
        return [
            m for m in self.get_markets(limit=limit)
            if m.volume >= min_volume
            and m.outcome_prices
            and all(price_guard < p < (1 - price_guard) for p in m.outcome_prices)
        ]

    def _parse(self, d: dict) -> Market:
        def _loads(v: object) -> list:
            return json.loads(v) if isinstance(v, str) else (v or [])

        prices = _loads(d.get("outcomePrices", []))
        clob = _loads(d.get("clobTokenIds", []))
        return Market(
            id=d["id"],
            question=d["question"],
            outcomes=_loads(d.get("outcomes", [])),
            outcome_prices=[float(p) for p in prices],
            token_ids=[t["token_id"] for t in clob if isinstance(t, dict)],
            volume=float(d.get("volume", 0)),
            liquidity=float(d.get("liquidity", 0)),
            active=bool(d.get("active", False)),
            end_date=d.get("endDate", ""),
            condition_id=d.get("conditionId", d["id"]),
        )

    def close(self) -> None:
        self._client.close()

    def __enter__(self) -> "GammaClient":
        return self

    def __exit__(self, *_) -> None:
        self.close()
