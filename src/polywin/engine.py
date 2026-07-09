from __future__ import annotations
import asyncio
import logging
from datetime import datetime, timezone

from polywin.config import Settings, AppConfig
from polywin.clients.creds import ClobCreds, derive_clob_creds
from polywin.clients.gamma import GammaClient
from polywin.clients.data_api import DataApiClient
from polywin.clients.clob import ClobClient, PaperClobClient
from polywin.db import init_db, save_pnl_snapshot, get_today_trades
from polywin.strategies.base import Strategy, StrategyContext

log = logging.getLogger("polywin.engine")


class Engine:
    def __init__(self, settings: Settings, config: AppConfig, paper: bool = False):
        self._s = settings
        self._cfg = config
        self._paper = paper
        self._strategies: list[Strategy] = []

    def register(self, strategy: Strategy) -> None:
        self._strategies.append(strategy)

    async def start(self) -> None:
        db_path = "data/polywin.db"
        await init_db(db_path)

        creds = self._resolve_creds()
        clob: ClobClient | PaperClobClient
        if self._paper:
            clob = PaperClobClient(db_path=db_path)
            log.info("Paper trading mode — orders simulated locally")
        else:
            clob = ClobClient(private_key=self._s.private_key, creds=creds)
            log.info("Live trading mode")

        gamma = GammaClient(self._s.gamma_api_url)
        data_api = DataApiClient(self._s.data_api_url)

        ctx = StrategyContext(
            clob=clob,
            gamma=gamma,
            data_api=data_api,
            config=self._cfg,
            db_path=db_path,
            paper=self._paper,
        )

        names = [s.name for s in self._strategies]
        log.info(f"Starting engine with strategies: {names}")

        tasks = [asyncio.create_task(s.run(ctx), name=s.name) for s in self._strategies]
        tasks.append(asyncio.create_task(self._daily_snapshot(db_path), name="pnl_snapshot"))

        try:
            done, pending = await asyncio.wait(tasks, return_when=asyncio.FIRST_EXCEPTION)
            for t in pending:
                t.cancel()
            for t in done:
                if exc := t.exception():
                    log.error(f"Task {t.get_name()} crashed: {exc}")
                    raise exc
        except asyncio.CancelledError:
            log.info("Engine shutting down gracefully")
        finally:
            gamma.close()
            data_api.close()

    def _resolve_creds(self) -> ClobCreds:
        if self._s.clob_api_key:
            return ClobCreds(
                api_key=self._s.clob_api_key,
                api_secret=self._s.clob_api_secret or "",
                api_passphrase=self._s.clob_api_passphrase or "",
            )
        log.info("Deriving CLOB credentials from private key...")
        creds = derive_clob_creds(self._s.private_key, self._s.clob_host, self._s.chain_id)
        log.info(f"CLOB creds derived: api_key={creds.api_key[:8]}...")
        return creds

    async def _daily_snapshot(self, db_path: str) -> None:
        while True:
            now = datetime.now(timezone.utc)
            seconds_to_midnight = 86_400 - (now.hour * 3600 + now.minute * 60 + now.second)
            await asyncio.sleep(seconds_to_midnight)
            date_str = now.strftime("%Y-%m-%d")
            trades = await get_today_trades(date_str, path=db_path)
            await save_pnl_snapshot(
                date=date_str,
                realized=0.0,
                unrealized=0.0,
                total_trades=len(trades),
                path=db_path,
            )
            log.info(f"P&L snapshot saved: date={date_str} trades={len(trades)}")
