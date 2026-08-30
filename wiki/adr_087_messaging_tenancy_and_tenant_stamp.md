# ADR-087: The Messaging Kind Has a Tenancy, and the Tenant Travels as a Stamp

- **Status**: Accepted
- **Date**: 2026-08-30
- **Related**: [ADR-041](adr_041_shared_ledger_tenancy.md) (shared ledger tenancy; §4 deferred the consumer half this delivers) · [ADR-039](adr_039_composite_resolver_order.md) (tenant resolution is identification, not authorization — the rule this extends to the broker) · [ADR-059](adr_059_streams_consumption.md) / [ADR-063](adr_063_streams_native_publishing.md) (the streams lane, single-tenant until now) · [ADR-070](adr_070_inbound_trace_identifier_validation.md) (the sibling rule for an inbound identifier the framework must validate before trusting it)

## Context

A consuming service in the #1230 topology has one broker and thousands of tenants. Its
tenants are **silo** tenants — one database each, resolved from a dynamic
`app.Options.ResourceSource` — while the events it consumes are published by **platform
services acting for every tenant**: `customer.created`, `payment_instrument.created`. The
consumer must route each event to the right tenant database, and ordering matters, because
a payment instrument cannot be created before its customer.

Until this decision the messaging kind had exactly one multi-tenant shape: one client per
tenant, resolved from `multitenant.tenants.<id>.messaging` or the resource source, replayed
lazily. That shape is **vhost-per-tenant**, and it does not survive this topology:

- an AMQP connection is bound to one vhost, so N tenant vhosts cost N connections per
  consumer instance × replicas;
- vhosts are not free — metadata, processes, definitions, per-vhost users and policies.
  Hundreds are fine; thousands are painful;
- quorum queues are Raft groups. Per-tenant queues × per-service consumers reaches tens of
  thousands of them, which they are not designed for.

It is also broken for a consume-only service: such a deployment has no per-tenant broker to
name, so `multitenant.enabled: true` forced a configuration lie — tenant messaging blocks
that exist only to satisfy a check.

### Three concerns "put the tenant in a header" conflates

| Concern | header stamp | routing-key segment | vhost per tenant |
| --- | --- | --- | --- |
| **Routing** — which database this event belongs to | yes | yes | yes |
| **Isolation** — noisy neighbour, per-tenant backpressure and DLQ | none | only with per-tenant queues | full, does not scale |
| **Authorization** — may this producer speak for tenant X | none | topic permissions, and only with per-tenant credentials | full |

Broker-side authorization is the column worth dwelling on, because it is the one a header
appears to give up. Topic permissions authorize by routing-key regex, never by header — so
the routing-key column is the only one that could enforce anything, and only if producers
hold per-tenant credentials. Platform services do not: they publish for every tenant by
definition. Against this topology the authorization column buys nothing whichever mechanism
is chosen, so choosing the one that also costs nothing is not a compromise.

**Stated goal: zero broker objects per tenant.** Onboarding a tenant creates no vhost, no
queue, no user and no permission — it touches the database side only.

## Decision

**The messaging KIND has a tenancy, and the tenant travels as a stamp.**

1. **`messaging.tenancy: per-tenant | shared`**, defaulting to `per-tenant`, reusing
   `config.TenancyPerTenant`/`TenancyShared` — the same two values, and the same word, the
   ledgers already use (ADR-041). Under `shared` with `multitenant.enabled`, declared
   consumers replay ONCE at boot against the control-plane key through the single-tenant
   consumer branch itself, `deps.Messaging(ctx)` resolves the control-plane publisher with no
   tenant required, per-tenant replay never happens, and the publisher is pre-warmed as
   single-tenant is. With `multitenant.enabled: false`, `shared` is a no-op — same branch,
   same key — which keeps ADR-041's environment parity.

2. **The tenant travels as a tenant stamp**: the `x-tenant-id` carrier entry — an AMQP 0.9.1
   header on the classic lane, an AMQP 1.0 application property on the streams lane. The
   routing key stays the application's.

3. **The framework is the stamp's only writer.** Outbox `Publish` persists it beside the
   trace keys; both lanes' publish doors write it from the context tenant. A caller-supplied
   `x-tenant-id` is a publish error on both lanes — one sentinel value, re-exported as
   `messaging.ErrTenantStampConflict` and `streams.ErrTenantStampConflict` so `errors.Is`
   holds across lanes. **Equal is refused too**: a caller that happens to guess the resolved
   tenant is still claiming a field it does not own, and a rule that turned on what the
   framework resolved would be one a caller cannot check.

4. **Which tenant gets stamped** is decided in one place. The publishing context's tenant
   wins; when the context carries none, the pool key the client was created for is used —
   that key was itself resolved from an authenticated context at creation, so it is not a
   weaker source; when both exist and disagree, the publish is refused, because no precedence
   makes publishing for one tenant on another's client correct. Under shared tenancy the key
   is the control-plane `""`, so the context is the only source.

5. **Check rules.** A `multitenant.tenants.<id>.messaging` block beside shared messaging is a
   contradiction and fails check — nothing would ever read it. The root `messaging:` block
   becomes legal beside static tenants under shared, because it IS the control-plane broker.
   Stream consumption is refused only under PER-TENANT tenancy, which would need one
   `Environment` per tenant; shared consumes once on the control-plane key.

6. **The read side is one implementation, in the pipeline both lanes share.** Under
   `multitenant.enabled` + `shared`, the delivery pipeline reads the carrier's stamp,
   validates it against the exported default tenant-id grammar
   (`multitenant.DefaultTenantIDPattern()`, `^[a-z0-9-]{1,64}$` — the HTTP resolver's own
   rule, #1004), and `SetTenant`s before the handler. Under per-tenant tenancy the replay key
   is authoritative and a stamp is ignored. Owning this in the lanes would have meant two
   copies of one rule with nothing keeping them equal.

7. **Fail closed.** A missing or malformed stamp never reaches the handler: the classic lane
   nacks without requeue (DLQ if declared), the streams lane leaves the offset uncommitted,
   and the failure line names the reason and the byte length — **never the value**, which is
   producer-written and arrives unauthenticated. `ConsumerOptions.TenantOptional` lets a
   control-plane consumer run without a stamp; it never admits one that is present but
   unusable, and a stamp written as nil is present, not absent. Tenant EXISTENCE is proven at
   the first `deps.DB(ctx)` — an unknown tenant is a handler failure, not a delivery one.

8. **`multitenant.TenantID(ctx) (string, error)`** returning `multitenant.ErrNoTenant` gives
   every lane the same answer to "which tenant am I". `GetTenant`'s ok-form stays
   (`trace.IDFromContext` precedent) and `SetTenant` stays exported.

## Alternatives considered

- **A consumers-only knob (`messaging.consumers.tenancy`).** Rejected: it leaves the kind
  half-shared — publishers resolved per tenant while consumers resolve once — which is a
  configuration that cannot describe a real deployment.
- **Per-consumer tenancy.** Deferred: no deployment needs one consumer per-tenant beside
  another shared, and the knob would be reachable long before it was understood.
- **The routing-key segment as the contract.** Rejected: it is the only shape that could
  carry broker-side authorization, and only with per-tenant producer credentials, which
  platform services do not have. It also takes the routing key away from the application.
- **vhost per tenant, or per-tenant queues.** Rejected on the constraints above — connection
  per vhost, vhost cost, the quorum-queue ceiling.
- **`RequireTenant *bool` as a tri-state.** Rejected in favour of `TenantOptional bool`: Go
  has no "unset" for a plain bool, and the safe default must be fail-closed, so the
  zero-value-safe spelling is the one that cannot be got wrong.

## Consequences

**Positive.** A consume-only multi-tenant service configures what it actually has — one
broker — with no per-tenant blocks that exist only to satisfy a check, and no second
messaging manager wired by hand in `main`. Zero broker objects per tenant, so onboarding is a
database operation. The stamp is the carrier the ordered-source work (#1232) and the
per-tenant hold (#1231) both build on.

**Accepted costs.**

- **The stamp is identification, not authorization** (ADR-039's rule, one layer down). Under
  `messaging.tenancy: shared` the shared queue's **publish ACL is the tenant-isolation
  boundary**: anyone who can publish to it can act as any syntactically valid tenant — reach
  that tenant's database and cache through any handler, and stamp downstream publishes as it.
  The deployment authorizes the resolved tenant; the framework identifies it.
- **Every publish door must write the stamp.** Both of today's doors are pinned by tests, but
  the coupling is real: a third publish path added later would go unstamped unless it goes
  through the manager's stamping wrapper, which is why the wrapper wraps every client the
  manager hands out rather than depending on a concrete client type.
- **`AutoAck` discards a refusal like any other failure.** Under `AutoAck` the broker
  considers the message delivered before the handler runs, so a refused stamp is dropped
  rather than dead-lettered — the consumer's own choice, and the refusal still emits its
  ERROR line. A consumer that needs a refused delivery kept must not set `AutoAck`.
- **A stamp only helps once producers upgrade.** Until then a consumer under shared tenancy
  sees unstamped deliveries and refuses them, which is the fail-closed behaviour working, not
  a defect.
- **Under per-tenant tenancy a stamp is ignored.** A mixed fleet must know that the same
  message is routed differently by the two tenancies.
- **Shared tenancy pools tenants onto shared streams and partitions.** One tenant's poisoned
  message can impose bounded restart-replay cost on the tenants sharing its partition; the
  isolation is application-level, by the stamp, not transport-level.
- **Partition sizing is a documented rule, not an enforced one**: 2–4× the maximum consumer
  replica count. Growing later is a new super stream and a cutover.

## Pointers

- **#1232** builds the ordered source on this stamp: the outbox relay's native streams leg
  keys super-stream partitions by the tenant stamp (ADR-063's murmur3 interop).
- **#1231** builds the per-tenant hold on it: a failed delivery parks by tenant so ordering
  survives a failure without stalling the tenants that share its partition.
