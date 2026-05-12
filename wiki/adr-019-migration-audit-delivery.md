# ADR-019: Migration Audit-Event Delivery — OpenTelemetry-first with Pluggable Sink Override

**Status:** Accepted
**Date:** 2026-05-12
**Related issues:** [#381](https://github.com/gaborage/go-bricks/issues/381) (this decision), [#382](https://github.com/gaborage/go-bricks/issues/382) (audit-event emission implementation, depends on this ADR)

## Context

[#375](https://github.com/gaborage/go-bricks/issues/375)'s vertical slice ships migrations as code (Runner contract, fan-out, role separation, state machine, quiesce flag). Once the state machine ([#379](https://github.com/gaborage/go-bricks/issues/379)) and the engine ([#376](https://github.com/gaborage/go-bricks/issues/376)) are in place, every migration application and every state-machine transition needs to leave an auditable record. Concretely: who applied which version to which target, when, from which pipeline run, with what outcome — and **also** every operator action that changes deployment behaviour (quiesce set/clear).

The original [#375](https://github.com/gaborage/go-bricks/issues/375) proposal sketched a pluggable audit-event sink (Kafka / S3 / OpenTelemetry / etc.). GoBricks already has an OpenTelemetry-first observability seam: `MeterProvider`, `TracerProvider`, and `LoggerProvider` on `ModuleDeps`. OTLP exporters are wired by default. Operators already monitor those collectors.

The question this ADR resolves: **do migration audit events ride the existing OpenTelemetry seam, or do they need a separate sink?**

The trade-off matrix from [#381](https://github.com/gaborage/go-bricks/issues/381):

| Criterion | OTel reuse | Separate sink |
|---|---|---|
| Compliance-grade durability (PCI / SOC 2 evidence retention) | Collectors typically buffer-and-drop under load | Stronger durability if backed by Kafka / S3 / append-only store |
| Operator surface | Reuses existing dashboards, alerts, retention | New pipeline to operate, monitor, retain |
| Implementation cost | Trivial — emit spans + structured log records via existing providers | New abstraction, new config, new tests |
| Format flexibility | Bound to OTel semconv | Free to define audit-specific schema |
| Ability for consumers to route to their own audit pipeline | Limited — they already get OTel | Full — explicit interface |

## Options Considered

### Option 1: OpenTelemetry-only (Rejected)

Emit a `migration.audit.*` span on every application + transition, attach event-shape attributes (`target`, `version`, `applied_by_principal`, …), and write the same payload through the existing zerolog → OTLP log pipeline (per [ADR-006](adr-006-otlp-log-export.md)). No new package surface.

**Rejected because:** OTLP collectors (OTel Collector, vendor agents) are designed for telemetry, not durable audit. The default Collector batch processor buffers up to `send_batch_size` records in memory and drops on backpressure or restart. That's appropriate for traces/metrics where some loss is acceptable; it is **not** appropriate as the sole record-of-truth for PCI / SOC 2 evidence retention. Customers operating under those regimes need an audit trail that survives collector outages.

### Option 2: Pluggable-sink-only (Rejected)

Require every consumer to provide an `AuditSink` implementation. Ship one or two default impls (no-op, file-on-disk) and force PCI/SOC 2 customers to plug in Kafka/S3.

**Rejected because:** Most teams adopting go-bricks aren't operating under compliance regimes that require audit-grade durability. Forcing every consumer to wire a sink — even a no-op — adds a configuration cliff with no upside for the 80% case. It also wastes the OpenTelemetry infrastructure already running in every deployment: the audit data is genuinely useful as structured logs/traces for ordinary troubleshooting, and emitting it nowhere by default would be a regression.

### Option 3: Hybrid — OTel default, opt-in `AuditSink` override (Chosen)

Always emit to the OpenTelemetry seam: a span per audit event (with the full attribute set) plus a structured log record via the `LoggerProvider`. Additionally, expose a `migration.AuditSink` interface; when configured on `migration.Runner` (or via `ModuleDeps`), every emitted event is dispatched to the sink **after** the OTel emission, in a non-blocking fan-out (separate goroutine with a bounded send queue) so the sink cannot stall the migration. Sink failures log a warning but do not abort the migration.

**Chosen because:**

1. **Zero-config baseline** — adopters without compliance pressure get OTel-grade audit (good enough for troubleshooting, retention via the customer's existing collector pipeline) with no new wiring.
2. **Opt-in durability** — PCI / SOC 2 customers implement `AuditSink` against Kafka, S3, or their existing audit pipeline. They get full control over schema, retention, and back-pressure behaviour without forking the framework.
3. **Matches the rest of the framework.** Observability has OTel default + custom exporters. Cache has Redis default + abstract interface. Messaging has AMQP + abstract `Client` interface. This decision follows the same shape: a sensible default, with an interface for replacement.
4. **OTel is the cheap insurance.** Even sink-enabled customers benefit from OTel emission — it correlates audit events with the spans/metrics around them in the existing observability dashboards. The sink covers the durability gap that OTel alone leaves.

## Decision

GoBricks ships **both paths simultaneously**. The default is OTel; the `AuditSink` is optional but always fires when present.

### Event taxonomy

Four event types, defined in `migration`:

| Type | Emitter | When |
|---|---|---|
| `migration.applied` | Engine ([#376](https://github.com/gaborage/go-bricks/issues/376)) | Every successful or failed Flyway application against a target |
| `state.transitioned` | Orchestrator ([#379](https://github.com/gaborage/go-bricks/issues/379)) | Every provisioning-state-machine transition (`pending` → `schema_created` → … → `ready`/`failed`) |
| `quiesce.set` | CLI / orchestrator ([#380](https://github.com/gaborage/go-bricks/issues/380)) | Operator sets the deployment quiesce flag |
| `quiesce.cleared` | CLI / orchestrator ([#380](https://github.com/gaborage/go-bricks/issues/380)) | Operator clears the deployment quiesce flag |

### Event payload

Required fields on every event:

| Field | Type | Notes |
|---|---|---|
| `Type` | `AuditEventType` | One of the four above |
| `Target` | `string` | Schema/database identifier. **MUST NOT** be a DSN — credentials never appear in audit |
| `AppliedByPrincipal` | `string` | Sourced from explicit input (CLI flag or library call argument), not inferred from IAM/OS — operators must pass it |
| `StartedAt`, `CompletedAt` | `time.Time` | RFC3339 in serialized form |
| `Outcome` | `AuditOutcome` | `success` / `failed` / `skipped` |

Conditional fields:

| Field | When set |
|---|---|
| `Version` | On `migration.applied` (the Flyway version applied) |
| `FromState`, `ToState` | On `state.transitioned` |
| `ErrorClass` | When `Outcome == failed`; a stable string (e.g. `checksum_mismatch`, `lock_timeout`, `connection_refused`) — NOT the raw error message |
| `GitCommitSHA`, `PipelineRunID` | Optional but recommended; sourced from explicit input |
| `Attributes` | `map[string]string` for free-form callsite extension |

### Default emission path (always on)

- One `migration.audit.<type>` span per event, with all fields as span attributes (`code.namespace = "migration"`).
- One structured log record at `INFO` (or `ERROR` when `Outcome == failed`) via the framework's `LoggerProvider`, routed to OTLP per ADR-006.
- No metrics. Counts/rates are derivable from spans or logs at the backend.

### Optional emission path (`AuditSink`)

```go
package migration

type AuditSink interface {
    Record(ctx context.Context, event AuditEvent) error
}
```

Wired on the `Runner` (or via `ModuleDeps.AuditSink`). When set:

- Every event fires to the sink **after** the OTel emission, in a non-blocking fan-out (separate goroutine with a bounded send queue).
- A sink-side error logs a warning and increments an internal `audit_sink_failures_total` metric, but **does not** abort the migration. Audit must not block business work; that's a deliberate trade-off — the sink owner is responsible for backing it with a durable buffer (Kafka commit-log, S3 staging, etc.) if zero-loss is required.
- The sink is invoked with the same `AuditEvent` struct that OTel sees — schemas can't drift.

### Stable error-class taxonomy

When `Outcome == failed`, `ErrorClass` is a stable string from a published list, not a raw error message. The published list is:

```text
checksum_mismatch       — Flyway detected an applied script was modified after the fact
lock_timeout            — Could not acquire the advisory / DBMS_LOCK within the configured timeout
schema_history_corrupt  — flyway_schema_history is in an inconsistent state
target_not_ready        — State-machine target is not in a state that allows migration
target_unreachable      — Target database refused / timed out / DNS-failed
quiesce_blocked         — Quiesce flag was set; the run aborted before any Flyway work
internal_error          — Catch-all for unclassified panics / unexpected errors
```

This list is part of the public API (additive changes only) so downstream alerting can pin on stable strings.

## Consequences

**Pros:**
- Zero-config audit for the majority of adopters; durable audit for the compliance minority.
- Audit events benefit from the same backend correlation as spans / metrics / logs in the OTel pipeline — operators can pivot from a span to an audit event and back in their existing tooling.
- The `AuditSink` interface is small (one method) and stable; backwards-compatible additions to `AuditEvent` follow Go's struct-additive rules.

**Cons:**
- Two emission paths means schema discipline is essential: any change to `AuditEvent` must update OTel attribute mapping **and** sink contract. Mitigated by the single `AuditEvent` struct flowing into both paths.
- The "sink-failure does not abort migration" semantics is a real trade-off. Customers requiring zero-loss audit must back their sink with a durable buffer; the framework does not retry on the sink's behalf. Documented in the operator wiki.
- `ErrorClass` taxonomy must be maintained as Runner implementations grow. Adding a class is non-breaking; removing one is.

## Migration

No breaking changes. The interface is new in [#382](https://github.com/gaborage/go-bricks/issues/382)'s implementation PR. Existing `FlywayMigrator` callers see no behavioural change — audit emission is wired into the new `Runner` contract from [#376](https://github.com/gaborage/go-bricks/issues/376), not retrofitted onto `FlywayMigrator`.

## Stakeholder check

This decision should be validated with the people who own PCI / SOC 2 evidence collection at the consuming organizations before [#382](https://github.com/gaborage/go-bricks/issues/382) is started. Key questions to confirm:

1. Is OTel-grade audit (collector-backed structured logs + traces, with retention configured at the collector) sufficient for their compliance regime, **or** does the regime require an append-only durable store as the record-of-truth?
2. If they need durable: do they already operate a Kafka / S3 / append-only audit pipeline they want the `AuditSink` to plug into, or are they expecting the framework to ship a default durable impl?
3. Is the published `ErrorClass` taxonomy sufficient, or do they have classes they need to pin on for compliance reporting?

These answers don't change the ADR's decision (hybrid is the right shape regardless), but they shape the default `AuditSink` we ship (no-op, or a thin OTel-only wrapper, or a documented Kafka impl) and the initial `ErrorClass` list.

## References

- Issue [#381](https://github.com/gaborage/go-bricks/issues/381) — decision-only tracking issue.
- Issue [#382](https://github.com/gaborage/go-bricks/issues/382) — implementation, depends on this ADR.
- [ADR-006](adr-006-otlp-log-export.md) — OTLP log export; describes the existing `LoggerProvider` seam this ADR builds on.
- [wiki/observability.md](observability.md) — full observability deep-dive.
- [wiki/multi-tenant-migration.md](multi-tenant-migration.md) — multi-tenant migration operator/developer guide.
