# ADR-106: The Dead-Letter Helper Declares Quorum Queues on Both Sides

**Status:** Accepted
**Date:** 2026-09-08
**Issue:** #1548

## Amendment (2026-10-04, #1568): at-least-once dead-lettering is an opt-in on the primary

`DeadLetterSpec` gains `DeadLetterStrategy string`. Its one accepted non-empty value is
`messaging.DeadLetterStrategyAtLeastOnce` (`"at-least-once"`), which makes the helper write
`x-dead-letter-strategy=at-least-once` and `x-overflow=reject-publish` on the primary, so the
broker re-publishes a dead-lettered message with internal confirms and keeps it in the primary
until the parking queue confirms it. Empty — the `nil` spec and `&DeadLetterSpec{}` included —
writes neither argument, so every existing declaration and its `Hash()` are unchanged and the
default stays at-most-once: flipping it would redeclare every existing primary with new `Args`
and fail startup fleet-wide. Any other value, the literal `"at-most-once"` included, is a
`Validate` error rather than a synonym for empty, because the broker treats an explicit
at-most-once argument and an absent one as inequivalent. A string, not a bool, for the reason
Alternative B gives for `QueueType`. Three decisions depart from the rules below:

- **Primary only — an exception to "one field, both queues".** Both arguments are properties
  of the queue a message is dead-lettered FROM, so writing them on the parking queue would
  change nothing about the hop into it. When several primaries share one dead-letter exchange or parking queue, each carries
  its own strategy; a primary re-declared without the opt-in merges under the usual rule
  (absent keys do not conflict) and keeps both arguments.
- **Any `x-overflow` other than exactly `reject-publish` is refused — an exception to "an
  existing value wins".** The broker accepts a quorum queue carrying the strategy with no
  `x-overflow`, with `drop-head`, or with `reject-publish-dlx` (which quorum queues do not
  support), and silently falls back to at-most-once with only a broker-log warning; it never
  refuses those shapes. So for an opted-in primary the framework does not defer to a value
  already in `Args`: `Validate` judges the FINAL registered declaration — after any
  `d.Queues[name].Args` edit the helper's godoc invites — and refuses every other overflow
  shape, absent included, by queue name and argument key, never the value.
- **Quorum only.** An opted-in primary whose resolved `x-queue-type` is not quorum —
  `QueueType: QueueTypeClassic`, or a classic type already in its `Args`, which the helper
  never overwrites — is refused at `Validate`. The broker refuses the strategy on a classic
  queue and an unknown strategy value itself, but only mid-startup and once per tenant;
  refusing both by name in the validate-once path is the fail-fast pattern below.

These refusals are aggregated with the queue-type and quorum-shape errors, and per-tenant
replay is unchanged. Not a break: nothing changes for a caller who does not opt in, so there
is no migrations atom. Opting in on an EXISTING primary is a topology migration — the broker
refuses the new arguments with `406 PRECONDITION_FAILED` (inequivalent arg) until the queue is
drained and recreated, or the strategy is applied as an operator policy instead
(`dead-letter-strategy=at-least-once`, `overflow=reject-publish` on quorum queues). The
prerequisite is RabbitMQ ≥ 3.10 with a quorum primary; the `stream_queue` feature flag is
Required from 3.11.0, so it matters only on a 3.10.x broker. Costs and the remedies are in
[wiki/messaging.md](messaging.md#dead-lettering). Retry queues (#1549) and a general rule
judging hand-set `x-dead-letter-strategy` in raw `Args` (#1732) are out of scope.

## Context

`Declarations.DeclareQueueWithDLQ` (`messaging/helpers.go`) is the one-call form of
the dead-letter route: it registers the primary queue, a derived `<queue>.dlx` fanout
exchange, a derived `<queue>.dlq` parking queue, and the binding between them, and sets
`x-dead-letter-exchange` on the primary. `DeadLetterSpec` lets a caller override the
derived exchange name, the parking-queue name and `x-dead-letter-routing-key`.

What the spec could not say was what KIND of queue either side is. The helper sets no
`x-queue-type` on the primary queue or on the parking queue, so both take whatever queue
type the broker defaults to for the vhost. That default is the one thing a parking queue
should not be left to: the queue exists to RETAIN a message nobody could handle, and
the framework's whole reason for creating it (ADR-040: failed deliveries are nacked
without requeue, so an unparked message is gone) is retention across the failure that
parked it — including the loss of the node the queue happens to live on.

That is a claim about the queue, not about the hop into it. A quorum queue is
replicated across nodes, so a message already IN it survives the loss of a node; the
dead-letter hop from the primary to the DLX keeps RabbitMQ's default
`dead-letter-strategy=at-most-once`, which re-publishes without internal confirms, so a
message can still be lost in transit between the two queues on a target or node failure.
Loss-resistant dead-lettering additionally needs `x-dead-letter-strategy=at-least-once`
with `x-overflow=reject-publish` on a quorum primary — a separate opt-in that this
decision does not make; the 2026-10-04 amendment above adds it.

ADR-040 already named the escape hatch and already named the target:
`Args["x-queue-type"] = "quorum"` reaches the broker, and that ADR calls quorum
"RabbitMQ's recommended production queue type". But the hatch is reachable only on a
declaration the caller builds itself — `messaging.NewQueue` plus `RegisterQueue`, or a
post-registration mutation of `d.Queues["<queue>.dlq"].Args`. Both spellings are the
raw-`Args` path, and neither is available on the helper's own two derived declarations
without reaching back into the registry for a queue the helper just created. The
ergonomic door and the recommended topology pointed in different directions.

Redeclaring an existing queue with different arguments is refused by the broker with
`406 PRECONDITION_FAILED`, and a queue's type is one of those arguments. So this
decision cannot be a silent improvement: whichever type the helper picks, it decides
whether an existing deployment's queues still declare.

## Decision

**`DeadLetterSpec` gains a `QueueType string` field, applied as `x-queue-type` to BOTH
queues the helper touches, and an empty value resolves to quorum.**

- **Quorum is the default.** An empty `QueueType` — including the `nil` spec and the
  zero-value `&messaging.DeadLetterSpec{}` — resolves to `QueueTypeQuorum`. The
  exported constants `messaging.QueueTypeQuorum` and `messaging.QueueTypeClassic` are
  the caller-facing values; an explicit `QueueTypeClassic` is honoured, on both queues.
  This is the breaking part of the change: it moves the queue type of the primary queue
  AND of the parking queue for every existing caller of `DeclareQueueWithDLQ`.
- **One field, both queues.** The value applies to the primary queue and to the derived
  parking queue — `DeadLetterSpec.ParkingQueue` when configured, the derived `<queue>.dlq`
  otherwise. A dead-letter route whose two halves have different
  durability guarantees is not a posture anyone asked for: the primary decides whether
  the message survives long enough to be dead-lettered, the parking queue decides whether
  it survives after parking, and a single knob cannot express half a route. Neither half
  makes the HOP between them reliable — that is the at-most-once strategy above, which the
  2026-10-04 amendment lets a primary opt out of. A caller who genuinely wants
  the two sides to differ writes the odd side by hand through the passthrough below.
- **An existing `x-queue-type` wins.** The helper sets `x-queue-type` only on a queue
  that does not already carry one. So the ADR-040 passthrough — a `NewQueue` registered
  with the arg already set, or `d.Queues["<queue>.dlq"].Args["x-queue-type"]` written
  after the fact, which is the workaround this helper's godoc documents — keeps working
  and is never silently overwritten. This field is the ergonomic front door OVER that
  passthrough, not a replacement for it: `Args` remains the general-purpose door for
  every broker argument, this field is the named spelling of the one argument the helper
  itself has an opinion about.
- **An unknown `QueueType` is a declaration-time error, not a passthrough.** A value
  that is neither of the two constants fails validation rather than being forwarded for
  the broker to reject. The field exists precisely because callers should not have to
  spell broker argument values, so a typo in it is a framework-level mistake, and the
  general-purpose door for a value the framework does not know is still `Args`.
- **Pre-setting the argument on one queue leaves the other at the spec's value.** The
  precedence rule is per queue, not per route, so a deployment that wants a quorum
  primary with a classic parking queue registers the `.dlq` with `x-queue-type: classic`
  and lets the spec resolve the primary — the two halves need not agree.
- **Quorum-incompatible shapes fail at declaration time.** A queue that resolves to
  quorum and is non-durable, auto-delete or exclusive, or carries `x-max-priority` or
  `x-queue-mode` (lazy), is refused with a validation error naming what conflicts.
  Quorum queues do not support those shapes, so without this check the deployment
  learns of the conflict from the broker as `PRECONDITION_FAILED` mid-startup, against
  a message that names an AMQP argument rather than the call site that produced it.
  Fail fast, with the conflict named.
- **The checks live in the validate-once path.** Declarations are validated once and
  replayed per tenant, so the queue-type resolution and both refusals are decided on the
  single validated declaration set and cost nothing per tenant. Per-tenant replay is
  unchanged: it replays the same resolved `Args` it always did.

**The fleet is quorum-capable.** Every broker this framework is deployed against supports
quorum queues — confirmed by the maintainer at triage on 2026-09-08 — so the default flip
does not strand a deployment on an old broker that cannot honour it. CI declares against
`rabbitmq:4.3.5-management-alpine` (`.github/workflows/ci-v2.yml`). This is recorded as
a fact about the fleet the framework is developed for, not as a claim about every broker
a consumer might point a service at; a consumer whose broker is older sets
`QueueType: messaging.QueueTypeClassic`.

## Alternatives

**A — default to classic, so nothing breaks.** Add the field, resolve an empty value to
classic, and let a caller opt into quorum. Rejected: it leaves the default posture at the
one the ADR-040 escape hatch exists to escape. The parking queue is created by the
framework, for a retention purpose the framework chose, and defaulting it to the type the
framework itself calls non-production makes the ergonomic door the wrong door — the exact
divergence this ADR closes. The break is real, it is compiler-invisible, and it is
therefore documented as an atom rather than avoided by picking the weaker default.

**B — a `bool` field (`Quorum bool`).** Two states, no unknown value to validate, and a
zero value that reads as "not quorum". Rejected on both counts: the zero value would have
to mean quorum to keep the default above, so `Quorum: false` would either be unreachable
or mean the opposite of what it says; and a bool cannot grow a third queue type without
a second break.

**C — validate against the broker instead, at replay.** Let the incompatible shapes
reach the declare call and translate the broker's `PRECONDITION_FAILED` into a better
message. Rejected: the translation would run per tenant, on the replay path, and it
would have to reverse-engineer which argument the broker objected to from an error string
that names neither the call site nor the spec field. Everything needed to refuse these
shapes is already in the declaration set at validate time.

**D — apply the type to the parking queue only.** The parking queue is the one the
framework invents, so leave the primary alone and break nothing about it. Rejected: it
splits the route's guarantee in half. A message dropped with the classic primary's node
never reaches the quorum parking queue at all, so the parking guarantee would be bounded
by the weaker half while reading as the stronger one.

## Consequences

- **An existing deployment's queues must be reconciled before the bump.** An existing
  classic parking queue — `DeadLetterSpec.ParkingQueue` when set, else `<queue>.dlq` — and an
  existing classic PRIMARY queue cannot be redeclared
  as quorum: the broker refuses with `PRECONDITION_FAILED` and startup fails. The
  population is every deployment that already has a parking queue declared by this helper,
  whatever its name. The
  remedy is either `QueueType: messaging.QueueTypeClassic`, which keeps today's topology
  exactly, or deleting/migrating the queues. Tracked as migrations atom **[C64.12]**.
- **Nothing in a consumer's build flags this.** `DeadLetterSpec` gains a field; every
  existing call site — `nil`, `&messaging.DeadLetterSpec{}`, or a spec setting only the
  name overrides — still compiles and still means what it said. The change is visible
  only at declaration time, so it is a topology change, not a compile break.
- **A quorum-incompatible queue that used to start now does not.** A caller who declared
  a non-durable, auto-delete or exclusive queue through this helper, or set
  `x-max-priority` or `x-queue-mode` on it, gets a validation error at startup naming the
  conflict. Both remedies are available: drop the incompatible shape, or set
  `QueueType: messaging.QueueTypeClassic` and keep it.
- **The ADR-040 passthrough is now load-bearing in a second way.** It remains the door
  for every other broker argument, and it is additionally the only way to give the two
  halves of one dead-letter route different queue types — deliberately, since the helper
  has one field for both.
- Callers who want the recommended production topology stop needing the raw-`Args`
  workaround at all, which is the ergonomic point of the field.

## References

- [ADR-040](adr_040_declaration_args_passthrough.md) — declaration `Args` reach the
  broker; names `Args["x-queue-type"] = "quorum"` as the sanctioned passthrough and
  quorum as RabbitMQ's recommended production queue type. This field is the ergonomic
  front door over that passthrough, which is unchanged and still wins where it is set
- [ADR-033](adr_033_outbox_retry_count_status_parking.md) — the outbox-side parking
  story this helper's broker-native parking complements
- [wiki/messaging.md](messaging.md#dead-lettering) — the consumer-facing dead-letter
  documentation
- [wiki/migrations.md](migrations.md) atom `[C64.12]` — the detect/gate/apply runbook
  for the default flip
