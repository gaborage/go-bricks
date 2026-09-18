# PostgreSQL Role Separation for Migrations

This guide documents the migrator-vs-runtime role-separation model that lets
auditors answer a flat **no** to *"can the running service alter its own
database schema?"*. Tracked under issue #378; depends on the multi-tenant
migration runner from #377 (PR #421).

**Scope:** PostgreSQL only in v1. Oracle is tracked separately under #385
because its user/role/grant model is fundamentally different.

## The model

Two kinds of role — one migrator for the deployment and one runtime role per
tenant:

| Role | Owns | Privileges | Used by |
| ------ | ------ | ----------- | --------- |
| **Migrator** (one per deployment, shared across tenants) | Every tenant schema (the `AUTHORIZATION` target of each provisioning call) | DDL on its own schemas | `migration.MigrateAll` via [`MigrateAllOptions.MigratorIdentity`](multi_tenant_migration.md#migrator-identity); the `go-bricks-migrate` CLI only when every tenant secret carries the migrator's own `username` and `password` |
| **Per-tenant runtime** (one per tenant) | Nothing | `USAGE` on the tenant schema; `SELECT/INSERT/UPDATE/DELETE` on all current and future tables; `USAGE/SELECT/UPDATE` on sequences | The running service (connects with the runtime role's credentials via `database.username`/`database.password` in `config.yaml`) |

The `WithSharedMigrator` guard described below is **library-only today**:
`go-bricks-migrate` builds its runner with a bare `NewFlywayMigrator` and has no
flag for it, so a CLI-driven shared-migrator fleet still owns the
`postgresql.schema` requirement itself. Exposing it on the CLI is tracked in
[#1730](https://github.com/gaborage/go-bricks/issues/1730).

Every role the helper creates starts at the same locked-down attribute floor:
`NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS`. By default
the attribute lockdown is reapplied on every provisioning call so a
misconfigured role (e.g., someone ran `ALTER ROLE tenant_a_app SUPERUSER`
manually) snaps back on the next provisioning call. `SkipFloorReassert` drops
that repair; drift is then reported only when the caller runs
`CheckPGRoleFloor` (see
[Reporting drift instead of repairing it](#reporting-drift-instead-of-repairing-it)).
A migrator shared across tenants is created once, out of band, and provisioned
with `SkipMigratorRole` (see
[Shared and out-of-band migrators](#shared-and-out-of-band-migrators)).

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

## Default search_path

Every provisioning run also sets a default `search_path` on each role it manages:

```sql
ALTER ROLE "migrator" SET search_path = "tenant_a";
ALTER ROLE "tenant_a_app" SET search_path = "tenant_a";
```

This is idempotent: re-running the same spec converges the setting, and
manual drift (e.g. someone runs `ALTER ROLE tenant_a_app SET search_path =
public`) snaps back on the next provisioning call. The runtime statement is
always emitted; `SkipMigratorRole` drops the migrator's.

**Why it matters on both sides:**

- **Migrator side.** Flyway's fallback when no schema is explicitly targeted
  resolves against the *connection's* current schema, which in turn is
  driven by the role's default `search_path`. Without this `ALTER ROLE`, a
  freshly provisioned tenant whose migration runner omits explicit schema
  args would silently land migrations in `public` and still report success.
  See [Schema targeting (PostgreSQL)](multi_tenant_migration.md#schema-targeting-postgresql)
  for how the runner passes `-schemas`/`-defaultSchema` explicitly — this
  role-level default is the belt to that suspenders, covering any caller
  that provisions roles without going through the runner's explicit args.
  A migrator provisioned with `SkipMigratorRole` gets no such default, so
  every tenant `DatabaseConfig` a shared-migrator run passes to the runner
  **must** set `postgresql.schema`: the runner emits `-schemas` /
  `-defaultSchema` only when it is set, and with it empty Flyway falls back to
  the connection's default schema (typically `public`) and still reports
  success. Build the runner with
  [`FlywayMigrator.WithSharedMigrator()`](multi_tenant_migration.md#schema-targeting-postgresql)
  and it is enforced rather than left to the caller.
- **Runtime side.** Without a role default, unqualified `INSERT`/`SELECT`
  statements from the running service resolve against `public`. The grants
  boundary still prevents cross-tenant reads (this is not a leak), but an
  unqualified query from application code or an operator's `psql` session
  is a silent wrong-schema bug rather than a hard failure.

**Scoping decision:** the statements use plain `ALTER ROLE ... SET`
(cluster-global default for the role), not `ALTER ROLE ... IN DATABASE ...
SET`. That is sound only for a role used with one schema in one database: the
runtime role is per tenant, so its cluster-global default behaves like a
database-scoped one in practice, and DB-scoping was deferred as unnecessary
complexity for v1. A migrator shared across tenants does not belong to one
schema; see [Shared and out-of-band migrators](#shared-and-out-of-band-migrators).
Shared-cluster deployments that reuse role names across databases would need
to revisit this.

## Using the helper

The call below creates a migrator dedicated to `tenant_a`. For the model's
migrator shared across tenants, see
[Shared and out-of-band migrators](#shared-and-out-of-band-migrators).

```go
import (
    "fmt"
    "os"
    "strings"

    "github.com/gaborage/go-bricks/migration"
)

spec := &migration.PGRoleSpec{
    Schema:           "tenant_a",
    MigratorRole:     "migrator",
    MigratorPassword: strings.TrimSpace(os.Getenv("MIGRATOR_PASSWORD")), // optional, omit if managed externally
    RuntimeRole:      "tenant_a_app",
    RuntimePassword:  strings.TrimSpace(os.Getenv("TENANT_A_RUNTIME_PASSWORD")),
}

// db is an *sql.DB authenticated as the provisioner: the instance bootstrap
// superuser. A CREATEROLE-only provisioner also needs SkipFloorReassert: true
// and the setup in "Provisioner privileges".
if err := migration.ProvisionPGRoles(ctx, db, spec); err != nil {
    return fmt.Errorf("provision tenant %q: %w", spec.Schema, err)
}
```

The `strings.TrimSpace` calls are not decorative: `Validate` rejects a
password containing CR, LF, or NUL with `ErrPGRolePasswordHasControlChar`,
naming the offending field and never its value, and a trailing newline is
what a file-sourced or `echo`-piped secret routinely carries. PostgreSQL
itself accepts such passwords — the restriction is the framework's, taken
because the provisioning path cannot carry them log-safely (see
[ADR-061](adr_061_role_password_control_chars.md)).

All operations are idempotent, so rerunning the same spec converges instead
of failing. Every call reapplies each managed role's attribute floor (unless
`SkipFloorReassert`) and `search_path`, and a non-empty `MigratorPassword` /
`RuntimePassword` — repairing drift and making secret rotation a plain rerun.

### Shared and out-of-band migrators

A migrator shared across tenants must not be provisioned per tenant: every call
would re-lock its attributes, reset its password when one is passed, and point
its cluster-wide `search_path` at the tenant that ran last. Create it once out
of band and set `SkipMigratorRole`:

```go
spec := &migration.PGRoleSpec{
    Schema:            "tenant_a",
    MigratorRole:      "migrator", // still the schema owner and the FOR ROLE target
    RuntimeRole:       "tenant_a_app",
    RuntimePassword:   strings.TrimSpace(os.Getenv("TENANT_A_RUNTIME_PASSWORD")),
    SkipMigratorRole:  true,
    SkipFloorReassert: true, // CREATEROLE-only provisioner; a superuser omits it to keep the repair
}
```

The migrator still owns the schema and is still the `FOR ROLE` target of both
`ALTER DEFAULT PRIVILEGES` statements. `Validate` refuses a non-empty
`MigratorPassword` with `ErrPGRoleSkippedMigratorHasPassword`, so its password
is rotated out of band. The runner's explicit schema targeting — not the
role's `search_path` — aims a shared migrator at each tenant, so each tenant's
`DatabaseConfig` must set `postgresql.schema` (see
[Default search_path](#default-search_path), which `WithSharedMigrator`
enforces).

### Running inside your own transaction

`ProvisionPGRoles` takes a bare `*sql.DB` and executes one statement per
call, so a failure halfway leaves the earlier steps in place and the fix is
to rerun. When the caller already owns a transaction — provisioning the
tenant's schema, tables, ledger row and outbox event as one unit —
`ProvisionPGRolesTx` runs the identical statement list, with identical
validation and error wrapping, against a `database.Executor`:

```go
import (
    "context"

    "github.com/gaborage/go-bricks/database"
    "github.com/gaborage/go-bricks/migration"
)

err := database.WithTx(ctx, conn, func(ctx context.Context, tx database.Tx) error {
    if err := migration.ProvisionPGRolesTx(ctx, tx, spec); err != nil {
        return err
    }
    return applyTenantDDL(ctx, tx, spec.Schema)
})
```

`database.Executor` has two methods,
`Query(ctx, query string, args ...any) (*sql.Rows, error)` and
`Exec(ctx, query string, args ...any) (sql.Result, error)`, which both
`database.Tx` and `database.Interface` already satisfy — no adapter. Every
statement the template emits is ordinary transactional DDL, `CREATE ROLE`
included; the full argument, with the list of statements PostgreSQL really
does refuse inside a transaction block, is in
[migration_provisioning.md](migration_provisioning.md#single-transaction-provisioning-on-postgresql-consumer-side-pattern).
So a rollback leaves nothing behind and the rerun-to-converge guidance does
not apply; the roles and schema are not created, so after fixing the failure
the caller reruns the whole transaction and must get a successful commit.
Hand it a plain `database.Interface` instead of a transaction and
each statement lands independently, exactly as on the `*sql.DB` path — the
guidance applies again.

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

## Provisioner privileges

`ProvisionPGRoles` runs as a **provisioner** — never as the migrator or the
runtime role. What that connection needs depends on who it is and where the
migrator comes from (PostgreSQL 16+; the integration tests run on 18):

| Provisioner | Migrator | Spec options | One-time setup |
| ----------- | -------- | ------------ | -------------- |
| Superuser | created by the call, or out of band | any; `SkipMigratorRole` for a shared migrator | none |
| `LOGIN CREATEROLE NOSUPERUSER` with `CREATE` on the database | created by the call | `SkipFloorReassert: true` | `ALTER ROLE provisioner SET createrole_self_grant = 'set, inherit'`, before the provisioning connections open |
| `LOGIN CREATEROLE NOSUPERUSER` with `CREATE` on the database | created out of band | `SkipMigratorRole: true`, `SkipFloorReassert: true` | `GRANT migrator TO provisioner WITH INHERIT TRUE, SET TRUE` |

Why each requirement exists:

- **`SkipFloorReassert`.** PostgreSQL checks the attribute *keyword*, not its
  value: of `NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS`, a
  CREATEROLE-only role may `ALTER ROLE` only `NOCREATEROLE`. `CREATE ROLE`
  with the same five attributes is allowed, so a role the call creates still
  starts at the floor. With the default spec such a provisioner fails at
  step 1 (the error counts steps from 0), the migrator's lockdown
  `ALTER ROLE`, with SQLSTATE 42501.
- **`SET` and `INHERIT` on the migrator.** `CREATE SCHEMA … AUTHORIZATION
  migrator` requires the right to `SET ROLE migrator`; `ALTER DEFAULT
  PRIVILEGES FOR ROLE migrator`, and the grants on the schema the migrator
  owns, require inheriting its privileges.
  PostgreSQL 16+ grants a role's creator `ADMIN` alone, so without the setup
  above the call fails at `CREATE SCHEMA`, and with `SET` but no `INHERIT` at
  the first schema `GRANT`, both with SQLSTATE 42501. `createrole_self_grant`
  applies only to roles created after it is set, and a role setting reaches
  only sessions opened after it; for a migrator that already exists, use the
  `GRANT` form.
- **`ADMIN` on every role the call alters.** A password or `search_path` needs
  it. The provisioner holds it on the roles it created itself, but not on a
  runtime role that a different role created earlier; there the call fails at
  that role's `ALTER ROLE`, with SQLSTATE 42501.

`createrole_self_grant = 'set, inherit'` also lets the provisioner inherit the
privileges of every runtime role it creates. Its `ADMIN` on those roles already
let it grant itself that membership, so this adds no reach it could not take.

The framework neither emits these grants nor checks the server version: the
table and the three requirements above are the whole contract, and
`TestPGRolesCreateroleProvisionerLimits` pins each refusal by SQLSTATE and
failing step.

### Reporting drift instead of repairing it

With `SkipFloorReassert` set, a role someone later granted `CREATEDB` keeps it.
`CheckPGRoleFloor` reads the five attributes from `pg_catalog.pg_roles`, which
any role that can connect may read, and returns `ErrPGRoleFloorViolated`
naming each one held:

```go
if err := migration.CheckPGRoleFloor(ctx, db, "tenant_a_app"); err != nil {
    return err // e.g. `... role "tenant_a_app" holds CREATEDB`
}
```

## Identifier safety

`PGRoleSpec.Schema`, `MigratorRole`, and `RuntimeRole` are validated against
the shared PostgreSQL bare-identifier grammar, `^[A-Za-z_][A-Za-z0-9_$]*$`
capped at 63 bytes (NAMEDATALEN-1). Identifiers that fail this check (hyphens,
dots, Unicode, embedded quotes, NUL bytes, leading digits, names longer than
63 bytes) are rejected with `ErrInvalidPGIdentifier` before any DDL is built.

### Reserved schema and role names

No identifier field may name something PostgreSQL reserves. `public`, and
anything under the `pg_` prefix (`pg_catalog`, `pg_toast`, every `pg_temp*`, and
the predefined `pg_*` roles), are refused in `Schema`, `MigratorRole` and
`RuntimeRole` alike; `information_schema` is refused in `Schema` only, having no
role meaning. Such a name is refused with `ErrReservedPGIdentifier`, wrapped —
like every other identifier refusal — with `ErrInvalidPGIdentifier`, so an
existing `errors.Is(err, ErrInvalidPGIdentifier)` matcher keeps matching.

Provisioning a tenant into `public` passes every charset check and quietly lands
that tenant's tables in the schema every role on the instance can reach. The role
half is the same failure through the other door: PostgreSQL's `RoleSpec` maps the
name `public` — **quoted included** — onto the PUBLIC pseudo-role, so
`RuntimeRole: "public"` emits `GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES
IN SCHEMA "tenant_a" TO "public"` and grants the tenant's DML to every role on the
instance. `ProvisionPGRoles` would at least meet the server's own `reserved_name`
error (42939) on `CREATE ROLE "public"`, but `PGRoleProvisioningSQL` hands the
script to an operator with no backstop at all — so the refusal belongs here.

Matching is **case-insensitive**. The provisioning path quotes every identifier,
so `"Public"` really is a schema distinct from `"public"` — but any operator,
`psql` session or migration script that writes the name unquoted folds it to the
shared one, so a case twin is a trap rather than a second name.

The rule is the framework's own, not a default a caller can replace: it runs after
the floor and *before* any `IdentifierPolicy`, so no policy can waive it or mask
its sentinel. Near misses are unaffected — `publicx`, `mypublic`, `pgx` and a role
named `information_schema` all provision.

### Tightening the rule

A deployment that needs a stricter rule can set `PGRoleSpec.IdentifierPolicy`
to a `PGIdentifierChecker` (or wrap a plain `func(value string) error` in
`PGIdentifierCheckerFunc`). The floor and the reserved-name rule always run
first, so a policy can only tighten — never re-admit a name either rejected —
and it is consulted once per identifier, in `Schema` → `MigratorRole` →
`RuntimeRole` order, stopping at the first refusal. A nil policy means the
framework's own rules alone; note that a *typed* nil `PGIdentifierCheckerFunc`
stored in the field is a non-nil interface and is therefore still consulted —
it refuses every identifier rather than panicking, so leave the field unset
rather than assigning one. The policy's error is wrapped with
`ErrInvalidPGIdentifier` and the failing field name, so the policy need not
identify the identifier it judged.

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
- **Who:** Only the migration runner, via
  [`MigrateAllOptions.MigratorIdentity`](multi_tenant_migration.md#migrator-identity);
  `go-bricks-migrate` does not expose it yet and connects with each tenant
  secret's `username` and `password` as stored, so CLI use means storing both
  of the migrator's credential fields in every tenant secret. Runtime services
  must connect as their per-tenant runtime role, never as the migrator.
- **Rotation:** The shared migrator (provisioned with `SkipMigratorRole`) is
  rotated out of band. A migrator the call manages takes the new password as
  `MigratorPassword` on the next provisioning call; the helper emits
  `ALTER ROLE ... PASSWORD ...`
  unconditionally when the field is non-empty, so rerunning with a rotated
  secret is sufficient — trim it first if it came from a file, a mounted
  secret, or a command substitution, since a stray CR/LF/NUL is rejected.

For the AWS Secrets Manager naming convention used by `go-bricks-migrate`,
see [multi_tenant_migration.md](multi_tenant_migration.md#aws-secrets-manager-convention).

## Provisioning flow

```text
              ┌──────────────────────┐
              │ Provisioner role     │  (superuser, or CREATEROLE NOSUPERUSER
              │                      │   set up as in Provisioner privileges)
              └──────────┬───────────┘
                         │ ProvisionPGRoles(spec)
                         ▼
   ┌─────────────────────────────────────────────────┐
   │ DO $$ … CREATE ROLE migrator LOGIN NO* …        │ [M]    create; EXCEPTION swallows
   │   EXCEPTION WHEN duplicate_object … $$          │        an existing or concurrent one
   │ ALTER ROLE migrator NO*                         │ [M][F] attribute floor re-assert
   │ ALTER ROLE migrator PASSWORD '...'              │ [M]    only when set (rotation)
   │ DO $$ … CREATE ROLE runtime LOGIN NO* … $$      │        create
   │ ALTER ROLE runtime NO*                          │    [F] attribute floor re-assert
   │ ALTER ROLE runtime PASSWORD '...'               │        only when set (rotation)
   │ CREATE SCHEMA IF NOT EXISTS tenant_a            │        schema owned by migrator
   │   AUTHORIZATION migrator                        │
   │ GRANT USAGE ON SCHEMA tenant_a TO runtime       │
   │ GRANT SELECT/INSERT/UPDATE/DELETE               │        existing-object grants
   │   ON ALL TABLES IN SCHEMA tenant_a TO runtime   │
   │ GRANT USAGE/SELECT/UPDATE ON ALL SEQUENCES      │
   │   IN SCHEMA tenant_a TO runtime                 │
   │ ALTER DEFAULT PRIVILEGES FOR ROLE migrator      │        future-object grants
   │   IN SCHEMA tenant_a … ON TABLES TO runtime     │
   │ ALTER DEFAULT PRIVILEGES FOR ROLE migrator      │
   │   IN SCHEMA tenant_a … ON SEQUENCES TO runtime  │
   │ ALTER ROLE migrator SET search_path = tenant_a  │ [M]    role-level default schema
   │ ALTER ROLE runtime  SET search_path = tenant_a  │        role-level default schema
   └─────────────────────────────────────────────────┘
   [M] not emitted with SkipMigratorRole
   [F] not emitted with SkipFloorReassert
   runtime = the spec's RuntimeRole (tenant_a_app above)
```

After provisioning:

- A `MigrateAll` run with `MigratorIdentity` set to the migrator's credentials
  connects as `migrator` and applies Flyway migrations.
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

Three more back [Provisioner privileges](#provisioner-privileges), provisioning
as a CREATEROLE-only provisioner (the second also provisions one tenant as the
superuser):

1. **`TestPGRolesCreateroleProvisionerMintsTheMigrator`** — with
   `SkipFloorReassert` and `createrole_self_grant`, provisioning succeeds, the
   runtime role is still refused DDL (permission denied) and still gets DML on
   tables the migrator creates later, and `CheckPGRoleFloor` names a
   `CREATEDB` granted afterwards.
2. **`TestPGRolesCreateroleProvisionerLeavesASharedMigratorUntouched`** — three
   tenants provisioned against one out-of-band migrator, two by the
   CREATEROLE provisioner and one by the superuser, leave its attributes,
   password and role settings exactly as they were.
3. **`TestPGRolesCreateroleProvisionerLimits`** — each requirement above, left
   out, fails with SQLSTATE 42501 at the statement its bullet names.

Run them with:

```bash
go test -tags=integration -run TestPGRoles ./migration/
```

Docker is required (testcontainers spins up a fresh PostgreSQL 18 instance
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
- **Provisioning state machine.** Shipped in PR #429. A durable,
  crash-recoverable state machine (`migration/provisioning/`) orchestrates the
  full per-tenant provisioning flow (`pending → schema_created → role_created →
  migrated → seeded → ready`, with `cleanup → failed` branches) and wraps the
  role-provisioning helper. See [migration_provisioning.md](migration_provisioning.md).
