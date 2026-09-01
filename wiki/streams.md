# RabbitMQ Streams (native stream protocol)

GoBricks consumes RabbitMQ streams over the **native stream protocol** (default
port 5552, `rabbitmq_stream` plugin) through the `messaging/streams` package.
The lane is **opt-in at the build graph** (ADR-091): import
`github.com/gaborage/go-bricks/messaging/streams` (a blank import is enough)
so the package can register its runtime factory. A `messaging.streams.uri`
without that import fails startup naming it; no URI and no import starts clean.
Streams are append-only replicated logs: reads are non-destructive, positions are
offsets, and the broker itself remembers where a named consumer got to.

Publishing over this lane is **confirmed and synchronous**: `DeclarePublisher`
returns a `*Publisher` whose `Publish` blocks until the send resolves — a broker
confirmation, a client-side failure, the context expiring, or the publisher
closing. Delivery is at-least-once, and a context expiry leaves the outcome
**unknown**: the message may still land. See [Publishing](#publishing).

## Which lane

| | AMQP lane (`x-queue-type: stream`) | Native lane (this page) |
| --- | --- | --- |
| Port / plugin | 5672, no plugin | 5552, `rabbitmq_stream` |
| Start position | `x-stream-offset` consumer arg | `Start` + stored offset |
| Offset tracking | client-side, session-local | **server-side, survives restarts** |
| Single active consumer | no | yes |
| Super streams | no | yes (RabbitMQ 3.13+) |
| Publishing | through an exchange bound to the stream queue, on AMQP publisher confirms | **direct to the stream** (`Publisher.Publish`), one broker confirmation per message, hash-routed across super-stream partitions |
| Handler concurrency | worker pool (`NumCPU*4`) | **sequential per stream**, one goroutine per partition |
| Multi-tenant | yes | yes under `messaging.tenancy: shared` (the tenant stamp seeds the tenant); per-tenant tenancy is not supported |

Pick the AMQP lane when port 5552 is not reachable, or when the deployment needs
per-tenant brokers rather than one shared one; see [messaging.md](messaging.md#stream-queues-amqp-lane). Pick this
lane when a restart must resume where it left off.

## Configuration

```yaml
messaging:
  streams:
    uri: rabbitmq-stream://user:pass@broker:5552/%2f   # or rabbitmq-stream+tls:// (5551)
    addressresolver:              # both or neither
      host: rabbitmq.example.com
      port: 5552
    offsetstore:
      countbeforestorage: 500     # handled messages before a commit (default 500)
      flushinterval: 5s           # elapsed time before a pending commit (default 5s)
```

- `messaging.streams.uri` is **not** derived from `messaging.broker.url`: the
  stream protocol is a separate listener a deployment may not expose at all. It
  carries credentials and is never logged raw.
- `addressresolver` is **required** behind a load balancer, NAT, or Docker port
  mapping. Without it the client dials the address the broker advertises in its
  metadata response, which from outside the cluster is unreachable.
- Declaring anything on this lane — a stream, a consumer, a publisher — with no
  `uri` set **fails startup**; the declarations would otherwise be silently
  dropped.
- `multitenant.enabled` together with a stream `uri` needs
  **`messaging.tenancy: shared`** (ADR-087). Under that tenancy the lane consumes
  once on the control-plane key, exactly as single-tenant does, and each delivery's
  `x-tenant-id` stamp seeds the handler's tenant. Under the default `per-tenant`
  tenancy it is a **startup validation error**: per-tenant stream consumption would
  need one Environment per tenant, which does not exist. `config.Validate` enforces
  this, and `app.NewWithConfig` runs `config.Validate` too (ADR-064), so the shape is
  caught at construction; startup repeats the check before building the manager as
  defense-in-depth.
- A delivery whose stamp is missing or malformed is refused **before the handler** and
  its offset is **not committed**, so it is redelivered rather than skipped. Set
  `TenantOptional` on a consumer whose events legitimately belong to no tenant; it
  never admits a stamp that is present but unusable.
- **Partition sizing is a documented rule, not an enforced one:** provision 2–4×
  the maximum number of consumer replicas. Partition count is fixed at creation, so
  growing later means a new super stream and a cutover, and replicas beyond the
  partition count idle. Under shared tenancy all tenants share these partitions:
  isolation is by the stamp, at the application level, not by the transport.
- Plaintext `rabbitmq-stream://` is accepted but **logs a WARN outside
  development**: the URI's credentials cross the network in the clear. There is
  no TLS configuration surface yet (see [ADR-059](adr_059_streams_consumption.md)
  future work), so terminate TLS in front of the broker or use
  `rabbitmq-stream+tls://` with a publicly-trusted certificate.

## Declaring streams and consumers

Implement `streams.StreamDeclarer` on a module (`DeclareStreams(*streams.Declarations)`);
the framework calls it during startup, validates every declaration at once, and starts
the consumers. The import that declares topology is also the import that links the lane.

```go
func (m *Module) DeclareStreams(decls *streams.Declarations) {
    decls.DeclareStream("orders", &streams.StreamSpec{
        MaxAge:         7 * 24 * time.Hour, // whole seconds (floors at 1s), as in the AMQP lane
        MaxLengthBytes: 5 * 1024 * 1024 * 1024,
    })

    decls.DeclareConsumer(&streams.ConsumerOptions{
        Stream:  "orders",
        Name:    "order-projector", // required: the offset-tracking key
        Start:   streams.OffsetFirst(),
        SAC:     true,
        Handler: m.svc.HandleOrder,
    })
}

func (s *Service) HandleOrder(ctx context.Context, msg *streams.Message) error {
    return s.project(ctx, msg.Data) // msg.Offset, msg.Stream, msg.Properties also available
}
```

Start positions: `OffsetFirst()`, `OffsetLast()`, `OffsetNext()` (the zero
value), `OffsetAt(n)`, `OffsetSince(t)`.

`DeclareStream` is replayed against the broker at startup. An identical existing
stream is accepted; a **retention mismatch** surfaces as precondition-failed and
aborts startup rather than consuming a stream configured differently from the
declaration. Duplicate `(stream, name)` consumer registrations panic at startup,
exactly as in the AMQP lane, and so does a nil `*ConsumerOptions` — a declaration
that consumes nothing is a wiring error, not a no-op.

Retention values must not be negative: a negative `MaxAge`, `MaxLengthBytes` or
`MaxSegmentSizeBytes` fails validation, because it would be dropped on the way to
the broker and silently leave the stream with the broker's default. **Zero is
different** — it is how a field asks for exactly that default.

## Typed consumers

`DeclareTypedConsumer` is the streams-lane mirror of the AMQP lane's
[typed consumers](messaging.md#typed-consumers): it decodes the message body into
a struct, validates it against the same `validate` tags HTTP handlers use, and
calls your function — so `json.Unmarshal`, the validation call and the two error
branches stop being copy-pasted into every handler.

```go
type OrderPlaced struct {
    Reference string `json:"reference" validate:"required,max=32"`
    Amount    int64  `json:"amount"    validate:"required,gt=0"`
}

func (m *Module) DeclareStreams(decls *streams.Declarations) {
    decls.DeclareStream("orders", nil)

    streams.DeclareTypedConsumer(decls, &streams.ConsumerOptions{
        Stream: "orders",
        Name:   "order-projector",
        Start:  streams.OffsetFirst(),
    }, m.svc.HandleOrderPlaced)
}

func (s *Service) HandleOrderPlaced(ctx context.Context, order OrderPlaced) error {
    return s.project(ctx, order)
}
```

`T` is inferred from the function and never spelled out. `ConsumerOptions.Handler`
must be nil — the declaration builds it — and a nil `*Declarations`, a nil
`*ConsumerOptions` or a nil function all panic at declaration, as the lane's other
wiring mistakes do.

Four entry points cover the lane's two axes:

| | plain stream | super stream |
| --- | --- | --- |
| `func(ctx, T) error` | `DeclareTypedConsumer` | `DeclareTypedSuperStreamConsumer` |
| `func(ctx, T, *streams.Message) error` | `DeclareTypedConsumerWithMeta` | `DeclareTypedSuperStreamConsumerWithMeta` |

The `WithMeta` shape is how a typed consumer still reads `msg.Offset`,
`msg.Stream` (the *partition*, on a super stream) and `msg.Properties`. There is
deliberately **no exported `NewTypedHandler`** on this lane, unlike the AMQP one:
a typed declaration carries a poison screen alongside its handler, and a bare
`streams.Handler` could not carry one — see below.

**A payload failure is deterministic poison** (ADR-092). A body that does not
decode, or decodes but fails validation, fails identically on every attempt and
every replica, so the lane:

- does **not** retry it in place, whatever `Retry` says — it is returned
  `Permanent`;
- does **not** park it when `Hold` is set, and does not park it behind an
  already-held tenant either — the screen rejects it before the hold's gate;
- skips the offset, exactly as ADR-059 settles any failure on a consumer that
  does not hold.

Parking one would defer that tenant on every drain pass, forever — the opposite
of what the hold exists for.

The consequence is that poison is ultimately **dropped**, with nothing durable
recording it. As everywhere else on this lane, "skipped" only becomes true once a
LATER delivery succeeds and commits a higher offset: the poison's own offset is
never committed, so a restart before that later commit resumes from the last
stored offset and **redelivers** it. That is harmless — the screen and the handler
reject the same bytes the same way, so it is simply skipped again — but it does
mean one malformed message can be logged more than once. Once a later success
commits past it, it survives only in the failure log line and the consume metric.

Keeping a rejected body is only partly in your reach, and the two stages differ.
A **validation-invalid** payload decoded fine, so declaring `T` without
`validate` tags lets it reach the handler, which can park the rejection itself.
An **undecodable** body never reaches the handler — there is no `T` to hand it —
so no typed declaration can retain it. To see those bytes, consume that stream
with a hand-written `Handler` on `DeclareConsumer` instead: it is given
`msg.Data` raw and does its own decoding.

Match the two modes with `errors.Is` against `streams.ErrPayloadUndecodable` and
`streams.ErrPayloadInvalid`; `errors.As` to `*streams.PayloadError` reaches
`Consumer`, `Stage` and `Fields()`. **`Error()` and `Fields()` are safe to log;
`Unwrap()` is not** — the raw cause may carry the rejected literal, the offending
byte or a partner-supplied map key, which is exactly why the framework's own
rendering never interpolates it.

## Offset semantics

**A stored offset always wins over `Start`.** At startup — and at SAC promotion —
the framework queries the broker for the consumer name's stored offset and
resumes at `stored + 1`. `Start` only applies when the broker has no offset for
that name, which makes restart behavior deterministic.

**A failed offset query never falls back to `Start`.** If the broker cannot be
asked — a connection or RPC failure, as opposed to simply having no offset — the
framework resumes from the position this process last committed, or from the
oldest retained message if it has committed none, and logs the choice at ERROR.
Falling back to `Start` would mean attaching at `OffsetNext()` by default, i.e.
past everything written since the last commit, and a stream has no redelivery to
get it back. Replay is the affordable failure here; a silent skip is not.

**Offsets are committed only AFTER a handler returned successfully.** The
client's own auto-commit is deliberately not used: it advances the offset for
messages the handler may have failed. A commit happens when either
`countbeforestorage` successes have accumulated since the last one or
`flushinterval` has elapsed since it, and once more — a **final flush** — when
consumers stop. That final flush narrows the replay window but does not close
it: nothing waits for an in-flight handler callback, so one that finishes after
the flush is never committed. **Delivery is at-least-once — a message may be
handled more than once, so handlers must be idempotent.**

That final flush is **bounded to 5 seconds in total**, across every consumer.
A super stream commits through the environment's locator connection, and the
client reconnects that locator in a retry loop with no attempt cap and no
deadline — so against a broker that is down, one commit would otherwise never
return and shutdown would never complete. When the budget runs out the
remaining commits are skipped and each is logged at WARN naming its stream
(`Shutdown offset flush budget spent`). Skipping only widens the replay window
that at-least-once delivery already allows for, which is why it is preferred
over a shutdown that hangs and takes `/ready` down with it for the whole drain.

**Without `Hold`, a failed message is not redelivered once a later success commits
past it.**
Streams have no nack. On a consumer WITHOUT `Hold`, a handler error (or a
recovered panic) is logged and counted, the offset is not committed, and the next
message is processed. With `Hold: true` the message is parked instead, the offset
commits after that ledger write, and the tenant's later messages are gated — see
below. The skip only sticks once a later success commits a *higher* offset;
restart before that and the failed message comes back, along with everything
after the last stored offset. Anything that must not be lost belongs in the
handler's own durable store, or in the hold below
([ADR-089](adr_089_per_tenant_hold_on_the_streams_lane.md)).

**Retry, then hold.** A consumer can bound in-place retries with `Retry:
&streams.RetryOptions{MaxAttempts, InitialBackoff, MaxBackoff}` — `MaxAttempts`
counts the first attempt, the wait doubles from `InitialBackoff` up to
`MaxBackoff`, and the whole policy is capped at `streams.MaxRetryAttempts` (10)
and `streams.MaxRetryWait` (1m) because the waits happen on the partition's own
delivery goroutine. A handler returning `streams.Permanent(err)` ends the
delivery on that attempt whatever the policy allows; a recovered panic is never
retried.

With `Hold: true`, a delivery that settles on a failure is not skipped — the
**tenant is held**. That covers all three ways a delivery can fail: the retries
running out, a `streams.Permanent(err)` that refused them, and a recovered
panic, which is never retried ([ADR-089](adr_089_per_tenant_hold_on_the_streams_lane.md)):

1. **Park.** The message and a tenant marker are written to the `inbox` hold
   ledger in one durable write, and the offset commits only after it succeeds. If
   that write fails the partition **stalls** on the message rather than
   committing past a message the ledger does not have.
2. **Gate.** While the tenant is held, its later messages on that consumer are
   parked instead of delivered, so its order is preserved. Other tenants on the
   same partition keep flowing.
3. **Drain.** The `inbox-hold-drain` job takes each due tenant under a lease
   (`inbox.hold.leaseduration`) and replays its rows through the consumer's own
   handler in ledger order, deleting each row only after its replay succeeds. The
   first failure stops the pass and defers the tenant under a backoff capped at
   `inbox.hold.maxbackoff`.
4. **Release.** Draining the last row deletes the tenant marker and the tenant
   leaves the held set; a concurrent park keeps it held.

A parked message becomes the ledger's rather than the broker's, and is
idempotent on `(consumer, stream, offset)`. It is **never auto-dropped** — there
is no retention on the hold ledger; rows leave through a successful replay or an
operator's `DELETE`. The backlog is visible as the gauges
`inbox.hold.tenants`, `inbox.hold.rows` and `inbox.hold.oldest_age` (keyed by
`messaging.consumer.name`), plus one WARN per drain pass naming any tenant held
longer than `inbox.hold.maxage`.

A holding consumer that declares no `Retry` gets `streams.DefaultHoldRetry` (3
attempts, 200ms initial, 2s cap). The single-attempt default is therefore a
NON-holding consumer's: one that neither holds nor declares a `Retry` still gets
exactly one attempt. `Hold: true` requires an `inbox` hold ledger with
`inbox.tenancy: shared` — see [outbox.md](outbox.md#hold-ledger) for the tables
and keys — and a consumer declaring `Hold` without one fails startup.

**Handlers run inline and sequentially within a stream — there is no worker
pool.** A stream is an ordered log: parallel handlers would break that order and
make a committed offset claim that messages behind it were handled. This is the
deliberate opposite of the AMQP lane's `NumCPU*4` default.

Parallelism comes from **partitions**, not from threads. Each partition of a
super stream is a separate connection with its own delivery loop, so a
super-stream handler is called **concurrently across partitions** — sequential
and ordered within one, concurrent between them. **A handler registered with
`DeclareSuperStreamConsumer` must be goroutine-safe**; one registered with
`DeclareConsumer` on a plain stream never needs to be. On a plain stream, SAC is
not a throughput lever; see below.

**There is no handler timeout.** Unlike an HTTP handler, a stream handler gets no
deadline: the context it receives is canceled only by `StopConsumers`, so a
handler that ignores it runs unbounded. Because delivery is sequential, one hung
handler stalls that consumer entirely and leaks its goroutine at exit — it does
not block shutdown, but nothing after it is consumed either. Respect `ctx` and
bound your own work (`context.WithTimeout` around the slow call).

### Retrying in place

A consumer may ask for a bounded in-place retry: `Retry: &streams.RetryOptions{MaxAttempts: 3, InitialBackoff: 200 * time.Millisecond, MaxBackoff: 2 * time.Second}`. The handler is re-invoked after it returns an error, waiting `InitialBackoff` before the second attempt and doubling up to `MaxBackoff` (zero means uncapped, so the doubling runs for the whole bound); the lane settles on the FINAL attempt, so one delivery is still one span, one outcome line and one consume record however often it was tried. Declaring nothing keeps today's behaviour — exactly one attempt.

Two outcomes end the retries early. A panic is never retried: a handler that panics on a message panics on it again. Neither is an error the handler wrapped in `streams.Permanent(err)`, which is its claim that retrying cannot help; the wrapper renders and unwraps as the original error, so nothing downstream has to know about it.

**The waits happen inside the partition's own delivery callback**, which is why a policy is bounded: `Validate` refuses more than `streams.MaxRetryAttempts` (10) attempts, or a policy whose worst-case total wait exceeds `streams.MaxRetryWait` (1 minute); the error names that total as a lower bound, since the walk stops at the wait that proves the crossing. That budget is one tenant's failure spending every OTHER tenant's throughput on the same partition, so a failure needing more patience than a minute is not a retry at all. Where such a failure goes instead depends on `Hold`: without it, a delivery that exhausts its policy is skipped, exactly as an unretried failure always was; with it, the tenant is held and the message parked for the drain to replay (see **Retry, then hold** above).

The context the wait selects on is the consumer's, so `StopConsumers` cuts a pending backoff rather than sleeping it out.

## Single active consumer

`SAC: true` makes the broker deliver to exactly one member of the consumer-name
group at a time (RabbitMQ 3.11+). On a **plain stream** this is **failover, not
parallelism**: the other members are standbys promoted when the active one goes
away, so more members buy availability, not throughput. On promotion the
framework re-resolves the stored offset, so a takeover resumes where the previous
active member committed.

On a **super stream** the same mechanism does more, which is why the flag is not
the caller's there: the broker distributes the *partitions* across the group, so
a second member consumes the partitions the first is not active on. Same
mechanism, two outcomes — failover on one stream, shared partitions on a super
stream.

## Super streams

A super stream is a partitioned stream: `n` ordinary streams the broker names
`<name>-0` … `<name>-(n-1)`, addressed as one. Order holds within a partition,
not across the whole super stream, and the framework tracks a **separate committed
offset per partition**.

```go
func (m *Module) DeclareStreams(decls *streams.Declarations) {
    decls.DeclareSuperStream("orders", 3, &streams.StreamSpec{
        MaxLengthBytes: 5 * 1024 * 1024 * 1024, // applies to every partition
    })

    decls.DeclareSuperStreamConsumer(&streams.SuperStreamConsumerOptions{
        SuperStream: "orders",
        Name:        "order-projector", // offset key per partition, and the group identity
        Start:       streams.OffsetFirst(),
        Handler:     m.svc.HandleOrder, // called concurrently across partitions
    })
}
```

- **There is no `SAC` field, and consumption is always a SAC group.** The client
  attaches every partition with one shared offset specification, so the promotion
  callback — fired once per partition — is the only place a per-partition stored
  offset can be restored; without it a restart would replay every partition from
  `Start`. A lone member is promoted on every partition, so a single-instance
  service loses nothing. See [ADR-059](adr_059_streams_consumption.md).
- **Scale by adding members, up to the partition count.** Members beyond `n` are
  standbys. Partitions are fixed at declaration time; sizing them is a capacity
  decision, not something to change casually (see the mismatch trap below).
- Offsets, the commit policy, the skip-on-failure semantics and the shutdown
  flush all work per partition. `/ready` and `Stats` report one position per
  partition, keyed `<partition>/<consumer name>`.
- Declaring a super stream requires **RabbitMQ 3.13+** (the client's
  `DeclareSuperStream` command); plain-stream SAC only needs 3.11+.
- **Trap: re-declaring a super stream with a different partition count is
  accepted silently.** Where `DeclareStream` surfaces a retention mismatch as
  precondition-failed and aborts startup, the client swallows "already exists"
  for super streams, so a changed `partitions` value neither reshapes the
  topology nor fails — the service simply keeps consuming the partitions that
  exist. Change the count by declaring a new super stream, not by editing the
  old one.
- **Trap: a locator outage during promotion can stall one partition, and
  `/ready` will not say so.** Resolving a partition's offset asks the broker,
  and the client's locator reconnect is uncapped — it retries forever, with no
  timeout to give up on. That call runs on the partition's own read loop, so
  while it is stuck the broker never gets its promotion answer and that
  partition consumes nothing. The consumer's status field is untouched by a
  blocked read loop, so readiness still reports healthy. Alert on
  **consumed-message rate per partition**, not on `/ready` alone; a restart
  clears it. Blast radius is one partition per stall.
- Producer-side routing picks the partition from the murmur3 hash of a
  caller-supplied `RoutingKey` — see [Publishing](#publishing). The partition a
  message actually reached is on `msg.Stream`.

## Publishing

Declare a publisher next to the stream it targets. The declaration hands back an
inert `*Publisher`; `Manager.Start` binds it to a producer at startup, on the
same broker environment the consumers use. Hold the returned handle — there is
no `ModuleDeps` field and no accessor to look one up again.

```go
type Module struct {
    orders   *streams.Publisher
    payments *streams.Publisher
}

func (m *Module) DeclareStreams(decls *streams.Declarations) {
    decls.DeclareStream("orders", &streams.StreamSpec{MaxAge: 7 * 24 * time.Hour})
    m.orders = decls.DeclarePublisher(&streams.PublisherOptions{Stream: "orders"})

    decls.DeclareSuperStream("payments", 3, nil)
    m.payments = decls.DeclareSuperStreamPublisher(&streams.SuperStreamPublisherOptions{
        SuperStream: "payments",
    })
}

func (m *Module) Emit(ctx context.Context, order *Order) error {
    err := m.orders.Publish(ctx, &streams.PublishMessage{
        Data:       order.JSON(),
        Properties: map[string]any{"order.id": order.ID}, // AMQP 1.0 application properties
    })
    if err != nil {
        return err
    }

    return m.payments.Publish(ctx, &streams.PublishMessage{
        Data:       order.PaymentJSON(),
        RoutingKey: order.CustomerID, // a string key; picks the partition, required here
    })
}
```

A publisher's target must be declared in the same `Declarations` **and as the
same kind**: publishing to a super stream with `DeclarePublisher` (or to a plain
stream with `DeclareSuperStreamPublisher`) fails validation at startup, naming
the method to use instead, and an undeclared target fails the same way. One
publisher per target per process — a second declaration on the same target
**panics** at startup, as a duplicate consumer does, and so does a nil options
pointer.

`Publish` before `Manager.Start` bound the handle returns
`ErrPublisherNotStarted`; after shutdown, `ErrPublisherClosed`. Match both with
`errors.Is`.

### What the returned error means

**`Publish` blocks until the send resolves**: a broker confirmation, a
client-side failure, `ctx` expiring, or the publisher closing. A `nil` return
means the broker acknowledged the message — not merely that it was handed to the
client. That is the whole reason the surface is synchronous: the client's own
send is asynchronous and swallows write errors, so its `nil` proves nothing and
the confirmation is the only truth available
([ADR-063](adr_063_streams_native_publishing.md)).

Errors come in two kinds, and only one of them is ambiguous.

**Rejected before submission — nothing was published.** A nil
`*PublishMessage`, a `RoutingKey` that does not match the publisher's kind,
`ErrPublisherNotStarted`, and a publish that arrives once the publisher is
already closed are all rejected before anything reaches the client. No message
was sent, so a retry cannot duplicate.

**Failed after submission — the outcome is unknown.** Once the send is in the
client's hands, a context expiry, the client's confirmation timeout, the
shutdown sweep or a rejected confirmation can each arrive while the message is
still in flight, and it may still land. The broker's own rejection is a definite
no, but it reaches the caller in the same shape as the ambiguous cases and
cannot be told apart from them — so treat every post-submission error as
**unknown**, not as failure. Delivery is **at-least-once**, exactly as on the
consume side: retrying is allowed, and **consumers must be idempotent**.

`ErrPublisherClosed` is the one sentinel that spans both kinds: it rejects a
publish that starts after shutdown, and it is also what the close sweep resolves
an already-submitted publish with. The sentinel alone does not say which
happened.

### What bounds a publish

Publishing adds **no configuration keys**, and the framework adds no timeout of
its own — the caller's `ctx` is the only bound the *framework* applies. The
client underneath has one of its own, so a publish ends on whichever fires
first.

In an HTTP handler `ctx` carries the request context's 5s default; background
work — a scheduled job, a consumer handler, a relay — must set a deadline of its
own, because a `context.Background()` publish can wait indefinitely while the
producer reconnects. See [context_deadlines.md](context_deadlines.md).

Two client behaviours decide which bound actually applies:

- **A reconnect can park a send indefinitely, and no client timeout covers
  it.** While a producer is reconnecting, the client waits on a condition
  variable with no timeout. The framework runs the send on a goroutine it is
  willing to abandon and selects against `ctx`, so the caller's deadline — or
  shutdown — is what returns control.
- **While connected, the client fails a message left unconfirmed for ~10s.**
  That is the client's own `ConfirmationTimeOut` default, which GoBricks exposes
  no key for; a deadline longer than it usually surfaces that error at around
  ten seconds instead of waiting on the broker. The ticker behind it stops while
  the producer reconnects, and a send that never reached the client's queue is
  not tracked by it at all — which is why the first bullet is the unbounded
  case.

### Routing keys

`PublishMessage.RoutingKey` selects the partition of a super stream: the client
hashes it with murmur3 under RabbitMQ's shared seed and takes the remainder over
the partition list. That is the cross-client default, so the same key over the
same partition list lands where the Java, .NET and Python clients would put it.
The partition count is the divisor, so changing it moves existing keys to
different partitions — one more reason it is fixed at declaration time (see the
mismatch trap above).

- On a **super-stream** publisher a non-empty key is **required**. An empty one
  is rejected rather than defaulted: hashing `""` is well defined and would pile
  every message onto a single partition.
- On a **plain-stream** publisher the key must be **empty** — a plain stream has
  no partitions to pick.

Both violations are rejected before the client is touched, so they fail
immediately and send nothing. Key routing (asking the broker to resolve a key to
partitions) and producer deduplication are deferred
([ADR-063](adr_063_streams_native_publishing.md)).

### The message

`Data` becomes the AMQP 1.0 data section of the body, and `Properties` become
the application properties a consumer reads back on `msg.Properties`. The
caller's map is **copied**, never aliased, so publishing does not write into it.

The framework injects W3C trace context into those properties through its own
`trace` package — `traceparent`, `tracestate` when the context carries one, and
`X-Request-ID` — so both messaging lanes write the same header names. A
`traceparent` the caller put in `Properties` itself is preserved, while
`X-Request-ID` is always overwritten with the trace ID aligned to it. The consume
side now reads them back: this lane runs on the shared delivery pipeline
([ADR-068](adr_068_delivery_pipeline.md), [ADR-069](adr_069_pipeline_owns_settlement_timing.md)),
which extracts the carrier into the per-message context, so a handler gets the
originating trace ID from `trace.IDFromContext(ctx)` rather than parsing
`msg.Properties` itself. Inbound identifiers are validated at that seam
([ADR-070](adr_070_inbound_trace_identifier_validation.md)) — a non-conforming
one is discarded and replaced rather than carried.

### Readiness, stats and shutdown

Publishers count on the same non-critical `streams` probe as consumers: `/ready`
reports `unhealthy` unless every bound publisher's connection is open, and the
`streams_stats` body carries a `publishers` count beside `consumers`. The probe
exists for a publisher-only service too — the manager is built whenever anything
was declared.

On shutdown the consumers stop first and the publishers close after them,
because a handler may publish on its way out. Closing the producer gives
in-flight confirmations a last chance to arrive; every publish still awaiting
one after that is then resolved with `ErrPublisherClosed` rather than left
hanging, because a send parked in a reconnect never reached the client's queue
and would never be confirmed at all.

The sweep takes **both** kinds of outstanding send, and the sentinel does not
tell them apart: the parked one definitely did not land, while one already
submitted may have been accepted by the broker before the producer closed. So a
shutdown `ErrPublisherClosed` is an unknown outcome, not proof of non-delivery —
the [post-submission case](#what-the-returned-error-means) above, with the same
rule: retry only where consumers are idempotent.

### The outbox relay as a super-stream producer

A service that needs its super-stream publishes to survive a crash publishes them through the
transactional outbox instead of calling a publisher directly: list the target in
`outbox.superstreams`, give the event a `Stream`, and the relay publishes it on this lane with
the row's tenant stamp as the partition key ([outbox.md](outbox.md#lanes-and-ordering)).

A non-empty `outbox.superstreams` requires `messaging.streams.uri`. Every listed name must
already be declared as a super stream (`DeclareSuperStream`) before the outbox creates its
publisher — otherwise startup fails with "undeclared super stream". See
[C61.23](migrations.md) for the hop that added the stream leg.

The consequence to know: the outbox declares one publisher per listed super stream, and a
super stream accepts only ONE publisher per process. So a target the outbox owns cannot also be
published to directly by another module in the same service — that second declaration is
refused at startup. Publish through the outbox, or do not list the stream.

## Observability

Each delivery opens a Consumer-kind span named `"<stream> receive"` under the
`go-bricks/messaging` tracer and records `messaging.client.operation.duration`
plus `messaging.client.consumed.messages` with `messaging.system=rabbitmq`,
`messaging.operation.name=receive`, `messaging.destination.name=<stream>`, and
`error.type` when handling failed. The consumed counter increments once per
delivery regardless of the outcome — `error.type` separates them. After each
offset settlement attempt, the lane increments `messaging.settlement.total` with
`lane=streams` and `outcome=committed` or `failed`.

Each publish opens a Producer-kind span named `"<stream> publish"` on the same
tracer, carrying `messaging.message.body.size`, and records
`messaging.client.operation.duration` plus `messaging.client.sent.messages` with
`messaging.operation.name=publish` and the same destination and `error.type`
attributes. Unlike the consumed counter, the sent counter increments **only for
a confirmed publish** — a failed or abandoned one is visible as duration with an
`error.type`.

`/ready` gains a `streams` component (and `streams_stats`) once anything is
declared on this lane: `healthy` while every consumer and publisher is
connected, `unhealthy` whenever one is not — reconnecting, closed, or the
manager stopped. The probe is **non-critical** —
the reliable consumers and producers recover on their own, so a broker flap must
not pull the whole service out of the load
balancer. Trace-context propagation from published messages is read back on the
consume side, through the shared delivery pipeline.

## Operations

- Enable the plugin: `rabbitmq-plugins enable rabbitmq_stream`.
- RabbitMQ 3.11+ for single active consumer, **3.13+ for super streams** — the
  framework declares one at startup, and that command is 3.13-only.
- Publish port 5552 (or 5551 for TLS) and make it reachable from the service.
- Behind an LB or NAT, set `messaging.streams.addressresolver.*`.
- Streams need explicit retention (`MaxAge` / `MaxLengthBytes`); they do not
  shrink when consumed.

### Super-stream `SubEntrySize = 1` (vendor pin)

Pointer-identity confirmation correlation requires the client's default
`SubEntrySize` of 1 ([ADR-063](adr_063_streams_native_publishing.md)). On a
plain stream the production options are those defaults verbatim. On a super
stream the field is not on `SuperStreamProducerOptions` at all: each
partition producer is built inside the client by `ConnectPartition` calling
`NewProducerOptions()`, so go-bricks cannot assert the size at startup. A
client refactor of that construction would break every super-stream publish
on its deadline with no other signal.

The two guards are:

- the `go.mod` comment on `rabbitmq-stream-go-client` — any version bump must
  re-verify `ConnectPartition` still constructs partition producers with
  default `SubEntrySize`;
- `TestStreamsSuperStreamPublisherPartitionsIntegration` — the only runtime
  guard, and it stays in the required integration suite.

## Testing

`testing/containers` boots a broker with the plugin:

```go
cfg := containers.DefaultRabbitMQConfig()
cfg.EnableStreamPlugin = true
c := containers.MustStartRabbitMQContainer(ctx, t, cfg).WithCleanup(t)

opts := streams.ManagerOptions{
    URI:                 c.StreamURI(),
    AddressResolverHost: c.Host(), // required under Docker port mapping
    AddressResolverPort: c.StreamPort(),
    Logger:              logger.New("error", false), // required: NewManager panics without one
}
```

See `messaging/streams/streams_integration_test.go` for the offset-restore and
skip-on-failure proofs, the publish round trip, super-stream partitioning by
routing key, and publish rejection after a stop.
