# ADR-119: A Declaration Set May Reference an Exchange Another Service Owns, Verified Passively

**Status:** Accepted
**Date:** 2026-09-19
**Issue:** #1760

## Context

`Declarations.Validate` required every binding's exchange — and every typed publisher's — to be
declared in the same set, so the single-declarer pattern could not be expressed. One service owns a
shared exchange; every other service only binds to it or publishes through it. To say that today, a
non-owner must repeat the owner's declaration and accept the shape race: two active
`exchange.declare` calls that disagree on `Type` or a flag are a `PRECONDITION_FAILED` at the
broker, which since [ADR-113](adr_113_amqp_topology_redeclare_on_reconnect.md) is skipped until the
process restarts. [ADR-118](adr_118_exchange_redeclaration_conflicts.md) made the repeat safe
*within* one process's declaration set; across services nothing can align the call sites, because
the owner's shape is not in this repository.

The validator's own wording made this harder to diagnose. `binding references non-existent
exchange: X` describes a map lookup that never contacts the server, but reads as a broker fact — it
sends the reader to `rabbitmqctl` and the management UI, which show a healthy exchange.

## Decision

- **A name-only external declaration.** `DeclareExternalExchange(name)` records an exchange this
  service references but does not own. It satisfies reference validation for bindings and typed
  publishers exactly as a locally declared one does. Name only: a passive `exchange.declare`
  ignores every field except the name and no-wait (AMQP 0-9-1), so a type, a flag or an `Args` key
  on one would be a value nobody reads and the owner alone decides. `Validate()` refuses an
  external declaration that carries any of them, naming every field set.
- **Verified on every declare pass, never created.** The marker rides on the declaration as
  `ExchangeDeclaration.Passive`, and the client issues `exchange.declare` with `passive=true`
  instead of creating the exchange. That covers the startup pass and each redeclare pass on a new
  channel generation without a second code path: the recorded declarations are the same list. A
  missing exchange is a channel-level 404 that ends the pass, and the next generation retries it,
  exactly as a bind 404 does today. Multi-tenant replay carries the external name into each
  tenant's pass unchanged.
- **The marker, not the interface.** `AMQPClient` is unchanged — ADR-113's reasoning holds, adding
  a method breaks every external implementer. The passive door goes on the internal `amqpChannel`
  adapter. An external `AMQPClient` implementation that ignores the marker issues an active declare
  with no type, which the broker refuses loudly rather than silently creating the wrong thing.
- **Ownership is a conflict class of its own.** One name declared locally AND marked external in
  one set is self-contradictory. It is reported in the ADR-118 aggregate style — first-wins, every
  name in one boot — but under its own header, because no shape can be aligned: the remedy is to
  drop one call site. Ownership is compared before shape, or the external declaration's absent
  `Type` would be reported as a type conflict and name neither call site's real mistake.
- **A passive step never enters the 406 skip set.** A passive declare cannot legitimately answer
  `PRECONDITION_FAILED`: the broker ignores every field it could disagree over. One that does is an
  anomaly, and ADR-113's skip-until-restart would wedge the reference on it for the process
  lifetime — with no surviving definition for an operator to fix. It is retried on the next
  generation like any other refused declaration. ADR-113's 406 semantics for local declarations are
  untouched.
- **Reference errors say what they checked.** The binding, consumer and publisher reference errors
  name the missing entity, state that this is a local check and that the broker was not contacted,
  and name the remedy. Only the exchange forms offer the external one: a reference-only queue is
  not a thing this framework has.

## Alternatives considered

**Boot and converge: make a startup declare failure non-fatal and let the reconnect redeclare pass
heal it.** Rejected. It collides with a pinned decision — a consumer-declaring service must abort
startup rather than boot deaf (`TestPrepareRuntimeConsumersFailsStartupOnEnsureError`) — and it
would be invisible to `messaging.consumers.critical`: queues declare before bindings, so after a
bind 404 the consumer still subscribes to its unbound queue on the next channel and reads healthy.
The accepted answer to a consumer deploying before the owner is a bounded, opt-in in-process wait
at startup, decided here and shipped separately.

**A validator-only marker that never reaches the broker.** It would satisfy the reference check
with no wire cost. Rejected on evidence: a passive declare needs no `configure` permission —
measured against a real broker with a user holding `configure=""`, where the passive declare
answered declare-ok and an active one was refused with 403. A publisher-only service is the case
that needs the check: nothing else contacts the exchange until the first publish, which is loud
only after `reconnect.maxpublishattempts`.

**Passive-declare the locally owned exchanges too, to catch shape drift.** Rejected: passive
verifies existence only, so it would catch nothing a bind does not already catch, and it would stop
the framework creating the topology it owns.

## Consequences

- **The single-declarer pattern is expressible.** A non-owner declares no shape, so it cannot race
  the owner's, and the owner's later declare of the real shape still succeeds.
- **The ordering dependency is real and new.** Only the reference-only form introduces it: before
  this, a consumer deploying first created the exchange itself and no 404 could occur at boot. A
  consumer-declaring service whose external exchange does not yet exist aborts startup with the
  broker's 404. That is what the opt-in startup wait addresses.
- **Verification is existence only.** A type or durability mismatch against the owner's exchange is
  not detectable passively and is not detected. `queue.bind` still answers 404 for a missing
  exchange, so for a *binding* the passive step adds nothing; it is the publisher-only service that
  gains a check it did not have.
- **`Validate()` refuses two declaration sets that used to pass** — one name both local and
  external, and an external declaration carrying shape. Both are new call sites only, so no
  existing set can hit them, and neither is a migration.

## References

- [ADR-113](adr_113_amqp_topology_redeclare_on_reconnect.md): the redeclare pass the passive step joins, and its 406 skip set
- [ADR-118](adr_118_exchange_redeclaration_conflicts.md): the exchange-conflict aggregate this mirrors
- [ADR-116](adr_116_exchange_type_validation.md): the exchange-type check an external declaration is exempt from
- [wiki/messaging.md](messaging.md#external-exchanges): the door, its rules and the operator-facing failures
- `messaging/helpers.go` (`DeclareExternalExchange`), `messaging/declarations.go` (`validateExternalExchangeConflicts`, `validateExternalExchangeShape`)
- `messaging/amqp_client.go` (`DeclareExchange`), `messaging/amqp_adapters.go` (`amqpChannel.ExchangeDeclarePassive`), `messaging/registry.go` (`topologyStep.passive`)
