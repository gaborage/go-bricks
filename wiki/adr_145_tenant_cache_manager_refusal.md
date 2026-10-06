# ADR-145: A Tenant Cache Refuses a `manager` Block

**Status:** Accepted
**Date:** 2026-10-05

## Context

One cache manager serves every tenant, and it reads only the root block:
`app/bootstrap.go` sets `configBuilder.cacheConfig = cfg.Cache.Manager`. A static
tenant's `multitenant.tenants.<id>.cache` decodes into the same `CacheConfig`
type, so it carries a `Manager` field too. `config.Validate` accepted a
`multitenant.tenants.<id>.cache.manager` block, and for every enabled tenant
cache `normalizeTenantCache` → `normalizeCache(cache, true)` →
`applyCacheManagerDefaults` filled its `IdleTTL` and `CleanupInterval`. Nothing
read the result. Setting `multitenant.tenants.<id>.cache.manager.maxsize`
changed nothing, and nothing said so. Fixes #2046.

The database already draws this line: `normalizeDatabaseSection`
(`config/database_section.go`) refuses a `manager` block outside the primary
database with `database.manager.* is only supported on the primary database`.
CONTEXT.md's **Placement** entry says Placement decides whether a `manager`
block is allowed; for caches nothing enforced it.

## Decision

**`config.Validate` refuses a tenant cache `manager` block.** In
`normalizeTenantCache`, a tenant `Manager` that differs from the zero
`CacheManagerConfig{}` fails with:

- `Category`: `invalid`
- `Field`: `multitenant.tenants.<id>.cache.manager`
- `Message`: `cache.manager.* is only supported on the root cache`
- `Action`: `remove the manager block from multitenant.tenants.<id>.cache; tune
  the shared pool via cache.manager.*`

The block is judged as written, before any fill, and whether or not the tenant
cache is enabled: a disabled tenant cache's block was just as unread. The
comparison is against the zero value, the same test the database's
`DatabaseManagerConfig.isSet()` makes, so the mirror is exact.

**Tenant manager blocks are no longer filled.** `normalizeTenantCache` now
applies only the Redis and `loadtimeout` defaults `normalizeCache` applied, not
`applyCacheManagerDefaults`. A validated tenant cache keeps a zero `Manager`,
so a second `Validate` over the same `Config` passes.

**Only a tenants block the deployment consumes is judged.** The check runs in
the static tenant walk, which `normalizeMultitenant` enters only under
`multitenant.enabled: true` with `source.type: static` and a non-empty
`multitenant.tenants` map. A leftover block under single-tenant mode is inert,
as ADR-051 already holds for tenant databases, and a dynamic source never
enters the walk.

**The root `cache.manager` is unchanged:** accepted and filled as before
(`idlettl` 15m, `cleanupinterval` 5m, `maxsize` 100 single-tenant, 0 kept in
multi-tenant so the pool scales to `multitenant.limits.tenants`).

## Alternatives considered

- **Honor a per-tenant manager.** Rejected: there is one cache pool, and its
  size, idle TTL and cleanup interval are pool-wide; a per-tenant value has
  nothing to attach to.
- **Keep filling and WARN.** Rejected: a WARN does not stop a deployment that
  believes it tuned a tenant's pool, and the database precedent refuses.
- **Refuse only an enabled tenant cache.** Rejected: a disabled tenant cache
  reads its manager block no more than an enabled one, and judging it as
  written keeps the rule one sentence.

## Consequences

- **Breaking.** A config file, overlay, `MULTITENANT_TENANTS_<ID>_CACHE_MANAGER_*`
  variable or hand-built `Config` that sets a static tenant's
  `cache.manager.*` to a non-zero value now fails `Validate` at startup. The block never had an
  effect, so deleting it changes no runtime behavior.
- A tenant block whose every leaf is an explicit `0` equals the zero value and
  passes, as it does for the database.
- A negative tenant manager value used to fail inside the fill, and only for
  an enabled tenant cache, with a root-style field such as
  `cache.manager.maxsize`; it now fails as a tenant `manager` block either way.

**Out of scope:**

- Dynamic-source tenant configs resolve at runtime and never reach
  `config.Validate`, and the #2044 connect door carries `Manager` untouched,
  so a dynamic tenant's `manager` block stays unread and unrefused.
- `config` and `cache.NewCacheManager` disagree on `cleanupinterval`
  (`cache/manager.go` replaces a non-positive value where config refuses a
  negative one); this change does not touch it.
- How the root manager is built.

## References

- `config/multitenant_section.go` — `normalizeTenantCache`,
  `normalizeMultitenant`, `hasStaticTenants`
- `config/database_section.go` — `normalizeDatabaseSection`, the database
  precedent
- `config/defaults.go` — `applyCacheManagerDefaults`
- `app/bootstrap.go` — `configBuilder.cacheConfig = cfg.Cache.Manager`
- `config/multitenant_section_test.go` —
  `TestValidateRefusesTenantCacheManagerBlock`,
  `TestValidateLeavesTenantCacheManagerUnfilled`,
  `TestValidateFillsRootCacheManagerDefaults`
- [ADR-051](adr_051_delivered_empty_database_identity.md) — the inert tenants
  block and the dynamic-source blind spot
- gaborage/go-bricks#2046
- See [migrations.md](migrations.md) `[C72.25]`.
