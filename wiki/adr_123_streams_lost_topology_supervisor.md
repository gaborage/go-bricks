# ADR-123: The Streams Lane Reports a Stream the Broker Lost

**Status:** Accepted
**Date:** 2026-09-27
**Issue:** #1797

## Context

The streams lane declares its topology once, in `Manager.Start`, and `Start` refuses to run again
while an environment is open. The client's HA layer (rabbitmq-stream-go-client v1.8.3, `pkg/ha`)
reattaches a consumer or producer after a broker outage by itself. It gives up when the stream is
gone: its retry asks for the stream's metadata, gets `StreamDoesNotExist`, and sets the handle to
`StatusClosed`, which is final. There is no further retry and no callback. A metadata query the
broker answers with an error the retry does not classify, such as access refused or an internal
error, ends the same way, although the stream still exists. A query that gets no answer, such as
one that times out, is retried instead.

So a stream deleted by an operator, or lost with broker data, stopped consumption and publishing for
the life of the process. The only traces were the vendor's unstructured `log.Printf` line and the
non-critical `streams` component turning unhealthy, which leaves `/ready` healthy overall. A replica
that restarted re-declared the stream but did not revive its peers, whose handles stayed closed.

Re-creating the stream is not free. A re-created stream starts empty and holds no stored offsets, so
`QueryOffset` answers `OffsetNotFoundError` and each consumer falls back to its declared `Start`.
What the lost stream held is gone. With the default `OffsetNext`, a consumer also skips whatever was
published between the re-creation and its reattach. [ADR-113](adr_113_amqp_topology_redeclare_on_reconnect.md)
repairs the AMQP lane's topology on reconnect without such a cost, which is why that lane repairs by
default and this one does not.

## Decision

- **The manager owns a supervisor.** One goroutine reads every tracked consumer's and publisher's
  status every 5s. The client has no close callback, so polling is the only signal. It starts at the
  end of a successful `Start` when a consumer or publisher came up, and it runs under the consumers'
  own context.
- **Loud.** A handle in `StatusClosed` that the manager did not close is reported once at ERROR,
  naming the stream and the consumer, or the stream a publisher targets. The line says the handle
  stays down until the service restarts. Nothing is re-declared.
- **The `streams` component stays unhealthy.** A consumer or publisher found lost is never ready
  again in that run, even when the client later reports a super-stream handle open because another
  of its partitions reconnected.
- **No offset book is touched.** The supervisor marks the consumer lost, and the shutdown flush
  skips a lost consumer, with a WARN naming it. A super stream's flush commits by name through the
  environment, and by then another replica may have re-created the stream under that name. A
  super-stream consumer whose client gave up on some partitions keeps delivering and committing on
  the others.
- **An orderly shutdown is never reported.** `stopLocked` cancels the supervisor's context and
  empties the manager's consumer and publisher lists under the manager lock, before it releases it,
  so a pass that runs afterwards finds nothing to report. A pass also checks that context under the
  same lock, which keeps a supervisor that a stop gave up waiting for off the handles of a later
  `Start`. `StopConsumers` and `Close` wait for the supervisor to exit, after releasing the lock,
  within what is left of the one flush budget the shutdown flush drew from, so a stop phase takes no
  longer than it did before the supervisor existed.

## Consequences

- A lost stream is a structured ERROR that an alert can match, not an unstructured vendor line.
- Nothing is re-declared, so the component stays unhealthy until a restart. A replica that restarts
  re-declares the stream without reviving its peers, so every replica that logged the ERROR needs
  one.
- The one other change is the skipped shutdown flush: what a lost consumer handled since its last
  commit replays after the restart, which at-least-once delivery already permits. For a super stream
  that covers every partition, including the ones that kept delivering.
- Readiness follows the supervisor as well as the client: a super stream found to have lost one
  partition keeps the component unhealthy while its other partitions deliver.
- For a plain stream, detection lags the client's own metadata retry (3–11s for a consumer,
  6–22s for a producer, which waits once before the retry and again inside it) by up to one 5s
  interval. A super stream takes longer. The client retries its lost partitions one after another
  in a single goroutine, 3–11s each for a consumer or a producer, so the handle settles on closed
  only after the last partition's retry fails, and the lag grows with the partition count. The
  cost is one goroutine and a status read per handle per interval.
- Detection needs the handle to stay closed until the next pass. A super-stream partition lost while
  the client still has another partition queued for retry leaves the handle closed only until that
  retry starts, so it can pass straight back to reconnecting and open, and the loss goes unreported.

## Alternatives considered

- **Re-create a lost stream by default.** Rejected: it silently discards offsets and can skip
  messages, and a deleted stream is sometimes deliberate.
- **Re-create a lost stream on opt-in.** Deferred to
  [#1826](https://github.com/gaborage/go-bricks/issues/1826). A draft failed review: the client's
  super-stream consumer `Close` is unguarded, so a second `Close` crashes, and closing a
  super-stream consumer while the client retries its partitions races the client.
- **Call `Start` again.** Rejected: `Start` refuses while an environment is open, and closing and
  redialing would drop every healthy handle to repair one stream.
- **Make the `streams` component critical on a loss.** Out of scope. The component stays
  non-critical so a broker flap does not pull the service out of rotation; operators alert on the
  ERROR.

Out of scope as well: changing a super stream's partition count, retention or argument drift, and
the classic AMQP lane.

## Migration

No configuration or API change, but the default behavior moves. A lost stream is now reported at
ERROR, and its component stays unhealthy until a restart, as before. A lost consumer's shutdown
flush is skipped, so the restart replays what it handled since its last commit. See
[C69.6](migrations.md).

## Related

- [ADR-059](adr_059_streams_consumption.md) — streams consumption and offsets
- [ADR-063](adr_063_streams_native_publishing.md) — streams publishing
- [ADR-113](adr_113_amqp_topology_redeclare_on_reconnect.md) — the AMQP lane's topology repair
- [streams.md](streams.md#a-lost-stream)
