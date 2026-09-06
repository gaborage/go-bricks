# ADR-103: The Publish Bound Governs Every Wait It Can Reach

**Status:** Accepted
**Date:** 2026-09-06

## Context

`messaging.publishtimeout` derives an aggregate context deadline covering one whole
publish — the readiness pre-flight and the entire retry loop. The documented contract
has always been narrow on purpose: **the bound governs waiting, not an in-flight socket
write.** A deployment is told to size a hard end-to-end SLO with a transport-level
control, not with this key alone.

The client's publish serialization primitive, however, was a plain `sync.Mutex` held
across the blocking call into the driver. That put a second, undocumented category of
waiting outside the bound. `amqp091-go` v1.14.0 offers exactly one cancellation
checkpoint on the publish path — `Channel.PublishWithContext`
(`channel.go:1886-1893`) is, in substance:

```go
select {
case <-ctx.Done():
    return ctx.Err()
default:
    return ch.Publish(exchange, key, mandatory, immediate, msg)
}
```

One check, then a blocking write. A broker that stops reading parks that write for as
long as the socket buffers stay full. Holding a plain mutex across it meant every
publisher queued behind the stuck one also sat outside its own deadline, learning of
the expiry only after the write returned — measured at roughly 8x its configured
bound. That is not the residual the contract describes; it is waiting, and waiting is
what the bound is supposed to govern.

## Decision

The publish slot becomes a one-place channel acquired under the caller's context
(option B). A publisher that cannot take the slot before its deadline expires returns
immediately rather than blocking until the in-flight write completes.

- **Acquisition is context-aware** on the caller-facing publish path: a `select` over
  the slot channel and `ctx.Done()`.
- **The reconnect path keeps an unconditional acquire.** Reconnection is framework-
  internal housekeeping with no caller deadline to honor, and abandoning the slot
  there would leave the client's publish state half-rebuilt.
- **Error identity is unchanged.** The early exit reuses the existing publish abort
  path, which already wraps `ctx.Err()`. No new sentinel is introduced, and
  `errors.Is(err, context.DeadlineExceeded)` holds exactly as it did for an expiry
  observed at the readiness pre-flight or the confirmation wait.

The change is non-breaking: no exported signature, config key, or error value moves.

## Alternatives

**A — a transport write deadline via a custom dialer.** Bound the write itself by
supplying `amqp.Config.Dial` that sets a `SetWriteDeadline` on the connection.
Rejected on two counts. It bypasses `amqp091`'s `DefaultDial`, which installs
`net.DialTimeout` plus a handshake `SetDeadline` and then clears that deadline once
the handshake completes (`connection.go:201-212`, `:1343-1345`) — reimplementing it
means owning the handshake-timeout semantics forever. Worse, `amqp091` tears the whole
connection down on any write error, so a write deadline firing for one slow publish
would drop the consumers sharing that connection's channel. The blast radius is wrong
for the problem.

**C — record it as an out-of-scope entry.** Document the mutex as a second residual
alongside the socket write and move on. Rejected because B is cheap (a channel in
place of a mutex, on one code path) and non-breaking, so there is nothing to trade
against fixing it.

**D — abandon the blocking publish on a goroutine.** Run `PublishWithContext` on a
goroutine and return at `ctx.Done()`. Rejected for the AMQP leg: the goroutine still
holds the slot, so nothing is unblocked; it leaks until the write returns; and a write
that lands after the caller has already reported failure leaves the send ambiguous —
the relay retries the record with no way to know the original write succeeded, so the
duplicate is untracked. At-least-once permits the duplicate itself; what it does not
supply is an answer to whether one was created. GoBricks does use this shape where
the trade works out — `messaging/streams/publisher.go:284-303` and
`messaging/streams/manager.go:807-822`
both abandon a send whose only cancellation point is the caller's context — but both
resolve their waiter explicitly and neither pins a shared serialization slot.

## Consequences

- A queued publisher's late `context.DeadlineExceeded` becomes an early one **with the
  same identity**. Callers already handling the late form need no change.
- **No late success becomes a failure.** Only a publisher that had not yet acquired the
  slot when its deadline passed is released early; a publish already in the write is
  untouched, and one that acquires the slot proceeds exactly as before.
- **The in-flight socket write remains the documented residual.** One publish — the one
  inside `PublishWithContext` — can still run past the bound until the write returns.
  The guidance to size a hard end-to-end SLO with a transport-level control stands.
- The contract prose in `wiki/messaging.md`, `wiki/context_deadlines.md`,
  `config/types.go`, and `config.example.yaml` now states the split explicitly: waiting
  for the publish slot is bounded, the socket write is not.

## References

- Issue #1434 (the aggregate bound arrives ~8x late behind a stuck write)
- Issue #1250 (`messaging.publishtimeout`)
- [ADR-033](adr_033_outbox_retry_count_status_parking.md) — outbox retry accounting;
  option D would leave it unable to distinguish a sent record from an unsent one
- [ADR-088](adr_088_outbox_ordered_leader_relay.md) — the ordered relay whose batch a
  stuck publish stalls
- [ADR-063](adr_063_streams_native_publishing.md) — the streams publish path and its
  abandonable send
- [ADR-029](adr_029_graceful_shutdown_order.md) — deadline propagation on teardown
- `amqp091-go` v1.14.0 `channel.go:1886-1893`, `connection.go:201-212`, `:1343-1345`
