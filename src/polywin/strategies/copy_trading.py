from __future__ import annotations
import asyncio
import logging
import time
from polywin.strategies.base import Strategy, StrategyContext
from polywin.db import upsert_position, get_positions

log = logging.getLogger("polywin.strategy.copy_trading")


class CopyTradingStrategy(Strategy):
    name = "copy_trading"

    async def run(self, ctx: StrategyContext) -> None:
        cfg = ctx.config.strategies.copy_trading
        if not cfg.enabled:
            log.info("CopyTrading disabled")
            return
        log.info(f"CopyTrading started — poll={cfg.poll_interval_seconds}s min_pnl=${cfg.min_pnl:,.0f}")
        while True:
            try:
                await self._tick(ctx)
            except Exception as exc:
                log.error(f"CopyTrading tick error: {exc}")
            await asyncio.sleep(cfg.poll_interval_seconds)

    async def _tick(self, ctx: StrategyContext) -> None:
        cfg = ctx.config.strategies.copy_trading
        risk = ctx.config.risk

        top_traders = ctx.data_api.get_top_traders(min_pnl=cfg.min_pnl)
        if not top_traders:
            log.info("No qualifying traders found")
            return

        my_positions = {p["token_id"] for p in await get_positions(path=ctx.db_path)}
        open_count = len(my_positions)

        for trader in top_traders:
            if open_count >= risk.max_open_positions:
                break
            positions = ctx.data_api.get_positions(trader.address)
            copied = 0
            for pos in positions:
                if copied >= cfg.max_copy_per_trader:
                    break
                if not pos.token_id or pos.token_id in my_positions:
                    continue
                if not (cfg.price_guard < pos.avg_price < (1 - cfg.price_guard)):
                    continue

                size = min(risk.max_position_usdc, pos.size * pos.avg_price)
                try:
                    ctx.clob.place_limit_order(
                        token_id=pos.token_id,
                        side="BUY",
                        price=pos.avg_price,
                        size_usdc=size,
                        market_id=pos.market_id,
                    )
                    await upsert_position(
                        token_id=pos.token_id, market_id=pos.market_id, outcome=pos.outcome,
                        size=size / pos.avg_price, avg_price=pos.avg_price,
                        updated_ts=int(time.time()), path=ctx.db_path,
                    )
                    log.info(
                        f"Copied: trader={trader.address[:8]} token={pos.token_id[:8]} "
                        f"outcome={pos.outcome} price={pos.avg_price:.3f} size=${size:.2f}"
                        f"{' [PAPER]' if ctx.paper else ''}"
                    )
                    copied += 1
                    open_count += 1
                    my_positions.add(pos.token_id)
                except Exception as exc:
                    log.error(f"Order error token={pos.token_id}: {exc}")
