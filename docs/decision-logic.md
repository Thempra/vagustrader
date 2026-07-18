# VagusTrader — Decision Logic

This document describes how the system decides **which events to bet on** and **which outcome to choose**.

---

## Strategy: Copy Trading

VagusTrader does not analyze events independently. Instead, it **replicates the open positions of the most profitable Polymarket traders**. The hypothesis is that traders with high historical PnL possess an informational or analytical edge, and replicating their open positions extracts positive expected value.

---

## Step 1 — Select traders to follow

**Source:** Polymarket public leaderboard (`data-api.polymarket.com/v1/leaderboard`)

**Eligibility:**

| Criterion | Config Key | Default | Purpose |
|---|---|---|---|
| Cumulative PnL ≥ `min_pnl` | `strategies.copy_trading.min_pnl` | $5,000 | Filter for proven profitability |
| Cumulative volume ≥ `min_volume_usd` | `strategies.copy_trading.min_volume_usd` | $1,000 | Filter out low-activity accounts |

**Process:**
1. Download leaderboard (up to 100 entries, ordered by PnL).
2. Filter entries above both thresholds.
3. For each qualifying trader, fetch their open positions via Data API `GET /positions?user={address}`.

**Note:** The leaderboard only shows **currently** profitable traders. Traders who lost money and stopped trading are invisible (survivorship bias).

---

## Step 2 — Select eligible positions

Each open position from a qualifying trader is evaluated independently:

| Condition | Check | Config |
|---|---|---|
| Token in dead cache | Skip if seen < 1 hour ago | `deadTokenTTL = 3600` (hardcoded) |
| Already held | Skip if token already in DB | `positions` table lookup |
| CLOB market active | Fetch `/markets/{condition_id}` | Must resolve to active data |
| Price within guard | `abs(price - trader_price) / price < guard` | `price_guard` (default 0.05 = 5%) |
| Days to resolution | `days < max_days_to_resolution` | `max_days_to_resolution` (default 7) |

### Price Guard Detail

The bot compares the trader's entry price to the current CLOB ask:

```
deviation = abs(current_price - trader_entry_price) / trader_entry_price
if deviation > price_guard → SKIP
```

Prevents copying trades where the price has moved significantly against entry.

### Dead Token Cache

Tokens that fail any eligibility check enter a 1-hour cache (`deadTokens map[string]int64`). Subsequent ticks skip them until the TTL expires. This prevents repeated API calls for permanently invalid positions.

---

## Step 3 — Select outcome

**No independent analysis.** The bot buys the same outcome the source trader holds:

- Trader has `YES` → buy `YES`
- Trader has `NO` → buy `NO`
- Trader has `Clippers` → buy `Clippers`

**The side is always BUY.** Polymarket does not support short selling. A bearish view is expressed by buying the complementary outcome token (e.g. `NO` instead of `YES`).

---

## Step 4 — Position sizing

```go
size_usdc = min(max_position_usdc, trader_value_usdc, available_balance)
```

Where `trader_value_usdc` is the trader's position size in USDC (adjusted for the min of the individual's position and a per-trader cap).

**Config:** `risk.max_position_usdc` (default: $50)

---

## Step 5 — Risk limits per tick

| Limit | Scope | Enforced |
|---|---|---|
| Max open positions | Global — total across all traders | ✅ |
| Max copies per trader per tick | Per trader per tick | ✅ |
| USDC balance ≥ $1.00 | Global — skip if insufficient | ✅ |
| Token in dead cache | Per token — 1h TTL | ✅ |
| Already held | Per token — no duplicates | ✅ |
| Daily loss limit | Global | ❌ (config exists, not implemented) |

---

## Step 6 — Two-phase tick execution

Each tick runs in two distinct phases:

### Phase 1: SCAN — Build token → sources map

```
1. Fetch top traders from Data API leaderboard
2. For each qualifying trader:
   a. Fetch their current positions from Data API
   b. For each position token:
      - Append (trader_address, outcome, size, entry_price) to token_sources[token_id]
3. Result: map[string][]sourceTrader  — every source trader for every candidate token
```

```go
type sourceTrader struct {
    Address string
    Outcome string
    Size    float64
    Price   float64
}
```

### Phase 2: COPY — Evaluate and execute

```
For each (token, sources) in token_sources map:
  1. Skip if token in dead cache
  2. Skip if already held in DB
  3. Aggregate: use the FIRST source trader's outcome (all sources should agree)
  4. Fetch CLOB market data (cached per tick — one API call per unique market)
  5. Verify market is active on CLOB
  6. Determine buy price:
     a. Try CLOB market data "price" field first
     b. Fall back to orderbook best ask
  7. Apply price guard: compare buy price to source trader's price
  8. Apply max_days_to_resolution filter
  9. Size position: min(max_position, trader_value)
  10. Place BUY limit order (paper or live)
  11. On success:
      - Record trade in DB with outcome + source_json
      - Upsert position (weighted average blend)
      - Insert market metadata (question, slug)
      - Log "Copied" with trader, outcome, price, size, source count
  12. On failure: add token to dead cache
```

**Source tracking:** Each trade stores a JSON array of all source traders that held the same token:

```json
[
  {"address": "0xabc...", "outcome": "Yes", "size": 100.0, "price": 0.55},
  {"address": "0xdef...", "outcome": "Yes", "size": 50.0, "price": 0.60}
]
```

This enables the dashboard to show a "source traders" popup per token.

---

## Step 7 — Order execution

Places a **BUY limit order** on the Polymarket CLOB:

| Field | Value |
|---|---|
| `side` | BUY |
| `price` | Current market price (CLOB or orderbook) |
| `size` | Calculated in Step 4 |
| `token_id` | The chosen outcome token |
| `order_type` | LIMIT (GTC — good-till-cancelled) |

### Live Mode (ClobClient)

1. Build EIP-712 signed order using `BuildSignedOrder()`.
2. Round amounts per CLOB tick size.
3. POST to `/order` with HMAC authentication.
4. Parse response for order ID.

### Paper Mode (PaperClient)

1. Generate fake order ID `paper-{timestamp}`.
2. Return success immediately — no network call.

### Post-Execution (both modes)

1. `RecordTrade()` — insert trade into SQLite.
2. `UpsertPosition()` — weighted average blending.

---

## Step 8 — Settlement

The settlement checker runs independently every 300 seconds:

```
For each open position in DB:
  1. Fetch market data from CLOB: GET /markets/{condition_id}
  2. Check if market is resolved:
     - Has a winning token ID
     - Token prices are final
  3. If resolved:
     a. Get token_id for each outcome
     b. Get token's price (1.0 = win, 0.0 = loss)
     c. SettleTrades(tokenID, finalPrice)
        → UPDATE trades SET result = $finalPrice WHERE token_id = ? AND result IS NULL
     d. DeletePosition(tokenID)
        → Remove from active positions
     e. updateSnapshot(DB, pnl)
        → INSERT OR UPDATE pnl_snapshots SET realized += pnl, total_trades += 1
     f. Send Telegram notification
```

### PnL Calculation on Settlement

```go
shares    = size_usdc / price        // tokens bought
recovered = shares * final_price     // 1.0 = win, 0.0 = loss
gain      = recovered - size_usdc    // +$X or -$50
```

### Snapshot Update

```go
existing := today's snapshot (or zero)
SavePNLSnapshot{
    Date:        today,
    Realized:    existing.Realized + gain,
    Unrealized:  0.0,
    TotalTrades: existing.TotalTrades + 1,
}
```

---

## Execution cycle summary

```
Every poll_interval (default 120s):
  ┌─ Phase 1: SCAN ──────────────────────────────┐
  │  1. Fetch leaderboard                         │
  │  2. Fetch positions for each qualifying trader │
  │  3. Build token → sources map                 │
  └───────────────────────────────────────────────┘
  ┌─ Phase 2: COPY ───────────────────────────────┐
  │  4. For each candidate (deduplicated):         │
  │     a. Check dead cache                        │
  │     b. Check already held                      │
  │     c. Fetch market data + apply filters       │
  │     d. Place BUY limit order                   │
  │     e. Record trade + upsert position          │
  └───────────────────────────────────────────────┘

Every 300s (independently):
  ┌─ Settlement Check ────────────────────────────┐
  │  1. For each open position in DB               │
  │  2. Check CLOB market resolution               │
  │  3. If resolved: settle trades, snapshot PnL   │
  └───────────────────────────────────────────────┘
```

---

## Summary diagram

```
Polymarket Leaderboard
  └─ Filter: PnL ≥ min_pnl, Volume ≥ min_volume
       └─ For each qualifying trader
            └─ Fetch their open positions
                 └─ Deduplicate by token → build token→sources map
                      └─ For each unique token:
                           ├─ Dead cache? → SKIP
                           ├─ Already held? → SKIP
                           ├─ CLOB active? → SKIP if not
                           ├─ Price guard? → SKIP if deviated
                           ├─ Resolution too far? → SKIP
                           └─ PASS → BUY limit order
                                ├─ Record trade + outcome + sources
                                ├─ Upsert position (weighted avg)
                                └─ Save market metadata
          
Hold position until market resolution
  └─ Settlement check (every 300s)
       └─ Market resolved?
            ├─ UPDATE trades SET result
            ├─ DELETE position
            └─ UPDATE pnl_snapshot
```

---

## Known limitations

- **No independent event analysis:** The system trusts the trader's signal completely.
- **Copy lag:** Up to `poll_interval` (default 120s) delay. Price may have moved.
- **No stop-loss/take-profit:** Positions stay until market resolution.
- **Survivorship bias:** Leaderboard only shows currently profitable traders.
- **Daily loss limit not enforced:** Config option exists but is not checked at runtime.
- **No position reduction:** Cannot scale down positions — only add or hold.
