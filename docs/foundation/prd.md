# VagusTrader — PRD (Product Requirements)

> Author of: what/why/scope, feature inventory, risk-guard catalog, open questions.
> Deep per-strategy alpha logic: `docs/decision-logic.md` (annex). PnL math: `docs/accounting-model.md` (annex).

## 1. Idea in one paragraph

VagusTrader is a personal, self-hosted automated trading bot for **Polymarket**
(the decentralized prediction market on Polygon). Written in Go as a single
binary, it runs a pluggable set of strategies that scan open markets and the
behavior of profitable traders, place signed orders through the Polymarket
CLOB, track positions and PnL in local SQLite, and expose a real-time web
dashboard plus Telegram notifications. It runs in two mutually exclusive modes:
**paper** (simulated fills against live prices, virtual wallet per strategy)
and **live** (real orders from a proxy wallet). No external users, no fees, no
custody of third-party funds — 100% single-operator tool.

## 2. Problem & value

- Manually monitoring Polymarket's thousands of open markets and top traders is
  un-scalable and emotional.
- The bot systematizes edge capture: copy top-PnL traders, fade overreactions,
  ride volume/momentum, harvest settlement inefficiencies — 24/7, with hard
  risk limits.
- Full transparency: every fill, skip reason and diagnostic is persisted and
  visible; no black-box signals.

## 3. Target user

The owner-operator: crypto-native, runs the bot on home infra (Coolify on the
"Nuke" box), reviews the dashboard and Telegram alerts, and makes the
strategy on/off and paper→live promotion decisions manually.

## 4. Feature inventory

### 4.1 Execution core
- F1. Dual mode: `--paper` (simulated fills at scanned price, per-strategy
  virtual wallet starting at live-account parity) and live (real signed orders).
- F2. Live order placement via CLOB with EIP-712 signing, GTC limit orders,
  fill verification (authed order lookup + data-api `FindRecentFill` fallback),
  auto-cancel of unverified resting orders.
- F3. Buy-only behavior (the bot never sells; exits come from market resolution).
- F4. Paper wallet accounting: initial/spent/recovered per strategy.
- F5. Live balance sync from **on-chain USDC** (native + bridged) of the deposit
  wallet — never from portfolio value.

### 4.2 Strategies (each independently enable-able via config)
- S1 `copy_trading` — mirror open positions of top-leaderboard traders.
- S2 `contrarian` — fade sharp recent price moves (>threshold in lookback).
  *The only strategy authorized in live (config.live.yml).*
- S3 `under_hunter`, S4 `whale_consensus`, S5 `momentum_rider`,
  S6 `deep_value`, S7 `volume_surge`, S8 `arbitrage`, S9 `resolution_sniper` —
  paper-only alpha experiments.
- S10 `settlement` — housekeeping: detects resolved markets, books WIN/LOSS to
  PnL, closes positions, Telegram-notifies.
- S11 `kelly` — shared position-sizing helper (fractional Kelly), not a strategy.
- Rule: **live mode allows exactly one trading strategy** (+ settlement);
  engine-enforced at startup.

### 4.3 Risk guards (catalog; enforcement detail in risk-management.md)
- R1 max position size (USDC) · R2 max open positions per strategy + global cap
  · R3 daily loss limit · R4 hard rule: never buy markets resolving > N days out
  · R5 min market liquidity · R6 price guard (max deviation from source price)
  · R7 min/max entry price band · R8 blocked/allowed sports · R9 trader quality
  gates (min PnL, min volume, min win rate) · R10 max copies per trader per tick
  · R11 size tiers by price range.

### 4.4 Persistence & reporting
- D1 SQLite (WAL): trades, positions, pnl_snapshots, paper_wallets,
  market_meta, strategy_diagnostics (skip-reason funnel per tick).
- D2 Weighted-average position costing; realized/unrealized PnL; daily snapshots.
- D3 CSV export of trades.

### 4.5 Dashboard (read-only web UI, embedded static assets)
- U1 Header KPIs: balance, invested today, PnL (realized/unrealized), open
  position count, mode badge (PAPER/LIVE), clock.
- U2 Positions panel (55%) + Trades panel, resizable dividers, sortable
  truncatable columns, hover row highlight.
- U3 Market cells link out to `polymarket.com/market/<slug>`.
- U4 Click on position "source" badge → popup listing source traders per token.
- U5 Snapshot strip of last daily PnL snapshots.
- U6 `/api/diagnostics` JSON endpoint + diagnostics rendering.
- U7 Cached HTML rendering (<1 ms response; refresh in background).

### 4.6 Notifications & CLI
- N1 Telegram: fills, settlements (WIN/LOSS), balance/risk events.
- C1 CLI (Cobra): `run [--paper]`, `dashboard`, `status`, `markets`,
  `top-traders`, `derive-creds`, `deposit [amount]`, `simulate-settlement`.

## 5. Non-functional requirements
- NF1. Single static Go binary, multi-arch Docker image, non-root (uid 100).
- NF2. Dashboard must stay responsive with >2000 open positions (indexed queries).
- NF3. Graceful shutdown (errgroup, context cancellation) without leaving orders unverified.
- NF4. Volumes survive container recreation (`<uuid>_data`, `<uuid>_logs`).
- NF5. Structured logging (slog) to file volume for post-incident forensics.

## 6. Out of scope
- Multi-user accounts, SaaS, custody of others' funds, selling/exit orders,
  shorting, external brokerage, mobile apps, automated strategy retraining,
  routing bot traffic through VPN/WARP (explicit operator decision — see TRD §7).

## 7. Acceptance criteria (product-level)
- AC1. A fill placed in live appears in the bot DB **and** data-api within one tick, matched by token+timestamp.
- AC2. In paper mode no network order call reaches the CLOB (PaperClient only).
- AC3. Startup with 2 enabled trading strategies in live mode refuses to boot.
- AC4. Daily loss limit breach halts new entries for the rest of the UTC day.
- AC5. Dashboard reflects a new trade within one refresh cycle.

## 8. Current product status & known limitations
- Live **STOPPED** since 2026-09-10 after **-60% drawdown**; auto-deploy off;
  manual start only. Contrarian is knife-catching with **no stop-loss** — the
  main open risk item.
- Paper profitability is an **artifact** (instant fills at scanned price; no
  book depth, slippage, or rejections). Never treat paper PnL as predictive.
- Copy lag up to one `poll_interval`.
- Open questions: (a) stop-loss or max-hold-time for contrarian before any live
  restart; (b) whether daily_loss_limit R3 must become hard-enforced (docs flag
  it as currently not enforced); (c) promotion criteria from paper→live.
