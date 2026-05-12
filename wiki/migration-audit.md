# Migration audit events

Every Flyway migration application emits a structured audit event so operators and compliance reviewers can answer the basic "who applied which version to which target, when, with what outcome?" question without grep-by-hand through logs.

Implements [ADR-019](adr-019-migration-audit-delivery.md). See also [wiki/multi-tenant-migration.md](multi-tenant-migration.md) for the multi-tenant operator workflow.

## Quick start

The default emission path is on by default — no wiring required. Every `FlywayMigrator.Migrate*` call against a target produces:

1. An OpenTelemetry span named `migration.audit.migration.applied` with the event payload as span attributes.
2. A structured log record at `INFO` (or `ERROR` when the migration failed) via the framework's `LoggerProvider`, routed to OTLP per [ADR-006](adr-006-otlp-log-export.md).

To get audit-grade durability (Kafka, S3, your own append-only store), implement `migration.AuditSink` and wire it via `FlywayMigrator.WithAuditSink(sink)`.

## Configuration

Audit context flows through the per-call `migration.Config.Audit` field:

```go
mcfg := &migration.Config{
    FlywayPath:    "/usr/local/bin/flyway",
    ConfigPath:    "flyway/flyway-postgresql.conf",
    MigrationPath: "migrations/postgresql",
    Timeout:       5 * time.Minute,
    Audit: migration.AuditContext{
        Principal:     "deployer@example.com",   // REQUIRED — see below
        GitCommitSHA:  "cafebabe1234",           // optional, recommended
        PipelineRunID: "gha-987654",             // optional, recommended
        Target:        "tenant_acme",            // optional, defaults to db.Database
    },
}
```

**`Principal` is required by contract**, not enforced at runtime. The framework will emit an audit event with `AppliedByPrincipal = "<unspecified>"` and a warning log when it's empty — so the gap is itself auditable, but business work isn't blocked. Operators MUST supply an explicit principal; the framework refuses to infer one from IAM/OS context.

## Event taxonomy

ADR-019 defines four event types. v1 ships only the engine-layer event:

| Type | Emitter | Status |
|---|---|---|
| `migration.applied` | `FlywayMigrator` | ✅ Shipped |
| `state.transitioned` | Provisioning state machine | Pending [#379](https://github.com/gaborage/go-bricks/issues/379) |
| `quiesce.set` / `quiesce.cleared` | Deployment quiesce gate | Pending [#380](https://github.com/gaborage/go-bricks/issues/380) |

`migration.applied` fires on every `migrate` invocation regardless of outcome. `info` and `validate` invocations do NOT emit `migration.applied` — they read state, they don't apply migrations.

## AuditEvent schema

```go
type AuditEvent struct {
    Type               AuditEventType
    Target             string         // schema/tenant identifier, never a DSN
    AppliedByPrincipal string         // explicit input; "<unspecified>" sentinel when empty
    StartedAt          time.Time
    CompletedAt        time.Time
    Outcome            AuditOutcome   // "success" / "failed" / "skipped"

    Version            string         // conditional: migration.applied (currently empty — see "Known gaps")
    FromState, ToState string         // conditional: state.transitioned
    ErrorClass         ErrorClass     // conditional: Outcome == failed
    GitCommitSHA       string         // optional
    PipelineRunID      string         // optional
    Attributes         map[string]string  // free-form callsite extension
}
```

The same struct flows into the OpenTelemetry emission path AND the optional `AuditSink`, so schemas cannot drift across delivery paths.

## ErrorClass taxonomy

When `Outcome == failed`, `ErrorClass` is a stable string from ADR-019's published list. Downstream alerting can pin on these values:

| ErrorClass | Set by | Trigger |
|---|---|---|
| `checksum_mismatch` | engine | Flyway detected an applied script was modified after the fact |
| `lock_timeout` | engine | Could not acquire the advisory / `DBMS_LOCK` within the configured timeout |
| `schema_history_corrupt` | engine | `flyway_schema_history` is in an inconsistent state |
| `target_unreachable` | engine | Target database refused, timed out, or DNS-failed |
| `target_not_ready` | orchestrator ([#379](https://github.com/gaborage/go-bricks/issues/379)) | State-machine target is not in a state that allows migration |
| `quiesce_blocked` | orchestrator ([#380](https://github.com/gaborage/go-bricks/issues/380)) | Quiesce flag was set; the run aborted before any Flyway work |
| `internal_error` | engine | Catch-all for unclassified panics / unexpected errors |

The list is additive — adding a class is non-breaking; removing one is. The engine's classification is best-effort substring matching against Flyway's combined output; classification is intentionally permissive to avoid silently downgrading real errors to `internal_error`.

## Implementing an AuditSink

```go
type AuditSink interface {
    Record(ctx context.Context, event AuditEvent) error
}
```

Minimal Kafka example (illustrative — your producer choice is up to you):

```go
type kafkaAuditSink struct {
    producer *kafka.Writer
    topic    string
}

func (s *kafkaAuditSink) Record(ctx context.Context, event migration.AuditEvent) error {
    payload, err := json.Marshal(event)
    if err != nil {
        return err
    }
    return s.producer.WriteMessages(ctx, kafka.Message{
        Topic: s.topic,
        Key:   []byte(event.Target),
        Value: payload,
    })
}

// Wire it at startup:
migrator := migration.NewFlywayMigrator(cfg, log).
    WithAuditSink(&kafkaAuditSink{producer: producer, topic: "migration-audit"})

// On shutdown, drain the queue:
shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
defer cancel()
_ = migrator.Close(shutdownCtx)
```

### Delivery semantics

- **At-least-once via OTel.** The OpenTelemetry path is always on. If the collector pipeline is healthy, every event reaches your backend exactly once. Under collector backpressure, the default Collector batch processor may drop records — this is appropriate for telemetry but is NOT sufficient as the sole record-of-truth for PCI / SOC 2 evidence retention. For that, layer an `AuditSink` on top.
- **Non-blocking sink fan-out.** Events are enqueued onto a bounded buffer (256 events) and delivered from a single consumer goroutine. If the queue fills (slow sink + bursty traffic), new events are dropped silently and the `migration.audit.sink_drops` counter increments — the trade-off is that audit must never stall a Flyway migration.
- **Sink errors do not abort the migration.** `AuditSink.Record` returning an error logs a warning and increments `migration.audit.sink_failures`; the migration proceeds. Sink owners requiring zero-loss audit must back their implementation with a durable buffer (Kafka commit-log, S3 staging, etc.). The framework does not retry on the sink's behalf.
- **Shutdown drains.** `FlywayMigrator.Close(ctx)` waits for the queue to drain before returning. Events still buffered when `ctx` expires are dropped (their OTel emission already succeeded).

## Metrics

The migration audit emitter publishes two counters via the global OTel meter:

| Metric | Type | Attributes | Trigger |
|---|---|---|---|
| `migration.audit.sink_drops` | `Int64Counter` | `audit.type` | Sink queue was full when an event was enqueued |
| `migration.audit.sink_failures` | `Int64Counter` | `audit.type` | `AuditSink.Record` returned a non-nil error |

These are silent — they don't fire warnings. Wire them into your alerting if your `AuditSink` is the compliance record-of-truth.

## Known gaps

- **`Version` is currently empty** on `migration.applied` events. Populating it requires parsing Flyway's JSON output; that work is part of the structured-`Result` retrofit tracked in [#376](https://github.com/gaborage/go-bricks/issues/376). The other AuditEvent fields are sufficient for compliance correlation today.
- **State-machine and quiesce events** (`state.transitioned`, `quiesce.set`, `quiesce.cleared`) are not yet emitted. Their emission lands when [#379](https://github.com/gaborage/go-bricks/issues/379) and [#380](https://github.com/gaborage/go-bricks/issues/380) ship.
- **CLI flag plumbing** for `--applied-by` / `--git-sha` / `--pipeline-run-id` in `go-bricks-migrate` is a separate follow-up — for now, callers using the library API can populate `migration.Config.Audit` directly.

## Stakeholder checklist

ADR-019 includes a checklist for validating the design with PCI / SOC 2 evidence owners. Re-confirm before committing to a default `AuditSink` implementation:

1. Is OTel-grade audit (collector-backed structured logs + traces) sufficient for your compliance regime?
2. If durable audit is required, which pipeline does the `AuditSink` plug into (Kafka / S3 / append-only DB)?
3. Is the published `ErrorClass` taxonomy sufficient, or do you have classes you need to pin alerting on?

These answers don't change the framework — the hybrid OTel-default + opt-in sink shape is correct regardless — but they shape the example sink implementations the project may ship over time.
