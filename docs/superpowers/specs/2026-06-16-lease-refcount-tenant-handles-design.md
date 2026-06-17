# Lease/Refcount Per-Tenant Handles — Design (issue #606, M3 deeper fix)

**Date:** 2026-06-16
**Issue:** [#606](https://github.com/gaborage/go-bricks/issues/606) — eliminate the eviction-while-in-use race across the three per-tenant resource managers.
**Predecessor:** PR #605 (non-breaking: close evicted handles outside the lock + under-provision WARN). This is the breaking deeper fix.
**Breaking:** yes → ADR-032 + `wiki/migrations.md` row + `fix!:` PR title.

## Problem

`cache.CacheManager`, `database.DbManager`, and `messaging.Manager` cache one handle per
tenant key in an LRU. `Get()`/`Publisher()` hand out the **bare handle**; eviction (LRU at
capacity) and idle cleanup later `Close()` that **same handle** with **no reference counting**.
A request on tenant X obtains the handle, then enough other tenants' `Get()`s push X to the LRU
tail and evict+`Close()` it **while X's request is still mid-operation** → `redis: ErrClosed`,
`sql: database is closed`, or a publish failure. #605 narrowed the window (close outside the
lock) but did not close it.

## Why "in use" spans the whole unit of work, not a single call

The three handles are long-lived, concurrency-safe, **shared** objects (a pool / client). The
refcount counts *all current borrowers*, not one exclusive owner. Critically, `db.Begin(ctx)`
returns a `Tx` holding a pooled connection across many statements **outside** the manager's view,
so a lease cannot be released per-operation — it must be held until the borrower is fully done
(end of HTTP request / AMQP message / scheduler job). This rules out per-method-call refcounting.

## Decision: context-scoped auto-release (approved)

Apps do **not** change. `deps.DB/Cache/Messaging` and `ResourceProvider` signatures are
**unchanged**. The framework installs a lease *scope* in `context.Context` at each unit-of-work
boundary and releases all leases when the unit completes. Breaking surface is confined to the
three raw-manager signatures.

## Components

### 1. `internal/leasescope` (new)

```go
type Scope struct { mu sync.Mutex; releases []func() }
func (s *Scope) Add(release func())   // append under mu; release slice nil until first Add
func (s *Scope) ReleaseAll()          // call every release once, in LIFO; idempotent, clears slice

func Install(ctx context.Context) (context.Context, *Scope) // park *Scope under a private key
func FromContext(ctx context.Context) (*Scope, bool)
func Register(ctx context.Context, release func())          // FromContext → Add, else release() now
```

The `releases` slice is `nil` until the first `Add`, so a unit of work that touches no managed
resource pays only the tiny struct (ADR-026 frugality). For HTTP the `Install` is folded into
`RequestEnrich`'s existing `WithContext` clone — **no extra per-request allocation**.

### 2. Manager refcount + deferred close (×3: database, cache, messaging publishers)

Each entry gains `refs int` and `detached bool` (guarded by the existing manager mutex).

- `Get()`/`Publisher()` → `(handle, ReleaseFunc, error)`. On hand-out, `refs++` under the lock.
  `ReleaseFunc` is a `sync.Once`-guarded closure (per-lease idempotent) that decrements `refs`
  and, **if `detached && refs==0`, closes the handle outside the lock**.
- Eviction / idle-cleanup / `Close()` set `detached=true` and remove the entry from map+LRU
  **immediately** (new `Get()`s create a fresh handle); they close **now** only if `refs==0`,
  otherwise the **last** `ReleaseFunc` closes it. Reuses #605's collect-then-close-outside-lock.
- Double-create race path: the redundant freshly-created handle is closed outside the lock (as
  today); the surviving entry's `refs` is incremented for the returning caller.

`type ReleaseFunc func()` is defined per manager package (clear godoc; no cross-package coupling).

### 3. `app/resource_provider.go`

Each accessor calls the manager, then `leasescope.Register(ctx, release)`:
- scope present → lease auto-released at the boundary;
- no scope → `release()` immediately = today's behavior (non-leaking, **unprotected**), so
  uncovered paths (health/prewarm/startup probes, key `""`) never regress.

Signatures unchanged. Framework-internal direct callers (`app/health.go`, `app/prewarm.go`,
`app/app_builder.go`) adapt to the new manager signature and release immediately.

### 4. Three install seams (cover six unit-of-work types via ctx inheritance)

| Seam | File | Covers |
|---|---|---|
| HTTP | `server/request_enrich.go` (folded into the clone) | HTTP requests |
| AMQP message | `messaging/registry.go:690` `processMessage` (install at entry, `ReleaseAll` in the existing defer) | AMQP consumers **+ inbox `ProcessOnce`** (runs inside a handler, inherits ctx) |
| Scheduler job | `scheduler/module.go:610` `executeJob` (install after ctx creation, `ReleaseAll` in defer) | Scheduler jobs **+ outbox relay + inbox cleanup** (run as jobs; per-tenant `SetTenant` children inherit the scope) |

## Data flow

acquire (`refs++`) → `Register` in scope → handler uses handle (incl. `Begin`/`Tx` spanning the
unit) → boundary `ReleaseAll()` (`refs--`) → if the entry was evicted mid-unit, the **last**
release closes it. An in-use handle is never `Close()`d. Race closed.

## Error handling / edge cases

- **Double release**: `sync.Once` per lease; `ReleaseAll` is also idempotent.
- **Late release after manager `Close()`**: entry carries a `closed` guard; a late decrement is a
  no-op (handle already closed during `Close()`).
- **No scope in ctx**: immediate release (documented limitation; matches pre-#606 behavior).
- **Panic in handler/job**: `ReleaseAll` runs in the existing `defer` (after recovery), so leases
  are always returned.

## Testing (TDD)

Per manager: refcount inc/dec; detached entry not closed until `refs==0`; eviction-while-leased
defers close (concurrent cross-tenant evictor cannot close a leased handle); idle-cleanup-while-
leased defers close; `Close()` with outstanding leases; double-release safety; no-scope immediate
release. `leasescope` unit tests (lazy slice, LIFO, idempotent, immediate-release fallback).
Accessor+middleware integration test proving the documented attack (concurrent evictor) can no
longer close an in-use handle. All under `-race`.

## Breaking change artifacts

- **ADR-032** — `wiki/adr_032_lease_refcount_tenant_handles.md` + index entry in
  `wiki/architecture_decisions.md` + bump the "ADR-001 through ADR-031" numbering note.
- **`wiki/migrations.md`** row (Before/After for the three `Get()`/`Publisher()` signatures).
- Wiki deep-dive notes in `wiki/{database,cache,messaging}.md` on lease lifecycle.
- PR title: `fix!: lease/refcount per-tenant resource handles to close the eviction-while-in-use race (#606)`.
