# VagusTrader — Foundation Documents (authority index)

Single source of truth. Each fact lives in exactly ONE doc (its "author");
other docs only reference it (e.g. "risk guards: PRD §4"), never re-describe it.

| Doc | Authorship (canonical scope) |
|---|---|
| [prd.md](prd.md) | What / why / for whom, feature inventory, risk-guard catalog, scope in/out, open questions |
| [trd.md](trd.md) | Stack, libraries, external APIs + endpoints, wallet/signing model, infra & deploy, network constraints, testing |
| [flow.md](flow.md) | Bot lifecycle phases, strategy tick flow, dashboard navigation, per-button/per-CLI-command behavior |
| [design-brief.md](design-brief.md) | Color/typography/spacing tokens, component inventory + states, per-screen appearance |
| [backend-schema.md](backend-schema.md) | SQLite tables/columns/relations (ER), indexes, auth & credential flow, end-to-end data flows, KV/persistence semantics |
| [implementation-plan.md](implementation-plan.md) | Phase sequence + acceptance criteria only |

## Relationship to the rest of `docs/`

The older docs (`business-overview.md`, `architecture-overview.md`,
`risk-management.md`, `decision-logic.md`, `security-model.md`,
`clob-api-integration.md`, `accounting-model.md`, `configuration-reference.md`,
`deployment-guide.md`) predate this set. Where they overlap, foundation docs win
on: scope/features (PRD), stack & deploy (TRD), schema (backend-schema).
`decision-logic.md` and `accounting-model.md` remain the deep-dive annexes for
per-strategy alpha logic and PnL math respectively; foundation docs link, not
duplicate.

## Status caveat (important)

These docs describe the system as built (retrofit, 2026-10-14). Live trading is
**STOPPED** since 2026-09-10 after a -60% drawdown; live runs exactly one
trading strategy (contrarian) + settlement, manual start only. See PRD §8.
