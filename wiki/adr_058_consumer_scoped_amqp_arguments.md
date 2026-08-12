# ADR-058: Consumers carry per-consumer AMQP arguments, at the cost of struct comparability

- **Status**: Accepted
- **Date**: 2026-08-11
- **Related**: [ADR-040](adr_040_declaration_args_passthrough.md) (queue/exchange/binding `Args`, which this completes on the consumer side), [migrations.md](migrations.md) `[C59.2]`

## Context

[ADR-040](adr_040_declaration_args_passthrough.md) made declaration `Args` reach the broker on
queue, exchange and binding declares. That was enough to **declare** a RabbitMQ stream queue —
`queue.Args["x-queue-type"] = "stream"` — and it has been possible since. It was never enough to
**consume** one.

A stream is an append-only replicated log rather than a destructive queue: acking removes
nothing, and every consumer chooses for itself where in the log it starts reading. That choice
is `x-stream-offset`, and it is not a queue argument. It is a **consumer** argument, carried on
`basic.consume`, because two consumers on one stream legitimately want different start
positions — that is the whole point of a replayable log.

`AMQPClientImpl.ConsumeFromQueue` passed a hardcoded `nil` args table to `channel.Consume`. The
framework therefore had no path for a per-consumer argument of any kind, and the effect on
streams was specific and silent: every consumer attached at the broker default, `next`. A stream
declared so that a projector could rebuild from `first` delivered only what was published after
the projector happened to connect. Nothing failed; the replay simply did not happen. ADR-040
opened a capability that could not be reached from the consumer side.

The gap sat in three structs, one per hop from a module's declaration to the wire:

| Type | Role |
| --- | --- |
| `messaging.ConsumerOptions` | what a module hands `DeclareConsumer` |
| `messaging.ConsumerDeclaration` | registry state, deep-copied and replayed per tenant |
| `messaging.ConsumeOptions` | the argument to `AMQPClient.ConsumeFromQueue` |

Anything reaching `basic.consume` has to cross all three.

## Decision

**Add `Args map[string]any` to all three types**, forward it through the existing `toTable`
helper at the client boundary, and give stream queues a declaration helper. `toTable` already
normalizes a nil or empty map to a nil `amqp.Table`, so a consumer that sets no arguments
produces the same wire bytes as before.

`Args` is treated as declaration state everywhere the other `Args` maps already are: deep-copied
in `RegisterConsumer` and `Clone` so a caller mutating its own map after registration cannot
reach the registry, and folded into `Declarations.Hash()` via `writeMapArgs`. The hash decides
whether a per-tenant replay is a duplicate; two consumers differing only in start offset are not
the same consumer, so omitting `Args` from the hash would have made the second one a no-op.

`DeclareStreamQueue(name, *StreamQueueSpec)` mirrors `DeclareQueueWithDLQ`'s spec-struct shape.
`NewQueue`'s existing production defaults — durable, non-exclusive, non-auto-delete — are exactly
what a stream requires, so the helper adds `x-queue-type: stream` plus opt-in retention
(`x-max-age`, `x-max-length-bytes`, `x-stream-max-segment-size-bytes`) and nothing else.

### The comparability cost — this is the breaking part

A Go struct containing a map field is **not comparable**. Adding `Args` therefore removes `==`
and map-key use from `ConsumeOptions`, `ConsumerOptions` and `ConsumerDeclaration`. `apidiff`
reports it as three incompatible changes:

```text
./messaging.ConsumeOptions: old is comparable, new is not
./messaging.ConsumerDeclaration: old is comparable, new is not
./messaging.ConsumerOptions: old is comparable, new is not
```

Stated plainly: `optsA == optsB` and `map[messaging.ConsumeOptions]T` **no longer compile**. This
is a compile-time break, caught by the compiler at every affected site, with no runtime or
silent-behavior component. Assignment, copying, struct literals, and passing by value are all
unaffected — only equality and map-key use.

The framework accepts the break rather than engineering around it. That is
[CLAUDE.md's stated position](../CLAUDE.md) on its own API surface: remove obsolete paths and
document the break, rather than add a compatibility shim. It is also the honest shape of the
feature — per-consumer arguments are inherently a variable-length set, and a type carrying one is
not a comparable value.

### Rejected alternatives

**A pointer to a map (`Args *map[string]any`), or wrapping the map in a pointer-to-struct.** Both
restore `==`, and both make it a lie: the restored comparison is pointer identity, so two
consumers with identical arguments compare unequal and one consumer compares equal to itself
regardless of what its map now contains. Code that kept compiling would silently change meaning,
which is strictly worse than code that stops compiling. It also makes the common case hostile —
`(*c.Args)["x-stream-offset"]` at every read, and a nil-pointer footgun where a nil map is
harmlessly readable today.

**An opaque comparable handle** — an interned or index-typed `ArgsID` resolved through a package
registry. It preserves comparability and hides the map, at the cost of a global registry, a
lifetime question the framework does not otherwise have, and an API a reader cannot understand
from its signature. This is the "patterns, not over-design" line: a substantial mechanism whose
only purpose is to preserve an operation no framework code performs.

**Parallel `ConsumerOptionsWithArgs` types**, leaving the originals comparable. This is the shape
[ADR-040](adr_040_declaration_args_passthrough.md) already rejected for the declare methods
(its Option A), for the same reason: two near-identical types per hop, a permanent "which one do
I use" question, and the args-free variant quietly becoming the wrong default.

### Startup validation, because the broker's own answer is unreadable

The broker enforces stream rules by killing the channel with an opaque error. A stream queue
declared non-durable, or consumed with auto-ack, surfaces as a channel exception naming neither
the rule nor — usefully — the queue. `Declarations.Validate` therefore rejects four shapes at
startup, aggregated through `errors.Join` so one boot reports all of them:

1. A stream queue that is not durable.
2. A stream queue that is exclusive or auto-delete.
3. A stream consumer with `AutoAck: true`. Acks are consumer **credit** on a stream, so auto-ack
   is not a preference but a protocol error.
4. An `x-stream-offset` on a consumer whose queue is not a stream queue — the broker ignores it
   silently, and a start offset that does nothing is exactly the class of bug this ADR exists to
   remove.

Accepted `x-stream-offset` values are `"first"`, `"last"`, `"next"`, a non-negative `int` or
`int64`, a `time.Time`, or an interval string such as `"7D"`. A declared `int` is **widened to
`int64`** before it reaches the wire: amqp091 encodes a Go `int` as a 32-bit AMQP field and an
`int64` as 64-bit, so an offset past 2³¹ — the range a high-throughput stream genuinely reaches —
would otherwise truncate silently, and `1 << 32` would arrive as `0` and replay the entire log.

### Flap-resume, and the drain barrier that makes it correct

`superviseConsumer` re-subscribes after the broker drops a delivery channel. Re-subscribing with
the *declared* options would re-read the stream from its declared start position on every flap —
a consumer declared at `first` would replay the whole log through its handlers each time the
connection blinked. The supervisor therefore tracks the last offset it observed and re-subscribes
at `last + 1`, on a **copied** `Args` map, because the declaration's map is shared with registry
state and every other session.

`last + 1` is correct rather than lossy, and the reason is a barrier rather than an assumption:
`handleMessages` closes the jobs channel and waits for its worker pool before returning, and
`resubscribe` runs only after it returns. So `last` is the last offset **fully processed**, not
merely received, and nothing unprocessed can be skipped. An inclusive resume would redeliver that
message on every single reconnect for no gain.

Stream-ness is read from the declared queue table, never from a delivery header, so a
publisher-forged `x-stream-offset` on a classic queue cannot reach the resume path.

The resume is deliberately session-local and best-effort. AMQP 0.9.1 has no server-side offset
tracking, so a **process restart** re-attaches at the declared offset. Handlers must be
idempotent — already the framework-wide consumer rule.

## Consequences

**Positive.** A stream queue is now declarable *and* correctly consumable through the existing
AMQP connection, port, tenant manager, worker pool and OTel instrumentation — no second messaging
stack beside the framework, and no new dependency. Non-destructive reads, per-consumer start
offsets and retention are available to any module. The `Args` field is general: `x-priority` and
any future per-consumer argument ride the same path, so this is the last time the framework has
to widen a struct for one.

**Negative — the three types stop being comparable**, as above. Migration is
[migrations.md](migrations.md) `[C59.2]`.

**Negative — this lane cannot do what the stream protocol does.** Consuming a stream over AMQP
0.9.1 gets no server-side offset tracking, no single active consumer, and no super streams. Those
are protocol features of RabbitMQ's stream protocol on port 5552, not options this lane declined
to pass. A deployment that needs them needs the native client, which is the subject of ADR-059;
this lane exists for deployments where port 5552 is not reachable, and remains the right choice
there.

**Neutral — consumer concurrency is unchanged.** A stream is an ordered log, but this lane keeps
the standard `NumCPU * 4` worker pool, so a consumer that must preserve log order sets
`Workers: 1` explicitly. The framework does not infer ordering intent from the queue type,
because a projector that shards by key legitimately wants the parallelism.

## Future work

- **Native stream protocol** (ADR-059) — server-side offset tracking, single active consumer, and
  super streams, via the official `rabbitmq-stream-go-client`.
- **Client-side offset persistence for this lane.** It would make a process restart resume where
  it left off rather than at the declared offset. If it is ever added, the session-local resume
  described above should be **deleted** rather than layered on: two offset sources disagreeing is
  worse than one that is honest about its scope.
- **Native stream publishing.** Out of scope here; an AMQP publisher can already publish into a
  stream queue through a bound exchange.

## References

- [ADR-040](adr_040_declaration_args_passthrough.md) — declaration `Args` reach the broker
- [messaging.md](messaging.md#stream-queues-amqp-lane) — the consumer-facing guide
- [migrations.md](migrations.md) `[C59.2]` — the comparability break
- `messaging/messaging.go` (`ConsumeOptions`), `messaging/helpers.go` (`ConsumerOptions`, `DeclareStreamQueue`), `messaging/registry.go` (`ConsumerDeclaration`, `consumeOptionsFor`, `streamResume`)
- `messaging/declarations.go` — `validateStreamDeclarations`
- <https://www.rabbitmq.com/docs/streams> — stream semantics
