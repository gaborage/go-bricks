# ADR-040: Forward Declaration `Args` to the Broker on Queue/Exchange/Binding Declares

**Status:** Accepted
**Date:** 2026-07-17

## Context

`QueueDeclaration.Args`, `ExchangeDeclaration.Args`, and `BindingDeclaration.Args`
(`messaging/registry.go`) are exported `map[string]any` fields. The framework
takes them seriously everywhere except the one place that talks to the broker:

- `Declarations.RegisterQueue` (`messaging/declarations.go`) deep-copies `Args`
  into a fresh map at register time.
- `Declarations.Hash()` folds `Args` into the topology hash via `writeMapArgs`
  (`messaging/declarations.go`), so two declaration sets differing only in
  `Args` are treated as different topologies.
- `Registry.DeclareInfrastructure` (`messaging/registry.go`) replays the stored
  declarations against the broker — but its three call sites into
  `AMQPClient.DeclareQueue` / `DeclareExchange` / `BindQueue` never passed
  `Args` through, and `AMQPClientImpl` (`messaging/amqp_client.go`) hardcoded a
  `nil` arguments table on all three amqp091 calls. The `Args` field is
  populated, deep-copied, and hashed, then discarded exactly where it would
  reach RabbitMQ.

This produced three concrete problems:

1. **Permanent message loss with no escape hatch.** The consumer runtime nacks
   failed deliveries with `requeue=false` by design (v2.X breaking change: no
   infinite retry loops). In RabbitMQ that signal triggers broker-side
   dead-lettering *if* the queue has `x-dead-letter-exchange` set — but users
   had no way to set it, because `Args` never reached the broker. Every
   handler error or panic dropped the message forever, with no framework
   knob to prevent it.
2. **Cannot attach to ops-provisioned queues.** RabbitMQ's declare call
   includes an equivalence check: redeclaring an existing queue with
   different arguments fails the channel with `406 PRECONDITION_FAILED`. A
   queue pre-created by operations as a quorum queue (`x-queue-type=quorum`,
   RabbitMQ's recommended production queue type) could never be declared by
   a go-bricks service — startup would fail every time.
3. **Hash/state drift.** Two `Declarations` sets differing only in `Args`
   produced different topology hashes yet identical broker state, undermining
   the value of hash-based topology comparisons.

## Decision

Pass `ctx context.Context` plus the existing declaration struct to the three
`AMQPClient` declare/bind methods, and forward the declaration's fields —
including `Args` — to amqp091 at every implementation and pass-through:

```go
DeclareQueue(ctx context.Context, queue *QueueDeclaration) error
DeclareExchange(ctx context.Context, exchange *ExchangeDeclaration) error
BindQueue(ctx context.Context, binding *BindingDeclaration) error
```

The declaration structs (`QueueDeclaration` / `ExchangeDeclaration` /
`BindingDeclaration`) already exist as the package's domain vocabulary: the
registry stores and replays exactly these values, so its call sites collapse
to `r.client.DeclareQueue(ctx, queue)`. A nil declaration returns
`errNilDeclaration`. Build them with the `messaging.NewQueue` /
`NewTopicExchange` / `NewBinding` helpers (production-safe defaults) or as
struct literals.

The ctx addition rides the same compile-time break: these were the only
`AMQPClient` methods without a context parameter (`PublishToExchange` and
`ConsumeFromQueue` already take one), and `Registry.DeclareInfrastructure(ctx)`
could not propagate cancellation into them. amqp091's declare/bind operations
are not context-aware on the wire, so implementations honor ctx as a pre-flight
check (`ctx.Err()` before the broker call) — canceled startup fails fast
instead of issuing further declares. Folding this into the same release avoids
a second break to the same interface later (Context-First Design).

`AMQPClientImpl` converts `args` to `amqp.Table` via a `toTable` helper that
normalizes `nil`/empty maps to a `nil` table, keeping the wire frame for the
no-args path byte-identical to pre-change behavior. `Registry.DeclareInfrastructure`
now passes `exchange.Args` / `queue.Args` / `binding.Args` at its three replay
call sites. `tenantAwarePublisher` and the testify `MockAMQPClient` forward
the new parameter unchanged.

This is a **deliberate, compile-time-enforced breaking change** — the
exported interface's methods replace their positional parameters with the
`(ctx, declaration)` shape — consistent with the manifesto's
**Type Safety > Dynamic Hacks**: "breaking changes prioritized for
compile-time safety" over a silently-dead field.

## Alternatives Considered

### Option A: Add parallel `...WithArgs` methods (Rejected)

Keep `DeclareQueue(...)` as-is and add `DeclareQueueWithArgs(..., args)` (and
the exchange/binding equivalents) as new interface methods.

**Rejected because:** adding methods to a released interface is exactly as
apidiff-incompatible for implementers (every hand-rolled fake/mock in the
codebase and downstream still fails to satisfy the interface) as changing the
existing signatures — so it buys no compatibility. It would also leave the
old, argument-dropping methods live and callable, so the silent-data-loss
path this ADR closes would remain fully reachable; a caller (or an existing
integration) using the old method would still lose its `Args` with no
compiler signal.

### Option B: Positional parameters plus a trailing `args` map (Superseded)

Keep the positional-bool signatures and append `args map[string]any` (plus the
leading ctx):
`DeclareQueue(ctx, name string, durable, autoDelete, exclusive, noWait bool, args map[string]any) error`.

**Superseded during review:** this was the initially implemented shape, but it
puts `DeclareExchange` at 8 parameters — breaching the project's 7-parameter
ceiling (SonarCloud S107) at the interface AND at every implementation, fake,
and mock that must mirror it (7 flagged sites) — and every future field would
breach further. The declaration-struct form removes that entire class, matches
how `PublishToExchange`/`ConsumeFromQueue` already take option structs, and
reuses the values the registry stores anyway.

### Option C: Leave `Args` dead, document the limitation (Rejected)

Keep the fields for forward-compatibility, and document in the wiki that they
are not yet honored.

**Rejected because:** this perpetuates silent data loss. A user who reads the
`Args` field, the deep-copy semantics in `RegisterQueue`, or the hash
contribution in `Hash()` would reasonably conclude the value is honored — the
framework already goes out of its way to preserve and compare it. Shipping
that appearance without the behavior is worse than not having the field.

## Consequences

### Positive

- **Closes the message-loss gap.** A queue declared with
  `Args["x-dead-letter-exchange"]` now has failed, non-requeued deliveries
  parked in the broker instead of dropped — provided the full dead-letter
  route is provisioned (the DLX exchange, a queue, and a binding between
  them; the arg only routes, the route retains) — see
  `TestAMQPClientDeclareQueueArgsDeadLetter`
  (`messaging/amqp_client_integration_test.go`) for the end-to-end pin.
- **Unblocks ops-provisioned queues.** `Args["x-queue-type"] = "quorum"` (or
  any other broker-recognized argument) now participates in the declare call
  and RabbitMQ's equivalence check, so a service can attach to a
  pre-provisioned queue whose arguments match — see
  `TestAMQPClientDeclareQueueArgsQuorum`.
- **Hash and broker state agree again.** Since `Args` now reaches the broker
  wherever it reaches the hash, a topology hash change and an actual broker
  declare change move together.
- Consumers using only the `Declarations` helper API (`decls.DeclareQueue`,
  `decls.DeclareTopicExchange`, `decls.DeclareBinding`, ...) are unaffected —
  they never called the three-method interface directly and simply recompile.

### Negative

- **Every direct caller of `AMQPClient.DeclareQueue` / `DeclareExchange` /
  `BindQueue`, and every hand-rolled fake or mock implementing `AMQPClient`,
  must move to the `(ctx, declaration)` shape.** Within this repo that touched
  `messaging/registry.go`, `messaging/tenant_publisher.go`,
  `testing/mocks/amqp.go` (the `ExpectDeclare*`/`ExpectBindQueue` helpers now
  take the declaration — nil matches any — and the `...Any` variants match two
  parameters), and several in-package test fakes. Downstream consumers with
  their own fakes face the same mechanical fix. Tracked as migrations atom
  **C52.1**.
- **Users who set `Args` on a declaration in ≤v0.51.0 (silently dropped) will
  see it take effect on upgrade.** If the broker already has a queue with
  different server-side arguments than what is now declared, the app fails
  to start with `406 PRECONDITION_FAILED` instead of the previous silent
  success. Tracked as migrations atom **C52.2**.

### Neutral

- `messaging/declarations.go`'s deep-copy (`RegisterQueue`) and hashing
  (`Hash()`/`writeMapArgs`) logic is unchanged — it was already correct; only
  the broker-facing boundary changed.
- `ConsumeOptions`'s consume-time `args` (still hardcoded `nil` in
  `ConsumeFromQueue`) and any framework-managed auto-DLQ provisioning
  (auto-declaring DLX/parking queues, retry policies) are explicitly deferred
  — this ADR ships the enabling primitive only.

## Migration Impact

See [wiki/migrations.md](migrations.md) hop **E52** for the full
detect/gate/apply/verify runbook (atoms **C52.1** and **C52.2**).

## Related ADRs

- [ADR-033: Outbox Retry-Count Status Parking](adr_033_outbox_retry_count_status_parking.md) —
  the outbox side's dead-lettering story (`MarkDeadLettered`, `status='failed'`
  parking); this ADR is the consumer-side complement, giving operators a
  broker-native parking mechanism for non-outbox consumers.
- [ADR-017: Insert Query Builder / ToSQL Standardization](adr_017_insert_query_builder.md) —
  prior precedent for a deliberate, compiler-enforced breaking change chosen
  over a parallel/compatible API, for the same reason: closing a
  silent-defect surface outweighs the migration cost.
