# tests/clients/test_data_api.py
from polywin.clients.data_api import DataApiClient, LeaderboardEntry, Position

LEADERBOARD = [
    {"proxyWallet": "0xAAA", "userName": "alpha", "pnl": 50000.0, "vol": 1e6},
    {"proxyWallet": "0xBBB", "userName": "beta",  "pnl": 5000.0,  "vol": 2e5},
]
POSITIONS = [
    {"asset": "0xTOK", "conditionId": "0xMKT", "outcome": "Yes",
     "size": 200.0, "avgPrice": 0.60, "currentValue": 130.0},
]

LEADERBOARD_URL = (
    "https://data-api.polymarket.com/v1/leaderboard"
    "?limit=100&offset=0&timePeriod=ALL&orderBy=PNL"
)


def test_get_leaderboard(httpx_mock):
    httpx_mock.add_response(url=LEADERBOARD_URL, json=LEADERBOARD)
    entries = DataApiClient().get_leaderboard()
    assert len(entries) == 2
    assert isinstance(entries[0], LeaderboardEntry)
    assert entries[0].address == "0xAAA"
    assert entries[0].name == "alpha"
    assert entries[0].pnl == 50000.0
    assert entries[0].volume == 1e6


def test_get_top_traders_filters_by_pnl(httpx_mock):
    httpx_mock.add_response(url=LEADERBOARD_URL, json=LEADERBOARD)
    top = DataApiClient().get_top_traders(min_pnl=10_000)
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
