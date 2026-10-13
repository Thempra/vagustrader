# VagusTrader — Design Brief (Dashboard)

> Author of: color/type/spacing tokens, component inventory + states, per-screen look.
> Behavior of each interaction lives in flow.md §3; this doc only says how it looks.

## 1. Design language

Dark, dense, terminal-flavored **operations console**: everything on one
non-scrolling page, monospace numerics, no decoration. Sober and formal —
color is used exclusively for meaning (state/valence), never ornament.
Think Bloomberg/Cockpit-tools, not crypto-app. No gradients, no shadows except
the popup, no rounded corners beyond small chips (3–6 px).

## 2. Tokens (canonical, from `internal/dashboard/static/style.css`)

```css
--bg:     #0a0e17  /* page/panel base (near-black navy) */
--bg2:    #111827  /* header/footer/panel-header/popup */
--bg3:    #1a2234  /* hover, table headers, chips */
--border: #1e293b  /* 1px hairlines everywhere (also used as panel gap) */
--text:   #e2e8f0  /* primary text */
--dim:    #64748b  /* labels, timestamps, empty states */
--accent: #3b82f6  /* BLUE = brand + interactive + USD figures */
--green:  #22c55e  /* positive PnL, WIN, LIVE badge */
--red:    #ef4444  /* negative PnL, LOSS */
--yellow: #f59e0b  /* PAPER badge, caution KPI */
--font:   'JetBrains Mono', ui-monospace, monospace
```

- Base font-size **13 px**; KPI value 15 px bold; labels 10 px, uppercase,
  letter-spacing .06–.08 em; brand 15 px bold accent.
- Spacing: header/footer 4–6 px × 16 px; cells 4–8 px; panels separated by 1 px
  `--border` gaps (body flex row, `overflow:hidden` — the page itself never scrolls).
- Numerics always `font-variant-numeric: tabular-nums`, right-aligned.
- Semantic pairs for chips: green text on `#14532d`, red on `#450a0a`,
  yellow on `#44300a`, live-green on `#0a2a0a`.
- Accent is **blue** per owner standing preference (green reserved for
  valence only, matching the skin-design rule: acento azul, verde=positivo).

## 3. Component inventory & states

| Component | Spec | States |
|---|---|---|
| Header bar | bg2, bottom 1px border; brand → mode badge → KPI cluster → clock (right) | — |
| KPI | vertical label(10px dim)/value(15px bold), left 1px border separators | `.green/.yellow/.blue/.red` by valence |
| Mode badge | tiny chip: `PAPER` yellow bg / `LIVE` green bg | static per process |
| Panel | bg2 header (uppercase title + accent count) + scrollable `.tbl-wrap` | empty → centered dim message |
| Table | fixed layout; sticky th on bg3; 1px row borders; `td.usd` accent; `.val-pos/.val-neg` green/red right-aligned | hover row bg3; text ellipsis (never wraps) |
| Column resize handle | invisible 4px strip on th right edge | hover → accent 4px |
| Panel divider | 1px bg-border with 7px invisible grab area | cursor col-resize |
| Outcome badge | `YES` green-chip / `NO` red-chip, 11px bold | source variant clickable (hover: 1px accent outline) |
| Result chip | `WIN`/`LOSS` chips; open → dim dash | — |
| Market link | text-colored link | hover → accent + underline |
| Snapshot cell | footer strip: date(10 dim)/PnL(13 bold, ±colored)/trades count, left-border separated | — |
| CSV button | bg3 chip, dim uppercase 10px, 1px border | hover → text white, border accent |
| Source popup | fixed layer, bg2, 1px border, radius 6, shadow 0 8 24 rgba(0,0,0,.5), 280–380 px wide, mini-table inside | open on source-click only |
| Scrollbar | 4 px, border-colored thumb, transparent track | — |

## 4. Screens

Only one screen exists (`/`), so: full-viewport 3-zone grid —
**Header (auto)** · **Main (fill): Positions 55% | Trades flex-1** ·
**Footer (auto)**. On narrow viewports panels shrink to their min-widths
(20%/15%) before any content truncates harder; there is deliberately no
responsive stacking (it is an operations display, typically wall/secondary
monitor).

Header order: `VagusTrader` · mode badge · KPIs [Balance | Invested today |
Realized | Unrealized | Positions] · UTC clock. Footer order: brand-less left
→ last ~7 daily snapshot cells → CSV button.

`/csv` and `/api/diagnostics` have no visual design (raw download / JSON).

## 5. Do-nots

- No neon glows, no card shadows on panels, no rounded panels, no icons/emoji.
- No green accents on chrome (green only for valence/chips).
- No data-ink loss: no pagination, no charts — tables + KPIs only; history is
  the CSV/snapshot strip's job.
- Never introduce client-side frameworks; embedded templates + vanilla JS only.
