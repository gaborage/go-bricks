# Outbox Architecture (Deep Dive)

This document covers GoBricks' built-in Transactional Outbox: the components that solve the dual-write problem, the at-least-once delivery guarantee (consumers MUST be idempotent), wiring patterns, and the production-safe defaults.

## Outbox Architecture

GoBricks provides a built-in **Transactional Outbox** for reliable event publishing. It solves the dual-write problem: events are written to an outbox table in the **same database transaction** as business data, then reliably delivered to the message broker by a background relay.

**Core Components:**
- **Publisher**: Writes events to the outbox table within a database transaction
- **Relay**: Background poller (scheduler job) that publishes pending events to AMQP
- **Cleanup**: Scheduled job that removes published events after retention period
- **Store**: Vendor-agnostic SQL abstraction (PostgreSQL + Oracle)

**Delivery Guarantee:** At-least-once. Consumers MUST be idempotent. Use the `x-outbox-event-id` header for deduplication.

**Module Setup:**
```go
fw.RegisterModules(
    scheduler.NewModule(),  // Required: relay runs as a scheduled job
    outbox.NewModule(),     // Outbox module
    &myapp.OrderModule{},
)

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
1. `Publish()` writes an `OutboxRecord` to the outbox table within the caller's transaction
2. The **relay job** (`outbox-relay` via scheduler) polls for pending events every `pollinterval`
3. Each pending event is published to the target AMQP exchange with `x-outbox-event-id` header
4. Successfully published events are marked as `published`
5. Failed events have their `retry_count` advanced and stay `pending` for the next cycle — on **every** failed attempt, including while the broker is unavailable (see [Retry & Dead-Lettering](#retry--dead-lettering) below)
6. The **cleanup job** (`outbox-cleanup`) removes published events older than `retentionperiod`

**Configuration:**

```yaml
outbox:
  enabled: true
  tablename: gobricks_outbox       # Default table name
  autocreatetable: false          # Auto-create table on first use (default: false; enable for development only)
  defaultexchange: ""              # Fallback if Event.Exchange empty
  pollinterval: 5s                 # Relay poll frequency
  batchsize: 100                   # Events per relay cycle
  maxretries: 5                    # Dead-letter ceiling for POISON (undecodable headers) only — see below
  publishtimeout: 60s              # Per-record publish bound (MUST be >= messaging connectiontimeout)
  retentionperiod: 72h             # Keep published events (0=disable cleanup)
```

**Event Struct:**

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `EventType` | string | Yes | Event routing key (e.g., "order.created") |
| `AggregateID` | string | Yes | Entity identifier for idempotency (e.g., "order-123") |
| `Payload` | any | No | Event data. `[]byte` stored as-is, otherwise JSON-marshaled. Nil is accepted and stored as JSON `null`. |
| `Headers` | map[string]any | No | Custom AMQP headers propagated to published message |
| `Exchange` | string | No | Target AMQP exchange (falls back to `defaultexchange` config) |
| `RoutingKey` | string | No | AMQP routing key (falls back to `EventType`) |

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
|-------|--------|----------|
| **Connectivity** | broker down / not ready, **broker NACK**, confirmation timeout, per-record `publishtimeout` elapsed, missing exchange (surfaces as a synthesized NACK) | `retry_count` advances; **never** dead-lettered. The event stays `pending` and delivers once the broker recovers or the config is fixed. |
| **Poison** | corrupt / undecodable headers **only** (a deterministic, broker-independent failure) | `retry_count` advances; once it reaches `maxretries` the event is **dead-lettered** to `status = 'failed'` and stops being retried. |

Consequences worth knowing:

- **A broker NACK is treated as connectivity, not poison.** A RabbitMQ `basic.nack` on a publish
  confirm is a *transient broker condition* (disk alarm, mirror resync, node failover), not a
  statement that the message is bad — so the event is retried (at-least-once), never auto-parked.
  A permanently mis-named exchange likewise surfaces as a NACK and keeps retrying, so it delivers
  the moment an operator creates the exchange. The only auto-parked failure is genuine, deterministic
  corruption (undecodable headers, which the framework essentially never produces).
- **`maxretries` bounds poison only.** Connectivity failures (including a permanently-failing publish)
  retry indefinitely with a climbing `retry_count` — monitor that growth to catch a stuck event.
- **One stuck record cannot starve the batch:** each publish is bounded by `outbox.publishtimeout`
  (default 60s). It **must be ≥ `messaging.reconnect.connectiontimeout`** (default 30s) — the module
  **fails to start** otherwise, because a shorter value truncates every legitimate confirmation into a
  connectivity failure and re-publishes the (already-delivered) event every cycle.
- **Underneath, the AMQP publish itself is bounded** by `messaging.reconnect.maxpublishattempts`
  (default 5), after which it returns `messaging.ErrPublishRetriesExhausted` wrapping the cause.
- **A relay cycle that has pending work but cannot reach the broker returns a job error** (after
  advancing every record's `retry_count`), so the failure stays visible at the scheduler level and,
  in multi-tenant mode, names the affected tenant. An idle relay (nothing pending) is not an error.
- **`failed` rows accumulate:** `outbox-cleanup` purges only `published` events. Monitor and prune
  dead-lettered rows; they are intentionally never auto-deleted so they stay visible.

## Outbox Defaults

GoBricks applies production-safe outbox defaults when outbox is enabled:

| Setting | Default | Purpose |
|---------|---------|---------|
| `outbox.tablename` | `gobricks_outbox` | Outbox table name |
| `outbox.autocreatetable` | `false` | Auto-create table on first use (opt-in) |
| `outbox.pollinterval` | `5s` | Relay poll frequency |
| `outbox.batchsize` | `100` | Events per relay cycle |
| `outbox.maxretries` | `5` | Dead-letter ceiling for **poison** events only (undecodable headers) |
| `outbox.publishtimeout` | `60s` | Per-record publish bound; **must be ≥ `messaging.reconnect.connectiontimeout`** (Init fails otherwise) |
| `outbox.retentionperiod` | `72h` | Published event retention |

**Override defaults** in `config.yaml`:

```yaml
outbox:
  enabled: true
  pollinterval: 2s           # Lower latency
  batchsize: 200             # Higher throughput
  retentionperiod: 168h      # 7-day retention
```

## Multi-Tenant

In multi-tenant mode the relay and cleanup jobs **fan out across the configured static tenants** (`multitenant.tenants`): each poll cycle resolves every tenant's database independently (via `multitenant.SetTenant` + `deps.DB`), relays that tenant's pending events, and prunes its published rows. A failure for one tenant is logged and does not block the others.

**Dynamic tenant sources are not supported** for the relay/cleanup: because the tenant set is not enumerable at job-registration time, the framework fails fast rather than silently never relaying. With `multitenant.enabled` and `source.type: dynamic`, enabling the outbox is rejected at module `Init` (and the inbox cleanup job at `RegisterJobs`). Use static `multitenant.tenants` config for outbox/inbox relay and cleanup.

## Oracle: Default (Empty) Exchange

The AMQP **default exchange** is the empty string, and a common pattern is "publish straight to a pre-declared queue" with `Exchange: ""` and `RoutingKey: "<queue-name>"`. Because Oracle treats `''` as `NULL`, the `gobricks_outbox.exchange`/`routing_key` columns are **nullable** on Oracle (PostgreSQL stores `''` as a real value). The relay's `FetchPending` maps the stored `NULL` back to `""`, so the default exchange works transparently on both vendors.

**Upgrading an existing Oracle deployment:** older framework versions created the table with `exchange ... DEFAULT '' NOT NULL` (a self-contradictory constraint that rejected default-exchange events with `ORA-01400`). The framework only auto-creates *fresh* tables, so a table created by an older version must be migrated once:

```sql
ALTER TABLE gobricks_outbox MODIFY (exchange DEFAULT NULL NULL, routing_key DEFAULT NULL NULL);
```

(Substitute your configured `outbox.tablename`.) Dropping `NOT NULL` is the part that matters; `DEFAULT NULL` also clears the now-meaningless `DEFAULT ''` so an auditor doesn't see a lingering empty-string default. Fresh deployments need no action.
