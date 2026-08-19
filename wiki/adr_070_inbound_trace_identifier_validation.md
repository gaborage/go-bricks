# ADR-070: Inbound trace identifiers are validated at the trace seam

- **Status**: Accepted
- **Date**: 2026-08-19
- **Related**: [ADR-068](adr_068_delivery_pipeline.md) and [ADR-069](adr_069_pipeline_owns_settlement_timing.md) (the delivery pipeline both messaging lanes run on, which is where the AMQP door reaches this seam)

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
`^[0-9a-f]{2}-[0-9a-f]{32}-[0-9a-f]{16}-[0-9a-f]{2}`, rejecting the all-zero
trace-id and parent-id as OpenTelemetry's own `Extract` does, and rejecting
version `ff`, which the spec forbids outright. The previous check was
length-only, which left an attacker 32 arbitrary non-hyphen bytes inside a
"valid" traceparent.

Validation is strict on the grammar but **forward-compatible on the version**,
because the spec makes later versions additive. Version `00` is exactly 55
characters and anything longer is malformed. Versions `01`–`fe` may append
further dash-delimited fields, which a receiver parses past: trace-id, parent-id
and flags are read from the version-00 positions and the remainder is ignored,
subject to the extra fields actually being delimited. Pinning the length at 55
instead would have been a stricter check that fails the wrong way — it would
discard genuine upstream traces the day a version `01` appears on the wire, which
is a correlation outage caused by our own validator rather than by an attacker. This also closes an outbound vector: `forceAlignTraceID`
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
