# ADR-149: A Type-less Tenant Runs With the Runner's Vendor, and an Undeliverable Migrator Identity Is Refused

**Status:** Accepted
**Date:** 2026-10-17

## Context

`FlywayMigrator.MigrateFor`, `ValidateFor` and `InfoFor` share one run path,
which `Migrate`, `Validate`, `Info` and `MigrateAll` all reach. It resolves the
vendor with `dbVendor`: the tenant's own `DatabaseConfig.Type`, or the runner's
`Database.Type` when the tenant's is empty. The schema args (and
`WithSharedMigrator`'s `ErrSharedMigratorSchemaRequired`), the framework-built
PostgreSQL `-url` ([ADR-085](adr_085_framework_owned_flyway_url.md)), the
default conf and migrations path, the run log, the audit attribute
`migration.vendor` and the duplicate-target key
([ADR-143](adr_143_duplicate_migration_target_refusal.md)) all read that
resolved vendor.

The environment builder alone switched on the tenant's raw `Type`. A type-less
tenant, one whose database section sets no `type`, therefore got no `DB_*` and
no `ORACLE_*` variables, with or without a `MigrateAllOptions.MigratorIdentity`
overlay, and `TenantResult.Vendor` reported `""`:

- **PostgreSQL runner.** Flyway got the tenant's `-url` and conf but the
  credentials of the conf or of the parent process: with a parent `DB_USER`
  exported, the parent's user connected to the tenant's database.
- **Oracle runner.** No variables and no URL, so the conf and the parent
  environment picked the host, the PDB and the credentials. The duplicate-target
  key could differ from Flyway's target in every field.
- **Untyped runner** (the `go-bricks-migrate` shape), with a type-less tenant or
  a non-canonical type (`postgres`). Nothing to inherit: no variables, no URL,
  and a `flyway-unknown.conf` path unless the caller supplied a conf. A
  migrator identity was silently dropped and the run used the conf's or the
  parent's credentials.

Output redaction masks only the run config's own password, never an inherited
parent one. Fixes #2019.

## Decision

**A type-less tenant runs as a copy typed with the runner's vendor.** Right
after `dbVendor`, the shared run path hands every later step a copy of the
config whose `Type` is the resolved vendor, when the tenant's is empty and the
runner has one. The URL, schema args, conf and migrations path, environment,
redaction, audit `migration.vendor` and the duplicate-target key now read one
vendor, and the key matches what Flyway is handed. The caller's or provider's
config is never mutated. Because the copy is made in the shared path, a caller
that loops `MigrateFor` itself, the pattern the rejected `MigrateAll`
interceptor (#1690) points to, is covered too. `TenantResult.Vendor` reports the
resolved vendor; it stays `""` only when neither the tenant nor the runner is
typed, or when the config could not be resolved.

**`MigrateAll` refuses a migrator identity it cannot deliver.** With
`MigrateAllOptions.MigratorIdentity` set, a tenant whose resolved vendor is
neither `postgresql` nor `oracle` is refused before the duplicate-target claim
and before any Flyway process, for migrate, validate and info alike. Its
`TenantResult.Err` wraps the new `migration.ErrMigratorIdentityUndeliverable`
and names only the tenant ID and the resolved type, `%q`-quoted so an empty type
shows as `""`, with the remedy: set the tenant's `type` to `postgresql` or
`oracle`. It never names the host, username, password or connection string. A
refused tenant was dispatched and failed, so `Verdict()` is `ErrFleetSplit`
under [ADR-115](adr_115_fleet_migration_run_verdict.md); fail-fast stops there
and `ContinueOnError` runs the rest. Since an undeliverable vendor is never
keyed, refusing before the claim changes no claim outcome. This is what fixes
the overlay on the CLI, whose runner is untyped.

Unchanged: `dbVendor` and `safeVendorSegment` semantics; an untyped runner with a
type-less tenant, and a non-empty unsupported type (`mysql`, `postgres`), export
nothing and, with no overlay, run as today; tenants typed `postgresql` or
`oracle`; no signature, config key, `MigrateAllOptions` or `MigrateAllResult`
field moves.

## Alternatives considered

- **Refuse every type-less tenant.** Rejected: it contradicts
  [ADR-050](adr_050_connectionstring_type_inference.md), under which whether an
  empty `type` is fatal depends on who connects, and the CLI's unarmed posture,
  and it refuses fleets that run correctly once the environment agrees with the
  URL.
- **Resolve the vendor only in `MigrateAll`.** Rejected: a caller looping
  `MigrateFor` would keep the mismatch.
- **Scrub the parent's `DB_*`/`ORACLE_*` from Flyway's environment.** Rejected:
  conf files read them on purpose.

## Consequences

- **Breaking.** Under a PostgreSQL runner a type-less tenant now connects with
  its own or the overlay's credentials, not the conf's or the parent's. Under an
  Oracle runner it gets `ORACLE_HOST/PORT/PDB` from the tenant, which changes the
  target for a conf that interpolates `${env.ORACLE_*}`. A type-less
  `connectionstring` tenant under a PostgreSQL runner still gets no `-url` but
  now receives `DB_*`, empty fields included (`DB_HOST=`, `DB_PORT=0`), which
  override the parent's.
- With an overlay, a tenant whose vendor resolves to neither, including an
  untyped DSN with an unrecognized scheme, is refused: ADR-050's tolerance of
  that DSN yields to the overlay. `go-bricks-migrate` gets this at its routine pin bump
  and exits `1`.
- ADR-143's type-less blind spot is closed: the tenant is keyed as a typed
  tenant is. On Oracle the conf's `flyway.url` owns the target, so the key
  matches it only where that URL interpolates `${env.ORACLE_*}`; a fixed URL
  can put tenants keyed apart on one target without
  `ErrDuplicateMigrationTarget`.
- A non-empty, non-canonical type (`postgres`, `PostgreSQL`) with no overlay
  keeps its current behavior; that is #2145.

## References

- `migration/flyway.go` — `runFor`, `typedWithVendor`, `dbVendor`,
  `buildEnvironmentVariables`
- `migration/multi_tenant.go` — `runOne`, `ErrMigratorIdentityUndeliverable`,
  `MigrateAllOptions.MigratorIdentity`, `TenantResult.Vendor`
- [ADR-143](adr_143_duplicate_migration_target_refusal.md) — the duplicate-target
  key whose type-less blind spot this closes
- [multi_tenant_migration.md](multi_tenant_migration.md#migrator-identity)
- See [migrations.md](migrations.md) `[C73.1]`.
