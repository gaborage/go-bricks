# PostgreSQL Role Separation for Migrations

This guide documents the migrator-vs-runtime role-separation model that lets
auditors answer a flat **no** to *"can the running service alter its own
database schema?"*. Tracked under issue #378; depends on the multi-tenant
migration runner from #377 (PR #421).

**Scope:** PostgreSQL only in v1. Oracle is tracked separately under #385
because its user/role/grant model is fundamentally different.

## The model

Two roles per tenant:

| Role | Owns | Privileges | Used by |
|------|------|-----------|---------|
| **Migrator** (one per deployment, shared across tenants) | The tenant schema(s) it provisioned | DDL on its own schemas | `go-bricks-migrate` CLI / `migration.MigrateAll` |
| **Per-tenant runtime** (one per tenant) | Nothing | `USAGE` on the tenant schema; `SELECT/INSERT/UPDATE/DELETE` on all current and future tables; `USAGE/SELECT/UPDATE` on sequences | The running service (`runtime.role` in `database.yaml`) |

Both roles are created with the same locked-down attribute floor:
`NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS`. The
attribute lockdown is reapplied on every provisioning call so a misconfigured
role (e.g., someone ran `ALTER ROLE migrator SUPERUSER` manually) snaps back
on the next migration run.

The runtime role's DDL rejection is **not** enforced by explicit `REVOKE`
statements — it falls out of PostgreSQL's default ownership model. The
runtime role doesn't own the schema and doesn't have `CREATE` on it, so
`CREATE TABLE`, `ALTER TABLE`, and `DROP TABLE` are rejected with SQLSTATE
42501 by construction. This means there's no grant arithmetic to audit; the
proof is **"the runtime role never owns anything"**.

## Future-table auto-grants

The hinge of the model is `ALTER DEFAULT PRIVILEGES`. Every table created by
the migrator inside a tenant schema is owned by the migrator, so without
this clause the runtime role would have *no* privilege on tables added by
future Flyway migrations. The provisioning helper emits:

```sql
ALTER DEFAULT PRIVILEGES FOR ROLE "migrator" IN SCHEMA "tenant_a"
  GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO "tenant_a_app";

ALTER DEFAULT PRIVILEGES FOR ROLE "migrator" IN SCHEMA "tenant_a"
  GRANT USAGE, SELECT, UPDATE ON SEQUENCES TO "tenant_a_app";
```

Result: a Flyway migration adding `CREATE TABLE tenant_a.gadgets (...)` does
not need a follow-up `GRANT` statement — the table is automatically
DML-accessible to the runtime role.

## Using the helper

```go
import "github.com/gaborage/go-bricks/migration"

spec := migration.PGRoleSpec{
    Schema:           "tenant_a",
    MigratorRole:     "migrator",
    MigratorPassword: os.Getenv("MIGRATOR_PASSWORD"), // optional, omit if managed externally
    RuntimeRole:      "tenant_a_app",
    RuntimePassword:  os.Getenv("TENANT_A_RUNTIME_PASSWORD"),
}

// db is an *sql.DB authenticated as a role with CREATEROLE — typically the
// instance bootstrap superuser or a dedicated provisioner role.
if err := migration.ProvisionPGRoles(ctx, db, spec); err != nil {
    return fmt.Errorf("provision tenant %q: %w", spec.Schema, err)
}
```

All operations are idempotent — rerunning with the same spec is a no-op
except that `MigratorPassword` / `RuntimePassword` (when non-empty) are
reapplied on every call, which makes secret rotation a no-op rerun.

### Operator escape hatch

When you want to inspect or apply the provisioning via `psql` instead, use
`PGRoleProvisioningSQL` to get the statements:

```go
stmts, err := migration.PGRoleProvisioningSQL(spec)
if err != nil { return err }
for _, s := range stmts {
    fmt.Println(s + ";")
}
```

## Identifier safety

`PGRoleSpec.Schema`, `MigratorRole`, and `RuntimeRole` are validated against
a conservative ASCII subset: `[A-Za-z_][A-Za-z0-9_]{0,62}` (NAMEDATALEN-1
maximum). Identifiers that fail this check (hyphens, dots, Unicode, embedded
quotes, NUL bytes, leading digits, names longer than 63 bytes) are rejected
with `ErrInvalidPGIdentifier` before any DDL is built.

If your tenant IDs include hyphens or other characters outside this subset,
normalize them upstream (e.g., `tenant-a` → `tenant_a`) before constructing
the spec. The migration boundary deliberately enforces a single forcing
function rather than scattering input filters across the codebase.

## Credential handling

The migrator role holds privileged access to every tenant schema in the
deployment. Treat its credentials accordingly:

- **Where:** AWS Secrets Manager, HashiCorp Vault, GCP Secret Manager, or
  equivalent. Never check the password into a config file or environment
  variable that is broadly readable.
- **Who:** Only the migration runner (the `go-bricks-migrate` CLI or the
  in-process `migration.MigrateAll` caller). Runtime services must connect
  as their per-tenant runtime role, never as the migrator.
- **Rotation:** Pass the new password as `MigratorPassword` on the next
  provisioning call. The helper emits `ALTER ROLE ... PASSWORD ...`
  unconditionally when the field is non-empty, so rerunning with a rotated
  secret is sufficient.

For the AWS Secrets Manager naming convention used by `go-bricks-migrate`,
see [multi-tenant-migration.md](multi-tenant-migration.md#aws-secrets-manager-convention).

## Provisioning flow

```text
              ┌──────────────────────┐
              │ Provisioner role     │  (bootstrap superuser or dedicated
              │ (CREATEROLE)         │   role with CREATEROLE granted)
              └──────────┬───────────┘
                         │ ProvisionPGRoles(spec)
                         ▼
   ┌─────────────────────────────────────────────────┐
   │ DO $$ BEGIN IF NOT EXISTS CREATE ROLE migrator  │  idempotent role create
   │ ALTER ROLE migrator NO*                         │  attribute floor lockdown
   │ DO $$ BEGIN IF NOT EXISTS CREATE ROLE runtime   │
   │ ALTER ROLE runtime NO*                          │
   │ ALTER ROLE migrator PASSWORD '...'              │  optional rotation
   │ ALTER ROLE runtime  PASSWORD '...'              │  optional rotation
   │ CREATE SCHEMA IF NOT EXISTS tenant_a AUTH...    │  schema owned by migrator
   │ GRANT USAGE ON SCHEMA tenant_a TO runtime       │
   │ GRANT SELECT/INSERT/UPDATE/DELETE ON ALL TABLES │  existing-object grants
   │ ALTER DEFAULT PRIVILEGES ... ON TABLES TO ...   │  future-object grants
   └─────────────────────────────────────────────────┘
```

After provisioning:
- The migration runner connects as `migrator` and applies Flyway migrations.
  All new tables are owned by `migrator`.
- The running service connects as `tenant_a_app` and performs DML only. Any
  attempt to issue DDL is rejected with SQLSTATE 42501.

## Verifying the model

The acceptance tests under `migration/roles_integration_test.go` codify
three claims that should remain true forever:

1. **`TestPGRolesRuntimeRoleRejectedOnDDL`** — `CREATE/ALTER/DROP TABLE`
   from the runtime role return SQLSTATE 42501.
2. **`TestPGRolesAlterDefaultPrivilegesAutoGrants`** — A table created by
   the migrator *after* provisioning is automatically DML-accessible to the
   runtime role with no intervening `GRANT`.
3. **`TestPGRolesRuntimeRoleHasNoSuperPowers`** — Both roles have
   `rolsuper=false`, `rolcreatedb=false`, `rolcreaterole=false`,
   `rolbypassrls=false`, and `rolreplication=false` in `pg_catalog.pg_roles`.

Run them with:

```bash
go test -tags=integration -run TestPGRoles ./migration/
```

Docker is required (testcontainers spins up a fresh PostgreSQL 17 instance
per test). Flyway is **not** required for these tests — they exercise the
role helpers directly against PostgreSQL.

## Limitations & future work

- **Multi-database vs multi-schema.** This model assumes the multi-schema
  multi-tenant pattern (one database, schema-per-tenant). For
  database-per-tenant deployments, the same role attributes apply but the
  helper would need to grow a `CREATE DATABASE ... OWNER` step.
- **Oracle.** Tracked under #385. Oracle's user-as-schema model and
  privilege grants are fundamentally different — likely a separate
  `OracleRoleSpec` and `ProvisionOracleRoles` rather than an extension here.
- **Provisioning state machine.** #379 will add a durable, crash-recoverable
  state machine that orchestrates the full per-tenant provisioning flow
  (schema_created → role_created → migrated → seeded → ready). Today the
  helper is a single idempotent call; the state machine will wrap it.
