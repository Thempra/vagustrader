from __future__ import annotations
from abc import ABC, abstractmethod
from dataclasses import dataclass
from typing import TYPE_CHECKING

if TYPE_CHECKING:
    from polywin.clients.gamma import GammaClient
    from polywin.clients.data_api import DataApiClient
    from polywin.clients.clob import ClobClient
    from polywin.config import AppConfig


@dataclass
class StrategyContext:
    clob: "ClobClient"       # ClobClient or PaperClobClient (duck-typed)
    gamma: "GammaClient"
    data_api: "DataApiClient"
    config: "AppConfig"
    db_path: str
    paper: bool


class Strategy(ABC):
    name: str = "base"

    @abstractmethod
    async def run(self, ctx: StrategyContext) -> None:
        """Run indefinitely. Handle own errors and loop internally."""
