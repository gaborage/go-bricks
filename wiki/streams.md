# RabbitMQ Streams (native stream protocol)

GoBricks consumes RabbitMQ streams over the **native stream protocol** (default
port 5552, `rabbitmq_stream` plugin) through the `messaging/streams` package.
Streams are append-only replicated logs: reads are non-destructive, positions are
offsets, and the broker itself remembers where a named consumer got to.

Publishing to streams is **not** part of this surface. Publish through the AMQP
lane (a stream queue bound to an exchange) or a dedicated client — see
[ADR-059](adr_059_streams_consumption.md).

## Which lane

| | AMQP lane (`x-queue-type: stream`) | Native lane (this page) |
| --- | --- | --- |
| Port / plugin | 5672, no plugin | 5552, `rabbitmq_stream` |
| Start position | `x-stream-offset` consumer arg | `Start` + stored offset |
| Offset tracking | client-side, session-local | **server-side, survives restarts** |
| Single active consumer | no | yes |
| Super streams | no | Phase 3 |
| Handler concurrency | worker pool (`NumCPU*4`) | **sequential, inline** |
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

**Offsets are committed only AFTER a handler returned successfully.** The
client's own auto-commit is deliberately not used: it advances the offset for
messages the handler may have failed. A commit happens when either
`countbeforestorage` successes have accumulated since the last one or
`flushinterval` has elapsed since it, and once more — a **final flush** — when
consumers stop. That final flush narrows the replay window but does not close
it: nothing waits for an in-flight handler callback, so one that finishes after
the flush is never committed. **Delivery is at-least-once — a message may be
handled more than once, so handlers must be idempotent.**

**A failed message is skipped, not redelivered.** Streams have no nack: on a
handler error (or a recovered panic) the failure is logged and counted, the
offset is not committed, and the next message is processed. The skip only sticks
once a later success commits a *higher* offset; restart before that and the
failed message comes back, along with everything after the last stored offset.
Anything that must not be lost belongs in the handler's own durable store.
Parking failed messages is future work
([ADR-059](adr_059_streams_consumption.md)).

**Handlers run inline and sequentially — there is no worker pool.** A stream is
an ordered log: parallel handlers would break that order and make a committed
offset claim that messages behind it were handled. This is the deliberate
opposite of the AMQP lane's `NumCPU*4` default. Parallel consumption comes from
super-stream partitions — one active consumer per partition, order preserved
within each — which is Phase 3, not from threads inside one process. SAC is not
a throughput lever; see below.

**There is no handler timeout.** Unlike an HTTP handler, a stream handler gets no
deadline: the context it receives is canceled only by `StopConsumers`, so a
handler that ignores it runs unbounded. Because delivery is sequential, one hung
handler stalls that consumer entirely and leaks its goroutine at exit — it does
not block shutdown, but nothing after it is consumed either. Respect `ctx` and
bound your own work (`context.WithTimeout` around the slow call).

## Single active consumer

`SAC: true` makes the broker deliver to exactly one member of the consumer-name
group at a time (RabbitMQ 3.11+). This is **failover, not parallelism**: the
other members are standbys promoted when the active one goes away, so more
members buy availability, not throughput. On promotion the framework re-resolves
the stored offset, so a takeover resumes where the previous active member
committed.

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
