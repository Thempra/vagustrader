# Risk Management

## Current Risk Controls

### Position-Level Limits

| Control | Config Key | Default | Enforced |
|---|---|---|---|
| Max position size | `max_position_usdc` | $10 | ✅ Yes |
| Price guard (max deviation) | `price_guard` | 5% | ✅ Yes |
| Max copies per trader per tick | `max_copy_per_trader` | 3 | ✅ Yes |
| Min entry price | `min_entry_price` | 0.15 | ✅ Yes |
| Max entry price | `max_entry_price` | 0.80 | ✅ Yes |
| Dynamic size tiers | `size_tiers` | $3–10 by range | ✅ Yes |

### Market-Level Limits

| Control | Config Key | Default | Enforced |
|---|---|---|---|
| Min liquidity | `min_liquidity_usdc` | $1,000 | ✅ Yes |
| Max days to resolution | `max_days_to_resolution` | 7 days | ✅ Yes |
| Min trader PnL threshold | `min_pnl` | $5,000 | ✅ Yes |
| Min trader volume | `min_volume_usd` | $1,000 | ✅ Yes |
| Blocked sports | `blocked_sports` | `["nba", "nhl"]` | ✅ Yes |
| Allowed sports | `allowed_sports` | `[]` (all) | ✅ Yes |

### Trader-Level Limits

| Control | Config Key | Default | Enforced |
|---|---|---|---|
| Min trader win rate | `min_trader_win_rate` | 0.60 | ✅ Yes |

### System-Level Limits

| Control | Config Key | Default | Enforced |
|---|---|---|---|
| Max concurrent positions | `max_open_positions` | 2000 | ✅ Yes |
| Daily loss limit | `daily_loss_limit_usdc` | $200 | ❌ No |

---

## Risk Control Details

### Price Guard

Before placing a buy order, the bot compares the trader's entry price to the current CLOB ask price:

```
deviation = abs(current_price - trader_price) / trader_price
if deviation > price_guard → SKIP position
```

Default: 5%. Prevents copying trades that have moved significantly against the original entry.

### Max Position Size

Each copy trade is sized as:

```
size = min(
    config.max_position_usdc,
    (trader_value / total_trader_value) * available_balance
)
```

Default: $50 per position. Hard limit — no single trade can exceed this value.

### Market Liquidity Filter

Only markets with sufficient CLOB liquidity are traded:

```
if market.liquidity < config.min_liquidity_usdc → SKIP market
```

Prevents entering illiquid positions that cannot be properly priced.

### Trader Quality Filters

Traders must meet both criteria:

- `PnL >= min_pnl` — proven profitability
- `Volume >= min_volume_usd` — sufficient activity to filter out noise

---

## Limitations

### Not Enforced

**Daily loss limit** (`daily_loss_limit_usdc`): The config option exists but `copytrading.go` does not check it before placing new trades. A series of losing days will continue accumulating new positions until `max_open_positions` is reached.

### Architectural

| Limitation | Impact | Mitigation |
|---|---|---|
| **Copy lag** (poll interval) | Up to 120s delay in replicating trades. Fast traders may exit before copy executes. | Lower `poll_interval_seconds` (min 30s recommended). |
| **No stop-loss** | Positions held until market resolution. A losing position cannot be exited early. | None (Polymarket CLOB does not support stop-loss orders). |
| **No independent analysis** | Strategy blindly follows trader signals without market evaluation. | Add custom filters in `copytrading.tick()` or implement a new `Strategy`. |
| **Survivorship bias** | Leaderboard shows only currently profitable traders. Failed traders are invisible. | Track historical trader performance in the database for offline analysis. |
| **Paper/live mode** | No automated failover. A live mode restart with paper trades in DB creates confusion. | Always run `--paper` for testing. Clear DB when switching modes. |

### Market-Specific

| Risk | Description |
|---|---|
| **Market manipulation** | Traders can inflate volume/PnL to attract copiers, then exit before market resolution. |
| **CLOB liquidity** | Thin order books cause wider spreads and worse execution prices. |
| **Resolution risk** | Markets with disputed outcomes may delay resolution indefinitely. |
| **Oracle failure** | Polymarket relies on UMA or CTH oracle for resolution. Oracle failure = no settlement. |
| **Smart contract risk** | The CTF Exchange contract is audited but not immune to exploits. |

---

## Recommendations

### Short Term

1. **Set realistic position sizes.** $50 per trade with 2000 max positions = $100,000 total exposure. Adjust `max_position_usdc` based on account size.
2. **Monitor daily.** Check the dashboard each day for PnL trends and unusual activity.
3. **Use paper mode first.** Run for at least 1-2 weeks in paper mode before risking real capital.
4. **Diversify traders.** The `max_copy_per_trader` limit prevents overexposure to any single trader.

### Medium Term

1. **Implement daily loss limit.** Add a check in `copytrading.tick()`:

```go
func checkDailyLossLimit(db *db.DB, limit float64) bool {
    today := time.Now().UTC().Format("2006-01-02")
    snapshots, _ := db.GetPNLSnapshots(1)
    for _, s := range snapshots {
        if s.Date == today && s.Realized < -limit {
            return false // stop trading
        }
    }
    return true
}
```

2. **Track historical trader performance.** Store trader PnL history in the database and apply a minimum track record filter (e.g. "profitable for 7+ days").
3. **Add Telegram alerts for large PnL swings.** The `sendNotification` function is already in place; extend it to alert on large daily losses.

### Long Term

1. **Multi-strategy support.** The `Strategy` interface already supports this. Add trend-following, arbitrage, or market-making strategies to diversify.
2. **Portfolio-level risk.** Implement Value-at-Risk (VaR) calculations and correlation tracking between positions.
3. **Automatic position reduction.** Scale down position sizes as total exposure increases.
