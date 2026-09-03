# Draft atoms for the v0.62.0 → v0.63.0 hop (E63)

Form copied from `[C61.25]` (ADR-091) and `[C45.7]` (ADR-033) in `wiki/migrations.md`. Atom
ids assume the hop row `E63 | v0.62.0 → v0.63.0`; renumber `.1`/`.2` if another atom lands on
the hop first. Each atom ships WITH its code PR (#1350 for C63.1, #1353 for C63.2), never alone.

## Hop row (add to the hop table)

| E63 | v0.62.0 → v0.63.0 | compile-break (C63.1 — `Client.Publish` / `AMQPClient.PublishToExchange` removed from the module-facing types) + breaking (C63.2 — a header-sourced event id outside `^[A-Za-z0-9_-]{1,128}$` is refused before the inbox ledger) | 2 | C63.1 | if any consumer's producer mints `x-outbox-event-id` values outside that grammar (a `:`, whitespace, over 128 chars), re-mint before the bump or those messages nack into the DLQ on first delivery (C63.2) |

## Atoms

### [C63.1] `Client.Publish` / `AMQPClient.PublishToExchange` leave the module surface — publish through `DeclareTypedPublisher[T]` · compile-break · when: match

- detect: `git grep -nE '\.PublishToExchange\(|\.Publish\(ctx, [^&]' -- '*.go'`
  and `git grep -nE 'messaging\.PublishOptions\{' -- '*.go'`
  and `git grep -nE 'MockMessagingClient|MockAMQPClient' -- '*_test.go'`
  Hits on the first two in module or job code are compile breaks: the methods are gone from
  `messaging.AMQPClient` (what `deps.Messaging(ctx)` returns) and from the type
  `scheduler.JobContext.Messaging()` now returns. Hits on the mocks are compile breaks in
  your tests: `MockMessagingClient.Publish` and `MockAMQPClient.PublishToExchange` are
  removed with the interface methods. A hit on `streams.Publisher.Publish(&streams.PublishMessage{…})`
  is NOT affected — the streams door is unchanged.
- scope: `DeclareTypedPublisher[T](decls, opts) *Publisher[T]` is the only module-facing
  publish door. The handle is bound at declaration to the exchange, routing key, event type
  and default headers you used to re-spell in `PublishOptions`; `h.Publish(ctx, client, evt)`
  JSON-marshals `evt` and publishes through the stamped client you already obtain with
  `deps.Messaging(ctx)`. Retry bounds (ADR-033, `ErrPublishRetriesExhausted`), readiness
  pre-flight and the ADR-087 tenant stamp are unchanged behind the handle. Framework
  internals (outbox relay, stamping wrapper, manager lease) keep a bytes door on an
  internal type you cannot reach.
- gate: match = you call `Publish`/`PublishToExchange` on a client obtained from
  `deps.Messaging(ctx)` or `JobContext.Messaging()`, or inject the removed mock methods.
  no-match = you publish only through the outbox (`deps.Outbox.Publish` with a struct
  payload) or the streams lane — nothing to do.
- apply: In `DeclareMessaging`, replace `decls.DeclarePublisher(&messaging.PublisherOptions{…}, ex)`
  with `m.orders = messaging.DeclareTypedPublisher[OrderCreated](decls, &messaging.PublisherOptions{…})`
  (same options struct, same exchange/routing key/event type) and at the call site replace
  `body, _ := json.Marshal(evt); client.PublishToExchange(ctx, messaging.PublishOptions{Exchange: …, RoutingKey: …}, body)`
  with `m.orders.Publish(ctx, client, evt)`. Default-exchange sends (`client.Publish(ctx, queue, body)`)
  become a typed publisher declared with an empty `Exchange` and the queue name as
  `RoutingKey`. Tests: capture the published frame through the typed-door test double
  `testing/mocks` now ships (named in the #1350 PR) instead of asserting on the removed
  mock methods.
- verify: `go build ./... && go vet ./...`  # clean; then `git grep -nE '\.PublishToExchange\(' -- '*.go'` returns nothing outside vendored framework code
- ref: gaborage/go-bricks#1350 · [ADR-096](adr_096_typed_publish_door.md) · `messaging/typed_publisher.go`, `messaging/messaging.go`, `scheduler/job.go`, `testing/mocks/`

### [C63.2] header-sourced event ids are validated against `^[A-Za-z0-9_-]{1,128}$` before the inbox ledger · breaking · when: match

- detect: `git grep -nE 'ProcessOnce\(|EventIDFromHeaders\(|x-outbox-event-id' -- '*.go'`
  and, on the PRODUCER side (possibly another repo), `git grep -nE 'x-outbox-event-id|OutboxEvent\{' -- '*.go'`
  Hits on the consumer side mean the new check is in your path. The id's shape is decided
  by whoever mints it: framework-minted outbox ids are UUIDs and already comply; a
  hand-set header, an upstream bridge, or a partner producer may not.
- scope: every id that reaches `inbox.ProcessOnce` through the `x-outbox-event-id` header is
  validated against `^[A-Za-z0-9_-]{1,128}$` first — for unsealed consumers too. A failing
  id is a typed error (`inbox.ErrHeaderIDInvalid`, name per #1353) returned from the
  framework path before any ledger write; the handler returns it and the delivery is
  nacked without requeue into the standard DLQ path. `Meta.DedupKey()` on a plain typed
  consumer returns the validated id or that error. The grammar exists so a sealed Dedup key
  `<SignFamily>:<jti>` (ADR-097) can never be pre-inserted through a rewritable header —
  `:` is outside it. Ids are never logged by value (presence and length only). The ledger's
  duplicate short-circuit now emits a counter and a structured log line.
  `inbox.retentionperiod` is the replay window: it must exceed the broker's redelivery
  window AND cover DLQ drains and outbox re-drives you intend to replay.
- gate: match = any producer of the events you consume mints `x-outbox-event-id` outside the
  grammar (contains `:` or whitespace, empty, or over 128 characters), or your tests inject
  such ids. no-match = ids are framework-minted UUIDs — nothing to do beyond the counter.
- apply: Re-mint non-conforming ids at the producer (a UUID or any `[A-Za-z0-9_-]{1,128}`
  string) before upgrading consumers, OR move that event to the sealed Typed door
  (ADR-097), where the Dedup key is framework-composed and the header is not consulted.
  Consumers that need to replay a DLQ older than `inbox.retentionperiod` raise the
  retention first. Wire the new dedup-hit counter into the replay-campaign alert.
- verify: `go test ./...`  # then publish one message with `x-outbox-event-id: "bad:id"` to a test queue and confirm it lands in the DLQ with the typed error and no ledger row; publish a UUID id and confirm it processes
- ref: gaborage/go-bricks#1353 · [ADR-097](adr_097_sealed_amqp_messages.md) §4 · `inbox/inbox.go`, `outbox/headers.go`, `messaging/typed_consumer.go`

## Skill lines (`.claude/skills/breaking-changes/SKILL.md`, newest last)

- **Typed publish door replaces raw byte publishing (ADR-096):** `messaging.Client.Publish` and `messaging.AMQPClient.PublishToExchange` are removed from the module-facing types (`deps.Messaging(ctx)`, `JobContext.Messaging()`) with no escape hatch; declare `messaging.DeclareTypedPublisher[T](decls, opts)` in `DeclareMessaging` and publish with `h.Publish(ctx, client, evt)` — the handle carries the declared exchange, routing key, event type and headers, and `h.Seal(ctx, evt)` yields bytes for the outbox lane. `testing/mocks` loses the two raw methods; `streams.Publisher.Publish` is unchanged. See `[C63.1]`.
- **Header-sourced event ids must match `^[A-Za-z0-9_-]{1,128}$` (#1353, ADR-097 §4):** `x-outbox-event-id` is validated before `inbox.ProcessOnce` for every consumer, sealed or not; a non-conforming id is a typed error and a nack-without-requeue on first delivery. Framework-minted UUIDs comply; re-mint hand-set ids or move the event to the sealed Typed door. `Meta.DedupKey()` returns the validated id (plain `T`) or `<SignFamily>:<jti>` (sealed `T`); `inbox.retentionperiod` is the replay window. See `[C63.2]`.
