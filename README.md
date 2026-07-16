# VagusTrader

Automated Polymarket copy-trading bot written in Go. Follows the most profitable traders and replicates their positions.

## Architecture

```
cmd/vagustrader/main.go     CLI entrypoint (Cobra)
internal/
  cfg/                       Config loading (env vars + YAML)
  db/                        SQLite persistence (WAL mode)
  client/
    clob/                    Polymarket CLOB client (EIP-712 signing)
    gamma/                   Gamma Markets REST API
    dataapi/                 Data API (leaderboard, positions)
    ws/                      WebSocket market feed
  strategy/
    copytrading/             Copy trading strategy
    settlement/              Settlement checker (auto-resolve closed markets)
  engine/                    Orchestrator (errgroup, graceful shutdown)
  dashboard/                 HTTP dashboard (embedded static files, CSV export)
  notify/                    Telegram notifications
```

## Quick Start

```bash
# Configure
cp .env.example .env
# Edit .env with your PRIVATE_KEY, CLOB_API_KEY, CLOB_API_SECRET, CLOB_API_PASSPHRASE
cp config.yml.example config.yml
# Edit config.yml with your strategy parameters

# Build and run (paper mode)
docker compose up -d --build

# Or use make
make deploy        # build + start
make paper         # run in paper mode
make stop          # stop all
```

## CLI Commands

```bash
vagustrader run --paper              # Start bot in paper trading mode
vagustrader run                      # Start bot in live mode
vagustrader dashboard --port 8080    # Start web dashboard
vagustrader status --db data/vagustrader.db
vagustrader markets --min-vol 10000
vagustrader top-traders --min-pnl 10000
vagustrader derive-creds             # Derive CLOB API credentials
vagustrader simulate-settlement      # Simulate 1 WIN + 1 LOSS (testing)
```

## Configuration

### Environment Variables (.env)

| Variable | Required | Description |
|----------|----------|-------------|
| `PRIVATE_KEY` | Yes | Ethereum private key (0x prefix) |
| `CLOB_API_KEY` | Yes | Polymarket CLOB API key |
| `CLOB_API_SECRET` | Yes | Polymarket CLOB API secret |
| `CLOB_API_PASSPHRASE` | Yes | Polymarket CLOB API passphrase |
| `CLOB_HOST` | No | CLOB API host (default: https://clob.polymarket.com) |
| `CHAIN_ID` | No | Chain ID (137 for Polygon) |
| `TELEGRAM_BOT_TOKEN` | No | Telegram bot token for notifications |
| `TELEGRAM_CHAT_ID` | No | Telegram chat ID for notifications |

### config.yml

```yaml
risk:
  max_position_usdc: 50
  max_open_positions: 10
  daily_loss_limit_usdc: 200

strategies:
  copy_trading:
    enabled: true
    poll_interval_seconds: 300
    min_pnl: 10000
    max_copy_per_trader: 3
    price_guard: 0.05
    max_days_to_resolution: 30

dashboard:
  port: 8080
```

## Database

SQLite with WAL mode. Tables:

- **trades**: All executed trades (price, size, side, paper/live, result)
- **positions**: Open positions (shares, weighted avg price)
- **market_meta**: Market metadata cache (question, slug)
- **pnl_snapshots**: Daily realized PnL snapshots

## Accounting Model

- `positions.avg_price` uses weighted average when adding to existing positions.
- Per-trade PnL: `gain = (size_usdc / price) * result - size_usdc`
- `trades.result`: NULL = open, 0.0 = LOSS, 1.0 = WIN
- Dashboard Realized PnL sums all snapshots (no time cap).

## Dashboard

Web UI at `http://localhost:8080` with:
- KPI header: positions count, total PnL (realized + unrealized), today stats
- Open positions table with live prices (fetched in parallel, cached 30s)
- Trade history (Balances) with per-trade gain/loss
- PnL history footer (last 7 days)
- CSV export at `/csv`

Response time: ~1ms (background cache refreshes every 30s).

## Docker

```bash
docker compose up -d              # Start bot + dashboard
docker compose logs -f bot        # Watch bot logs
docker compose exec bot vagustrader status
docker compose exec bot vagustrader simulate-settlement
```

## Dependencies

- Go 1.22
- [go-ethereum](https://github.com/ethereum/go-ethereum) v1.13.15 — EIP-712 signing
- [gorilla/websocket](https://github.com/gorilla/websocket) v1.5.3 — market feed
- [mattn/go-sqlite3](https://github.com/mattn/go-sqlite3) v1.14.22 — CGo SQLite
- [spf13/cobra](https://github.com/spf13/cobra) v1.8.1 — CLI framework
- [golang.org/x/sync](https://pkg.go.dev/golang.org/x/sync) v0.6.0 — errgroup
- [gopkg.in/yaml.v3](https://gopkg.in/yaml.v3) v3.0.1 — config parsing
