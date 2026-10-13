# VagusTrader — Implementation Plan

> Author of: phase sequence + acceptance criteria ONLY. Rationale → PRD, technical choices → TRD.
> Status: retrofit (2026-10-14). Phases P0–P6 are BUILT and shipped (criteria = what
> was actually verified); P7+ is the forward sequence to a safe live restart.
> Build strictly in this order; any deviation updates the owning doc in the same commit.

## Legend
✅ done & verified · 🔒 hard prerequisite of the next phase

## P0 — Foundations ✅
Repo, Go module, Cobra skeleton, cfg layering (env > YAML > defaults), slog,
SQLite open+migrations, Dockerfile (non-root 100:101), compose files.
**AC:** `make paper` boots to "engine started" with empty account creds; DB
file + volumes created; container runs as uid 100.

## P1 — Market/data clients ✅
gamma, dataapi (custom UA!), clob (L1/L2 + EIP-712 + derive-creds), ws feed,
PaperClient. **AC:** `markets`, `top-traders`, `derive-creds` return real data
from a laptop and from inside the container (after TRD §7 pins).

## P2 — Broker + accounting core ✅
Risk gates, order path, fill verification + `FindRecentFill` fallback +
cancel-on-unverified (fix fb6194c), weighted-avg positions, trades recording
with `schema_version`, paper wallets, on-chain USDC balance sync (fix 907801a).
**AC:** AC1/AC2/AC4 of PRD §7; live DB shows every portal fill; balance
matches eth_call sum of both USDC contracts.

## P3 — Strategies ✅
settlement first (simulate-settlement green), then copy_trading, then the
paper-only experimental set + kelly lib; live single-strategy engine rule.
**AC:** AC3; diagnostics funnel rows appear per tick; settlement WIN/LOSS
visible in dashboard + Telegram.

## P4 — Dashboard ✅
Cached-render handlers, `/`, `/csv`, `/api/diagnostics`, embedded static UI
per design-brief tokens, source popup, snapshot strip; composite index
`idx_trades_token_ts` (fix for 121% CPU incident, applied to live DBs via
throwaway-container trick, no restart).
**AC:** AC5; page TTFB < 1 ms cached; refresh < 1 s with 2000+ positions;
CPU idle < 2%.

## P5 — Deploy & network hardening ✅
Coolify apps (paper `hdlvr…`, live `euhd7…`), data/logs named volumes,
API-deploy recipe, `x-polymarket-hosts` extra_hosts pins (1918b29), volume
chown runbook in TRD §6.
**AC:** after redeploy, `docker exec <bot> grep polym /etc/hosts` non-empty and
`wget /ok` inside container 200; `readonly database` grep count = 0 post-restart.

## P6 — Quality gates ✅
Unit tests + committed coverage, SonarQube (sast.thempra.net) rules clean.
**AC:** Sonar gate passes per profile ("JobPilot way" config); no new blocker
rules on push.

## P7 — Risk remediation before any live restart 🔒
1. Implement/hard-enforce daily_loss_limit (docs flag it unenforced) — stop
   entries UTC-day on breach.
2. Contrarian stop-loss OR max-hold-time exit (needs first-ever SELL path or
   resolution-time cap — decision PRD §8a).
3. Kill-switch runbook tested: Coolify stop + manual resting-order sweep.
**AC:** paper scenario forces a limit breach → zero new orders rest of day;
simulated -X% position auto-exits/caps within one tick; runbook dry-run clean.

## P8 — Live restart protocol (gated, manual) 🔒
1. Data-API fill-path probe via `go run` vs known historical fill.
2. Confirm on-chain collateral > 0, config.live.yml reviewed, auto-deploy OFF.
3. Manual start with reduced bankroll (≤ previous), contrarian only.
4. 72 h observation: fills match portal 1:1; daily loss guard exercised.
**AC:** PRD AC1–AC5 re-verified on live; sign-off from owner required at each
of steps 2→3 and 3→4. No phase P9 until P8 completes.
