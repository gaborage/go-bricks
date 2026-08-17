# Streams environment port and one delivery pipeline — design

**Date:** 2026-08-16
**Status:** Accepted (grilling session, architecture review cards 6 and 8)
**Vocabulary:** [CONTEXT.md](../../../CONTEXT.md) — *Environment port*,
*Delivery pipeline*, *Carrier*, *Settlement*; design terms per
`/codebase-design`.

## Problem

- `messaging/streams/manager.go` threads the concrete `*stream.Environment`
  through ten methods; seams exist only downstream of it (`consumerHandle`,
  `offsetStorer`, `producerHandle`, `producerFactory`), so `Start` never reaches
  `declareStreams` in any unit test, `startStreamConsumer`,
  `startSuperStreamConsumer`, `resolveOffset`, `trackConsumer`, `newRunner` have
  zero unit hits, `messagesHandler` (the vendor callback) has zero test hits,
  tests fabricate the state `Start` should produce (`attach`,
  `attachPartitioned`, `rebindPublisher`) and one asserts by panicking on a nil
  environment.
- Both lanes implement span → invoke → recover → record → settle
  independently: the AMQP copy across seven functions in `registry.go` plus an
  exported `StartConsumeSpan` in the client file; the streams copy in
  `runner.deliver`/`invoke`. The copies drifted: streams never extracts the
  trace context its own publisher injects (`runner.go:240` starts from
  `r.baseCtx`; `publisher.go:338-340` claims symmetry); streams installs no
  per-message lease scope (`leasescope.Register` then releases immediately);
  AMQP counts `messaging.client.consumed.messages` at receive with no
  `error.type`, streams at completion with it; #940/#951/#954 rewrote
  `processMessage` three times in a month without reaching the streams copy.

## Decisions

### Environment port (card 8) — Stack B PR1, no ADR

1. **Seam shape mirrors `amqp_adapters.go`:** unexported `environment`
   interface + unexported `dialEnvironment func(*stream.EnvironmentOptions)
   (environment, error)` field on `Manager`, defaulting to the vendor dial;
   in-package tests swap the field. Nothing outside the package varies across
   the seam (one production adapter + one test fake), so it stays unexported.
2. **Port method set — one method per vendor call the manager makes:**
   `DeclareStream(name, opts)` · `DeclareSuperStream(name, opts)` ·
   `QueryOffset(consumer, stream)` · `StoreOffset(consumer, stream, offset)` ·
   `NewConsumer(stream, opts, handler)` · `NewSuperStreamConsumer(stream, opts,
   handler)` · `NewProducer(stream, opts, confirmed)` ·
   `NewSuperStreamProducer(superStream, opts, confirmed)` · `Close()`.
   Constructors return the existing `consumerHandle` / `producerHandle`
   interfaces; `producerFactory`/`superProducerFactory` fold into the port.
   `OffsetStoreCount`/`OffsetStoreInterval`/`Logger` stay manager policy.
3. **Offset-storer asymmetry preserved behind the port:** plain consumers keep
   committing through their own handle (`StoreCustomOffset`); super-stream
   consumers commit through `port.StoreOffset` per partition; `trackConsumer`'s
   `storerFor` stays and is exercised in-process.
4. **Handler shape at the port is go-bricks-shaped:** `NewConsumer` /
   `NewSuperStreamConsumer` take `func(streamName string, offset int64, msg
   *amqp.Message)`; the vendor adapter does the `stream.ConsumerContext`
   unwrapping (a fake cannot construct that vendor struct). `*amqp.Message` is
   constructible in tests.
5. **Fake fidelity:** records call order (declare→bind→start assertions),
   injects errors per method, hands back consumer handles a test can push
   messages through so `deliver` runs end-to-end, stores/queries offsets in
   memory. The same fake serves the streams-lane pipeline tests.
6. **Container tests shrink to a smoke set only after** the in-process suite
   covers declare→bind→start ordering, `abortStartLocked` unwind, the SAC
   promotion callback and `resolveOffset`'s three-way fallback — a container
   assertion is deleted only when an in-process assertion replaced it.

### Delivery pipeline (card 6) — Stack B PR2 (ADR-068 + atom) and PR3 (atom lines)

1. **Location:** new `messaging/internal/delivery` package, importable by both
   lanes (`messaging/streams` already imports `messaging/internal/tracking`).
2. **The pipeline owns the per-message context:** trace extraction from a
   carrier (`trace.HeaderAccessor` — the streams `propertyAccessor` already
   satisfies it), span (kind consumer), per-message `leasescope.Install`,
   `EnsureTraceID`, handler invocation, panic → error, one `RecordConsume` at
   completion with `error.type`, failure log with lane-supplied fields.
   Outcomes: `succeeded · handlerError · panicked`.
3. **Settlement is a lane adapter:** AMQP ack / nack(requeue=false) /
   nack(requeue=false); streams store-offset / skip / skip (ADR-059: commit only
   after success). "Never requeue" and "commit only on success" stay adapter
   policy. Publisher-side injection is untouched (consume-side only).
4. **Telemetry values preserved per lane:** each lane supplies an attribute
    bundle (span extras, metric attributes, log fields, destination strings);
    the pipeline owns *when* and *once*. The only telemetry that moves is the
    consumed counter — now recorded at completion with `error.type` on failure
    for both lanes.
5. **Deletions:** exported `StartConsumeSpan` (repo policy: no compat shim;
    zero wiki/llms references; `!` + ADR-068 + atom), the receive-time
    `RecordAMQPConsumeMetrics` path, and the test-only `amqpDeliveryAccessor`
    (`registry.go:919-931`); `tracking` ends with one
    `RecordConsume(ctx, attrs, duration, err)` for both lanes.
6. **Streams lane on the pipeline (PR3):** trace context extracted from
    application properties (the consume span becomes a child of the producer's
    context), lease scope per delivery, unified metric; two atom lines for the
    two behavior changes.
7. **PRs:** PR1 environment port + fake + in-process `Start` suite; PR2
    pipeline + AMQP lane + `StartConsumeSpan` removal + ADR-068 + atom; PR3
    streams lane migration.

## Constraints

- ADR-059/063 chose parallel *shape* and shared instruments; this keeps the
  shape and shares only the pipeline body. ADR-059's future-work item
  "trace-context propagation" is the entry point.
- Concurrency shape (worker pool vs one goroutine per partition) stays outside
  the pipeline.
- ADR-033 bounded publish and ADR-063 confirmed publish are untouched.
