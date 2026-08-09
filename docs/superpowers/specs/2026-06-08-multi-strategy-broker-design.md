# Multi-Strategy Broker Architecture

**Date**: 2026-06-08
**Status**: Approved

## Problem

VagusTrader has a single trading strategy (copy trading) with execution logic embedded in the strategy itself. Adding new strategies requires duplicating ~80 lines of order execution, fill verification, trade recording, and position management.

## Solution

Introduce a **Broker** layer between strategies and infrastructure. The Broker centralizes all trading operations. Strategies only decide *what* and *how much* to buy — the Broker handles *how*.

## Constraints

- Paper mode: multiple trading strategies can run simultaneously
- Live mode: only 1 trading strategy allowed (enforced at startup)
- Settlement always runs in the background
- Backwards-compatible config format

## Architecture

```
config.yml → Registry → Engine
                          │
                          ▼
                        Broker (shared infrastructure)
                          │
                    ┌─────┼─────┐
                    ▼     ▼     ▼
               CopyTrad  Settle  (future strategies)
```

## New Components

### Broker (`internal/broker/`)

Single entry point for all trading infrastructure:

- `Buy(ctx, BuyRequest) (*TradeResult, error)` — full execution pipeline
- `GetMarketData(conditionID)` — cached market fetch
- `GetPositions()` — open positions from DB
- `IsHeld(tokenID)` — duplicate check
- `GetBalance()` — available USDC
- `GetDataAPI()` — raw data-api access
- `GetConfig()` — app config access
- `IsPaper()` — mode check
- `Notify(msg)` — telegram notification

### Registry (`internal/strategy/registry.go`)

Maps strategy names to constructors. `main.go` iterates enabled strategies from config and instantiates via registry.

## File Changes

| File | Action | Description |
|---|---|---|
| `internal/broker/broker.go` | New | Broker struct, helpers |
| `internal/broker/buy.go` | New | BuyRequest, TradeResult, Buy() execution |
| `internal/strategy/registry.go` | New | Name → factory mapping |
| `internal/strategy/strategy.go` | Modify | Strategy.Run() receives *Broker |
| `internal/strategy/copytrading/copytrading.go` | Refactor | Use Broker.Buy() |
| `internal/strategy/settlement/settlement.go` | Modify | Use Broker.GetMarketData() |
| `internal/engine/engine.go` | Modify | Create Broker, enforce live mode |
| `cmd/vagustrader/main.go` | Modify | Use Registry |
| `internal/cfg/cfg.go` | Modify | SettlementConfig |

## Config Format

```yaml
strategies:
  copy_trading:
    enabled: true
    # ... strategy-specific config
  settlement:
    enabled: true
    interval_seconds: 300
```

Adding a new strategy = adding a new section + registering in `registry.go`.

## Live Mode Enforcement

Engine counts non-settlement strategies. If >1 and `!paper`, returns error at startup.
