# ADR-106: The Dead-Letter Helper Declares Quorum Queues on Both Sides

**Status:** Accepted
**Date:** 2026-09-08
**Issue:** #1548

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
  `<queue>.dlq` parking queue. A dead-letter route whose two halves have different
  durability guarantees is not a posture anyone asked for: the primary decides whether
  the message survives to be parked, the parking queue decides whether it survives after
  parking, and a single knob cannot express half a route. A caller who genuinely wants
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
- **Quorum-incompatible shapes will fail at declaration time.** A queue that resolves to
  quorum and is non-durable, auto-delete or exclusive, or carries `x-max-priority` or
  `x-queue-mode` (lazy), is a shape quorum queues do not support, so today the deployment
  learns of the conflict from the broker as `PRECONDITION_FAILED` mid-startup, against
  a message that names an AMQP argument rather than the call site that produced it.
  Refusing those shapes with a validation error naming what conflicts is the fail-fast
  half of this decision, and **declaration-time shape validation lands in the follow-up
  link of this stack**; this change ships the queue-type resolution alone.
- **The checks live in the validate-once path.** Declarations are validated once and
  replayed per tenant, so the queue-type resolution and the unknown-value refusal are
  decided on the single validated declaration set and cost nothing per tenant — as the
  shape refusal will be. Per-tenant replay is unchanged: it replays the same resolved
  `Args` it always did.

**The fleet is quorum-capable.** Every NovoPayment broker supports quorum queues —
confirmed by the maintainer at triage on 2026-09-08 — so the default flip does not
strand a deployment on an old broker that cannot honour it. CI declares against
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
  classic `<queue>.dlq` — and an existing classic PRIMARY queue — cannot be redeclared
  as quorum: the broker refuses with `PRECONDITION_FAILED` and startup fails. The
  population is every deployment that already has a `.dlq` declared by this helper. The
  remedy is either `QueueType: messaging.QueueTypeClassic`, which keeps today's topology
  exactly, or deleting/migrating the queues. Tracked as migrations atom **[C64.12]**.
- **Nothing in a consumer's build flags this.** `DeadLetterSpec` gains a field; every
  existing call site — `nil`, `&messaging.DeadLetterSpec{}`, or a spec setting only the
  name overrides — still compiles and still means what it said. The change is visible
  only at declaration time, so it is a topology change, not a compile break.
- **A quorum-incompatible queue fails, for now at the broker.** A caller who declared
  a non-durable, auto-delete or exclusive queue through this helper, or set
  `x-max-priority` or `x-queue-mode` on it, gets `PRECONDITION_FAILED` mid-startup until
  the follow-up link names the conflict at declaration time. Both remedies are available
  already: drop the incompatible shape, or set
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
