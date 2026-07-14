import pytest
import yaml


def test_settings_only_needs_private_key(monkeypatch):
    monkeypatch.setenv("PRIVATE_KEY", "0x" + "ab" * 32)
    monkeypatch.delenv("CLOB_API_KEY", raising=False)
    from polywin.config import Settings
    s = Settings()
    assert s.private_key == "0x" + "ab" * 32
    assert s.clob_api_key is None
    assert s.clob_host == "https://clob.polymarket.com"
    assert s.chain_id == 137


def test_settings_missing_private_key_raises(monkeypatch):
    monkeypatch.delenv("PRIVATE_KEY", raising=False)
    from polywin.config import Settings
    with pytest.raises(Exception):
        Settings()


def test_load_app_config_defaults_when_file_absent():
    from polywin.config import load_app_config
    cfg = load_app_config("nonexistent.yml")
    assert cfg.risk.max_position_usdc == 50.0
    assert cfg.strategies.copy_trading.enabled is True
    assert cfg.strategies.copy_trading.min_pnl == 10_000


def test_load_app_config_overrides_from_file(tmp_path):
    from polywin.config import load_app_config
    f = tmp_path / "config.yml"
    f.write_text(yaml.dump({
        "risk": {"max_position_usdc": 25.0},
        "strategies": {"copy_trading": {"min_pnl": 50_000}},
    }))
    cfg = load_app_config(str(f))
    assert cfg.risk.max_position_usdc == 25.0
    assert cfg.strategies.copy_trading.min_pnl == 50_000
    assert cfg.strategies.copy_trading.poll_interval_seconds == 300
