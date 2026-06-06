# ADR-025: Connection Pool Idle Connections Default to Track Max

**Status:** Accepted
**Date:** 2026-06-06

## Context

`applyDatabasePoolDefaults` (`config/validation.go`) applies production-safe pool
defaults when the corresponding values are zero. Two of those defaults interacted
badly:

- `database.pool.max.connections` → **25** (maximum open connections)
- `database.pool.idle.connections` → **2** (a fixed `defaultPoolIdleConnections`)

Defaulting idle to a fixed `2` against a max of `25` means that under sustained
load the pool opens up to 25 physical connections to serve concurrent work but
keeps only 2 of them idle. Every connection beyond the second is **closed** as
soon as it goes idle and **re-opened** (TCP + TLS + auth) on the next burst —
continuous connection churn. The const name `defaultPoolIdleConnections` and the
config docs called this "minimum warm connections," but `database/sql` has no
floor concept: `SetMaxIdleConns` is a **cap**, not a minimum, and the driver
never pre-warms connections.

Profiling the framework with a connection-reusing read workload at 2000 req/s
(70% list / 30% get, PostgreSQL) made the cost concrete. With the shipped
default (idle 2 / max 25) the run showed connection-establishment failures
surfacing as HTTP 500s and a long tail dominated by `pgx` connect / TLS handshake
allocations. Raising idle to match max eliminated the churn:

| Config | p95 | p99 | errors | warm backends |
|---|---|---|---|---|
| idle 2 / max 25 (shipped default) | 16.25 ms | 38.19 ms | 8.15% | 3 (churning) |
| idle = max (A/B run at max 50) | 1.46 ms | 2.20 ms | 0% | 51 (warm) |

That is a 91% p95 / 94% p99 reduction and errors to zero, achieved purely by not
discarding warm connections. (The pooled A/B raised max to 50 and set idle = max,
so the warm-backend count reflects the larger pool plus one system backend; the
mechanism — reusing warm connections instead of churning them — is what removes
the latency and errors, independent of the absolute pool size.) `database/sql`
already guarantees the safety of the change: `SetMaxIdleConns` clamps the idle
count to the open-connection max (verified in Go 1.26.4 `src/database/sql/sql.go`),
so idle can never exceed max regardless of how it is configured.

## Decision

In `applyDatabasePoolDefaults`, when `database.pool.idle.connections` is unset
(zero), default it to the effective `database.pool.max.connections` instead of a
fixed `2`. Max is defaulted first, so idle always tracks the resolved max. An
explicit idle *below* max is honored; an explicit idle *above* max is clamped to
max (since `database/sql` caps idle at max-open, clamping keeps the value the
framework reports — startup log, `Stats()`, OTEL idle gauges — equal to what the
driver actually enforces). The negative-value validation is unchanged.

Supporting changes:

- Remove the `defaultPoolIdleConnections` const (its "minimum warm connections"
  semantics never existed) and extract the previously-inline `25` into a named
  `defaultPoolMaxConnections` const, so both pool defaults are explicit and
  greppable (Explicit > Implicit).
- Correct the misleading "minimum warm connections" wording in `config/types.go`
  and `wiki/database.md` to describe idle as a cap that tracks max.
- Log the effective `pool_max_connections` / `pool_idle_connections` /
  `pool_max_lifetime` / `pool_idle_time` at `Info` on every successful
  connection (both PostgreSQL and Oracle vendors), so the resolved pool shape is
  observable at startup.

The fix lives entirely in `config/validation.go`. Both vendor connection layers
(`database/postgresql/connection.go`, `database/oracle/connection.go`) read the
**defaulted** config value, and named / per-tenant databases route through the
same `applyDatabasePoolDefaults` call site, so no vendor-specific edit is needed.

Rejected alternatives:

- **Keep idle at a fixed `2`:** preserves the churn pathology that motivated the
  change; the perf win is unobtainable without idle tracking max.
- **Default idle to `min(max, cap)` (e.g. cap at 10):** a partial fix — the pool
  still churns connections between the cap and max under load, reintroducing the
  problem the change exists to remove.
- **Bump idle in each vendor's `connection.go`:** redundant. The vendors already
  consume the config value; centralizing in config covers main, named, and
  per-tenant databases with one edit.
- **Make it opt-in (new flag, keep `2`):** leaves every default deployment on the
  slow, error-prone path; the safe, churn-free behavior should be the default.

## Consequences

- **Behavioral / capacity change (apiImpact: none):** an application that left
  `pool.idle.connections` unset previously settled to 2 idle connections and now
  holds up to `pool.max.connections` (default 25), still reaped after
  `pool.idle.time` (5m). For multi-tenant deployments the per-tenant idle ceiling
  rises up to ~12.5×; operators should confirm their PostgreSQL `max_connections`
  / Oracle session budget. Documented in [migrations.md](migrations.md#connection-pool-idle-default--tracks-max-adr-025).
- **Observability shift:** the connection-pool idle gauges and the `Stats()`
  `max_idle_connections` / `configured_idle_connections` keys now report the
  effective max rather than `2`. Dashboards/alerts keyed to `idle == 2` must be
  updated.
- **Opt-out is explicit:** set `database.pool.idle.connections` to any value to
  override; explicit configuration is unchanged by this ADR.
- **Guard tests:** `TestApplyDatabasePoolDefaultsIdleAndLifetime` now asserts that
  an unset idle tracks the resolved max (including a custom `max=10` case), and
  that an explicit idle below max is preserved.

## Related

- ADR-016 (database session timezone), ADR-022/ADR-023/ADR-024 — other small,
  breaking config-default/convention changes.
- ADR-003 (database by intent) — pool defaults apply only when a database is
  explicitly configured.
