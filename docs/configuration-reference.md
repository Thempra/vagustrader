# Configuration Reference

VagusTrader uses two configuration sources merged at startup:

| Source | File | Env Prefix | Purpose |
|---|---|---|---|
| YAML | `config.yml` | None | Trading strategy, risk, market filtering |
| Environment | `.env` | (see below) | Secrets, API URLs, mode flags |

---

## Environment Variables (`cfg.LoadSettings()`)

| Variable | Required | Default | Description |
|---|---|---|---|
| `PRIVATE_KEY` | Yes* | — | Ethereum private key (hex, with or without 0x prefix). Required for live trading. |
| `CLOB_API_KEY` | No | `""` | Polymarket CLOB API key. Derived from private key if empty. |
| `CLOB_API_SECRET` | No | `""` | CLOB API secret (base64). Derived from private key if empty. |
| `CLOB_API_PASSPHRASE` | No | `""` | CLOB API passphrase. Derived from private key if empty. |
| `GAMMA_API_URL` | No | `https://gamma-api.polymarket.com` | Gamma Markets API base URL. |
| `DATA_API_URL` | No | `https://data-api.polymarket.com` | Polymarket Data API base URL. |
| `CLOB_HOST` | No | `https://clob.polymarket.com` | CLOB API host. |
| `CHAIN_ID` | No | `137` | Polygon chain ID (137 = mainnet). |
| `TELEGRAM_BOT_TOKEN` | No | `""` | Telegram bot token for notifications. Notifications disabled when empty. |
| `TELEGRAM_CHANNEL_ID` | No | `""` | Telegram channel/chat ID for notifications. |

> `*` Paper trading does not require `PRIVATE_KEY`. Derive-only operations (e.g. `derive-creds` command) also require it.

---

## YAML Config (`config.yml`)

### `risk` — Risk Controls

```yaml
risk:
  max_position_usdc: 50.0
  max_open_positions: 2000
  daily_loss_limit_usdc: 200.0
```

| Field | Default | Description |
|---|---|---|
| `max_position_usdc` | `50.0` | Maximum USDC size per individual position. |
| `max_open_positions` | `10` | Maximum number of concurrent open positions. |
| `daily_loss_limit_usdc` | `200.0` | Daily loss limit (configured but **not enforced** at runtime). |

### `strategies.copy_trading` — Copy Trading Strategy

```yaml
strategies:
  copy_trading:
    enabled: true
    poll_interval_seconds: 120
    min_pnl: 5000
    min_volume_usd: 1000
    price_guard: 0.05
    max_copy_per_trader: 5
    max_days_to_resolution: 7
```

| Field | Default | Description |
|---|---|---|
| `enabled` | `true` | Enable/disable copy trading strategy. |
| `poll_interval_seconds` | `300` | Seconds between copy-trading ticks (how often to scan traders and copy positions). |
| `min_pnl` | `10000` | Minimum PnL (USDC) for a trader to be considered on the leaderboard. |
| `min_volume_usd` | `10000` | Minimum trading volume (USDC) for a trader to be considered. |
| `price_guard` | `0.05` | Maximum acceptable price deviation (as fraction of 1.0). E.g. `0.05` = 5%. Skips positions where the buy price differs from the trader's entry price by more than this. |
| `max_copy_per_trader` | `3` | Maximum number of positions to copy from a single trader per tick. |
| `max_days_to_resolution` | `0` | Maximum days until market resolution. `0` = no limit. Filters out markets resolving too far in the future. |

### `markets` — Market Filters

```yaml
markets:
  min_liquidity_usdc: 1000
```

| Field | Default | Description |
|---|---|---|
| `min_liquidity_usdc` | `5000` | Minimum CLOB liquidity (USDC) for a market to be tradeable. |

---

## Full Example

**`config.yml`:**

```yaml
risk:
  max_position_usdc: 50.0
  max_open_positions: 2000
  daily_loss_limit_usdc: 200.0

strategies:
  copy_trading:
    enabled: true
    poll_interval_seconds: 120
    min_pnl: 5000
    min_volume_usd: 1000
    price_guard: 0.05
    max_copy_per_trader: 5
    max_days_to_resolution: 7

markets:
  min_liquidity_usdc: 1000
```

**`.env`:**

```env
PRIVATE_KEY=0x1234567890abcdef...
CLOB_API_KEY=
CLOB_API_SECRET=
CLOB_API_PASSPHRASE=
TELEGRAM_BOT_TOKEN=
TELEGRAM_CHANNEL_ID=
```

If CLOB credentials are left empty, the bot derives them from `PRIVATE_KEY` on first startup via the `POST /auth/api-key` endpoint.
