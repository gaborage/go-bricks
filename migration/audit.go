package migration

import (
	"context"
	"time"
)

// AuditEventType enumerates the four migration audit-event types defined by
// ADR-019. Engine-layer emission covers migration.applied; orchestrator-layer
// events (state.transitioned, quiesce.*) are emitted by their respective
// subsystems when those land (#379, #380).
type AuditEventType string

const (
	// AuditEventTypeMigrationApplied marks a Flyway migration application
	// (successful or failed) against a target.
	AuditEventTypeMigrationApplied AuditEventType = "migration.applied"
	// AuditEventTypeStateTransitioned marks a provisioning-state-machine
	// transition. Emitted by #379 (not in this PR).
	AuditEventTypeStateTransitioned AuditEventType = "state.transitioned"
	// AuditEventTypeQuiesceSet marks an operator setting the deployment
	// quiesce flag. Emitted by #380 (not in this PR).
	AuditEventTypeQuiesceSet AuditEventType = "quiesce.set"
	// AuditEventTypeQuiesceCleared marks an operator clearing the deployment
	// quiesce flag. Emitted by #380 (not in this PR).
	AuditEventTypeQuiesceCleared AuditEventType = "quiesce.cleared"
)

// AuditOutcome is the terminal outcome of the audited operation.
type AuditOutcome string

const (
	AuditOutcomeSuccess AuditOutcome = "success"
	AuditOutcomeFailed  AuditOutcome = "failed"
	AuditOutcomeSkipped AuditOutcome = "skipped"
)

// ErrorClass is a stable string from a published taxonomy that downstream
// alerting can pin on. ADR-019 publishes seven values; the list is additive
// (new classes are non-breaking; removing one is breaking). Set only when
// Outcome == failed; otherwise leave empty.
type ErrorClass string

const (
	// ErrorClassChecksumMismatch — Flyway detected an applied script was
	// modified after the fact.
	ErrorClassChecksumMismatch ErrorClass = "checksum_mismatch"
	// ErrorClassLockTimeout — could not acquire the advisory / DBMS_LOCK
	// within the configured timeout.
	ErrorClassLockTimeout ErrorClass = "lock_timeout"
	// ErrorClassSchemaHistoryCorrupt — flyway_schema_history is in an
	// inconsistent state.
	ErrorClassSchemaHistoryCorrupt ErrorClass = "schema_history_corrupt"
	// ErrorClassTargetNotReady — the state-machine target is not in a state
	// that allows migration. Set by the orchestrator (#379), not the engine.
	ErrorClassTargetNotReady ErrorClass = "target_not_ready"
	// ErrorClassTargetUnreachable — the target database refused, timed out,
	// or DNS-failed.
	ErrorClassTargetUnreachable ErrorClass = "target_unreachable"
	// ErrorClassQuiesceBlocked — the quiesce flag was set; the run aborted
	// before any Flyway work. Set by the orchestrator (#380), not the engine.
	ErrorClassQuiesceBlocked ErrorClass = "quiesce_blocked"
	// ErrorClassInternal is the catch-all for unclassified panics and
	// unexpected errors.
	ErrorClassInternal ErrorClass = "internal_error"
)

// PrincipalUnspecified is the sentinel emitted when an operator does not
// supply AppliedByPrincipal. The audit event still fires (so the gap is
// itself auditable) and the emitter logs a warning. Operators MUST pass an
// explicit principal in well-behaved callers; the framework refuses to
// invent one from IAM/OS context per ADR-019.
const PrincipalUnspecified = "<unspecified>"

// AuditEvent is the canonical payload that flows into both the OpenTelemetry
// emission path and the optional AuditRecorder. The two paths share this struct
// so schemas cannot drift. Backwards-compatible additions follow Go's
// struct-additive rules; removing a field is a breaking change.
//
// Target is an opaque schema/database identifier (tenant ID or schema name)
// and MUST NOT be a DSN — credentials never appear in audit events.
type AuditEvent struct {
	Type               AuditEventType
	Target             string
	AppliedByPrincipal string
	StartedAt          time.Time
	CompletedAt        time.Time
	Outcome            AuditOutcome

	// Version is the Flyway version applied. Set on migration.applied.
	Version string

	// FromState / ToState describe a provisioning-state-machine transition.
	// Set on state.transitioned.
	FromState string
	ToState   string

	// ErrorClass is set when Outcome == failed; one of the published
	// constants above. Empty for success/skipped outcomes.
	ErrorClass ErrorClass

	// GitCommitSHA and PipelineRunID are optional but strongly recommended
	// for deployment-time runs; sourced from explicit caller input.
	GitCommitSHA  string
	PipelineRunID string

	// Attributes is a free-form extension point for callsite-specific
	// metadata. Keys SHOULD use dotted lowercase (e.g. "migration.vendor").
	Attributes map[string]string
}

// AuditRecorder is the opt-in delivery path described in ADR-019. When wired
// (typically via FlywayMigrator.WithAuditRecorder), every AuditEvent fires to
// Record after the OTel emission, on a separate goroutine with a bounded
// send queue.
//
// Record receives a non-nil *AuditEvent — the pointer matches the framework
// convention for medium-sized event payloads (see outbox.OutboxPublisher).
// Implementations SHOULD treat the event as read-only; the framework does
// not synchronize concurrent reads if a sink decides to mutate.
//
// The framework calls Record with a fresh background context that may be
// cancelled by FlywayMigrator.Close. Implementations SHOULD respect
// ctx.Done() for prompt cancellation, but the framework does not retry on
// the sink's behalf — sink owners requiring zero-loss audit must back their
// implementation with a durable buffer (Kafka commit-log, S3 staging, etc.).
//
// Errors returned from Record are logged as warnings and increment the
// migration.audit.sink_failures counter; they do NOT abort the migration.
// This is a deliberate trade-off per ADR-019: audit must not block business
// work.
type AuditRecorder interface {
	Record(ctx context.Context, event *AuditEvent) error
}
