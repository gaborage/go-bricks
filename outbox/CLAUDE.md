# outbox/ — GoBricks package rules

Loaded when work touches `outbox/`. Repo-wide rules stay in the root [CLAUDE.md](../CLAUDE.md).

## Outbox

Transactional outbox for reliable event publishing. Solves the dual-write problem: events written to an outbox table in the **same database transaction** as business data, then delivered to the broker by a background relay job.

Registration order matters: `scheduler.NewModule()` is required (the relay runs as a scheduled job) and `outbox.NewModule()` must register BEFORE consumer modules. Publish inside the business transaction: `s.outbox.Publish(ctx, tx, &app.OutboxEvent{...})` before `tx.Commit` (full example in [llms.txt](../llms.txt)).

**Delivery Guarantee:** At-least-once. Consumers MUST be idempotent; use the `x-outbox-event-id` header for deduplication.

For configuration, event-struct fields, retry behavior, and operational defaults, see [wiki/outbox.md](../wiki/outbox.md).
