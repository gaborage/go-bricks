# ADR-070: Inbound trace identifiers are validated at the trace seam

- **Status**: Accepted
- **Date**: 2026-08-19
- **Related**: [ADR-068](adr_068_delivery_pipeline.md) and [ADR-069](adr_069_pipeline_owns_settlement_timing.md) (the delivery pipeline both messaging lanes run on, which is where the AMQP door reaches this seam)

> **Amended (2026-08-19):** `tracestate` is scoped to the carrier that brought its
> parent, in addition to the size cap the Decision describes. It is retained only
> when the SAME carrier supplied a valid `traceparent`, and discarded otherwise —
> including when the surrounding context carries an inherited parent of its own.
> A `tracestate` annotates one `traceparent`; attaching one carrier's vendor state
> to a parent it never accompanied would re-emit it downstream under a trace it
> does not belong to. The size cap below still applies to whatever survives that
> scoping.
>
> **Amended (2026-08-19, mixed trace lineage):** every step now decides against the
> CARRIER rather than the surrounding context, so one delivery cannot straddle two
> traces. The traceparent-derived id is planted unless the SAME carrier also
> supplied a valid `X-Request-ID` — the Decision below describes the original
> guard, "is there no id yet", which let an id inherited from the caller outrank
> the carrier's own `traceparent`, leaving a delivery logging under one trace while
> its span hung under another. And a carrier that brings a valid `traceparent` but
> no usable `tracestate` now shadows any inherited `tracestate` with empty, so one
> trace's vendor state is never re-emitted under another's parent.
>
> **Amended (2026-08-19, the fourth door opened):** the Context below says
> `messaging/streams` "extracts no trace context today and becomes live the moment
> it routes onto the delivery pipeline". That moment has arrived: the streams lane
> now runs on the shared pipeline and extracts through this seam, so all four doors
> are live. Alternatives likewise reasons about covering the streams lane "when that
> door opens" — it is open. Both read as written at decision time.
>
> **Amended (2026-08-20, the doors this ADR did not reach):** the Decision below
> says "Only the HTTP door validated, via a private `validateRequestID` in
> `server`". That sentence was true of the request id and of nothing else. Three
> seams reached a framework sink without passing this validator at all, and all
> three are now closed on the same terms.
>
> **The HTTP ingress traceparent and tracestate.** `enrichTraceContext` read
> `req.Header` directly, so the seam this ADR built never saw the HTTP door's
> `traceparent`. It is now validated with `ValidateTraceParent` before it is
> planted, and the accompanying `tracestate` gets the carrier scoping the first two
> amendments describe plus the cap — the latter through a new exported
> `ValidateTraceState`, so both doors share the RULE and not merely the constant.
> The HTTP door also shadows an inherited `tracestate` with empty, as the messaging
> door does, rather than resting on a premise about which middleware ran first. The failure mode is
> drop-and-mint, never reject: an unusable traceparent leaves the context in
> exactly the state an untraced request produces, which every request without the
> header already exercises.
>
> **The response reflection.** `ensureTraceParentHeader` echoed the raw request
> header back onto the response from six call sites. It reads `c.Request().Header`
> itself, so validating at the context seam does not reach it; it validates its own
> read now. The access-log metadata reader does the same, at both the response and
> the request header, for the same reason `validateRequestID` already ran at both.
>
> **The AMQP properties and envelope.** `ExtractFromHeaders` guards a delivery's
> `headers` TABLE. `CorrelationId` and `MessageId` are content-header PROPERTIES;
> `RoutingKey` and `Exchange` are `basic.deliver` ENVELOPE metadata. No header
> extractor reaches either kind, which is why no amount of header validation was
> ever going to cover them, and the classic consume path read all four raw into
> log fields, span attributes and metric attributes. They are now
> resolved once per delivery, in `processMessage`, and the one verdict is threaded
> to every sink — re-judging per sink is how one of them stays open. `CorrelationId`
> and `MessageId` answer to `ValidateRequestID`; the routing key answers to a
> distinct rule, printable ASCII up to the 255-byte shortstr ceiling, because the
> request-id charset would discard the dotted key of essentially every real
> deployment. The charset is the load-bearing half: a CONSUMED routing key arrives
> through amqp091's `readShortstr` and is already ≤255 bytes, so the length half is
> belt. An identifier that fails is OMITTED from the sink rather than
> substituted or truncated — the receive span's own rule for a field the delivery
> did not carry. The `messaging/streams` lane is untouched because it SURFACES
> none of these three today — an AMQP 1.0 message does carry `Properties.MessageID`
> and `Properties.CorrelationID`, so the rule is kept reachable behind a plain
> string-triple constructor rather than welded to an `*amqp.Delivery`.
>
> **`tracestate` gains a charset; the grammar is still refused.** The Decision
> below says `tracestate` gets "a length cap and no grammar", justified by
> refusing an OpenTelemetry dependency underneath `server`, `messaging` and
> `outbox`. That argument covers `ParseTraceState`. It does not cover a
> control-byte check, which needs no dependency — and the cap alone let CR/LF, NUL
> and ESC through every door that is not HTTP, since Go's own header reader
> rejects those on an inbound HTTP request but an AMQP longstr carries any byte. A
> value this framework stores, re-emits on every outbound hop and persists in
> outbox rows must not be one `net/http` will later refuse to write, which turns a
> single cheap message into a client that burns its whole retry budget.
> `ValidateTraceState` is therefore the cap plus printable ASCII — a strict
> superset of the W3C list syntax, so it costs no interoperability, and still not
> the grammar.
>
> **The vouched set includes `Exchange`.** It is not publisher-controlled in the
> direct way the other three are — a consumer sees only exchanges bound to its own
> queue, and creating one needs configure permission — but that is a property of
> the deployment, not a guarantee the code holds, and RabbitMQ bounds an exchange
> name by length and the `amq.` reservation, not by charset. It reaches the same
> three sinks under the same rule, so it is judged by the rule rather than by an
> assumption about who holds which permission on a shared vhost. `ConsumerTag`
> stays out: it is the tag this process handed to `basic.consume`.
>
> **Omission is now visible.** Discarding a value silently would leave the operator
> nothing to search for, and the Consequences below name a log search as the
> detect. The consume lines stamp `identity_rejected` when a delivery carried a
> value validation refused — one bounded boolean, not the unbounded value it
> replaces — and the failure line stamps `delivery_tag`, the one identifier no
> publisher supplies, so a delivery whose every vouched field was dropped is still
> attributable.
>
> What is deliberately NOT extended: the emit side. `computeTraceParent` still
> prefers a `traceparent` already in the outgoing header map and takes it verbatim,
> and `extractTraceIDFromParent` still checks the trace-id's length rather than its
> charset. Ingress validation closes that transitively for values the framework
> itself put there, which leaves first-party code hand-setting the header map and
> outbox rows persisted before this change — neither remote-triggerable, and the
> second bounded by the backlog draining once. A guard on the publish path
> constrains a caller-facing capability rather than refusing attacker input, so it
> is a decision of its own rather than a belt to this one:
> [#1121](https://github.com/gaborage/go-bricks/issues/1121). Widening
> `ValidateRequestID`'s charset stays refused.

## Context

`trace.ExtractFromHeaders` stored every inbound identifier verbatim. The only
test applied to any extracted value was non-emptiness.

That is a remote availability attack on a shared resource, not a log-hygiene
problem. A consumed AMQP message carrying an `X-Request-ID` over 255 bytes is
stored raw; a handler that publishes downstream has that raw value assigned to
`amqp.Publishing.CorrelationId`; amqp091's `writeShortstr` refuses a shortstr
over 255 bytes; and amqp091 answers *any* frame-write error by tearing down the
whole `Connection` rather than failing that publish. One oversized header from
anyone who can publish to a consumed queue drops the connection every publisher
in the process shares.

The transactional outbox makes it durable. Poisoned headers are persisted with
no length constraint and replayed by the relay, which classifies every
non-cancel publish failure as connectivity and retries forever, so a stored
record re-kills the connection on every relay cycle.

Three doors reached this code with no validation of their own: the AMQP classic
consume path, the outbox relay, and the exported extractor any consumer can
call. A fourth — `messaging/streams` — extracts no trace context today and
becomes live the moment it routes onto the delivery pipeline. Only the HTTP door
validated, via a private `validateRequestID` in `server`.

## Decision

**The validator lives in `trace`, exported, and `server` calls it.** `trace` is
an import leaf and `server` already reaches it, so this is the only non-cyclic
shape. The pattern is reused byte-for-byte from `server` — `^[A-Za-z0-9_-]{1,128}$`
— rather than inventing a second number for the same question.

**The failure mode is discard-and-regenerate, never truncate.** Truncation
silently forges correlation by mapping distinct upstream identifiers onto one.
W3C, OpenTelemetry's `Extract` and Heroku all independently refuse it. Nor is the
delivery rejected: the messaging lanes have no way to reject one, and the HTTP
precedent never returned 4xx for a bad request id either.

**A rejected `X-Request-ID` falls through to the traceparent-derived id, then to
a fresh UUID.** This needed no new code — the derivation is already guarded by
"is there no id yet", so not planting the rejected value opens the guard. A
gateway emitting a slightly-off id alongside a good traceparent keeps its
correlation.

**`traceparent` is validated against the spec's grammar** —
`^[0-9a-f]{2}-[0-9a-f]{32}-[0-9a-f]{16}-[0-9a-f]{2}(-[[:graph:]]+)*$`, the
pattern `trace/validate.go` compiles verbatim: the four version-00 fields, then
the optional dash-delimited suffix a future version may carry (see below). It
rejects the all-zero
trace-id and parent-id as OpenTelemetry's own `Extract` does, and rejecting
version `ff`, which the spec forbids outright. The previous check was
length-only, which left an attacker 32 arbitrary non-hyphen bytes inside a
"valid" traceparent.

Validation is strict on the grammar but **forward-compatible on the version**,
because the spec makes later versions additive. Version `00` is exactly 55
characters and anything longer is malformed. Versions `01`–`fe` may append
further dash-delimited fields, which a receiver parses past: trace-id, parent-id
and flags are read from the version-00 positions and the remainder is ignored,
subject to the extra fields being delimited, printable non-space ASCII, and the
whole value fitting `MaxTraceParentBytes` (255). Pinning the length at 55 instead
would have been a stricter check that fails the wrong way — it would discard
genuine upstream traces the day a version `01` appears on the wire, which is a
correlation outage caused by our own validator rather than by an attacker.

The suffix bounds are where this seam deliberately parts company with
OpenTelemetry's propagator, which accepts any suffix whatsoever. It can afford
to: it parses the value and discards the remainder. This seam **stores** the raw
traceparent and re-emits it verbatim on every outbound hop, so accepting an
unconstrained suffix would turn it into a relay for whatever an upstream caller
attached — CR/LF into an outbound header, or kilobytes held per delivery. Both
bounds cost forward compatibility nothing, because no defined field uses a
character they exclude. This also closes an outbound vector: `forceAlignTraceID`
re-emits the raw request id whenever the accompanying traceparent is malformed,
which was the one condition under which a poisoned value escaped onto the next
hop.

**`tracestate` gets a length cap and no grammar.** Validating the grammar means
`go.opentelemetry.io/otel/trace`'s `ParseTraceState`, which would put an
OpenTelemetry dependency underneath `server`, `messaging` and `outbox` for a
value this framework only stores and forwards. The cap bounds the real harm —
unbounded storage and unbounded re-emission — at a fraction of the coupling.
**This is a deliberate cheaper choice, not an oversight.**

**`CorrelationId` is capped again at its assignment site**, independently of the
seam. `WithTraceID` and `EnsureTraceID` are exported: the seam guards the
framework's doors and cannot guard against the framework's own public API. This
is defense in depth rather than redundancy — the two checks answer different
questions, and the cost of being wrong is a torn-down shared connection rather
than a bad log line.

## Consequences

Previously-accepted values are now discarded, so this is a behavior change with
a migrations atom (`[C60.8]`). An operator cannot grep their Go code to discover
that an upstream gateway emits a 200-character request id — the detect is a log
search, and the atom's gate is `when: always`.

`httpclient` needs no change. It sets the header straight from the context, but
once the seam is in place the only way to get a bad value there is a consumer
planting one through the exported API, and Go's `Transport` already refuses CTL
bytes in header values.

Every `X-Request-ID` and `traceparent` literal already in the repository's tests
passes both checks, so the existing suite could not have caught a regression
here. The new tests carry the entire proof.

## Alternatives considered

**Validate in `messaging/registry.go`, at the AMQP door.** Rejected: it leaves
the outbox relay and the exported extractor open, and it would not cover the
streams lane when that door opens.

**Truncate to 128 bytes instead of discarding.** Rejected on correlation
grounds, above — and it would still be wrong: a truncated id is a *plausible*
id, so the damage is silent rather than loud.

**Reject the delivery.** Rejected: a consumer has no way to refuse a message
that has already arrived, and doing so would convert an availability attack on
the connection into an availability attack on the queue.

**Validate the `tracestate` grammar with OpenTelemetry's parser.** Rejected on
dependency-direction grounds, above.
