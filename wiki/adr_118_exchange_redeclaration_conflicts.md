# ADR-118: Declarations Merge a Compatible Exchange Re-Declaration and Refuse the Rest

**Status:** Accepted
**Date:** 2026-09-18
**Issue:** #1714

## Context

`Declarations.RegisterExchange` had no existence check. It wrote `d.Exchanges[e.Name] = decl`
unconditionally, so re-declaring one name replaced the earlier declaration whatever its `Type`,
flags or `Args`. `RegisterQueue` had stopped doing that one hop earlier
([migrations.md](migrations.md) `[C56.7]`); `Validate()` reported a conflicting queue
re-declaration and had no exchange analogue.

The framework reaches that door twice on one name by itself. `DeclareQueueWithDLQ` registers
its dead-letter exchange as a **fanout** (`newDurableExchange(dlx, ExchangeTypeFanout)`, named
`DeadLetterSpec.Exchange` or `<queue>.dlx`) and binds the parking queue with an empty routing
key. A module that also declares that name through `DeclareTopicExchange` or
`DeclareDirectExchange` decided the broker's exchange type by registration order: the typed
exchange winning left the parking binding unreachable, so every dead-lettered message carrying
a non-empty routing key was dropped instead of parked; the fanout winning declared the module's
own exchange as fanout, fanning its traffic to every bound queue. Neither failed startup, and
neither call site can see the other — the two declarations usually live in different modules.

## Decision

- **Merge a compatible repeat.** A second declaration of one exchange name merges when `Type`
  and the four flags (`Durable`, `AutoDelete`, `Internal`, `NoWait`) are equal and every `Args`
  key the two share carries the same value. The union of their `Args` is stored, so the outcome
  does not depend on which call ran first. Several primary queues sharing one DLX — an identical
  repeat — stay silent, as they always did.
- **Refuse the rest, first-wins.** An incompatible repeat keeps the incumbent, mutates nothing,
  and records an `exchangeConflict` naming the exchange, the field and both values. There is no
  partial merge: a rejected declaration cannot leave an `Args` key behind.
- **Compare `Type` first.** The comparison order is `Type`, `Durable`, `AutoDelete`, `Internal`,
  `NoWait`, then `Args` keys in sorted order, returning at the first difference. `Type` leads
  because it is the field the broker routes on and the one a shared DLX name actually collides
  over. Sorting the `Args` keys makes the reported conflict identical across runs.
- **Report from `Validate()`.** `validateExchangeConflicts` aggregates every distinct conflict
  into one `errors.Join` — a header naming the count and the collision to look for, then one
  detail line per conflict reading `exchange %q: %s kept %q vs rejected %q`. `Validate()` runs
  once at declaration collection, so the error aborts startup naming every conflict in one boot.
- **Every door that registers an exchange goes through the merge.** `DeclarePublisher`'s
  "register only if the name is absent" guard is deleted, as `DeclareConsumer`'s queue guard was
  in `[C56.7]`. A guard that skips a registered name reinstates the order dependence this
  removes: a publisher handed `NewTopicExchange("<queue>.dlx")` after `DeclareQueueWithDLQ`
  would keep the fanout with no conflict and no error, while the reverse order aborts startup.
  The guard's purpose — do not clobber the incumbent — is what the merge now does.
- **Mirror the queue mechanism exactly.** `exchangeConflict`, `exchangeMergeConflict`,
  `newExchangeConflict`, `recordExchangeConflict` and `validateExchangeConflicts` are the
  siblings of the queue five, down to `reflect.DeepEqual` over `==` on `Args` values (`==`
  panics on an uncomparable dynamic type such as a slice or an `amqp.Table`) and to the
  all-string conflict struct that makes `slices.Contains` deduplication work. `Clone()` copies
  the new slice: a clone that passed a validation its source failed would be a trap.

The runtime `Registry.RegisterExchange` is untouched. It is the replay sink, and the declaration
set it replays has already been validated.

## Alternatives considered

**Keep last-write-wins and log a WARN.** Rejected: the wrong topology still reaches the broker,
and a startup WARN in a service that boots green is exactly how this shipped unnoticed. Fail
Fast says a declaration set the framework knows is self-contradictory is a startup failure.

**Return an `error` from `RegisterExchange`.** It would surface the problem at the call site.
Rejected: it breaks every caller's signature for a diagnosis the call site cannot act on — the
conflicting declaration is in another module — and it diverges from `RegisterQueue`, whose
whole design is that the once-path reports what no single call site can see.

**Refuse only a `Type` mismatch and merge flag and `Args` differences.** Rejected: a
`Durable` or `Internal` mismatch is refused by the broker at `exchange.declare` with a
`PRECONDITION_FAILED` channel exception, which since [ADR-113](adr_113_amqp_topology_redeclare_on_reconnect.md)
is skipped until the process restarts. Silently picking one side of a flag disagreement moves
the failure to the broker and makes it survivable-looking. One rule for all fields also keeps
the exchange path readable as the queue path's sibling.

## Consequences

- **A declaration set that used to boot now refuses.** Two declarations of one exchange name
  that disagree on `Type`, a flag, or a shared `Args` value fail `Validate()` at startup where
  the later one used to overwrite the earlier one silently. Migration is
  [migrations.md](migrations.md) `[C66.7]`.
- **A compatible repeat can now send more `Args` to the broker.** The stored declaration is the
  union, where it used to be whatever the last call carried. No framework helper registers an
  exchange with non-empty `Args`, so this has no in-framework population. On a broker already
  holding that exchange the union is a `PRECONDITION_FAILED` redeclare; the controlled
  migration — and why deleting and recreating the exchange is not one — is
  [migrations.md](migrations.md) `[C66.7]`.
- **The contested `Args` values are rendered into a logged startup error**, which the logger's
  key-based `SensitiveDataFilter` cannot mask. Exchange `Args` are broker topology and must not
  carry secrets — the same caveat the queue path already carries.
- **`[C56.7]`'s "Exchanges are unchanged and still last-write-wins" is superseded**, not
  corrected: that atom records the E56 hop accurately, and `[C66.7]` supersedes it in sequence.

## References

- [migrations.md](migrations.md) `[C56.7]`: the queue-re-declaration merge this mirrors, which carries no ADR of its own
- [ADR-040](adr_040_declaration_args_passthrough.md): declaration `Args` reach the broker unjudged
- [ADR-116](adr_116_exchange_type_validation.md): the exchange-type check this sits beside
- [ADR-106](adr_106_dlq_helper_declares_quorum_queues.md): `DeclareQueueWithDLQ`, whose fanout DLX is the collision
- [ADR-113](adr_113_amqp_topology_redeclare_on_reconnect.md): why a broker-side `PRECONDITION_FAILED` is not a fallback
- [wiki/messaging.md](messaging.md#helper-functions-for-simplified-declarations): the merge rules and the rendered error
- `messaging/declarations.go` (`RegisterExchange`, `exchangeMergeConflict`, `validateExchangeConflicts`, `Clone`)
- `messaging/helpers.go` (`DeclarePublisher`, the auto-registering door whose exists-guard this removes)
- `app/module_registry.go` (`DeclareMessaging`, the startup door that reports it)
