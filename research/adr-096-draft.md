# ADR-096: The Typed Publish Door Replaces Raw Byte Publishing

- **Status**: Proposed (ships with the last link of the #1347 stack, #1350)
- **Date**: 2026-09-03
- **Related**: [ADR-033](adr_033_outbox_retry_count_status_parking.md) (bounded
  publish retries — unchanged, now behind the typed door), [ADR-087](adr_087_messaging_tenancy_and_tenant_stamp.md)
  (the tenant stamp the framework alone writes — the stamping wrapper stays between
  the handle and the wire), [ADR-091](adr_091_streams_opt_in_registration.md)
  (the removed-surface + import-gate precedent), ADR-097 (sealed messages — the
  consumer of this door)
- **Issue**: #1305 (decision), #1309 (spec); inventory on branch
  `research/publish-surface-inventory`; #1308 (prototype)

## Context

`Declarations.DeclarePublisher` registers a destination — exchange, routing key,
event type, default headers — and hands back nothing. The bytes go through a
different object: `messaging.AMQPClient.PublishToExchange(ctx, PublishOptions, []byte)`
or the default-exchange `Client.Publish(ctx, destination, []byte)`. Every module
therefore re-spells `Exchange`/`RoutingKey` in a `PublishOptions` literal at each
call site, and nothing at runtime ties a `PublisherDeclaration` to the publish that
follows it — the registry validates, replays per tenant and hashes an `EventType`
that no publish ever carries. Six consumer-facing doc call sites (`llms.txt`) teach
the raw doors; the demo project has zero (it publishes through the outbox with
struct payloads only); `scheduler.JobContext.Messaging()` is typed `messaging.Client`,
so a job reaches only the default-exchange door.

Sealing (#1305, ADR-097) needs a door that sees the typed event: sealing engages from
`seal` struct tags, and a raw `[]byte` door beside the typed one would leave
plaintext publication of a seal-tagged type representable at the module surface.

## Decision

`DeclareTypedPublisher[T](decls, opts) *Publisher[T]` is the only module-facing
publish door. The handle is bound at declaration to the declared exchange, routing
key, event type and default headers; `Publish(ctx, client, evt)` JSON-marshals the
event, seals it when `T` is seal-tagged (ADR-097), and publishes through the stamped
client passed explicitly (`getMessaging(ctx)` idiom — `DeclareMessaging` never sees
`ModuleDeps`). `Seal(ctx, evt) ([]byte, error)` returns the wire bytes for the
outbox lane.

`Client.Publish` and `AMQPClient.PublishToExchange` leave the module-facing types
with no escape hatch. Framework internals keep a bytes door on an internal type: the
stamping wrapper (`stamping_publisher.go`), the outbox relay (`outbox/relay.go`) and
the manager lease (`Manager.Publisher`) move bytes under the hood. `ModuleDeps.Messaging`
and `JobContext.Messaging()` return the module-facing type without the bytes methods.

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

## Consequences

- **Breaking, at compile time** (`fix(messaging)!`). Every module publishing today
  migrates to a `DeclareTypedPublisher[T]` in `DeclareMessaging` plus
  `h.Publish(ctx, client, evt)` at the call site. See [migrations.md](migrations.md)
  `[C63.1]`.
- apidiff reports the interface-method removals and the `testing/mocks` doubles
  (`MockMessagingClient.Publish`, `MockAMQPClient.PublishToExchange`) — expected,
  covered by the `!` title marker on the removing link.
- `JobContext.Messaging()` changes type (recorded in the #1349 PR); a job publishes
  through a typed handle declared by its module.
- `PublishOptions` leaves the module surface with the methods (or moves with the
  internal door if the relay still needs it — the #1350 PR states which).
- ADR-033's bounded-retry semantics are unchanged and now described on
  `Publisher[T].Publish`; ADR-087 stamping is unchanged (the handle publishes through
  the stamped client).
- One declaration, one destination, one event type: the registry's `EventType` is now
  the value a publish carries, which ADR-097 enforces on the consumer (`etyp`).
