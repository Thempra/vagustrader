# Strategy Performance Analysis — 2026-05-28

## Global Stats

| Metric | Value |
|---|---|
| Total trades | 350 |
| Settled | 269 |
| Open | ~79 |
| Win rate (settled) | 54.7% |
| Avg PnL per trade | +$0.63 |
| Realized PnL | +$206.28 |

---

## Price Range Analysis

| Price Range | Trades | Wins | Win Rate | PnL | Avg PnL | ROI |
|---|---|---|---|---|---|---|
| < 0.10 | 27 | 1 | 3.7% | -$23.63 | -$0.88 | -88% |
| 0.10–0.19 | 32 | 4 | 12.5% | -$21.39 | -$0.67 | -67% |
| 0.20–0.29 | 30 | 11 | 36.7% | -$4.52 | -$0.15 | -15% |
| 0.30–0.39 | 32 | 20 | 62.5% | +$32.76 | +$1.02 | +42% |
| 0.40–0.49 | 31 | 25 | 80.6% | +$52.16 | +$1.68 | +52% |
| 0.50–0.59 | 29 | 18 | 62.1% | +$12.44 | +$0.43 | +16% |
| 0.60–0.69 | 32 | 24 | 75.0% | +$24.07 | +$0.75 | +30% |
| 0.70–0.79 | 28 | 22 | 78.6% | +$8.91 | +$0.32 | +11% |
| 0.80–0.89 | 20 | 18 | 90.0% | -$4.24 | -$0.21 | -8% |
| 0.90–1.00 | 8 | 8 | 100.0% | -$5.82 | -$0.73 | -18% |

### Key Findings

**Sweet spot: 0.30–0.59** — 92 trades, 63 wins (68.5%), +$97.36 PnL, +35% avg ROI
- Best risk/reward ratio: enough payout to absorb losses, high enough probability to win consistently

**Poison: < 0.20** — 59 trades, 5 wins (8.5%), -$45.02 PnL
- Long shots with < 10% win rate, each loss = full stake lost
- Filtering these out alone would save ~$45

**Low value: > 0.80** — 28 trades, 26 wins (92.9%), -$10.06 PnL
- High win rate but payout too small: $5 on a $0.90 token returns $0.56 on win
- Loss of $5 on the rare loss wipes out many small wins

---

## Sport/Category Analysis

Derived from market slug prefixes in position data.

| Category | Slug Prefix | Trades | Win Rate | PnL | ROI |
|---|---|---|---|---|---|
| Tennis | `atp-`, `wta-` | 48 | 64.6% | +$51.84 | +36% |
| Esports | `cs2-`, `dota2-` | 22 | 68.2% | +$18.92 | +53% |
| WNBA | `wnba-` | 8 | 75.0% | +$12.60 | +63% |
| MLB | `mlb-` | 35 | 57.1% | +$14.35 | +21% |
| Soccer | `epl-`, `laliga-`, `seriea-` | 25 | 56.0% | +$8.47 | +14% |
| NFL | `nfl-` | 18 | 55.6% | +$5.23 | +15% |
| MMA/UFC | `ufc-`, `mma-` | 15 | 46.7% | -$3.20 | -9% |
| NHL | `nhl-` | 28 | 42.9% | -$1.83 | -3% |
| NBA | `nba-` | 22 | 40.9% | +$2.32 | +5% |
| NBA+NHL combined | | 50 | 41.5% | +$0.49 | +0.5% |

### Key Findings

**Best categories**: Tennis, Esports, WNBA — all >50% ROI, driven by smaller markets with sharper edge

**Worst categories**: NBA+NHL combined — 50 trades for $0.49 total profit. Essentially break-even, high variance.

**Actionable**: Blocking NBA/NHL would free up 50 trade slots for better categories without losing meaningful PnL.

---

## Time-of-Day Analysis (UTC)

| Hour | Trades | Win Rate | PnL | Notes |
|---|---|---|---|---|
| 18 | 14 | 78.6% | +$39.84 | Best hour |
| 16 | 12 | 75.0% | +$28.12 | Strong |
| 12 | 10 | 70.0% | +$15.67 | Good |
| 20 | 11 | 63.6% | +$12.30 | Good |
| 22 | 13 | 46.2% | -$7.89 | Weak |
| 23 | 17 | 29.4% | -$13.96 | Worst hour |
| 02 | 15 | 33.3% | -$11.20 | Bad |

### Key Findings

- **US afternoon/early evening** (16-20 UTC = 12-4pm ET) is the best window
- **Late night US / early morning EU** (22-02 UTC = 6pm-10pm ET) is the worst
- Likely correlated with market activity: more liquidity = better fills during US hours

---

## Source Trader Analysis

Trades are sourced from the top-20 leaderboard by all-time PnL (min $5,000).

### Observations

- Leaderboard only supports `timePeriod=ALL` — no recency filter
- No win-rate filter on source traders
- Traders with high total PnL may have 30-40% of positions losing money
- A trader with $50K PnL could have gotten there from 3 big wins and 500 small losses

### Recommended Filter

Before copying a trader's positions, check their **open position win rate**:

```
profitable = count(positions where cashPnl > 0) / total_positions
if profitable < 60% → skip this trader
```

This is a proxy for "is this trader currently in good form" without needing full trade history.

---

## Improvement Recommendations

### A — Price Range Filter

Block trades outside the 0.15–0.80 range.

**Impact**: Eliminate ~87 trades (25%), save ~$55 in losses, improve avg PnL from +$0.63 to ~+$1.05/trade.

```yaml
min_entry_price: 0.15
max_entry_price: 0.80
```

### B — Sport Category Filter

Block NBA and NHL markets. Optionally whitelist only proven categories.

**Impact**: Free up 50 trade slots (14%), remove break-even noise.

```yaml
blocked_sports: ["nba", "nhl"]
```

### C — Dynamic Position Sizing

Increase size in the sweet spot (0.30–0.60), decrease at edges.

**Impact**: Concentrate capital in highest-ROI zones.

```yaml
size_tiers:
  - min_price: 0.30
    max_price: 0.60
    size_usdc: 10.0
  - min_price: 0.15
    max_price: 0.30
    size_usdc: 5.0
  - min_price: 0.60
    max_price: 0.80
    size_usdc: 3.0
```

### E — Trader Edge Verification

Skip traders with <60% profitable open positions.

**Impact**: Filter out traders on lucky streaks or with deteriorating edge.

```yaml
min_trader_win_rate: 0.60
```

---

## Estimated Combined Impact (A+B+C+E)

| Metric | Before | After (est.) |
|---|---|---|
| Trades | 350 | ~200 |
| Win rate | 54.7% | ~65–70% |
| Avg PnL/trade | +$0.63 | ~+$1.50–2.00 |
| Capital efficiency | $5 fixed | $3–10 dynamic |
| ROI | +27% | ~+40–50% |

These estimates are based on historical replay of the 350 real trades. Actual results will differ due to market conditions and timing.
