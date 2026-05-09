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
2. The **relay job** (`outbox-relay` via scheduler) polls for pending events every `poll_interval`
3. Each pending event is published to the target AMQP exchange with `x-outbox-event-id` header
4. Successfully published events are marked as `published`
5. Failed events are retried up to `max_retries` times
6. The **cleanup job** (`outbox-cleanup`) removes published events older than `retention_period`

**Configuration:**
```yaml
outbox:
  enabled: true
  table_name: gobricks_outbox       # Default table name
  auto_create_table: true           # Create table on first use
  default_exchange: ""              # Fallback if Event.Exchange empty
  poll_interval: 5s                 # Relay poll frequency
  batch_size: 100                   # Events per relay cycle
  max_retries: 5                    # Max attempts before giving up
  retention_period: 72h             # Keep published events (0=disable cleanup)
```

**Event Struct:**

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `EventType` | string | Yes | Event routing key (e.g., "order.created") |
| `AggregateID` | string | Yes | Entity identifier for idempotency (e.g., "order-123") |
| `Payload` | any | Yes | `[]byte` stored as-is, otherwise JSON-marshaled |
| `Headers` | map[string]any | No | Custom AMQP headers propagated to published message |
| `Exchange` | string | No | Target AMQP exchange (falls back to `default_exchange` config) |
| `RoutingKey` | string | No | AMQP routing key (falls back to `EventType`) |

## Outbox Defaults

GoBricks applies production-safe outbox defaults when outbox is enabled:

| Setting | Default | Purpose |
|---------|---------|---------|
| `outbox.table_name` | `gobricks_outbox` | Outbox table name |
| `outbox.auto_create_table` | `true` | Auto-create table on first use |
| `outbox.poll_interval` | `5s` | Relay poll frequency |
| `outbox.batch_size` | `100` | Events per relay cycle |
| `outbox.max_retries` | `5` | Max publish attempts |
| `outbox.retention_period` | `72h` | Published event retention |

**Override defaults** in `config.yaml`:
```yaml
outbox:
  enabled: true
  poll_interval: 2s           # Lower latency
  batch_size: 200             # Higher throughput
  retention_period: 168h      # 7-day retention
```
