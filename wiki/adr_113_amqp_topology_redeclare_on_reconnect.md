# ADR-113: AMQP Topology Is Re-declared Once per Channel Generation After a Reconnect

**Status:** Accepted
**Date:** 2026-09-14
**Issue:** #1618

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

- Before each re-subscribe attempt the registry asks the client for its channel generation through an
  unexported optional interface, `channelGeneration() (generation uint64, ready bool)`, which
  `AMQPClientImpl` implements. `AMQPClient` is unchanged: adding a method to it would break every
  external implementer. When the client is ready on a generation the topology has not been declared
  on, the registry records that generation and then re-runs its recorded declarations — exchanges,
  then queues, then bindings, the same order `DeclareInfrastructure` uses. Backoff attempts within
  the same generation declare nothing, and the pass is serialized across consumers, so a registry
  makes at most one pass per generation, whether or not that pass succeeds.
- `DeclareInfrastructure` records the generation it declares on, so a delivery channel closed without a
  new channel (a broker `basic.cancel`) re-subscribes without a pass.
- Only `*AMQPClientImpl`, the type `NewAMQPClient` returns, carries the accessor (a struct that embeds
  it inherits it). Any other `AMQPClient` — an external implementation, or a wrapper holding the
  client in a field — never re-declares, which is the behavior before this ADR. A custom
  `app.Options.MessagingClientFactory` therefore keeps the pass only when it returns
  `NewAMQPClient`'s client or a struct embedding it.
- The first failure ends the pass with one WARN naming the declaration and the channel generation,
  with the broker's reply code and text when the error is an `*amqp.Error`. A refused declare closes
  the channel, so the next generation retries. A failure once the consumer context is canceled ends
  the pass without a log.
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

**Positive:** a broker that lost its topology recovers without a process restart; a consumer that
cannot re-attach is visible at WARN with the broker's reason; a healthy reconnect costs one idempotent
declare pass.

**Negative:** an argument mismatch that appears at runtime is logged once and then stays skipped until
restart, by design. A pass that fails part-way leaves the rest of that generation undeclared until the
next channel.

**Neutral:** no configuration key and no exported API. `RegistryInterface`, `testing/mocks` and
`testing/fixtures` are unchanged.

## References

- #1618 — this decision; #480 — consumer auto-resubscribe
- `messaging/registry.go` — `redeclareTopology`, `resubscribe`; `messaging/amqp_client.go` — `channelGeneration`
- [ADR-058](adr_058_consumer_scoped_amqp_arguments.md), [ADR-059](adr_059_streams_consumption.md)
- [migrations.md](migrations.md) `[C65.9]`
