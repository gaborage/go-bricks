# Per-Tenant Provisioning State Machine

This guide documents the durable, crash-recoverable state machine that
provisions per-tenant resources (schema, runtime role, migrations, seed
data) under the multi-tenant migration model. Implementation tracked under
issue #379; design rationale in [ADR-021](adr-021-provisioning-state-machine.md).

**Scope:** PostgreSQL only in v1. Oracle support is tracked under #385.

## The model

A `Job` represents one per-tenant provisioning attempt. It walks a finite
state graph:

```text
pending → schema_created → role_created → migrated → seeded → ready
   \           \              \              \           \
    +-----------+--------------+--------------+-----------+--> cleanup → failed
```

Every transition is **durably persisted** by a `StateStore` *before* the
next forward step runs. If the worker crashes mid-flow, the next `Run`
call reloads the persisted state and resumes from there. Steps must be
idempotent so re-invocation after a crash is safe; the framework's
`migration.ProvisionPGRoles` (see [migration-roles.md](migration-roles.md))
and Flyway's `flyway_schema_history` give the first three forward steps
idempotency for free. The consumer is responsible for designing the
`Seed` step idempotently (typical pattern: `INSERT ... ON CONFLICT DO
NOTHING`).

Forward steps that error transition the job to `StateCleanup`. The
`Cleanup` step drops partially-provisioned resources (schema, role) and
unconditionally moves the job to `StateFailed`. `StateReady` and
`StateFailed` are terminal; calling `Run` on a terminal job is a no-op.

## Quick start

```go
import (
    "context"
    "database/sql"

    "github.com/gaborage/go-bricks/logger"
    "github.com/gaborage/go-bricks/migration"
    "github.com/gaborage/go-bricks/migration/provisioning"
)

// Construct the store and create the persistence table.
store, err := provisioning.NewPostgresStore(adminDB, "" /* default table */)
if err != nil { return err }
if err := store.CreateTable(ctx); err != nil { return err }

// Wire the steps. CreateSchema + CreateRole call ProvisionPGRoles from #378;
// Migrate runs Flyway; Seed and Cleanup are consumer-specific.
steps := provisioning.Steps{
    CreateSchema: func(ctx context.Context, job *provisioning.Job) error {
        return migration.ProvisionPGRoles(ctx, adminDB, &migration.PGRoleSpec{
            Schema:           "tenant_" + job.TenantID,
            MigratorRole:     "migrator",
            RuntimeRole:      "tenant_" + job.TenantID + "_app",
            RuntimePassword:  fetchSecret(job.TenantID),
        })
    },
    CreateRole: func(_ context.Context, _ *provisioning.Job) error {
        // ProvisionPGRoles created both schema and role in one call.
        return nil
    },
    Migrate: func(ctx context.Context, job *provisioning.Job) error {
        _, err := flywayMigrator.MigrateFor(ctx, tenantDBConfig(job.TenantID), nil)
        return err
    },
    Seed: func(ctx context.Context, job *provisioning.Job) error {
        return seedReferenceData(ctx, tenantDB(job.TenantID))
    },
    Cleanup: func(ctx context.Context, job *provisioning.Job) error {
        return dropTenantArtifacts(ctx, adminDB, "tenant_"+job.TenantID, "migrator", "tenant_"+job.TenantID+"_app")
    },
}

exec, err := provisioning.NewExecutor(store, steps, logger.New("info", false))
if err != nil { return err }

// Provision a new tenant (idempotent by job ID).
job := &provisioning.Job{ID: "job-tenant-a-2026-05-13", TenantID: "tenant-a"}
if _, err := store.Upsert(ctx, job); err != nil { return err }
if err := exec.Run(ctx, job.ID); err != nil {
    return err // job ended in StateFailed; LastError carries the diagnostic
}
```

## API surface

| Type / function | Purpose |
|---|---|
| `provisioning.State` | The eight-value enum (`StatePending`, `StateSchemaCreated`, …, `StateFailed`) |
| `provisioning.Job` | Persisted record: ID, TenantID, State, Attempts, LastError, Metadata, timestamps |
| `provisioning.Steps` | Consumer-supplied callbacks invoked at each forward transition |
| `provisioning.Executor` | Orchestrates Job through the state graph; `Run(ctx, jobID)` is the entry point |
| `provisioning.StateStore` | Interface: `Get`, `Upsert`, `Transition`, `CreateTable` |
| `provisioning.NewPostgresStore` | PG reference implementation |
| `provisioning.NewMemoryStore` | In-memory reference implementation (unit tests, alt-vendor template) |
| `provisioning.PostgresStateTableDDL` | Exported DDL constant for external migration tooling |
| `provisioning.ErrJobNotFound` / `ErrIllegalTransition` / `ErrStaleRead` | Sentinel errors |

## Crash-recovery contract

Every step **must** be idempotent. The framework's design assumes:

| State at crash | What the persisted row reflects | What `Run` does on restart |
|---|---|---|
| Step `X` running when crash hits, side effects done | Previous state | Re-invokes step `X` (idempotency required) |
| Step `X` returned successfully, Transition not yet persisted | Previous state | Re-invokes step `X` (idempotency required) |
| Transition committed, step `Y` not yet started | New state from `X` | Invokes step `Y` |
| Cleanup running when crash hits | `StateCleanup` | Re-invokes Cleanup (idempotency required) |
| At terminal state | `StateReady` or `StateFailed` | No-op |

The "step runs, then state persists" order means crashes can re-invoke
the side effect. This is deliberate: persisting first and rolling back
the side effect on failure is harder than running idempotent side
effects twice. The forward steps (schema, role, migrate) are idempotent
by construction. The `Seed` and `Cleanup` steps are the consumer's
responsibility.

## Metadata

`Job.Metadata` is `map[string]string` opaque to the framework. Use it to
record step-specific state that must survive a crash — for example, the
Flyway version range applied so an interrupted-and-restarted `Migrate`
step can resume cleanly rather than re-checking from scratch. The
`Executor`'s success path threads `Job.Metadata` back through to the
next `Transition` call, so step functions that mutate `Job.Metadata`
in place will see those changes persisted. Pass `nil` to `Transition`
to preserve the persisted metadata unchanged.

> **Security note.** Metadata is persisted in plaintext (JSONB on
> PostgreSQL). Never store credentials, secret tokens, or other
> sensitive material in `Job.Metadata`. Use a secret manager and
> record only opaque references (e.g. the secret name) when continuity
> across crashes is needed.

## Operator surface

### Inspecting jobs

```sql
SELECT id, tenant_id, state, attempts, last_error, updated_at
FROM provisioning_jobs
WHERE tenant_id = 'tenant-a'
ORDER BY updated_at DESC;
```

The `idx_provisioning_jobs_tenant` and `idx_provisioning_jobs_state`
indexes make tenant- and state-scoped ad-hoc queries efficient.

### Manually resolving stuck jobs

If a job is stuck in `StateCleanup` (e.g., because Cleanup keeps erroring
on a database connection issue), an operator can `UPDATE provisioning_jobs
SET state = 'failed', last_error = 'manual override' WHERE id = ...`.
The executor will treat the row as terminal on its next `Run` call.

For external migration tooling that prefers to manage schema explicitly:
the exported `provisioning.PostgresStateTableDDL` constant can be
templated with the table name and applied via Flyway, Liquibase, or psql.

## Testing

Two ergonomic patterns:

**Unit tests for consumer step functions.** Use the in-memory store + a
fake job:

```go
store := provisioning.NewMemoryStore()
exec, _ := provisioning.NewExecutor(store, mySteps, logger.New("disabled", true))
_, _ = store.Upsert(ctx, &provisioning.Job{ID: "j1", TenantID: "t1"})
err := exec.Run(ctx, "j1")
require.NoError(t, err)
```

**Integration tests against the PG store.** Use the existing
`testing/containers` PG helpers (see `migration/provisioning/integration_test.go`).

**Mocked StateStore for failure-injection.** The `migration/provisioning/testing/`
subpackage provides `MockStateStore` with `WithGetError`, `WithUpsertError`,
`WithTransitionError`, `WithCreateTableError`, plus assertion helpers
(`AssertJobReachedState`, `AssertTransitionPath`).

## What's out of scope (and where it lives)

- **The dispatcher** that polls for `StatePending` jobs and calls `Run`
  on them: deferred to a follow-up after the state machine itself is
  solid (issue body, "Out of scope").
- **Quiesce-flag interaction** that pauses the dispatcher during
  deployments: tracked under #380.
- **State-transition audit events** (`state.transitioned`,
  `cleanup.completed`): tracked under #382. The existing ADR-019
  audit-emitter seam is the integration point.

## Related

- [migration-roles.md](migration-roles.md) — the role-separation model
  the `CreateSchema` / `CreateRole` steps invoke.
- [multi-tenant-migration.md](multi-tenant-migration.md) — the `MigrateAll`
  orchestrator the `Migrate` step typically calls.
- [migration-audit.md](migration-audit.md) — the audit-event seam future
  state-transition events will use.
- [ADR-021](adr-021-provisioning-state-machine.md) — the reuse-vs-divergence
  decision that put this package in `migration/provisioning/` rather than
  inside `outbox/`.
