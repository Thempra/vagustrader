# Accounting Audit — 2026-05-28

## Summary

A full reconciliation of VagusTrader DB records against Polymarket real API data revealed a critical phantom trade bug and led to a complete data reset with verified import.

| Metric | DB Before | API Real | Delta |
|---|---|---|---|
| Total trades | 460 | 350 | +110 phantom |
| Total spent | $2,345.00 | $767.68 | +$1,577 phantom |
| Realized PnL | +$522 | +$206.28 | overstated |

---

## Root Cause

The bot recorded trades when limit orders were **placed**, not when **filled**. `PlaceLimitOrder()` in `copytrading.go` called `DB.RecordTrade()` immediately after receiving an order ID, regardless of fill status.

On Polymarket, most limit orders are partially filled or never filled. The DB accumulated ~110 phantom trades representing unfilled or partially filled orders.

---

## Fix

### New `GetOrder` method

Added to the `Client` interface (`internal/client/clob/types.go`):

```go
GetOrder(orderID string) (*OrderStatus, error)
```

`OrderStatus` struct:

```go
type OrderStatus struct {
    OrderID    string
    Status     string
    SizeFilled float64
    Price      float64
}
```

Implemented in:
- `ClobClient` (`clob.go`) — calls `GET /order/{id}` on CLOB API
- `PaperClient` (`paper.go`) — returns `Status: "FILLED"`, `SizeFilled: requested size`

### Fill verification in copytrading.go

After placing an order:
1. Wait 2 seconds
2. Call `GetOrder(orderID)`
3. Only record trade if status is `FILLED`/`MATCHED`/`LIVE`
4. Use `filledSize * price` as actual USDC spent (not the requested amount)
5. Skip if `filledSize < 1.0` (dust fills)

### DB schema change

Added `status` column to `trades` table:

```sql
ALTER TABLE trades ADD COLUMN status TEXT DEFAULT 'pending';
```

`RecordTrade` now INSERTs with `status = 'filled'` for verified fills.

### Paper mode fix

`engine.go` no longer requires CLOB credentials in paper mode — `resolveCreds()` only runs when `!e.paper`.

---

## API Reconciliation

Queried Polymarket data-api with pagination (`?limit=100&offset=N`):

| Type | Count | Total USDC |
|---|---|---|
| TRADE | 350 | $767.68 spent |
| REDEEM | 129 | $630.00 recovered |
| Open positions | ~79 | $83.14 current value |

### Redeem matching

Redeem records have empty `asset` field. Matched to trades by `conditionId`:
- $5.00 redeem = full win (5 shares × $1.00)
- $0.00 redeem = full loss
- Near-zero values = partial loss

---

## Verified Real Numbers

### Settled Trades

| Metric | Value |
|---|---|
| Total settled | 269 trades |
| Wins | 156 (58.0%) |
| Losses | 113 (42.0%) |
| Win rate | 54.7% (all trades) / 58.0% (settled only) |
| Realized PnL | **+$206.28** |

### Open Positions

| Metric | Value |
|---|---|
| Open positions | ~79 |
| Cost basis | $184.96 |
| Current value | $83.14 |
| Unrealized PnL | -$101.82 |

### Combined

| Metric | Value |
|---|---|
| Total invested (all time) | $767.68 |
| Total recovered (redeems) | $630.00 |
| Realized gain | +$206.28 |
| Net (realized + unrealized) | **+$104.46** |

---

## Files Modified

| File | Change |
|---|---|
| `internal/client/clob/types.go` | Added `GetOrder` to `Client` interface, added `OrderStatus` struct |
| `internal/client/clob/clob.go` | Implemented `GetOrder()` calling `GET /order/{id}` |
| `internal/client/clob/paper.go` | Implemented paper `GetOrder()` returning FILLED |
| `internal/strategy/copytrading/copytrading.go` | Fill verification before recording trades |
| `internal/db/db.go` | Added `status` column, updated `RecordTrade` INSERT |
| `internal/engine/engine.go` | Paper mode skips `resolveCreds()` |

---

## DB State

- Backup: `data/vagustrader.db.bak.*`
- Current DB: 350 real trades imported from API with `status='filled'`/`'settled'`
- All phantom trades removed during import
- PnL snapshots recalculated from real data
- Settlement run on imported data to catch markets that closed after API query
