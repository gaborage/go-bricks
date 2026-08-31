# ADR-092: A Typed Stream Consumer's Payload Failure Is Skipped, Never Held

- **Status**: Accepted
- **Date**: 2026-08-31
- **Related**: [ADR-089](adr_089_per_tenant_hold_on_the_streams_lane.md) (the hold
  this decision carves an exception in), [ADR-059](adr_059_streams_consumption.md)
  (skip-on-failure, which a payload failure falls back to),
  [ADR-091](adr_091_streams_opt_in_registration.md) (why the two lanes share
  through `messaging/internal/*` instead of importing each other),
  [ADR-084](adr_084_response_error_details_carry_no_request_input.md) (the same
  no-input-in-errors rule on the HTTP side)

## Context

`streams.Handler` hands every consumer raw bytes. So every stream consumer that
wants a struct writes the same three steps itself — `json.Unmarshal`, the
validation call, and the two error branches — which is exactly the copy-paste the
AMQP lane's `messaging.DeclareTypedConsumer` was built to end. Worse, the branch
consumers write by hand is the one issue #1176 had to fix in the framework:
rendering a decode or validation failure by interpolating the cause echoes the
partner's own bytes into a log line, because `json.UnmarshalTypeError` carries the
rejected literal, `json.SyntaxError` quotes the offending byte, and a validator
namespace interpolates map keys verbatim. Leaving that branch to every consumer
relocates a solved hazard rather than solving it.

The two lanes are deliberately decoupled: `messaging/streams` does not import
`messaging`, so a service that never links the stream client carries none of it
(ADR-091). A typed consumer for the streams lane therefore cannot reuse
`messaging.PayloadError` by importing it.

ADR-089 then adds a second question the AMQP lane never had to answer. A holding
consumer parks a failed delivery per tenant and gates that tenant's later
messages behind it, and the `inbox-hold-drain` job replays each row until it
succeeds, deferring the tenant on every failure. A decode failure replayed that
way fails identically forever.

## Decision

### 1. The payload-error core hoists; each lane exports a thin type

`messaging/internal/payloaderr` owns the decode step, the shared validator
instance, the codec seam and the payload-free rendering — `Body`, its fail-closed
summary substitution, and the redact-on-read namespace list. `messaging` and
`messaging/streams` each export their own `PayloadError` over a `*payloaderr.Body`
with their own prefix, subject and sentinels: `messaging` renders
`event "OrderCreated"`, `streams` renders `consumer "order-projector"`. The AMQP
lane's exported surface is unchanged.

### 2. `streams.DeclareTypedConsumer[T]` mirrors the AMQP lane's shape

Decode (JSON) → validate (go-playground struct tags) → typed handler, with
`DeclareTypedConsumerWithMeta`, `DeclareTypedSuperStreamConsumer` and
`DeclareTypedSuperStreamConsumerWithMeta` covering the lane's other two axes.

There is deliberately **no exported `NewTypedHandler`** on this lane, which the
AMQP lane does have. A typed declaration carries a poison *screen* next to its
handler (§3), and a bare `Handler` handed to `DeclareConsumer` could not carry
one — a typed consumer assembled that way would silently reintroduce the failure
mode this ADR forbids. Making the declaration the only entry point makes the
invariant structural.

### 3. A payload failure is deterministic poison: skip immediately, never hold

| failure | in-place retry | hold | offset |
| --- | --- | --- | --- |
| decode / validate, tenant not held | none — returned `Permanent` | never parked | not committed; skipped |
| decode / validate, tenant already held | none — screened before the gate | never parked | not committed; skipped |
| handler error, `Hold` set | per `RetryOptions` | parked, tenant held | committed after the park lands |
| handler error, no `Hold` | per `RetryOptions` | n/a | not committed; skipped (ADR-059) |

The same bytes fail the same way on every attempt and every replica, so retrying
in place spends the partition's own goroutine on a certainty. Parking is worse
than useless: the drain replays each row until it succeeds, so a parked
undecodable body defers its tenant on every pass, forever. That inverts the
hold's purpose — it exists to preserve one tenant's order through a *transient*
failure, not to stop that tenant permanently on a message no replay can get past.

Two mechanisms enforce it, because a holding consumer reaches the hold by two
different paths:

- **`consumerRunner.parks` bypasses a `*PayloadError`.** A finished delivery whose
  error is one — read through the wrap chain, so the `Permanent` marker and a
  caller's own `%w` are both transparent — settles as a skip.
- **The screen runs before the gate.** A delivery for a tenant that is already
  held is parked *without running*, so nothing downstream could tell that this
  body would never have decoded. The runner asks the typed declaration's screen
  first, which decodes exactly the same `T` through exactly the same decoder. It
  runs only on that gated path, so an ordinary delivery still decodes once.

Skipping poison does not disturb the held tenant's order: the messages that *can*
be handled still arrive behind the one they are held behind.

## Consequences

**Positive.** A stream consumer declares a struct instead of writing the decode
branch, and the payload-free rendering is the framework's on both lanes rather
than each consumer's. A holding consumer can no longer be stopped indefinitely by
one malformed message. The two lanes' failure vocabulary stays in step by
construction, not by review.

**Negative.** A payload failure is ultimately *dropped*, and nothing durable
records it. Precisely: its own offset is never committed, so the skip becomes
final only once a LATER delivery succeeds and commits a higher offset — until
then a restart resumes from the last stored offset and redelivers the poison,
which is harmless because the screen and the handler reject it identically and
skip it again. That is ADR-059's existing settlement, but it means poison lives
in the failure log line and the consume metric alone, never in a ledger an
operator can read back. A consumer that needs the body kept should validate
loosely (`T` with no `validate` tags) and park the rejection itself from inside
the handler.

**Neutral.** `streams.PayloadError.Unwrap()` still reaches the raw cause, which
MAY carry payload-derived text. Logging it is opt-in and on the caller, exactly as
on the AMQP lane.

## Migration Impact

None. Everything here is additive: `DeclareConsumer` and
`DeclareSuperStreamConsumer` behave exactly as before, and a hand-written
`Handler` carries no screen, so its gated deliveries park as they did under
ADR-089.
