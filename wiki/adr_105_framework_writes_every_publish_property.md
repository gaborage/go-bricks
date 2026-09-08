# ADR-105: The Framework Writes Every Publish Property, or None

**Status:** Accepted
**Date:** 2026-09-07

## Context

The NKH1 topology standard asks two things of a publish that GoBricks was not
supplying. §6.2 requires a business message to survive a broker restart; §6.5
requires the message to identify itself on the wire — which application produced it,
when, what event it is, how its body is encoded, and under what id.

`preparePublishing` (`messaging/amqp_client.go`) set three fields and no more:
`ContentType` hard-coded to `application/octet-stream`, the body, and an
`amqp.Table` for headers, plus the `CorrelationId`/`MessageId` pair derived below it.
`DeliveryMode` was left at its zero value, so **every** message the framework
published on that lane — a typed publish and an outbox-relayed row alike — was
transient and discarded when the broker restarted, and no caller could ask for
anything else: `publishOptions` carried no such knob and, since ADR-096, is
unexported. `AppId`, `Timestamp` and `Type` were never set at all. The one property
that WAS set was wrong for most traffic: the typed handle
(`messaging/typed_publisher.go`) marshals `T` as JSON and the sealed arm produces a
compact JWS, and both went out labelled as opaque octets.

The information each property needs is not held in one place. The typed handle knows
the encoding and the declared `EventType`; the outbox relay knows the row's event
type and its ledger id; the client knows the application identity and the clock.
Nothing about any of it is a caller's decision. The stream lane is not among them:
`messaging/streams` publishes through the AMQP 1.0 client and sets only application
properties (the trace keys and the tenant stamp), no message properties at all, so
none of §6.5's values was ever in play there.

## Decision

**Every property is written by the framework, at the seam that knows the answer, with
no caller knob anywhere.**

The work ships as a two-link stack, and this section describes the end state of the
pair. The first link (`fix(messaging)!`) is the messaging and `app` half: the
properties `preparePublishing` writes, the carrier they travel on, and the app
identity. The relay half (`feat(outbox)`) is the second link, and is where the
outbox-side values below — the relayed `MessageId` and `Type`, and the enqueue-time
encoding stamp — are set.

- **`DeliveryMode: amqp.Persistent` unconditionally**, in `preparePublishing`. There
  is no transient mode and no option to request one.
- **`AppId` is `app.name`.** `app/bootstrap.go` reads `cfg.App.Name` into two seams,
  both ending at the new exported `messaging.WithAppName` `ClientOption`:
  `newFactoryResolverForConfig` sets the unexported `FactoryResolver.appName` that
  the default factory returned by `MessagingClientFactoryWithOptions` passes on, and
  `ManagerConfigBuilder.appName` travels `messaging.ManagerOptions.AppName` into the
  fallback client factory `NewMessagingManager` builds when it is handed none.
  `MessagingClientFactoryOptions` gained NO field: it is passed by value, so the
  identity rides the resolver rather than growing that struct against gocritic's
  `hugeParam` bound. `WithAppName` is exported because the factory that calls it lives
  in `app`; it is the only new consumer-visible identifier. An empty name stamps no
  `app_id` rather than a placeholder.
- **`Timestamp` is `time.Now()`, read inline in `preparePublishing`.** Deliberately
  not an injectable clock: an unexported `now func() time.Time` field would be
  production-unused state, so the test that pins the property brackets the call and
  asserts with `assert.WithinDuration` instead. The consumer surface gains no time
  knob either — this is not a `ClientOption`.
- **`Type` is the event type** — the typed handle's declared `EventType`, and on a
  relayed publish the outbox row's `EventType`, which is the same value as the
  `x-outbox-event-type` header. That header STAYS; the property mirrors it.
- **`MessageId` on a relayed publish is the outbox row id**, the same value as
  `x-outbox-event-id`, which also stays: it remains the ledger key consumers dedupe
  on (ADR-097), and nothing reads the property in preference to it. Every other
  publish keeps the framework-minted UUID `preparePublishing` already generated.
- **`ContentType` is claimed only where it is known.** The three doors carry it on the
  ONE field both option structs gained — `publishdoor.Options.Props` → the unexported
  `publishOptions.props`, a `*publishdoor.MessageProps` holding `ContentType`,
  `EventType` and `MessageID` together; a nil carrier or an empty value falls back to
  `application/octet-stream` in `preparePublishing`. The typed handle
  answers from `Publisher[T].contentType()` — `application/jose` when the handle
  holds a sealer (ADR-097), `application/json` otherwise. The raw bytes path carries
  nothing and so keeps octet-stream.

**The outbox records the encoding at enqueue, because the relay cannot recover it.**
`outbox/publisher.go`'s `marshalPayload` has three arms: a nil payload becomes the
JSON literal `null`, a struct is `json.Marshal`ed, and a caller-supplied `[]byte` is
returned **unexamined**. The persisted-sealed path (`Publisher[T].Seal` →
`OutboxEvent.Payload`, the only sealed shape the outbox accepts) arrives as exactly
such a `[]byte`, indistinguishable from a hand-marshaled body, and nothing in the row
records which arm ran. So `marshalPayload` now also returns the encoding it produced
— `application/json` for the marshal and nil arms, **nothing at all** for the
`[]byte` arm — and `marshalHeaders` persists it as the unexported
`x-gobricks-content-type` stamp in the row's existing headers map. Both lanes strip
that stamp in `Plan` (`outbox/amqp_shipper.go`, `outbox/stream_shipper.go`), exactly
as they strip the tenant stamp, so it never reaches the wire; the AMQP lane reads it
into `shipment.ContentType` and hands it to the publish door. A row carrying no stamp
ships as octet-stream. A persisted-sealed or hand-marshaled event is therefore
**under**-labelled rather than mislabelled, which is the direction that cannot break
a consumer switching on the property.

`x-gobricks-content-type` is a **reserved** key, not an enforced one: nothing validates
caller header keys, and `marshalHeaders` writes the stamp into its copy of the caller's
own map *after* the copy, so a caller header of that exact spelling is silently
overwritten at enqueue and then deleted before the wire — the caller's value never
reaches a consumer and nothing reports the loss. The `x-gobricks-` namespace is chosen
to make that implausible, not impossible.

The stamp records the **JSON** arm rather than the opaque one — which costs a map
allocation and a non-NULL `headers` blob on the common enqueue, where the reverse
polarity would cost neither — because a stamp meaning "opaque" would make every
pre-upgrade row, which carries no stamp at all, read as JSON: exactly the mislabelling
this design exists to avoid.

## Alternatives

**A — sniff the payload in the relay.** Read the row's first non-space byte, or try a
`json.Unmarshal`, and label accordingly. Rejected: a compact JWS is
base64url — `eyJ...` — and so is any number of other bodies, so the heuristic answers
from the shape of the bytes rather than from what the producer meant. It would also
be a second, weaker copy of a fact the enqueue side holds exactly, and it would label
`application/json` onto bodies that merely parse as JSON. The framework does not
guess a producer's encoding on the producer's behalf.

**B — a real content-type column on the ledger.** The honest substrate, and where
this belongs eventually. Rejected for this change: it is a schema migration on every
deployed outbox table (`CreateTable` never `ALTER`s — see `[C61.23]`), which is a
far larger blast radius than the property it would improve. The header stamp needs no
schema change and is strictly internal, so the column stays available as a later
move.

**C — expose the properties as caller options.** Let a publisher set its own
content type, app id, timestamp or delivery mode. Rejected: the standard's §6.5
values are deployment identity, not message content, and ADR-096 has just finished
removing the raw options door from the module-facing surface. A knob here would
re-open it, and the framework writing these values is what makes them consistent across
a fleet — not what makes them trustworthy (see Consequences).

## Consequences

- **The three message properties travel as one carrier, for a size budget.** Inlining
  `content_type`, `type` and `message_id` as three strings on both option structs took
  them from 48 to 96 bytes, past the 80-byte `hugeParam` bound gocritic enforces on the
  by-value publish path — a bound this change may not widen and may not annotate away.
  They therefore travel as a single `*publishdoor.MessageProps`, +8 bytes, which is why
  every read of them is nil-guarded and why a nil carrier must publish exactly as three
  empty strings did (`messaging/bytes_door_test.go` pins that arm).
- **Persistent messages hit the broker's disk.** Publish throughput on a queue that
  was previously transient will drop, and the broker's disk becomes part of the
  publish path. This is the durability the standard requires and there is no opt-out;
  size the broker's disk for it, and expect confirm latency to move.
- **A consumer switching on `content_type` sees new values.** Where every delivery
  used to read `application/octet-stream`, a typed publish now reads
  `application/json` (or `application/jose` when sealed) and an outbox row reads
  `application/json` or octet-stream depending on the arm that encoded it. A
  consumer that branches on the property must handle all three.
- **A consumer relying on transient delivery loses that behaviour.** A deployment
  using non-persistence as an implicit TTL — messages evaporating with the broker —
  now keeps them across a restart, and a queue that was durable but fed only
  transient messages will retain a backlog it used to shed.
- **A client from a consumer's own `MessagingClientFactory` publishes no `app_id`.**
  `FactoryResolver.MessagingClientFactoryWithOptions` hands a custom factory only
  `(url, log)` — no field of the options struct reaches it, and neither does the
  resolver's own app name — so such a client stamps an empty `app_id`, in keeping with
  every other `messaging.reconnect.*` knob that path already bypasses. A deployment that needs
  the property must either use the default factory or call
  `messaging.WithAppName` itself.
- **A resolver a caller builds itself carries no app name.** `NewFactoryResolver` is
  exported and sets `appName` on nothing; only the unexported
  `newFactoryResolverForConfig` that `app/bootstrap.go` calls does. So a caller pairing
  `NewFactoryResolver` with `MessagingClientFactoryWithOptions` outside the framework's
  bootstrap gets default-factory clients that publish with no `app_id`, and nothing
  reports it: there is no options field to leave unset and no seam that checks the value.
- **`app_id` is unauthenticated provenance metadata, not an identity.** It is free text the
  publisher asserts, useful for tracing, dashboards and triage — never an authorization,
  routing-trust or identity input. Any publisher on the bus can stamp any string, including
  this service's name; RabbitMQ validates only `user_id` against the connection's
  authenticated user, and the framework sets no `UserId` at all. A consumer that needs a
  broker-validated producer identity has to use `user_id`, which is bound to the broker
  credential rather than to `app.name`.
- **Two guards keep an over-long value off the frame, at startup and per publish.** Startup
  bounds `app.name` at the 255-byte AMQP shortstr ceiling (`config`'s `maxAppNameBytes`,
  `[C64.13]`) and bounds a declared publisher's `EventType`, which leaves as the `type`
  property. The per-publish pre-flight then judges the message properties AND the client's own
  app id, because `WithAppName` is exported: a client a consumer built itself carries a value
  the config bound never saw. It returns `ErrInvalidPublishDestination`, already the outbox
  shipper's poison classification, so a bad row parks instead of retrying — and the shared
  connection is never touched. Without the guard amqp091 would answer the unwritable frame by
  shutting down the whole Connection every publisher in the process shares, the precedent
  ADR-070 established for `CorrelationId`.
- **An outbox `[]byte` payload stays octet-stream.** That covers the
  persisted-sealed path and any hand-marshaled body: the row is honest about not
  knowing, not wrong about knowing. A producer that wants `application/json` on the
  wire hands `Publish` the struct and lets the outbox marshal it.
- **The streams lane still publishes without these properties.** The AMQP 0-9-1 lane is
  what changed; `messaging/streams` sets no message properties, and its only part in
  this change is that `stream_shipper.go`'s `Plan` strips the content-type stamp so a
  stream row cannot carry the framework's bookkeeping onto the wire. NKH1 §6.5 is
  therefore satisfied on the AMQP lane only — giving the stream lane its own properties
  is follow-up work, not something this change did.
- **A pre-upgrade backlog drains under two labels.** Rows enqueued before the upgrade
  carry no content-type stamp and so relay as `application/octet-stream`, while rows
  enqueued after it ship `application/json`. For the length of the drain a consumer sees
  both labels for the same event type, so it must not read the absence of
  `application/json` as a different message shape.
- **A row the publisher marshaled itself now always persists headers.**
  `marshalHeaders` returned SQL NULL for an untraced, tenant-less publish with no
  caller headers; such a row now carries at least the content-type stamp, so the
  `headers` column is non-NULL on more rows than before. Nothing reads that column
  outside the relay.

## References

- Issue #1545 (publish properties against the topology standard)
- NKH1 topology standard §6.2 (durability), §6.5 (message properties)
- [ADR-096](adr_096_typed_publish_door.md) — the typed publish door; the reason
  there is no caller-facing options struct to hang these on
- [ADR-097](adr_097_sealed_amqp_messages.md) — the sealed arm's compact JWS, and
  `x-outbox-event-id` as the ledger key the property mirrors rather than replaces
- [ADR-087](adr_087_messaging_tenancy_and_tenant_stamp.md) — the tenant stamp, whose
  enqueue-record-then-strip mechanism the content-type stamp copies
- [ADR-088](adr_088_outbox_ordered_leader_relay.md) — the ledger schema the
  content-type column would have had to migrate
- [ADR-070](adr_070_inbound_trace_identifier_validation.md) — the `CorrelationId` precedent
  for guarding a shortstr before it reaches the frame, and why the failure is
  connection-wide
- `messaging/amqp_client.go` (`preparePublishing`, `WithAppName`),
  `messaging/typed_publisher.go` (`contentType`), `messaging/messaging.go`
  (`publishOptions`), `internal/publishdoor/publishdoor.go` (`Options`),
  `outbox/publisher.go` (`marshalPayload`, `marshalHeaders`), `outbox/headers.go`
  (`headerContentTypeStamp`), `outbox/amqp_shipper.go`, `outbox/stream_shipper.go`,
  `app/factory_resolver.go`, `app/managers.go`, `app/bootstrap.go`
