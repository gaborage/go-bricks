# ADR-054: A cache the framework cannot construct aborts startup

- **Status**: Accepted
- **Date**: 2026-08-07
- **Related**: [ADR-011](adr_011_redis_cache.md), [ADR-046](adr_046_cache_readiness_strict_default.md), [ADR-047](adr_047_database_absence_vs_misconfiguration.md), [ADR-049](adr_049_debug_endpoints_fail_closed.md)

## Context

`ResourceManagerFactory.CreateCacheManager` swallowed its only failure:

```go
manager, err := cache.NewCacheManager(cacheOptions, cacheConnector)
if err != nil {
    f.logger.Warn().Err(err).Msg("Failed to create cache manager, cache will be disabled")
    return nil
}
```

The failure it swallowed is narrow. The connector cannot be nil —
`FactoryResolver.CacheConnector` always falls back to `newRedisConnector` — so what
remains is a negative `cache.manager.maxsize` or `cache.manager.idlettl`. Those reach
`NewCacheManager` deliberately; `BuildCacheOptions` says so in the code:

```go
// Not resolveMaxSize: cache.NewCacheManager rejects negatives (unlike the
// db/messaging managers, which coerce), so a negative must pass through and
// fail loudly there instead of being silently swallowed into a live pool.
```

The immediate caller swallowed it. The comment described an intent its own caller defeated.

**Where that was actually reachable** decides how much this matters, and it is not where
the shape suggests. `config.Validate` runs `applyCacheManagerDefaults`, which already
rejects a negative `maxsize`/`idlettl`/`cleanupinterval` — so a YAML or env deployment
loaded through `config.Load` with `cache.enabled: true` never got here at all. Two paths
did:

- **`app.NewWithConfig` with a hand-assembled `*config.Config`** — including anything
  supplied through `Options.ConfigLoader`. That constructor hands the config straight to
  the builder and never calls `config.Validate`, so nothing checks the pool values and
  every failure mode of `NewCacheManager` landed on the swallowed `Warn`.
- **`cache.enabled: false` with a leftover `cache.manager.*` value** — `validateCache`
  returns early for a disabled cache, so the defaults applier never runs and a negative
  passed through untouched.

One input reaches the same failure without naming a `cache.manager.*` key at all: in
multi-tenant mode `BuildCacheOptions` substitutes `multitenant.limits.tenants` for an unset
`maxSize`, so a negative tenant limit becomes a negative pool size.
`validateMultitenantLimits` clamps that for `config.Load`, which leaves it reachable on the
same unvalidated path as the rest. The error therefore reports the resolved `maxsize` and
`idlettl` rather than only the key names, so it cannot point an operator at a key they
never set.

What the resulting `nil` costs is larger than a missing cache.
[ADR-046](adr_046_cache_readiness_strict_default.md) made the cache probe critical by
default precisely so that a cache outage answers `503` and drains the replica from
rotation. A cache that failed to *construct* skipped that entirely: `createHealthProbes`
registers a cache probe only when the manager is non-nil, so with no manager there was no
probe, `/ready` reported the cache `disabled`, and the pod answered `200`. The strict
default was bypassable by breaking the cache badly enough that nothing was left to probe.

Absence and breakage had collapsed into the same `nil` — the conflation
[ADR-047](adr_047_database_absence_vs_misconfiguration.md) untangled for the database.

`CreateCacheManager` and `NewResourceManagerFactory` are both exported, so the bare `nil`
also escapes the framework. A consumer sees no error, holds a
`(*cache.CacheManager)(nil)`, and panics on first use — the zero-value guards added in
\#859 cover `&cache.CacheManager{}`, not a nil pointer.

## Decision

**`CreateCacheManager` returns `(*cache.CacheManager, error)`, and a cache the framework
was told to build but could not build aborts startup.**

The error travels to the composition root rather than being decided in the factory:
`appBootstrap.dependencies` returns it and `Builder.ResolveDependencies` records it in
`b.err`, exactly like every neighbouring build step. The factory reports; the composition
root decides.

The `Warn` is gone. Returning the error *and* logging it would report the same fact twice
on two channels.

The grade is scoped to construction. Reaching the cache stays best-effort:
`Builder.preInitCache` still logs a WARN and continues when Redis is unreachable at boot,
because that is a runtime condition and the readiness probe is the right instrument for
it. And a service with `cache.enabled: false` and no stale tuning values never fails
`NewCacheManager` at all, so running without a cache is still silent and supported.

## Alternatives considered

**Keep returning `nil`, promote the `Warn` to a fatal inside the factory.** Narrower, and
it needs no signature change. Rejected: it puts the abort decision inside an exported
constructor. A library constructor that terminates the process is a worse contract than
one that returns an error, and it leaves a consumer calling `CreateCacheManager` directly
with no way to choose.

**Coerce a negative to the default, as the database and messaging managers do.** Rejected
on the evidence already in the tree: `BuildCacheOptions` deliberately declined to coerce,
and documented why. A negative pool size is a typo, and coercing it hides the operator's
mistake behind a working service.

**Extend `validateCache` to check `cache.manager.*` even when the cache is disabled.**
This would catch the second reachable path earlier, with a cleaner error naming the key.
Rejected as a *substitute*: it does nothing for `app.NewWithConfig`, which never validates
at all, so the bare `nil` would survive on the path where it is most reachable. It remains
a reasonable independent follow-up.

## Consequences

**Positive.** A cache misconfiguration fails at boot instead of at the first
cache-dependent request — the Fail Fast principle applied where it had been skipped. The
exported API stops handing out a bare `nil` that panics on use. ADR-046's
critical-by-default posture is no longer bypassable by producing no manager to probe.

**Negative.** This breaks an exported signature, so a consumer calling
`CreateCacheManager`, or driving `appBootstrap` directly, must adopt the two-value form.
And it converts one previously inert config shape into a startup failure: a deployment
with `cache.enabled: false` that still carries a negative `cache.manager.maxsize` booted
fine before — the cache was off anyway — and now refuses to start. That is the concrete
upgrade hazard; `[C58.3]` in [migrations.md](migrations.md) carries the pre-bump check and
the one-line fix.

**Neutral.** The sibling factories are unchanged. `CreateDatabaseManager` and
`CreateMessagingManager` call constructors that cannot fail, so they keep their single
return value; this is not a symmetry break to repair later. The `disabled` cache status in
`/ready` also stops being reachable through `app.New`/`app.NewWithConfig` — it now
describes only a directly-assembled `App` — and [cache.md](cache.md) records that.

## References

- \#861 — the report, found during the correctness review of \#859
- `app/managers.go` (`CreateCacheManager`, `BuildCacheOptions`)
- `app/bootstrap.go` (`dependencies`)
- `app/app_builder.go` (`ResolveDependencies`, `preInitCache`)
- `app/app.go` (`createHealthProbes`) · `app/readiness.go` (`cacheProbe`; before [ADR-066](adr_066_readiness_one_module.md): `app/health.go`'s `cacheManagerHealthProbe`)
- `config/validation.go` (`validateCache`, `applyCacheManagerDefaults`)
- `cache/manager.go` (`NewCacheManager`)
- [migrations.md](migrations.md) `[C58.3]`
