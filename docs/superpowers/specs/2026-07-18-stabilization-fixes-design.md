# Stabilization Fixes — Design Spec

**Date:** 2026-07-18
**Status:** Approved
**Branch:** `fix/stabilization-p1`
**Supersedes:** none (follows `2026-06-08-multi-strategy-broker-design.md`)

## Context

Tras 7 semanas en producción paper multi-strategy (1,131 trades), una auditoría
reveló bugs operacionales + de correctitud que invalidan la data para decidir
sobre estrategias. Este spec captura el plan de estabilización **antes** de
 cualquier cambio estratégico.

### Hallazgos críticos del audit

1. **Routing por estrategia roto** — `?strategy=X` devuelve HTML byte-idéntico
2. **Data race en `Broker.marketCache`** — 5 goroutines sin mutex, panics garantizados
3. **Settlement no idempotente per-strategy** — doble conteo entre strategies
4. **Paper/Live mode label incorrecto** — compose `--paper` pero CSV marca LIVE
5. **Kelly mal aplicado** — todos los callers pasan `b=1` en vez de `b=(1-p)/p`
6. **`getPositionSize` default inseguro** — devuelve maxSize en vez de 0
7. **`broker.Buy` silent fill** — si GetOrder falla, asume fill completo
8. **Sin mark-to-market** — unrealized P&L siempre `$0.00`
9. **Concentración 96% en trader perdedor** (swisstony, -2.8% ROI)
10. **Profit factor global: 0.97** (ligeramente negativo)

### Datos del baseline (1,131 trades, 7 semanas)

- WR global: 46.6%
- Fútbol internacional: +$77.90 (único centro de beneficio)
- Béisbol: -$76.12
- Precio 0.60–0.80 (favoritos): -$54.30 a pesar de 65% WR
- Jueves (UTC): -$165.39; Martes (UTC): +$113.64
- Horas 02-03 UTC: -$114.71; Horas 19-20 UTC: +$202.32

## Objetivo

Dejar el sistema estable, con datos confiables separables por estrategia,
taggeados por schema_version, para que en 2-3 semanas se pueda decidir
fundamentadamente sobre kill/keep/redesign de cada estrategia.

## Out of scope (explícito)

- No nuevas estrategias
- No refactor del `Strategy` interface
- No cambio de DB engine
- No migrate a live trading
- No backfill de datos históricos
- No toucha las tesis de las 5 estrategias actuales

## FASE P1 — Stop bleeding + tag (3-4 días)

### P1.0 — Revivir contenedor (operacional)
- Contenedor en `exited:unhealthy` desde 2026-07-07 con 10 restarts agotados
- Acción: redeploy via Coolify API

### P1.1 — Mutex en `Broker.marketCache` + TTL
- **Archivo:** `internal/broker/broker.go`
- **Cambio:**
  ```go
  type Broker struct {
      // ...
      marketMu    sync.RWMutex
      marketCache map[string]marketCacheEntry
  }
  type marketCacheEntry struct {
      data      MarketData
      fetchedAt time.Time
  }
  ```
- Reemplazar accesos directos por `getMarket(id)` / `setMarket(id, md)`
- TTL implícito de 60s (también soluciona P2.3 parcialmente)
- **Test:** stress con 5 goroutines leyendo/escribiendo en paralelo

### P1.2 — Routing `?strategy=X` realmente filtra
- **Archivo:** `internal/dashboard/dashboard.go`
- **Diagnóstico:**
  - Confirmar `enabledStrategies()` devuelve 5 claves correctas
  - Confirmar `resolveStrategy()` valida query param
  - Confirmar `query(dbPath, strategy)` pasa param al SQL
  - Confirmar `stratCache[strategy]` se popula distinto por strategy
- **Test E2E:** `?strategy=copy_trading` vs `?strategy=arbitrage` deben tener
  HTML con MD5 distinto
- Sospechoso principal: SQL bind de `WHERE p.strategy = ?` o cache miss devolviendo vacío

### P1.3 — Kill switch por estrategia via config
- **Archivos:** `internal/cfg/cfg.go`, `internal/engine/engine.go`,
  `internal/strategy/registry.go`
- Verificar que `Strategies.X.Enabled` se respeta al instanciar
- Log en startup: `"Strategy X disabled by config"`
- No hot reload — restart del contenedor suficiente

### P1.4 — Settlement idempotente per-strategy
- **Archivos:** `internal/db/db.go`, `internal/strategy/settlement/settlement.go`
- Nuevo método: `SettleTradesByStrategy(tokenID, strategy string, ...)`
- `checkPosition` recibe `pos.Strategy` y lo pasa
- Wrap en transacción `BEGIN/COMMIT`
- Guard `WHERE settled_at IS NULL`

### P1.5 — Schema version tag (boundary marker)
- **Archivo:** `internal/db/db.go`
- **Migración:**
  ```sql
  ALTER TABLE trades ADD COLUMN schema_version TEXT NOT NULL DEFAULT 'legacy';
  ALTER TABLE positions ADD COLUMN schema_version TEXT NOT NULL DEFAULT 'legacy';
  ALTER TABLE pnl_snapshots ADD COLUMN schema_version TEXT NOT NULL DEFAULT 'legacy';
  ```
- Debe deployed ANTES que cualquier otro fix → marca el boundary
- Tras deploy P1, nuevos writes usan `'v1'`; P2 `'v2'`; P3 `'v3'`
- Permite: `SELECT ... WHERE schema_version >= 'v1'` para excluir baseline

### P1.6 — Paper/Live mode label fix
- **Archivos:** `cmd/vagustrader/main.go`, `internal/broker/buy.go`
- Confirmar `--paper` flag llega al broker
- Confirmar `SaveTrade` recibe `mode = "PAPER"`
- Sospechoso: default hardcodeado en struct literal

### Criterios de salida P1

- [ ] `curl ?strategy=copy_trading` y `?strategy=arbitrage` devuelven HTML distinto
- [ ] No panics en logs tras 24h
- [ ] Nuevas trades tienen `schema_version='v1'`
- [ ] Nuevas trades tienen `Mode=PAPER`
- [ ] Config `enabled: false` para una strategy la desactiva

## FASE P2 — Correctness (3-4 días)

### P2.1 — Kelly `b` parameter correcto
- En todas las calls a `kelly.Size()`:
  ```go
  // Antes: Payout: price, LossAmount: price → b=1
  // Después: Payout: 1 - price, LossAmount: price → b=(1-p)/p
  ```
- Longshots aumentan size, favorites disminuyen
- **Mitigación:** `MaxPositionUSDC` cap sigue activo
- **Archivos:** `internal/strategy/{copytrading,underhunter,whaleconsensus,contrarian}/*.go`

### P2.2 — `getPositionSize` default seguro
- **Archivo:** `internal/strategy/copytrading/copytrading.go`
- Sin tier match → return 0 (no trade) + log warning

### P2.3 — TTL en marketCache
- Completado en P1.1 con `marketCacheEntry{fetchedAt time.Time}`
- Verificar que todas las estrategias se benefician automáticamente
- Remove `ClearMarketCache()` calls (innecesario)

### P2.4 — `broker.Buy` sin silent fill
- **Archivo:** `internal/broker/buy.go`
- Si `GetOrder` falla: retornar error, NO asumir fill
- Si `status == "LIVE"`: retornar error, tratar como no-fill
- Log WARN con orderID para traceabilidad

### P2.5 — Hard cap global `MaxOpenPositions`
- **Archivos:** `internal/broker/broker.go`, `internal/broker/buy.go`,
  `internal/cfg/cfg.go`, `config.yml`
- Config: `risk.max_open_positions_global: 100` (default 100)
- En `Buy()`: contar total positions abiertas; si > cap, rechazar
- Log WARN cuando se rechaza

### Criterios de salida P2

- [ ] Position sizes para longshots aumentan visiblemente
- [ ] Nuevas trades tienen `schema_version='v2'`
- [ ] Buy rechazados aparecen en logs con orderID
- [ ] `MaxOpenPositions` global respeta el cap

## FASE P3 — Observability (3-4 días)

### P3.1 — Mark-to-market en positions table
- **Archivos:** `internal/dashboard/dashboard.go`,
  `internal/dashboard/static/template.html`
- Batch fetch CLOB prices para tokens abiertos
- Añadir columnas "Current" y "Unrealized"
- KPI Unrealized pasa de hardcoded `$0.00` a suma real
- Reusar `marketCacheEntry` con TTL 60s

### P3.2 — Wallet card visible
- **Archivo:** `internal/dashboard/dashboard.go`
- Poblar `data.Wallet` con `db.GetWallet(strategy)`
- Log si `GetWallet` returns nil/err

### P3.3 — Sign display fix
- **Archivos:** `internal/dashboard/dashboard.go` o `template.html`
- Buscar helper que genera `$+-90.01`
- Usar formato único que respete el signo

### P3.4 — Strategy stats table funcional
- **Archivo:** `internal/dashboard/dashboard.go`
- Query para `StrategyStatsRows`:
  ```sql
  SELECT strategy, COUNT(*) as trades,
         SUM(CASE WHEN result='WIN' THEN 1 END)*1.0/COUNT(*) as wr,
         SUM(cost) as invested, SUM(pnl) as pnl
  FROM trades WHERE schema_version >= 'v1'
  GROUP BY strategy
  ```

### P3.5 — Settlement defensive idempotency
- **Archivo:** `internal/db/db.go`
- Migración:
  ```sql
  ALTER TABLE trades ADD COLUMN settled_at DATETIME;
  ALTER TABLE positions ADD COLUMN settled_at DATETIME;
  ```
- Skip si `settled_at IS NOT NULL`

### P3.6 — Gráfico P&L por estrategia
- **Archivos:** `template.html`, `script.js`
- Reusar Chart.js (ya cargado)
- Stacked area: tiempo vs P&L, una serie por estrategia
- Datos: `pnl_snapshots` filtrando por strategy

### Criterios de salida P3

- [ ] Dashboard muestra current price + unrealized por position
- [ ] Wallet card visible con valores
- [ ] Sign display correcto
- [ ] Strategy stats table muestra 5 filas con datos
- [ ] Nuevas trades tienen `schema_version='v3'`

## Orden de deployment estricto

1. P1.0 (operacional) → contenedor vivo
2. P1.5 (schema_version) → marca boundary legacy
3. P1.1 (mutex) → no más panics
4. P1.2 (routing) → data separable
5. P1.3 (kill switch) → capacidad de frenar
6. P1.4 (settlement idempotencia) → data limpia
7. P1.6 (paper/live label) → consistencia
8. Deploy P1, observar 48h, verificar criterios salida
9. P2.1-P2.5 en un solo PR
10. Deploy P2, observar 5-7 días
11. P3.1-P3.6 en un solo PR
12. Deploy P3, observar 7-14 días
13. Decisión sobre strategies con datos limpios

**Tiempo total:** 9-12 días implementación + 2-3 semanas observación.

## Riesgos y mitigaciones

| Riesgo | Mitigación |
|---|---|
| P2 sizing change causa overexposure | Tras deploy, monitorear `Invested` KPI por strategy, kill switch via config |
| P3 precio fetch masivo (5x API load) | TTL cache 60s, batch requests |
| Container sigue crasheando post-fix | P1.1 (mutex) debería resolver; si no, dump de stack trace |
| Historical data contamina métricas | Schema_version tag permite filtrar |
| Regresión entre fases | Cada fase deployable independientemente, revertible |
