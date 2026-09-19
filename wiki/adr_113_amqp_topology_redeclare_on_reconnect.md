# ADR-113: AMQP Topology Is Re-declared Once per Channel Generation After a Reconnect

**Status:** Accepted
**Date:** 2026-09-14
**Issue:** #1618

> **Amended (2026-09-19, #1761 — the client announces its channels, and the guard is per source):**
> the pass below had exactly one driver, the consumer supervisor's re-subscribe loop, so a registry
> holding declarations but no consumers never re-declared at all — a publisher-only service kept
> publishing into topology the broker had lost until someone restarted the process. A client now
> announces every channel it becomes ready on over a second unexported seam, and the registry
> observes its own client on that seam until StopConsumers ends repair, consumers or not, and again
> on the observer a later re-arm starts — and the manager attaches the same observer to every
> publisher client it pools under the registry's key, which is
> what makes the mechanism reach the connection that actually breaks: when an operator deletes an
> exchange under a LIVE connection, the channel that takes the 404 belongs to a pooled PUBLISHER,
> while the registry's own client sits idle and never rotates at all. Without that source every
> publish failed, the channel closed, the pending publish drained as a synthetic NACK, and the caller
> got `ErrPublishRetriesExhausted` for the rest of the process's life. The pass itself
> takes any source and DECLARES through the registry's own client — topology is broker-global, and a
> declare that sat on a publishing channel would hold up the traffic it is restoring — and its
> once-per-generation guard is keyed per `(source, generation)`, because sources number their
> channels independently — through an internal per-source token the ledger keys by pointer, never the
> client value, since a custom `MessagingClientFactory` may return a shape Go cannot hash. The
> `channelGeneration()` gate below is unchanged and deliberate: a client
> carrying neither seam is never a source. `AMQPClient`, the 406 skip, the WARN logging and the
> first-failure-ends-the-pass rule are untouched.

## Context

The registry declared its exchanges, queues and bindings once, in `DeclareInfrastructure`, behind a
latch that lasts for the process lifetime. The client's reconnect supervisor only reopens a channel and
enables publisher confirms, and the consumer supervisor added by #480 re-subscribes with identical
consume options but never re-declares. On a broker that lost topology — a node restarted without
durable definitions, a queue deleted by an operator or a policy — `ConsumeFromQueue` fails with a
channel-level `404 NOT_FOUND`, and the re-subscribe loop retried forever with full-jitter backoff,
logging every failure at Debug. A consumer that could never re-attach was indistinguishable in
production logs from one riding out a routine reconnect. Nothing in the messaging wiki, the package
rules or any ADR promised or forbade re-declaration.

## Options Considered

**A — a `messaging.reconnect.redeclare` knob.** Rejected: broker declares are idempotent for matching
arguments, so a healthy reconnect costs nothing, and a fix that is off by default leaves the bug in
place for every service that does not find the knob.

**B — re-declare before every re-subscribe attempt.** Rejected: against a hard-down broker it adds a
declare storm to the retry storm, and every refused declare tears the channel down again.

**C — re-declare once per channel generation.** Chosen.

**D — delete and recreate a queue whose arguments no longer match.** Rejected: it destroys messages.
The framework never deletes or recreates broker state.

## Decision

- On each new channel generation the registry re-runs its recorded declarations — exchanges, then
  queues, then bindings, the same order `DeclareInfrastructure` uses. Two drivers reach the pass, and
  neither is privileged: a client announces every generation that becomes ready over an unexported
  seam an observer subscribes to, and the consumer supervisor asks the registry's own client for its
  generation before each re-subscribe attempt, through an unexported optional interface,
  `channelGeneration() (generation uint64, ready bool)`, which `AMQPClientImpl` implements.
  `AMQPClient` is unchanged: adding a method to it would break every external implementer. The
  registry observes its own client until repair ends, whether or not it has consumers, and the
  observer stops on the client's own end as well as on `StopConsumers` — a failed `StartConsumers`
  closes the client and drops the registry without ever calling `StopConsumers`. A later
  `StartConsumers` starts it again: for a publisher-only registry that observer is the only driver it
  owns, so a stop/start cycle must not retire it for the process lifetime. The manager observes each
  pooled publisher on the same seam, stops that observer when the pool retires the client — LRU
  eviction, the idle sweep, `Close` — and drops the client's ledger entry as the observer exits, so
  the guard's per-source map does not grow one dead entry per eviction. A pooled publisher's
  observer runs on a background context rather than inheriting the one that created the client,
  which is the opposite of what the registry's own observer does. The difference is ownership: a
  registry's observer belongs to the startup that built it, while a pooled publisher belongs to no
  one request — it is created by whichever caller missed the pool and then serves every later
  borrower. Inheriting that caller's context would file every later repair under that one request's
  trace, and under `messaging.tenancy: shared` that is one tenant's context on a client every tenant
  publishes through. No trace beats the wrong trace. Equal for the guard
  is not equal in ordering, though: the inline pre-subscribe call is a BARRIER, taken under the same
  pass mutex as the pass, so a completed pass on the current generation happens-before the
  `ConsumeFromQueue` that follows it. The observer is eventual and orders nothing against a
  re-subscribe, so the inline call is not a redundant second driver and cannot be dropped as one.
- A source only says WHEN to declare. The pass always declares through the registry's OWN client:
  topology is broker-global, so repairing it over the registry's connection is correct, and it is the
  only connection the registry owns — a declare that sat on a publishing channel would hold up the
  traffic the repair exists to restore. The pass never runs inline in a publish, and nothing on the
  publish path waits on one.
- The recorded generation is per `(source, generation)`, not one per registry, and with the pass mutex
  it is the only arbiter: at most one pass per source channel, whichever driver arrives first, whether
  or not that pass succeeds. It has to be per source because sources number their channels
  independently — each starts at 1 — and a single counter would swallow a rotation only one of them
  saw. A source's FIRST sighting therefore declares rather than being adopted at whatever generation
  it reports: that channel is one this registry has not declared on, and the route has to exist
  before the first publish goes out on it. Backoff attempts within the same generation declare
  nothing, and the re-subscribe call is a no-op once its generation is recorded.
- The announcement is what covers a registry that has declarations but no consumers at all: the
  re-subscribe driver alone never reached it.
- `DeclareInfrastructure` is the latch as well as the first pass. It records, for the registry's own
  client, the generation it declared on, so a delivery channel closed without a new channel (a broker
  `basic.cancel`) re-subscribes without a pass; and a sighting that arrives before it declares
  nothing and records nothing, so the startup topology is never run off a background wake outside the
  error path that makes a failed startup fatal. It holds the pass mutex across its whole body,
  readiness wait included, which makes that first declare one of the passes rather than a race
  against one. Anything that wakes during startup queues behind the whole body — no source can
  today, since the only one is the registry's own observer and `startRedeclaring` does not create it
  until the declare has finished, so this is the cost of a second source rather than one paid here.
  That queue is bounded only loosely: the readiness wait is `readyTimeoutDuration` (30s,
  `messaging/constants.go`), NOT the 5s `reconnect.readytimeout`, which bounds the publish pre-flight
  and never reaches the registry — the two share the 100ms poll cadence, not the timeout. The
  declares that follow are amqp091 RPCs the context does not cancel on the wire, so the hold is that
  30s plus N uncancelable round-trips, under the manager-side soft `infraSetupTimeout` (45s).
- Only `*AMQPClientImpl`, the type `NewAMQPClient` returns, carries either seam (a struct that embeds
  it inherits them). Any other `AMQPClient` — an external implementation, or a wrapper holding the
  client in a field — never re-declares, on either driver, which is the behavior before this ADR. A
  custom `app.Options.MessagingClientFactory` therefore keeps the pass only when it returns
  `NewAMQPClient`'s client or a struct embedding it.
- The first failure ends the pass with one WARN naming the declaration and the channel generation —
  the DRIVING source's own counter, so two sources can both report generation 2 for different
  channels — with the broker's reply code and text when the error is an `*amqp.Error`. A refused declare closes
  the channel, so the next generation retries. A failure once the driving context is canceled ends
  the pass without a log; a pass announced by the registry's OWN client runs on a context detached
  from the setup budget that seeded it, so it keeps the trace and tenant values and carries no
  deadline. A pass announced by a pooled publisher runs on a background context instead, for the
  ownership reason given above: that client belongs to no one request.
- `StopConsumers` ends every driver: the observer stops, and the registry refuses any later pass. It
  has to refuse rather than only stop its own observer, because a source that outlives the
  registry's consumers has no other way to learn the registry is done. The refusal lasts for the
  stop, not for the registry's lifetime: `StartConsumers` re-arms it, so the consumer's
  pre-subscribe pass works across a stop/start as it did before the halt gate existed. The observer is
  restarted with it: `DeclareInfrastructure` starts the first one and a re-arm starts another on the
  reopened context, because for a publisher-only registry that observer is the only driver the
  registry owns. The re-arm never WAITS for the observer the halt ended — that one may be inside a
  pass holding the pass mutex — and it does not need to: the halted observer runs its loop against
  the context it captured at spawn, which the halt canceled, so its passes end on that check even
  though the re-arm has already replaced the registry's own context.
- `PRECONDITION_FAILED` (406) is the exception: a surviving entity whose arguments differ from the
  declaration. The triage brief asked for "WARN and the consume proceeds", but amqp091 closes the
  channel on a 406, so the consume on that incarnation cannot proceed, and re-declaring on every later
  generation would loop forever, tearing the channel down each time while the surviving queue sat
  unconsumed. The declaration is instead remembered for the registry's lifetime and skipped by every
  later pass, with one WARN telling the operator to fix the server-side definition and restart the
  process. A restart is the only thing that clears the skip; nothing retries a 406 in-process. The
  consumer attaches to the surviving entity on the next channel.
- The re-subscribe loop logs its first four consecutive failures at Debug and every failure from
  the fifth at WARN (`consumerResubscribeWarnFromAttempt`), with `amqp_reply_code` and
  `amqp_reply_text` when the error is an `*amqp.Error`. A success still logs at Info with the
  attempt count.
- Stream consumers keep their resume offset (ADR-058). The native streams lane (ADR-059) has its own
  reconnect semantics and is untouched.

## Consequences

**Positive:** a broker that lost its topology recovers without a process restart, whether or not the
service consumes; a consumer that cannot re-attach is visible at WARN with the broker's reason; a
healthy reconnect costs one idempotent declare pass.

**Negative:** an argument mismatch that appears at runtime is logged once and then stays skipped until
restart, by design. A pass that fails part-way leaves the rest of that generation undeclared until the
next channel. The background goroutines are one per registry plus one per pooled publisher client,
each for that client's pooled life. The repair is priced per SOURCE, not per registry: a broker
restart that rotates every channel costs one full declare pass for each of the T registries and each
of their M pooled publishers, and those passes serialize behind one mutex per registry. A newly
pooled publisher pays one pass on its first channel too, on creation and on every re-creation after
an eviction or the idle sweep. Declares are idempotent for matching arguments, so a pass that finds
nothing lost is a no-op at the broker — it is round-trips, not damage. One waking during startup
blocks until `DeclareInfrastructure` returns — 30s of readiness wait plus uncancelable declare
round-trips, at worst.

**Neutral:** no configuration key and no exported API. `RegistryInterface`, `testing/mocks` and
`testing/fixtures` are unchanged.

## References

- #1618 — this decision; #1761 — the client announces its channels; #480 — consumer auto-resubscribe
- `messaging/registry.go` — `redeclareTopologyFrom`, `observeChannelReady`, `resubscribe`;
  `messaging/amqp_client.go` — `channelGeneration`, `channelReadyNotify`
- [ADR-058](adr_058_consumer_scoped_amqp_arguments.md), [ADR-059](adr_059_streams_consumption.md)
- [migrations.md](migrations.md) `[C65.10]`
