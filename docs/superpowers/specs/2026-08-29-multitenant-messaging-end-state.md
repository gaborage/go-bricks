# Multi-tenant messaging end state — design (2026-08-29)

Settled in the triage grill of #1230 (three rounds, maintainer decisions recorded per
question). This document is the authority the plans for #1230 (A), #1232 (B) and #1231 (C)
argue from. Sub-decisions the issue briefs settled count as settled here too.

## Deployment facts

- **Silo tenants** — one database per tenant, resolved from a dynamic
  `app.Options.ResourceSource` (`DBConfig(ctx, tenant)`); the consumer side holds pools in
  the `DbManager` LRU (`database.manager.maxsize`, idle sweep). Thousands of tenants; the
  LRU is sized by tenants ACTIVE per consumer instance, not by the total.
- **One control-plane broker.** `multitenant.enabled: true` for HTTP resolution only; the
  resource source resolves `BrokerURL` for the control-plane key `""` and for no tenant.
- **Producer systems are platform services** acting for every tenant. They publish
  `customer.created`, `payment_instrument.created`, …; the consuming service must route
  each event to the right tenant database.
- **Ordering matters.** A payment instrument cannot be created before its customer.

## RabbitMQ constraints that decide the design

- A connection is bound to one vhost: N tenant vhosts = N connections per consumer
  instance × replicas. Today's per-tenant broker model (`multitenant.tenants.<id>.messaging`,
  one client per key, replayed lazily) IS vhost-per-tenant, is broken for consume-only
  services (#1230), and does not survive thousands of tenants.
- vhosts are not free (metadata, processes, definitions, per-vhost users/policies):
  hundreds fine, thousands painful. Quorum queues are Raft groups: tens of thousands not
  designed for — per-tenant queues × per-service consumers hits that fast.
- Topic permissions authorize by routing-key regex, never by header, and only bite when
  producers hold per-tenant credentials, which platform services do not.
- Super streams partition by murmur3 hash of a routing key: fixed partition count, order per
  partition, single active consumer per partition, zero per-tenant broker objects. go-bricks
  ships this lane (ADR-059/063), single-tenant only today.

## Three concerns the "tenant in a header" idea conflates

| Concern | header stamp | routing-key segment | vhost per tenant |
| --- | --- | --- | --- |
| Routing (which DB) | yes | yes | yes |
| Isolation (noisy neighbour, per-tenant backpressure/DLQ) | none | only with per-tenant queues | full, does not scale |
| Authorization (may this producer speak for tenant X) | none | topic permissions, needs per-tenant credentials | full |

**Stated goal for ADR-087: zero broker objects per tenant** — no vhost, queue, user or
permission is created when a tenant is onboarded. Onboarding touches the database side only.

## Decisions

1. **Tenant identity travels as a tenant stamp** — the `x-tenant-id` carrier entry (AMQP
   header on the classic lane, application property on the streams lane), written by the
   producer's framework from its authenticated context, never copied from a payload. The
   routing key stays the application's. The consumer reads the stamp as identification, not
   authorization (ADR-039).
2. **The framework is the stamp's only writer.** Outbox `Publish` persists it beside the
   trace keys; classic direct publishes and the streams `Publisher` stamp it from ctx. No
   tenant in ctx → no stamp (a control-plane event). A caller-supplied `x-tenant-id` is a
   publish error on both lanes wrapping one sentinel (the same error value re-exported by
   `messaging` and `messaging/streams`). The dead `tenantAwarePublisher` goes.
3. **The messaging KIND has a tenancy**, `messaging.tenancy: per-tenant | shared` (default
   per-tenant, unchanged), reusing `config.TenancyPerTenant`/`TenancyShared`. Under
   `shared` + `multitenant.enabled`: declared consumers replay once at boot against the
   control-plane key through the single-tenant grading branch itself (declared consumers
   that cannot start fail `Run`; none declared → WARN); `deps.Messaging(ctx)` resolves the
   control-plane publisher with no tenant required (a tenant in ctx is ignored, not an
   error); per-tenant replay never happens; the control-plane publisher is pre-warmed like
   single-tenant. With `multitenant.enabled: false`, `shared` is a no-op (same branch, same
   key — ADR-041 env-parity).
4. **Check rules.** A `multitenant.tenants.<id>.messaging` block beside shared messaging is
   a contradiction and fails check; the root `messaging:` block is legal beside static
   tenants under shared (it IS the control-plane broker). `checkMessagingStreams` accepts
   multitenant only under shared messaging tenancy.
5. **Read side, both lanes.** Under `multitenant.enabled` + `shared`, the consumer reads the
   carrier's stamp, validates it with the exported default tenant-id grammar
   (`^[a-z0-9-]{1,64}$`, the HTTP resolver's rule — #1004), and `SetTenant`s before the
   handler. Under per-tenant tenancy the replay key stays authoritative and a stamp is
   ignored. A per-consumer option lets a control-plane consumer run without a tenant; the
   default fails closed.
6. **Fail closed** on a missing or malformed stamp: classic lane nacks without requeue (DLQ
   if declared), handler not called, the lane's failure line names the reason and byte
   length — never the value; streams lane records today's skip outcome. Tenant EXISTENCE is
   proven at first `deps.DB(ctx)` — an unknown tenant is a handler failure.
7. **Accessor.** `multitenant.TenantID(ctx) (string, error)` returning
   `multitenant.ErrNoTenant`; same answer on every lane. `GetTenant` ok-form stays
   (`trace.IDFromContext` precedent); `SetTenant` stays exported — app code sets a tenant in
   tests, hand-built adapters and fan-out jobs.
8. **Ordered lane = super streams keyed by the tenant stamp** (partition key). Concretely,
   the publisher sets `streams.PublishMessage.RoutingKey` to the tenant stamp — that is the
   value murmur3-hashes to a partition (ADR-063) — while the application's own routing key,
   if it has one, stays an entry in `PublishMessage.Properties` beside the `x-tenant-id`
   stamp. The two never share a field: partition selection is the tenant's, application
   routing semantics are the payload's. Partition count is fixed at creation; growing later
   is a new super stream and a cutover; replicas
   beyond partitions idle. Documented rule: partitions = 2–4× max consumer replicas. Not
   enforced.
9. **Failure posture on the ordered lane: D then C.** D — bounded in-place retry with
   backoff, per-consumer `Retry{MaxAttempts, Backoff}`, every handler error retryable unless
   wrapped permanent (`delivery.Permanent(err)`). C — per-tenant **hold**: gate → park
   (offset committed only after a durable hold write; a failed hold write stalls the
   partition) → drain (scheduler job, per tenant in offset order through the same delivery
   pipeline, per-tenant backoff, one drainer per tenant across replicas) → release →
   visibility (gauges, max-age WARN, never auto-dropped). Ledger home: `inbox` on the
   control-plane database (`inbox.tenancy: shared` required). Ownership transfers to the
   ledger after the durable write; the hold insert is idempotent on (consumer, stream,
   offset). Classic lane untouched.
10. **Source ordering.** The outbox relay drains each ledger key-ordered (a monotonic
    per-ledger sequence; the first failed row for key K parks K's later rows for the cycle
    without marking them), runs on one relay instance per ledger at a time (leader lock, a
    dead leader releases without operator action), and gains a native streams leg using the
    existing confirmed `streams.Publisher` (ADR-063 murmur3 interop) — the 0.9.1 route to a
    super stream's direct exchange (`"0"…"n-1"` binding keys) was rejected for needing a
    hash reimplementation.
11. **Streams under per-tenant tenancy stay rejected** (one Environment per tenant does not
    exist). Lifting the gate under shared is A's; per-tenant streams are nobody's.
12. **Bookkeeping.** A: ADR-087 + one-line pointer at the top of ADR-041, no migrations
    atom, no `!`. B and C: their own ADRs, numbered by merge order after 087; B carries a
    migrations atom for its DDL.

## Glossary (`CONTEXT.md`, `### Tenancy`, inserted before `### Observability`)

**Control-plane key**:
The `""` key: the deployment's own resources — the root `database:` and
`messaging:` blocks, or whatever a custom resource source returns for `""`.
Never a tenant; no resolver can produce it.
_Avoid_: root key, empty key, default tenant, shared key

**Tenancy**:
Which key a resource kind is resolved and replayed under when multitenant is
enabled: `per-tenant` (the resolved tenant) or `shared` (the control-plane
key). The ledgers carry one; the messaging kind carries one.
_Avoid_: mode, scope, isolation, tenant model

**Replay**:
Applying validated declarations to one key — declare infrastructure, start
consumers — exactly once per key, idempotent on the declaration hash.
_Avoid_: bootstrap, setup, fan-out (the relay's per-tenant pass)

**Tenant stamp**:
The tenant identity a producer writes into the carrier from its authenticated
context — never copied from a payload. The consumer reads it as
identification, not authorization.
_Avoid_: tenant header, tenant tag, tenant id (for the carried value)

**Partition key**:
The value hashed to choose a super-stream partition; on the ordered lane it is
the tenant stamp, so one tenant's messages keep their order.
_Avoid_: routing key (the classic lane's word), shard key, hash key

**Hold**:
Per-tenant parking that keeps every later message for a tenant behind a failed
one until that one succeeds, so order survives a failure without stalling the
tenants that share its partition.
_Avoid_: DLQ (where a classic-lane message goes instead), quarantine, retry
queue, parking lot

## The carve

| Deliverable | Issue | Owns | Depends on |
| --- | --- | --- | --- |
| A | #1230 | `messaging.tenancy` config + checks; control-plane replay at boot; `deps.Messaging` under shared; stamp write on outbox `Publish`, classic and streams publishers; stamp read + `SetTenant` + fail-closed on both lanes; per-consumer tenant-optional; `multitenant.TenantID`, `ErrNoTenant`, exported tenant-id grammar (#1004); streams gate lifted under shared; ADR-087; glossary; docs | — |
| B | #1232 | outbox streams leg (lane marker, stream name, partition key = stamp); per-ledger sequence + key-ordered drain; relay leader lock; own ADR + atom | A (stamp) |
| C | #1231 | D (bounded retry, `delivery.Permanent`) + C (hold ledger in `inbox`, gate/park/drain/release/visibility, SAC-promotion reload, `Init` refusal on per-tenant ledgers); own ADR | A (stamp); independent of B |

Settled in the briefs: a tenant in ctx under shared is ignored; the fail-closed line carries
byte length and reason, never the value; a tenant-optional consumer with no stamp runs the
handler and `TenantID(ctx)` returns `ErrNoTenant`; B key-orders untenanted AMQP rows by
routing key, declares stream publishers lazily, adds no leader-lock key, documents the
`ALTER`; C's retry defaults and max-age are orders of magnitude, its drain lock a property.
