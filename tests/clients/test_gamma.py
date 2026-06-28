# tests/clients/test_gamma.py
import pytest
from polywin.clients.gamma import GammaClient, Market

FIXTURE = {
    "id": "mkt-1",
    "question": "Will BTC hit $100k?",
    "outcomes": '["Yes","No"]',
    "outcomePrices": '["0.65","0.35"]',
    "clobTokenIds": '[{"token_id":"0xYES","outcome":"Yes"},{"token_id":"0xNO","outcome":"No"}]',
    "volume": "123456.78",
    "liquidity": "45678.90",
    "active": True,
    "endDate": "2026-06-01T00:00:00Z",
    "conditionId": "0xcond1",
}
NEAR_RESOLVED = {**FIXTURE, "outcomePrices": '["0.97","0.03"]', "id": "mkt-nr"}


def test_get_markets_parses_all_fields(httpx_mock):
    httpx_mock.add_response(
        url="https://gamma-api.polymarket.com/markets?limit=100&offset=0&active=true",
        json={"data": [FIXTURE], "next_cursor": None},
    )
    markets = GammaClient().get_markets()
    m = markets[0]
    assert isinstance(m, Market)
    assert m.outcomes == ["Yes", "No"]
    assert m.outcome_prices == [0.65, 0.35]
    assert m.token_ids == ["0xYES", "0xNO"]
    assert m.volume == 123456.78
    assert m.condition_id == "0xcond1"


def test_get_market_by_id(httpx_mock):
    httpx_mock.add_response(
        url="https://gamma-api.polymarket.com/markets/0xcond1",
        json=FIXTURE,
    )
    m = GammaClient().get_market("0xcond1")
    assert m.token_ids == ["0xYES", "0xNO"]


def test_get_tradeable_filters_low_volume(httpx_mock):
    low = {**FIXTURE, "volume": "100.0", "id": "mkt-low"}
    httpx_mock.add_response(
        url="https://gamma-api.polymarket.com/markets?limit=100&offset=0&active=true",
        json={"data": [FIXTURE, low], "next_cursor": None},
    )
    result = GammaClient().get_tradeable_markets(min_volume=10_000)
    assert len(result) == 1 and result[0].id == "mkt-1"


def test_get_tradeable_filters_near_resolved(httpx_mock):
    httpx_mock.add_response(
        url="https://gamma-api.polymarket.com/markets?limit=100&offset=0&active=true",
        json={"data": [FIXTURE, NEAR_RESOLVED], "next_cursor": None},
    )
    result = GammaClient().get_tradeable_markets(min_volume=0, price_guard=0.05)
    assert len(result) == 1 and result[0].id == "mkt-1"
