# ADR-045: Resource Managers Expose No Producer-Side Interface

**Status:** Accepted
**Date:** 2026-08-04

## Context

`cache/types.go` declared an exported `Manager` interface alongside the concrete
`*cache.CacheManager`. Nothing in go-bricks implemented it, nothing consumed it, and
`*CacheManager` did not satisfy it — the concrete `Stats()` returns the typed
`cache.ManagerStats` while the interface demanded `map[string]any`, so
`var _ cache.Manager = (*cache.CacheManager)(nil)` would not have compiled had anyone written
it. That one return type is the whole of the incompatibility; the concrete `Get`'s `key`
parameter and its extra `Remove(key string) error` are drift from the documented shape but do
not affect satisfaction, since Go ignores parameter names and tolerates extra methods.

The drift was invisible for two compounding reasons: no compile-time assertion existed
anywhere in the package, and the interface had zero consumers, so nothing ever
type-checked against it. The type doc on `CacheManager` asserted the opposite — "implements
the Manager interface" — which is how the false claim survived review (corrected in #859).

The deeper question the drift exposed is whether a per-tenant resource manager should
carry a consumer-facing interface at all. The two sibling managers answer it implicitly:
`database.DbManager` and `messaging.Manager` are both plain structs with no interface seam,
and `app/` consumes all three concretely (`app/app.go`, `app/health.go`, `app/bootstrap.go`).
That the three managers are meant to behave alike is not an invention of this ADR: #859
independently made `CacheManager`'s zero value fail closed *"matching `database.DbManager` and
`messaging.Manager`"*, arriving at the same "treat the three as one family" principle from a
different direction.
`cache.Manager` was the only one of the three that had ever grown one, and it grew one that
nothing could use.

## Decision

**Delete `cache.Manager`. Resource managers in go-bricks expose no producer-side interface.**

The operative rule is narrower than the heading and should be read as the binding form: **no
exported interface without a client.** A manager that one day acquires two consumers needing
genuinely different subsets has an Interface Segregation case, and this ADR is not a standing
ban on ever answering it — it rejects the interface that had no consumer at all.

Interface seams in this framework sit on three axes, and a manager type is on none of them:

1. **Manager inputs** — `cache.ConfigProvider`, `cache.Connector`, `database.DBConfigProvider`,
   `messaging.BrokerURLProvider`. These exist so a test can inject configuration and
   construction without a live backend. `Connector`'s own doc states the purpose: "This
   abstraction allows dependency injection for testing."
2. **The leaf resource** — `cache.Cache`, `database.Interface`, `messaging.AMQPClient`. This is
   what application code actually calls, and what `cache/testing.MockCache` substitutes.
3. **The app boundary** — `app.ResourceProvider`, which deliberately hands out leaf
   interfaces rather than manager types.

Interface Segregation does not argue for a manager interface here, despite the surface
resemblance. ISP prevents forcing a *client* to depend on methods it does not use; a manager
interface with no client segregates nothing. The framework's own worked example (`Client` vs
`AMQPClient`, [ADR-008](adr_008_database_testing_interface_segregation.md)) is two interfaces
over one implementation precisely because two different consumers need different subsets — the
segregation is driven by consumers that exist.

The alternative — keeping the interface and repairing its signature — was evaluated and
rejected on three counts. It costs the *same* breaking change: `apidiff` classifies
`Manager.Stats: changed from func() map[string]any to func() ManagerStats` as Incompatible
exactly as it does `Manager: removed`, so both variants require the `!` PR title, the
migrations hop atom, and the minor bump. It buys nothing, because every substitution need is
already served twice — `Connector` on the input side and `MockCache` on the leaf side
(`app/health_test.go` builds a real `*CacheManager` with a failing connector and wants no
interface). And repairing it means either re-weakening `Stats()` to `map[string]any` — a
type-safety regression that breaks `app/health.go`'s `convertCacheStatsToMap` — or re-typing
the interface to `ManagerStats`, at which point it is a strict-subset shadow of the concrete
method set with no abstraction value.

**Consumers that want a manager seam declare it themselves.** A narrow, consumer-side
interface is the Go convention and is strictly better here: it names only the methods that
consumer calls, and it cannot drift from the producer, because the compiler checks it at the
point of use.

```go
type cacheGetter interface {
    Get(ctx context.Context, key string) (cache.Cache, cache.ReleaseFunc, error)
}
```

## Consequences

**Breaking, and a real one despite having no in-repo implementations.** Although no go-bricks
type could satisfy `cache.Manager`, a consumer's own adapter could — a wrapper over
`*CacheManager` converting `ManagerStats` back to a map compiled cleanly against the
pre-v0.56.0 interface, and was verified to do so before the removal. From v0.56.0 onward,
consumers naming the type get `undefined: cache.Manager` and migrate per
[wiki/migrations.md](migrations.md) (`[C56.9]`).

**Removal, not a shim.** Per the Developer Manifesto's backward-compatibility rule, the
obsolete path is removed rather than aliased or deprecated in place.

**The `//nolint:revive` directive on `CacheManager` stays, with a new reason.** revive's
stutter rule fires on `cache.CacheManager` regardless of whether an interface named `Manager`
exists, so the directive remains load-bearing; only its stated justification ("Manager is the
interface name") became false and was reworded. Renaming `CacheManager` to `Manager` — which
deletion now makes possible — is deliberately *not* done here: it is a separate exported-API
break touching five `app/` files (`app.go`, `health.go`, `internal_types.go`, `managers.go`,
`resource_provider.go`), and warrants its own decision. That split is not free, and the cost
falls on the consumer: from their seat, removing `Manager` and renaming `CacheManager` to
`Manager` are one cleanup, so splitting them across releases means two compile breaks and two
migration atoms for a single idea. It is accepted here because the two changes have different
blast radii — this one touches `cache/` alone, whereas the rename touches nine `CacheManager`
type references under `app/` (sixteen if it also carries the `NewCacheManager` constructor and
`CreateCacheManager` factory, as it likely would) — and because a self-contained, independently
reviewable PR is the stronger constraint.

**The `go doc` usage example was preserved, not lost.** The deleted interface's doc comment
was the only place in the package carrying the `NewCacheManager` + ADR-032 `defer release()`
lease idiom; it moved onto the `CacheManager` struct doc.

**No compile-time assertion is added for cache, because there is no longer an interface to
assert against** — the drift class is eliminated at the root rather than guarded. Issue #862
framed the assertion as the thing that mattered, but an assertion only helps if someone writes
one. The precise invariant is narrower than "the interface has a consumer": Go checks a
producer against an interface only where that concrete type is actually *converted* to it —
assigned, passed as an argument, or asserted. A consumer taking a `cache.Manager` parameter
proves nothing on its own; what proves compatibility is a call site handing `*CacheManager` to
it. `cache.Manager` had neither, which is why the drift went unseen, and it is also why the
`var _ Iface = (*Impl)(nil)` assertion has real value: it manufactures that conversion in one
line, at the producer, where no call site is required to exist. A survey of the 75 exported
non-test interfaces at the time of this decision found no other interface with zero consumers,
so removing this one empties that class rather than merely thinning it — though "has a
consumer" is, per the above, a weaker guarantee than "is compile-time checked against its
implementation."

Assertions remain worthwhile in one place this ADR does *not* address: the duck-typed
provider interfaces (`app.OutboxProvider`, `app.KeyStoreProvider`, `app.JobProvider`), which
the framework discovers by runtime type assertion. A signature drift there silently *disables*
a feature instead of failing the build — a different and more dangerous defect class than the
inert dead code removed here, and one worth its own change.

**No DB schema migration, no config key changes.** Go API surface only.

See [wiki/cache.md](cache.md) for the cache architecture and
[wiki/architecture_decisions.md](architecture_decisions.md) for the ADR index.
