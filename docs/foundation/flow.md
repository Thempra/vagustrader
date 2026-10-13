# VagusTrader — App Flow

> Author of: lifecycle phases, navigation map, per-button / per-CLI-command behavior.
> Tables referenced here are defined in backend-schema.md; strategies listed in PRD §4.2.

## 1. App phases (bot process)

```
[Boot] → [Config/Gate] → [Wire] → [Run loop] → [Shutdown]
```

1. **Boot** — Cobra parses command. `run` accepts `--paper` flag (absent ⇒ live).
2. **Config/Gate** — `cfg.LoadSettings` (env) + `cfg.LoadAppConfig` (YAML).
   Gates, in order, each fatal on failure:
   - live: PRIVATE_KEY present and parseable; CLOB creds present or derivable.
   - live: **exactly one** enabled trading strategy (+settlement allowed).
   - DB opened (WAL), migrations applied; volumes writable (else `readonly
     database` errors at first write — see TRD §6 pitfall).
   - live: on-chain USDC balance synced (deposit wallet, native+bridged).
     Balance ≤ 0 ⇒ strategies idle-retry, no orders.
3. **Wire** — build clients (clob or PaperClient, gamma, dataapi, ws), DB,
   notifier, dashboard HTTP server; paper wallets initialized/loaded
   (`paper_wallets`). errgroup starts each enabled strategy + settlement + dashboard.
4. **Run loop** — per strategy: ticker every `poll_interval_seconds` → **tick**
   (§2). WS feed continuously updates book prices used by price guards.
5. **Shutdown** — SIGINT/SIGTERM cancels root context; errgroup drains; no new
   orders dispatched; in-flight fill verification completes or the resting order
   is cancelled (never leave an unverified resting GTC order).

## 2. Strategy tick (canonical happy path)

```
scan → filter → decide → size → guard → order → verify → record → notify
```

1. **Scan**: fetch candidate markets (gamma) and/or leader positions (dataapi).
2. **Filter** (each skip increments `strategy_diagnostics` counter):
   not-open-unresolved (skipped_dead), liquidity < min, resolution > max_days_to_end,
   blocked sport, already held (skipped_held), price band/price guard (skipped_price).
3. **Decide**: strategy-specific signal (decision-logic.md annex).
4. **Size**: kelly_fraction × edge, clamped by size tiers and `max_position_usdc`.
5. **Guard**: global open-position caps, daily loss limit, per-trader copy caps.
6. **Order**: live → signed GTC limit to CLOB; paper → instant simulated fill
   (known artifact, PRD §8).
7. **Verify** (live): authed order lookup, fallback `FindRecentFill` (TRD §8).
8. **Record**: trade row + position upsert (weighted avg) + paper wallet move.
9. **Notify**: Telegram fill message with market, outcome, size, price.

**Settlement tick** (every `interval_seconds`): for each open position, check
market resolution via gamma/data-api → resolved WIN: realized += size×(1−avg),
close position, recovered += size×1; LOSS: realized −= cost; both: set
`trades.result`, Telegram WIN/LOSS alert. Daily: `pnl_snapshots` upsert.

## 3. Dashboard navigation (read-only SPA, single page)

Route map:

| Route | Behavior |
|---|---|
| `/` | Cached HTML page (header KPIs + positions + trades + snapshot strip) |
| `/csv` | Streams trades CSV download |
| `/api/diagnostics` | JSON: latest `strategy_diagnostics` rows per strategy |

Layout: header (fixed) / main = [Positions panel 55% | trades panel], vertical
drag divider / footer (brand, snapshots, CSV button) / popup layer.

### Per-interaction behavior

- **Page load** → serve cached render instantly (<1 ms); background refresher
  rebuilds from DB every few seconds; JS polls and swaps table bodies.
- **Click market cell** → opens `https://polymarket.com/market/<slug>` in new
  tab (link, no in-app state).
- **Click source badge (side cell)** of a position → fixed-position popup under
  cursor listing source traders for that token (from latest `source_json`);
  click outside or ESC → popup closes.
- **Drag column-header right edge** → live column resize (persisted in-memory
  only). **Drag panel divider** → panel flex rebalance.
- **Hover row** → bg3 highlight; header cells sortable on click.
- **CSV button** (footer) → browser navigates `/csv`, downloads export.
- **PAPER/LIVE badge** → static indicator from start-mode; never toggles at runtime.

No forms, no auth, no mutations: every interaction is view-only. The dashboard
never triggers trading actions; all control is via config + CLI + deploy.

## 4. CLI command behaviors

| Command | What happens |
|---|---|
| `run [--paper]` | Full lifecycle §1. Blocks until signal. |
| `dashboard --port N` | Dashboard server only, no bot loop (same DB file). |
| `status --db PATH` | Prints balances, open positions, today's trades/PnL from SQLite; no network. |
| `markets --min-vol N` | One-shot gamma query, prints filtered open markets table. |
| `top-traders --min-pnl N` | One-shot leaderboard query, prints traders table. |
| `derive-creds` | L1 EIP-712 signature → prints CLOB key/secret/passphrase to stdout (operator pastes into env). |
| `deposit [amount]` | Live-only helper: reports deposit-wallet address and current on-chain USDC for the requested funding amount (operator funds manually). |
| `simulate-settlement` | Test path: inserts 1 fake WIN + 1 fake LOSS settlement against local DB to exercise accounting + notifications. |

## 5. Operator journey (outside the app)

1. Fund proxy/deposit wallet USDC manually (Polymarket or transfer).
2. Paper burn-in: run paper app, watch diagnostics funnel + PnL per strategy.
3. Promotion decision (manual, PRD §8 open question c) → edit `config.live.yml`.
4. Live start: deploy/start live app in Coolify **manually** (auto-deploy off).
5. Monitor Telegram + dashboard :live port; incident triage per TRD §7-8.
6. Kill switch: stop the Coolify app (orders already resting are cancelled on
   next tick only — after emergency stop, check portal for resting orders).
