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

RabbitMQ 4.3.0 denies `transient_nonexcl_queues` by default: a queue declared with both `Durable: false` and `Exclusive: false` gets the connection closed with a 541 instead of the queue created. The helpers above are unaffected — `NewQueue` defaults to `Durable: true` — but a hand-built `QueueDeclaration` using that transient shape needs the broker configured with `deprecated_features.permit.transient_nonexcl_queues = true`, which is what GoBricks' own RabbitMQ test container sets.

**Key Helpers:** `DeclareTopicExchange()`, `DeclareQueue()`, `DeclareBinding()`, `DeclarePublisher()`, `DeclareConsumer()`

**Re-declaring one queue name is allowed when the shapes are compatible.** Two declarations of the same queue *merge*: the four flags (`Durable`, `AutoDelete`, `Exclusive`, `NoWait`) must be equal, and any `Args` key they share must carry the same value; the union of their `Args` is what reaches the broker. So `DeclareQueueWithDLQ("orders.events.queue", nil)` and `DeclareQueue("orders.events.queue")` from two different modules now compose, instead of whichever ran last silently dropping the other's dead-letter args — declaration order across modules is invisible at any single call site, and the pre-merge behavior could revert a queue to dropping failed deliveries with no error and no WARN. Incompatible shapes (a differing flag, or one `Args` key with two values) keep the first declaration and fail startup with a single aggregate error naming every conflict:

```text
declaration validation failed: conflicting queue declarations (2 conflict(s)) — declarations merge only when compatible; align the call sites (DeclareQueueWithDLQ and DeclareQueue on one name must agree)
queue "orders.events.queue": Args["x-dead-letter-exchange"] kept "orders.dlx" vs rejected ""
queue "payments.events.queue": Durable kept "true" vs rejected "false"
```

`kept` is the incumbent — the declaration still in effect — and `rejected` is the one that was refused, so the message says which of the two call sites won. Repeats of one disagreement collapse: the count enumerates distinct conflicts, not rejected declarations.

Two caveats on `Args`. Values are compared with `reflect.DeepEqual`, which is **type-sensitive**: `int(1)` and `int64(1)` are a conflict, not a match, so give one `Args` key the same Go type at every call site. And the contested values are rendered into the startup error, which the framework logs — `Args` is broker topology, so never put credentials, tokens, or PII there.

Exchanges and bindings are unaffected: `RegisterExchange` still keeps the last declaration of a name, and `RegisterBinding` still appends every declaration it is given (two identical bindings both reach the broker; neither replaces the other).

**For verbose before/after comparison**, see [messaging/declarations.go](../messaging/declarations.go)

## Consumer Registration Best Practices

### CRITICAL: Deduplication Rules

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

## Typed Consumers

`DeclareTypedConsumer` is the consumer mirror of `server.POST(hr, r, path, handler)`: it binds the message body to a struct, validates it against the same `validate` tags HTTP handlers use, and calls your function — so `json.Unmarshal`, the validation call, and the two error branches stop being copy-pasted into every `Handle`.

```go
type OrderCreated struct {
    OrderID  int64  `json:"orderId"  validate:"required"`
    Currency string `json:"currency" validate:"required,len=3"`
}

func (m *Module) DeclareMessaging(decls *messaging.Declarations) {
    exchange := decls.DeclareTopicExchange("orders.events")
    queue := decls.DeclareQueueWithDLQ("orders.events.queue", nil)
    decls.DeclareBinding(queue.Name, exchange.Name, "orders.*")

    messaging.DeclareTypedConsumer(decls, &messaging.ConsumerOptions{
        Queue:     queue.Name,
        Consumer:  "order-processor",
        EventType: "OrderCreated",
    }, m.svc.HandleOrderCreated) // func(context.Context, OrderCreated) error
}
```

`T` is inferred from the function, so it is never spelled out. `ConsumerOptions.Handler` must be nil — the helper builds the handler and panics at declaration time if one is already set (use `DeclareConsumer` for a hand-written `MessageHandler`). The queue argument is deliberately absent: declare the queue yourself, exactly as an untyped `DeclareConsumer(opts, nil)` does. A consumer naming a queue nobody declared surfaces at `Declarations.Validate()` as `consumer references non-existent queue`, not at the call site.

`messaging.NewTypedHandler[T](eventType, fn)` builds the same adapter without registering anything, for a hand-assembled `ConsumerOptions`.

**Failure semantics.** Decode and validation failures return a `*messaging.PayloadError` before `fn` runs, and the worker loop nacks it WITHOUT requeue like any other handler error — which is right, since neither failure gets better on redelivery. Pair the queue with `DeclareQueueWithDLQ` so those messages park instead of being dropped. Discriminate the two with `errors.Is`:

```go
if errors.Is(err, messaging.ErrPayloadUndecodable) { /* malformed body */ }
if errors.Is(err, messaging.ErrPayloadInvalid)     { /* decoded, failed validation */ }
```

`fn`'s own error is returned **unwrapped**, so `errors.Is(err, ErrAlreadyProcessed)` against your own sentinels keeps working and never collides with the two above.

**No payload bytes in the error — and where that guarantee ends.** AMQP bodies carry partner PII/PCI, so `PayloadError.Error()` is composed from schema facts only: the event type, the stage, and the failing field namespaces. `Fields()` redacts every bracketed span (`Limits[4111111111111111]` → `Limits[*]`), because go-playground interpolates map keys verbatim. Both are safe to log. `Unwrap()` is **not** — it returns the raw decoder or validator error, which may quote a rejected literal, an offending byte, an unknown key, or a map key. It is the deliberate escape hatch; logging it is opt-in and on you.

The decode summary's field path is gated on the payload type, not on the error: a decoder reports a *map* destination's path with the input key in it (`limits.<key>`, from Go 1.27), and a `json.Unmarshaler` or `encoding.TextUnmarshaler` field's path with whatever that method decoded into. So if a map, an interface (including `any`), or either unmarshaler interface — `time.Time` included — is reachable from `T`, the summary drops the path and reports only the wanted type and byte offset. A `T` free of all three keeps the field name.

```text
messaging: decode failed for event "OrderCreated": json: type mismatch at field "orderId" (want int64, offset 15)
messaging: decode failed for event "LimitsUpdated": json: type mismatch (want int, offset 47)
messaging: validate failed for event "OrderCreated" (fields: OrderCreated.Currency)
```

**Delivery metadata.** `DeclareTypedConsumerWithMeta` / `messaging.NewTypedHandlerWithMeta[T](eventType, fn)` are the metadata-carrying siblings of `DeclareTypedConsumer` / `NewTypedHandler`: `fn` is `func(ctx context.Context, payload T, meta messaging.Metadata) error`, with the same decode → validate → `fn` pipeline and failure semantics. `Metadata` exposes three read-only accessors — `Headers() amqp.Table`, `EventType() string`, `Redelivered() bool` — so a typed consumer can read `x-outbox-event-id` and wrap its body in `inbox.ProcessOnce`, the canonical composition for an outbox-fed consumer:

```go
// Same DeclareMessaging body as above, with this call REPLACING the
// DeclareTypedConsumer one — the same queue + consumer tag + event type twice
// panics at startup. m.inbox is deps.Inbox, captured in Init; DeclareMessaging
// receives only decls.
messaging.DeclareTypedConsumerWithMeta(decls, &messaging.ConsumerOptions{
    Queue:     queue.Name,
    Consumer:  "order-processor",
    EventType: "OrderCreated",
}, func(ctx context.Context, evt OrderCreated, meta messaging.Metadata) error {
    id, ok := outbox.EventIDFromHeaders(meta.Headers())
    if !ok {
        // No id, no dedup key — processing here would repeat the business
        // write on every redelivery, so fail closed.
        return fmt.Errorf("missing x-outbox-event-id header")
    }
    return m.inbox.ProcessOnce(ctx, id, func(ctx context.Context, tx dbtypes.Tx) error {
        return processTx(ctx, tx, evt) // business write joins the dedup transaction
    })
})
```

**Mixed-queue variant.** A queue that also carries directly-published messages has deliveries with no ledger key by design. There, and only there, swap the `!ok` branch for `return process(ctx, evt)` — processed without dedup, so that handler must be idempotent on its own. An outbox-only queue keeps the fail-closed default above.

**Headers are publisher-controlled.** AMQP headers come from whoever published the message, so on a queue fed by an exchange outside this service `meta.Headers()` is caller-supplied input — reading it is identification, not authorization. In the dedup shape above the publisher therefore picks the ledger key: replaying a known `x-outbox-event-id` makes `ProcessOnce` skip the handler and ACK (a silent drop), and novel ids each cost a ledger row until retention sweeps them. Omitting the header is a third lever: the relay stamps `x-outbox-event-id` on every message it publishes, so a publisher that simply drops it would opt out of dedup entirely — which is why the example returns an error on `!ok` rather than processing, and why the mixed-queue variant is a deliberate opt-in for a queue whose traffic you know. Broker-side publish authorization is what bounds all three.

**Concurrency.** One adapter instance serves every worker of the consumer and every tenant replaying the declarations. It holds no mutable state and allocates a fresh payload per delivery, so the concurrency rules below apply unchanged: the default is `NumCPU * 4` workers, and `Workers: 1` still buys sequential processing when ordering matters. Your `fn` must be safe for concurrent use.

**Non-struct `T`** (`[]int`, `map[string]int`, a bare scalar) fails closed on the first delivery with `ErrPayloadInvalid` and no field list — go-playground validates structs only, and skipping validation silently would be worse.

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

**Observability:** ERROR logs ALWAYS include `queue`, `event_type`, `error`, `correlation_id` (the framework trace ID) and `delivery_tag`. They include `amqp_correlation_id`, `message_id`, `routing_key` and `exchange` only when that field was both carried and accepted: these are the delivery's four identity fields — `amqp_correlation_id` and `message_id` are content-header properties the publisher sets, `routing_key` and `exchange` are `basic.deliver` envelope metadata the broker supplies — and they are validated once per delivery, since no header extractor reaches either kind. A field that fails is omitted from its log field, its span attribute and its own metric attribute; the derived `messaging.destination.name` is still emitted, with that segment empty, because it is always stamped. A line that dropped a field carries `identity_rejected: true`, and the failure and panic lines carry `delivery_tag` so the delivery stays identifiable. The handler still sees the raw `*amqp.Delivery` (ADR-070, C60.17). Each delivery opens a Consumer-kind span named `"<queue> receive"` and records `messaging.client.operation.duration` plus `messaging.client.consumed.messages` when it finishes — both carrying `error.type` when handling failed, so a failure is separable on the counter as well as the histogram (ADR-068).

**Best Practices:** Thorough handler testing, monitor ERROR logs with alerts, use trace IDs for manual replay.

**Breaking Change (v2.X):** Previous behavior auto-requeued errors (infinite retry risk). New behavior drops failed messages with rich logging. Review handler error handling and set up monitoring.

### Dead-Lettering

Handler errors and panics nack without requeue. Without a dead-letter exchange
configured on the queue, that message is dropped (logged, but gone). Setting
`x-dead-letter-exchange` on the queue tells RabbitMQ to park the message on
that exchange instead of dropping it.

**`DeclareQueueWithDLQ` — the one-call form:**

```go
queue := decls.DeclareQueueWithDLQ("orders.queue", nil)
```

This declares a durable fanout exchange (`orders.queue.dlx`), a parking queue
(`orders.queue.dlq`) bound to it, and sets `x-dead-letter-exchange` on
`orders.queue` — the full route in one call. A fanout DLX is used
deliberately: a dead-lettered message keeps its original routing key, and a
direct/topic DLX whose binding key doesn't happen to match that routing key
silently drops the message on the floor instead of parking it. Fanout ignores
routing keys entirely, so the parking queue always receives it. Override the
derived names or set `x-dead-letter-routing-key` via `&messaging.DeadLetterSpec{
Exchange: "...", ParkingQueue: "...", RoutingKey: "..."}` as the second
argument. Inspect a parked message's `x-death` header (queue, exchange,
reason, count) for triage.

**Custom topology — the raw `Args` escape hatch:**

For DLX topology `DeclareQueueWithDLQ` doesn't fit (shared DLX across queues,
non-fanout exchange with a deliberately matching binding key, ...), set
`Args["x-dead-letter-exchange"]` directly and declare/bind the DLX and parking
queue yourself:

`Declarations.RegisterQueue` deep-copies `Args` at registration — and
`decls.DeclareQueue(name)` registers immediately — so `Args` must be set on the
declaration **before** registering:

```go
q := messaging.NewQueue("orders.queue")
q.Args["x-dead-letter-exchange"] = "orders.dlx" // failed deliveries park here
decls.RegisterQueue(q)
```

For a queue already registered elsewhere, mutate the stored copy instead:
`decls.Queues["orders.queue"].Args["x-dead-letter-exchange"] = "orders.dlx"`.

Args participate in RabbitMQ's declare-equivalence check: redeclaring an
existing queue with different args fails the channel with 406
PRECONDITION_FAILED. Values must be amqp091-supported types (string,
int/int64, bool, float64, nested `amqp.Table`, ...).

Both forms only shape topology (Tier 1). Bounded redelivery — capping retries
via `x-death` count before parking permanently — remains future work; see
[#721](https://github.com/gaborage/go-bricks/issues/721).

## Stream Queues (AMQP lane)

A RabbitMQ **stream** is an append-only replicated log rather than a classic
queue: reads are non-destructive, so acking never deletes anything and each
consumer picks its own start position in the log. This lane speaks ordinary
AMQP 0.9.1 over the existing broker connection (port 5672) — no extra port, no
extra dependency.

**Declaring:**

```go
decls.DeclareStreamQueue("orders.events", &messaging.StreamQueueSpec{
    MaxAge:              7 * 24 * time.Hour, // x-max-age, whole seconds (floors at 1s)
    MaxLengthBytes:      10 << 30,           // x-max-length-bytes
    MaxSegmentSizeBytes: 500 << 20,          // x-stream-max-segment-size-bytes
})
```

A `nil` spec declares the stream with broker-default retention. Zero-valued
fields are omitted, so each retention arg is opt-in.

**Consuming** — pick the start position with the `x-stream-offset` consumer
argument:

```go
decls.DeclareConsumer(&messaging.ConsumerOptions{
    Queue:     "orders.events",
    Consumer:  "orders-projector",
    EventType: "order.created",
    Handler:   handler,
    AutoAck:   false,
    Args:      map[string]any{"x-stream-offset": "first"},
}, nil)
```

Legal `x-stream-offset` values: `"first"`, `"last"`, `"next"` (the broker
default when the arg is absent), an absolute offset (`int`/`int64` ≥ 0), a
`time.Time`, or an interval string such as `"7D"`. Each delivery carries its own
position in `delivery.Headers["x-stream-offset"]`.

An `int` offset is safe up to the platform's `int` range: AMQP encodes a Go
`int` as a 32-bit field, so the framework widens it to `int64` before it reaches
the broker. Without that, an offset past 2³¹ — the range a high-throughput
stream actually reaches — would truncate silently, and `1 << 32` would arrive as
`0` and replay the whole stream. Go's `int` is itself 32-bit on some platforms,
where such a value cannot be held at all and no widening can recover it, so use
`int64` for offsets beyond 2³¹.

**Startup validation** — `Declarations.Validate` rejects, by name, five shapes
the broker would otherwise refuse with an opaque channel error:

1. A stream queue that is not durable.
2. A stream queue that is exclusive or auto-delete.
3. A stream consumer with `AutoAck: true` — streams require manual acks,
   because acks act as consumer credit.
4. An `x-stream-offset` on a consumer whose queue is not a stream queue (the
   broker would silently ignore it).
5. An `x-stream-offset` value on a stream queue that isn't a valid position
   (not `"first"`/`"last"`/`"next"`, a non-negative int, a `time.Time`, or an
   interval like `"7D"`).

**Resume on broker flap:** when the broker drops the delivery channel, the
supervisor re-subscribes one past the last offset it handed to the worker pool
instead of replaying the whole stream from the declared start position. This is
best-effort and session-local: messages already in flight may be redelivered,
and a **process restart** re-attaches at the declared offset. AMQP 0.9.1 has no
server-side offset tracking, so handlers must be idempotent — the framework-wide
consumer rule.

Consumer concurrency is unchanged in this lane (see below), so a stream consumer
that must preserve log order needs `Workers: 1`.

Server-side offset tracking, single active consumer, and super streams need the
native stream protocol (port 5552), which this lane does not cover — see
[streams.md](streams.md).

**Publishing** to a stream queue here goes through an exchange bound to it, like
any other queue. The native lane publishes to the stream directly instead, with
one synchronous broker confirmation per message and murmur3 partition routing on
a super stream — see [streams.md#publishing](streams.md#publishing).

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
- Caps are applied silently (no warning is currently logged when a value is reduced)

**Performance Impact (8-core machine, 100ms handler):**

| Version | Workers | Throughput | Speedup |
| --- | --- | --- | --- |
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

GoBricks applies production-safe AMQP reconnection defaults unconditionally at startup — even when
`messaging.broker.url` is unset (multi-tenant deployments rely on this: per-tenant clients and
cross-field validators like the outbox `publishtimeout` guards read these effective values):

| Setting | Default | Purpose |
| --------- | --------- | --------- |
| `reconnect.delay` | 5s | Initial delay before reconnect attempts |
| `reconnect.reinitdelay` | 2s | Delay between channel re-initialization |
| `reconnect.resenddelay` | 5s | Delay before resending failed messages |
| `reconnect.connectiontimeout` | 30s | Per-publish broker confirmation (ACK/NACK) timeout |
| `reconnect.readytimeout` | 5s | Bounded pre-flight wait for a not-yet-ready client before a publish begins (see below) |
| `reconnect.maxpublishattempts` | 5 | Max publish attempts before returning `ErrPublishRetriesExhausted` (see below) |
| `reconnect.maxdelay` | 60s | Maximum backoff cap for exponential retry |
| `publisher.maxcached` | 50 single-tenant / unset multi-tenant (pool scales to `multitenant.limits.tenants`) | Maximum cached publisher channels |
| `publisher.idlettl` | 1h single-tenant / 10m multi-tenant | TTL for idle publisher channels |
| `publisher.cleanupinterval` | 2m | How often the idle-publisher cleanup goroutine runs |

The `publisher.idlettl` default is deployment-mode-dependent: `multitenant.enabled: false` gets 1h,
`multitenant.enabled: true` gets a shorter 10m to bound per-tenant publisher churn (see
config/validation.go: `applyMessagingDefaults`). An explicit `publisher.idlettl` always overrides
both defaults, in either mode.

**Override defaults** in `config.yaml`:

```yaml
messaging:
  reconnect:
    delay: 10s            # Slower initial reconnect
    maxdelay: 120s       # Higher backoff cap
  publisher:
    maxcached: 100       # More cached publishers for high-throughput
    idlettl: 2h          # Keep publishers longer than the default (1h single-tenant / 10m multi-tenant)
```

### Bounded publish retries (`reconnect.maxpublishattempts`)

`PublishToExchange` (and the `Publish` convenience) retries a failing publish — publish error,
broker NACK, or confirmation timeout — but the loop is **bounded** by
`reconnect.maxpublishattempts` (default 5). On exhaustion it returns
`messaging.ErrPublishRetriesExhausted` **wrapping the last cause**, so callers can classify the
failure:

| Cause sentinel | Meaning |
| --- | --- |
| `messaging.ErrPublishNacked` | the broker received the message and returned `basic.nack` (a transient broker condition — disk alarm, mirror resync, failover; also how a missing exchange surfaces) |
| `messaging.ErrPublishConfirmTimeout` | no ACK/NACK arrived within `connectiontimeout` |

`messaging.ErrInvalidPublishDestination` is refused **before** any of this. The exchange and routing key (basic.publish's
method frame) and every header key (the content-header frame beside
`CorrelationId`) are AMQP shortstrs, and amqp091 answers an over-long one with a
frame-write error by shutting down the whole `Connection` every publisher in the process shares —
so a publish carrying one is rejected up front, with zero channel attempts, no reconnect and no
retry: the frame is unwritable whatever the broker's state. The error names the field and its byte
length, never the value. The bound is 255 bytes (empty is legal), it is length only — the charset
is the *consume* side's rule, for a different reason (ADR-070) — and `Declarations.Validate`
applies the same bound at startup so an over-long declared name never reaches a publish.

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
`messaging.ErrNotConnected`, even though the client would have become ready a moment later
(issue #655).

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

Idle-TTL eviction is sweep-driven: publishers are only checked when the cleanup goroutine wakes every `publisher.cleanupinterval` (default 2m), so an idle publisher can outlive its `publisher.idlettl` by up to one full sweep interval — keep `cleanupinterval` well below `idlettl`. The sweep starts when the manager is constructed and stops in `Manager.Close()`; calling `StartCleanup` yourself is not required, and a second call while a loop is already running is a no-op.

Eviction churn is observable via counters, not logs: `Manager.Stats()` exposes cumulative `evictions` and `idle_cleanups` counters alongside `active_publishers` (there is no per-event log line for either removal path). The stats map is surfaced as `messaging_stats` in the `GET /ready` response and as the `messaging` component's `details` in `GET /_sys/health-debug` (when debug endpoints are enabled). A steadily climbing `evictions` count under normal load is the signature of an undersized cap.

> Eviction (and idle cleanup) closes the evicted publisher **outside** the manager lock, so a slow `Close()` on an evicted tenant never blocks concurrent `Publisher()` calls for other tenants.
>
> A publisher that is **still in use** when evicted (held by an in-flight request, message, or job) is detached immediately but its `Close()` is **deferred until the last borrower releases its lease** — so an in-use publisher is never closed under an active caller ([ADR-032](adr_032_lease_refcount_tenant_handles.md), the M3 fix). The lease is reference-counted by the messaging `Manager` and released by the framework at each request/message/job boundary; **application code is unchanged** (`deps.Messaging(ctx)` keeps its `(AMQPClient, error)` signature). Direct callers of `Manager.Publisher` see a new `ReleaseFunc` third return — see [migrations.md](migrations.md). (Consumers are long-lived and not leased.)
