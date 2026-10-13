# VagusTrader — Backend Schema

> Author of: storage layout, every table/column/index, ER, auth/credential flow, end-to-end data flows & persistence semantics.
> Where the queries live and perf constraints: TRD §8. Behavior that writes these rows: flow.md §2.

## 1. Storage model

Single SQLite file `/app/data/vagustrader.db` (named volume `<uuid>_data`),
WAL mode, accessed only through `internal/db`. No external DB service, no
cache layer; dashboard reads the same file (or its own connection to it).
Migrations are forward-only, idempotent, executed at Open():
`ALTER TABLE ADD COLUMN` for light adds; `*_new` table-rebuild + copy + drop +
rename for shape changes (positions, pnl_snapshots strategy-dimension migrations).

## 2. Tables (final post-migration shape)

### trades — one row per recorded fill
| column | type | notes |
|---|---|---|
| id | INTEGER PK AUTOINCREMENT | |
| ts | INTEGER NOT NULL | unix seconds, fill time |
| market_id | TEXT NOT NULL | condition_id |
| token_id | TEXT NOT NULL | outcome token (ERC-1155 id) |
| side | TEXT NOT NULL | BUY (bot is buy-only, PRD F3) |
| price | REAL NOT NULL | fill price 0–1 |
| size_usdc | REAL NOT NULL | notional |
| order_id | TEXT | CLOB order id; empty for unverified/paper |
| paper | INTEGER NOT NULL DEFAULT 0 | 1 = simulated fill |
| result | REAL | NULL=open; 1=WIN, 0=LOSS (settlement writes) |
| outcome | TEXT | YES/NO |
| status | TEXT | lifecycle (`filled`, …) |
| strategy | TEXT | owning strategy name |
| schema_version | TEXT NOT NULL DEFAULT 'legacy' | post-fix data isolation tag; queries filter on it |
| settled_at | INTEGER | settlement timestamp |
| source_json | TEXT | JSON provenance (copied traders / signal inputs) — feeds dashboard popup |

Indexes: `idx_trades_strategy(strategy)`, `idx_trades_schema_version`,
**`idx_trades_token_ts(token_id, ts)`** (mandatory — see TRD §8 CPU incident).

### positions — current holding per (token, strategy)
| column | type | notes |
|---|---|---|
| id | INTEGER PK AUTOINCREMENT | |
| token_id | TEXT NOT NULL | |
| market_id | TEXT NOT NULL | |
| outcome | TEXT NOT NULL | YES/NO |
| size | REAL NOT NULL | shares |
| avg_price | REAL NOT NULL | weighted-average cost |
| strategy | TEXT NOT NULL DEFAULT 'copy_trading' | |
| updated_ts | INTEGER NOT NULL | |
| schema_version | TEXT NOT NULL DEFAULT 'legacy' | |
| UNIQUE | (token_id, strategy) | upsert key on fill |

Index: `idx_positions_strategy`.

### pnl_snapshots — daily EOD rollup per strategy
`id` PK, `date TEXT` (YYYY-MM-DD UTC), `strategy`, `realized REAL`,
`unrealized REAL`, `total_trades INTEGER`, `schema_version`;
`UNIQUE(date, strategy)`. Written by settlement housekeeping.

### paper_wallets — virtual bankroll per strategy (paper mode only)
`strategy` PK, `initial_balance REAL DEFAULT 10000`, `spent REAL`,
`recovered REAL`, `updated_ts`. Spendable = initial − spent + recovered.
Live mode: no row; spendable comes from on-chain USDC (never stored as truth;
synced each cycle into memory per TRD §3).

### market_meta — market ↔ slug cache
`condition_id` PK, `question TEXT`, `slug TEXT`. Enables polymarket.com links
without re-hitting gamma.

### strategy_diagnostics — per-tick funnel (write-heavy, append-only)
`id` PK, `ts`, `strategy`, `scanned`, `skipped_not_ou`, `skipped_price`,
`skipped_sport`, `skipped_held`, `skipped_dead`, `opened`, `errors`, `note`.
Index: `idx_diag_strategy_ts(strategy, ts)`. Pruned/limited reads only latest N.

## 3. ER (logical)

```
market_meta 1───* trades        (market_id = condition_id)
market_meta 1───* positions     (market_id)
trades      *──1 positions      (token_id+strategy; positions is the folded state)
trades      *──1 strategy       (name, soft FK)
positions   1──0..1 pnl path    (settlement books result into trades, rolls into pnl_snapshots)
paper_wallets 1──1 strategy     (paper mode)
```
No enforced FK constraints (SQLite left unenforced); integrity is
application-side via upserts in `internal/db` transactions.

## 4. Authentication / credential flows

There is **no user auth** (single operator, dashboard unauthenticated and
read-only, intended for LAN/Traefik-protected port — do not expose to internet).

Machine auth, two tiers (Polymarket CLOB):
1. **L1**: EIP-712 signature by signer EOA (PRIVATE_KEY) → derives/registers
   API creds; orders carry signature type POLY_1271 against proxy wallet
   `0x2ed0…3173` (funds live in the proxy, orders signed by EOA).
2. **L2**: HMAC headers `POLYMARKET_APIKEY/SECRET/PASSPHRASE + timestamp +
   signature` on every authed REST call (place/cancel/order lookup).
   Known quirk: L1-derived keys without 1271 binding 404 on `GET /order/{id}`
   → fill verification fallback per TRD §8.
Lifecycle: `derive-creds` regenerates deterministically from the key; creds
stored only in env/secrets (TRD §4). Telegram: plain bot-token Bearer.
On-chain: read-only eth_calls, no auth.

## 5. Key data flows

1. **Fill flow (live)**: signal → POST /order (signed) → verify (authed GET →
   fallback data-api `/trades?user=`) → tx{insert trades, upsert positions,
   (paper only) bump paper_wallets} → Telegram. Unverified ⇒ cancel, no row.
2. **Settlement flow**: gamma resolution check per open position → UPDATE
   trades.result/settled_at → realized rollup → DELETE/zero position → snapshot
   upsert → Telegram WIN/LOSS.
3. **Balance flow (live)**: eth_call `balanceOf` (both USDC contracts, deposit
   wallet) → in-memory spendable → risk gate each tick. Portfolio value from
   data-api is display context ONLY, never spendable (bug class, TRD §3).
4. **Dashboard flow**: handlers query trades/positions/pnl_snapshots/
   paper_wallets/diagnostics → cached HTML render; source popup reads latest
   `source_json` per token via `idx_trades_token_ts`.

## 6. Persistence semantics & KV contracts

- `source_json` contract: JSON object/array of source entries `{"trader":
  "0x..","pnl":..,"size":..}`; dashboard renders it raw — keep keys stable.
- `schema_version` values: `'legacy'` (pre-fix rows, excluded from post-fix PnL
  views) → bump per accounting-breaking change; filter in every aggregate query.
- Time: all `ts` unix seconds UTC; `pnl_snapshots.date` is UTC calendar day.
- Logs (not DB): JSON slog at `/app/logs` in volume `<uuid>_logs`; "Order
  payload" lines contain full signed orders (tokenId) — the forensic join key
  against portal fills.
