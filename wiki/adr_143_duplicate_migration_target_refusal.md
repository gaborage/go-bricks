# ADR-143: A Fleet Run Refuses a Tenant Whose Migration Target Another Tenant Claimed

**Status:** Accepted
**Date:** 2026-10-04

> **Amended (2026-10-17, [ADR-149](adr_149_typeless_tenant_runs_with_runner_vendor.md)):**
> a type-less tenant now runs as a copy typed with the runner's vendor, so the
> environment builder reads the same vendor as the key and the type-less blind spot
> below is closed. With `MigratorIdentity` set, a tenant whose vendor resolves to
> neither no longer runs unkeyed as today: it is refused with
> `ErrMigratorIdentityUndeliverable` before the claim.

## Context

`MigrateAll` resolves each tenant's `*config.DatabaseConfig` through the
`database.DBConfigProvider`, applies the `MigrateAllOptions.MigratorIdentity`
overlay to a copy, and runs `MigrateFor` / `ValidateFor` / `InfoFor`. No step
compared one tenant's target with another's. Triage of #1731 reproduced three
cases on main:

1. Two tenants on one host, port and database, an empty `postgresql.schema` and
   one shared `MigratorIdentity`, run sequentially.
2. The same pair with `Parallelism: 4`.
3. Two tenants with the same explicit `postgresql.schema` and no identity.

In each case both Flyway runs got the same `-url` and schema flags, both tenants
succeeded, and `Verdict()` was nil. The harm is a false green, not corruption:
the second tenant's run finds the shared history table at head and reports
success, so that tenant's real schema is never migrated. In the shared-migrator
empty-schema case the migrator's default schema (typically `public`) collects
the application tables, so the first tenant is a false green too.

The only guard was `(*FlywayMigrator).WithSharedMigrator()` (#1737): a
pre-flight for the empty-schema case, armed only by a library caller, and not
reachable from `go-bricks-migrate` until #1730. It cannot see copy-pasted tenant
documents sharing an explicit schema, a `MigratorIdentity` set without the flag,
a lister returning one ID twice, or alias tenants.

## Decision

**Each `MigrateAll` call owns one mutex-guarded claim set, shared by the
sequential and parallel runners.** A tenant claims its target after its config
resolves and after the `MigratorIdentity` overlay, before any Flyway verb runs.
The first claimant proceeds. A later tenant whose key is already claimed starts
no Flyway process, for migrate, validate and info alike; its `TenantResult.Err`
wraps the exported sentinel `migration.ErrDuplicateMigrationTarget`. The set is
neither a package global nor a `FlywayMigrator` field, so two separate
`MigrateAll` calls against one tenant are unaffected and keep relying on
Flyway's own lock.

**The key is read from the effective, post-overlay config:**

- **PostgreSQL with a framework-built `-url`** (discrete host and database, no
  `connectionstring`): `(host, port, database, schema)` when `postgresql.schema`
  is set, else `(host, port, database, effective username)`. The username is
  mandatory in the empty-schema key: a dedicated per-tenant migrator role
  carries its own role-level `search_path` (`ProvisionPGRoles`), so two tenants
  in one database with distinct migrator roles and an empty schema migrate
  different schemas and keep passing.
- **Oracle:** `(host, port, PDB, effective username)`. Oracle's schema is the
  connecting user. This arm is an exception to the conf-owned boundary below:
  [ADR-085](adr_085_framework_owned_flyway_url.md) leaves every Oracle target
  conf-owned (no framework-built `-url`, no schema flags), so Flyway sees the
  tenant only through the `ORACLE_*` variables the run exports to the shared
  `-configFiles` conf. It stays keyed because every keyed field is one of those
  exported variables, read by one conf for every tenant of the run: tenants that
  share a key share whatever target the conf resolves, so the key can only err
  toward admission, and it still catches the collisions where the conf
  interpolates `${env.ORACLE_*}`. The key sees only `database.*`, never a target
  part the conf or an inherited `FLYWAY_*` variable sets (see Blind spots).
- **Not keyed, runs as today:** a PostgreSQL `connectionstring`, a block with no
  host or database, and a vendor that resolves to neither (a type-less tenant
  under a runner with an empty `Database.Type`). This is the conf-owned boundary
  [ADR-085](adr_085_framework_owned_flyway_url.md) and
  `ErrIncompleteMigrationTarget` already draw, less the Oracle exception above.
  With `MigratorIdentity` set, the last of these is refused instead with
  `ErrMigratorIdentityUndeliverable`
  ([ADR-149](adr_149_typeless_tenant_runs_with_runner_vendor.md)).

The key reuses the run path's own vendor resolution (`dbVendor`) and
URL-ownership decision (`usesFrameworkOwnedURL`) rather than re-implementing
them; since [ADR-149](adr_149_typeless_tenant_runs_with_runner_vendor.md) the
environment builder reads the same resolved vendor.

**Normalization:** the host is lowercased and IPv6 brackets are stripped; a
PostgreSQL port of 0 equals 5432, the driver default; database, schema and
username compare byte-exact, because PostgreSQL role names are case-sensitive at
connect time. DNS is never resolved.

**Messages** name both tenant IDs and never the host, username or password — a
host can carry a whole DSN, and the overlay credential is fleet-wide:
`tenant "t2" resolves to the same migration target as tenant "t1"; …`, or
`tenant "t1" is listed more than once; …` for a repeated ID. Both say the fix is
configuration (a distinct `postgresql.schema` or migrator role, or de-listing
the alias), since a re-run collides again.

**Verdict, unchanged under [ADR-115](adr_115_fleet_migration_run_verdict.md):**
a refused tenant was dispatched and failed, so `Verdict()` is `ErrFleetSplit`.
The listing is not deduplicated, so `Listed()` and `NeverDispatched` keep their
meaning. Under fail-fast `MigrateAll` returns the refusal; under
`Parallelism > 1` it joins fail-fast cancellation like any other failure, which
usually cancels the in-flight claimant. Under `ContinueOnError` every later
collider is refused. Sequentially the claimant is the first tenant in listing
order; in parallel it is whichever tenant reaches the claim first, so which
tenant carries the refusal depends on scheduling, and the verdict does not.

**Claimant corner:** a claimant that then fails its own pre-flight or Flyway run
still holds its key, so its colliders are still refused. The sentinel's godoc
names this.

**Blind spots** (false negatives unless noted):

- conf-owned targets and an unresolvable vendor, as above — including a tenant
  ID listed twice whose target is one of them: the claim is per target, and
  the listing is not deduplicated;
- DNS aliases: a CNAME, or an IP against a name;
- an Oracle port of 0 against 1521;
- Oracle's case-insensitive unquoted user and service names (`app` against
  `APP`), compared byte-exact;
- an Oracle tenant whose target the conf or the environment sets: the key reads
  only `database.*`, so tenants keyed apart are admitted onto one target when
  the conf fixes `flyway.url`, or interpolates only some of
  `${env.ORACLE_HOST}`, `${env.ORACLE_PORT}` and `${env.ORACLE_PDB}`; when it
  fixes `flyway.user`; when a parent `FLYWAY_URL` or `FLYWAY_USER`, inherited by
  every run, overrides the conf, even a fully interpolated one; or when the conf
  or the environment sets `flyway.schemas` or `flyway.defaultSchema`
  (`FLYWAY_SCHEMAS`, `FLYWAY_DEFAULT_SCHEMA`), which then picks the schema that
  holds the history table (`defaultSchema`, else the first of `schemas`) in place
  of the connecting user's;
- distinct PostgreSQL roles with no role-level `search_path`: all resolve to
  `public` through `$user, public`, but are keyed apart by username;
- a type-less tenant under a typed runner, until
  [ADR-149](adr_149_typeless_tenant_runs_with_runner_vendor.md): the environment
  builder switched on the tenant's own `type`, so none of its connection variables
  were delivered and its key could differ from Flyway's target. The tenant now
  runs typed with the runner's vendor and is keyed as a typed tenant is (on
  Oracle, the conf-set target bullet above applies);
- two separate `MigrateAll` calls, which rely on Flyway's own lock;
- in the shared-migrator empty-schema case, the first tenant's own false green.

No new option, hook or callback: `MigrateAllOptions`, `MigrateAllResult` and
`Verdict()` keep their signatures (#1690 rejected a `MigrateAll`
interceptor).

## Alternatives considered

- **A pre-pass that refuses every collider before the first claimant runs, or
  deduplicating the listing.** Rejected: either changes ADR-115's
  `NeverDispatched` / `ErrNothingAttempted` semantics, which classify strictly
  by dispatch.
- **An opt-out option or alias escape hatch.** Rejected: two tenants sharing a
  target is never a correct fleet run, and an opt-out reintroduces the false
  green.
- **Resolving DNS.** Rejected: a network lookup in a pre-flight, with answers
  that differ by resolver and over time, for a check that must be
  deterministic.
- **Keying PostgreSQL conf-owned targets, or treating an empty schema as
  `public`.** Rejected: the framework never reads `flyway.conf` or the role's
  `search_path`, so any such key would be a guess. The Oracle arm is not such a
  guess: it is built only from the variables the run exports, so it can miss a
  shared target but never invent one.
- **Inferring the check from `MigratorIdentity` or `WithSharedMigrator()`.**
  Rejected: an explicit-schema collision needs neither.

## Consequences

- **Breaking.** A fleet run that is clean today because two tenants share a
  target — copy-pasted documents, a repeated ID, alias tenants, or a shared
  migrator with an empty schema in one database — now ends `ErrFleetSplit`,
  and the colliding tenant carries `ErrDuplicateMigrationTarget`.
- The classification is per tenant: the refused tenant's schema is untouched,
  and only the configuration fixes it.
- `go-bricks-migrate` exits `1` on a collision once its go-bricks pin is bumped
  (a routine `chore(deps)`); until then it keeps the old behavior.
- The check complements, and does not replace, `WithSharedMigrator()` (#1737)
  and its CLI exposure (#1730): neither depends on the other, and only that
  guard catches the first tenant of a shared-migrator empty-schema fleet.
- The provisioning path that migrates one tenant per process
  (`provisioning.Steps.Migrate`, or a direct `MigrateFor`) has no run to scope
  a claim set to and is unchanged.

## References

- `migration/target_claim.go` — `migrationTargetFor`, `targetClaims`
- `migration/multi_tenant.go` — `ErrDuplicateMigrationTarget`, `MigrateAll`,
  `duplicateTargetError`
- [ADR-018](adr_018_multi_tenant_migration_cli.md) — the fleet migration CLI
  and library entry point
- [ADR-085](adr_085_framework_owned_flyway_url.md) — the framework-owned URL
  boundary the key follows
- [ADR-115](adr_115_fleet_migration_run_verdict.md) — the dispatch-based verdict
  the refusal lands in
- gaborage/go-bricks#1731 · #1737 (`WithSharedMigrator`) · #1730 (CLI exposure)
- See [migrations.md](migrations.md) `[C72.14]` and
  [multi_tenant_migration.md](multi_tenant_migration.md#duplicate-targets).
