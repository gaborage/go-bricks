# ADR-033: Bounded Publish Retries + Status-Driven Outbox Dead-Lettering

**Status:** Accepted
**Date:** 2026-06-30

> **Amended (2026-08-30, a second lane and a parked outcome):** this ADR classified the
> outcomes of ONE lane. The relay now dispatches by the row's `lane`, and the stream lane
> (ADR-088) answers to the same test rather than a new one: `streams.ErrPublisherClosed` is
> shutdown and marks nothing, a publisher not yet started and a broker that will not confirm
> are CONNECTIVITY and retry, and three message-intrinsic conditions are POISON — a lane this
> build does not recognise, a stream no longer listed in `outbox.superstreams`, and a row with
> no partition key. All three are config drift on a row already written, so they read the same
> way every cycle, which is this ADR's own definition of poison. A fifth outcome joins the four: a row can now be PARKED. When an earlier row of the same
> ordering key failed in this cycle, the row is neither published nor marked and its
> `retry_count` does NOT advance — it simply waits for the next cycle in sequence order. Only
> `outcomeFailed` parks a key; a dead-lettered row is terminal and a delivered-but-unrecorded
> row was delivered, so neither holds anything behind it. A dashboard reading `retry_count` as
> liveness should know that a parked row's count is deliberately still.
>
> **Amended (2026-08-29, a second poison class):** this ADR classified every broker-side
> publish failure as connectivity, leaving undecodable headers as its single poison class. That was
> written before `messaging.ErrInvalidPublishDestination` existed (ADR-070's 2026-08-28
> amendment, `[C61.17]`): a destination past the AMQP shortstr limit is refused BEFORE any
> channel work, identically on every cycle, whatever the broker's state — deterministic,
> broker-independent, and therefore poison by this ADR's own test. Under the old
> classification such a row advanced `retry_count` and stayed `pending` for the life of the
> table. A publish error satisfying `errors.Is(err, messaging.ErrInvalidPublishDestination)`
> now takes the same path as an undecodable header: parked via `MarkDeadLettered` at
> `MaxRetries`, never first-hit. Connectivity is unchanged — NACK, confirmation timeout,
> not-connected, deadline and `ErrPublishRetriesExhausted` still retry forever.
>
> The bound also moved to the source, so a row that can never be delivered is normally
> refused before it is written: `messaging` exports the rule its publish doors already run
> (`ValidatePublishDestination`), `outbox.Publish` runs it on the post-fallback exchange and
> routing key and the caller's header keys before the INSERT, and the outbox module's `Init`
> refuses an over-long `outbox.defaultexchange`. Parking is the backstop for a row that
> reached the ledger another way — a hand-managed schema, a direct INSERT (#1229,
> `[C61.19]`).

## Context

The transactional outbox relay republishes pending events and increments a per-row
`retry_count` on each failed delivery (`outbox.MarkFailed`). Operators reported that under
negative conditions (RabbitMQ unavailable, a missing exchange, and similar) the relay logged
that it was "retrying" yet `retry_count` never moved.

Two independent defects produced that symptom:

1. **Unbounded messaging retry loop.** `AMQPClientImpl.PublishToExchange` retried a failing
   publish in an unbounded `for {}` whose local `retryCount` was only logged, never a
   loop-exit ceiling. The loop returned only on context cancellation, client shutdown, a
   positive ACK, or a not-ready snapshot. On a persistent failure (exchange-not-found →
   confirmation timeout; broker flap → publish error) it spun ~indefinitely — and the relay
   calls it with a deadline-free context, so the relay never regained control to run
   `MarkFailed`. The "…retrying…" the operator saw was this in-memory loop; the durable
   `retry_count` was frozen behind it. The NACK branch additionally `continue`d with zero
   delay, busy-spinning a core.

2. **Relay readiness early-return.** When the broker was fully down the relay returned before
   fetching/marking anything (`if !IsReady { return }`), so a clean outage froze `retry_count`
   for the whole batch.

A naive fix ("bound the loop and mark failed") risked three regressions surfaced by an
adversarial design review: counting `context.Canceled`/shutdown as real attempts (inflating
`retry_count` on every deploy); a NACK that rode into the per-record deadline returning a bare
`DeadlineExceeded` and so never being parked; and — because the original fetch gate was
`retry_count < maxretries` — a prolonged outage burning through `MaxRetries` and permanently
parking otherwise-healthy events.

## Decision

Fix both layers and **decouple the counter from the give-up gate**.

**Messaging (breaking).** `PublishToExchange` is now bounded by
`messaging.reconnect.maxpublishattempts` (default 5; `0` internally means unbounded so
struct-literal test clients keep their behavior). On exhaustion it returns
`ErrPublishRetriesExhausted` **wrapping the last cause** — one of the new exported sentinels
`ErrPublishNacked` / `ErrPublishConfirmTimeout`, or the raw publish error. Cancel / shutdown /
deadline returns also wrap the last cause, so `errors.Is` still matches both the primary error
and *why* the publish was failing. The NACK branch gained a small cancelable backoff
(`nackBackoff`, 100ms) replacing the zero-delay hot-spin.

**Outbox relay & store.** The readiness early-return is removed: a not-ready / unresolved
broker takes an *outage fast-path* that advances every pending record's `retry_count` (so the
count climbs during an outage — the operator's missing signal) and then **returns a job error**
so the failure stays visible at the scheduler level (an idle relay with nothing pending is not
an error). Each publish runs under a per-record `outbox.publishtimeout` (default 60s) that
**must be ≥ the confirmation wait** — the module fails to start otherwise, because a shorter
value truncates every legitimate confirmation into a connectivity failure (an unbounded
duplicate-delivery loop). Failures are **classified**: only **undecodable headers** are *poison*
(a deterministic, broker-independent failure); everything broker-side — broker down, **NACK**,
confirmation timeout, deadline — is *connectivity*. A RabbitMQ `basic.nack` is a transient broker
condition (disk alarm, mirror resync, failover), and a missing exchange surfaces *as* a synthesized
NACK, so neither is treated as bad content. `FetchPending` no longer filters on `retry_count` (it
is purely status-gated); parking happens only via a new `MarkDeadLettered` (`status = 'failed'`)
and **only for poison at `MaxRetries`**. Connectivity failures never park, so neither a prolonged
outage nor a transient broker fault can exhaust a healthy event. A shutdown/cancel during a publish
returns an "aborted" outcome that does **not** advance `retry_count` and stops the batch cleanly.

No schema migration is required: the `status` column and the `'failed'` value already exist.

## Consequences

- **`MaxRetries` now bounds poison only** (undecodable headers, and — since the 2026-08-29
  amendment — an unpublishable destination), not connectivity. A permanently-
  misnamed exchange or a persistently-NACKing event keeps retrying with a climbing `retry_count`
  rather than parking — so it delivers the moment the broker recovers or the config is fixed, at
  the cost of retrying indefinitely until then (monitor `retry_count` growth to catch it). This is
  a deliberate at-least-once trade-off: the previous `retry_count < maxretries` fetch gate was a
  universal backstop that also (silently) abandoned events; removing it never drops a deliverable
  event, but a genuinely-undeliverable connectivity event now needs operator attention.
- **Observability:** a relay cycle that has pending work but cannot reach the broker returns a
  job error (after advancing every record's `retry_count`), so operators keep the scheduler-level
  failure signal *and* the per-record `retry_count` growth signal. An idle relay (nothing pending)
  returns success even if the broker is down.
- **Breaking for direct `PublishToExchange` / `Publish` callers:** a persistently-failing publish
  now returns `ErrPublishRetriesExhausted` instead of blocking forever. The durable at-least-once
  path (the outbox) re-fetches next cycle, so this loses no outbox messages; best-effort direct
  publishers get an error they can handle. The `Store` interface changed (`FetchPending` drops its
  `maxRetries` parameter and gains `MarkDeadLettered`) — breaking for custom `Store` implementers;
  `outbox/testing.MockOutbox` implements `OutboxPublisher`, not `Store`, and is unaffected.
- **Accumulation:** `DeletePublished` only purges `published` rows, so `failed` rows accumulate —
  operators should monitor and prune them (never auto-deleted, so they stay visible).
- **Deferred (tracked follow-up):** bounding the `getMessaging` resolver call, a cycle budget /
  `retry_count`-ordered fetch for batch fairness, per-event "stuck" escalation metrics,
  `FOR UPDATE SKIP LOCKED` for multi-instance relays, and a `deadletteronconnectivity` opt-in.
