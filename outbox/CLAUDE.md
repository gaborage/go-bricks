# outbox/ — GoBricks package rules

Repo-wide rules stay in the root [CLAUDE.md](../CLAUDE.md).

## Outbox

Transactional outbox for reliable event publishing. Solves the dual-write problem: events written to an outbox table in the **same database transaction** as business data, then delivered to the broker by a background relay job.

Registration order matters: `scheduler.NewModule()` is required (the relay runs as a scheduled job) and `outbox.NewModule()` must register BEFORE consumer modules. Publish inside the business transaction: `s.outbox.Publish(ctx, tx, &app.OutboxEvent{...})` before `tx.Commit` (full example in [llms.txt](../llms.txt)).

**Broker check (#366):** a per-tenant ledger refuses `Init` exactly when `deps.MessagingConfigured` is false — the Resource plan's answer, never root config (ADR-128); the fan-out guard runs before it. The shared ledger still reads the root broker URL.

**Delivery Guarantee:** At-least-once. Consumers MUST be idempotent; use the `x-outbox-event-id` header for deduplication.

For configuration, event-struct fields, retry behavior, and operational defaults, see [wiki/outbox.md](../wiki/outbox.md).
