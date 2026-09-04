# Outbox Architecture (Deep Dive)

This document covers GoBricks' built-in Transactional Outbox: the components that solve the dual-write problem, the at-least-once delivery guarantee (consumers MUST be idempotent), wiring patterns, and the production-safe defaults.

## Outbox Architecture

> Sealed events reach the outbox as bytes (`Publisher[T].Seal`, persisted-sealed);
> `outbox.Publish` refuses a seal-tagged struct payload. See [sealing.md](sealing.md).

GoBricks provides a built-in **Transactional Outbox** for reliable event publishing. It solves the dual-write problem: events are written to an outbox table in the **same database transaction** as business data, then reliably delivered to the message broker by a background relay.

**Core Components:**

- **Publisher**: Writes events to the outbox table within a database transaction
- **Relay**: Background poller (scheduler job) that publishes pending events to AMQP
- **Cleanup**: Scheduled job that removes published events after retention period
- **Store**: Vendor-agnostic SQL abstraction (PostgreSQL + Oracle)

**Delivery Guarantee:** At-least-once. Consumers MUST be idempotent. Use the `x-outbox-event-id` header for deduplication.

**Module Setup:**

```go
for _, m := range []app.Module{
    scheduler.NewModule(), // Required: relay runs as a scheduled job
    outbox.NewModule(),    // Outbox module
    &myapp.OrderModule{},
} {
    if err := fw.RegisterModule(m); err != nil {
        log.Fatal(err)
    }
}

// In your module:
func (m *Module) Init(deps *app.ModuleDeps) error {
    m.getDB = deps.DB
    m.outbox = deps.Outbox  // nil if outbox not configured (zero cost)
    return nil
}
```

**Business Logic Pattern (atomic write + event):**

```go
func (s *OrderService) CreateOrder(ctx context.Context, req CreateOrderReq) error {
    db, err := s.getDB(ctx)
    if err != nil { return err }

    tx, err := db.Begin(ctx)
    if err != nil { return err }
    defer tx.Rollback(ctx)

    // 1. Write business data
    _, err = tx.Exec(ctx, "INSERT INTO orders (id, customer_id) VALUES ($1, $2)",
        req.ID, req.CustomerID)
    if err != nil { return fmt.Errorf("insert order: %w", err) }

    // 2. Write event to outbox (SAME transaction — atomic!)
    payload, _ := json.Marshal(OrderCreatedEvent{OrderID: req.ID})
    _, err = s.outbox.Publish(ctx, tx, &app.OutboxEvent{
        EventType:   "order.created",
        AggregateID: fmt.Sprintf("order-%d", req.ID),
        Payload:     payload,
        Exchange:    "order.events",
    })
    if err != nil { return fmt.Errorf("outbox publish: %w", err) }

    return tx.Commit(ctx)
    // Event GUARANTEED to reach the broker eventually
}
```

**How It Works:**

1. `Publish()` writes an `OutboxRecord` to the outbox table within the caller's transaction, refusing an exchange, routing key (or the `EventType` an empty one falls back to) or header key past the AMQP shortstr limit (255 bytes) before the INSERT — a destination the broker can never accept is rejected at its source
2. The **relay job** (`outbox-relay` via scheduler) polls for pending events every `pollinterval`
3. Each pending event is published to the target AMQP exchange with `x-outbox-event-id` header
4. Successfully published events are marked as `published`
5. Failed events have their `retry_count` advanced and stay `pending` for the next cycle — on **every** failed attempt, including while the broker is unavailable; only a **poison** event (undecodable headers, or a destination the AMQP frame can never carry) is parked once `retry_count` reaches `maxretries` (see [Retry & Dead-Lettering](#retry--dead-lettering) below)
6. The **cleanup job** (`outbox-cleanup`) removes published events older than `retentionperiod`

**Configuration:**

```yaml
outbox:
  enabled: true
  tablename: gobricks_outbox       # Default table name
  autocreatetable: false          # Auto-create table on first use (default: false; enable for development only)
  defaultexchange: ""              # Fallback if Event.Exchange empty (≤255 bytes; Init fails otherwise)
  pollinterval: 5s                 # Relay poll frequency
  batchsize: 100                   # Events per relay cycle
  maxretries: 5                    # Dead-letter ceiling for POISON (undecodable headers, unpublishable destination) — see below
  publishtimeout: 60s              # Per-record publish bound (MUST be >= messaging connectiontimeout)
  retentionperiod: 72h             # Keep published events (0=disable cleanup)
```

**Event Struct:**

| Field | Type | Required | Description |
| ------- | ------ | ---------- | ------------- |
| `EventType` | string | Yes | Event routing key (e.g., "order.created") |
| `AggregateID` | string | Yes | Entity identifier for idempotency (e.g., "order-123") |
| `Payload` | any | No | Event data. `[]byte` stored as-is, otherwise JSON-marshaled. Nil is accepted and stored as JSON `null`. |
| `Headers` | map[string]any | No | Custom AMQP headers propagated to published message |
| `Exchange` | string | No | Target AMQP exchange (falls back to `defaultexchange` config) |
| `RoutingKey` | string | No | AMQP routing key (falls back to `EventType`) |
| `Stream` | string | No | Selects the native super-stream lane instead of an exchange. Must be listed in `outbox.superstreams`, requires a tenant in context (it becomes the partition key), and is mutually exclusive with `Exchange` and `RoutingKey`. See [Lanes and Ordering](#lanes-and-ordering). |

## Sealed Payloads

The outbox is **persisted-sealed only**: a sealed event is sealed *before* it is written, so
the ledger row already holds the wire form (the compact JWS from `Publisher[T].Seal`) and
the relay publishes bytes it never needs to open. `Publish` therefore refuses a struct or
pointer payload whose type carries `seal` tags with `outbox.ErrSealedPayloadNeedsBytes`
(`errors.Is`-able); the fix is to seal first and hand over the returned `[]byte`. A `[]byte`
payload is stored as-is whatever it contains — a hand-marshaled plaintext body is the
documented residual the guard cannot see — and a struct without `seal` tags is JSON-marshaled
as before.

**Sensitive Authentication Data never rides the outbox lane.** CVV/CVC, full track data and
PIN blocks may transit a sealed event, but PCI DSS forbids storing them after authorization
regardless of encryption, and an outbox row *is* storage. Keep SAD out of any event that is
outboxed; the framework's examples use PAN-class subjects only.

## Trace Propagation

Outbox publishes are **trace-equivalent to direct AMQP publishes**: the W3C trace
context (`traceparent` / `X-Request-ID`, plus `tracestate` when the inbound
request carries it) is propagated end-to-end so a single trace id spans the
originating HTTP request, the persisted outbox row, and the downstream
consumer's per-message log.

This requires capture at two points, because the relay runs as a *detached*
scheduled job whose context carries no inbound trace:

1. **`Publish` captures** the trace context from the publish `ctx` into the row's
   `headers` column — the only point where the originating request context is
   still live. Untraced publishes (background jobs with no trace in context) are
   left untouched and persist no synthetic trace headers.
2. **The relay rehydrates** that trace context from the persisted headers into the
   context it republishes with, so the AMQP `CorrelationId` (surfaced by the
   consumer's failure-path log and the consume span) and the re-injected
   `traceparent` all carry the originating trace id rather than a freshly
   generated one.

No application code is required — capture/rehydration is automatic. Custom
`Headers` you set on the event are preserved alongside the trace keys.

## Retry & Dead-Lettering

The relay advances a row's `retry_count` on **every** failed delivery attempt and keeps the
row `pending` so it is retried on a later cycle. Crucially, `retry_count` climbs even while the
broker is unavailable — that visible, monotonic count is the operator's signal that delivery is
being retried (a frozen `retry_count` here was the symptom [ADR-033](adr_033_outbox_retry_count_status_parking.md) fixes).

Whether the relay ever **gives up** on an event is decoupled from `retry_count` and driven by
the failure's class:

| Class | Causes | Behavior |
| --- | --- | --- |
| **Connectivity** | broker down / not ready, **broker NACK**, confirmation timeout, per-record `publishtimeout` elapsed, missing exchange (surfaces as a synthesized NACK) | `retry_count` advances; **never** dead-lettered. The event stays `pending` and delivers once the broker recovers or the config is fixed. |
| **Poison** | corrupt / undecodable headers, or a destination past the AMQP shortstr limit (`messaging.ErrInvalidPublishDestination`) — both deterministic, broker-independent failures | `retry_count` advances; once it reaches `maxretries` the event is **dead-lettered** to `status = 'failed'` and stops being retried. |

Consequences worth knowing:

- **A broker NACK is treated as connectivity, not poison.** A RabbitMQ `basic.nack` on a publish
  confirm is a *transient broker condition* (disk alarm, mirror resync, node failover), not a
  statement that the message is bad — so the event is retried (at-least-once), never auto-parked.
  A permanently mis-named exchange likewise surfaces as a NACK and keeps retrying, so it delivers
  the moment an operator creates the exchange. Auto-parking is reserved for deterministic,
  broker-independent failures: undecodable headers (which the framework essentially never produces)
  and a destination the AMQP frame cannot carry.
- **An over-long destination parks rather than retrying forever.** A row whose exchange, routing key
  (which carries the `EventType` when `RoutingKey` is empty) or header key exceeds 255 bytes is refused before any channel work, identically on every cycle, so
  it is poison: `retry_count` climbs and it is dead-lettered at `maxretries`. `Publish()` and the
  module's startup check on `outbox.defaultexchange` normally refuse such a destination first — a
  parked row means it reached the ledger another way (a hand-managed schema, a direct INSERT).
- **`maxretries` bounds poison only.** Connectivity failures (including a permanently-failing publish)
  retry indefinitely with a climbing `retry_count` — monitor that growth to catch a stuck event.
- **The relay bounds the error text it persists to 1 KiB.** A failed attempt records why in the
  row's `error` column (`error_msg` on Oracle), and that text comes from the broker or driver
  rather than from the framework. The bound sits in the relay, not in `Store`, so a hand-rolled
  relay writing that column directly is responsible for its own. Since a connectivity failure retries forever and rewrites the column
  every cycle, an unbounded message would be unbounded storage per retry on a table a service
  cannot drop. Longer text is **truncated**, not discarded — it is diagnostic and nothing keys
  on it, so a shortened error still says what went wrong — and a truncated value ends in
  `...[truncated]` so a reader can tell it from a short one. Control bytes are replaced with
  spaces (the column is read back into logs and dashboards, and a broker-supplied newline must
  not be able to forge a line there) and invalid UTF-8 is dropped, which PostgreSQL would
  otherwise reject outright — failing the UPDATE and leaving `retry_count` un-advanced.
- **One stuck record cannot starve the batch:** each publish is bounded by `outbox.publishtimeout`
  (default 60s). It **must be ≥ `messaging.reconnect.connectiontimeout`** (default 30s) — the module
  **fails to start** otherwise, because a shorter value truncates every legitimate confirmation into a
  connectivity failure and re-publishes the (already-delivered) event every cycle.
- **Underneath, the AMQP publish itself is bounded** by `messaging.reconnect.maxpublishattempts`
  (default 5), after which it returns `messaging.ErrPublishRetriesExhausted` wrapping the cause.
  Note the two ceilings interact on the relay path: with default `connectiontimeout` (30s) a
  stalled-confirmation worst case is `5 × 30s = 150s`, which exceeds the 60s `publishtimeout`, so
  the per-record deadline usually fires first and the relay observes `DeadlineExceeded` (still
  connectivity) rather than `ErrPublishRetriesExhausted` — keep that in mind when tuning the three
  knobs together.
- **A relay cycle that has pending work but cannot reach the broker returns a job error** (after
  advancing every record's `retry_count`), so the failure stays visible at the scheduler level and,
  in multi-tenant mode, names the affected tenant. An idle relay (nothing pending) is not an error.
- **`failed` rows accumulate:** `outbox-cleanup` purges only `published` events. Monitor and prune
  dead-lettered rows; they are intentionally never auto-deleted so they stay visible.

## Lanes and Ordering

Every row carries a **lane**. The default is `amqp`: the relay publishes it to an exchange, as
it always has. A row whose event named a `Stream` is on the `stream` lane and is published
through the native streams publisher for that super stream, partitioned by the row's tenant
stamp — the same murmur3 routing every RabbitMQ client uses ([streams.md](streams.md),
[ADR-063](adr_063_streams_native_publishing.md)).

```yaml
outbox:
  enabled: true
  superstreams: [customers]      # each name needs one DeclareStreams super stream
messaging:
  streams:
    uri: rabbitmq-stream://localhost:5552   # required once superstreams is set
```

```go
// A stream-targeted event. The tenant in ctx becomes the partition key, so a tenant is
// REQUIRED; Exchange and RoutingKey must be empty.
_, err := s.outbox.Publish(ctx, tx, &app.OutboxEvent{
    EventType:   "customer.created",
    AggregateID: customer.ID,
    Payload:     customer,
    Stream:      "customers",
})
```

Three refusals happen at `Publish`, where the developer sees them rather than as poison rows
cycles later: naming a stream beside an exchange or routing key, naming a stream absent from
`outbox.superstreams`, and publishing a stream target with no tenant in context.

**Ordering.** Rows drain in `seq` order — a per-ledger sequence the database assigns at insert
— and one relay instance per ledger drains at a time, holding the ledger's `<table>_leader`
row. When a row fails, the later rows of ITS key are parked for that cycle: not published, not
marked, `retry_count` untouched — so a cycle advances the count of every ATTEMPTED record,
not of every fetched one, keeping their place for the next cycle. The key is the tenant
stamp for a stamped AMQP row, the destination (exchange and routing key) for an unstamped one,
and the stream plus partition key on the stream lane. A dead-lettered row is terminal and a
delivered-but-unrecorded row was delivered, so neither parks anything behind it.

A stream producer that is not carrying messages holds back only ITS OWN stream's remaining rows
for that cycle, rather than each of them paying the publish deadline in turn. Each super stream
has its own producer, so a stall in one says nothing about the others: rows aimed at a healthy
stream, and every AMQP row, drain the same cycle.

The guarantee is **causal**, not global: a dependent event's transaction begins after its cause
committed, so its `seq` is higher. Two independent transactions may commit out of `seq` order
and the relay claims nothing between them.

### Managed migration (existing deployments)

`outbox.autocreatetable: true` applies all of this on the next start. A deployment that manages
its own schema runs these BEFORE deploying the new relay, in this order — the backfill after the
`ALTER` and before the index, so the index is built once over final values.

The backfill is **not optional**. Adding an identity column populates existing rows in the order
the rewrite reads them (heap order on PostgreSQL, rowid order on Oracle), which is not
`created_at` order; because the outbox updates pending rows, the divergence lands precisely on the
retried rows a backlog is made of. Skipping it drains the backlog once in an arbitrary order and
nothing reports that it happened.

```sql
-- PostgreSQL. Flyway runs a migration in ONE transaction, so the ALTER's ACCESS EXCLUSIVE lock
-- blocks writers from here through the sequence reset: safe against a racing insert, but a write
-- outage to schedule on a large ledger.
ALTER TABLE gobricks_outbox
    ADD COLUMN seq BIGINT GENERATED BY DEFAULT AS IDENTITY,
    ADD COLUMN lane VARCHAR(16) NOT NULL DEFAULT 'amqp',
    ADD COLUMN stream VARCHAR(255) NOT NULL DEFAULT '',
    ADD COLUMN partition_key VARCHAR(255) NOT NULL DEFAULT '';

WITH ordered AS (
    SELECT id, row_number() OVER (ORDER BY created_at, id) AS rn FROM gobricks_outbox
)
UPDATE gobricks_outbox o SET seq = ordered.rn FROM ordered WHERE o.id = ordered.id;

-- Three-argument setval: an EMPTY ledger has no max, and setval(seq, 0) violates MINVALUE 1.
SELECT setval(pg_get_serial_sequence('gobricks_outbox', 'seq'),
              (SELECT coalesce(max(seq), 1) FROM gobricks_outbox),
              (SELECT max(seq) IS NOT NULL FROM gobricks_outbox));

DROP INDEX IF EXISTS idx_gobricks_outbox_pending;
CREATE INDEX idx_gobricks_outbox_pending ON gobricks_outbox (seq) WHERE status = 'pending';

CREATE TABLE gobricks_outbox_leader (id SMALLINT PRIMARY KEY);
INSERT INTO gobricks_outbox_leader (id) VALUES (1) ON CONFLICT (id) DO NOTHING;
```

```sql
-- Oracle. stream/partition_key stay NULLABLE: '' IS NULL there, so NOT NULL DEFAULT '' would
-- reject every AMQP-lane insert with ORA-01400 (issue #586); FetchPending maps NULL back to "".
-- Each DDL autocommits, so nothing brackets these statements — quiesce the relay and stop
-- writers for the whole block, or rows inserted between the ALTER and the identity reset take
-- seq values the reset then hands out again.
ALTER TABLE gobricks_outbox ADD (
    seq NUMBER(19) GENERATED BY DEFAULT AS IDENTITY,
    lane VARCHAR2(16) DEFAULT 'amqp' NOT NULL,
    stream VARCHAR2(255),
    partition_key VARCHAR2(255));

MERGE INTO gobricks_outbox t USING (
    SELECT id, ROW_NUMBER() OVER (ORDER BY created_at, id) AS rn FROM gobricks_outbox
) s ON (t.id = s.id) WHEN MATCHED THEN UPDATE SET t.seq = s.rn;

-- Only when rows exist: START WITH LIMIT VALUE restarts at max(seq)+1 and has no max to read
-- on an empty ledger.
ALTER TABLE gobricks_outbox MODIFY (seq GENERATED BY DEFAULT AS IDENTITY (START WITH LIMIT VALUE));

DROP INDEX idx_gobricks_outbox_pending;
CREATE INDEX idx_gobricks_outbox_pending ON gobricks_outbox (CASE WHEN status = 'pending' THEN seq END);

CREATE TABLE gobricks_outbox_leader (id NUMBER(3) PRIMARY KEY);
MERGE INTO gobricks_outbox_leader t USING (SELECT 1 AS id FROM dual) s ON (t.id = s.id)
  WHEN NOT MATCHED THEN INSERT (id) VALUES (s.id);
```

Substitute your configured `outbox.tablename` throughout, and grant the relay's role
`SELECT … FOR UPDATE` on the leader table. The table's own segment is bounded at 49 bytes so
every identifier derived from it stays distinct under PostgreSQL's 63-byte truncation.

**The persisted tenant stamp is rehydrated onto the publish context**, never forwarded as a
stored header: `Publish` persists it in the row, the relay strips it from the headers and
`SetTenant`s the publish context with it, and the pooled publisher — the stamp's only writer
([ADR-087](adr_087_messaging_tenancy_and_tenant_stamp.md)) — re-stamps the frame from that
context. A consumer still reads `x-tenant-id` as before, and a hand-written stamp in
`OutboxEvent.Headers` is refused at `Publish` with `messaging.ErrTenantStampConflict`.

## Outbox Defaults

GoBricks applies production-safe outbox defaults when outbox is enabled:

| Setting | Default | Purpose |
| --------- | --------- | --------- |
| `outbox.tablename` | `gobricks_outbox` | Outbox table name |
| `outbox.autocreatetable` | `false` | Auto-create table on first use (opt-in) |
| `outbox.pollinterval` | `5s` | Relay poll frequency |
| `outbox.batchsize` | `100` | Events per relay cycle |
| `outbox.defaultexchange` | `""` | Fallback exchange when `Event.Exchange` is empty; **must be ≤ 255 bytes** (Init fails otherwise) |
| `outbox.maxretries` | `5` | Dead-letter ceiling for **poison** events only (undecodable headers, a destination past the AMQP shortstr limit, an unknown lane, a stream not listed below, or a stream row with no partition key) |
| `outbox.superstreams` | none | Super streams the relay may publish to over the native streams lane. Requires `messaging.streams.uri`; each listed name gets one publisher declared by the outbox |
| `outbox.publishtimeout` | `60s` | Per-record publish bound; **must be ≥ `messaging.reconnect.connectiontimeout`** (Init fails otherwise) |
| `outbox.retentionperiod` | `72h` | Published event retention |

**Override defaults** in `config.yaml`:

```yaml
outbox:
  enabled: true
  pollinterval: 2s           # Lower latency
  batchsize: 200             # Higher throughput
  retentionperiod: 168h      # 7-day retention of PUBLISHED events (outbox side)
```

`outbox.retentionperiod` bounds the published-event ledger only. The consumer side has its own:
`inbox.retentionperiod` (168h default, `config.InboxConfig.RetentionPeriod`) is the **replay
window** — it must exceed the broker's redelivery window AND cover every DLQ drain or outbox
re-drive you intend to replay, because a message replayed after its inbox row was swept is
processed again. Raising outbox retention does not extend it.

## Startup Verification

When `outbox.enabled: true` (or `inbox.enabled: true`), `Init` verifies the `""`-key ledger database
and table are actually usable before the app finishes booting — instead of booting green and failing
once per poll interval forever. The check probes that database with the operation the relay or
cleanup job already performs every cycle — the outbox's `FetchPending(…, 1)`, a read; the inbox's
`DeleteProcessed` before the Unix epoch, a write that matches no row (so it changes nothing, but it
is still a write and will fail against a read-only replica) — and fails `Init` with one of:

- `outbox.enabled=true requires a database, but none is configured` / same for `inbox` — the
  `""`-key database resolver reports `config.IsNotConfigured`.
- `database unreachable at startup` — the resolver returned a different error (network, auth, DNS).
- `table %q is not usable (missing table or insufficient privileges)` — the database is reachable but
  the table is missing, or the runtime role lacks the privilege the probe needs (outbox: `SELECT`;
  inbox: `DELETE`). Missing table → run migrations, or set
  `outbox.autocreatetable`/`inbox.autocreatetable` where the role also holds DDL rights. Privilege
  failure → grant that privilege; auto-creation does not help.
- `database resolver returned a nil database` — a resolver contract violation (`(nil, nil)`).

**Exempt modes** (the `""` key is not statically resolvable at `Init` time, so the check is skipped —
these deployments keep today's runtime-resolution behavior):

- **Per-tenant fan-out** (`multitenant.enabled: true`, default `outbox.tenancy`/`inbox.tenancy`) —
  each tenant's database is resolved per-poll via the resource source, not at `Init`.
- **Dynamic source** (`source.type: dynamic`) — the `""` key itself resolves at runtime.

`tenancy: shared` with a **static** source IS checked: the shared ledger's control-plane database is
statically known at `Init`, so a slow or unreachable control-plane database now delays startup by up
to `app.startup.database` (10s default) instead of failing silently on the first relay/cleanup cycle.

Three further consequences of running the check inside `Init`:

- The inbox probe requires **`DELETE`** on the inbox table even where nothing else deletes from it:
  `ProcessOnce` only ever issues an `INSERT`, and the retention cleanup job — the sole runtime
  `DELETE` — is registered only when a scheduler module is present. A least-privilege runtime role
  for a `ProcessOnce`-only deployment (no scheduler registered) must be granted `DELETE` before
  upgrading, or `Init` fails with `table … is not usable`. The converse also holds: because the
  inbox probe is the cleanup job's `DELETE`, it does **not** prove `ProcessOnce`'s `INSERT`, so a
  role granted `DELETE` but not `INSERT` still passes `Init` and fails at the first processed
  event. The outbox probe has no such gap — `FetchPending` is exactly what the relay reads.
- A **custom dynamic `Options.ResourceSource` behind a static `source.type`** is NOT exempt, although
  the app builder's own pre-init and `/ready` database probe both skip it. The module sees only
  `*config.Config`, so it cannot detect that resource source; such a deployment is probed at startup
  where it previously wasn't. Set `source.type: dynamic` to opt out until the exemption is threaded
  down to modules — that opt-out is open to single-tenant and `tenancy: shared` deployments only. A
  per-tenant fan-out deployment is already exempt by the rule above and must **not** set it: the
  outbox relay (and the inbox cleanup job) reject dynamic multi-tenant sources outright.
- With `outbox.autocreatetable`/`inbox.autocreatetable` enabled, the `""` key's table DDL now runs at
  `Init` rather than on the first publish or poll — the probe initializes that store, which is what
  creates the table. The exempt modes above run no probe, so their DDL still waits for first use.

## Multi-Tenant

In multi-tenant mode the outbox/inbox support two tenancy modes, set via `outbox.tenancy` /
`inbox.tenancy` (`"per-tenant"` default, or `"shared"`):

### Per-tenant fan-out (default)

The relay and cleanup jobs **fan out across the configured static tenants** (`multitenant.tenants`): each poll cycle resolves every tenant's database independently (via `multitenant.SetTenant` + `deps.DB`), relays that tenant's pending events, and prunes its published rows. A failure for one tenant is logged and does not block the others.

**Dynamic tenant sources are not supported** for per-tenant fan-out: because the tenant set is not enumerable at job-registration time, the framework fails fast rather than silently never relaying. With `multitenant.enabled` and `source.type: dynamic`, enabling the outbox is rejected at module `Init` (and the inbox cleanup job at `RegisterJobs`) — unless `tenancy: shared` is set (see below).

### Shared (control-plane) ledger

For a **pool-model** deployment — one shared database, tenant identity carried as a data column
rather than a separate schema/instance — `multitenant.enabled: true` is often needed only for HTTP
tenant resolution, not for the outbox/inbox. `tenancy: shared` runs the relay/cleanup as a **single
pass** against the control-plane database and broker, resolved via the empty key (`""`) — the same
key the built-in resource store already maps to the root `database:`/`messaging:` blocks, and which
HTTP tenant resolution can never produce. This is what unblocks `source.type: dynamic` for the
outbox/inbox: shared mode does not need an enumerable tenant set at all.

```yaml
multitenant:
  enabled: true
source:
  type: dynamic          # or static — shared mode works with either
database:                 # root block: the control-plane database
  host: control-plane-db
  # ...
messaging:                 # root block: the control-plane broker
  broker:
    url: amqp://control-plane-broker/
outbox:
  enabled: true
  tenancy: shared
inbox:
  enabled: true
  tenancy: shared
```

**Enabling shared tenancy, step by step:**

1. Keep (or add) the root `database:`/`messaging:` blocks — shared mode resolves them via key `""`,
   exactly like single-tenant mode does. A custom `app.Options.ResourceSource` must resolve
   `DBConfig`/`BrokerURL` for `""` to these control-plane resources if you're not using the
   built-in store.
2. Set `outbox.tenancy: shared` and/or `inbox.tenancy: shared`.
3. **Shared-mode outbox publishes must originate from `RunInSharedTx`.** Because `dbtypes.Tx` is
   opaque (no vendor/connection identity), the framework cannot verify a caller's transaction
   targets the control-plane database any other way — a foreign transaction's events would be
   silently lost, since the relay only ever polls the control-plane ledger. `Publish` rejects any
   transaction that didn't originate from `RunInSharedTx`:

   ```go
   r, ok := deps.Outbox.(app.SharedTxRunner)
   if !ok {
       // Fail loudly: a custom OutboxPublisher (e.g. a test mock) doesn't support
       // shared tenancy — silently skipping the write would lose the event.
       return fmt.Errorf("outbox: deps.Outbox does not implement app.SharedTxRunner")
   }
   err := r.RunInSharedTx(ctx, func(ctx context.Context, tx dbtypes.Tx) error {
       if _, err := tx.Exec(ctx, "INSERT INTO orders ..."); err != nil {
           return err
       }
       _, err := deps.Outbox.Publish(ctx, tx, &app.OutboxEvent{
           EventType: "order.created", AggregateID: "order-123",
           Payload: payload, Exchange: "order.events",
       })
       return err
   })
   ```

4. The inbox needs no code change — `deps.Inbox.ProcessOnce` already originates its own
   transaction and simply runs against the shared database once `inbox.tenancy: shared` swaps the
   resolver in.

**Caveats:**

- **Pool-model only.** Shared tenancy is for "one database, tenant as data," not for silo-model
  dynamic deployments that still want automatic per-tenant fan-out (that use case is deferred —
  see ADR-041).
- **Consumers on the shared broker are `messaging.tenancy: shared`.** This ledger setting stays
  publisher-side; the consumer half is the messaging kind's own tenancy (ADR-087). Set
  `messaging.tenancy: shared` and declared consumers replay once at boot on the control-plane key,
  with the tenant carried as the `x-tenant-id` stamp — see
  [messaging.md](messaging.md#multi-tenant-consumption). Under the default `per-tenant` tenancy
  `DeclareMessaging` consumers still start per tenant.
- **Tenant identity travels in the event as the tenant stamp, not in the ledger schema.** The
  outbox/inbox table schemas are unchanged; the framework writes the tenant into the
  `x-tenant-id` header from the publishing context (ADR-087), so a downstream consumer under shared
  messaging tenancy reads it back automatically. The write point is `Publish` itself, which snapshots
  the stamp beside the trace keys because the relay cycle carries no tenant under shared tenancy. A
  caller must not set that header itself — it is a publish error. For anything else the event needs, carry it in the payload (the inbox's `Record` already persists `TenantID` from ctx,
  regardless of tenancy mode).
- **First relay cycle after cold start may log one broker-outage cycle.** The connection pre-warmer
  is single-tenant-only, so a shared-tenancy deployment (which requires `multitenant.enabled: true`)
  isn't pre-warmed; this is a one-time, self-resolving startup artifact.
- **Shared + `multitenant.enabled: false` is a no-op by design** — both resolve via the same `""`
  key, so the same YAML works unchanged across single-tenant dev and multi-tenant prod.

See [ADR-041](adr_041_shared_ledger_tenancy.md) for the full design rationale, alternatives
considered, and the `""`-key resource-source contract.

### Hold ledger

`inbox.hold` is the durable home for the streams lane's **per-tenant hold**
([ADR-089](adr_089_per_tenant_hold_on_the_streams_lane.md)): a stream consumer
declaring `Hold: true` parks a tenant's failed message here instead of skipping
it, and the `inbox-hold-drain` job replays the tenant's rows in order. It lives
in the control plane on purpose — the tenant whose database is down is exactly
the tenant that needs to be held — so it **requires `inbox.tenancy: shared`**.

```yaml
inbox:
  enabled: true
  tenancy: shared          # required by the hold
  hold:
    enabled: true          # opt-in; nothing below is defaulted while it is false
    tablename: gobricks_inbox_hold   # tenant table is "<tablename>_tenant"
    draininterval: 5s      # how often the drain looks for due tenants
    maxbackoff: 5m         # cap on the per-tenant retry backoff
    maxage: 1h             # a tenant held longer than this earns one WARN per pass
    leaseduration: 60s     # one drainer's hold on a tenant, and the replay's time bound
```

Two tables. The framework creates them only when `inbox.autocreatetable` is on;
the DDL is here so a migration can own them instead.

**PostgreSQL:**

```sql
CREATE TABLE IF NOT EXISTS gobricks_inbox_hold (
    consumer      VARCHAR(255) NOT NULL,
    stream        VARCHAR(255) NOT NULL,
    stream_offset BIGINT       NOT NULL,
    tenant_id     VARCHAR(255) NOT NULL,
    data          BYTEA,
    properties    BYTEA,
    held_at       TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT NOW(),
    PRIMARY KEY (consumer, stream, stream_offset)
);

CREATE TABLE IF NOT EXISTS gobricks_inbox_hold_tenant (
    consumer        VARCHAR(255) NOT NULL,
    tenant_id       VARCHAR(255) NOT NULL,
    held_since      TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT NOW(),
    attempts        INTEGER      NOT NULL DEFAULT 0,
    next_attempt_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT NOW(),
    last_error      TEXT,
    lease_owner     VARCHAR(255),
    lease_until     TIMESTAMP WITH TIME ZONE,
    PRIMARY KEY (consumer, tenant_id)
);

CREATE INDEX IF NOT EXISTS idx_gobricks_inbox_hold_tenant_order
    ON gobricks_inbox_hold (consumer, tenant_id, stream, stream_offset);
CREATE INDEX IF NOT EXISTS idx_gobricks_inbox_hold_tenant_due
    ON gobricks_inbox_hold_tenant (consumer, next_attempt_at);
```

**Oracle:**

```sql
CREATE TABLE gobricks_inbox_hold (
    consumer      VARCHAR2(255) NOT NULL,
    stream        VARCHAR2(255) NOT NULL,
    stream_offset NUMBER(19)    NOT NULL,
    tenant_id     VARCHAR2(255) NOT NULL,
    data          BLOB,
    properties    BLOB,
    held_at       TIMESTAMP WITH TIME ZONE DEFAULT SYSTIMESTAMP NOT NULL,
    CONSTRAINT pk_gobricks_inbox_hold PRIMARY KEY (consumer, stream, stream_offset)
);

CREATE TABLE gobricks_inbox_hold_tenant (
    consumer        VARCHAR2(255) NOT NULL,
    tenant_id       VARCHAR2(255) NOT NULL,
    held_since      TIMESTAMP WITH TIME ZONE DEFAULT SYSTIMESTAMP NOT NULL,
    attempts        NUMBER(10)    DEFAULT 0 NOT NULL,
    next_attempt_at TIMESTAMP WITH TIME ZONE DEFAULT SYSTIMESTAMP NOT NULL,
    last_error      CLOB,
    lease_owner     VARCHAR2(255),
    lease_until     TIMESTAMP WITH TIME ZONE,
    CONSTRAINT pk_gobricks_inbox_hold_tenant PRIMARY KEY (consumer, tenant_id)
);

CREATE INDEX idx_gobricks_inbox_hold_tenant_order
    ON gobricks_inbox_hold (consumer, tenant_id, stream, stream_offset);
CREATE INDEX idx_gobricks_inbox_hold_tenant_due
    ON gobricks_inbox_hold_tenant (consumer, next_attempt_at);
```

**Oracle prerequisite.** `inbox.hold.tablename` is bounded at 46 bytes because the
longest name derived from it, `idx_<tablename>_tenant_order`, adds 17 bytes and
must fit PostgreSQL's 63-byte identifier limit. Oracle allows 128 bytes only at
`COMPATIBLE = 12.2` or higher; below that its limit is 30, where even the default
`gobricks_inbox_hold` cannot produce that index (17 + 19 = 36). On an instance
under 12.2, either raise `COMPATIBLE`, or choose a `tablename` of at most 13
bytes so the derived name stays within 30.

Substitute your own `inbox.hold.tablename` throughout if you changed it; the
tenant table is always that name with `_tenant` appended, and the index names are
derived from it, which is why the configured name is bounded at 46 bytes.

**Lease semantics.** `lease_owner` / `lease_until` on the tenant row mean "this
drainer owns this tenant until this instant". Every write the drain makes carries
the lease predicate in the statement itself, so a drainer whose lease expired
mid-pass writes nothing. A crashed drainer costs one `leaseduration` of waiting,
after which the next pass picks the tenant up. `leaseduration` is therefore also
the time bound on a replayed handler: the drain gives each replay a deadline at
the lease's end, and stops the batch there with the remaining rows still held. A
pass that stops with work left — because the batch was full or the lease ran out
— hands the lease back rather than idling on it, so the next pass can start at
once on any replica.

**Operator purge.** A held message is never dropped automatically — there is no
retention on these tables. To abandon one tenant's backlog, delete its rows
first, then its marker (the marker cannot be released while rows remain):

PostgreSQL:

```sql
DELETE FROM gobricks_inbox_hold        WHERE consumer = $1 AND tenant_id = $2;
DELETE FROM gobricks_inbox_hold_tenant WHERE consumer = $1 AND tenant_id = $2;
```

Oracle:

```sql
DELETE FROM gobricks_inbox_hold        WHERE consumer = :consumer AND tenant_id = :tenant;
DELETE FROM gobricks_inbox_hold_tenant WHERE consumer = :consumer AND tenant_id = :tenant;
```

**Quiesce the drain first.** The statements above are fenced, so a purge cannot
corrupt a drainer's bookkeeping — but a fence does not reach into a handler that
is already running, and deleting a row mid-replay leaves that replay's side
effects with nothing recording that they happened. A live consumer is the second
reason: a park landing between the two statements writes its row after the rows
were deleted and before the marker is, stranding a row under a marker that is
about to go — held by nobody, replayed by nobody.

Stop the consumer FIRST, which is what forecloses that park; then stop (or wait
out) its drain — one `inbox.hold.leaseduration` is enough for an in-flight pass to
finish or lose its lease — and only then purge.

Run both against the names your `inbox.hold.tablename` configures. Those messages
are gone: nothing else holds a copy once the offset was committed.

## Oracle: Default (Empty) Exchange

The AMQP **default exchange** is the empty string, and a common pattern is "publish straight to a pre-declared queue" with `Exchange: ""` and `RoutingKey: "<queue-name>"`. Because Oracle treats `''` as `NULL`, the `gobricks_outbox.exchange`/`routing_key` columns are **nullable** on Oracle (PostgreSQL stores `''` as a real value). The relay's `FetchPending` maps the stored `NULL` back to `""`, so the default exchange works transparently on both vendors.

**Upgrading an existing Oracle deployment:** older framework versions created the table with `exchange ... DEFAULT '' NOT NULL` (a self-contradictory constraint that rejected default-exchange events with `ORA-01400`). The framework only auto-creates *fresh* tables, so a table created by an older version must be migrated once:

```sql
ALTER TABLE gobricks_outbox MODIFY (exchange DEFAULT NULL NULL, routing_key DEFAULT NULL NULL);
```

(Substitute your configured `outbox.tablename`.) Dropping `NOT NULL` is the part that matters; `DEFAULT NULL` also clears the now-meaningless `DEFAULT ''` so an auditor doesn't see a lingering empty-string default. Fresh deployments need no action.
