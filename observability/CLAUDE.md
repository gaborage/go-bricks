# observability/ — GoBricks package rules

Repo-wide rules stay in the root [CLAUDE.md](../CLAUDE.md).

## Observability

W3C traceparent propagation, OpenTelemetry metrics (database/HTTP/AMQP/Go runtime), health endpoints (`/health`, `/ready`), dual-mode logging with conditional sampling, export timeouts gated on `observability.environment` (independent of `app.env`, defaults to `development`) and the signal endpoint — 10s for `development`/`stdout`, 60s otherwise.

**Custom metrics via `deps.MeterProvider`:** nil-check it in `Init`, get a `Meter`, create instruments (full example in [llms.txt](../llms.txt)).

**Helper Functions:** `CreateCounter`, `CreateHistogram`, `CreateUpDownCounter` in `observability/metrics.go`. When `observability.enabled: false`, a no-op provider is used (zero overhead, nil-safe).

For dual-mode log routing, runtime metrics, custom-metric patterns, vendor authentication (New Relic/Honeycomb/Datadog), and OTLP collector deployment, see [wiki/observability.md](../wiki/observability.md).

**Migration audit events**: every Flyway `migrate` emits a `migration.applied` event via the OTel seam; durable delivery is opt-in (`FlywayMigrator.WithAuditRecorder(sink)` — bounded-queue goroutine, sink errors never abort a migration). Operators MUST supply the principal explicitly (`Config.Audit.Principal`, `provisioning.AuditContext.Principal`) — the framework refuses to infer it and emits `<unspecified>` with a warning. Provisioning transitions (`state.transitioned`) and the quiesce flag (`quiesce.set`/`quiesce.cleared`) go through the same `migration.Emitter` seam, so the audit schema can't drift. See [wiki/migration_audit.md](../wiki/migration_audit.md) and [ADR-019](../wiki/adr_019_migration_audit_delivery.md).
