# VagusTrader — TRD (Technical Requirements)

> Author of: stack, libraries, external APIs/endpoints, wallet & signing, infra/deploy, network constraints, testing/deploy recipes.

## 1. Stack

| Layer | Choice | Notes |
|---|---|---|
| Language | Go (single module) | ~10.7k LOC excl. tests; `go.mod` at repo root |
| CLI | Cobra | `cmd/vagustrader`, 9 commands |
| Persistence | SQLite via modernc/go-sqlite3-style driver, **WAL mode** | file `/app/data/vagustrader.db` on named volume |
| HTTP server | stdlib `net/http` + `html/template` | dashboard; static assets embedded (`embed`) |
| Signing | EIP-712 typed orders (Polymarket CLOB schema) | private key → EOA signer |
| Logging | `log/slog`, JSON to `/app/logs` volume | |
| Concurrency | `golang.org/x/sync/errgroup`, context-based graceful shutdown | engine orchestrates strategies |
| Packaging | Multi-stage Dockerfile, non-root `vagus` uid=100:101 | multi-arch |
| CI/Quality | SonarQube at `sast.thempra.net` (`sonar-project.properties`), coverage profile refreshed in-repo | |

Repo: Forgejo `origin` = `code.thempra.net/research/vagustrader.git` (master);
GitHub mirror `github` = `thempra/vagustrader` (Coolify builds from the GitHub
SSH URL, which mirrors Forgejo — pushing to origin is enough).

## 2. Package layout (blueprint)

```
cmd/vagustrader/        Cobra commands
internal/cfg/           env vars (Settings) + config.yml / config.live.yml (AppConfig)
internal/db/            SQLite schema + migrations + queries
internal/client/clob/   CLOB REST + EIP-712 order signing + HMAC L1/L2 auth + PaperClient
internal/client/gamma/  Gamma markets API client
internal/client/dataapi/ Data-API client (leaderboard, positions, trades, FindRecentFill)
internal/client/ws/     gorilla/websocket market feed (clob ws-subscriptions)
internal/broker/        order routing, fill verify, position/trade recording, risk gates
internal/engine/        orchestrator; paper/live wiring; live 1-strategy rule
internal/strategy/*/    one package per strategy + kelly (sizing lib) + settlement
internal/dashboard/     handlers + cached HTML + embedded static/
internal/notify/        Telegram bot API
internal/logging/       slog setup
```

## 3. External APIs

| API | Base | Used for | Auth | Quirks |
|---|---|---|---|---|
| CLOB | `https://clob.polymarket.com` | place/cancel orders, orderbook, prices, `derive-creds`, balances | L1 (EIP-712) → L2 HMAC key/passphrase headers | Authed `GET /order/{id}` **404s for L1-derived keys without 1271 binding** — never trust it alone for fill verification. Health probe path `/ok`. |
| Gamma | `https://gamma-api.polymarket.com` | open markets discovery, metadata, slugs, resolution status | none | Paginate; filter by liquidity/close date client-side |
| Data-API | `https://data-api.polymarket.com` | leaderboard, `/positions?user=`, `/trades?user=`, `/value?user=`, `/activity`; live fill verification | none | CDN 403s default Go/Python UAs → always send custom `User-Agent` |
| WS | `wss://ws-subscriptions-clob.polymarket.com` | live price book updates | none | gorilla/websocket ignores `HTTPS_PROXY` (no patch) |
| Polygon RPC | public RPC | `balanceOf` on-chain USDC via eth_call | none | collateral truth (PRD F5) |
| Telegram Bot API | api.telegram.org | notifications | bot token env | |

**Tokens (collateral contracts, Polygon chainId 137):** native USDC
`0x3c499c542cEF5E3811e1192ce70d8cC03d5c3359`, bridged USDC
`0x2791Bca1f2de4661ED88A30C99A7a9449Aa84174`. Spendable balance = sum of both
for the deposit wallet. **Never** use data-api portfolio value as balance.

## 4. Wallet & signing model

- Signer EOA: `0xd8d9ADa9...` (PRIVATE_KEY env). Trading account: Polymarket
  **proxy wallet** `0x2ed0ef344e96fc0e2e2fb3622474715f92d93173` (POLY_1271
  signature type; funds live here, orders signed by EOA on behalf of proxy).
- CLOB L2 creds: env-provided or `derive-creds` (L1 signature → key/secret/passphrase).
- Secrets via env vars only (Coolify secrets / .env): `PRIVATE_KEY`,
  `CLOB_API_KEY/SECRET/PASSPHRASE`, `CLOB_ADDRESS`, `CLOB_DEPOSIT_WALLET`,
  `TELEGRAM_BOT_TOKEN`, `TELEGRAM_CHANNEL_ID`. Never in git/config YAML.

## 5. Configuration

- `config.yml` (dev/paper) vs `config.live.yml` (production live). Same schema;
  env-var overrides per key (cfg layering: defaults ← YAML ← env).
- Live deployment mounts `config.live.yml`; it pins strategies to
  `contrarian` + `settlement` only. Engine re-checks the 1-strategy rule.
- Reference keys: `docs/configuration-reference.md`; risk semantics: PRD §4.3.

## 6. Infrastructure & deployment

- Host: "Nuke" (192.168.50.5) under **Coolify** (`orchestrator.thempra.net`),
  two Docker-Compose apps sharing pattern `<uuid>_data` + `<uuid>_logs` volumes:
  - Paper/dev: app `hdlvr3ovsw9gr59rog3j13dz`, bot + dashboard, dashboard :18080.
  - Live: app `euhd7rtcnic6vn3bxxcrwclx`, `docker-compose.live.yml`,
    **auto-deploy OFF**, manual start only (post-drawdown policy).
- Deploy via API: `POST https://orchestrator.thempra.net/api/v1/deploy?uuid=<uuid>`
  (+`&restart_only=true` to skip rebuild), `Authorization: Bearer $(cat ~/.coolify_token)`;
  poll `GET /api/v1/deployments/<id>` (FLAT JSON). Full build ≈ 5 min.
- Volume ownership pitfall: image is uid 100:101 but volumes can revert to
  root → `attempt to write a readonly database`. Fix:
  `docker run --rm -v <uuid>_data:/d alpine chown -R 100:101 /d` then restart.

## 7. Network egress constraints (hard-won; do not regress)

- Upstream ISP **selectively resets TLS** to certain Cloudflare IPs serving
  `clob/gamma-api/data-api/ws-relayer` hostnames (SNI-/IP-targeted, not DNS).
- Fix (commit 1918b29): `x-polymarket-hosts` YAML anchor → `extra_hosts` pinning
  `clob, gamma-api, data-api, ws-subscriptions-clob, www.clob, relayer-v2` →
  healthy Cloudflare IP `104.17.207.5`, in BOTH compose files.
  - Pitfall 1: `extra_hosts` only applies to **recreated** containers — verify
    `docker exec <bot> grep polym /etc/hosts` after any reset storm.
  - Pitfall 2: PoP IPs rotate; re-probe (`curl --resolve host:443:<ip>
    https://host/ok`) and bump the anchor if resets return.
  - Decision: bots do **not** use WARP/SOCKS5 (websocket proxy blindness,
    datacenter-egress risk-flag profile, SPOF). WARP lives only on the Hermes
    ops box.
- Reachability test after redeploy: `docker exec <bot> wget -qO-
  --header='User-Agent: Mozilla/5.0' https://clob.polymarket.com/ok`.

## 8. Performance & correctness constraints

- Any query touching positions must use `idx_trades_token_ts ON
  trades(token_id, ts)` — the latest-source-per-token correlated subquery scans
  the whole trades table otherwise (caused 121% CPU dashboard incident, 2131
  positions → 3 min/refresh). Verify with `EXPLAIN QUERY PLAN` on a copy of the live DB.
- Fill verification path: authed order lookup → on 404/miss, `dataapi.FindRecentFill()`
  (token + timestamp window); still unverified → cancel the resting GTC order and
  do NOT record. (Bug class that silently dropped every live fill.)
- Balance: on-chain USDC only (see §3/§4). Portfolio value ≠ spendable.
- SQLite writes are serialized through `internal/db`; WAL mode; migrations are
  table-rebuild style (create_new → copy → drop → rename), keep idempotent.

## 9. Testing & verification

- Unit tests colocated (`*_test.go`); coverage profile committed (`coverage.out`),
  Sonar gate at sast.thempra.net.
- `simulate-settlement` CLI exercises 1 WIN + 1 LOSS path against local DB.
- API-path changes get a `go run` probe against a known historical fill before
  any live restart.
- Dashboard schema changes: copy live `.db` off the volume, run queries +
  `EXPLAIN QUERY PLAN` locally with realistic row counts.
