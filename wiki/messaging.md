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
    Handler: m.handler, // a value implementing messaging.MessageHandler (Handle + EventType)
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
        Handler:   m.discoverHandler,
    }, nil)

    decls.DeclareConsumer(&messaging.ConsumerOptions{
        Queue:     "events.queue",
        Consumer:  "process-batch",  // Different consumer tag - OK
        EventType: "process-batch-events",
        Handler:   m.processHandler,
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
| `reconnect.reinitdelay` | 2s | Delay between channel re-initialization |
| `reconnect.resenddelay` | 5s | Delay before resending failed messages |
| `reconnect.connectiontimeout` | 30s | Per-publish broker confirmation (ACK/NACK) timeout |
| `reconnect.readytimeout` | 5s | Bounded pre-flight wait for a not-yet-ready client before a publish begins (see below) |
| `reconnect.maxpublishattempts` | 5 | Max publish attempts before returning `ErrPublishRetriesExhausted` (see below) |
| `reconnect.maxdelay` | 60s | Maximum backoff cap for exponential retry |
| `publisher.maxcached` | 50 | Maximum cached publisher channels |
| `publisher.idlettl` | 1h | TTL for idle publisher channels |
| `publisher.cleanupinterval` | 2m | How often the idle-publisher cleanup goroutine runs |

**Override defaults** in `config.yaml`:

```yaml
messaging:
  reconnect:
    delay: 10s            # Slower initial reconnect
    maxdelay: 120s       # Higher backoff cap
  publisher:
    maxcached: 100       # More cached publishers for high-throughput
    idlettl: 2h          # Keep publishers longer than the 1h default
```

### Bounded publish retries (`reconnect.maxpublishattempts`)

`PublishToExchange` (and the `Publish` convenience) retries a failing publish — publish error,
broker NACK, or confirmation timeout — but the loop is **bounded** by
`reconnect.maxpublishattempts` (default 5). On exhaustion it returns
`messaging.ErrPublishRetriesExhausted` **wrapping the last cause**, so callers can classify the
failure:

| Cause sentinel | Meaning |
|----------------|---------|
| `messaging.ErrPublishNacked` | the broker received the message and returned `basic.nack` (a transient broker condition — disk alarm, mirror resync, failover; also how a missing exchange surfaces) |
| `messaging.ErrPublishConfirmTimeout` | no ACK/NACK arrived within `connectiontimeout` |

> `messaging.ErrNotConnected` is **not** one of the wrapped causes above — it is returned directly
> (unwrapped), most commonly by the readiness pre-flight described below, timing out after waiting
> up to `reconnect.readytimeout`. That case means "still not ready after the bounded wait," not
> "not connected right now." The retry loop's own per-attempt readiness check — which pre-dates the
> pre-flight and still runs on every iteration — can also raise it immediately, with no wait, if the
> client drops connectivity again between two retry attempts (e.g. a broker blip during a NACK
> backoff).

Cancel / shutdown / deadline returns are also wrapped with the last cause, so a deadline that
fires after a NACK still reports `ErrPublishNacked` — **match with `errors.Is`, not `==`**
(use `errors.Is` for the raw `ErrNotConnected` sentinel too: it works on unwrapped errors, and
it keeps working if a future version ever wraps it).
Between NACK retries the client waits a small cancelable `nackBackoff` (100ms) rather than
busy-spinning. These causes are informational for logging/observability; the outbox relay treats
**every** publish failure as a recoverable *connectivity* failure that retries and never parks —
NACK included, and likewise a raw `ErrNotConnected`, though the relay rarely sees one: it checks
`IsReady()` itself at the start of each cycle and routes a cold broker to its outage path
(advancing `retry_count` without calling `PublishToExchange` at all). Only undecodable message
headers are poison — see
[outbox.md](outbox.md#retry--dead-lettering) and [ADR-033](adr_033_outbox_retry_count_status_parking.md).

> **Breaking change:** before this, a persistently-failing publish looped **forever** (returning
> only on cancel/shutdown/ACK). It now returns an error after `maxpublishattempts`. Direct
> publishers that relied on infinite blocking should handle the error; durable delivery should go
> through the outbox, which retries on its next cycle. See [migrations.md](migrations.md).

### Cold-client readiness pre-flight (`reconnect.readytimeout`)

`NewAMQPClient` starts connecting asynchronously and returns immediately — `IsReady()` only flips
true once the broker handshake and channel init finish. Before this pre-flight existed, the very
first publish against a freshly created (or mid-reconnect) client failed instantly with
`messaging.ErrNotConnected`, even though the client would have become ready a moment later (issue
#655).

`PublishToExchange` now runs a bounded, context-aware wait for readiness **before** entering the
retry loop described above. The wait polls every 100ms (the same cadence
`Registry.DeclareInfrastructure` uses) up to `reconnect.readytimeout` (default 5s):

- If the client becomes ready within the window, the publish proceeds normally into the retry loop.
- If `reconnect.readytimeout` elapses first, `PublishToExchange` returns the raw
  `messaging.ErrNotConnected` — the same unwrapped error **shape** pre-#655 callers received
  (only the timing changed: up to `readytimeout` instead of instant) — without consuming a
  `reconnect.maxpublishattempts` slot, since the wait happens entirely before the retry loop
  starts.
- If the context is canceled/deadlined, or the client is shutting down, while waiting, the
  pre-flight returns that error (`ctx.Err()` / `messaging.ErrShutdown`) instead.

There is no circuit breaker or single-flight coalescing at the client level: during a sustained
broker outage, every publish independently waits up to `min(readytimeout, ctx deadline)` before
failing — with `messaging.ErrNotConnected` when `readytimeout` expires first, or the ctx's own
error (`context.DeadlineExceeded` / `context.Canceled`) when the ctx deadline binds — so match
both when classifying. Prefer short ctx deadlines on latency-sensitive paths.

**Need fail-fast anyway?** Two working options:

- **Context deadline (preferred):** pass a `ctx` with a short deadline — the pre-flight is
  context-aware (third bullet above), so the effective wait is the *smaller* of the ctx deadline
  and `readytimeout`. This is the framework's context-first idiom, and the same deadline also
  bounds the publish attempts that follow.
- **Config:** set `reconnect.readytimeout` to a small positive value (e.g. `1ms`) for
  near-instant failure on a not-ready client.

> **Disabling the wait:** `reconnect.readytimeout: 0` in `config.yaml` is treated the same as
> leaving the key unset — like every other `reconnect.*` duration — and defaults to 5s. A
> **negative** value does not fall back to the default either: it fails startup with a validation
> error (`config_invalid: messaging.reconnect.readytimeout must be non-negative`). There is no
> way to reach a `readyTimeout <= 0` (the pre-#655 instant fail-fast) through the public API:
> `NewAMQPClient` always initializes it to the 5s default before applying `ClientOption`s, and
> `WithReadyTimeout` itself ignores non-positive values — the same guard `WithMaxPublishAttempts`
> applies to `reconnect.maxpublishattempts`' `<= 0` "unbounded retries" mode. Both zero-value
> sentinels are reachable only by building an `AMQPClientImpl` struct literal that bypasses
> `NewAMQPClient` — a dead end outside the `messaging` package: the fields are unexported, so only
> the package's own test suite can set them, and while a bare `&messaging.AMQPClientImpl{}` does
> compile externally, it is non-functional (nil unexported mutex and channels — the first method
> call panics). No *working* client with `readyTimeout <= 0` is constructible outside the
> `messaging` package. A custom `app.Options.MessagingClientFactory` supplying a
> different `AMQPClient` implementation sidesteps the concept entirely: it receives only
> `(url, log)`, so `reconnect.readytimeout` never reaches it.

### Sizing the publisher pool for multi-tenant deployments

`publisher.maxcached` is the LRU cap on cached publisher clients (in multi-tenant mode, it falls back to `multitenant.limits.tenants` when unset), not a per-tenant guarantee. When more tenants publish than the cap allows, every publish for a not-currently-cached tenant evicts the least-recently-used publisher and creates a fresh one — **eviction thrash** that silently degrades latency (each miss reopens a broker connection) without an error.

Size the cap to hold every concurrently-publishing tenant. For **statically-configured** tenants (`multitenant.tenants`) the framework counts them at startup and emits a **WARN** when the publisher pool's max size is below the configured tenant count. For **dynamic** tenant sources the count is unknown at startup, so no warning can be emitted — size the cap against your expected fleet manually.

Idle-TTL eviction is sweep-driven: publishers are only checked when the cleanup goroutine wakes every `publisher.cleanupinterval` (default 2m), so an idle publisher can outlive its `publisher.idlettl` by up to one full sweep interval — keep `cleanupinterval` well below `idlettl`.

Eviction churn is directly observable: both removal paths log at **Info** (`"Evicted publisher client due to LRU limit"` for LRU eviction, `"Cleaned up idle publisher client"` for idle-TTL cleanup), and `Manager.Stats()` exposes cumulative `evictions` and `idle_cleanups` counters alongside `active_publishers`. The stats map is surfaced as `messaging_stats` in the `GET /ready` response and under the `messaging_manager` component of `GET /_sys/health-debug` (when debug endpoints are enabled). A steadily climbing `evictions` count under normal load is the signature of an undersized cap.

> Eviction (and idle cleanup) closes the evicted publisher **outside** the manager lock, so a slow `Close()` on an evicted tenant never blocks concurrent `Publisher()` calls for other tenants.
>
> A publisher that is **still in use** when evicted (held by an in-flight request, message, or job) is detached immediately but its `Close()` is **deferred until the last borrower releases its lease** — so an in-use publisher is never closed under an active caller ([ADR-032](adr_032_lease_refcount_tenant_handles.md), the M3 fix). The lease is reference-counted by the messaging `Manager` and released by the framework at each request/message/job boundary; **application code is unchanged** (`deps.Messaging(ctx)` keeps its `(AMQPClient, error)` signature). Direct callers of `Manager.Publisher` see a new `ReleaseFunc` third return — see [migrations.md](migrations.md). (Consumers are long-lived and not leased.)
