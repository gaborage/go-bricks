# ADR-063: Native stream publishing is synchronous and confirmed, correlated by message pointer

- **Status**: Accepted
- **Date**: 2026-08-15
- **Related**: [ADR-059](adr_059_streams_consumption.md) (the consume side, whose Environment this reuses), [ADR-058](adr_058_consumer_scoped_amqp_arguments.md) (the AMQP stream-queue lane), [ADR-045](adr_045_no_producer_side_manager_interfaces.md) (no exported manager interface), [ADR-033](adr_033_outbox_retry_count_status_parking.md) (bounded publish on the AMQP lane)

## Context

[ADR-059](adr_059_streams_consumption.md) added `messaging/streams` for **consumption only** and
named native publishing as future work with one hard constraint: *"it must reuse the Environment
this manager owns, not open a second connection path."* Until now a service that consumed a stream
natively still had to publish to it through the AMQP lane (an exchange bound to a stream queue) or
hand-roll a second client — and a super stream over AMQP is N unrelated queues with no partition
routing at all.

The client this lane already depends on (`rabbitmq-stream-go-client` v1.8.3) offers producers, but
its publish path has two properties that decide the whole design:

- **`Send` is asynchronous, and its error return proves nothing.** `checkWriteError`
  (`pkg/ha/reliable_common.go`) swallows every write error except `FrameTooLarge`, returning `nil`
  after a 500 ms sleep. A message is enqueued into client-side batching and confirmed later, out of
  band, on a callback.
- **`Send` can block without bound.** `isReadyToSend` does a bare `sync.Cond.Wait()` with no timeout
  while the producer's status is `StatusReconnecting`, so a broker outage can park a caller
  indefinitely.

## Decision

Add a publish surface to `messaging/streams`, on the Environment `Manager` already owns. Two declare
methods return an inert handle at declaration time — `DeclarePublisher` for a plain stream,
`DeclareSuperStreamPublisher` for a partitioned one — and `Manager.Start` binds each to a client
producer.

### The surface is synchronous and confirmed, and nothing else

`Publisher.Publish(ctx, *PublishMessage) error` rejects a malformed call outright — a nil message, a
routing key that does not match the target kind, a publisher not yet bound or already closed — and
otherwise blocks until one of four things happens: the broker confirms the message, the client's
`Send` fails synchronously, the caller's context expires, or the publisher closes. There is no async
callback surface and no fire-and-forget mode.

That follows directly from the second context point: since a `nil` from the client's `Send` is
compatible with a write that failed, the **confirmation is the only truth available**. A surface
that returned after `Send` would be reporting success it cannot observe. Making the confirmation the
return value puts the one fact the client can actually establish in the one place a caller cannot
ignore it.

A context expiry is **not** proof of failure: the send may still be in flight and may still land.
Delivery stays at-least-once, exactly as on the consume side, and consumers must be idempotent.

### Confirmations are correlated by message pointer, which requires `SubEntrySize = 1`

Each in-flight send registers a waiter keyed by the `message.StreamMessage` handed to `Send`.
`ConfirmationStatus.GetMessage()` returns that exact value (`producer.go:386` stores `ps.sourceMsg`),
so pointer identity is a valid correlation key and no framework-side sequence number, header or
publishing ID is needed.

This holds **only at the client's default `SubEntrySize` of 1** (`producer.go:219`). Sub-entry
aggregation would batch several messages behind one entry and break the one-to-one mapping, so on the
plain lane the production producer options are the client's defaults verbatim — no `Name`, no
`SubEntrySize`, no compression — and `messaging/streams/manager.go`'s `constructProducer` carries
that note at the call site.

The super lane reaches the same guarantee by a different route, which is worth stating because the
framework cannot state it directly: `SuperStreamProducerOptions` has no `SubEntrySize` field at all,
and each partition's producer is built inside the client by `SuperStreamProducer.ConnectPartition`,
which calls `NewProducerOptions()` itself (`super_stream_producer.go:267`). The default therefore
holds per partition — but it is the client's own construction holding it, not anything this framework
passes, so a change there would break the correlation with nothing on our side to notice. Either way
this is not an oversight to be tuned later: it is a precondition of the correlation, and changing it
means revisiting this decision.

The correlation is proven end to end against a real broker rather than argued
(`TestStreamsPublisherRoundTripIntegration`): if `GetMessage` ever stopped returning the message
passed to `Send`, no waiter would resolve and every publish in that test would fail on its deadline.

### The send runs on a goroutine the caller is willing to abandon

Because `isReadyToSend` can park forever, `Publish` dispatches `handle.Send` on its own goroutine and
selects between the waiter's channel and `ctx.Done()`. The caller's context is therefore the only
bound on **how long `Publish` waits** — the framework adds no timeout of its own and no config key
for one. It does not bound the send behind that wait: the goroutine runs on until the client's own
send path unblocks, whatever the caller's context did.

Abandoning that goroutine leaks it until the client's send path releases it: during a reconnect that
is as long as the reconnect lasts, but on the close path it can be permanent — see the close sweep
below for why the broadcast that would release it never arrives. This is the same trade
`Manager.flushOffsetsLocked` already documents and accepts: a leaked goroutine parked on a vendor
condition variable is cheaper than a handler thread that cannot be interrupted, and the alternative —
bounding a call that accepts neither context nor deadline — means reimplementing the client.

### A context expiry tombstones the correlation entry; it never removes it

An entry leaves the map on exactly one of four events: its confirmation arrives, its `Send` returned a
synchronous error (which the client never confirms), the close sweep resolves it, or — in the one race
the sweep cannot cover — the send sees `closed` at registration and resolves itself, which is what
stops an entry landing *after* the sweep from sitting outside every removal path. **A context expiry
does none of those.** It marks the waiter done, so the eventual confirmation resolves to a no-op, and
leaves the entry in place.

That asymmetry is load-bearing for super streams. `HashRoutingStrategy.Route` is the first statement
of the inner `SuperStreamProducer.Send`, but `isReadyToSend` parks the goroutine *before* it — so an
abandoned send can ask for its routing key long after the caller gave up. Removing the entry on
context expiry would answer `""`, hash it, and pile that message onto whichever partition the empty
string lands on. Keeping the entry keeps the caller's partition choice intact for a send that has not
routed yet.

### Known limitation: outstanding sends are unbounded during a reconnect

A context-abandoned send holds both its goroutine and its tombstoned entry until the publisher closes,
and **nothing caps how many of those can accumulate.** The client's `QueueSize` back-pressure does not:
`isReadyToSend` parks in the `ha` layer on a bare cond wait (`reliable_common.go:114`, the `Wait` at
`:126`) *before* `ReliableProducer.Send` delegates to the inner `stream.Producer` where that queue
lives (`ha_publisher.go:116-120`), so a parked send never occupies queue capacity and never exerts
back-pressure. The real bound is the caller's publish rate multiplied by the outage duration.

This is accepted for v1 rather than solved. The fix is an **outstanding-send limit** — a semaphore
admitting N in-flight sends and failing the rest fast with a dedicated sentinel — which needs both
that new error and a way to configure N, and v1 deliberately adds no configuration keys. It is listed
under future work below. Until it exists, a service that publishes at high rate on a path with no
deadline of its own is the shape to watch during a broker outage; the mitigations available today are
a per-publish deadline (which caps how long each send waits, not how many accumulate) and alerting on
the publish-error rate.

### Publishers bind before consumers start

`Manager.Start` binds every declared publisher after the stream declarations and **before** it starts
any consumer. A consumer handler may publish from its very first delivery, and an unbound publisher
rejects that with `ErrPublisherNotStarted`. Shutdown runs the same order in reverse: consumers stop
first, publishers close after them, because a handler may publish on its way out.

### Closing sweeps every outstanding waiter with `ErrPublisherClosed`

`Publisher` close resolves and removes every entry still in the map. This is mandatory, not
belt-and-braces. The client's `entityClosed` confirmations (`markUnsentAsUnconfirmed`,
`producer.go:374-395`) cover only messages that reached its internal queue; a send parked in
`isReadyToSend` was never enqueued and gets no
confirmation at all — and the super producer's normal-close path `break`s out of its event loop
*before* the reconnection broadcast (`ha_super_stream_publisher.go:91-94`), so that goroutine may stay
parked for good. Without the sweep, a `Publish` caller with no deadline of its own hangs across the
whole shutdown. Resolution is idempotent, so a late `entityClosed` for a swept message is a no-op.

### Super streams route by murmur3 hash of a caller-supplied key

`PublishMessage.RoutingKey` is **required non-empty** on a super-stream publisher and **must be
empty** on a plain one; both are rejected before the client is touched. An empty key on a super
stream is refused rather than defaulted precisely because hashing `""` is well-defined and silently
wrong — every message would land on one partition.

`stream.NewHashRoutingStrategy` is murmur3 with RabbitMQ's shared seed, so a partition assignment made
here matches what the Java, .NET and Python clients compute for the same key. That interoperability is
the reason it is the only strategy offered.

### Shape follows the consume side

Publishers reach the broker through the same `producerHandle` seam pattern the consumers use, so the
confirmation-correlation policy is testable without a broker; the vendor constructors are reached
through the manager's Environment port for the same reason (originally two factory fields, folded
into the port when it landed). No `ModuleDeps` field is added — a module holds the handle its own
`DeclareStreams` returned. Publishers count towards `Manager.Ready()` and `Stats()` alongside
consumers, on the same non-critical probe. Metrics and spans reuse the AMQP lane's instruments and the
`go-bricks/messaging` tracer, and trace context is injected through the framework's own `trace`
package rather than the OTel propagator, so both messaging lanes write the same header names.

Single-tenancy is inherited unchanged: this lane is still gated by `config.Validate` and
`app.assertStreamsSingleTenant`, and publishing adds no tenancy work of its own.

### Deferred, deliberately

- **Producer deduplication** (`ProducerOptions.Name` plus publishing IDs) — needs caller-persistent
  ID sequences and its own ADR.
- **Key routing** (`KeyRoutingStrategy`, which asks the broker to resolve a key to partitions) — hash
  routing is the cross-client default and the only one v1 offers.
- **Sub-entry batching and compression** — incompatible with pointer correlation as it stands.
- **Outbox relay to streams** — the transactional outbox stays on the AMQP lane.

## Consequences

**Positive.** A service that consumes a stream natively can now publish to it over the same
connection, with a confirmed result rather than a hope. Super-stream partitioning is reachable from
the framework surface for the first time, interoperably with the other RabbitMQ clients. No new
configuration keys — the client's defaults and the caller's context are the only settings in play.
That context bounds how long `Publish` makes its caller wait; it does not bound the send behind it,
which is the known limitation above.

**Negative — the wait is only as bounded as the caller's context.** A caller that passes
`context.Background()` to `Publish` can wait for as long as the broker takes. Only an HTTP handler
inherits a deadline for free — `server.timeout.middleware`, 5 s by default. Every other caller this
lane attracts carries none: a stream consumer handler runs under `consumeContext`
(`manager.go`'s `consumeContext`), which is `context.WithCancel(context.WithoutCancel(ctx))` and therefore has no
deadline at all, and scheduled jobs and relays are the same. A consumer republishing downstream is the
likeliest publisher on this lane and the one with nothing to inherit, so it must pass a deadline of
its own.

**Negative — the correlation is a vendor-internal guarantee.** It rests on `ConfirmationStatus`
returning the exact message pointer at `SubEntrySize = 1`. A client upgrade must re-verify that, and
the integration round trip is what fails loudly if it ever stops holding.

**Negative — abandoned sends leak a goroutine and hold a map entry, with no cap on how many.** Both
live until the publisher closes, and `QueueSize` does not bound them because a parked send never
reaches the queue — see the known limitation above. That is the accepted price of a vendor call that
cannot be interrupted, and an outstanding-send limit is the named follow-up.

**Neutral — one publisher per target per process.** A second declaration on the same stream panics at
startup, matching the consumer contract: it is a wiring mistake, not a fan-out.

## Future work

- An **outstanding-send limit**: a semaphore admitting N in-flight sends and failing the rest with a
  dedicated sentinel, so a broker outage cannot accumulate abandoned goroutines and tombstoned entries
  without bound. Deferred out of v1 because it needs a new error sentinel and a configuration key, and
  v1 adds none.
- Producer-side deduplication, key routing, sub-entry batching and compression (see above).
- Outbox integration, so transactionally recorded events can be relayed to a stream.
- Multi-tenant fan-out, which publishing inherits from ADR-059's `single-tenant only` fail-fast.
- Parking a message whose publish exhausted every retry, the publish-side twin of ADR-059's
  failed-message parking.

## References

- [wiki/streams.md](streams.md) — consumer-facing guide
- [ADR-059](adr_059_streams_consumption.md) — the consume side and the Environment this reuses
- `messaging/streams/publisher.go` — the correlation, tombstone lifecycle and close sweep
- `messaging/streams/manager.go` — producer construction, bind ordering and the routing extractor
- `messaging/streams/declarations.go` — the two declare methods and their target validation
- <https://www.rabbitmq.com/docs/streams#super-streams>
