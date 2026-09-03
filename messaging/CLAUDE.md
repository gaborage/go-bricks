# messaging/ — GoBricks package rules

Loaded when work touches `messaging/`. Repo-wide rules stay in the root [CLAUDE.md](../CLAUDE.md).

## Messaging Architecture

AMQP-based messaging with **validate-once, replay-many** pattern. Declarations validated upfront, replayed per-tenant for isolation. Automatic reconnection with exponential backoff. Context propagation for tenant IDs and tracing.

**Concise declaration pattern (use the helpers, not raw structs):** in `DeclareMessaging`, use `decls.DeclareTopicExchange` / `DeclareQueue` / `DeclareBinding` / `DeclarePublisher` / `DeclareConsumer` (full example in [llms.txt](../llms.txt)).

**Critical Rules:**

- Each `queue + consumer_tag + event_type` triple must be registered exactly **once** — duplicates panic at startup.
- Handler errors and panics → message nacked WITHOUT requeue (no infinite retry loops). Make handlers thread-safe and idempotent; use `DeclareQueueWithDLQ` to park failures in a dead-letter queue instead of dropping them (raw `Args["x-dead-letter-exchange"]` remains the custom-topology escape hatch — set Args before registration; see [wiki/messaging.md](../wiki/messaging.md)).
- Default consumer concurrency is `runtime.NumCPU() * 4` workers (v0.17+ breaking change). Set `Workers: 1` explicitly when message ordering matters.

For helper API, error handling deep dive, panic recovery, concurrency tuning, and reconnection defaults, see [wiki/messaging.md](../wiki/messaging.md).
