# ADR-041: Shared (Control-Plane) Ledger Tenancy for Outbox/Inbox

**Status:** Accepted
**Date:** 2026-07-23
**Ships in:** the first release *after* v0.53.0 — the Context below describes
v0.53.0 behavior; `outbox.tenancy` / `inbox.tenancy` are not available in
v0.53.0 or earlier.

## Context

At v0.53.0 the outbox relay and inbox retention cleanup hard-fail startup when
`multitenant.enabled: true` unless a **static** `multitenant.tenants` map
exists; `source.type: dynamic` is rejected outright. Those guards (PR #581)
exist so the relay/cleanup jobs never *silently* fail to deliver: the tenant
set they fan out across must be enumerable at job-registration time, which a
dynamic source cannot provide.

Issue #758 reports a legitimate topology with no supported mode: a
**pool-model** service — one shared database, tenant identity carried as a
data column rather than a separate schema/instance — that needs
`multitenant.enabled: true` purely for HTTP tenant resolution (so
`multitenant.GetTenant(ctx)` works in handlers) and wants **one shared
outbox/inbox ledger**, written and relayed once, never fanned out per tenant.
Today its only options are `outbox.enabled: false` (losing the outbox
entirely) or a config lie: a decorative static `multitenant.tenants` entry
that exists only to satisfy the guard. That workaround additionally exposes
control-plane resources to any caller who sends
`X-Tenant-ID: <that-decorative-key>` — a synthetic tenant ID an attacker can
guess or discover, granting them a data-plane resolution path into
infrastructure that was never meant to be tenant-addressable.

**Design provenance:** the direction was chosen by the operator and then
challenged by a 5-lens adversarial panel (framework-boundary, security,
API-minimalism, outbox-correctness, ops/DX). The rulings below are baked into
the design and are not re-litigated by this ADR.

## Decision

Add an explicit per-module tenancy mode: `outbox.tenancy` / `inbox.tenancy`,
one of `"per-tenant"` (default, unchanged fan-out behavior) or `"shared"`. In
shared mode the relay/cleanup run a **single pass** against the control-plane
database and broker resolved via the **empty key** (`""`) — the same key the
built-in `config.TenantStore` already maps to the root `database:`/
`messaging:` YAML blocks, and which HTTP tenant resolution can never produce
(`multitenant.SetTenant(ctx, "")` is a no-op; `GetTenant` returns `ok=false`
for empty — `multitenant/context.go`). Default (`per-tenant`) behavior is
byte-for-byte unchanged; `Tenancy` is additive-only.

### 1. This is a framework capability, not a documented workaround

The blocker is framework code (the #581 guards) with no clean consumer
escape hatch — a consumer cannot patch around a fail-fast `Init()` error
without forking the module. A `shared` tenancy mode closes the gap with the
same fail-fast discipline the #581 guards already established, rather than
asking pool-model operators to lie to the config validator.

### 2. No new public `ModuleDeps` resource accessors

Resolving the control-plane ("" key) DB/messaging resources reuses
`App.dbManager`/`App.messagingManager` — but the panel explicitly rejected
adding public `SharedDB`/`SharedMessaging` fields to `ModuleDeps` or new
methods on `ResourceProvider`. Both would be permanent, general-purpose API
surface for a narrow, module-internal need. Instead, the two ledger modules
receive the resolvers through an **unexported duck-typed setter**,
`sharedResolverSetter` (`app/interfaces.go`), mirroring the existing
`declarationSetter` precedent used for messaging declarations:

```go
type sharedResolverSetter interface {
	SetSharedResolvers(
		db func(context.Context) (database.Interface, error),
		msg func(context.Context) (messaging.AMQPClient, error),
	)
}
```

`App.RegisterModule` type-asserts every registered module against this
interface and injects the resolvers **before** `registry.Register` runs
`Init` — injection order matters because `Init`'s shared-mode checks need
the resolvers already present. `sharedDBResolver`/`sharedMessagingResolver`
mirror `SingleTenantResourceProvider.DB`/`Messaging` exactly (same
`acquireLease` lease-scope registration, ADR-032), except the messaging
resolver is **publisher-only** — it never calls `EnsureConsumers` (see
point 4).

Injection happens for *every* registration of the outbox/inbox modules,
regardless of configured tenancy — the closures are simply unused unless
`tenancy: shared`. A module constructed directly (bypassing
`app.RegisterModule`) and configured for shared tenancy fails `Init` with an
actionable error naming `app.RegisterModule` as the fix.

### 3. Shared-mode publishes require a framework-originated transaction

`dbtypes.Tx` is an opaque interface (`database/types/interfaces.go`) — it
exposes `Query`/`Exec`/`Commit`/`Rollback` but no vendor or connection
identity. Nothing about a `dbtypes.Tx` value lets the framework verify which
physical database a caller's transaction targets. In shared tenancy this
matters more than in per-tenant mode: if a caller began a transaction on
*any other* database (a tenant DB, a differently-configured named DB) and
called `deps.Outbox.Publish(ctx, tx, event)`, the outbox row would be
written there — a database the shared relay never polls. The event is not
merely delayed; it is **permanently, silently lost**, reintroducing exactly
the class of failure the #581 guards exist to prevent, just one layer
deeper (co-location instead of enumerability).

A docs-only contract ("always use the control-plane DB in shared mode")
cannot be verified by the framework and would degrade to hope. Instead:

- `RunInSharedTx(ctx, fn)` begins a transaction on the shared database
  (`p.module.getDB`, which `Init` has already swapped to the injected shared
  resolver) via `database.WithTx`, and wraps the resulting `tx` in an
  unexported marker type, `sharedTx{ dbtypes.Tx }` (embedding, so all
  `Tx` methods are promoted transparently).
- `Publish` in shared tenancy type-asserts the incoming `tx` to `*sharedTx`
  and rejects anything else with an error naming `RunInSharedTx` and
  explaining the silent-loss risk. The check is `p.module.sharedLedger()`
  gated, so it is **completely unreachable, zero behavior delta**, in the
  default per-tenant tenancy.
- `RunInSharedTx` itself is gated the other way: it errors in per-tenant
  tenancy, so it cannot be used to accidentally bypass per-tenant isolation.

This converts an entire class of silent, undetectable data loss into a loud,
first-publish compile-adjacent error: a caller either uses the sanctioned
entry point or gets told exactly why not, on the very first attempt.

`RunInSharedTx` is exposed to applications only via an exported assertion
interface, `app.SharedTxRunner`, implemented by the outbox's internal
publisher type:

```go
type SharedTxRunner interface {
	RunInSharedTx(ctx context.Context, fn func(ctx context.Context, tx dbtypes.Tx) error) error
}

// Usage:
if r, ok := deps.Outbox.(app.SharedTxRunner); ok {
    err = r.RunInSharedTx(ctx, func(ctx context.Context, tx dbtypes.Tx) error {
        // business writes + deps.Outbox.Publish(ctx, tx, event) — same tx
        return nil
    })
}
```

This keeps `app.OutboxPublisher`'s method set unchanged (no apidiff break)
while giving shared-tenancy consumers a discoverable, type-safe entry point.

### 4. Consumers on the shared broker are explicitly out of scope

This ADR ships the **publisher** side only. `DeclareMessaging` consumers
still start per-tenant as before; shared tenancy does not declare or start
any consumer against the `""` key. A deployment must not declare consumers
intended to drain the shared-ledger's exchange without separately bootstrapping
that — `messagingManager.EnsureConsumers(ctx, "", decls)` is a plausible
follow-up but is deliberately not built here, since no concrete deployment
has asked for it yet (YAGNI). File a follow-up issue if one does.

### 5. Relay/cleanup/init logs stamp the tenancy mode

`Relay.logCycle` adds `.Str("tenancy", "shared")` when the relay was
constructed with `shared: true`; both modules' `Init` success logs add
`.Str("tenancy", m.cfg.Tenancy)`. Operators reading logs from a fleet mixing
per-tenant and shared deployments can tell which mode produced a given line
without cross-referencing config. `Cleanup` (outbox and inbox) emits no
per-cycle log at all — both delegate to `multitenant.FanOutRetentionCleanup`
— so no field was added there; doing so would be unread, dead code
(staticcheck).

## The `""` key contract

A custom `app.Options.ResourceSource` (replacing the built-in
`config.TenantStore` for **all** keys, not just tenant IDs — see
`app/factory_resolver.go`) that is used together with `outbox.tenancy: shared`
or `inbox.tenancy: shared` **MUST** resolve `DBConfig`/`BrokerURL` for the
empty key (`""`) to the deployment's control-plane resources. The empty key
is never a tenant — HTTP tenant resolution can never produce it, and no
resolver in `multitenant/` will ever assign a real tenant that identifier.
A custom resource source that returns per-request or per-tenant data for
`""` breaks the shared-ledger contract silently: the relay would poll
whatever `""` happens to resolve to at call time, which may not be the same
database business writes landed in.

## Alternatives Considered

### Option A: A decorative static tenant entry (status quo workaround, Rejected)

Add a single fake `multitenant.tenants` entry pointing at the shared
database, satisfying the #581 guard without any framework change.

**Rejected because:** the fake tenant ID is attacker-reachable via
`X-Tenant-ID: <that-key>` — any caller who learns or guesses the decorative
key gains a legitimate-looking tenant-scoped resolution path into
control-plane resources that were never meant to be tenant-addressable.
This is a real privilege-escalation surface, not merely an ergonomic wart,
and the framework should not document it as the sanctioned path.

### Option B: Add a `TenantLister` for periodic dynamic re-enumeration (Deferred)

Let dynamic sources supply a `TenantLister` interface the relay polls each
cycle to re-discover the current tenant set, turning "not enumerable at
registration time" into "re-enumerable every cycle."

**Deferred, not rejected:** this solves a different problem — a **silo**
dynamic-multi-tenant deployment (one DB per tenant, tenants added/removed at
runtime) that still wants automatic per-tenant fan-out. Issue #758 is a
**pool** model (one shared DB) that wants no fan-out at all; a `TenantLister`
would not address it. Reopen only if a genuine silo-model dynamic user
appears — building it speculatively for this issue would be scope creep
(YAGNI).

### Option C: Add a tenant-id column to the outbox/inbox tables (Rejected)

Track which tenant's data-plane row a shared-ledger event belongs to via a
new schema column, so the relay could theoretically fan out even in shared
mode.

**Rejected because:** it is a schema change to a table the framework
auto-creates and that operators may have already provisioned via managed
migrations — a real, resistant-to-add capability wanting a "shared, no
fan-out" primitive. Tenant identity does not need a first-class ledger
column: outbox events already carry arbitrary headers/payload (the outbox
schema is deliberately unchanged), and the inbox's `Record` already persists
`TenantID` (`inbox/inbox.go`'s `ProcessOnce` resolves it from ctx via
`multitenant.GetTenant` and stores it, unconditionally of tenancy mode).
Consumers that need tenant identity read it from the header/payload/record
they already have.

### Option D: A docs-only "always use the shared DB" contract (Rejected)

Document that shared-tenancy consumers must originate their transaction from
the shared database, without a compile/runtime-enforced marker.

**Rejected because:** this reintroduces exactly the silent-orphan class
the #581 guards exist to prevent, just relocated from "no enumerable tenant
set" to "no verifiable transaction origin." `dbtypes.Tx` gives the framework
no way to check compliance, so a violation would surface only as an
inexplicably-missing event, discovered (if ever) far downstream of the
mistake. See point 3 above for the chosen alternative.

## Consequences

### Positive

- Pool-model deployments (one shared DB, `multitenant.enabled: true` for
  HTTP resolution only) can now use the transactional outbox and the
  consumer-side inbox without a config lie or losing the feature entirely.
- The synthetic-tenant workaround's attacker-reachable resolution path is
  closed by removing the reason to reach for it.
- `RunInSharedTx`'s marker enforcement makes ledger/business co-location a
  build-time-adjacent guarantee rather than an operator-trust convention.
- Zero behavior change for every existing per-tenant and single-tenant
  deployment: `Tenancy` defaults to `"per-tenant"`, and every new guard is
  gated on `m.sharedLedger()`, which is `false` unless explicitly configured.

### Negative / Accepted Trade-offs

- **Shared ledger raises data classification/PCI scope to the maximum across
  all tenants sharing it.** A single control-plane database now holds events
  for every tenant using the shared ledger; whatever compliance boundary
  applies to the most sensitive tenant's data effectively applies to the
  whole ledger. This is an inherent property of the pool model the operator
  already chose (one shared database), not something this feature adds, but
  it is worth stating explicitly: shared tenancy does not give per-tenant
  data segregation at the ledger layer.
- **Resolution is identification, not authorization** (the same caveat
  ADR-039 states for tenant resolvers generally): the framework resolving
  key `""` to the control-plane database says nothing about whether the
  *caller* is authorized to trigger writes that land there. The deployment
  must still authorize the resolved tenant/request at the application layer;
  this ADR does not add an authorization boundary.
- **No startup connection probe.** Shared tenancy (like dynamic per-tenant
  sources before it) does not verify the `""` key resolves to a live,
  reachable database/broker at `Init` time — the first relay cycle surfaces
  any resolution failure as a loud job-level error instead. This preserves
  the existing dynamic-source startup semantics (no synchronous external
  call during startup) at the cost of a later failure signal.
- **First shared relay cycle after cold start may log one broker-outage
  cycle.** The connection pre-warmer only runs in single-tenant mode
  (`!a.cfg.Multitenant.Enabled` — `app/lifecycle.go`), so a shared-tenancy
  deployment (which necessarily has `multitenant.enabled: true`) gets no
  pre-warm. The first relay cycle may observe a cold/not-yet-ready AMQP
  client and log a connectivity-outage cycle before the client finishes
  connecting. This is a one-time, self-resolving startup artifact, not a
  sustained failure.
- **Shared + multitenant-disabled is a no-op by design.** Setting
  `tenancy: shared` with `multitenant.enabled: false` changes nothing
  observable: single-tenant mode already resolves everything via the empty
  key, so the shared resolvers and the default resolvers hit the exact same
  manager call with the exact same key. This is intentional — it gives
  operators **env-parity**: the same `outbox.tenancy: shared` YAML value
  works unchanged whether a given environment currently runs single-tenant
  (dev) or multi-tenant (prod), rather than requiring an env-conditional
  config fork.

## Related ADRs

- [ADR-032: Lease/Refcount Per-Tenant Resource Handles](adr_032_lease_refcount_tenant_handles.md) —
  the lease-registration pattern (`acquireLease`) the new shared resolvers
  reuse verbatim from `SingleTenantResourceProvider`.
- [ADR-033: Bounded Publish Retries + Status-Driven Outbox Dead-Lettering](adr_033_outbox_retry_count_status_parking.md) —
  the outbox's dead-lettering/parking semantics, unchanged by this ADR and
  identical in shared and per-tenant tenancy.
- [ADR-039: Require an Explicit Composite Tenant Resolver Order](adr_039_composite_resolver_order.md) —
  the "identification, not authorization" caveat this ADR's Consequences
  section cross-references; both ADRs agree the framework's tenant-resolution
  layer does not substitute for an application-level authorization check.
