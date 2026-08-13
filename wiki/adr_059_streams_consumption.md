# ADR-059: Native stream consumption commits offsets only after successful handling

- **Status**: Accepted
- **Date**: 2026-08-12
- **Related**: ADR-058 (the AMQP stream-queue lane and the two-lane framing), [ADR-040](adr_040_declaration_args_passthrough.md) (declaration args reach the broker), [ADR-045](adr_045_no_producer_side_manager_interfaces.md) (no exported manager interface), [ADR-041](adr_041_shared_ledger_tenancy.md) (single-tenant fail-fast precedent)

## Context

ADR-058 established the AMQP 0.9.1 lane:
a stream *queue* (`x-queue-type: stream`) consumed through the existing client
with an `x-stream-offset` start position. That lane reaches streams through the
port a service already speaks, and it is the right answer where 5552 is not
reachable or the deployment is multi-tenant.

What it structurally cannot do is what streams are actually for:

- **Server-side offset tracking.** AMQP 0.9.1 has no notion of a stored consumer
  position. The resume point is the client's problem, so a process restart
  re-attaches at the declared offset and replays.
- **Single active consumer.** Group coordination is a stream-protocol feature
  (RabbitMQ 3.11+).
- **Super streams.** Partition discovery and per-partition SAC exist only on the
  stream protocol; over AMQP a super stream is N unrelated queues.

Reaching those means speaking the native stream protocol, which means a new
dependency — the reason this decision is recorded separately from ADR-058.

## Decision

Add `messaging/streams`, built on the official
`github.com/rabbitmq/rabbitmq-stream-go-client` v1.8.3, for **consumption only**.

### Consume only; publishing stays out

The framework surface has no producer. Services already publish into streams
through the AMQP lane (a stream queue bound to an exchange), and adding a
producer would mean owning batching, deduplication, compression, and routing
strategies — a second, much larger design. When native publishing is added it
must reuse the Environment this manager owns, not open a second connection path.

### Offsets are committed only after successful handling

The client offers `SetAutoCommit`, which stores an offset every N messages or T
seconds as they are *delivered*. That is deliberately not used. A delivered
message is not a handled message, and a stored offset that outruns handling is
the one failure mode that cannot be recovered from: the messages between the
stored offset and the real progress point are simply never seen again.

Instead a per-consumer tracker records the last offset whose handler returned
`nil` and calls `StoreCustomOffset` when either
`messaging.streams.offsetstore.countbeforestorage` successes have accumulated or
`messaging.streams.offsetstore.flushinterval` has elapsed since the last commit.
There is no background goroutine: a stalled stream commits nothing, which is
correct because nothing new was handled. `StopConsumers` performs a **final
flush before closing each consumer**, which narrows the replay window without
closing it: nothing joins an in-flight `MessagesHandler` callback, so one that
finishes after that flush leaves its offset unstored and replays on restart.
Delivery is **at-least-once**, and handlers must be idempotent.

At startup — and at SAC promotion — the framework queries the broker for the
consumer name's stored offset and resumes at `stored + 1`; the declared `Start`
applies only when no offset exists. **A stored offset always wins**, which is
what makes restart behavior deterministic instead of dependent on how a module
happened to spell its start position.

### A failed message is skipped, not redelivered

Streams have no nack and no redelivery. On a handler error, or a recovered
panic, the failure is logged and counted, the offset is not committed, and
consumption continues. The skip becomes permanent only once a *later* success
stores a **higher** offset; until that commit lands, a restart resumes from the
last stored offset and replays the failed message along with everything after
it. This is inherent to the medium, and stating it plainly is better than a shim
that pretends otherwise. Parking failed messages (the dead-letter analog) is
named as future work.

### Handlers run inline, sequentially — no worker pool

The AMQP lane defaults to `runtime.NumCPU() * 4` workers per consumer. This lane
runs handlers inline on the client's callback. A stream is an ordered log: a
worker pool would break that order *and* make a committed offset lie, because
the offset only means "everything up to here was handled" when handling is
sequential. Parallelism therefore comes from super-stream partitions — each
partition gets its own active consumer, and order is preserved within one —
which is future work in this lane, not from threads inside a process. SAC is not
that lever: exactly one group member consumes, so it buys failover, not
throughput.

### Single-tenant only, enforced by config validation

`multitenant.enabled` together with `messaging.streams.uri` fails
`config.Validate` at startup. Multi-tenant support means one Environment per
tenant plus a per-tenant stream-URI leg on the resource source; until that
exists, failing loudly beats consuming one tenant's streams on behalf of all of
them. The error fragment `single-tenant only` is the greppable marker for that
future work.

The check is repeated at startup, in `app.prepareStreamConsumers`, because
`config.Validate` is skippable: `app.NewWithConfig` accepts a hand-built
`*config.Config` and never calls it — the same bypass `untypedConnectionStringPaths`
guards the database-type invariant against. Note also that `streams.NewManager` is
exported and carries no tenancy guard of its own; the invariant lives in the two
places above, not in the manager.

### The endpoint is configured, never inferred

`messaging.streams.uri` is not derived from `messaging.broker.url`. The stream
protocol is a separate listener on a separate port behind a plugin a deployment
may not have enabled; inferring it would be exactly the hidden default the
framework's Explicit > Implicit principle rejects. Declaring stream consumers
without configuring the URI aborts startup rather than dropping the
declarations, and so does a failure to start them.

### Shape follows the existing lanes

`Manager` is a plain struct consumed concretely by `app/` — no exported manager
interface ([ADR-045](adr_045_no_producer_side_manager_interfaces.md)) — and no
`ModuleDeps` field is added, because consumption needs no per-request accessor.
Metrics reuse the AMQP instruments rather than minting a second meter.

## Consequences

**Positive.** A restart resumes from the last stored offset, with no client-side
offset store to operate — at-least-once, so anything handled after that offset
is replayed and handlers must be idempotent. SAC gives failover without a second
coordination mechanism. Declarations, lifecycle, readiness, logging, and OTel
signals match the AMQP lane, so a stream consumer is not a foreign object in a
GoBricks service.

**Negative — a new dependency.** v1.8.3 drags in `golang/snappy`, `pierrec/lz4`,
`spaolacci/murmur3`, `pkg/errors`, and bumps `klauspost/compress`. Its `go.mod`
asks for `go.opentelemetry.io/otel` v1.44.0 against this repo's v1.45.0, so MVS
keeps v1.45.0 today — but Renovate will now track a module that churns OTel
versions, and the `go.work.sum` gate is where that lands.

**Negative — throughput per consumer is bounded by one handler.** A slow handler
is a slow stream. That is the price of ordered, truthful offsets, and the
mitigation is partitioning (future work), not threading — adding SAC members
adds standbys, not consumers.

**Negative — a failed message is lost to the consumer.** See above. Until
parking exists, a handler that must not lose a message has to persist it itself.

**Negative — the lane is invisible to multi-tenant deployments.** They keep the
AMQP lane, with its session-local resume.

**Neutral — the readiness probe is non-critical.** A reconnecting consumer
reports `not_ready` without failing `/ready`, because the reliable consumers
recover on their own and pulling the instance from the load balancer over a
broker flap would be worse than the flap.

## Future work

- Native publishing (must reuse this manager's Environment).
- Multi-tenant fan-out: per-tenant Environments plus a stream-URI leg on the
  resource source; remove the `single-tenant only` fail-fast then.
- Parking failed messages instead of skipping them.
- Trace-context propagation from AMQP-published stream messages (stream
  deliveries are AMQP 1.0; nothing is extracted today).
- RabbitMQ 3.13 stream filtering.
- A TLS configuration surface. `rabbitmq-stream+tls://` works today — the client
  builds a `tls.Config` with `MinVersion: TLS 1.2` and never sets
  `InsecureSkipVerify` — but `ManagerOptions` exposes no `tls.Config`, so a private
  CA or a client certificate has no seam. Until one exists, plaintext outside
  development only WARNs rather than failing closed, because failing would leave
  those deployments no working option.

## References

- [wiki/streams.md](streams.md) — consumer-facing guide
- [wiki/messaging.md](messaging.md#stream-queues-amqp-lane) — the AMQP lane
- `messaging/streams/runner.go` — the offset-commit policy
- `messaging/streams/manager.go` — environment, declaration replay, shutdown flush
- `app/streams_setup.go` — runtime probe and closer registration
- <https://www.rabbitmq.com/docs/streams>
