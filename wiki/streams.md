# RabbitMQ Streams (native stream protocol)

GoBricks consumes RabbitMQ streams over the **native stream protocol** (default
port 5552, `rabbitmq_stream` plugin) through the `messaging/streams` package.
Streams are append-only replicated logs: reads are non-destructive, positions are
offsets, and the broker itself remembers where a named consumer got to.

Publishing over this lane is **confirmed and synchronous**: `DeclarePublisher`
returns a `*Publisher` whose `Publish` blocks until the broker confirms the
message, the context expires, or the publisher closes. Delivery is
at-least-once — a context expiry does not prove the message failed.

## Which lane

| | AMQP lane (`x-queue-type: stream`) | Native lane (this page) |
| --- | --- | --- |
| Port / plugin | 5672, no plugin | 5552, `rabbitmq_stream` |
| Start position | `x-stream-offset` consumer arg | `Start` + stored offset |
| Offset tracking | client-side, session-local | **server-side, survives restarts** |
| Single active consumer | no | yes |
| Super streams | no | yes (RabbitMQ 3.13+) |
| Handler concurrency | worker pool (`NumCPU*4`) | **sequential per stream**, one goroutine per partition |
| Multi-tenant | yes | no — single-tenant only |

Pick the AMQP lane when port 5552 is not reachable or the deployment is
multi-tenant; see [messaging.md](messaging.md#stream-queues-amqp-lane). Pick this
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
- Declaring stream consumers with no `uri` set **fails startup** — the
  declarations would otherwise be silently dropped.
- `multitenant.enabled` together with a stream `uri` is a **startup validation
  error**. Per-tenant stream consumption needs one Environment per tenant, which
  does not exist yet. `config.Validate` enforces this, so a service assembled by
  hand — `app.NewWithConfig` takes a `*config.Config` and never validates it —
  would slip past; startup repeats the check before building the manager, and
  both paths carry the `single-tenant only` marker.
- Plaintext `rabbitmq-stream://` is accepted but **logs a WARN outside
  development**: the URI's credentials cross the network in the clear. There is
  no TLS configuration surface yet (see [ADR-059](adr_059_streams_consumption.md)
  future work), so terminate TLS in front of the broker or use
  `rabbitmq-stream+tls://` with a publicly-trusted certificate.

## Declaring streams and consumers

Implement `app.StreamDeclarer` on a module; the framework calls it during
startup, validates every declaration at once, and starts the consumers.

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

**A failed message is skipped, not redelivered.** Streams have no nack: on a
handler error (or a recovered panic) the failure is logged and counted, the
offset is not committed, and the next message is processed. The skip only sticks
once a later success commits a *higher* offset; restart before that and the
failed message comes back, along with everything after the last stored offset.
Anything that must not be lost belongs in the handler's own durable store.
Parking failed messages is future work
([ADR-059](adr_059_streams_consumption.md)).

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
- Producer-side routing (which partition a message lands on) is out of scope
  here, as all stream publishing is. Publish through the AMQP lane or a
  dedicated client; the partition a message reached is on `msg.Stream`.

## Observability

Each delivery opens a Consumer-kind span named `"<stream> receive"` under the
`go-bricks/messaging` tracer and records `messaging.client.operation.duration`
plus `messaging.client.consumed.messages` with `messaging.system=rabbitmq`,
`messaging.operation.name=receive`, `messaging.destination.name=<stream>`, and
`error.type` when handling failed. The consumed counter increments once per
delivery regardless of the outcome — `error.type` separates them.

`/ready` gains a `streams` component (and `streams_stats`) once a stream consumer
is running: `healthy` while every consumer is connected, `not_ready` while any is
reconnecting. The probe is **non-critical** — the reliable consumers recover on
their own, so a broker flap must not pull the whole service out of the load
balancer. Trace-context propagation from AMQP-published messages is not
implemented (future work).

## Operations

- Enable the plugin: `rabbitmq-plugins enable rabbitmq_stream`.
- RabbitMQ 3.11+ for single active consumer, **3.13+ for super streams** — the
  framework declares one at startup, and that command is 3.13-only.
- Publish port 5552 (or 5551 for TLS) and make it reachable from the service.
- Behind an LB or NAT, set `messaging.streams.addressresolver.*`.
- Streams need explicit retention (`MaxAge` / `MaxLengthBytes`); they do not
  shrink when consumed.

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
skip-on-failure proofs.
