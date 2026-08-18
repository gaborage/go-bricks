# Messaging Delivery Pipeline Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Put the *Delivery pipeline* — trace extraction, consumer span, per-message lease scope, handler invocation, panic-to-error, one consumed record at completion, one outcome log line — in `messaging/internal/delivery`, run the AMQP lane on it, and delete the exported `StartConsumeSpan` and the receive-time metric path it carried.

**Architecture:** A lane hands `delivery.Run(ctx, *delivery.Request)` a *Carrier* (`trace.HeaderAccessor`), a destination, a body size, its span extras, its metric bundle, its logger, a `Handler`, and a `LogOutcome` closure. `Run` owns the per-message context and returns a `*Result` — outcome, error, duration, trace ID, the context-bound logger, and (on a panic) the recovered value and stack. *Settlement* never enters the pipeline: the classic lane acks or nacks-without-requeue on the returned `Result`; the streams lane will commit or skip on it in PR3. The consumed counter and the receive histogram collapse into one `tracking.RecordConsume(ctx, attrs, duration, err)` that both lanes call exactly once, at completion.

**Tech Stack:** Go 1.26 · `github.com/rabbitmq/amqp091-go` · OpenTelemetry (`otel`, `attribute`, `codes`, `semconv/v1.32.0`, `sdk/trace/tracetest`, `sdk/metric/metricdata`) · testify (`assert`/`require`) · `go test -race`.

**Spec:** [docs/superpowers/specs/2026-08-16-messaging-environment-port-and-delivery-pipeline-design.md](../specs/2026-08-16-messaging-environment-port-and-delivery-pipeline-design.md) — section "Delivery pipeline (card 6)", **decisions 1–5 and 7 only**. Decision 6 (streams lane on the pipeline) is **PR3 and out of scope**: do not touch `messaging/streams/runner.go` beyond the one-line recorder swap in Task 1, do not extract trace context in `runner.deliver`, do not install a lease scope there, do not change the streams panic wording.

**Vocabulary:** [CONTEXT.md](../../CONTEXT.md) — *Delivery pipeline*, *Carrier*, *Settlement*. Use those words in comments and commit messages; avoid "consume loop", "message processing", "headers", "ack"/"commit" as the name of the general step.

**Stack position:** Stack B PR2, on top of PR1 (`feature/streams-environment-port`, the *Environment port*, already implemented). This plan is **three dependent PRs**, in order:

| PR | Branch | Base | Carries |
| --- | --- | --- | --- |
| PR2a | `feature/messaging-consume-tracking` | `feature/streams-environment-port` | Task 1 + Task 2 (gates) |
| PR2b | `feature/messaging-delivery-pipeline` | `feature/messaging-consume-tracking` | Task 3 + Task 4 (gates) |
| PR2c | `feature/messaging-amqp-on-pipeline` | `feature/messaging-delivery-pipeline` | Tasks 5, 6, 7 + Task 8 (gates) |

Merging is bottom-up and maintainer-side. Only PR2c carries a `!` in its title (`refactor(messaging)!:`) — the CI apidiff job fails a new incompatible export change without it, and `StartConsumeSpan` is that change.

## Global Constraints

- Test function names are **camelCase** (`TestRunConvertsAPanicIntoAnError`); table-driven case names are **snake_case** (`{name: "handler_error"}`). 100% compliance across >800 test functions — no exceptions.
- Commit with `git commit -F <file>`; the repo's commit hook rejects heredoc `-m`. **Never** pass `--no-gpg-sign` — if signing fails, stop and report it.
- Implementers run `make check` **before every commit**, and `git branch --show-current` must print the branch of the PR they are in. The controller runs `make mutate`, the three pre-push gates, and every push.
- `messaging/streams` must **not** import `github.com/gaborage/go-bricks/messaging`. It imports `messaging/internal/tracking` today (`runner.go:19`, `publisher.go:22`) and will import `messaging/internal/delivery` in PR3 — both are under `messaging/`, both are legal, and neither is the parent package.
- The pipeline lives in `messaging/internal/delivery`. Nothing exported by `messaging` is added.
- **No `//nolint` anywhere in this plan.** One is deleted (`messaging/amqp_client.go:1213`, `//nolint:spancheck`) and none is added: the pipeline starts and ends its span in the same function, which is what spancheck asks for.
- Comments are bare-minimum — non-obvious intent only (`CLAUDE.md` → "Keep comments bare-minimum"). Every comment this plan writes is either a rationale a reader could not derive or a `// SECURITY:` annotation (there are none here).
- **Concurrency shape stays outside the pipeline.** The worker pool (`Registry.handleMessages`/`worker`, `registry.go:634-756`) and the streams lane's one-goroutine-per-partition shape are untouched.
- **ADR-033 (bounded publish) and ADR-063 (confirmed publish) are untouched.** Publisher-side trace injection (`amqp_client.go:349`, `streams/publisher.go`) is untouched. This is consume-side only.
- **The `messaging` allocs/op guard must not regress.** `TestRegistryProcessMessagePerDeliveryLoggerAllocs` (`messaging/registry_test.go:2905-2935`) asserts `avg < 42.0`; its comment records BEFORE = 47.0, AFTER = 38.0. The budget for Task 5 is therefore **4.0 allocs/op over the current 38.0**, and the analysis says the pipeline should land at or below it (Task 5 Step 6 measures and records it).

## Telemetry preserved, value by value

Every value the AMQP lane emits today, and where it comes from after Task 5. The single row marked **MOVED** is the only behavior change, and it is what ADR-068 and `[C60.6]` document.

| Telemetry value | Today | After Task 5 |
| --- | --- | --- |
| Span name `"<queue> receive"` | `amqp_client.go:1223` | `delivery.Run` — `req.Destination + " " + spanOperationReceive` |
| Span kind `Consumer` | `amqp_client.go:1226` | `delivery.Run` |
| `messaging.system=rabbitmq` | `amqp_client.go:1231` | `delivery.spanAttributes` (common four) |
| `messaging.operation.name=receive` | `amqp_client.go:1232` | `delivery.spanAttributes` |
| `messaging.destination.name=<queue>` | `amqp_client.go:1233` | `delivery.spanAttributes` — `req.Destination` |
| `messaging.message.body.size` | `amqp_client.go:1234` | `delivery.spanAttributes` — `req.BodySize` |
| `messaging.rabbitmq.exchange` (omitted when empty) | `amqp_client.go:1236-1238` | `registry.consumeSpanExtras` |
| `messaging.rabbitmq.destination.routing_key` (omitted when empty) | `amqp_client.go:1239-1242` | `registry.consumeSpanExtras` |
| `messaging.message.id` (omitted when empty) | `amqp_client.go:1243-1245` | `registry.consumeSpanExtras` |
| `messaging.message.conversation_id` (omitted when empty) | `amqp_client.go:1246-1248` | `registry.consumeSpanExtras` |
| `span.RecordError` + `SetStatus(codes.Error, …)` on handler error | `registry.go:814-815` | `delivery.Run` (one site for both failure outcomes) |
| `span.RecordError` + `SetStatus(codes.Error, …)` on panic | `registry.go:909-911` | `delivery.Run` (same site) |
| Trace extraction from delivery headers | `amqp_client.go:1217-1218` (`amqpHeaderAccessor` → `gobrickstrace.ExtractFromHeaders`) | `delivery.Run` — `req.Carrier`, still `amqpHeaderAccessor`, still go-bricks extraction only |
| `messaging.client.operation.duration` sample | `registry.go:818`, `:835`, `:912` (`RecordAMQPConsumeCompletion`) | `delivery.Run` — one `tracking.RecordConsume` |
| **`messaging.client.consumed.messages` +1** | **`amqp_client.go:1253` — at receive, `err` hardcoded `nil`, so never carries `error.type`** | **MOVED: `delivery.Run` at completion, with `error.type` when the delivery failed** |
| Metric attrs `messaging.system` / `operation.name` / `destination.name` | `tracking/metrics.go:224-228` | `tracking.ConsumeAttributes.slice` (Task 1) |
| Metric attrs `rabbitmq.exchange` / `routing_key` / `queue` (omitted when empty) | `tracking/metrics.go:229-237` | `tracking.ConsumeAttributes.slice` (Task 1) |
| Metric destination string `exchange:routing_key:queue` | `tracking/utils.go:28-40` (`formatDestinationName`, called twice per delivery) | unchanged function, called **once** per delivery via `tracking.AMQPConsumeAttributes` |
| `error.type` on the histogram | `tracking/metrics.go:238-240` | same helper |
| DEBUG `"Processing message"` + `correlation_id`, `message_id`, `routing_key`, `exchange`, `delivery_tag`, `body_size`, and the `Enabled()` skip | `registry.go:790-801` | `registry.logProcessing`, called inside the lane's `Handle` |
| INFO `"Message processed successfully"` + `correlation_id`, `message_id`, `processing_time` | `registry.go:828-832` | `registry.logOutcome`, case `Succeeded` |
| ERROR `"Message processing failed - discarding without requeue"` + `Err` | `registry.go:807-810` | `registry.logOutcome`, case `HandlerError` |
| ERROR `"Panic recovered in message handler - discarding without requeue"` + `panic`, `stack` | `registry.go:901-905` | `registry.logOutcome`, case `Panicked` |
| Failure lines stamp `correlation_id` **twice** (trace ID then `delivery.CorrelationId`; a parser takes the last) | `registry.go:865-887` (`buildFailureLogEvent`) | **unchanged function, unchanged order** |
| `trace_id` / `span_id` on every per-message line | `registry.go:780` (`log.WithContext(msgCtx)`) | `delivery.Run`, handed back as `Result.Log` |
| `"Failed to ack message"` + `correlation_id`, `delivery_tag` | `registry.go:838-845` | `registry.ackMessage`, using `Result.Log` / `Result.TraceID` |
| `"Failed to nack message"` + `correlation_id`, `delivery_tag` | `registry.go:850-863` | **unchanged function**, using `Result.Log` / `Result.TraceID` |
| Panic error text `panic in message handler: %v` | `registry.go:908` | `delivery.invoke` — the wording both lanes take (streams adopts it in PR3) |
| Duration measured from function entry, before span setup | `registry.go:760` (`startTime := time.Now()`) | `delivery.Run` first statement |

Two ordering facts change and neither is an emitted value: (1) settlement now runs **after** `span.End()` and `scope.ReleaseAll()` instead of before them, because settlement is lane-side — the ack/nack log line still carries `trace_id`/`span_id` because `Result.Log` was bound while the span was open; (2) on the panic path the metric is recorded after the outcome line instead of before it, which is already the order the success and error paths use.

## Design decisions this plan locks in

**The entry point returns a `*Result`; settlement is wholly lane-side.** `func Run(ctx context.Context, req *Request) *Result`. There is no settle closure, no `Settler` interface, and no broker call inside `messaging/internal/delivery`. The classic lane reads `res.Outcome` and calls `ackMessage` or `nackMessage`; the streams lane will read the same field and commit or skip in PR3. "Never requeue" (ADR-033's neighbourhood) and "commit only after success" (ADR-059) therefore stay exactly where they are.

**Pointers, not values, at the seam.** `Request` is 160 bytes and `Result` is 104; gocritic's `hugeParam` (the `performance` tag is enabled, `.golangci.yml:60-67`) flags value parameters over 80 bytes. `Run` takes `*Request`, `LogOutcome` takes `*Result`, and `Run` returns the same `*Result` the lane's `LogOutcome` already saw. `Run` never returns nil.

**Three outcomes, `Succeeded` as the zero value.** `type Outcome int` with `Succeeded`, `HandlerError`, `Panicked`. The zero value being `Succeeded` is what lets `invoke` build its `Result` first and only overwrite on failure. The lane's `switch` covers all three with no `default`, which satisfies `exhaustive`.

**The handler adapter receives the two things derived from the per-message context.** `type Handler func(ctx context.Context, log logger.Logger, traceID string) error`. The pipeline owns the context, so it owns `log.WithContext(msgCtx)` and `EnsureTraceID(msgCtx)`; handing both to the lane's adapter is what keeps the classic lane's DEBUG line stamping the same `correlation_id` without a second `WithContext` allocation, and it is why there is one lane closure for invocation rather than a closure plus a "before handle" hook.

**The Carrier is `trace.HeaderAccessor`, and extraction stays go-bricks-only.** `Run` calls `gobrickstrace.ExtractFromHeaders(ctx, req.Carrier)` unconditionally — that function already returns `ctx` unchanged for a nil accessor (`trace/trace.go:114-117`), so there is no nil branch to write or test. The classic lane passes `&amqpHeaderAccessor{headers: delivery.Headers}` (`amqp_client.go:1185`), the streams lane will pass its `propertyAccessor` in PR3. **What this deliberately does not do is run an OTel `TextMapPropagator`.** go-bricks extraction populates context *values* (`trace_id`, `traceparent`, `tracestate`) and never the OTel span context, so a consume span is a root span today and stays a root span here. Making it a true OTel child would re-parent every existing consumer span and change the trace ID of every AMQP consume trace in production — a separate decision, recorded as out of scope in ADR-068 and pinned by `TestRunStartsARootSpanWhenOnlyAW3CHeaderTravelled`.

**One lease scope per message, drained last.** `leasescope.Install(msgCtx)` then `defer scope.ReleaseAll()` before `defer span.End()`, so the LIFO order is span-then-scope: the span closes first, the scope drains last, exactly as `registry.go:768-778` documents today.

**One `RecordConsume` at completion, for both lanes.** `tracking.RecordConsume(ctx, attrs, duration, err)` records the duration histogram (skipped for a zero duration) and increments the consumed counter **regardless of outcome** — the message was consumed — with `error.type` separating the failures. That is `RecordStreamConsume`'s existing contract, generalized; the classic lane adopts it, which is the one moved value above.

**The attribute bundle is a lane-built value, not a `[]attribute.KeyValue`.** `tracking.ConsumeAttributes` has unexported fields and two constructors, `AMQPConsumeAttributes(exchange, routingKey, queue)` and `StreamConsumeAttributes(streamName)`. The destination-string rule (`formatDestinationName`) therefore keeps exactly one implementation, the pipeline stays lane-agnostic, and a lane cannot invent an attribute set the other lane's queries will not recognise.

**`LogOutcome` is one closure, not a field map.** The classic lane's failure lines stamp `correlation_id` twice *by design* (`registry.go:872-874`) and its success line is INFO while its failures are ERROR with different field sets; a field-map API would have to reproduce all of that. One closure taking `*Result` reproduces it by simply keeping `buildFailureLogEvent` unchanged.

**`Handle` and `LogOutcome` are required.** Both lanes are in this repository, a nil there is a programming error visible at the call site, and a nil-guard would be untested defensive code. `Run` does not check them.

## Reference counts for every symbol deleted or renamed

Counted with `git grep -c <symbol>` on `feature/streams-environment-port` at `2a9b20c`. `docs/superpowers/plans/2026-08-16-streams-environment-port.md` mentions three of these in its own out-of-scope note; that file is the PR1 plan and is **not** edited by this plan.

| Symbol | Total | Where | Disposition |
| --- | --- | --- | --- |
| `StartConsumeSpan` | 16 | `amqp_client.go` 2 · `registry.go` 2 · `otel_test.go` 7 · `tracking/metrics.go` 2 (doc comments) · `tracking/metrics_test.go` 1 (comment) · PR1 plan 1 | Deleted in Task 6. `wiki/` 0, `llms.txt` 0, `CLAUDE.md` 0 |
| `RecordAMQPConsumeMetrics` | 8 | `tracking/metrics.go` 2 · `tracking/metrics_test.go` 4 · `amqp_client.go` 1 · PR1 plan 1 | Deleted in Task 6 |
| `RecordAMQPConsumeCompletion` | 10 | `tracking/metrics.go` 3 · `tracking/metrics_test.go` 3 · `registry.go` 3 · `registry_test.go` 1 (comment) | Deleted in Task 6 |
| `RecordStreamConsume` | 9 | `tracking/metrics.go` 2 · `tracking/metrics_test.go` 6 · `streams/runner.go` 1 | Deleted in Task 1 (replaced by `RecordConsume`) |
| `amqpDeliveryAccessor` | 12 | `registry.go` 4 · `registry_test.go` 3 · `amqp_test.go` 5 | Deleted in Task 6; test sites retype to `amqpHeaderAccessor` |
| `handlePanicRecovery` | 3 | `registry.go` 3 (declaration + doc + call) | Deleted in Task 5; no test calls it |
| `consumeAttributes` (unexported) | 4 | `tracking/metrics.go` 4 | Deleted in Task 1, folded into `ConsumeAttributes.slice` |
| `streamAttributes` (unexported) | 4 | `tracking/metrics.go` 4 | Deleted in Task 1, folded into `ConsumeAttributes.slice` |
| `operationReceive` (in `messaging`) | 4 | `amqp_client.go` 4 (declaration + 3 uses, all inside `StartConsumeSpan`) | Deleted in Task 6 — otherwise `unused` flags it |
| `deliveryAttributes` (unexported) | 0 | introduced in Task 1 | Deleted in Task 6 with its two callers |

`buildFailureLogEvent`, `nackMessage`, `amqpHeaderAccessor`, `messagingTracerName`, `messagingSystemRabbitMQ` and `formatDestinationName` all survive unchanged.

---

## PR2a — the tracking collapse

**Branch:** `feature/messaging-consume-tracking`, cut from `feature/streams-environment-port`.

**Constraints reminder:** camelCase test names · `git commit -F <file>`, never `--no-gpg-sign` · `make check` before the commit, with `git branch --show-current` printing `feature/messaging-consume-tracking` · no `//nolint` · comments bare-minimum · `messaging/streams` must not import `messaging` · nothing exported by `messaging` changes, so **no ADR and no atom in this PR** (`messaging/internal/tracking` is an internal package; apidiff ignores it) · the streams lane's behavior must not change: `RecordConsume` with a stream bundle must emit byte-identical attributes to `RecordStreamConsume`.

### Task 1: One consume recorder for both lanes

**Files:**

- Modify: `messaging/internal/tracking/metrics.go` (`consumeAttributes` `215-242`, `streamAttributes` `244-258`, `RecordAMQPConsumeMetrics` `260-286`, `RecordAMQPConsumeCompletion` `288-304`, `RecordStreamConsume` `306-329`)
- Modify: `messaging/internal/tracking/metrics_test.go` (the three `TestRecordStreamConsume*` functions `531-611`)
- Modify: `messaging/streams/runner.go` (line `256`)

**Interfaces:**

- Produces, consumed by Tasks 3, 5 and PR3:
  - `type ConsumeAttributes struct` (unexported fields `destination`, `exchange`, `routingKey`, `queue`)
  - `func AMQPConsumeAttributes(exchange, routingKey, queue string) ConsumeAttributes`
  - `func StreamConsumeAttributes(streamName string) ConsumeAttributes`
  - `func RecordConsume(ctx context.Context, attrs ConsumeAttributes, duration time.Duration, err error)`
- Produces, package-internal and deleted again in Task 6:
  - `func deliveryAttributes(delivery *amqp.Delivery, queueName string) ConsumeAttributes`
  - `func (a ConsumeAttributes) slice(err error) []attribute.KeyValue`
- Consumes, **unchanged**: `formatDestinationName` (`tracking/utils.go:28`), `extractErrorType` (`utils.go:47`), `durationToSeconds` (`utils.go:65`), `getAMQPMeter` (`metrics.go:153`), the `amqpOperationDuration` / `amqpMessagesConsumed` instruments, `ResetMeterForTesting` (`tracking/testing.go:11`).
- Consumes, **unchanged signature**: `RecordAMQPConsumeMetrics`, `RecordAMQPConsumeCompletion` — both keep their exported signatures and their emitted attributes in this PR; only their bodies delegate to the new bundle.

**Estimated LoC:** ~160 changed (metrics.go +58/−52, metrics_test.go +112/−81, runner.go 1).

- [ ] **Step 1: Write the failing tests — one recorder, two attribute bundles**

In `messaging/internal/tracking/metrics_test.go`, delete `TestRecordStreamConsumeSuccess` (`531-559`), `TestRecordStreamConsumeFailureCarriesErrorType` (`561-583`) and `TestRecordStreamConsumeZeroDurationSkipsHistogram` (`585-600`), and put these five in their place (same position in the file, so `assertHasAttribute` at `520` and `assertAttributeAbsent` at `602` still bracket them):

```go
// setupRecordConsume installs a fresh meter provider bound to fresh instruments
// and returns it. The instruments are package singletons, so the reset must
// bracket the test on both sides or state leaks into siblings.
func setupRecordConsume(t *testing.T) *obtest.TestMeterProvider {
	t.Helper()
	prev := otel.GetMeterProvider()
	mp := obtest.NewTestMeterProvider()
	otel.SetMeterProvider(mp)
	resetMeterForTesting()
	initAMQPMeter()
	t.Cleanup(func() {
		otel.SetMeterProvider(prev)
		resetMeterForTesting()
		require.NoError(t, mp.Shutdown(context.Background()))
	})
	return mp
}

func TestRecordConsumeCarriesAMQPAttributes(t *testing.T) {
	mp := setupRecordConsume(t)

	RecordConsume(context.Background(), AMQPConsumeAttributes("events", testRoutingKey, testQueueName), 25*time.Millisecond, nil)

	rm := mp.Collect(t)

	durationMetric := obtest.FindMetric(rm, metricOperationDuration)
	require.NotNil(t, durationMetric)
	histData, ok := durationMetric.Data.(metricdata.Histogram[float64])
	require.True(t, ok)
	require.Len(t, histData.DataPoints, 1)

	attrs := histData.DataPoints[0].Attributes.ToSlice()
	assertHasAttribute(t, attrs, attrMessagingSystem, messagingSystemRabbitMQ)
	assertHasAttribute(t, attrs, attrMessagingOperation, operationReceive)
	assertHasAttribute(t, attrs, attrMessagingDestination, "events:test.key:test-queue")
	assertHasAttribute(t, attrs, attrMessagingRabbitMQExchange, "events")
	assertHasAttribute(t, attrs, attrMessagingRabbitMQRoutingKey, testRoutingKey)
	assertHasAttribute(t, attrs, attrMessagingRabbitMQQueue, testQueueName)
	assertAttributeAbsent(t, attrs, attrErrorType)

	obtest.AssertMetricValue(t, rm, metricMessagesConsumed, int64(1))
}

func TestRecordConsumeOmitsTheGranularAttributesTheDeliveryDidNotCarry(t *testing.T) {
	mp := setupRecordConsume(t)

	// The default exchange with no routing key: only the queue identifies it.
	RecordConsume(context.Background(), AMQPConsumeAttributes("", "", testQueueName), time.Millisecond, nil)

	rm := mp.Collect(t)
	durationMetric := obtest.FindMetric(rm, metricOperationDuration)
	require.NotNil(t, durationMetric)
	histData, ok := durationMetric.Data.(metricdata.Histogram[float64])
	require.True(t, ok)
	require.Len(t, histData.DataPoints, 1)

	attrs := histData.DataPoints[0].Attributes.ToSlice()
	assertHasAttribute(t, attrs, attrMessagingDestination, "::test-queue")
	assertHasAttribute(t, attrs, attrMessagingRabbitMQQueue, testQueueName)
	assertAttributeAbsent(t, attrs, attrMessagingRabbitMQExchange)
	assertAttributeAbsent(t, attrs, attrMessagingRabbitMQRoutingKey)
}

func TestRecordConsumeUsesTheStreamAsItsOwnDestination(t *testing.T) {
	mp := setupRecordConsume(t)

	RecordConsume(context.Background(), StreamConsumeAttributes(testStreamName), 15*time.Millisecond, nil)

	rm := mp.Collect(t)
	durationMetric := obtest.FindMetric(rm, metricOperationDuration)
	require.NotNil(t, durationMetric)
	histData, ok := durationMetric.Data.(metricdata.Histogram[float64])
	require.True(t, ok)
	require.Len(t, histData.DataPoints, 1)

	// The stream protocol routes to a stream directly: no exchange, no routing
	// key, no queue to attribute.
	attrs := histData.DataPoints[0].Attributes.ToSlice()
	assertHasAttribute(t, attrs, attrMessagingSystem, messagingSystemRabbitMQ)
	assertHasAttribute(t, attrs, attrMessagingOperation, operationReceive)
	assertHasAttribute(t, attrs, attrMessagingDestination, testStreamName)
	assertAttributeAbsent(t, attrs, attrMessagingRabbitMQExchange)
	assertAttributeAbsent(t, attrs, attrMessagingRabbitMQRoutingKey)
	assertAttributeAbsent(t, attrs, attrMessagingRabbitMQQueue)

	obtest.AssertMetricValue(t, rm, metricMessagesConsumed, int64(1))
}

func TestRecordConsumeCountsAFailedDeliveryWithItsErrorType(t *testing.T) {
	mp := setupRecordConsume(t)

	RecordConsume(context.Background(), StreamConsumeAttributes(testStreamName), 5*time.Millisecond, errors.New("handler failed"))

	rm := mp.Collect(t)

	durationMetric := obtest.FindMetric(rm, metricOperationDuration)
	require.NotNil(t, durationMetric)
	histData, ok := durationMetric.Data.(metricdata.Histogram[float64])
	require.True(t, ok)
	require.Len(t, histData.DataPoints, 1)
	assertHasAttribute(t, histData.DataPoints[0].Attributes.ToSlice(), attrErrorType, "*errors.errorString")

	// The message WAS consumed regardless of how its handler ended; error.type
	// is what separates the failures on the counter.
	consumed := obtest.FindMetric(rm, metricMessagesConsumed)
	require.NotNil(t, consumed)
	sumData, ok := consumed.Data.(metricdata.Sum[int64])
	require.True(t, ok)
	require.Len(t, sumData.DataPoints, 1)
	assert.Equal(t, int64(1), sumData.DataPoints[0].Value)
	assertHasAttribute(t, sumData.DataPoints[0].Attributes.ToSlice(), attrErrorType, "*errors.errorString")
}

func TestRecordConsumeZeroDurationSkipsHistogram(t *testing.T) {
	mp := setupRecordConsume(t)

	RecordConsume(context.Background(), StreamConsumeAttributes(testStreamName), 0, nil)

	rm := mp.Collect(t)

	assert.Nil(t, obtest.FindMetric(rm, metricOperationDuration), "zero duration records no histogram sample")
	obtest.AssertMetricValue(t, rm, metricMessagesConsumed, int64(1))
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./messaging/internal/tracking/ -run TestRecordConsume`

Expected: FAIL — a build failure, Go's red for a function that does not exist yet:

```text
# github.com/gaborage/go-bricks/messaging/internal/tracking [github.com/gaborage/go-bricks/messaging/internal/tracking.test]
messaging/internal/tracking/metrics_test.go:...: undefined: RecordConsume
messaging/internal/tracking/metrics_test.go:...: undefined: AMQPConsumeAttributes
messaging/internal/tracking/metrics_test.go:...: undefined: StreamConsumeAttributes
FAIL	github.com/gaborage/go-bricks/messaging/internal/tracking [build failed]
```

- [ ] **Step 3: Collapse the two attribute builders into one bundle**

In `messaging/internal/tracking/metrics.go`, replace `consumeAttributes` (`215-242`) and `streamAttributes` (`244-258`) with:

```go
// ConsumeAttributes identifies one consumed message on the receive instruments.
// A lane builds it once per message through AMQPConsumeAttributes or
// StreamConsumeAttributes; the delivery pipeline decides when it is recorded.
type ConsumeAttributes struct {
	destination string
	exchange    string
	routingKey  string
	queue       string
}

// AMQPConsumeAttributes identifies a classic-lane delivery: the OTel RabbitMQ
// consumer destination plus the granular fields metric queries filter on.
func AMQPConsumeAttributes(exchange, routingKey, queue string) ConsumeAttributes {
	return ConsumeAttributes{
		destination: formatDestinationName(exchange, routingKey, queue),
		exchange:    exchange,
		routingKey:  routingKey,
		queue:       queue,
	}
}

// StreamConsumeAttributes identifies a streams-lane delivery. The stream itself
// is the destination: the stream protocol routes to a stream directly, so there
// is no exchange and no routing key to attribute.
func StreamConsumeAttributes(streamName string) ConsumeAttributes {
	return ConsumeAttributes{destination: streamName}
}

// deliveryAttributes builds the bundle from a delivery, tolerating a nil one
// for the exported receive-time recorders below.
func deliveryAttributes(delivery *amqp.Delivery, queueName string) ConsumeAttributes {
	var exchange, routingKey string
	if delivery != nil {
		exchange = delivery.Exchange
		routingKey = delivery.RoutingKey
	}
	return AMQPConsumeAttributes(exchange, routingKey, queueName)
}

// slice renders the bundle plus the outcome as metric attributes. A granular
// field the message did not carry is omitted rather than reported empty.
func (a ConsumeAttributes) slice(err error) []attribute.KeyValue {
	attrs := make([]attribute.KeyValue, 0, 7)
	attrs = append(attrs,
		attribute.String(attrMessagingSystem, messagingSystemRabbitMQ),
		attribute.String(attrMessagingOperation, operationReceive),
		attribute.String(attrMessagingDestination, a.destination),
	)
	if a.exchange != "" {
		attrs = append(attrs, attribute.String(attrMessagingRabbitMQExchange, a.exchange))
	}
	if a.routingKey != "" {
		attrs = append(attrs, attribute.String(attrMessagingRabbitMQRoutingKey, a.routingKey))
	}
	if a.queue != "" {
		attrs = append(attrs, attribute.String(attrMessagingRabbitMQQueue, a.queue))
	}
	if errorType := extractErrorType(err); errorType != "" {
		attrs = append(attrs, attribute.String(attrErrorType, errorType))
	}
	return attrs
}
```

- [ ] **Step 4: Add `RecordConsume` and delegate the two surviving recorders to the bundle**

Still in `metrics.go`, replace the body of `RecordAMQPConsumeMetrics` (`268-286`) so its attribute build goes through the bundle, leaving its signature and emitted attributes untouched:

```go
func RecordAMQPConsumeMetrics(ctx context.Context, delivery *amqp.Delivery, queueName string, duration time.Duration, err error) {
	meter := getAMQPMeter()
	if meter == nil {
		return
	}

	commonAttrs := deliveryAttributes(delivery, queueName).slice(err)

	// Record duration histogram (in seconds) - only if duration is > 0
	if amqpOperationDuration != nil && duration > 0 {
		durationSeconds := durationToSeconds(duration)
		amqpOperationDuration.Record(ctx, durationSeconds, metric.WithAttributes(commonAttrs...))
	}

	// Record consumed messages counter (only on success)
	if amqpMessagesConsumed != nil && err == nil {
		amqpMessagesConsumed.Add(ctx, 1, metric.WithAttributes(commonAttrs...))
	}
}
```

Replace `RecordAMQPConsumeCompletion`'s last line (`303`) the same way:

```go
	amqpOperationDuration.Record(ctx, durationToSeconds(duration), metric.WithAttributes(deliveryAttributes(delivery, queueName).slice(err)...))
```

Then delete `RecordStreamConsume` (`306-329`) and put `RecordConsume` in its place:

```go
// RecordConsume records one finished delivery on the receive instruments both
// messaging lanes share: the duration histogram, and the consumed counter,
// which increments regardless of the outcome — the message WAS consumed —
// with error.type separating the failures.
//
// The delivery pipeline calls this exactly once per message, at completion.
func RecordConsume(ctx context.Context, attrs ConsumeAttributes, duration time.Duration, err error) {
	meter := getAMQPMeter()
	if meter == nil {
		return
	}

	attrSet := metric.WithAttributes(attrs.slice(err)...)

	if amqpOperationDuration != nil && duration > 0 {
		amqpOperationDuration.Record(ctx, durationToSeconds(duration), attrSet)
	}
	if amqpMessagesConsumed != nil {
		amqpMessagesConsumed.Add(ctx, 1, attrSet)
	}
}
```

- [ ] **Step 5: Swap the streams lane onto the new recorder**

In `messaging/streams/runner.go`, line `256`:

```go
	tracking.RecordConsume(ctx, tracking.StreamConsumeAttributes(streamName), time.Since(start), err)
```

Nothing else in the file changes. `deliver` keeps its signature, its span, its log lines and its commit policy; PR3 is what moves them.

- [ ] **Step 6: Run the new tests to verify they pass**

Run: `go test ./messaging/internal/tracking/ -run TestRecordConsume -v`

Expected: five `--- PASS` lines then `ok  github.com/gaborage/go-bricks/messaging/internal/tracking`.

- [ ] **Step 7: Run the affected packages to verify nothing else moved**

Run: `go test -race ./messaging/... ./messaging/internal/tracking/`

Expected: `ok` for every package. Specifically, these must still pass untouched, which is the proof the two surviving recorders emit what they always did: `TestRecordAMQPConsumeMetricsSuccess` (`metrics_test.go:190`), `TestRecordAMQPConsumeMetricsZeroDuration` (`:238`), `TestRecordAMQPConsumeCompletion` (`:275`, all three cases including `assert.Nil(t, obtest.FindMetric(rm, metricMessagesConsumed))`), and every consume test in `messaging/registry_test.go`.

- [ ] **Step 8: `make check`**

Run: `pwd && git branch --show-current && make check`

Expected: prints the repo root, `feature/messaging-consume-tracking`, then exits 0. Watch for `gci` import ordering in `metrics_test.go` (the new helper adds no import) — `gofmt -l` is silent on gci/gofumpt rules, so run `git status --porcelain` after any `make fmt`.

- [ ] **Step 9: Commit**

```bash
cat > /tmp/tracking-collapse-msg.txt <<'EOF'
refactor(messaging): one consume recorder for both lanes

The two messaging lanes recorded a consumed message through two functions
with two attribute builders: consumeAttributes for the classic lane,
streamAttributes for the streams lane. The rules they encode -- the OTel
RabbitMQ destination string, which granular attributes are omitted when
empty, when error.type is stamped -- were duplicated and free to drift.

Collapse them into one lane-built value, tracking.ConsumeAttributes, with
one constructor per lane and one renderer, and add
RecordConsume(ctx, attrs, duration, err): the duration histogram plus the
consumed counter, which increments regardless of the outcome because the
message was consumed either way, with error.type separating the failures.
That is RecordStreamConsume's contract, generalized; the streams lane
swaps onto it with a single line and emits byte-identical attributes.

RecordAMQPConsumeMetrics and RecordAMQPConsumeCompletion keep their
signatures and their emitted attributes, and now build them through the
same bundle. The classic lane moves onto RecordConsume when it moves onto
the delivery pipeline.

Internal package only: no exported surface changes.
EOF
git add messaging/internal/tracking/metrics.go messaging/internal/tracking/metrics_test.go messaging/streams/runner.go
git commit -F /tmp/tracking-collapse-msg.txt
```

### Task 2: Gates for PR2a (controller only)

Implementers stop after Task 1. The controller runs everything below, in this order, and never delegates it.

- [ ] **Step 1: `make check`, backgrounded**

```bash
make check
```

Run with `run_in_background: true` (CLAUDE.md workflow rule). Expected: exits 0.

- [ ] **Step 2: `/simplify`**

Runs first because it mutates the diff. Likely target: the three-line preamble each recorder repeats (`meter := getAMQPMeter(); if meter == nil { return }`) — leave it, it is the package's existing shape in nine functions and unifying it is a separate change. If it changes code, re-run `make check`.

- [ ] **Step 3: `/security-audit`**

This diff moves no secret and reaches no boundary; the audit's real question is whether any attribute now carries caller-controlled unbounded cardinality. Answer: no new attribute is added, and `destination`/`exchange`/`routingKey`/`queue` come from the same declaration-time strings they came from before. If it changes code, re-run `make check`.

- [ ] **Step 4: `/code-review` (CodeRabbit)**

Must see the final diff. Expect it to ask for an ADR: answer that `messaging/internal/tracking` is an internal package with no exported-surface change (apidiff ignores `internal/`), and the streams lane's emitted attributes are unchanged, so ADR-and-atom does not apply until PR2c.

- [ ] **Step 5: `make mutate`, backgrounded, after committing**

```bash
make mutate
```

`run_in_background: true`. The scope is `merge-base..HEAD`, so **commit first** — uncommitted work yields `no mutatable changes` and a misleading exit 0. Proof it ran is a `(N mutants on changed lines)` line with N > 0. The lines most likely to survive and their killers: `duration > 0` → `duration >= 0` is killed by `TestRecordConsumeZeroDurationSkipsHistogram`; each `!= ""` guard in `slice` is killed by `TestRecordConsumeOmitsTheGranularAttributesTheDeliveryDidNotCarry` and `TestRecordConsumeUsesTheStreamAsItsOwnDestination`.

- [ ] **Step 6: Push and open PR2a**

Confirm the branch is `feature/messaging-consume-tracking`, push, and open the PR against `feature/streams-environment-port` with title `refactor(messaging): one consume recorder for both lanes` and this body:

```markdown
## What

The two messaging lanes recorded a consumed message through two functions with two attribute builders, so the destination-string rule, the omit-when-empty rule and the `error.type` rule each existed twice. They collapse into one lane-built `tracking.ConsumeAttributes` and one `RecordConsume(ctx, attrs, duration, err)`; the streams lane swaps onto it in a line and emits byte-identical attributes.

## Impact

None. `messaging/internal/tracking` is internal, the two exported AMQP recorders keep their signatures and their attributes, and no metric value changes on either lane.

## Verification

CI gates only.
```

Note for the reviewer, only if asked: CodeRabbit skips stacked PRs whose base is not `main`, so post `@coderabbitai review` on the PR after opening it.

---

## PR2b — the delivery pipeline package

**Branch:** `feature/messaging-delivery-pipeline`, cut from `feature/messaging-consume-tracking`.

**Constraints reminder:** camelCase test names, snake_case table cases · `git commit -F <file>`, never `--no-gpg-sign` · `make check` before the commit, with `git branch --show-current` printing `feature/messaging-delivery-pipeline` · no `//nolint` · comments bare-minimum · **no lane is migrated in this PR**: `messaging/registry.go`, `messaging/amqp_client.go` and `messaging/streams/` are not edited · nothing exported by `messaging` changes, so no ADR and no atom · the package must be complete enough that Task 5 adds no production line to it.

### Task 3: The delivery pipeline and its suite

**Files:**

- Create: `messaging/internal/delivery/delivery.go`
- Create: `messaging/internal/delivery/delivery_test.go`

**Interfaces:**

- Consumes from Task 1: `tracking.ConsumeAttributes`, `tracking.AMQPConsumeAttributes`, `tracking.StreamConsumeAttributes`, `tracking.RecordConsume`, `tracking.ResetMeterForTesting`.
- Consumes, **unchanged**: `gobrickstrace.HeaderAccessor` / `ExtractFromHeaders` / `EnsureTraceID` (`trace/trace.go:108,114,45`), `leasescope.Install` (`internal/leasescope/scope.go:69`), `logger.Logger` / `logger.LogEvent` (`logger/interface.go:9,21`).
- Produces, consumed by Tasks 5–6 and PR3:
  - `type Outcome int` with `Succeeded`, `HandlerError`, `Panicked`
  - `type Handler func(ctx context.Context, log logger.Logger, traceID string) error`
  - `type Request struct { Carrier gobrickstrace.HeaderAccessor; Destination string; BodySize int; SpanExtras []attribute.KeyValue; Metrics tracking.ConsumeAttributes; Log logger.Logger; Handle Handler; LogOutcome func(*Result) }`
  - `type Result struct { Outcome Outcome; Err error; Duration time.Duration; TraceID string; Log logger.Logger; Panic any; Stack []byte }`
  - `func Run(ctx context.Context, req *Request) *Result`

**Estimated LoC:** ~710 new (delivery.go ~150, delivery_test.go ~560).

- [ ] **Step 1: Write the failing suite**

Create `messaging/internal/delivery/delivery_test.go`:

```go
package delivery

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	semconv "go.opentelemetry.io/otel/semconv/v1.32.0"
	"go.opentelemetry.io/otel/trace"

	"github.com/gaborage/go-bricks/internal/leasescope"
	"github.com/gaborage/go-bricks/logger"
	"github.com/gaborage/go-bricks/messaging/internal/tracking"
	obtest "github.com/gaborage/go-bricks/observability/testing"
	gobrickstrace "github.com/gaborage/go-bricks/trace"
)

const (
	testQueue     = "orders"
	testTraceID   = "req-2026"
	testTraceParent = "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"
)

// mapCarrier is a Carrier over a plain map: the pipeline only ever reads through
// trace.HeaderAccessor, so a test does not need either lane's header type.
type mapCarrier map[string]any

func (c mapCarrier) Get(key string) any        { return c[key] }
func (c mapCarrier) Set(key string, value any) { c[key] = value }

// nopEvent satisfies logger.LogEvent without recording: the pipeline itself
// writes no line, so there is nothing on an event to assert.
type nopEvent struct{}

func (nopEvent) Msg(string)                              {}
func (nopEvent) Msgf(string, ...any)                     {}
func (e nopEvent) Err(error) logger.LogEvent             { return e }
func (e nopEvent) Str(_, _ string) logger.LogEvent       { return e }
func (e nopEvent) Int(string, int) logger.LogEvent       { return e }
func (e nopEvent) Int64(string, int64) logger.LogEvent   { return e }
func (e nopEvent) Uint64(string, uint64) logger.LogEvent { return e }
func (e nopEvent) Dur(string, time.Duration) logger.LogEvent { return e }
func (e nopEvent) Interface(string, any) logger.LogEvent { return e }
func (e nopEvent) Bytes(string, []byte) logger.LogEvent  { return e }
func (e nopEvent) Bool(string, bool) logger.LogEvent     { return e }
func (nopEvent) Enabled() bool                           { return true }

// bindingLogger records the context it was bound to and the logger that binding
// produced, so a test can assert WHICH logger the lane gets back.
type bindingLogger struct {
	boundTo context.Context
	bound   *bindingLogger
}

func (l *bindingLogger) WithContext(ctx any) logger.Logger {
	msgCtx, _ := ctx.(context.Context)
	l.bound = &bindingLogger{boundTo: msgCtx}
	return l.bound
}
func (l *bindingLogger) WithFields(map[string]any) logger.Logger { return l }
func (l *bindingLogger) Info() logger.LogEvent                   { return nopEvent{} }
func (l *bindingLogger) Error() logger.LogEvent                  { return nopEvent{} }
func (l *bindingLogger) Debug() logger.LogEvent                  { return nopEvent{} }
func (l *bindingLogger) Warn() logger.LogEvent                   { return nopEvent{} }
func (l *bindingLogger) Fatal() logger.LogEvent                  { return nopEvent{} }

var _ logger.Logger = (*bindingLogger)(nil)

// setupTelemetry installs an in-memory span exporter and a test meter provider,
// both restored on cleanup. The tracking instruments are package singletons, so
// the reset brackets the test on both sides.
func setupTelemetry(t *testing.T) (*tracetest.InMemoryExporter, *obtest.TestMeterProvider) {
	t.Helper()

	prevTP := otel.GetTracerProvider()
	prevProp := otel.GetTextMapPropagator()
	prevMP := otel.GetMeterProvider()

	exporter := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.TraceContext{})

	mp := obtest.NewTestMeterProvider()
	otel.SetMeterProvider(mp)
	tracking.ResetMeterForTesting()

	t.Cleanup(func() {
		require.NoError(t, tp.Shutdown(context.Background()))
		otel.SetTracerProvider(prevTP)
		otel.SetTextMapPropagator(prevProp)
		otel.SetMeterProvider(prevMP)
		tracking.ResetMeterForTesting()
		require.NoError(t, mp.Shutdown(context.Background()))
	})

	return exporter, mp
}

// outcomes records every Result the lane's LogOutcome was handed.
type outcomes struct {
	seen []*Result
}

func (o *outcomes) log(res *Result) { o.seen = append(o.seen, res) }

// newRequest builds a Request with the classic lane's shape and a handler the
// test supplies. Fields a test cares about are overwritten by the caller.
func newRequest(log logger.Logger, rec *outcomes, handle Handler) *Request {
	return &Request{
		Carrier:     mapCarrier{},
		Destination: testQueue,
		BodySize:    12,
		Metrics:     tracking.AMQPConsumeAttributes("events", "orders.created", testQueue),
		Log:         log,
		Handle:      handle,
		LogOutcome:  rec.log,
	}
}

func succeedingHandler(context.Context, logger.Logger, string) error { return nil }

func assertAttribute(t *testing.T, attrs []attribute.KeyValue, key string, want any) {
	t.Helper()
	for _, attr := range attrs {
		if string(attr.Key) == key {
			assert.Equal(t, want, attr.Value.AsInterface(), "attribute %s", key)
			return
		}
	}
	t.Errorf("attribute %s not found", key)
}

func assertNoAttribute(t *testing.T, attrs []attribute.KeyValue, key string) {
	t.Helper()
	for _, attr := range attrs {
		if string(attr.Key) == key {
			t.Errorf("attribute %s should be absent, got %v", key, attr.Value.AsInterface())
		}
	}
}

func TestRunReportsSucceededForAHandlerThatReturnsNil(t *testing.T) {
	setupTelemetry(t)
	rec := &outcomes{}

	res := Run(context.Background(), newRequest(&bindingLogger{}, rec, func(context.Context, logger.Logger, string) error {
		time.Sleep(time.Millisecond)
		return nil
	}))

	require.NotNil(t, res)
	assert.Equal(t, Succeeded, res.Outcome)
	assert.NoError(t, res.Err)
	assert.Nil(t, res.Panic)
	assert.Nil(t, res.Stack)
	assert.GreaterOrEqual(t, res.Duration, time.Millisecond)
}

func TestRunReportsHandlerErrorAndCarriesTheError(t *testing.T) {
	setupTelemetry(t)
	rec := &outcomes{}
	handlerErr := errors.New("boom")

	res := Run(context.Background(), newRequest(&bindingLogger{}, rec, func(context.Context, logger.Logger, string) error {
		return handlerErr
	}))

	assert.Equal(t, HandlerError, res.Outcome)
	assert.Same(t, handlerErr, res.Err)
	assert.Nil(t, res.Panic)
}

func TestRunConvertsAPanicIntoAnError(t *testing.T) {
	setupTelemetry(t)
	rec := &outcomes{}

	var res *Result
	require.NotPanics(t, func() {
		res = Run(context.Background(), newRequest(&bindingLogger{}, rec, func(context.Context, logger.Logger, string) error {
			panic("handler exploded")
		}))
	})

	assert.Equal(t, Panicked, res.Outcome)
	require.Error(t, res.Err)
	// The classic lane's wording, now both lanes'.
	assert.Equal(t, "panic in message handler: handler exploded", res.Err.Error())
	assert.Equal(t, "handler exploded", res.Panic)
	assert.NotEmpty(t, res.Stack)
}

func TestRunBindsTheLoggerToThePerMessageContext(t *testing.T) {
	setupTelemetry(t)
	rec := &outcomes{}
	base := &bindingLogger{}

	var handleLog logger.Logger
	var handleCtx context.Context
	res := Run(context.Background(), newRequest(base, rec, func(ctx context.Context, log logger.Logger, _ string) error {
		handleCtx, handleLog = ctx, log
		return nil
	}))

	require.NotNil(t, base.bound)
	assert.Same(t, base.bound, res.Log, "the lane settles through the context-bound logger")
	assert.Same(t, base.bound, handleLog, "the handler adapter gets the same one")
	assert.Same(t, base.boundTo, handleCtx, "and it is bound to the context the handler ran under")
}

func TestRunCarriesTheCarrierTraceIDIntoTheContextAndTheResult(t *testing.T) {
	setupTelemetry(t)
	rec := &outcomes{}

	req := newRequest(&bindingLogger{}, rec, func(ctx context.Context, _ logger.Logger, traceID string) error {
		assert.Equal(t, testTraceID, traceID)
		got, ok := gobrickstrace.IDFromContext(ctx)
		assert.True(t, ok)
		assert.Equal(t, testTraceID, got)
		return nil
	})
	req.Carrier = mapCarrier{gobrickstrace.HeaderXRequestID: testTraceID}

	res := Run(context.Background(), req)

	assert.Equal(t, testTraceID, res.TraceID)
}

func TestRunGeneratesATraceIDWhenNoneTravelled(t *testing.T) {
	setupTelemetry(t)
	rec := &outcomes{}

	first := Run(context.Background(), newRequest(&bindingLogger{}, rec, succeedingHandler))
	second := Run(context.Background(), newRequest(&bindingLogger{}, rec, succeedingHandler))

	assert.NotEmpty(t, first.TraceID)
	assert.NotEqual(t, first.TraceID, second.TraceID)
}

func TestRunAcceptsANilCarrier(t *testing.T) {
	setupTelemetry(t)
	rec := &outcomes{}

	req := newRequest(&bindingLogger{}, rec, succeedingHandler)
	req.Carrier = nil

	var res *Result
	require.NotPanics(t, func() { res = Run(context.Background(), req) })
	assert.Equal(t, Succeeded, res.Outcome)
	assert.NotEmpty(t, res.TraceID)
}

func TestRunStartsARootSpanWhenOnlyAW3CHeaderTravelled(t *testing.T) {
	exporter, _ := setupTelemetry(t)
	rec := &outcomes{}

	req := newRequest(&bindingLogger{}, rec, succeedingHandler)
	req.Carrier = mapCarrier{gobrickstrace.HeaderTraceParent: testTraceParent}

	Run(context.Background(), req)

	spans := exporter.GetSpans()
	require.Len(t, spans, 1)
	// go-bricks extraction populates context VALUES, not the OTel span context,
	// so a consume span has always been a root span. Changing that would
	// re-parent every existing consumer span; ADR-068 keeps it out of scope.
	assert.False(t, spans[0].Parent.IsValid())
	tp, ok := gobrickstrace.ParentFromContext(rec.seen[0].Log.(*bindingLogger).boundTo)
	require.True(t, ok)
	assert.Equal(t, testTraceParent, tp)
}

func TestRunStartsOneConsumerSpanPerMessage(t *testing.T) {
	exporter, _ := setupTelemetry(t)
	rec := &outcomes{}

	req := newRequest(&bindingLogger{}, rec, succeedingHandler)
	req.SpanExtras = []attribute.KeyValue{attribute.String("messaging.rabbitmq.exchange", "events")}

	Run(context.Background(), req)

	spans := exporter.GetSpans()
	require.Len(t, spans, 1)
	span := spans[0]
	assert.Equal(t, testQueue+" receive", span.Name)
	assert.Equal(t, trace.SpanKindConsumer, span.SpanKind)
	assertAttribute(t, span.Attributes, string(semconv.MessagingSystemKey), "rabbitmq")
	assertAttribute(t, span.Attributes, string(semconv.MessagingOperationNameKey), "receive")
	assertAttribute(t, span.Attributes, string(semconv.MessagingDestinationNameKey), testQueue)
	assertAttribute(t, span.Attributes, string(semconv.MessagingMessageBodySizeKey), int64(12))
	assertAttribute(t, span.Attributes, "messaging.rabbitmq.exchange", "events")
	assert.Equal(t, codes.Unset, span.Status.Code)
}

func TestRunMarksTheSpanFailedForEveryFailingOutcome(t *testing.T) {
	tests := []struct {
		name    string
		handle  Handler
		wantMsg string
	}{
		{
			name:    "handler_error",
			handle:  func(context.Context, logger.Logger, string) error { return errors.New("nope") },
			wantMsg: "nope",
		},
		{
			name:    "panicked",
			handle:  func(context.Context, logger.Logger, string) error { panic("nope") },
			wantMsg: "panic in message handler: nope",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			exporter, _ := setupTelemetry(t)
			rec := &outcomes{}

			Run(context.Background(), newRequest(&bindingLogger{}, rec, tt.handle))

			spans := exporter.GetSpans()
			require.Len(t, spans, 1)
			assert.Equal(t, codes.Error, spans[0].Status.Code)
			assert.Equal(t, tt.wantMsg, spans[0].Status.Description)
			require.Len(t, spans[0].Events, 1)
			assert.Equal(t, "exception", spans[0].Events[0].Name)
		})
	}
}

func TestRunRecordsOneConsumeAtCompletion(t *testing.T) {
	_, mp := setupTelemetry(t)
	rec := &outcomes{}

	Run(context.Background(), newRequest(&bindingLogger{}, rec, func(context.Context, logger.Logger, string) error {
		time.Sleep(time.Millisecond)
		return nil
	}))

	rm := mp.Collect(t)
	obtest.AssertMetricValue(t, rm, "messaging.client.consumed.messages", int64(1))

	durationMetric := obtest.FindMetric(rm, "messaging.client.operation.duration")
	require.NotNil(t, durationMetric)
	histData, ok := durationMetric.Data.(metricdata.Histogram[float64])
	require.True(t, ok)
	require.Len(t, histData.DataPoints, 1)

	attrs := histData.DataPoints[0].Attributes.ToSlice()
	assertAttribute(t, attrs, "messaging.destination.name", "events:orders.created:orders")
	assertNoAttribute(t, attrs, "error.type")
}

func TestRunRecordsAFailedDeliveryWithItsErrorType(t *testing.T) {
	_, mp := setupTelemetry(t)
	rec := &outcomes{}

	Run(context.Background(), newRequest(&bindingLogger{}, rec, func(context.Context, logger.Logger, string) error {
		time.Sleep(time.Millisecond)
		return errors.New("nope")
	}))

	rm := mp.Collect(t)

	consumed := obtest.FindMetric(rm, "messaging.client.consumed.messages")
	require.NotNil(t, consumed)
	sumData, ok := consumed.Data.(metricdata.Sum[int64])
	require.True(t, ok)
	require.Len(t, sumData.DataPoints, 1)
	assert.Equal(t, int64(1), sumData.DataPoints[0].Value)
	assertAttribute(t, sumData.DataPoints[0].Attributes.ToSlice(), "error.type", "*errors.errorString")
}

func TestRunInstallsALeaseScopeAndDrainsItAfterTheHandler(t *testing.T) {
	tests := []struct {
		name   string
		handle func(released *bool) Handler
	}{
		{
			name: "succeeded",
			handle: func(released *bool) Handler {
				return func(ctx context.Context, _ logger.Logger, _ string) error {
					leasescope.Register(ctx, func() { *released = true })
					return nil
				}
			},
		},
		{
			name: "panicked",
			handle: func(released *bool) Handler {
				return func(ctx context.Context, _ logger.Logger, _ string) error {
					leasescope.Register(ctx, func() { *released = true })
					panic("after borrowing")
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setupTelemetry(t)
			rec := &outcomes{}
			released := false

			req := newRequest(&bindingLogger{}, rec, tt.handle(&released))
			req.LogOutcome = func(res *Result) {
				// The lane logs and settles while the lease is still held: a
				// handle borrowed by the handler must outlive the outcome line.
				assert.False(t, released, "the scope drained before the lane saw the outcome")
				rec.log(res)
			}

			Run(context.Background(), req)

			assert.True(t, released, "the scope must drain once the message is done")
		})
	}
}

func TestRunEndsTheSpanBeforeItDrainsTheLeaseScope(t *testing.T) {
	setupTelemetry(t)
	rec := &outcomes{}

	var order []string
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(onEndRecorder{onEnd: func() {
		order = append(order, "span-end")
	}}))
	otel.SetTracerProvider(tp)
	t.Cleanup(func() { require.NoError(t, tp.Shutdown(context.Background())) })

	Run(context.Background(), newRequest(&bindingLogger{}, rec, func(ctx context.Context, _ logger.Logger, _ string) error {
		leasescope.Register(ctx, func() { order = append(order, "release") })
		return nil
	}))

	assert.Equal(t, []string{"span-end", "release"}, order)
}

// onEndRecorder is a span processor that only reports that a span ended, so the
// pipeline's defer order can be asserted.
type onEndRecorder struct {
	onEnd func()
}

func (onEndRecorder) OnStart(context.Context, sdktrace.ReadWriteSpan) {}
func (p onEndRecorder) OnEnd(sdktrace.ReadOnlySpan)                   { p.onEnd() }
func (onEndRecorder) Shutdown(context.Context) error                  { return nil }
func (onEndRecorder) ForceFlush(context.Context) error                { return nil }

func TestRunHandsTheLaneOneOutcomePerMessage(t *testing.T) {
	tests := []struct {
		name   string
		handle Handler
		want   Outcome
	}{
		{name: "succeeded", handle: succeedingHandler, want: Succeeded},
		{
			name:   "handler_error",
			handle: func(context.Context, logger.Logger, string) error { return errors.New("nope") },
			want:   HandlerError,
		},
		{
			name:   "panicked",
			handle: func(context.Context, logger.Logger, string) error { panic("nope") },
			want:   Panicked,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setupTelemetry(t)
			rec := &outcomes{}

			res := Run(context.Background(), newRequest(&bindingLogger{}, rec, tt.handle))

			require.Len(t, rec.seen, 1)
			assert.Same(t, res, rec.seen[0], "the lane logs and settles the same Result")
			assert.Equal(t, tt.want, res.Outcome)
			assert.NotEmpty(t, res.TraceID)
			assert.NotNil(t, res.Log)
		})
	}
}
```

Add `"go.opentelemetry.io/otel/sdk/metric/metricdata"` to the import block — the two metric tests use it.

- [ ] **Step 2: Run the suite to verify it fails**

Run: `go test ./messaging/internal/delivery/`

Expected: FAIL — the package does not exist yet:

```text
# github.com/gaborage/go-bricks/messaging/internal/delivery [github.com/gaborage/go-bricks/messaging/internal/delivery.test]
messaging/internal/delivery/delivery_test.go:...: undefined: Run
messaging/internal/delivery/delivery_test.go:...: undefined: Request
messaging/internal/delivery/delivery_test.go:...: undefined: Result
messaging/internal/delivery/delivery_test.go:...: undefined: Handler
messaging/internal/delivery/delivery_test.go:...: undefined: Succeeded
FAIL	github.com/gaborage/go-bricks/messaging/internal/delivery [build failed]
```

- [ ] **Step 3: Write the pipeline**

Create `messaging/internal/delivery/delivery.go`:

```go
// Package delivery runs the delivery pipeline both messaging lanes share:
// everything that happens to one consumed message between "bytes arrived" and
// "outcome recorded" — trace extraction from the lane's carrier, the consumer
// span, the per-message lease scope, handler invocation, panic-to-error, one
// consumed record at completion, and the lane's own outcome line.
//
// Settlement is not here. Turning an outcome into a broker action — ack or
// nack-without-requeue on the classic lane, commit-offset or skip on the streams
// lane — is the lane's, and so is the policy behind it.
package delivery

import (
	"context"
	"fmt"
	"runtime/debug"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	semconv "go.opentelemetry.io/otel/semconv/v1.32.0"
	"go.opentelemetry.io/otel/trace"

	"github.com/gaborage/go-bricks/internal/leasescope"
	"github.com/gaborage/go-bricks/logger"
	"github.com/gaborage/go-bricks/messaging/internal/tracking"
	gobrickstrace "github.com/gaborage/go-bricks/trace"
)

const (
	// tracerName is the one instrumentation scope both lanes report under.
	tracerName = "go-bricks/messaging"

	spanOperationReceive = "receive"
	messagingSystem      = "rabbitmq"

	panicMessage = "panic in message handler: %v"
)

// Outcome names how one delivery ended.
type Outcome int

// The three outcomes of one delivery. Succeeded is the zero value, so a result
// is built as a success and only overwritten when the handler says otherwise.
const (
	Succeeded Outcome = iota
	HandlerError
	Panicked
)

// Handler invokes the module's handler for one message. The pipeline owns the
// per-message context, so it hands over the two things derived from it that a
// lane needs before its own handler runs: the context-bound logger and the
// trace ID.
type Handler func(ctx context.Context, log logger.Logger, traceID string) error

// Request is what one lane hands the pipeline for one message. Handle and
// LogOutcome are required.
type Request struct {
	// Carrier is where the trace context travelled: AMQP 0.9.1 headers on the
	// classic lane, AMQP 1.0 application properties on the streams lane.
	Carrier gobrickstrace.HeaderAccessor

	// Destination is the queue or stream the message arrived on — the span
	// name's prefix and messaging.destination.name.
	Destination string

	// BodySize is the payload length for messaging.message.body.size.
	BodySize int

	// SpanExtras are the lane's span attributes, set after the four both lanes
	// share.
	SpanExtras []attribute.KeyValue

	// Metrics identifies this message on the receive instruments.
	Metrics tracking.ConsumeAttributes

	// Log is the consumer's logger. The pipeline binds it to the per-message
	// context and hands the bound one back on Result.
	Log logger.Logger

	Handle Handler

	// LogOutcome writes the lane's own line for the finished delivery. It runs
	// while the span is open and the lease scope still holds, so a handle the
	// handler borrowed outlives the line.
	LogOutcome func(*Result)
}

// Result is what the lane settles on. Panic and Stack are set only when Outcome
// is Panicked; Err is nil only when Outcome is Succeeded. A pointer, not a
// value, so the lane's LogOutcome does not copy it per message.
type Result struct {
	Outcome  Outcome
	Err      error
	Duration time.Duration
	TraceID  string
	Log      logger.Logger
	Panic    any
	Stack    []byte
}

// Run puts one message through the delivery pipeline and returns the outcome for
// the lane to settle. It never returns nil and never panics: a handler panic
// becomes a Panicked result carrying the recovered value, its stack, and an
// error.
func Run(ctx context.Context, req *Request) *Result {
	start := time.Now()

	msgCtx := gobrickstrace.ExtractFromHeaders(ctx, req.Carrier)

	msgCtx, span := otel.Tracer(tracerName).Start(msgCtx, req.Destination+" "+spanOperationReceive,
		trace.WithSpanKind(trace.SpanKindConsumer))
	span.SetAttributes(spanAttributes(req)...)

	// Install the per-message lease scope (ADR-032): per-tenant handles borrowed
	// via deps.DB/Cache/Messaging while this message is handled (including inbox
	// ProcessOnce, which runs inside the handler and inherits msgCtx) are
	// released when the message is done, so a handle evicted mid-handling is not
	// closed under it. Deferred before span.End so the span closes first and
	// ReleaseAll runs last.
	msgCtx, scope := leasescope.Install(msgCtx)
	defer scope.ReleaseAll()
	defer span.End()

	log := req.Log.WithContext(msgCtx)
	traceID := gobrickstrace.EnsureTraceID(msgCtx)

	res := invoke(msgCtx, log, traceID, req.Handle)
	res.Duration = time.Since(start)
	res.TraceID = traceID
	res.Log = log

	req.LogOutcome(res)

	if res.Err != nil {
		span.RecordError(res.Err)
		span.SetStatus(codes.Error, res.Err.Error())
	}

	tracking.RecordConsume(msgCtx, req.Metrics, res.Duration, res.Err)

	return res
}

// spanAttributes renders the four attributes both lanes set, then the lane's own.
func spanAttributes(req *Request) []attribute.KeyValue {
	attrs := make([]attribute.KeyValue, 0, 4+len(req.SpanExtras))
	attrs = append(attrs,
		attribute.String(string(semconv.MessagingSystemKey), messagingSystem),
		semconv.MessagingOperationName(spanOperationReceive),
		semconv.MessagingDestinationName(req.Destination),
		semconv.MessagingMessageBodySize(req.BodySize),
	)
	return append(attrs, req.SpanExtras...)
}

// invoke runs the lane's handler, turning a panic into an error so one tail
// logs, marks and records every outcome.
func invoke(ctx context.Context, log logger.Logger, traceID string, handle Handler) (res *Result) {
	res = &Result{}

	defer func() {
		if recovered := recover(); recovered != nil {
			res.Outcome = Panicked
			res.Panic = recovered
			res.Stack = debug.Stack()
			res.Err = fmt.Errorf(panicMessage, recovered)
		}
	}()

	if err := handle(ctx, log, traceID); err != nil {
		res.Outcome = HandlerError
		res.Err = err
	}
	return res
}
```

- [ ] **Step 4: Run the suite to verify it passes**

Run: `go test ./messaging/internal/delivery/ -v`

Expected: every test `--- PASS`, then `ok  github.com/gaborage/go-bricks/messaging/internal/delivery`.

If `TestRunStartsARootSpanWhenOnlyAW3CHeaderTravelled` fails on `Parent.IsValid()`, an OTel propagator was wired into `Run` — remove it; parenting the consume span on the wire context is explicitly out of scope (see "Design decisions this plan locks in").

- [ ] **Step 5: Run the whole tree to verify nothing else moved**

Run: `go test -race ./messaging/... && go vet ./messaging/...`

Expected: `ok` for every package and no vet output. Nothing outside `messaging/internal/delivery` was edited in this task, so any failure here is a merge artifact, not this change.

- [ ] **Step 6: `make check`**

Run: `pwd && git branch --show-current && make check`

Expected: prints the repo root, `feature/messaging-delivery-pipeline`, then exits 0. Watch for: `gci` grouping in the two new files (standard → third party → `prefix(github.com/gaborage/go-bricks)`); `revive`'s `exported` rule wanting a doc comment on every exported identifier, including the `const` block (a comment on the block satisfies it); `gocritic`'s `hugeParam` — if it fires, a value parameter slipped back in where the plan specifies a pointer.

- [ ] **Step 7: Commit**

```bash
cat > /tmp/delivery-pipeline-msg.txt <<'EOF'
feat(messaging): add the delivery pipeline both lanes will share

Both messaging lanes implement span -> invoke -> recover -> record
independently: the classic lane across seven functions in registry.go plus
an exported StartConsumeSpan, the streams lane in runner.deliver/invoke.
The copies drifted -- streams never extracts the trace context its own
publisher injects, streams installs no per-message lease scope, and the
consumed counter is stamped at receive on one lane and at completion on the
other -- and #940/#951/#954 rewrote processMessage three times in a month
without reaching the streams copy.

Add messaging/internal/delivery: Run(ctx, *Request) *Result owns the
per-message context (carrier extraction, consumer span, lease scope,
EnsureTraceID), invokes the lane's handler, converts a panic into an error,
records exactly one tracking.RecordConsume at completion, and calls the
lane's own LogOutcome closure. Outcomes are succeeded, handlerError and
panicked.

Settlement stays with the lane: the pipeline makes no broker call, so
"never requeue" and "commit only after success" do not move. No lane runs
on it yet -- the classic lane migrates next, the streams lane after it.
EOF
git add messaging/internal/delivery/
git commit -F /tmp/delivery-pipeline-msg.txt
```

### Task 4: Gates for PR2b (controller only)

- [ ] **Step 1: `make check`, backgrounded**

```bash
make check
```

`run_in_background: true`. Expected: exits 0.

- [ ] **Step 2: `/simplify`**

Likely targets: `spanAttributes`'s two-step append (it is one allocation by design — the capacity is exact; do not let it become a literal plus repeated appends), and the `nopEvent`/`bindingLogger` doubles in the test file (they are the minimum the `logger.Logger` and `logger.LogEvent` interfaces admit). If it changes code, re-run `make check`.

- [ ] **Step 3: `/security-audit`**

Focus for this diff: the pipeline logs nothing itself, so no field it owns can leak a payload — confirm that stays true (`Result.Stack` is handed to the lane, never written by the pipeline); and confirm `recover()` cannot swallow a runtime error the process should die on — it recovers only around `handle`, exactly as `registry.processMessage` and `streams.consumerRunner.invoke` already do. If it changes code, re-run `make check`.

- [ ] **Step 4: `/code-review` (CodeRabbit)**

Must see the final diff. Expect two questions: (a) "why no nil-guard on `Handle`/`LogOutcome`" — answer from "Design decisions this plan locks in"; (b) "why is a new package added with no caller" — answer that the stack splits at ~400 LoC and the classic lane migrates in the next PR, which is reviewable only because the pipeline is already reviewed on its own.

- [ ] **Step 5: `make mutate`, backgrounded, after committing**

```bash
make mutate
```

`run_in_background: true`, and **commit first**. Lines most likely to survive and their killers: `res.Err != nil` → `== nil` is killed by `TestRunMarksTheSpanFailedForEveryFailingOutcome` (both cases) and by `TestRunStartsOneConsumerSpanPerMessage`'s `codes.Unset`; `recovered != nil` is killed by `TestRunConvertsAPanicIntoAnError`; the `4+len(req.SpanExtras)` capacity is not observable and gremlins does not mutate it.

- [ ] **Step 6: Push and open PR2b**

Push, open against `feature/messaging-consume-tracking`, title `feat(messaging): add the delivery pipeline both lanes will share`, body:

```markdown
## What

Both lanes implemented span → invoke → recover → record independently, and the copies drifted (no trace extraction and no lease scope on the streams lane; the consumed counter at receive on one lane and at completion on the other). `messaging/internal/delivery` now owns that body: `Run(ctx, *Request) *Result` handles the per-message context, the handler, panic-to-error, one `RecordConsume`, and the lane's own outcome line.

## Impact

None yet. Nothing exported changes and no lane runs on the pipeline in this PR — the classic lane migrates in the next one, the streams lane after it. Settlement stays lane-side, so no broker call moved.

## Verification

CI gates only.
```

Post `@coderabbitai review` after opening — CodeRabbit skips PRs whose base is not `main`.

---

## PR2c — the AMQP lane on the pipeline

**Branch:** `feature/messaging-amqp-on-pipeline`, cut from `feature/messaging-delivery-pipeline`.

**Constraints reminder:** camelCase test names, snake_case table cases · `git commit -F <file>`, never `--no-gpg-sign` · `make check` before **every** commit (there are three), with `git branch --show-current` printing `feature/messaging-amqp-on-pipeline` · no `//nolint` (one is deleted) · comments bare-minimum · `messaging/streams` is not edited in this PR · the alloc guard `TestRegistryProcessMessagePerDeliveryLoggerAllocs` must stay under `42.0` · **no compat shim**: `StartConsumeSpan` is deleted, not deprecated · ADR-033 and ADR-063 untouched, publisher-side injection untouched, concurrency shape untouched.

### Task 5: The classic lane on the pipeline

**Files:**

- Modify: `messaging/registry.go` (imports `3-20`, `processMessage` `758-848`, `handlePanicRecovery` `889-916` deleted, new `consumeSpanExtras` / `logProcessing` / `logOutcome` / `ackMessage`)
- Modify: `messaging/registry_test.go` (three test edits, three new tests)

**Interfaces:**

- Consumes from Task 3: `pipeline.Run`, `pipeline.Request`, `pipeline.Result`, `pipeline.Handler`, `pipeline.Succeeded` / `HandlerError` / `Panicked` (imported as `pipeline "github.com/gaborage/go-bricks/messaging/internal/delivery"` — the package name collides with the `delivery *amqp.Delivery` parameter that appears on every function in this file).
- Consumes from Task 1: `tracking.AMQPConsumeAttributes`.
- Consumes, **unchanged**: `amqpHeaderAccessor` (`amqp_client.go:1185`), `buildFailureLogEvent` (`registry.go:865`), `nackMessage` (`registry.go:850`), `ConsumerDeclaration`, `Handler.Handle`.
- Produces, package-internal:
  - `func consumeSpanExtras(delivery *amqp.Delivery) []attribute.KeyValue`
  - `func logProcessing(log logger.Logger, traceID string, delivery *amqp.Delivery)`
  - `func (r *Registry) logOutcome(res *pipeline.Result, consumer *ConsumerDeclaration, delivery *amqp.Delivery)`
  - `func (r *Registry) ackMessage(delivery *amqp.Delivery, autoAck bool, log logger.Logger, traceID string)`
- **Unchanged signature:** `func (r *Registry) processMessage(ctx context.Context, consumer *ConsumerDeclaration, delivery *amqp.Delivery, log logger.Logger)`. Twenty-eight test call sites across `registry_test.go` and `amqp_test.go` depend on it and none of them is touched by this task.

**Estimated LoC:** ~205 changed (registry.go +112/−93, registry_test.go +85/−12).

- [ ] **Step 1: Write the failing tests — the span extras and the outcome line, as their own units**

Append to `messaging/registry_test.go` (after `TestRegistryProcessMessageSkipsDebugFieldBuildWhenDisabled`, `:2984-3020`):

```go
// ===== Delivery-pipeline lane adapter tests (ADR-068) =====

func TestConsumeSpanExtrasCarryEveryDeliveryField(t *testing.T) {
	extras := consumeSpanExtras(&amqp.Delivery{
		Exchange:      testExchangeName,
		RoutingKey:    testRoutingKey,
		MessageId:     testMessageID,
		CorrelationId: "amqp-corr-1",
	})

	assertAttribute(t, extras, "messaging.rabbitmq.exchange", testExchangeName)
	assertAttribute(t, extras, "messaging.rabbitmq.destination.routing_key", testRoutingKey)
	assertAttribute(t, extras, string(semconv.MessagingMessageIDKey), testMessageID)
	assertAttribute(t, extras, string(semconv.MessagingMessageConversationIDKey), "amqp-corr-1")
}

func TestConsumeSpanExtrasOmitTheFieldsTheDeliveryDidNotCarry(t *testing.T) {
	assert.Empty(t, consumeSpanExtras(&amqp.Delivery{}),
		"a delivery with no exchange, routing key, message id or correlation id adds no span attribute")
}

func TestRegistryProcessMessageSpanCarriesEveryDeliveryAttribute(t *testing.T) {
	exporter, cleanup := setupTestTracing(t)
	defer cleanup()

	registry := NewRegistry(&simpleMockAMQPClient{}, &stubLogger{})
	consumer := &ConsumerDeclaration{
		Queue:     testQueueName,
		EventType: testEventType,
		Handler:   &countingTestHandler{},
		AutoAck:   false,
	}
	delivery := &amqp.Delivery{
		MessageId:     testMessageID,
		CorrelationId: "amqp-corr-1",
		RoutingKey:    testRoutingKey,
		Exchange:      testExchangeName,
		DeliveryTag:   123,
		Body:          []byte(testMessageBody),
		Headers:       amqp.Table{},
		Acknowledger:  &mockAcknowledger{},
	}

	registry.processMessage(context.Background(), consumer, delivery, &stubLogger{})

	spans := exporter.GetSpans()
	require.Len(t, spans, 1)
	span := spans[0]
	assert.Equal(t, testQueueName+" receive", span.Name)
	assert.Equal(t, trace.SpanKindConsumer, span.SpanKind)
	assertAttribute(t, span.Attributes, string(semconv.MessagingSystemKey), "rabbitmq")
	assertAttribute(t, span.Attributes, string(semconv.MessagingOperationNameKey), "receive")
	assertAttribute(t, span.Attributes, string(semconv.MessagingDestinationNameKey), testQueueName)
	assertAttribute(t, span.Attributes, string(semconv.MessagingMessageBodySizeKey), int64(len(testMessageBody)))
	assertAttribute(t, span.Attributes, "messaging.rabbitmq.exchange", testExchangeName)
	assertAttribute(t, span.Attributes, "messaging.rabbitmq.destination.routing_key", testRoutingKey)
	assertAttribute(t, span.Attributes, string(semconv.MessagingMessageIDKey), testMessageID)
	assertAttribute(t, span.Attributes, string(semconv.MessagingMessageConversationIDKey), "amqp-corr-1")
}
```

`assertAttribute` already exists in `messaging/otel_test.go:...` (same package) and takes `[]attribute.KeyValue`. Add `semconv "go.opentelemetry.io/otel/semconv/v1.32.0"` to `registry_test.go`'s import block.

- [ ] **Step 2: Run the new tests to verify they fail**

Run: `go test ./messaging/ -run 'TestConsumeSpanExtras|TestRegistryProcessMessageSpanCarriesEveryDeliveryAttribute'`

Expected: FAIL — build failure, `undefined: consumeSpanExtras`.

- [ ] **Step 3: Rewrite `processMessage` as the lane adapter**

In `messaging/registry.go`, replace the import block (`3-20`) with:

```go
import (
	"context"
	"errors"
	"fmt"
	"maps"
	"sync"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
	"go.opentelemetry.io/otel/attribute"
	semconv "go.opentelemetry.io/otel/semconv/v1.32.0"

	"github.com/gaborage/go-bricks/logger"
	pipeline "github.com/gaborage/go-bricks/messaging/internal/delivery"
	"github.com/gaborage/go-bricks/messaging/internal/tracking"
)
```

`runtime/debug`, `go.opentelemetry.io/otel/codes`, `go.opentelemetry.io/otel/trace`, `github.com/gaborage/go-bricks/internal/leasescope` and `github.com/gaborage/go-bricks/trace` all leave: every use of them moved into the pipeline.

Replace `processMessage` (`758-848`) with:

```go
// processMessage runs one delivery through the delivery pipeline and settles it.
// Settlement is this lane's, not the pipeline's: ack on success, negative
// acknowledgment WITHOUT requeue on a handler error or a panic, which prevents
// infinite retry loops. Queues declared with x-dead-letter-exchange route the
// nacked message to that exchange (retained only if a binding delivers it to a
// queue); queues without one drop it (logged by logOutcome).
// DeclareQueueWithDLQ declares that full route in one call.
func (r *Registry) processMessage(ctx context.Context, consumer *ConsumerDeclaration, delivery *amqp.Delivery, log logger.Logger) {
	res := pipeline.Run(ctx, &pipeline.Request{
		Carrier:     &amqpHeaderAccessor{headers: delivery.Headers},
		Destination: consumer.Queue,
		BodySize:    len(delivery.Body),
		SpanExtras:  consumeSpanExtras(delivery),
		Metrics:     tracking.AMQPConsumeAttributes(delivery.Exchange, delivery.RoutingKey, consumer.Queue),
		Log:         log,
		Handle: func(msgCtx context.Context, msgLog logger.Logger, traceID string) error {
			logProcessing(msgLog, traceID, delivery)
			return consumer.Handler.Handle(msgCtx, delivery)
		},
		LogOutcome: func(res *pipeline.Result) {
			r.logOutcome(res, consumer, delivery)
		},
	})

	if res.Outcome == pipeline.Succeeded {
		r.ackMessage(delivery, consumer.AutoAck, res.Log, res.TraceID)
		return
	}
	r.nackMessage(delivery, consumer.AutoAck, res.Log, res.TraceID)
}

// consumeSpanExtras renders this lane's span attributes, on top of the four the
// pipeline sets for both lanes. A field the delivery did not carry is omitted
// rather than reported empty, which is what the receive span has always done.
func consumeSpanExtras(delivery *amqp.Delivery) []attribute.KeyValue {
	extras := make([]attribute.KeyValue, 0, 4)
	if delivery.Exchange != "" {
		extras = append(extras, attribute.String("messaging.rabbitmq.exchange", delivery.Exchange))
	}
	if delivery.RoutingKey != "" {
		extras = append(extras, semconv.MessagingRabbitMQDestinationRoutingKey(delivery.RoutingKey))
	}
	if delivery.MessageId != "" {
		extras = append(extras, semconv.MessagingMessageID(delivery.MessageId))
	}
	if delivery.CorrelationId != "" {
		extras = append(extras, semconv.MessagingMessageConversationID(delivery.CorrelationId))
	}
	return extras
}

// logProcessing writes the per-delivery DEBUG line. The whole field chain is
// skipped when the event is dropped: DEBUG is below WarnLevel, so the adapter's
// Msg -> trackSeverity hook is a no-op and skipping Msg changes nothing.
func logProcessing(log logger.Logger, traceID string, delivery *amqp.Delivery) {
	dbg := log.Debug()
	if !dbg.Enabled() {
		return
	}
	dbg.Str("correlation_id", traceID).
		Str("message_id", delivery.MessageId).
		Str("routing_key", delivery.RoutingKey).
		Str("exchange", delivery.Exchange).
		Uint64("delivery_tag", delivery.DeliveryTag).
		Int("body_size", len(delivery.Body)).
		Msg("Processing message")
}

// logOutcome writes this lane's line for a finished delivery.
func (r *Registry) logOutcome(res *pipeline.Result, consumer *ConsumerDeclaration, delivery *amqp.Delivery) {
	switch res.Outcome {
	case pipeline.Succeeded:
		res.Log.Info().
			Str("correlation_id", res.TraceID).
			Str("message_id", delivery.MessageId).
			Dur("processing_time", res.Duration).
			Msg("Message processed successfully")
	case pipeline.HandlerError:
		r.buildFailureLogEvent(res.Log, res.TraceID, delivery, consumer, res.Duration).
			Err(res.Err).
			Msg("Message processing failed - discarding without requeue")
	case pipeline.Panicked:
		r.buildFailureLogEvent(res.Log, res.TraceID, delivery, consumer, res.Duration).
			Interface("panic", res.Panic).
			Bytes("stack", res.Stack).
			Msg("Panic recovered in message handler - discarding without requeue")
	}
}

// ackMessage acknowledges a handled message.
// Logs any ack errors but does not propagate them (robustness over strict error handling).
func (r *Registry) ackMessage(delivery *amqp.Delivery, autoAck bool, log logger.Logger, traceID string) {
	if autoAck {
		return // No manual ack/nack needed
	}
	if err := delivery.Ack(false); err != nil {
		log.Error().
			Str("correlation_id", traceID).
			Err(err).
			Uint64("delivery_tag", delivery.DeliveryTag).
			Msg("Failed to ack message")
	}
}
```

Then delete `handlePanicRecovery` (`889-916`) whole, including its doc comment. `nackMessage` (`850-863`) and `buildFailureLogEvent` (`865-887`) are **not** edited — the two-`correlation_id` comment on the latter stays exactly as written.

- [ ] **Step 4: Run the new tests to verify they pass**

Run: `go test ./messaging/ -run 'TestConsumeSpanExtras|TestRegistryProcessMessageSpanCarriesEveryDeliveryAttribute' -v`

Expected: three `--- PASS` lines then `ok  github.com/gaborage/go-bricks/messaging`.

- [ ] **Step 5: Update the three consume tests whose meaning moved**

In `messaging/registry_test.go`:

In `TestRegistryProcessMessageRecordsConsumeMetricsOnError` (`2396-2441`), replace the comment and assertion at `2427-2429`:

```go
	// The counter is stamped at completion now (ADR-068), so a failed delivery
	// is still counted once and carries the error type that failed it.
	consumed := obtest.FindMetric(rm, "messaging.client.consumed.messages")
	require.NotNil(t, consumed)
	sumData, ok := consumed.Data.(metricdata.Sum[int64])
	require.True(t, ok)
	require.Len(t, sumData.DataPoints, 1)
	assert.Equal(t, int64(1), sumData.DataPoints[0].Value)
	assertAttribute(t, sumData.DataPoints[0].Attributes.ToSlice(), "error.type", "*errors.errorString")
```

In `TestRegistryProcessMessageCountsExactlyOncePerDelivery` (`2443-2481`), nothing changes but the reason: add above the counter assertion:

```go
	// Two deliveries, two counts — recorded at completion, once each.
```

In `TestRegistryProcessMessagePerDeliveryLoggerAllocs` (`2905-2935`), leave the ceiling at `42.0` and extend the comment at `2932-2934` with the third measurement taken in Step 6:

```go
	// Ceiling fixed at 42.0 (advisor resolution 2026-08-09): measured BEFORE =
	// 47.0, AFTER = 38.0 allocs/op — fails the old per-delivery WithFields
	// layer, passes the new per-event stamps with headroom. On the delivery
	// pipeline (ADR-068) = <MEASURED> allocs/op.
```

- [ ] **Step 6: Run the whole package and record the allocation number**

Run: `go test ./messaging/ -run TestRegistryProcessMessagePerDeliveryLoggerAllocs -v -count=1`

Expected: `processMessage allocs/op = NN.N` in the log output, and PASS. Put that number into the comment from Step 5.

The number should be at or below 38.0: the pipeline **removes** the receive-time attribute build (a `formatDestinationName` `fmt.Sprintf`, a `[]attribute.KeyValue`, and an `attribute.Set` sort inside `metric.WithAttributes`) and the seven-variable `handlePanicRecovery` defer closure, and **adds** the `Request`, the `Result`, and two lane closures. If the measured value is **above 42.0**, do not raise the ceiling and do not `//nolint` it: report the number and stop — the controller decides.

Then run: `go test -race ./messaging/...`

Expected: `ok` for every package. In particular these must pass **unedited**, which is the proof that the lane adapter emits what `processMessage` emitted: `TestRegistryProcessMessageSuccess`, `…HandlerError`, `…AutoAck`, `…AckError`, `…NackError`, `…HandlerPanic`, `…HandlerPanicNack`, `…HandlerPanicLogging`, `…HandlerPanicWithAutoAck`, `TestRegistryHandleMessagesContinuesAfterPanic`, `TestRegistryMultipleConsumersPanicIsolation`, `TestRegistryProcessMessageRecordsConsumeMetricsOnSuccess`, `TestRegistryProcessMessageStartsReceiveSpan`, `TestRegistryProcessMessagePanicMarksSpanError`, `TestRegistryProcessMessageStampsCorrelationIDOnSuccessLines`, `TestRegistryProcessMessageCorrelationIDIsStableAcrossLines`, `TestRegistryProcessMessageFailureLineKeepsBothCorrelationIDs`, `TestRegistryProcessMessagePanicLineKeepsBothCorrelationIDs`, `TestRegistryProcessMessageLogsDebugFieldsWhenEnabled`, `TestRegistryProcessMessageSkipsDebugFieldBuildWhenDisabled`, `TestRegistryProcessMessageAutoAckGuard`.

- [ ] **Step 7: `make check` and commit**

Run: `pwd && git branch --show-current && make check`

Expected: exits 0. Watch for `dupl` on `ackMessage`/`nackMessage` — both are under the 100-token threshold, but if it fires, fold them into one `settle(delivery, autoAck, log, traceID, apply func() error, failMsg string)` rather than suppressing it.

```bash
cat > /tmp/amqp-on-pipeline-msg.txt <<'EOF'
refactor(messaging): run the AMQP lane on the delivery pipeline

processMessage carried the whole per-message body itself -- span, lease
scope, bound logger, trace ID, panic recovery, metric, log lines -- spread
across processMessage, handlePanicRecovery, buildFailureLogEvent and the
exported StartConsumeSpan.

It becomes the lane adapter it should be: build the Request (carrier, queue
as destination, body size, this lane's span extras, this lane's metric
bundle, the handler, the outcome line), call delivery.Run, and settle on the
Result -- ack on Succeeded, nack WITHOUT requeue on HandlerError and
Panicked. handlePanicRecovery is gone; buildFailureLogEvent and nackMessage
are untouched, so the failure lines keep both correlation_id stamps in the
order a parser reads them.

Every telemetry value is preserved: the same span name, kind and eight
attributes, the same four log lines with the same fields, the same
destination strings. The one thing that moves is the consumed counter --
recorded at completion now, with error.type on a failure, instead of at
receive with none. The lane's ack and nack now run after the span closes and
the lease scope drains, since settlement is lane-side.
EOF
git add messaging/registry.go messaging/registry_test.go
git commit -F /tmp/amqp-on-pipeline-msg.txt
```

### Task 6: The deletions, and the tests that move with them

**Files:**

- Modify: `messaging/amqp_client.go` (delete `operationReceive` `138`, `StartConsumeSpan` `1202-1256`)
- Modify: `messaging/registry.go` (delete `amqpDeliveryAccessor` `918-933`)
- Modify: `messaging/internal/tracking/metrics.go` (delete `RecordAMQPConsumeMetrics`, `RecordAMQPConsumeCompletion`, `deliveryAttributes`)
- Modify: `messaging/internal/tracking/metrics_test.go` (delete three tests, drop the `amqp` import)
- Modify: `messaging/otel_test.go` (delete four tests)
- Modify: `messaging/registry_test.go` (move one test, delete one)
- Modify: `messaging/amqp_test.go` (retype five accessor sites, receive one moved test)

**Interfaces:** consumes only what Task 5 produced. Produces nothing new.

**Estimated LoC:** ~325 changed (−55 `amqp_client.go`, −16 `registry.go`, −60 `tracking/metrics.go`, −95 `tracking/metrics_test.go`, −112 `otel_test.go`, ±70 `registry_test.go` + `amqp_test.go`).

**Test disposition — every test that moves or dies, with its named replacement:**

| Test (file:line) | Disposition | Replacement |
| --- | --- | --- |
| `TestStartConsumeSpanCreatesSpanWithAttributes` (`otel_test.go:263`) | Deleted | `TestRegistryProcessMessageSpanCarriesEveryDeliveryAttribute` (Task 5 Step 1) — the same eight attributes, asserted through the only caller that survives |
| `TestStartConsumeSpanExtractsTraceContext` (`otel_test.go:303`) | Deleted | `TestRunCarriesTheCarrierTraceIDIntoTheContextAndTheResult` + `TestRunStartsARootSpanWhenOnlyAW3CHeaderTravelled` (Task 3), which assert what this one only claimed in a comment |
| `TestConsumeSpanWithMinimalDelivery` (`otel_test.go:341`) | Deleted | `TestConsumeSpanExtrasOmitTheFieldsTheDeliveryDidNotCarry` (Task 5 Step 1) |
| `TestStartConsumeSpanNilDelivery` (`otel_test.go:364`) | Deleted, **no replacement, by design** | The nil-delivery branch existed only for external callers of the exported function; `Registry.worker` always sends `&d` (`registry.go:750`), so the branch is unreachable from the lane and is deleted with it |
| `TestRecordAMQPConsumeMetricsSuccess` (`tracking/metrics_test.go:190`) | Deleted | `TestRecordConsumeCarriesAMQPAttributes` (Task 1) |
| `TestRecordAMQPConsumeMetricsZeroDuration` (`tracking/metrics_test.go:238`) | Deleted | `TestRecordConsumeZeroDurationSkipsHistogram` (Task 1) |
| `TestRecordAMQPConsumeCompletion` (`tracking/metrics_test.go:275`) | Deleted | `TestRecordConsumeCarriesAMQPAttributes`, `TestRecordConsumeCountsAFailedDeliveryWithItsErrorType`, `TestRecordConsumeZeroDurationSkipsHistogram` (Task 1). Its `assert.Nil(t, obtest.FindMetric(rm, metricMessagesConsumed))` case has **no** replacement on purpose: "the completion recorder must never touch the counter" is precisely the invariant ADR-068 reverses |
| `TestAmqpDeliveryAccessorGet` (`registry_test.go:695`) | Moved and retyped | `TestAmqpHeaderAccessorGet` in `amqp_test.go` — the same five table cases against `amqpHeaderAccessor`, the accessor the consume path actually uses now |
| `TestAmqpDeliveryAccessorSet` (`registry_test.go:751`) | Deleted | `TestAMQPHeaderAccessorNilSafety` (`amqp_test.go:181`). The read-only `Set` no-op it pinned belonged to the accessor being deleted; the surviving accessor's `Set` writes, and that is what the publish path needs |
| Five `&amqpDeliveryAccessor{…}` sites in `amqp_test.go` (`95`, `120`, `206`, `315`, `340`) | Retyped in place | Same tests, `&amqpHeaderAccessor{…}`. `TestAMQPCentralizedArchitectureConsistentProcessing` keeps its name and gains one comment: with one accessor left, "both sides use the same centralized logic" is now structural |

- [ ] **Step 1: Delete the exported span factory and the receive-time metric path**

In `messaging/amqp_client.go`: delete `StartConsumeSpan` and its doc comment (`1202-1256`) — which deletes the file's only `//nolint:spancheck` — and the now-unused `operationReceive` const (`138`). `amqpHeaderAccessor` (`1184-1200`) stays: it is the publish path's injector and the consume path's Carrier.

In `messaging/registry.go`: delete `amqpDeliveryAccessor` and its two methods (`918-933`).

In `messaging/internal/tracking/metrics.go`: delete `RecordAMQPConsumeMetrics`, `RecordAMQPConsumeCompletion` and `deliveryAttributes`. That removes the last use of `amqp "github.com/rabbitmq/amqp091-go"` in the package — drop the import. `RecordConsume`'s doc comment must lose nothing; the two doc comments that named `StartConsumeSpan` go with the functions that carried them.

- [ ] **Step 2: Run the build to enumerate the fallout**

Run: `go build ./... && go vet ./messaging/...`

Expected: `go build` succeeds (no production caller remains). `go vet` FAILS on the test files that still reference the deleted symbols — that list is Step 3's worklist:

```text
messaging/otel_test.go:...: undefined: StartConsumeSpan
messaging/registry_test.go:...: undefined: amqpDeliveryAccessor
messaging/amqp_test.go:...: undefined: amqpDeliveryAccessor
messaging/internal/tracking/metrics_test.go:...: undefined: RecordAMQPConsumeMetrics
messaging/internal/tracking/metrics_test.go:...: undefined: RecordAMQPConsumeCompletion
```

- [ ] **Step 3: Apply the test disposition table**

Delete the four `otel_test.go` tests (`263-380`) and the three `tracking/metrics_test.go` tests (`190-352`). Drop `amqp "github.com/rabbitmq/amqp091-go"` from `tracking/metrics_test.go`; check whether `testQueueName` (`22`) still has a user there and drop it too if not.

Delete `TestAmqpDeliveryAccessorSet` (`registry_test.go:751-775`) and the `// ===== amqpDeliveryAccessor Tests =====` banner (`693`). Move `TestAmqpDeliveryAccessorGet` (`695-749`) into `messaging/amqp_test.go`, renamed and retyped:

```go
func TestAmqpHeaderAccessorGet(t *testing.T) {
	tests := []struct {
		name     string
		headers  amqp.Table
		key      string
		expected any
	}{
		{name: "nil headers", headers: nil, key: testKeyName, expected: nil},
		{name: "empty headers", headers: amqp.Table{}, key: testKeyName, expected: nil},
		{name: "existing key", headers: amqp.Table{testKeyName: testValueContent}, key: testKeyName, expected: testValueContent},
		{name: "non-existing key", headers: amqp.Table{"other-key": "other-value"}, key: testKeyName, expected: nil},
		{name: "multiple headers", headers: amqp.Table{"key1": "value1", "key2": 42, "key3": true}, key: "key2", expected: 42},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			accessor := &amqpHeaderAccessor{headers: tt.headers}
			assert.Equal(t, tt.expected, accessor.Get(tt.key))
		})
	}
}
```

`testKeyName` and `testValueContent` live in `messaging/testconsts_test.go` and are visible to both files (one package) — confirm with `git grep -n 'testKeyName' -- messaging/testconsts_test.go` before assuming.

Retype the five `&amqpDeliveryAccessor{headers: …}` sites in `amqp_test.go` to `&amqpHeaderAccessor{headers: …}` and, at `TestAMQPCentralizedArchitectureConsistentProcessing` (`327`), replace the leading comment with:

```go
	// One accessor now serves both directions: the publish path injects through
	// it and the consume path reads through it as the pipeline's Carrier.
```

- [ ] **Step 4: Verify the deletions are complete**

Run: `git grep -n 'StartConsumeSpan\|RecordAMQPConsumeMetrics\|RecordAMQPConsumeCompletion\|amqpDeliveryAccessor\|handlePanicRecovery' -- '*.go'`

Expected: **no output.**

Run: `git grep -n 'StartConsumeSpan\|RecordAMQPConsumeMetrics' -- '*.md' 'llms.txt'`

Expected: exactly the two `docs/superpowers/plans/2026-08-16-streams-environment-port.md` lines (the PR1 plan's out-of-scope note) and nothing else. That file is history and is **not** edited.

- [ ] **Step 5: Run the tree and `make check`, then commit**

Run: `go test -race ./messaging/... && go vet ./messaging/...`

Expected: `ok` for every package, no vet output.

Run: `pwd && git branch --show-current && make check`

Expected: exits 0. Watch for `unused` on a now-orphaned test constant in `tracking/metrics_test.go`, and for `gci` after the import deletions — always `git status --porcelain` after `make fmt`.

```bash
cat > /tmp/delete-start-consume-span-msg.txt <<'EOF'
refactor(messaging)!: remove StartConsumeSpan and the receive-time metric

StartConsumeSpan was exported for consumers driving their own AMQP consume
loop, and it did two things at once: build the receive span, and increment
messaging.client.consumed.messages at receive time with a hardcoded nil
error, so the counter could never carry error.type. The delivery pipeline
now owns the span, and tracking.RecordConsume owns the count -- once, at
completion, with error.type on a failure -- so both jobs have exactly one
home and the exported function has no caller left.

Delete it rather than deprecate it: the framework does not ship compat
shims. Deleted with it: RecordAMQPConsumeMetrics and
RecordAMQPConsumeCompletion (internal), the read-only amqpDeliveryAccessor,
whose surviving twin amqpHeaderAccessor is what the pipeline reads through,
and the package's only //nolint:spancheck, since the pipeline starts and
ends its span in one function.

BREAKING CHANGE: messaging.StartConsumeSpan is removed, and the AMQP
consumed counter is recorded at completion with error.type instead of at
receive without it. See ADR-068 and migrations.md [C60.6].
EOF
git add messaging/amqp_client.go messaging/registry.go messaging/amqp_test.go \
        messaging/otel_test.go messaging/registry_test.go \
        messaging/internal/tracking/metrics.go messaging/internal/tracking/metrics_test.go
git commit -F /tmp/delete-start-consume-span-msg.txt
```

### Task 7: ADR-068, the `[C60.6]` atom, and the docs

**Files:**

- Create: `wiki/adr_068_delivery_pipeline.md`
- Modify: `wiki/architecture_decisions.md` (new index entry after ADR-067's, and the numbering-policy counter)
- Modify: `wiki/migrations.md` (the `E60` section — extend or create — plus the `[C60.6]` atom)
- Modify: `wiki/messaging.md` (the consume observability paragraph, `203`)
- Modify: `CLAUDE.md` (one Breaking Changes index line)
- Modify: `llms.txt` (the `consumed.messages` attribute cell, `4642`)

**Interfaces:** none — documentation only.

**Estimated LoC:** ~185 (ADR ~110, atom ~45, index ~12, wiki/CLAUDE.md/llms.txt ~18).

- [ ] **Step 1: Write ADR-068**

Create `wiki/adr_068_delivery_pipeline.md`:

```markdown
# ADR-068: One delivery pipeline for both messaging lanes

- **Status**: Accepted
- **Date**: 2026-08-17
- **Related**: [ADR-059](adr_059_streams_consumption.md) (the streams lane's shape and its "trace-context propagation" future-work item), [ADR-063](adr_063_streams_native_publishing.md) (native publishing — untouched here), [ADR-032](adr_032_lease_scoped_handles.md) (the per-message lease scope), [ADR-058](adr_058_consumer_scoped_amqp_args.md) (the two-lane framing)

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

## References

- `messaging/internal/delivery/delivery.go` — the pipeline
- `messaging/registry.go` — the classic lane's adapter and its settlement
- `messaging/internal/tracking/metrics.go` — `ConsumeAttributes` and `RecordConsume`
- [wiki/messaging.md](messaging.md#message-error-handling) — the consumer-facing behavior
- [wiki/migrations.md](migrations.md) `[C60.6]`
```

Before writing the `Related` line, confirm the ADR-032 and ADR-058 filenames with `ls wiki/adr_032_* wiki/adr_058_*` and use whatever is there; drop a reference rather than inventing a filename.

- [ ] **Step 2: Add the ADR index entry and bump the counter**

In `wiki/architecture_decisions.md`, after ADR-067's entry and its `---` separator (Stack A's; if ADR-066/067 are not on `main` yet, append after ADR-065's instead and keep the same shape):

```markdown
### [ADR-068: One Delivery Pipeline for Both Messaging Lanes](adr_068_delivery_pipeline.md)

**Date:** 2026-08-17 | **Status:** Accepted

Both lanes implemented span → invoke → recover → record independently, and the copies drifted: the
classic lane counted `messaging.client.consumed.messages` at receive with a hardcoded nil error, the
streams lane at completion with `error.type`; the streams lane extracted no trace context and
installed no per-message lease scope; three issues rewrote `processMessage` in a month without
reaching the streams copy. `messaging/internal/delivery` now owns everything between "bytes arrived"
and "outcome recorded" — carrier extraction, the Consumer span, the lease scope, `EnsureTraceID`, the
handler, panic-to-error, one `RecordConsume`, the lane's outcome line — behind
`Run(ctx, *Request) *Result`. Settlement stays lane-side, so "never requeue" and ADR-059's "commit
only after success" do not move.

**Key Benefits:** One body to fix instead of two that drift, and one meaning for the consumed counter.
**Watch:** this is **breaking** — `messaging.StartConsumeSpan` is removed with no replacement export
(a service driving its own consume loop owns its own span), and the classic lane's consumed counter is
recorded at completion with `error.type` instead of at receive without it. OTel span parenting is
deliberately unchanged: a consume span is still a root span. See [migrations.md](migrations.md)
`[C60.6]`.

---
```

Then set the numbering-policy line (`wiki/architecture_decisions.md:1353` on this branch) to read `ADR-001 through ADR-068`.

- [ ] **Step 3: Add the `[C60.6]` atom**

**First check what is there:** `grep -n '^## E60' wiki/migrations.md` and `grep -n '^### \[C60' wiki/migrations.md`.

*If `## E60` exists* (Stack A merged first — its header reads "readiness speaks one status vocabulary"): append ` + one delivery pipeline for both messaging lanes` to the header, add `C60.6` to the section's `- build-caught:` line, append this sentence to the end of its `- gist:` bullet, and put the atom below after the last `C60` atom.

```markdown
  Separately, both messaging lanes now run one delivery pipeline
  (`messaging/internal/delivery`, ADR-068): `messaging.StartConsumeSpan` is
  removed with no replacement export, and the classic lane's
  `messaging.client.consumed.messages` is recorded at completion with
  `error.type` instead of at receive without it (C60.6).
```

*If `## E60` does not exist*, create it before the atom — header `## E60 · v0.59.0 → v0.60.0 — one delivery pipeline for both messaging lanes`, a `- gist:` built from the sentence above, `- build-caught: C60.6`, and an `- exit:` line running `go get github.com/gaborage/go-bricks@v0.60.0 && go mod tidy && go build ./... && go test ./...` — and also add the `─E60─ v0.60.0` edge to the Ladder block (`wiki/migrations.md:24`) and a row to the edge table (`:33-45`).

*If `main` already carries a `[C60.6]`*, renumber this one to the next free `[C60.N]` and update every reference to it: the ADR file, the ADR index entry, the commit body, and the PR body.

The atom:

````markdown
### [C60.6] `messaging.StartConsumeSpan` is removed and the AMQP consumed counter moves to completion with `error.type` · breaking · when: match

- detect: two doors, and you may be behind either. Code:
  `git grep -n 'StartConsumeSpan' -- '*.go'` in your service — hits mean you drive your own
  AMQP consume loop rather than declaring consumers through `DeclareConsumer`. Telemetry:
  search dashboards, alerts and saved queries for `messaging.client.consumed.messages`; no
  repo-local grep finds those.
- scope: `messaging.StartConsumeSpan` is deleted (no deprecated stub, no replacement export).
  Separately, the classic lane's consumed counter is recorded when a delivery finishes rather
  than when it arrives, and carries `error.type` when the handler returned an error or panicked.
  The count per delivery is unchanged and the duration histogram is unchanged. Framework-declared
  consumers need no code change: `Registry.processMessage` does all of this for you.
- gate: match = at least one `StartConsumeSpan` call, or at least one query over
  `messaging.client.consumed.messages`. no-match = neither; nothing to do.
- apply: for the code door, start the span yourself — the framework no longer offers one:

  ```go
  ctx, span := otel.Tracer("your-service").Start(ctx, queue+" receive",
      trace.WithSpanKind(trace.SpanKindConsumer))
  defer span.End()
  ```

  For the telemetry door, split by `error.type` where a query used to assume the counter had none,
  and expect a counter sample to land at handler completion — a query correlating counter and
  histogram timestamps now sees them together instead of a handler-duration apart.
- verify: `go build ./...` for the code door. For the telemetry door: consume one message whose
  handler fails and confirm the counter sample carries `error.type`.
- ref: [ADR-068](adr_068_delivery_pipeline.md) · `messaging/internal/delivery/delivery.go`
````

- [ ] **Step 4: Update the consumer-facing docs**

In `wiki/messaging.md`, replace the `**Observability:**` line (`203`):

```markdown
**Observability:** ERROR logs include `message_id`, `queue`, `event_type`, `correlation_id`, `error`. Each delivery opens a Consumer-kind span named `"<queue> receive"` and records `messaging.client.operation.duration` plus `messaging.client.consumed.messages` when it finishes — both carrying `error.type` when handling failed, so a failure is separable on the counter as well as the histogram (ADR-068).
```

In `llms.txt`, the `consumed.messages` row (`4642`), extend the attribute cell:

```text
| Messaging | `messaging.client.sent.messages` / `consumed.messages` | Counter | `messaging.destination.name`, `error.type` (consumed only, on failure) |
```

In `CLAUDE.md`, append to the **Breaking Changes** list:

```markdown
- **One delivery pipeline (ADR-068):** `messaging.StartConsumeSpan` is removed — a service driving its own consume loop starts its own span — and the AMQP `messaging.client.consumed.messages` counter is recorded at completion with `error.type` instead of at receive without it.
```

`CLAUDE.md` is already 42,159 B against its 40,960 B soft ceiling (no CI gate); this adds ~290 B and deliberately does not attempt an offsetting trim — that is its own task.

`wiki/streams.md`'s Observability section (`410-417`) is **not** edited: the streams lane's telemetry is unchanged by this PR, and PR3 is what rewrites that paragraph.

- [ ] **Step 5: Lint the docs, `make check`, and commit**

Run: `npx --yes markdownlint-cli2@0.23.2 --config .markdownlint-cli2.jsonc wiki/adr_068_delivery_pipeline.md wiki/architecture_decisions.md wiki/migrations.md wiki/messaging.md CLAUDE.md`

Expected: `Summary: 0 error(s)`. The fenced Go block nested inside the atom's `apply` bullet must stay indented to the bullet and must carry its `go` language tag (MD040); the atom's bullets need a blank line before and after that block (MD031/MD032).

Run: `pwd && git branch --show-current && make check`

Expected: exits 0 (`make check` runs `lint-md` over the whole tree).

```bash
cat > /tmp/adr-068-msg.txt <<'EOF'
docs(messaging): record the delivery pipeline as ADR-068

ADR-068 states the decision the last two commits implemented: one pipeline
package owns everything between "bytes arrived" and "outcome recorded",
settlement stays lane-side, each lane supplies its own attribute bundle and
the pipeline owns when and once, StartConsumeSpan is deleted rather than
deprecated, and OTel span parenting is deliberately left alone -- a consume
span is still a root span, and changing that would re-parent every existing
consumer span.

migrations.md [C60.6] gates both doors a consumer can be behind: a
StartConsumeSpan call that no longer compiles, and a dashboard query over
messaging.client.consumed.messages that now sees error.type and a sample
that lands at completion instead of at receive.
EOF
git add wiki/adr_068_delivery_pipeline.md wiki/architecture_decisions.md \
        wiki/migrations.md wiki/messaging.md CLAUDE.md llms.txt
git commit -F /tmp/adr-068-msg.txt
```

### Task 8: Gates for PR2c (controller only)

- [ ] **Step 1: `make check`, backgrounded**

```bash
make check
```

`run_in_background: true`. Expected: exits 0.

- [ ] **Step 2: `/simplify`**

Runs first because it mutates the diff. Likely targets: `ackMessage`/`nackMessage` (near-twins — folding them into one `settle` helper is a legitimate simplification, but only if it keeps the two distinct log messages); the two closures in `processMessage` (they are the seam, not duplication — resist hoisting them into methods that take five arguments each). If it changes code, re-run `make check` **and** re-measure the alloc guard (Task 5 Step 6).

- [ ] **Step 3: `/security-audit`**

Focus for this diff: (a) the panic path — confirm `Result.Stack` reaches only the ERROR log line it always reached and no metric or span attribute; (b) settlement now runs after `scope.ReleaseAll()`, so confirm nothing in `ackMessage`/`nackMessage` borrows a per-tenant handle (neither touches anything but the delivery); (c) `logProcessing` still guards the whole field chain behind `Enabled()`, so a dropped DEBUG event never runs the sensitive-data filter over a payload field. If it changes code, re-run `make check`.

- [ ] **Step 4: `/code-review` (CodeRabbit)**

Must see the final diff. It will ask about the breaking change: point at ADR-068, `[C60.6]`, and the `!` in the PR title. If it asks for a deprecated `StartConsumeSpan` stub, decline and cite CLAUDE.md's Backward Compatibility rule. Post `@coderabbitai review` explicitly — CodeRabbit skips stacked PRs whose base is not `main`.

- [ ] **Step 5: `make mutate`, backgrounded, after committing**

```bash
make mutate
```

`run_in_background: true`, and **commit first** — the scope is `merge-base..HEAD`. Lines most likely to survive and their killers: each `!= ""` guard in `consumeSpanExtras` is killed by `TestConsumeSpanExtrasOmitTheFieldsTheDeliveryDidNotCarry` and `TestConsumeSpanExtrasCarryEveryDeliveryField`; `res.Outcome == pipeline.Succeeded` in `processMessage` is killed by `TestRegistryProcessMessageSuccess` (asserts ack) and `TestRegistryProcessMessageHandlerError` (asserts nack); `!dbg.Enabled()` is killed by `TestRegistryProcessMessageSkipsDebugFieldBuildWhenDisabled`.

- [ ] **Step 6: Integration suite (optional, Docker required)**

```bash
make test-integration
```

Worth one run: `messaging/amqp_client_integration_test.go` drives real deliveries through `processMessage`, which is the only end-to-end check that ack and nack still reach the broker now that they run after `span.End()`.

- [ ] **Step 7: Push and open PR2c**

Confirm the branch is `feature/messaging-amqp-on-pipeline`, push, and open against `feature/messaging-delivery-pipeline`. **The title must carry the `!`** — the apidiff job fails a new incompatible export change without it, and the same `!` drives release-please's version bump:

Title: `refactor(messaging)!: run the AMQP lane on the delivery pipeline`

```markdown
## What

`processMessage` carried the whole per-message body — span, lease scope, bound logger, panic recovery, metrics, four log lines — across four functions plus an exported `StartConsumeSpan` that also stamped the consumed counter at receive time with a hardcoded nil error. It is now a lane adapter over `delivery.Run`, settling ack/nack on the returned `Result`; `StartConsumeSpan`, the receive-time metric path and the read-only `amqpDeliveryAccessor` are deleted.

## Impact

`messaging.StartConsumeSpan` is removed with no replacement export — start your own span if you drive your own consume loop. `messaging.client.consumed.messages` is now recorded at completion with `error.type` on failure instead of at receive without it; the count per delivery is unchanged. See ADR-068 and `[C60.6]`.

## Verification

`make mutate` ran clean on the changed lines. The container suite passed against a real broker, which is what covers ack and nack now running after the span ends.
```

---

## Self-review

**Spec coverage (Delivery pipeline decisions 1–5 and 7).**

| Spec decision | Where |
| --- | --- |
| 1 — new `messaging/internal/delivery`, importable by both lanes | Task 3 Step 3 (package created); Task 5 Step 3 (classic lane imports it as `pipeline`); the streams lane's import is PR3's, and Task 1 Step 5 proves the direction is already legal |
| 2 — the pipeline owns the per-message context: carrier extraction, Consumer span, `leasescope.Install`, `EnsureTraceID`, handler, panic → error, one `RecordConsume` with `error.type`, failure log with lane-supplied fields; outcomes `succeeded · handlerError · panicked` | Task 3 Step 3 (`Run`, `invoke`, `Outcome`); Task 3 Step 1 tests each clause: `TestRunCarriesTheCarrierTraceIDIntoTheContextAndTheResult`, `TestRunStartsOneConsumerSpanPerMessage`, `TestRunInstallsALeaseScopeAndDrainsItAfterTheHandler`, `TestRunConvertsAPanicIntoAnError`, `TestRunRecordsOneConsumeAtCompletion`, `TestRunRecordsAFailedDeliveryWithItsErrorType`, `TestRunHandsTheLaneOneOutcomePerMessage` |
| 3 — settlement is a lane adapter; "never requeue" and "commit only on success" stay adapter policy; publisher-side injection untouched | Task 5 Step 3 (`processMessage`'s tail, `ackMessage`, `nackMessage` unchanged); no broker call exists in `delivery.go` (Task 3 Step 3); `amqp_client.go:349` is not in any task's file list |
| 4 — telemetry values preserved per lane; each lane supplies an attribute bundle; the pipeline owns when and once; only the consumed counter moves | "Telemetry preserved, value by value" table above, row by row; Task 1 (`ConsumeAttributes`), Task 5 Step 3 (`consumeSpanExtras`, `logOutcome`), Task 5 Step 6 (the twenty-one unedited tests that prove it) |
| 5 — deletions: `StartConsumeSpan`, the receive-time `RecordAMQPConsumeMetrics` path, `amqpDeliveryAccessor`; `tracking` ends with one `RecordConsume(ctx, attrs, duration, err)` | Task 6 Steps 1–4 (with the reference-count table and the per-test disposition table); Task 1 Step 4 (`RecordConsume`'s signature is exactly the spec's) |
| 7 — PR2 = pipeline + AMQP lane + `StartConsumeSpan` removal + ADR-068 + atom | The whole plan; the three-PR split is the controller's mandated decomposition of that one PR, and PR2c carries ADR-068 and `[C60.6]` |

Spec items **not** covered, by design: decision 6 (streams lane on the pipeline — its trace extraction, its lease scope, its panic wording, and the two atom lines those need) is PR3. Task 1 Step 5 touches `messaging/streams/runner.go` only to swap one recorder call, which changes no emitted value.

**Placeholder scan.** No `TBD`, no "similar to Task N", no "add tests here". Every code step carries its code; every run step carries its command and its expected output. Three values are deliberately left to be measured or read rather than guessed, and each names the command that produces it: the allocs/op number in Task 5 Step 6 (`go test -run TestRegistryProcessMessagePerDeliveryLoggerAllocs -v`), the shape of the `E60` section in Task 7 Step 3 (`grep -n '^## E60' wiki/migrations.md`, with both branches written out), and the ADR-032/ADR-058 filenames in Task 7 Step 1 (`ls wiki/adr_032_* wiki/adr_058_*`).

**Type consistency.**

- `Handler` is `func(ctx context.Context, log logger.Logger, traceID string) error` in the type declaration (Task 3 Step 3), in `Request.Handle`, in `invoke`'s fourth parameter, in every test handler (Task 3 Step 1) and in the classic lane's closure (Task 5 Step 3).
- `LogOutcome` is `func(*Result)` in `Request`, in `outcomes.log` (Task 3 Step 1), and in the classic lane's closure, which forwards to `(*Registry).logOutcome(res *pipeline.Result, …)`.
- `Run` returns `*Result` and `invoke` returns `*Result`; `Run` hands the *same pointer* to `LogOutcome` and to its caller, which `TestRunHandsTheLaneOneOutcomePerMessage` asserts with `assert.Same`.
- `Request.Metrics` is `tracking.ConsumeAttributes` (Task 1), built by `tracking.AMQPConsumeAttributes(exchange, routingKey, queue)` in the classic lane (Task 5 Step 3) and by `tracking.StreamConsumeAttributes(streamName)` in the streams-lane tests (Task 3 Step 1) and in PR3.
- `Request.Carrier` is `gobrickstrace.HeaderAccessor` (`trace/trace.go:108`); `*amqpHeaderAccessor` satisfies it (`Get`/`Set`, `amqp_client.go:1189,1196`) and so does the tests' `mapCarrier`.
- `Request.Log` and `Result.Log` are both `logger.Logger`; the pipeline writes `Result.Log = req.Log.WithContext(msgCtx)`, and `ackMessage`/`nackMessage` take `logger.Logger` unchanged.
- `Request.SpanExtras` and `consumeSpanExtras`'s return are both `[]attribute.KeyValue`, appended after the four `spanAttributes` builds.
- `tracking.RecordConsume(ctx context.Context, attrs ConsumeAttributes, duration time.Duration, err error)` is the same four-parameter signature in Task 1, in `delivery.Run`, and in `streams/runner.go:256`.
- `processMessage`'s signature is byte-identical before and after Task 5, which is why the twenty-eight test call sites in `registry_test.go` and `amqp_test.go` are not in any task's edit list.
- The import alias `pipeline` is used for `messaging/internal/delivery` at every site in `registry.go`; the package's own files and tests use no alias. `.golangci.yml`'s `importas` map does not pin this path, so no conflict rule applies.
