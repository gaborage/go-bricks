# Per-Tenant Provisioning State Machine

This guide documents the durable, crash-recoverable state machine that
provisions per-tenant resources (schema, runtime role, migrations, seed
data) under the multi-tenant migration model. Implementation tracked under
issue #379; design rationale in [ADR-021](adr_021_provisioning_state_machine.md).

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
`migration.ProvisionPGRoles` (see [migration_roles.md](migration_roles.md))
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

`Migrate` is any callback — Flyway is the common choice, and the
single-transaction pattern (see [the section
below](#single-transaction-provisioning-on-postgresql-consumer-side-pattern))
is the PostgreSQL-only alternative when atomicity is required.

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
| `provisioning.ErrQuiesced` | Returned by `Run` when the deployment quiesce flag is set; callers should treat it as "retry later" |
| `Executor.WithQuiesce(g migration.QuiesceGate)` | Enables the deployment quiesce gate; `Run` returns `ErrQuiesced` instead of advancing while the flag is set |
| `Executor.WithAudit(emitter migration.Emitter, cfg AuditContext)` | Opt-in `state.transitioned` audit-event emission via the shared OTel seam (ADR-019) |

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

## Single-transaction provisioning on PostgreSQL (consumer-side pattern)

On PostgreSQL, a tenant's entire provisioning unit — schema, role-pair, and
tenant-table DDL, plus the tenant registry row and the "tenant
provisioned" outbox event (both DML) — fits in a single transaction: it
can commit or roll back as one unit. Flyway, however, is a subprocess; it
cannot join a `dbtypes.Tx`, so it structurally cannot participate in that
unit. The framework deliberately ships **no** tx-scoped migration applier
for this (decision 2026-07-18, issue #720; consistent with #375's standing
decision against a hand-rolled migration engine — "rejected, high cost,
low differentiation"). Consumers who need "the outbox event exists iff the
tenant fully exists" own the in-transaction apply step themselves. This
section is the blessed shape for it.

Not every statement belongs in this transaction, though: PostgreSQL
rejects some statements inside an explicit transaction block — e.g.
`CREATE INDEX CONCURRENTLY`, `REINDEX … CONCURRENTLY`, `CREATE DATABASE`,
`VACUUM`, `ALTER SYSTEM` —
so a consumer who needs one of those must run it outside this transaction,
forfeiting atomicity for that step.

**When to use which model.** The provisioning executor documented above
(`provisioning.Executor` + persisted state machine + `Cleanup`
compensation) is for flows that cross non-transactional boundaries: a
Flyway subprocess, an external secret store, Oracle (which this package
does not support in v1 — see "Scope" at the top of this page). The
single-transaction pattern below is for PostgreSQL-only control planes
where every step is SQL against one database and atomicity is worth more
than resumability. The two can compose, too: the pattern below can be the
entire body of a `Steps.Migrate`-style custom callback when a consumer
wants to wrap the transactional apply in persisted crash recovery.

**The pattern.**

```go
import (
    "context"

    "github.com/gaborage/go-bricks/app"
    "github.com/gaborage/go-bricks/database"
    dbtypes "github.com/gaborage/go-bricks/database/types"
    "github.com/gaborage/go-bricks/migration"
)

err := database.WithTx(ctx, adminDB, func(ctx context.Context, tx dbtypes.Tx) error {
    // 1. Serialize concurrent provisioning of the same tenant.
    if _, err := tx.Exec(ctx,
        `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, tenantID); err != nil {
        return err
    }

    // 2. Roles + schema + grants (transactional on PostgreSQL, including
    //    CREATE ROLE). PGRoleProvisioningSQL returns the ordered statement
    //    list; executing it on this tx makes the ProvisionPGRoles
    //    partial-progress caveat moot — everything rolls back together.
    stmts, err := migration.PGRoleProvisioningSQL(spec)
    if err != nil {
        return err
    }
    for _, s := range stmts {
        if _, err := tx.Exec(ctx, s); err != nil {
            return err
        }
    }

    // 3. Tenant DDL + the ledger (see below), applied in filename order.
    // 4. Tenant registry row (consumer-owned table).
    // 5. Outbox event — rides the same tx, so it exists iff the tenant does.
    if _, err := outboxPub.Publish(ctx, tx, &app.OutboxEvent{
        EventType:   "tenant.provisioned",
        AggregateID: tenantID,
        Payload:     payload,
        Exchange:    "tenant.events",
    }); err != nil {
        return err
    }
    return nil
})
```

`spec` is the same `*migration.PGRoleSpec` used in the Quick start above,
and `outboxPub` is an `app.OutboxPublisher` (`deps.Outbox` inside a
module). `database.WithTx` (`database/transaction.go`) commits on a nil
return and rolls back on error or panic, so a crash or error anywhere in
the callback rolls back schema, roles, tables, ledger, registry row, and
outbox row together — there is no window where the tenant half-exists. A
re-run converges: the statements from `PGRoleProvisioningSQL` are
idempotent, and the ledger below skips scripts it has already applied.
`PGRoleProvisioningSQL`'s own doc carries a `SECURITY` note that its
returned statements can include a password literal in clear text — that
applies here too; don't log the statement slice.

**Why the `ProvisionPGRoles` partial-progress caveat doesn't apply here.**
`ProvisionPGRoles`'s doc comment (`migration/roles.go:117-120`) warns that
a partial-progress failure can leak intermediate state. That caveat is
about `ProvisionPGRoles`'s own execution mode: it takes a bare `*sql.DB`
and calls `db.ExecContext` once per statement with no enclosing
transaction, so each statement lands (and can survive a later failure)
independently. The pattern above never calls `ProvisionPGRoles` — it calls
its sibling, `PGRoleProvisioningSQL`, which only builds the statement list,
and then executes that list itself with `tx.Exec` against the caller's own
transaction. Run that way, the statements are ordinary transactional DDL
inside one explicit transaction: PostgreSQL rolls back `CREATE ROLE`
together with everything else issued on the same transaction, so the whole
batch commits or rolls back as one unit.

### The ledger table

The shape consumers should use for the apply-once bookkeeping in step 3
above:

```sql
CREATE TABLE IF NOT EXISTS <tenant_schema>.provisioning_ledger (
    version    TEXT PRIMARY KEY,      -- e.g. "V1__create_widgets.sql"
    checksum   TEXT NOT NULL,         -- sha256 of the script bytes
    applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
```

Apply rule: for each embedded script, in lexical order — if a ledger row
exists with a matching checksum, skip it; a matching version with a
*different* checksum means edited history, fail; otherwise execute the
script and insert the row, all on the same transaction as everything else
in "The pattern" above.

### Coexistence with `flyway_schema_history`

The invariant: a given tenant schema's *ongoing* migrations have exactly
one owner. Two blessed shapes:

- **Bootstrap-then-Flyway (recommended).** The provisioning transaction
  applies the same `V*` scripts Flyway would via the ledger above. Ongoing,
  deployment-time migrations then run through `MigrateFor` (`cfg.ConfigPath`
  pointing at a Flyway config file that sets Flyway's own
  `baselineOnMigrate=true` and `baselineVersion` to the bootstrapped
  version — these are Flyway CLI/config properties, not go-bricks
  `migration.Config` fields), so Flyway baselines the schema on first
  contact instead of re-applying `V1..Vn`. The ledger is bootstrap-only
  history; `flyway_schema_history` owns everything after.
- **Ledger-only.** The tenant schema is never pointed at Flyway; every
  later change goes through the same in-transaction apply. Simpler
  invariants, but gives up Flyway's `info`/`validate` tooling for tenant
  schemas.

Never run both mechanisms against the same script set without a baseline —
that is how a second `MigrateFor` fails on objects that already exist.

**Related:** [multi_tenant_migration.md](multi_tenant_migration.md) covers
the fleet/deployment-time `MigrateFor` side of the Bootstrap-then-Flyway
shape; [outbox.md](outbox.md) covers the delivery guarantee of the event
published in step 5 of "The pattern"; [migration_roles.md](migration_roles.md)
covers the role model behind `spec`.

## What's out of scope (and where it lives)

- **The dispatcher** that polls for `StatePending` jobs and calls `Run`
  on them: deferred to a follow-up after the state machine itself is
  solid (issue body, "Out of scope").

## Related

- [migration_roles.md](migration_roles.md) — the role-separation model
  the `CreateSchema` / `CreateRole` steps invoke.
- [multi_tenant_migration.md](multi_tenant_migration.md) — the `MigrateAll`
  orchestrator the `Migrate` step typically calls.
- [migration_audit.md](migration_audit.md) — the audit-event seam that
  `state.transitioned` events are emitted through (ADR-019).
- [ADR-021](adr_021_provisioning_state_machine.md) — the reuse-vs-divergence
  decision that put this package in `migration/provisioning/` rather than
  inside `outbox/`.
