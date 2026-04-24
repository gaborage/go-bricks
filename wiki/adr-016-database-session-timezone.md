# ADR-016: Database Session Timezone Configuration

**Status:** Accepted
**Date:** 2026-04-23

## Context

Applications built on go-bricks read and write `time.Time` values against PostgreSQL and Oracle. Today, the framework does nothing to set a session-level timezone — every connection inherits whatever the database server (or, for some Oracle deployments, the host running the DB) decides. In practice this produces three failure modes:

1. **Cross-environment drift.** A developer's local Postgres reports `UTC`, staging's container reports `Etc/UTC`, prod's RDS reports `America/Chicago`. The same Go code that writes `time.Now().UTC()` then reads back values that get re-interpreted differently per environment when `TIMESTAMP WITHOUT TIME ZONE` columns are involved.
2. **Pool inconsistency.** Even when an app tries to fix this with a one-shot `SET TIME ZONE 'UTC'` after `sql.Open`, only the *first* borrowed connection carries that setting. As the pool grows (after drops, lifetime expiry, or burst load), new physical connections silently revert to the server default. Bugs from this are hard to reproduce because the first hour of traffic looks fine.
3. **Oracle host-TZ leak.** Oracle Free containers inherit `SESSIONTIMEZONE` from the host's `TZ` environment, so the same image reports `-06:00` on a developer Mac and `+00:00` on a CI runner. Our integration tests confirmed this concretely.

The triggering use case: a project on go-bricks needed UTC datetimes for queries; their Oracle was returning GMT. There was no framework-level knob to fix this.

## Decision

Add `database.timezone`, a single optional config field on `DatabaseConfig`, that propagates to:

- **Default DB** (`config.Database`)
- **Named databases** (`config.Databases` map — for legacy/migration scenarios)
- **Tenant DBs** (multitenant configs that embed `DatabaseConfig`)

### Behavior

| Value | Result |
|-------|--------|
| Unset (`""`) | Defaulted to `"UTC"` during `Validate()` |
| `"UTC"`, `"America/New_York"`, etc. | Validated via `time.LoadLocation`; applied to every new physical connection |
| `"-"` (sentinel) | Skip session timezone enforcement; sessions inherit DB server default (legacy behavior) |
| Anything else `time.LoadLocation` rejects | Validation error at startup (fail-fast) |

### Vendor implementations

| Vendor | Mechanism | Why |
|--------|-----------|-----|
| **PostgreSQL** | `pgxConfig.RuntimeParams["timezone"]` | pgx sends RuntimeParams in the StartupMessage on every new connection — automatic, zero round-trip overhead, guaranteed pool-wide consistency |
| **Oracle** | `driver.Connector` wrapper running `ALTER SESSION SET TIME_ZONE = '<tz>'` on every `Connect()` | go-ora has no equivalent of pgx's RuntimeParams. The wrapper at the connector level is the only mechanism that fires for every pool member, including ones spawned later for pool growth or after drops |

The Oracle wrapper (`*oracle.tzConnector`) closes the underlying connection and surfaces an error if the `ALTER` fails, so `database/sql` can retry rather than hand out a misconfigured connection.

### Override semantics

When the user provides both `database.timezone` and a `timezone=...` embedded in `database.connectionstring`, the explicit `database.timezone` field wins. Config is the declared source of truth; deterministic precedence beats merge gymnastics.

## Consequences

### Breaking Behavior Change

Apps that previously relied on the database server's default timezone (consciously or not) will now see `UTC` after upgrading. Migration knob: set `database.timezone: "-"` to preserve legacy behavior.

```yaml
# ❌ OLD (implicit) — sessions used DB server default (e.g., GMT, America/Chicago, host-TZ)
database:
  type: postgres
  host: db.example.com

# ✅ NEW (default) — sessions use UTC
database:
  type: postgres
  host: db.example.com
  # timezone: UTC  (implicit)

# 🔁 OPT-OUT — preserve old behavior (sessions inherit server default)
database:
  type: postgres
  host: db.example.com
  timezone: "-"
```

### IANA-only contract

Validation uses `time.LoadLocation`, which accepts IANA names (`UTC`, `America/New_York`) and the special `Local`, but **rejects numeric offsets** like `+00:00` and `-05:00`. Oracle natively accepts those formats, but go-bricks does not — users needing offsets should use the IANA `Etc/GMT±N` form (note the inverted sign per the IANA spec: `Etc/GMT+5` ≡ UTC−5).

### Pool consistency guarantees

Both implementations apply the timezone *per physical connection*, not once after `sql.Open`. The integration tests (`TestConnectionSessionTimezoneAppliedToAllPoolMembers` for both vendors) pin multiple `sql.Conn` handles concurrently to force pool growth, then assert every member reports the configured timezone. These tests are the load-bearing regression guard for this design.

### Code structure

- **PostgreSQL**: a five-line `RuntimeParams` injection in `database/postgresql/connection.go` after `pgx.ParseConfig`. No new files.
- **Oracle**: a new `database/oracle/tz_connector.go` containing the wrapper, plus a small refactor in `connection.go` that branches by `cfg.Timezone`. The legacy `openOracleDB` / `openOracleDBWithDialer` paths are preserved for backward compatibility (used when `Timezone == "-"` or in tests that don't go through validation), so existing test infrastructure is undisturbed.

### Rejected alternatives

| Alternative | Why rejected |
|-------------|--------------|
| One-shot `SET TIME ZONE` after `sql.Open` | Only the first borrowed connection inherits it; later pool members silently revert. Exactly the bug we want to prevent. |
| Per-query `SET` middleware | Doubles round-trip count, breaks correlated session state (e.g., temp tables). |
| DSN string mutation only | Fragile under user-supplied connection strings; doesn't work for Oracle (no DSN flag for session timezone). |
| Per-vendor sub-fields (`postgresql.timezone`, `oracle.timezone`) | Doubles the surface area; users would need to remember which to set. The single field forces a uniform IANA contract. |
| Default to "preserve DB server default" instead of UTC | Less opinionated, but doesn't actually fix the problem — apps would still drift across environments. The `"-"` sentinel preserves the escape hatch without making it the default. |
