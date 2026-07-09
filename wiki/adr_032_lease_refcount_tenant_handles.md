# ADR-032: Lease/Refcount Per-Tenant Resource Handles to Close the Eviction-While-In-Use Race

**Status:** Accepted
**Date:** 2026-06-17

## Context

The three per-tenant resource managers — `cache.CacheManager`, `database.DbManager`, and
`messaging.Manager` — cache one handle per tenant key in an LRU. Before this change,
`Get()` / `Publisher()` handed out the **raw handle** and later `Close()`d that **same handle**
from LRU eviction or idle cleanup, with **no reference counting**.

That is the M3 finding from the 2026-06-10 audit (issue #606): a request on tenant X obtains
the handle, then enough other tenants call `Get()` to push X to the LRU tail; tenant X+n's
create evicts X and `Close()`s it **while X's request is still mid-operation** → Redis
`ErrClosed`, `sql: database is closed`, or a publish failure on the evicted AMQP client. At
fleet sizes ≥ `MaxSize` (and only **10** in single-tenant mode, where named databases share the
manager), every new-tenant `Get()` can evict a possibly-active tenant — sustained spurious 500s
/ publish failures.

[PR #605](https://github.com/gaborage/go-bricks/pull/605) was the proportionate non-breaking
mitigation: it moved the evicted-handle `Close()` **outside** the manager mutex (eliminating
cross-tenant head-of-line blocking) and added a startup WARN when a pool's `MaxSize` is below
the static tenant count. It narrowed the race window but did not close it — an evicted handle
could still be `Close()`d while a caller obtained earlier was still using it.

Why "in use" must span the whole unit of work, not a single call: the three handles are
long-lived, concurrency-safe, **shared** objects (a pool / client). A refcount counts *all*
current borrowers, not one exclusive owner. Critically, `db.Begin(ctx)` returns a `Tx` holding
a pooled connection across many statements **outside** the manager's view, so a lease cannot be
released per-operation — it must be held until the borrower is fully done (end of HTTP request /
AMQP message / scheduler job).

## Decision

Introduce **lease / reference counting** on the handle lifecycle so eviction defers `Close()`
until all current holders have released.

**Manager layer (breaking):** `Get()` / `Publisher()` now return
`(handle, ReleaseFunc, error)`. Each cache entry carries a `refs` count plus `detached`,
`closed`, and `seedHeld` flags, all guarded by the existing manager mutex.

- A lease is acquired atomically with the LRU lookup (`getExisting` increments `refs` under the
  lock), so a handle cannot be evicted-and-closed between resolution and the lease being taken.
- Eviction / idle-cleanup / explicit `Remove` **detach** the entry from the map+LRU immediately
  (a new `Get()` makes a fresh handle) but close it **now** only if `refs == 0`; otherwise
  `detached` is set and the **last** `ReleaseFunc` closes it.
- `ReleaseFunc` is a `sync.Once`-guarded closure (per-lease idempotent) that decrements `refs`
  and, if `detached && refs == 0`, closes the handle outside the lock.
- **Seed lease.** A brand-new entry is created with `refs == 1` and `seedHeld == true`. The seed
  keeps it alive through the window before its first caller claims it, so concurrent
  eviction/`Remove`/idle-cleanup can only *detach* (never close) it. The first `claimOrAcquire`
  takes the seed (that ref becomes its lease); concurrent singleflight waiters each increment
  `refs`. Because callers operate on the shared entry *pointer* (not a map re-lookup), no caller
  can "miss" the entry under churn — eliminating a retry-storm that a naive re-lookup design
  exhibits when `MaxSize` < concurrent distinct keys (e.g. `Get` racing `Remove`).
- Manager `Close()` closes every still-mapped handle regardless of `refs` (shutdown is terminal;
  ADR-029 stops inbound work first) and marks them `closed` under the lock so a late release
  cannot double-close.

**Activation layer (non-breaking for apps):** a new private `internal/leasescope` package carries
a `*Scope` in `context.Context`. The framework installs a scope at each unit-of-work boundary and
calls `ReleaseAll()` when the unit completes; the per-tenant accessors register each lease into
the active scope.

- `app/resource_provider.go` (`deps.DB/Cache/Messaging`) registers each lease, then returns the
  bare handle — **accessor and `ResourceProvider` signatures are unchanged, so applications do
  not change.**
- Scopes are installed at three seams, which cover six unit-of-work types via `context.WithValue`
  inheritance: **HTTP** (folded into `RequestEnrich`'s existing context clone — zero extra
  per-request allocation), **AMQP consumers** (`registry.processMessage`, which also covers inbox
  `ProcessOnce`), and **scheduler jobs** (`module.executeJob`, which also covers outbox relay and
  inbox cleanup whose per-tenant `SetTenant` children inherit the scope).
- When a context carries **no** scope (framework health/prewarm probes using the fixed `""` key,
  ad-hoc background work), the lease is released as soon as the caller's own check completes —
  non-leaking, but unprotected, identical to the pre-lease behavior. Those framework-internal
  direct callers release explicitly; the messaging pre-warm holds its publisher lease through a
  bounded readiness wait (`messaging.reconnect.readytimeout`) before releasing.

The `Scope`'s release slice stays `nil` until the first borrow, so a unit of work that touches no
managed resource pays only a tiny struct (consistent with ADR-026's zero-overhead request path).

## Consequences

**Breaking (intended):** the public return types of `DbManager.Get`, `CacheManager.Get`, and
`messaging.Manager.Publisher` change from `(handle, error)` to `(handle, ReleaseFunc, error)`,
where `ReleaseFunc = func()`. Direct callers of those raw managers must capture and invoke the
release. See `wiki/migrations.md`. **No application-facing change:** `deps.DB/Cache/Messaging` and
`ResourceProvider` are unchanged.

**Benefits:**
- An in-use handle is **never** `Close()`d — the M3 eviction-while-in-use race is closed for every
  concurrent multi-tenant path (HTTP, consumers, jobs, outbox relay, inbox).
- Leased entries can temporarily exceed `MaxSize` (they are detached, off-book) instead of being
  destroyed under an active caller, so an under-provisioned pool degrades to extra connection
  churn rather than correctness failures.
- The seed-lease hand-off makes acquisition robust under heavy concurrent eviction/`Remove` churn
  with no retry storm.

**Costs / limitations:**
- A `refs++`/`refs--` per borrow under the manager mutex (negligible; already on the lock path).
- Unscoped contexts are not protected (documented fallback). New framework entry points that
  borrow per-tenant handles concurrently should install a `leasescope.Scope` at their boundary.
- A leased-then-detached entry whose lease is never released (a stuck goroutine during shutdown)
  leaks one handle until process exit — acceptable, and bounded by graceful-shutdown ordering.

## Alternatives Considered

- **Grace period on eviction** (skip victims used within the last N seconds): simpler but only
  probabilistic — a request slower than the grace window still races. Rejected: does not meet the
  "fully eliminate" bar.
- **Explicit lease object returned to apps** (`deps.DB` returns a lease the caller must
  `Release()`): maximally explicit per the manifesto, but breaks every consuming app's call sites.
  Rejected in favor of context-scoped auto-release, which protects the documented attack path
  (concurrent HTTP/consumer/job borrows) with no application change.
- **Per-operation leasing via an intercepting wrapper**: cannot cover `Begin`/`Tx`, which holds a
  pooled connection across many statements outside the manager's view. Rejected.

## Related

- Issue #606 (this deeper fix); PR #605 (non-breaking M3 mitigation).
- [ADR-026](adr_026_zero_overhead_request_path.md) (zero-overhead request path — informs the
  scope-allocation budget).
- [ADR-029](adr_029_graceful_shutdown_order.md) (shutdown stops inbound work before teardown —
  why manager `Close()` may close leased handles).
