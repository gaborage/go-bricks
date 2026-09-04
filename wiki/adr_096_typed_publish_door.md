# ADR-096: The Typed Publish Door Replaces Raw Byte Publishing

- **Status**: Accepted
- **Date**: 2026-09-04
- **Related**: [ADR-033](adr_033_outbox_retry_count_status_parking.md) (bounded
  publish retries — unchanged, now behind the typed door), [ADR-087](adr_087_messaging_tenancy_and_tenant_stamp.md)
  (the tenant stamp the framework alone writes — the stamping wrapper stays between
  the handle and the wire), [ADR-091](adr_091_streams_opt_in_registration.md)
  (the removed-surface + init-registration precedent this decision reuses for the
  relay's seam), ADR-097 (sealed messages — the consumer of this door)
- **Issue**: #1305 (decision), #1309 (spec), #1347 (stack), #1350 (this PR);
  inventory on branch `research/publish-surface-inventory`; #1308 (prototype)

## Context

`Declarations.DeclarePublisher` registers a destination — exchange, routing key,
event type, default headers — and hands back nothing. The bytes went through a
different object: `messaging.AMQPClient.PublishToExchange(ctx, PublishOptions, []byte)`
or the default-exchange `Client.Publish(ctx, destination, []byte)`. Every module
therefore re-spelled `Exchange`/`RoutingKey` in a `PublishOptions` literal at each
call site, and nothing at runtime tied a `PublisherDeclaration` to the publish that
followed it — the registry validated, replayed per tenant and hashed an `EventType`
that no publish ever carried. Six consumer-facing doc call sites (`llms.txt`) taught
the raw doors; the demo project had zero (it publishes through the outbox with
struct payloads only); `scheduler.JobContext.Messaging()` was typed `messaging.Client`,
so a job reached only the default-exchange door.

Sealing (#1305, ADR-097) needs a door that sees the typed event: sealing engages from
`seal` struct tags, and a raw `[]byte` door beside the typed one would leave
plaintext publication of a seal-tagged type representable at the module surface.

## Decision

`DeclareTypedPublisher[T](decls, opts) *Publisher[T]` is the only module-facing
publish door. The handle is bound at declaration to the declared exchange, routing
key, event type and default headers; `Publish(ctx, client, evt)` JSON-marshals the
event, seals it when `T` is seal-tagged (ADR-097), and publishes through the stamped
client passed explicitly (`getMessaging(ctx)` idiom — `DeclareMessaging` never sees
`ModuleDeps`). A module that wants to swap the handle in a test stores it behind the
additive `messaging.EventPublisher[T]` interface, which `*Publisher[T]` and
`messaging/testing.CapturePublisher[T]` (records `T` values) both satisfy.

`Client.Publish` and `AMQPClient.PublishToExchange` leave the module-facing types
with no escape hatch, and `PublishOptions` leaves with them (nothing module-facing
still needs it; the relay reaches the same shape through `internal/publishdoor.Options`).
`messaging.ValidatePublishDestination` keeps its job for the outbox and takes the
three destination parts directly — `(exchange, routingKey string, headers map[string]any)`.
`ModuleDeps.Messaging` and `JobContext.Messaging()` both return `messaging.AMQPClient`,
the module-facing type without the bytes methods. `testing/mocks` loses
`MockMessagingClient.Publish`, `MockAMQPClient.PublishToExchange` and the frame
capture that hung off it.

Framework internals keep a bytes door on an unexported type. `bytePublisher` is an
unexported interface inside `messaging` — one method, `publishBytes(ctx, publishOptions, []byte)` —
implemented by `AMQPClientImpl` and by the stamping wrapper the manager puts in front
of every pooled client (ADR-087). `Publisher[T].Publish` type-asserts it on the client
it is handed and fails with `messaging.ErrPublishDoorUnavailable` for a client that
lacks it — a hand-written `AMQPClient`, an `app.Options.MessagingClientFactory`
product, a `testing/mocks` double. The outbox relay, which lives outside the package
and must keep moving `record.Payload` bytes, reaches the door through
`internal/publishdoor`: `messaging` registers a dispatcher there at `init` (the
ADR-091 pattern) and the relay calls `publishdoor.Publish(ctx, client, opts, payload)`.

### Visibility argument — who can move bytes to the broker after this ADR

No exported symbol lets a module hand `[]byte` to the broker:

- `bytePublisher` and its one method are unexported, so only types declared in
  package `messaging` implement it, and `reflect` cannot call an unexported method.
  A module that type-asserts its `AMQPClient` to `*messaging.AMQPClientImpl` finds no
  exported publish method (`messaging/messaging_test.go` pins the exported method sets
  of `Client`, `AMQPClient`, `AMQPClientImpl` and the stamping wrapper;
  `scheduler/job_test.go` pins `JobContext.Messaging()`'s type).
- `internal/publishdoor` is under `internal/`, so no module can import it; its
  `Register`/`Swap`/`Publish` are reachable only from this repository's packages.
  The registered dispatcher itself is an unexported closure over `publishThroughDoor`.
- `Publisher[T].Publish` takes a `T`, marshals it with `encoding/json`, and is the
  one exported path to the wire. The residual it leaves is a JSON payload, not a
  byte payload: `json.Marshal` validates the output of any `json.Marshaler`
  (`json.RawMessage` included), so what reaches the frame is always well-formed JSON
  of a type the module declared — the same thing the sealed door judges by tags.
  Publishing an untagged `T` in clear is the documented default, not a bypass.
- A `go:linkname` pull of `(*AMQPClientImpl).publishBytes` needs `unsafe`, a
  layout-matching copy of the unexported `publishOptions`, and survives no
  refactor; it is outside any supported surface and is not a door this ADR
  reasons about.

`streams.Publisher.Publish(*PublishMessage)` is NOT touched by this decision; the
streams lane keeps its handle-at-declaration shape (which this ADR mirrors) and its
raw door until a streams-lane ADR decides otherwise.

## Alternatives considered

- **Additive typed door beside the raw one.** Rejected by the maintainer (2026-09-02):
  it leaves the bypass open — a seal-tagged type could still be marshaled and pushed
  through `PublishToExchange` as plaintext — and taxes every future reader with two
  doors and two sets of docs.
- **Raw door kept as a renamed escape hatch** (`PublishRaw`, `UnsafePublishBytes`).
  Rejected ("be bold"): an escape hatch is the bypass under another name. Byte-level
  partner interop, if it ever materializes, re-earns a narrow door through its own ADR
  with its own threat model.
- **Handle returned from `DeclarePublisher` itself** (non-generic). Rejected: the
  handle must know `T` for sealing to engage from tags and for the marshal step to be
  typed; a non-generic handle would need a `[]byte` `Publish` — the door being removed.
- **An exported bytes interface under `internal/` with an exported method name.**
  Rejected: an exported method on the stamping wrapper is reachable by asserting the
  client to an anonymous interface, which is exactly the door this ADR closes. The
  method must be unexported, which forces the interface into `messaging` and the
  relay onto an init-registered seam.

## Consequences

- **Breaking, at compile time** (`fix(messaging)!`). Every module publishing today
  migrates to a `DeclareTypedPublisher[T]` in `DeclareMessaging` plus
  `h.Publish(ctx, client, evt)` at the call site. See [migrations.md](migrations.md)
  `[C63.1]` for the complete list of exported changes.
- apidiff reports the interface-method removals, the `PublishOptions` type, the
  `ValidatePublishDestination` signature, `JobContext.Messaging()`'s return type and
  the `testing/mocks` doubles — expected, covered by the `!` title marker on the
  removing link.
- A client built by `app.Options.MessagingClientFactory` carries no byte door, so a
  `Publisher[T]` handed one fails with `ErrPublishDoorUnavailable`; the stamping
  wrapper refuses rather than bypassing the stamp. Consumer-built clients keep
  consuming and declaring; publishing through them was never stamped by the
  framework's own client either, and re-earns a door through its own ADR if needed.
- Framework tests outside `messaging` that observe the relay's bytes swap the
  `publishdoor` dispatcher in `TestMain` (see `outbox/main_test.go`); a module's tests
  observe typed events through `messaging/testing.CapturePublisher[T]`.
- No declaration-time guard rejects `T = json.RawMessage` or a custom `json.Marshaler`:
  the typed door publishes JSON documents, and the property this break exists for — a
  seal-tagged type never travels in plaintext — is judged by tags, not by `T`'s shape;
  a shape guard would be partial (it cannot see a custom `Marshaler`) and is not needed.
  The sealed door (ADR-097) judges the STATIC type of `T`: a hand-marshaled sealed struct
  carried as `json.RawMessage` has no tags and therefore travels in clear — that door
  should refuse or warn on `T = json.RawMessage`/`[]byte`/`any` when it lands.
- ADR-033's bounded-retry semantics are unchanged and now described on
  `Publisher[T].Publish`; ADR-087 stamping is unchanged (the handle publishes through
  the stamped client).
- One declaration, one destination, one event type: the registry's `EventType` is now
  the value a publish carries, which ADR-097 enforces on the consumer (`etyp`).
