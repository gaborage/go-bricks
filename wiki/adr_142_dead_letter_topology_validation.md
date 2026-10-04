# ADR-142: Declarations Refuse a Dead-Letter Route the Set Shows Cannot Park

**Status:** Accepted
**Date:** 2026-10-04
**Issue:** #1732

## Context

The framework nacks a failed delivery without requeue, so a queue's `x-dead-letter-exchange`
is the only thing between a handler error and a dropped message. `DeclareQueueWithDLQ` builds a
complete route — a fanout DLX, a parking queue, and a binding with an empty key — and
[ADR-118](adr_118_exchange_redeclaration_conflicts.md) refuses a conflicting re-declaration of
that DLX.

Nothing in `Declarations.Validate()` read a queue's `x-dead-letter-exchange` or
`x-dead-letter-routing-key`. A route built through the documented raw-`Args` escape hatch, or by
mutating `d.Exchanges`/`d.Queues` after registration, passed validation even when it could park
nothing: a DLX name no declaration provides, a fanout DLX with no binding, a topic DLX whose only
binding key is `""`, and `DeclareQueueWithDLQ` followed by retyping its DLX to `topic` through
`d.Exchanges` — which also bypasses ADR-118, since no second `RegisterExchange` call happens. By
RabbitMQ's DLX semantics each shape drops dead-lettered messages instead of parking them. Startup
did not fail, nothing was logged, and per-tenant replay copied the shape into every tenant.

## Decision

`Declarations.Validate()` runs `validateDeadLetterTopology` right after the exchange-shape check.
It judges each queue whose `Args["x-dead-letter-exchange"]` is a string X, with K the
`Args["x-dead-letter-routing-key"]` when that is a string. Both are read from the stored
declarations at validation time, so a mutation through `d.Queues` or `d.Exchanges` is judged too.

1. **X is `""` (the default exchange):** pass. #1549 owns the default exchange as a DLX.
2. **X is non-empty and absent from the set:** refuse, with the same "absent from this
   declaration set (a local check; the broker was not contacted)" wording and remedy as a
   dangling binding or publisher reference: declare it, or mark it external with
   `DeclareExternalExchange` when another service owns it.
3. **X cannot be judged:** pass when X is external (it carries no type,
   [ADR-119](adr_119_external_exchange_passive_verification.md)), of type `headers` (routing depends on
   `x-match`, not the key), of an `x-` plugin type, or carries an `alternate-exchange` argument
   (an unroutable message goes elsewhere). The set cannot show how such an exchange routes.
4. **X is a locally declared `fanout`, `direct` or `topic` exchange with no binding to it:**
   refuse. A fanout with at least one binding passes.
5. **X is `direct` or `topic` and K is set:** refuse unless some binding to X matches K. Direct
   compares for equality; topic uses AMQP pattern matching (`*` one word, `#` zero or more), via
   a private matcher in `messaging/topic_match.go` with no new dependency.
6. **X is `direct` or `topic`, K is unset, and every binding to X has the key `""`:** refuse. A
   dead-lettered message keeps its original routing key, and a `""` binding matches only an
   original key of `""`.
7. **X or K is not a string:** skip the queue. A non-string value is not refused as a shape
   error here.

The code checks rule 7 and rule 1 first, then rule 2, rule 3, rule 4, passes a bound fanout, and
then applies rule 5 when K is set or rule 6 when it is not. Every offending queue is reported in
one `errors.Join`, in sorted queue order, like the exchange-shape and quorum-shape checks. Each
error names the queue, the DLX, the DLX's type when it is declared, and the rule
(`(dead-letter rule N)`); a rule-5 error also names K. No other `Args` value is rendered.
`DeclareQueueWithDLQ` output passes by construction, and its behavior is unchanged. This
complements ADR-118's re-declaration check and does not replace it.

## Alternatives considered

**Require the DLX to be `fanout` or `headers`, or bound with `#` or with exactly K.** This was
the issue's first predicate. Rejected: it refuses working routes — a direct DLX bound with the
primary queue's name, a non-`#` topic pattern that matches K, an external DLX, the default
exchange, and an alternate-exchange DLX — while passing a fanout with no binding and any headers
DLX.

**Refuse only an absent DLX (rule 2).** It catches the likeliest drop. Rejected as the whole
change: an unbound fanout and a retyped helper DLX are drops the set shows just as plainly, and
leaving them in would keep the ADR-118 retype bypass open.

**Log a WARN.** Rejected for the same reason ADR-118 rejected it: a service that boots green with
a known-broken route is how this shipped unnoticed.

## Consequences

- **A declaration set that used to boot now refuses** when a queue's dead-letter route is absent
  or, by rules 4–6, visibly unroutable. Migration is [migrations.md](migrations.md) `[C73.6]`.
- **Accepted cost, rule 4:** a service whose DLX's parking side is owned by another service now
  fails, because the binding lives in that other set. That service declares the DLX; this one
  marks it external with `DeclareExternalExchange`.
- **Accepted cost, rule 6:** a source that publishes with the routing key `""` loses a working
  direct or topic DLX bound with `""`. Declare the DLX fanout, bind a key the messages carry, or
  set `x-dead-letter-routing-key` to a bound key.
- **Accepted cost, rule 3:** an alternate exchange set by an operator policy, like any policy-set
  DLX, is invisible to the declaration set, so a route it would rescue is still refused.
- **Out of scope:** the default exchange as a DLX (#1549), headers `x-match` and plugin routing,
  retyping a non-DLX exchange after registration (#1714's second harm), at-least-once
  dead-lettering (#1568), and publish-side routability (#1819).

## References

- [migrations.md](migrations.md) `[C73.6]`: detect, the error texts and the exits
- [ADR-118](adr_118_exchange_redeclaration_conflicts.md): the re-declaration conflict check this complements
- [ADR-119](adr_119_external_exchange_passive_verification.md): an external exchange carries no type
- [ADR-106](adr_106_dlq_helper_declares_quorum_queues.md): `DeclareQueueWithDLQ`, whose route passes by construction
- [ADR-040](adr_040_declaration_args_passthrough.md): declaration `Args` reach the broker unjudged
- [wiki/messaging.md](messaging.md): the raw-`Args` escape hatch and its rules
- `messaging/dead_letter_topology.go` (`validateDeadLetterTopology`, `deadLetterRouteError`, `routingIsVisible`)
- `messaging/topic_match.go` (the private AMQP topic-pattern matcher)
- `messaging/declarations.go` (`Validate`, `absentExchange`)
