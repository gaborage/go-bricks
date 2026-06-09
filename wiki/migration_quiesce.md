# Deployment quiesce flag

The quiesce flag lets a deployment-time migration job **pause provisioning workers and tenant fan-out** while it mutates shared roles or runs the deployment Flyway pass, then release them. PostgreSQL only in v1 (issue #380; Oracle parity is #385).

## What it blocks

The flag gates the **pickup of not-yet-started work**, never an in-flight step:

- A `provisioning.Executor` job picked up while the flag is set **parks** at its current persisted state (`Run` returns `provisioning.ErrQuiesced`); no step runs, no transition persists.
- A job already executing a step **drains** to its next durable checkpoint, persists it, then parks on the next loop iteration. No step is interrupted, so no partial provision is orphaned.
- A `migration.MigrateAll` fan-out **stops dispatching** new tenants once the flag is observed set; in-flight tenants finish and `MigrateAll` returns `migration.ErrQuiesceBlocked` with the partial result.

`ErrQuiesced` is **not a failure** — a dispatcher should treat `errors.Is(err, provisioning.ErrQuiesced)` as "retry later", never as a reason to move the job toward `cleanup`/`failed`.

## Auto-release (crash safety)

`Set` requires a TTL (`QuiesceSetOptions.TTL`; zero → `DefaultQuiesceTTL` = 30m, clamped to `MaxQuiesceTTL` = 2h). `IsSet` computes `cleared_at IS NULL AND now < expires_at` **at read time** — there is no sweeper goroutine. This is crash-proof by construction: if a migration job dies after `Set` but before `Clear`, the flag self-releases at `expires_at`; no process's death can strand provisioning. Long deploys call `Set` again to renew (heartbeat).

`Clear` is the unconditional **operator override** — keyed on scope, not the setter's session, so any operator can clear even if the original setter is dead.

## Fail-open

If the quiesce *check itself* errors (a transient control-plane DB hiccup), the worker logs a WARN and **proceeds** (`IsSet`-error → not quiesced). Availability is prioritized over a control-plane blip stranding all provisioning. A nil gate is never quiesced (the feature is fully opt-in).

## API

```go
// Control plane (deployment job / CLI):
ctrl, _ := migration.NewPostgresQuiesceController(db, "") // "" → quiesce_flags
_ = ctrl.CreateTable(ctx)                                 // idempotent
ctrl.WithAudit(emitter)                                   // optional quiesce.* audit (see below)

_, _ = ctrl.Set(ctx, migration.QuiesceSetOptions{
    By:     "deployer@ci",   // explicit principal — never inferred (ADR-019)
    Reason: "deploy-2026.06", // operator-supplied "why" (safe to audit)
    TTL:    time.Hour,
})
st, _ := ctrl.Query(ctx)     // QuiesceStatus{Active, SetAt, SetBy, Reason, ExpiresAt, ClearedAt, Expired}
_, _ = ctrl.Clear(ctx, "ops-oncall")

// Worker (per-tenant provisioning):
exec, _ := provisioning.NewExecutor(store, steps, log)
exec.WithQuiesce(ctrl) // ctrl satisfies migration.QuiesceGate

// Deployment fan-out:
migration.MigrateAll(ctx, fm, lister, configs, migration.ActionMigrate,
    migration.MigrateAllOptions{Quiesce: ctrl})
```

`migration.QuiesceGate` (read side: `IsSet`, `Query`) is the narrow interface both consumers depend on. `migration.QuiesceController` adds the write side (`Set`, `Clear`, `CreateTable`). `MemoryQuiesceController` is the in-memory implementation for tests.

## Storage

A single control-plane table (zero new dependencies), one row per scope (v1 uses the single `"global"` scope):

```sql
CREATE TABLE IF NOT EXISTS quiesce_flags (
    id          VARCHAR(64)  PRIMARY KEY,
    set_at      TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT NOW(),
    set_by      VARCHAR(255) NOT NULL DEFAULT '',
    reason      TEXT NOT NULL DEFAULT '',
    expires_at  TIMESTAMP WITH TIME ZONE NOT NULL,
    cleared_at  TIMESTAMP WITH TIME ZONE
)
```

The DDL is exported as `migration.PostgresQuiesceTableDDL` for operators managing schema externally. The `%s` is substituted only with the validated, quoted table identifier (shared `safePGIdentifier` check); all value-side input flows through `$N` placeholders.

Operators can inspect the flag directly: `SELECT * FROM quiesce_flags WHERE cleared_at IS NULL;`.

## Audit events

When `WithAudit` is wired, `Set` emits `quiesce.set` and `Clear` emits `quiesce.cleared` through the shared `migration.Emitter` — the same OTel-span + structured-log + optional-sink path as `migration.applied` / `state.transitioned`. The audited principal is the operator who performed the action (`Set`'s `By` / `Clear`'s `by`), never inferred; empty surfaces `<unspecified>`. Attributes carry `quiesce.scope`, `quiesce.reason`, and (on set) `quiesce.expires_at` — all operator-supplied, non-sensitive. Raw DB error text is never attached (it would bypass the field-name `SensitiveDataFilter`). See [migration_audit.md](migration_audit.md).

## Limitations (v1)

- Single `"global"` scope (per-region/per-pool scoping is additive via the `id` column; cross-region is deferred).
- PostgreSQL only; Oracle parity tracked in #385.
