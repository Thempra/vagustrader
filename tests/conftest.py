import os
import pytest


@pytest.fixture(autouse=True)
def _no_dotenv(monkeypatch):
    """Prevent pydantic-settings from loading the .env file during tests."""
    monkeypatch.setenv("POLYWIN_NO_DOTENV", "1")
