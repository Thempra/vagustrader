# tests/clients/test_data_api.py
from polywin.clients.data_api import DataApiClient, LeaderboardEntry, Position

LEADERBOARD = [
    {"address": "0xAAA", "name": "alpha", "pnl": 50000.0, "volume": 1e6, "winRate": 0.75},
    {"address": "0xBBB", "name": "beta",  "pnl": 10000.0, "volume": 2e5, "winRate": 0.60},
]
POSITIONS = [
    {"asset": "0xTOK", "conditionId": "0xMKT", "outcome": "Yes",
     "size": 200.0, "avgPrice": 0.60, "currentValue": 130.0},
]


def test_get_leaderboard(httpx_mock):
    httpx_mock.add_response(
        url="https://data-api.polymarket.com/leaderboard?limit=100&offset=0",
        json=LEADERBOARD,
    )
    entries = DataApiClient().get_leaderboard()
    assert len(entries) == 2
    assert isinstance(entries[0], LeaderboardEntry)
    assert entries[0].win_rate == 0.75


def test_get_top_traders_filters_by_win_rate(httpx_mock):
    httpx_mock.add_response(
        url="https://data-api.polymarket.com/leaderboard?limit=100&offset=0",
        json=LEADERBOARD,
    )
    top = DataApiClient().get_top_traders(min_win_rate=0.70)
    assert len(top) == 1 and top[0].address == "0xAAA"


def test_get_positions_parses_token_id(httpx_mock):
    httpx_mock.add_response(
        url="https://data-api.polymarket.com/positions?user=0xAAA",
        json=POSITIONS,
    )
    positions = DataApiClient().get_positions("0xAAA")
    p = positions[0]
    assert isinstance(p, Position)
    assert p.token_id == "0xTOK"
    assert p.market_id == "0xMKT"
    assert p.outcome == "Yes"
    assert p.avg_price == 0.60
