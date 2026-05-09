# Messaging Architecture (Deep Dive)

This document covers GoBricks' AMQP messaging subsystem in depth: declaration helpers, consumer registration rules, error handling and panic recovery, consumer concurrency (v0.17+), and the production-safe reconnection defaults.

## Messaging Architecture

AMQP-based messaging with **validate-once, replay-many** pattern:
- Declarations validated upfront, replayed per-tenant for isolation
- Automatic reconnection with exponential backoff
- Context propagation for tenant IDs and tracing

## Helper Functions for Simplified Declarations

GoBricks provides production-safe defaults to reduce AMQP boilerplate (~50+ lines → ~15 lines):

**Concise Declaration Pattern:**
```go
exchange := decls.DeclareTopicExchange("issuance.events")
queue := decls.DeclareQueue("issuance.events.queue")
decls.DeclareBinding(queue.Name, exchange.Name, "issuance.*")

decls.DeclarePublisher(&messaging.PublisherOptions{
    Exchange: exchange.Name, RoutingKey: "issuance.created",
    EventType: "CreateBatchIssuanceRequest",
}, nil)

decls.DeclareConsumer(&messaging.ConsumerOptions{
    Queue: queue.Name, EventType: "CreateBatchIssuanceRequest",
    Handler: amqp.NewHandler(m.logger),
}, nil)
```

**Production-Safe Defaults:**
- Exchanges: `Durable: true`, `AutoDelete: false`, `Type: "topic"`
- Queues: `Durable: true`, `AutoDelete: false`, `Exclusive: false`
- Publishers: `Mandatory: false`, `Immediate: false`
- Consumers: `AutoAck: false`, `Exclusive: false`, `NoLocal: false`

**Key Helpers:** `DeclareTopicExchange()`, `DeclareQueue()`, `DeclareBinding()`, `DeclarePublisher()`, `DeclareConsumer()`

**For verbose before/after comparison**, see [messaging/declarations.go](../messaging/declarations.go)

## Consumer Registration Best Practices

**CRITICAL: Deduplication Rules**

GoBricks enforces **strict deduplication** to prevent message duplication bugs. Each unique `queue + consumer_tag + event_type` combination must be registered exactly once:

```go
func (m *Module) DeclareMessaging(decls *messaging.Declarations) {
    decls.DeclareConsumer(&messaging.ConsumerOptions{
        Queue:     "events.queue",
        Consumer:  "discover-pending",
        EventType: "discover-pending-events",
        Handler:   m.discoverHandler.Handle,
    }, nil)

    decls.DeclareConsumer(&messaging.ConsumerOptions{
        Queue:     "events.queue",
        Consumer:  "process-batch",  // Different consumer tag - OK
        EventType: "process-batch-events",
        Handler:   m.processHandler.Handle,
    }, nil)
}
```

**Common Mistakes:**
- Registering consumers in loops or conditional blocks (creates duplicates)
- Calling `app.RegisterModule()` multiple times for the same module
- Module registration errors are unrecoverable - MUST use `log.Fatal(err)` to handle

See the Troubleshooting section in [CLAUDE.md](../CLAUDE.md) for diagnosing duplicate consumer/module errors.

## Message Error Handling

**IMPORTANT:** GoBricks uses a **no-retry policy** for failed messages to prevent infinite retry loops.

**Behavior:** All handler errors → Message nacked WITHOUT requeue (message dropped). Prevents poison messages from blocking queues. Rich ERROR logs + OpenTelemetry metrics track all failures.

**Panic Recovery:** Handler panics are automatically recovered and treated identically to errors:
- Panic recovered with stack trace logging
- Message nacked WITHOUT requeue (consistent with error policy)
- Service continues processing other messages
- Metrics recorded with panic error type
- Other consumers remain unaffected (panic isolation)

**Error Handling Pattern:**
```go
func (h *Handler) Handle(ctx context.Context, delivery *amqp.Delivery) error {
    var order Order

    // Validation errors → message dropped (no retry)
    if err := json.Unmarshal(delivery.Body, &order); err != nil {
        return fmt.Errorf("invalid message format: %w", err)
    }

    // Business logic errors → message dropped (no retry)
    if err := h.orderService.Process(ctx, order); err != nil {
        return fmt.Errorf("processing failed: %w", err)
    }

    return nil // Success → message ACKed
}
```

**Observability:** ERROR logs include `message_id`, `queue`, `event_type`, `correlation_id`, `error`. OpenTelemetry metrics track operation duration with `error.type` attribute.

**Best Practices:** Thorough handler testing, monitor ERROR logs with alerts, use trace IDs for manual replay. Dead-letter queue support planned for future releases.

**Breaking Change (v2.X):** Previous behavior auto-requeued errors (infinite retry risk). New behavior drops failed messages with rich logging. Review handler error handling and set up monitoring.

## Consumer Concurrency (v0.17+)

**Breaking Change (v0.17.0):** Default worker count changed from 1 to `runtime.NumCPU() * 4` for optimal I/O-bound performance (20-30x throughput improvement).

**Smart Auto-Scaling:**
GoBricks automatically configures `Workers = runtime.NumCPU() * 4` to handle blocking I/O operations (database queries, HTTP calls, file operations). The 4x multiplier ensures CPU utilization while threads wait on I/O.

**Configuration:**
```go
// Auto-scaling (default): Workers = NumCPU * 4, PrefetchCount = Workers * 10
decls.DeclareConsumer(&messaging.ConsumerOptions{
    Queue:     "orders",
    Consumer:  "processor",
    EventType: "order.created",
    Handler:   handler,
}, queue)
// 8-core machine: 32 workers, 320 prefetch

// Explicit sequential (for message ordering)
decls.DeclareConsumer(&messaging.ConsumerOptions{
    Queue:     "ordered.events",
    Consumer:  "sequencer",
    EventType: "event.sequence",
    Workers:   1,  // Sequential processing
    Handler:   handler,
}, queue)

// Custom high concurrency
decls.DeclareConsumer(&messaging.ConsumerOptions{
    Queue:         "batch.processing",
    Consumer:      "batch-worker",
    EventType:     "batch.import",
    Workers:       100,          // Explicit
    PrefetchCount: 500,          // Explicit
    Handler:       handler,
}, queue)
```

**Thread-Safety Requirements:**
- Handlers MUST be thread-safe (no shared mutable state without locks/atomic operations)
- Database pools MUST be sized: `MaxOpenConns >= NumCPU * 4 * NumConsumers`
- External APIs: Add semaphore for rate limit enforcement if needed
- Test with `go test -race` to detect data races

**Resource Safeguards:**
- Workers capped at 200 per consumer (prevents goroutine explosion)
- PrefetchCount capped at 1000 (prevents memory exhaustion)
- Warnings logged when caps are applied

**Performance Impact (8-core machine, 100ms handler):**
| Version | Workers | Throughput | Speedup |
|---------|---------|------------|---------|
| v0.16.x | 1 | 10 msg/sec | Baseline |
| v0.17.0 | 32 | 320 msg/sec | **32x** |

**When to Override Defaults:**
- **Workers=1**: Message ordering required (events must be processed sequentially)
- **Workers>NumCPU*4**: Very slow handlers (>1s per message) or high throughput needs
- **Workers<NumCPU*4**: CPU-bound handlers (rare - most handlers are I/O-bound)

**Observability:**
- Startup logs include `workers` and `prefetch` counts
- Each worker logs with `worker_id` for debugging
- OpenTelemetry metrics track per-consumer throughput

## Messaging Reconnection Defaults

GoBricks applies production-safe AMQP reconnection defaults when messaging is configured:

| Setting | Default | Purpose |
|---------|---------|---------|
| `reconnect.delay` | 5s | Initial delay before reconnect attempts |
| `reconnect.reinit_delay` | 2s | Delay between channel re-initialization |
| `reconnect.resend_delay` | 5s | Delay before resending failed messages |
| `reconnect.connection_timeout` | 30s | Timeout for connection establishment |
| `reconnect.max_delay` | 60s | Maximum backoff cap for exponential retry |
| `publisher.max_cached` | 50 | Maximum cached publisher channels |
| `publisher.idle_ttl` | 10m | TTL for idle publisher channels |

**Override defaults** in `config.yaml`:
```yaml
messaging:
  reconnect:
    delay: 10s            # Slower initial reconnect
    max_delay: 120s       # Higher backoff cap
  publisher:
    max_cached: 100       # More cached publishers for high-throughput
    idle_ttl: 30m         # Keep publishers longer
```
