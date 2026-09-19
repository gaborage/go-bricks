# messaging/ — GoBricks package rules

Repo-wide rules stay in the root [CLAUDE.md](../CLAUDE.md).

## Messaging Architecture

AMQP-based messaging with **validate-once, replay-many** pattern. Declarations validated upfront, replayed per-tenant for isolation. Automatic reconnection with exponential backoff. Context propagation for tenant IDs and tracing.

**Concise declaration pattern (use the helpers, not raw structs):** in `DeclareMessaging`, use `decls.DeclareTopicExchange` / `decls.DeclareDirectExchange` / `decls.DeclareQueue` / `decls.DeclareBinding` / `messaging.DeclareTypedPublisher[T]` (returns the `Publisher[T]` handle publishes go through) / `decls.DeclareConsumer` (full example in [llms.txt](../llms.txt)).

**Critical Rules:**

- Each `queue + consumer_tag + event_type` triple must be registered exactly **once** — duplicates panic at startup.
- Handler errors and panics → message nacked WITHOUT requeue (no infinite retry loops). Make handlers thread-safe and idempotent; use `DeclareQueueWithDLQ` to park failures in a dead-letter queue instead of dropping them (raw `Args["x-dead-letter-exchange"]` remains the custom-topology escape hatch — set Args before registration; see [wiki/messaging.md](../wiki/messaging.md)).
- Re-declaring one exchange or queue name merges only when the shapes agree (`Type` and the flags equal, shared `Args` values equal); an incompatible repeat keeps the FIRST declaration and fails startup with an aggregate conflict error — watch `DeclareQueueWithDLQ`'s fanout DLX against a `DeclareTopicExchange`/`DeclareDirectExchange` of the same name.
- Default consumer concurrency is `runtime.NumCPU() * 4` workers (v0.17+ breaking change). Set `Workers: 1` explicitly when message ordering matters.
- An exchange another service OWNS is referenced with `decls.DeclareExternalExchange(name)` — name only, verified with a passive declare on every declare pass and never created, so this service cannot race the owner's shape; verification is existence only, a missing exchange is the broker's 404 and the same name declared locally and as external fails startup (ADR-119).
- The registry re-declares its topology once per new channel, driven by the client's own new-channel announcement as well as by a consumer re-subscribe, so a registry that declares but consumes nothing still recovers topology the broker lost; the guard is keyed per `(source, generation)` and the pass always DECLARES through the registry's own client. Both seams are carried only by clients that are, or embed, the one `NewAMQPClient` returns; a declaration refused with `PRECONDITION_FAILED` is skipped until the process restarts (ADR-113).

For helper API, error handling deep dive, panic recovery, concurrency tuning, and reconnection defaults, see [wiki/messaging.md](../wiki/messaging.md).
