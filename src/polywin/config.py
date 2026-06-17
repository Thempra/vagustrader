from __future__ import annotations
import os
from pathlib import Path
from typing import Any, Optional, Tuple, Type
import yaml
from pydantic import BaseModel
from pydantic_settings import BaseSettings, SettingsConfigDict
from pydantic_settings.main import PydanticBaseSettingsSource


class Settings(BaseSettings):
    model_config = SettingsConfigDict(env_file=".env", env_file_encoding="utf-8")

    private_key: str
    clob_api_key: Optional[str] = None
    clob_api_secret: Optional[str] = None
    clob_api_passphrase: Optional[str] = None

    gamma_api_url: str = "https://gamma-api.polymarket.com"
    data_api_url: str = "https://data-api.polymarket.com"
    clob_host: str = "https://clob.polymarket.com"
    chain_id: int = 137

    @classmethod
    def settings_customise_sources(
        cls,
        settings_cls: Type[BaseSettings],
        init_settings: PydanticBaseSettingsSource,
        env_settings: PydanticBaseSettingsSource,
        dotenv_settings: PydanticBaseSettingsSource,
        file_secret_settings: PydanticBaseSettingsSource,
    ) -> Tuple[PydanticBaseSettingsSource, ...]:
        # Skip .env file loading when the sentinel env var is set (used in tests)
        if os.environ.get("POLYWIN_NO_DOTENV"):
            return (init_settings, env_settings, file_secret_settings)
        return (init_settings, env_settings, dotenv_settings, file_secret_settings)


class RiskConfig(BaseModel):
    max_position_usdc: float = 50.0
    max_open_positions: int = 10
    daily_loss_limit_usdc: float = 200.0


class CopyTradingConfig(BaseModel):
    enabled: bool = True
    poll_interval_seconds: int = 300
    min_win_rate: float = 0.70
    min_volume_usd: float = 10_000
    price_guard: float = 0.05
    max_copy_per_trader: int = 3


class StrategiesConfig(BaseModel):
    copy_trading: CopyTradingConfig = CopyTradingConfig()


class MarketsConfig(BaseModel):
    min_liquidity_usdc: float = 5_000


class AppConfig(BaseModel):
    risk: RiskConfig = RiskConfig()
    strategies: StrategiesConfig = StrategiesConfig()
    markets: MarketsConfig = MarketsConfig()


def load_app_config(path: str = "config.yml") -> AppConfig:
    p = Path(path)
    if not p.exists():
        return AppConfig()
    with open(p) as f:
        data = yaml.safe_load(f) or {}
    return AppConfig.model_validate(data)
