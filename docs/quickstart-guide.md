# Quickstart Guide

## Prerequisites

- Go 1.22+
- Docker & Docker Compose (for production deployment)
- Polymarket account with API credentials

## Local Development Setup

### 1. Clone and Build

```bash
git clone git@github.com:Thempra/vagustrader.git
cd vagustrader
docker compose build
```

Or build natively:

```bash
go build -o vagustrader ./cmd/vagustrader
```

### 2. Configure

Copy the example files and fill in your settings:

```bash
cp config.yml.example config.yml
cp .env.example .env
```

**Minimal `.env` for paper trading:**

```env
PRIVATE_KEY=0x...
CLOB_API_KEY=
CLOB_API_SECRET=
CLOB_API_PASSPHRASE=
```

If `CLOB_API_KEY`/`SECRET`/`PASSPHRASE` are empty, the bot derives them from the private key on startup.

**Optional:**

```env
TELEGRAM_BOT_TOKEN=...
TELEGRAM_CHANNEL_ID=...
```

### 3. Run in Paper Mode

```bash
docker compose up -d bot
```

Or with Make:

```bash
make paper
```

### 4. Start Dashboard

```bash
docker compose up -d dashboard
make dashboard
```

Open [http://localhost:18080](http://localhost:18080)

## CLI Commands

### `vagustrader run`

Starts the trading engine.

```bash
vagustrader run                    # live trading
vagustrader run --paper            # paper trading (default)
vagustrader run --config custom.yml
```

### `vagustrader dashboard`

Starts the web dashboard.

```bash
vagustrader dashboard              # default port 8080
vagustrader dashboard --port 9090
```

### `vagustrader status`

Shows open positions and today's trade count.

### `vagustrader markets`

Lists tradeable markets from Polymarket.

```bash
vagustrader markets --min-vol 50000 --limit 10
```

### `vagustrader top-traders`

Lists top traders from Polymarket leaderboard.

```bash
vagustrader top-traders --min-pnl 5000 --limit 20
```

### `vagustrader derive-creds`

Derives CLOB API credentials from a private key. Useful for setting up the `.env` file.

### `vagustrader simulate-settlement`

Simulates one WIN and one LOSS settlement for testing accounting. Requires existing open positions in the database.

```bash
vagustrader simulate-settlement
```

## Makefile Targets

| Target | Command |
|---|---|
| `make build` | `docker compose build` |
| `make run` | `docker compose up -d bot` |
| `make paper` | `docker compose run --rm bot vagustrader run --paper` |
| `make dashboard` | `docker compose up -d dashboard` |
| `make deploy` | `docker compose up -d --build` |
| `make stop` | `docker compose down` |
| `make status` | `docker compose exec bot vagustrader status` |
| `make simulate-settlement` | `docker compose exec bot vagustrader simulate-settlement` |

## Verify It's Working

1. Open dashboard: [http://localhost:18080](http://localhost:18080)
2. Check "Positions" panel for active positions being copied.
3. Check "Balances" panel for trade history.
4. After settlements, PnL will update in the header KPIs and the PnL history table.

## Database

SQLite database stored at `./data/vagustrader.db` (WAL mode). Shared between bot and dashboard containers via Docker volume.

```bash
# Query from host (requires Go):
go run -exec '' -mod=mod github.com/mattn/go-sqlite3/cmd/sqlite3 ./data/vagustrader.db "SELECT COUNT(*) FROM trades;"
```
