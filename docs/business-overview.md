# VagusTrader: Business Overview

## What It Is

VagusTrader is an automated copy-trading bot for [Polymarket](https://polymarket.com), the leading decentralized prediction market platform. It continuously monitors top-performing Polymarket traders and automatically replicates their positions, allowing users to benefit from the expertise of profitable traders without manual intervention.

## Core Value Proposition

- **Passive Exposure to Expert Trading:** Deploy capital once and let the bot systematically follow the best traders on Polymarket.
- **Time Efficiency:** Eliminates the need to manually research markets, monitor positions, or execute trades.
- **Data-Driven Trader Selection:** Uses Polymarket's own leaderboard data to select traders based on realized PnL, not subjective criteria.
- **Transparent Accounting:** Full PnL tracking per trade with weighted-average position costing and daily settlement snapshots.

## How It Generates Value

1. Trader Discovery — Polls Polymarket's Data API leaderboard for traders meeting minimum PnL threshold (`min_pnl`, default $5,000).
2. Position Mirroring — For each qualifying trader, fetches their open positions and replicates them proportionally, respecting risk limits.
3. Market-Aware Execution — Checks CLOB orderbooks for executable prices before placing limit orders; respects price guards to avoid adverse fills.
4. Settlement Tracking — Periodically checks open positions for market resolution, records results, and updates PnL.

## Revenue Model

VagusTrader does not charge fees or manage external funds. It operates as a personal automated trading tool. Value accrues directly as PnL on the user's Polymarket account.

## Key Metrics

| Metric | Tracking |
|---|---|
| Total PnL (Realized + Unrealized) | Dashboard header + pnl_snapshots table |
| Win/Loss per trade | Dashboard balances table |
| Daily trade volume | Dashboard invested-today counter |
| Open position count | Dashboard positions panel |
| Source trader diversity | Dashboard source-traders popup per token |

## Target Users

- **Polymarket power users** who want to scale their trading without more screen time.
- **Crypto-native investors** looking for automated alpha capture in prediction markets.
- **Quantitative traders** who want a reliable execution layer for Polymarket strategies.
- **Portfolio diversifiers** seeking non-correlated returns through prediction markets.

## Competitive Landscape

| Approach | Limitation |
|---|---|
| Manual trading | Time-intensive, emotional, hard to scale |
| Signal services | Delayed, opaque methodology, subscription cost |
| Custom bots | High engineering cost, maintenance burden |
| VagusTrader | Open source, transparent, minimal operational overhead |

## Current Status

- **Stage:** Production (paper trading mode active)
- **Deployment:** Dockerized with multi-architecture support
- **Dashboard:** Real-time web UI with cached rendering (<1ms response)
- **Notifications:** Telegram integration for settlement alerts

## Known Limitations (Product)

- **Copy lag:** Up to `poll_interval` (default 120s) delay in replicating trades.
- **No stop-loss:** Positions are held indefinitely until market resolution.
- **Daily loss limit:** Config option exists but is not enforced at runtime.
- **No independent analysis:** Relies solely on trader signals without fundamental market evaluation.
- **Survivorship bias:** Leaderboard only shows currently profitable traders.
