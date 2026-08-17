# ADR-068: One delivery pipeline for both messaging lanes

- **Status**: Accepted
- **Date**: 2026-08-17
- **Related**: [ADR-059](adr_059_streams_consumption.md) (the streams lane's shape and its "trace-context propagation" future-work item), [ADR-063](adr_063_streams_native_publishing.md) (native publishing — untouched here), [ADR-032](adr_032_lease_refcount_tenant_handles.md) (the per-message lease scope), [ADR-058](adr_058_consumer_scoped_amqp_arguments.md) (the two-lane framing)

## Context

Both messaging lanes implemented the same per-message body independently. The
classic lane spread it across `processMessage`, `handlePanicRecovery`,
`buildFailureLogEvent`, `nackMessage` and an exported `StartConsumeSpan` in the
client file; the streams lane wrote its own in `runner.deliver`/`invoke`.

The copies drifted, in ways nobody chose:

- The classic lane counted `messaging.client.consumed.messages` at **receive**
  time, inside the span factory, with a hardcoded `nil` error — so the counter
  could never carry `error.type`, and a delivery whose handler never returned was
  counted as consumed. The streams lane counted at **completion**, with
  `error.type`. One metric, two meanings, depending on the lane.
- The streams lane extracted no trace context at all, though its own publisher
  injects one.
- The streams lane installed no per-message lease scope.
- Three separate issues (#940, #951, #954) rewrote `processMessage` inside a
  month, and none of them reached the streams copy.

## Decision

### The pipeline is a package, and it owns the per-message context

`messaging/internal/delivery` runs everything between "bytes arrived" and
"outcome recorded": trace extraction from the lane's *Carrier*, the
Consumer-kind span, `leasescope.Install`, `EnsureTraceID`, the handler
invocation, panic-to-error, one `tracking.RecordConsume` at completion, and a
call to the lane's own outcome-logging closure. Its entry point is
`Run(ctx, *Request) *Result` and its outcomes are `Succeeded`, `HandlerError`
and `Panicked`.

### Settlement stays with the lane

`Run` returns; it never touches the broker. The classic lane acks or nacks
without requeue on the returned `Result`; the streams lane commits an offset or
skips. "Never requeue" and ADR-059's "commit only after successful handling" are
lane policy and stay where they were. The `Result` carries the pipeline's
context-bound logger, so a lane's "Failed to ack" line still carries `trace_id`
and `span_id`.

### Each lane supplies its own attribute bundle; the pipeline owns when and once

A lane hands over its span extras, its `tracking.ConsumeAttributes`, its
destination and its log closure. The pipeline decides that they are emitted
exactly once, at completion. Every value the classic lane emitted before — span
name, kind and all eight attributes; four log lines with their exact fields,
including the two deliberate `correlation_id` stamps on a failure line; the OTel
RabbitMQ destination strings — is emitted unchanged.

### The consumed counter moves, and that is the only telemetry that does

`messaging.client.consumed.messages` is now incremented at completion on both
lanes, with `error.type` when the delivery failed. The count per delivery is
unchanged; what changes is *when* it lands and that failures are now separable
on the counter as well as on the histogram.

### `StartConsumeSpan` is removed, not deprecated

It was exported for consumers driving their own consume loop, and it conflated
span creation with metric recording. Both jobs now have exactly one home. The
framework does not ship compatibility shims (CLAUDE.md → Backward Compatibility),
so the export is deleted, with an ADR and a migration atom instead of a stub.

### What this deliberately does not change

- **OTel span parenting.** go-bricks trace extraction populates context *values*
  (`trace_id`, `traceparent`, `tracestate`); it does not run an OTel
  `TextMapPropagator`, so a consume span is a root span today and stays one.
  Making it a true child of the producer's span would re-parent every existing
  consumer span and change the trace ID of every AMQP consume trace in
  production. That is a separate decision, not a side effect of this one.
- **Concurrency.** The classic lane's worker pool and the streams lane's
  one-goroutine-per-partition shape live around the pipeline, not in it.
- **Publishing.** ADR-033's bounded publish and ADR-063's confirmed publish are
  untouched, and so is publisher-side trace injection.

## Consequences

- One body to fix: the next `processMessage`-class bug is fixed once and lands on
  both lanes.
- A consumer that called `StartConsumeSpan` no longer compiles. There is no
  replacement export: a service driving its own consume loop owns its own span.
- Dashboards that split `messaging.client.consumed.messages` by attribute see a
  new `error.type` dimension on the classic lane, and a delivery is counted when
  it finishes rather than when it arrives — a stuck handler no longer inflates
  the counter ahead of its outcome.
- The classic lane's ack and nack now run after the span closes and the lease
  scope drains, because settlement is lane-side. The ack/nack log lines are
  unchanged: they go through the logger the pipeline bound while the span was
  open.
- The streams lane runs on the pipeline in a follow-up PR, which is where its
  trace extraction, its lease scope and its panic wording change.
- A panic inside the pipeline's own outcome-logging call (`LogOutcome`/`Log`) is
  no longer recovered by `processMessage` after this change: that call now runs
  on the worker goroutine with no enclosing recover, so it terminates the
  process instead of failing one delivery. The trade-off is deliberate — closing
  over a per-delivery recover would mean capturing a closure per message, and
  the lane's outcome logging is framework code, not user handler code, so a
  panic there is a framework bug, not a data-dependent failure to contain.
- The pipeline extracts trace context with go-bricks' own header extraction and
  does not re-parent the consumer span under an upstream `traceparent`; this
  matches "What this deliberately does not change" above — span re-parenting is
  out of scope and would re-parent every existing consumer span, not just the
  one this PR touches.

## References

- `messaging/internal/delivery/delivery.go` — the pipeline
- `messaging/registry.go` — the classic lane's adapter and its settlement
- `messaging/internal/tracking/metrics.go` — `ConsumeAttributes` and `RecordConsume`
- [wiki/messaging.md](messaging.md#message-error-handling) — the consumer-facing behavior
- [wiki/migrations.md](migrations.md) `[C60.6]`
