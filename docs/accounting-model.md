# Accounting Model

## Overview

VagusTrader tracks PnL at three levels:

1. **Per-trade** — gain/loss when a position is settled
2. **Per-position** — weighted-average cost basis for open positions  
3. **Aggregate (daily)** — snapshot of realized + unrealized PnL

---

## Data Model

### Positions Table

```sql
positions (
    token_id    TEXT PRIMARY KEY,  -- CLOB token identifier
    market_id   TEXT,              -- condition_id of the market
    outcome     TEXT,              -- Yes/No/etc.
    size        REAL,              -- total tokens held
    avg_price   REAL,              -- weighted average entry price
    updated_ts  INTEGER            -- Unix timestamp of last update
)
```

When a new trade is placed on an already-held token, the position is **blended** using weighted average:

```
new_size     = old_size + trade_size
new_avg_price = (old_size * old_avg_price + trade_size * trade_price) / new_size
```

### Trades Table

```sql
trades (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    ts          INTEGER,            -- Unix timestamp
    market_id   TEXT,                -- condition_id
    token_id    TEXT,                -- CLOB token ID
    side        TEXT,                -- always "BUY" (Polymarket semantics)
    outcome     TEXT,                -- Yes/No/Over/Under/etc.
    price       REAL,                -- execution price in USDC
    size_usdc   REAL,                -- total USDC spent (size * price)
    order_id    TEXT,                -- CLOB order ID (or "paper-{ts}" for paper)
    paper       INTEGER,             -- 1 = paper trade
    result      REAL,                -- NULL = unsettled; 1.0 = WIN; 0.0 = LOSS
    source_json TEXT                 -- JSON array of source traders for this copy
)
```

### PnL Snapshots Table

```sql
pnl_snapshots (
    date          TEXT PRIMARY KEY,  -- "YYYY-MM-DD"
    realized      REAL,              -- sum of all settled PnL for this day
    unrealized    REAL,              -- always 0.0 (reserved for future mark-to-market)
    total_trades  INTEGER            -- number of settled trades this day
)
```

---

## PnL Calculation

### Per-Trade PnL (Settlement)

When a market resolves, each unsettled trade for that token is updated:

```
shares    = size_usdc / price         -- tokens bought
recovered = shares * final_price      -- USDC returned (1.0 = win, 0.0 = loss)
gain      = recovered - size_usdc     -- profit or loss
```

| Result | `final_price` | `gain` |
|---|---|---|
| WIN | 1.0 | `(size_usdc / price) * 1.0 - size_usdc` |
| LOSS | 0.0 | `-size_usdc` (total loss) |

Partial resolutions (e.g. `final_price = 0.5`) are possible for markets with multiple outcomes.

### Aggregate Realized PnL

```sql
SELECT SUM(
    CASE WHEN result >= 0.99
        THEN (size_usdc / price) * 1.0 - size_usdc
        ELSE -size_usdc
    END
) FROM trades WHERE result IS NOT NULL;
```

This sum matches `pnl_snapshots.realized` exactly (verified in production).

### Daily Snapshot Update

On each settlement event:

```go
existing := snapshot for today (if any)
new_total := existing.realized + pnl
new_trades := existing.total_trades + 1
SavePNLSnapshot(Date: today, Realized: new_total, Unrealized: 0.0, TotalTrades: new_trades)
```

### Dashboard Display

| KPI | Formula | Source |
|---|---|---|
| Total P&L | `RealizedPnL + UnrealizedPnL` | In-memory sum |
| Realized | SUM of `pnl_snapshots.realized` | DB query |
| Unrealized | SUM of open positions PnL (current value - cost) | Live price fetch |
| Invested | SUM of today's `trades.size_usdc` | DB query (today filter) |
| Per-trade Gain | `(size_usdc / price) * result - size_usdc` | Per trade row |
| Wallet | Same as Realized | Settled PnL only |

---

## Settlement Flow

```
1. Every 300s, SettlementStrategy iterates open positions
2. For each: fetch market data from CLOB
   └─ Checks if market is resolved (has winner)
3. If resolved:
   a. Get final price for each outcome token
   b. SettleTrades(tokenID, finalPrice)
      └─ UPDATE trades SET result = finalPrice WHERE token_id = ? AND result IS NULL
   c. DeletePosition(tokenID)
   d. updateSnapshot(DB, pnl)
   e. Send Telegram notification
```

---

## Trade Duplication Prevention

The trade recording happens in exactly one place: `copytrading.go` calls `DB.RecordTrade()` after a successful order placement. The paper client does **not** insert into the trades table. This was a historical bug where `PaperClient.PlaceLimitOrder` was also inserting, causing 2x trade records. The fix removed the DB insert from PaperClient.

---

## Edge Cases

| Case | Handling |
|---|---|
| Multiple copies of same token | Weighted average blending in UpsertPosition |
| Partial resolution (0 < price < 1) | Result stored as float, Gain calculated proportionally |
| Same token, different direction | Not applicable — all trades are BUY on Polymarket |
| Duplicate trade rows | Historical duplication detected: the fix prevents new duplicates, but old data may have 2x records. PnL is correct because PnL was also doubled symmetrically. |
| Concurrent settlement + copy | SQLite WAL mode allows concurrent reads; writes are serialized. No race condition on position updates since settlement deletes the position row. |
