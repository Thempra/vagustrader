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
    token_id: str
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
        return [e for e in self.get_leaderboard(limit=limit) if e.win_rate >= min_win_rate]

    def get_positions(self, user_address: str) -> list[Position]:
        resp = self._client.get("/positions", params={"user": user_address})
        resp.raise_for_status()
        return [self._parse_position(p) for p in resp.json()]

    def get_activity(self, user_address: str, limit: int = 100) -> list[dict]:
        resp = self._client.get("/activity", params={"user": user_address, "limit": limit})
        resp.raise_for_status()
        return resp.json()

    def _parse_entry(self, d: dict) -> LeaderboardEntry:
        return LeaderboardEntry(
            address=d.get("address", ""),
            name=d.get("name", ""),
            pnl=float(d.get("pnl", 0)),
            volume=float(d.get("volume", 0)),
            win_rate=float(d.get("winRate", 0)),
        )

    def _parse_position(self, d: dict) -> Position:
        return Position(
            token_id=d.get("asset", d.get("tokenId", "")),
            market_id=d.get("conditionId", d.get("market", "")),
            outcome=d.get("outcome", ""),
            size=float(d.get("size", 0)),
            avg_price=float(d.get("avgPrice", 0)),
            current_value=float(d.get("currentValue", 0)),
        )

    def close(self) -> None:
        self._client.close()

    def __enter__(self) -> "DataApiClient":
        return self

    def __exit__(self, *_) -> None:
        self.close()
