# ADR-069: The delivery pipeline owns settlement timing, not the lanes

- **Status**: Accepted
- **Date**: 2026-08-18
- **Supersedes in part**: [ADR-068](adr_068_delivery_pipeline.md) — it keeps that ADR's "settlement policy stays lane-side" decision and reverses only its containment half, which left each lane to guard its own delivery tail.
- **Related**: [ADR-032](adr_032_lease_refcount_tenant_handles.md) (the per-message lease scope whose drain must precede settlement), [ADR-059](adr_059_streams_consumption.md) ("commit only after success", untouched)

## Context

ADR-068 moved the per-message body into `messaging/internal/delivery` but left
containment with the lanes: each was expected to guard the tail of its own
delivery — the outcome line, the telemetry, the settlement — against a panic
that would otherwise kill the consume loop with the message unsettled.

That containment drifted immediately, and in the way shared-nothing guards
always do. The classic lane grew a `settling` flag plus a deferred recover with
a nested recover inside it. The streams lane grew nothing: an unrecovered panic
in its delivery tail terminated the process, taking the AMQP lane, HTTP, the
scheduler and the outbox with it, and stranding up to 500 handled-but-uncommitted
messages per partition. ADR-068's own Consequences text asserted the classic
lane's `LogOutcome` panic was no longer recovered; the code recovered it, and a
test pinned that it did. The text and the recover landed in the same commit.

One lane guarded, one did not, and the ADR described neither accurately. That is
the signature of a guarantee stated in prose rather than owned by a structure.

## Decision

`delivery.Request` gains `Settle func(*Result)`, and `Run` owns when it is
called. The guard is deferred FIRST so it runs LAST:

    defer func() {                                  // runs last
        if recovered := recover(); recovered != nil {
            res = panickedResult(res, recovered, start)
        }
        settleOnce(req, res)
    }()
    defer scope.ReleaseAll()
    defer span.End()

Three consequences follow from that ordering, and they are the decision:

**Settlement happens after the span closed and the lease drained.** A handle the
handler borrowed via `deps.DB`/`Cache`/`Messaging` is released BEFORE the
acknowledgement, so a message is never acked while work it started is still
holding a resource.

**A panic anywhere in the delivery tail still settles.** The lane's outcome line,
the span marking and the consume record all sit inside the guard. A panic there
produces a `Panicked` result — even when the handler itself succeeded — so the
lane nacks rather than acks. A delivery the lane could not finish reporting is
not one it should acknowledge.

**A panic inside `Settle` is logged and stopped, not retried.** It is the lane's
own bug on its own broker call; retrying it would panic again. There is no
`SettleFallback`: the fallback a lane would have written is what `Settle` already
does with a `Panicked` result.

**The guarantee is at-most-once INVOCATION of the callback, not exactly-once
completion at the broker.** `Settle` is called once and never a second time, but
what happens inside it is the lane's: a broker call that fails, times out, or
panics part-way leaves the message in whatever state the broker decided. The
pipeline guarantees the lane gets exactly one attempt with a decided result, and
nothing about the attempt's outcome. "Delivery tail" throughout this ADR means
everything from the handler's return to the end of `RecordConsume` — it does NOT
include `Settle`, which runs outside the guard and carries its own nested
recover.

Settlement POLICY stays lane-side, exactly as ADR-068 decided — ack versus
commit, nack-without-requeue versus skip, and the AutoAck no-op are the lane's.
Only the timing and the at-most-once invocation guarantee move.

## Consequences

`messaging/registry.go`'s `processMessage` deletes its `settling` flag, its
deferred recover and its nested recover, and supplies a `Settle` closure. The
classic lane's SETTLEMENT OUTCOME is unchanged — a tail panic still nacks without
requeue, still on one attempt — but its timing is not: the acknowledgement now
happens after `span.End()` and `scope.ReleaseAll()` rather than before them. No
broader compatibility is claimed. The test that pinned that behaviour moves
into the pipeline suite, because the pipeline now owns what it pins.

The streams lane gains the guarantee it never had, without writing a guard, when
it moves onto the pipeline.

**Enforcement is a standing requirement, not a convention.** A lane driving
`delivery.Run` joins the contract harness in `messaging/internal/lanecontract`,
and any divergence between lanes is legitimate only as a typed field on its
`Lane` struct. The harness ships a deliberately non-conforming lane that every
assertion family must fail against, so a family that stops biting is caught by
its own suite rather than by the next incident.

## Alternatives considered

**A shared `containDeliveryTail` helper each lane calls.** Rejected: it is the
same guarantee-by-convention that drifted, one call site away. A lane that
forgets to call it is a lane with no guard, and nothing fails.

**`Settle` plus a `SettleFallback` for the panic path.** Rejected: a body panic
is recovered and `Settle` runs normally with a `Panicked` result, which is the
same action a fallback would take. Only a panic INSIDE `Settle` needs different
handling, and log-and-stop is not a fallback.

**Settling inside the span, before the lease drains.** Rejected: it acknowledges
a message while a handle its handler borrowed is still open, which is the failure
ADR-032 exists to prevent.
