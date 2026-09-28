# ADR-122: A Returned Mandatory Publish Fails Instead of Reporting Success

**Status:** Accepted
**Date:** 2026-09-27
**Issue:** #1794

## Context

`Mandatory: true` on a publisher asks the broker to refuse a message that no queue is bound to
receive. The broker does refuse it. It answers the publish with `basic.return` and then, because
it has finished handling the message, with `basic.ack`. The AMQP client registered only
`NotifyPublish`, and nothing called `NotifyReturn`. amqp091 discards a return that has no listener,
so the ack reached the publisher and the publish reported success. A caller that set `Mandatory`
to learn about a missing binding was told the opposite. The fake channel could not emit a return,
so no test had ever exercised this path.

Two comments made the gap harder to see. They documented the NACK backoff as spacing retries of a
"transiently-unroutable" publish. The broker never NACKs an unroutable publish: it returns the
publish and then ACKs it.

A missing exchange is a different failure. The broker closes the channel with a 404, and the
reconnect fails the in-flight publish with a synthetic NACK. It never sends a return. This ADR
covers only returns.

## Decision

- **Listen on every generation.** `changeChannel` registers a buffered return listener next to
  the confirm listener on every new channel, and passes both to that generation's dispatcher.
- **Drain before routing.** amqp091's reader hands a publish's return to its listener before it
  hands over the ack for the same publish. The dispatcher records returns as they arrive. Before
  it routes each ack, it also drains every buffered return without blocking, so a publish's
  return has always been recorded by the time its ack is routed.
- **Correlate by message id.** A return carries no delivery tag. A Mandatory publish's pending
  entry, keyed by `(generation, deliveryTag)`, also carries its `message_id` and is indexed by
  `(generation, message_id)`, so a return finds its publish in one lookup. The return is recorded
  on that entry, and the ack for the entry's tag hands it to the publisher. Every path that stops
  waiting on a publish drops the entry and its index key together. The index key carries the
  generation, so a late return from a torn-down generation cannot reach a publish on a newer one.
  A return that matches nothing belongs to a publish that has already stopped waiting, and it is
  dropped.
- **Retry, then fail as unroutable.** A returned publish is retried within the existing attempt
  limit, using the NACK arm's 100ms backoff. The redeclare pass on a new channel (ADR-113) runs
  asynchronously, so the retry rides it out only when it finishes within the retry budget, about
  `(maxpublishattempts − 1) × (round trip + 100ms)`, roughly 0.4s at the defaults. A longer pass,
  such as many registries times pooled publishers re-declaring together (C67.2), still fails the
  publish with `ErrPublishUnroutable`; an operator who needs a longer window raises
  `messaging.reconnect.maxpublishattempts`. An attempt the broker returned was never queued, so
  resending it cannot create a duplicate as long as the return is attributed to that attempt. A late
  return is the exception (see Consequences). When the attempts run out, the publish fails with
  `ErrPublishRetriesExhausted` wrapping the new exported sentinel `ErrPublishUnroutable`. As with
  every other cause, a deadline or shutdown during the retries wraps it as the last cause.
- **Vocabulary.** The retry counter records `retry.reason = returned`, and the span event records
  `reason = message returned` and the returned attempt's delivery tag. The WARN logs the delivery
  tag, the reply code and text, the exchange, the routing key and the message id. The client never
  keeps the returned body or headers. The broker's reply text stays off the span (ADR-083).
- **The last attempt is not a retry.** For every cause, the attempt that reaches the limit writes
  no `retrying...` WARN, counts no retry and adds no retry span event; it ends in the terminal
  error. A publish that exhausts `maxpublishattempts` attempts logs `maxpublishattempts − 1`
  retry WARNs. Before this change the shared retry tail recorded the last attempt too, for NACKs,
  timeouts and publish errors alike. The last attempt logs one terminal WARN instead,
  `Publish failed after its last attempt, giving up`, with its cause, the attempt count and the
  details a retry WARN would carry (the delivery tag, and for a return the reply code and text,
  exchange, routing key and message id, never the body), so even `maxpublishattempts: 1` leaves a
  diagnostic. It also adds one `amqp.publish.exhausted` span event with the retry event's
  attributes (the reason, the cause's type, the delivery tag) and the attempt count, because the
  terminal span status records only the type of the `ErrPublishRetriesExhausted` wrapper.
- **A NACK takes precedence over a return** for the same tag. The defect is an ACK that followed a
  return, and the broker never pairs a return with a NACK.
- **A channel that drops a returned publish still reports it unroutable.** amqp091 closes a
  channel's return listener before its confirm listener, so when the dispatcher sees the confirm
  listener closed, every return the channel delivered is buffered; the dispatcher records them all
  before it exits. The reconnect drain answers every publish of the torn-down channel with a
  synthetic NACK, except one whose return was recorded: that one is answered as the broker would
  have, the return and then an ACK, so it retries and fails as unroutable, not NACKed. The drain
  does not wait for the old dispatcher to exit. It runs once the reconnect has opened a new channel
  and put it in confirm mode, at least two broker round trips after the old channel closed, so a
  return the dispatcher has not recorded by then is answered with the NACK.
- **Non-mandatory publishes are unchanged.** Their entries carry no message id, so no return can
  match them. The broker drops an unroutable non-mandatory publish and ACKs it, and it succeeds as
  before.

## Alternatives considered

**Keep reporting success and log the return.** Rejected: a caller who set `Mandatory` asked to be
told about the failure. A log line that no code branches on repeats the defect.

**Fail on the first return, with no retry.** Rejected: after a reconnect, a publish can land while
the new channel is still redeclaring the binding. That publish is returned for a binding that is
about to exist, and a retry 100ms later can turn it into a success when the redeclare finishes in
time. The attempt limit still bounds a real misconfiguration.

**Correlate through a per-attempt header.** Rejected: every attempt resends one prepared frame, so
that a retry is recognizably the same publish (#1546). A per-attempt marker would also reach
consumers.

**A ledger of returns keyed by message id, consulted when the ack arrives.** Rejected: a return
whose publisher has already stopped waiting leaves an entry that no ack will ever clear. The index
this ADR keeps holds pending publishes instead, and each publish removes its own entry when it stops
waiting.

**Make the outbox relay publish as Mandatory.** Out of scope, tracked as #1819. The relay never
sets `Mandatory`, so this change does not affect it.

## Consequences

- **Breaking for `Mandatory: true` callers.** A publish the broker returns used to return nil. It
  now fails after `reconnect.maxpublishattempts` attempts, n in all, which take
  `n × round trip + (n − 1) × 100ms`: every attempt costs a round trip, and every attempt but the
  last waits the backoff. Code that set `Mandatory` but relied on the nil sees `ErrPublishUnroutable`, and
  so does code whose binding was missing without anyone noticing.
- **Correlation assumes unique message ids among in-flight publishes.** The framework mints a UUID
  for every publish made through an exported door. Only a door that supplies its own id can put two
  in-flight publishes under one id. Today that is the outbox relay, which passes its row id and
  never publishes `Mandatory` (#1819). If such a door published `Mandatory`, `trackPending` would
  overwrite the earlier publish's index entry with the later one's, so the earlier publish could no
  longer be found. Its return would mark the later publish instead, and its ack would read as a
  success. The later publish would spend an extra attempt even if it routed, or fail with
  `ErrPublishUnroutable` if that was its last attempt.
- **A late return can cost one extra attempt, or fail the last one.** Suppose an attempt times out waiting for its
  confirmation (30s by default) and its return arrives afterwards. That return can mark the next
  attempt of the same publish. If that attempt routed, it is sent once more, which stays within
  at-least-once delivery, as a confirmation timeout already allows. If it was the last attempt, the
  publish fails with `ErrPublishUnroutable` even though the message was queued.
- **A dropped return is still a false success.** amqp091 abandons a return that its listener has
  not taken within 5s. The listener is buffered like the confirm listener (256) and the dispatcher
  takes each return as it arrives, so amqp091 drops a return only while the buffer stays full for
  5s. A channel teardown loses none: the dispatcher records the returns still buffered before it
  exits, and a return it records only after the reconnect drain answered its publish leaves that
  attempt NACKed, never reported as routed.
- **No new configuration.** The attempt limit and the backoff are the existing ones.

## References

- [ADR-033](adr_033_outbox_retry_count_status_parking.md): the bounded retry loop and its cause sentinels
- [ADR-083](adr_083_span_sinks_record_errors_by_type.md): span sinks record errors by type
- [ADR-105](adr_105_framework_writes_every_publish_property.md): the message id minted once per publish
- [ADR-113](adr_113_amqp_topology_redeclare_on_reconnect.md): the redeclare pass the retry rides out when it finishes within the retry budget
- [wiki/messaging.md](messaging.md#bounded-publish-retries-reconnectmaxpublishattempts): the cause sentinels
- `messaging/publish_return.go` (`trackPending`, `untrackPending`, `recordReturn`,
  `drainReturns`),
  `messaging/amqp_client.go` (`ErrPublishUnroutable`, `changeChannel`,
  `drainPendingPublishesWithNack`, `dispatchConfirms`, `routeConfirm`, `armConfirmation`,
  `publishRetryEpilogue`, `attemptSpanAttributes`, `logRetry`, `logExhausted`, `withAttempt`),
  `messaging/amqp_adapters.go` (`amqpChannel.NotifyReturn`)
