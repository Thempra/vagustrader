# Architecture Overview

## High-Level Design

```
┌──────────────────────────────────────────────────────────────┐
│                   cmd/vagustrader/main.go                    │
│                    Cobra CLI — 7 commands                     │
└──────────┬──────────────────────────────────┬────────────────┘
           │                                  │
    ┌──────▼────────┐                ┌────────▼────────┐
    │ cfg.LoadSettings│               │ cfg.LoadAppConfig│
    │ (env vars)      │               │ (config.yml)     │
    └──────┬─────────┘                └────────┬─────────┘
           │                                   │
           └──────────────┬────────────────────┘
                          │
                   ┌──────▼──────┐
                   │   Engine    │
                   │  (errgroup  │
                   │  + graceful │
                   │  shutdown)  │
                   └──────┬──────┘
                          │
         ┌────────────────┼─────────────────┐
         │                │                  │
  ┌──────▼──────┐  ┌─────▼──────┐   ┌───────▼──────────┐
  │  CopyTrading │  │ Settlement │   │  (future         │
  │  Strategy    │  │ Strategy   │   │   strategies)    │
  └──────┬───────┘  └─────┬──────┘   └──────────────────┘
         │                 │
    ┌────┼────┐            │
    │    │    │            │
    ▼    ▼    ▼            │
 ┌────┐┌────┐┌────────┐   │
 │CLOB││Data││ Gamma  │   │
 │Clnt││ API││ Clnt   │   │
 └─┬──┘└────┘└────────┘   │
   │                       │
   ├── Signer (EIP-712)   │
   ├── HMAC auth          │
   └── PaperClient (sim)  │
                          │
┌─────────┐ ┌──────────┐  │
│  DB     │ │Dashboard │  │
│ (SQLite)│ │ (HTTP)   │  │
└─────────┘ └──────────┘  │
┌──────────┐              │
│ Notifier │              │
│(Telegram)│              │
└──────────┘              │
┌──────────┐              │
│ Logging  │              │
│ (slog)   │              │
└──────────┘              │
```

## Module Breakdown

### `cmd/vagustrader/main.go` — Entry Point

- Cobra CLI with 7 commands: `run`, `status`, `markets`, `top-traders`, `dashboard`, `derive-creds`, `simulate-settlement`
- The `run` command wires up all dependencies: logging → config → DB → clients → strategies → engine

### `internal/cfg/` — Configuration

Two configuration sources merged at startup:

| Source | Format | Loader |
|---|---|---|
| Environment variables | Key=Value | `cfg.LoadSettings()` |
| YAML file | config.yml | `cfg.LoadAppConfig(path)` |

`Settings` struct holds all env-based config (keys, URLs, Telegram tokens).  
`AppConfig` struct holds YAML-based config (risk limits, strategy parameters, market filters).

### `internal/engine/` — Orchestrator

- `Engine.RunWithGracefulShutdown()` creates a cancellable context, handles SIGINT/SIGTERM
- `Engine.Start()` initializes DB connection, resolves CLOB credentials, creates clients, launches all strategies via `errgroup`
- Each strategy runs in its own goroutine; engine exits when any strategy returns or context is cancelled

### `internal/strategy/` — Strategy Interface

```go
type Strategy interface {
    Name() string
    Run(ctx context.Context, sctx *Context) error
}
```

`Context` provides strategies access to all shared dependencies:
- `Clob` (CLOB client interface)
- `Gamma` (market metadata)
- `DataAPI` (leaderboard, positions)
- `DB` (persistence)
- `Config` (app config)
- `Notifier` (Telegram)
- `Paper` (mode flag)

### `internal/strategy/copytrading/` — Core Strategy

Two-phase tick cycle:

```
Phase 1: SCAN
  │
  ├── Fetch top traders from Data API (filtered by min_pnl + min_volume)
  ├── For each trader → fetch their positions
  └── Deduplicate candidates by token_id

Phase 2: COPY
  │
  ├── For each candidate position:
  │   ├── Skip if dead token (1h TTL)
  │   ├── Skip if already held
  │   ├── Verify market is active on CLOB
  │   ├── Verify price within guard (5% default)
  │   ├── Verify within max_days_to_resolution
  │   ├── Determine buy price from CLOB data or orderbook
  │   └── Place BUY limit order
  ├── Record trade in DB
  ├── Upsert position (weighted average blend)
  └── Log tick summary (opened, held, dead, skipped)
```

### `internal/strategy/settlement/` — Settlement Checker

- Runs every 300 seconds
- For each open position, checks the CLOB market data for resolved status
- If resolved, records `result` on matching trades, updates PnL snapshot, sends Telegram notification

### `internal/client/` — External API Clients

| Client | Package | APIs Used |
|---|---|---|
| CLOB Client | `clob` | Orderbook, limit orders, balance, open orders, cancel, market data, API key derivation |
| Gamma Client | `gamma` | Market listing, metadata by token ID |
| Data API Client | `dataapi` | Leaderboard, trader positions, activity feed |
| WebSocket Feed | `ws` | Real-time price updates per token |

### `internal/client/clob/` — CLOB Client Details

- `Client` interface with 5 methods (GetOrderbook, PlaceLimitOrder, GetBalance, GetOpenOrders, CancelOrder)
- `ClobClient` — real implementation with authenticated REST calls
- `PaperClient` — in-memory simulation (no real capital), implements same interface
- `Signer` — EIP-712 typed data signing for auth and orders
- HMAC-SHA256 request signing for authenticated endpoints

### `internal/db/` — Persistence

SQLite with 4 tables:

```
market_meta      ← condition_id, question, slug (market metadata cache)
trades           ← trade history with results
positions        ← current open positions (weighted average priced)
pnl_snapshots    ← daily PnL summaries
```

Connection uses WAL mode for concurrent read/write from bot and dashboard processes.

### `internal/dashboard/` — Web UI

- HTTP server with embedded static assets (via `//go:embed`)
- Background cache refresh every 30 seconds for sub-millisecond response times
- Parallel token price fetching with 12-goroutine semaphore
- CSV export of positions
- Two-panel layout: open positions + balance history
- PnL history chart (last 7 days)
- Source traders popup

### `internal/notify/` — Notifications

- Telegram bot integration via HTTP API
- Returns nil notifier when credentials are absent (graceful degradation)

### `internal/logging/` — Structured Logging

- `slog.JSONHandler` outputting to stderr + rotating file
- 10MB per file, 5 backup files
- Log directory: `logs/`

## Data Flow: Copy Trade

```
Data API          CLOB             Bot              DB              Dashboard
  │                │                │                │                  │
  │──leaderboard──▶│                │                │                  │
  │──positions────▶│                │                │                  │
  │                │──market data──▶│                │                  │
  │                │──orderbook────▶│                │                  │
  │                │                │──insert trade──▶                  │
  │                │                │──upsert pos────▶                  │
  │                │                │──upsert meta────▶                 │
  │                │                │                │                  │
  │                │                │                │──fetch every 30s─▶
  │                │                │                │                  │──render HTML
```

## Key Design Decisions

| Decision | Rationale |
|---|---|
| **Go over Python** | Performance, single binary, native EIP-712 signing, goroutine concurrency |
| **SQLite WAL mode** | Concurrent readers without locks; bot writes, dashboard reads simultaneously |
| **Background cache** | Dashboard serves cached HTML (<1ms) instead of querying DB per request |
| **Paper/Live via interface** | `PaperClient` implements same `Client` interface as `ClobClient` — zero strategy code changes for mode switching |
| **errgroup for strategies** | Clean goroutine lifecycle: if any strategy fails, all are cancelled |
| **EIP-712 native signing** | Pure Go implementation with go-ethereum, no Python sidecar |
| **Template+CSS embedded** | Single binary deployment — no external static files needed |
