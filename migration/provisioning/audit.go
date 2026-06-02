package provisioning

import (
	"context"
	"time"

	"github.com/gaborage/go-bricks/migration"
)

// attrKeyJobID is the orchestrator-specific attribute attached to every
// state.transitioned event. Dotted-lowercase per the AuditEvent.Attributes
// convention so it namespaces cleanly alongside the engine layer's migration.*
// keys.
//
// Note: the raw step-error string is deliberately NOT attached to the audit
// event. Provisioning steps wrap driver errors that can embed DSNs/credentials,
// and audit attributes bypass the logger's field-name-based SensitiveDataFilter.
// The engine layer (emitMigrationApplied) follows the same rule — failures are
// reduced to a classified ErrorClass, never raw text. The detailed error stays
// in the operational log (runForwardStep/runCleanup) and the persisted Job.
const attrKeyJobID = "provisioning.job_id"

// AuditContext carries the explicit, deployment-time context stamped onto every
// state.transitioned event an Executor emits. It mirrors the engine layer's
// migration.AuditContext (minus Target, which the orchestrator derives from the
// job's TenantID). Per ADR-019 the principal is never inferred from IAM/OS —
// operators pass it in (a CLI flag or library call argument). An empty Principal
// is substituted with PrincipalUnspecified by the emitter, which also logs a
// warning so the gap is itself auditable.
type AuditContext struct {
	// Principal identifies who triggered the provisioning run. Required by
	// contract; empty falls back to migration.PrincipalUnspecified + warning.
	Principal string
	// GitCommitSHA and PipelineRunID are optional but strongly recommended
	// for correlating a transition back to a deployment.
	GitCommitSHA  string
	PipelineRunID string
}

// WithAudit enables audit-event emission for this Executor. Every state
// transition then fires a migration.AuditEvent of type state.transitioned
// through emitter — the same OTel-span + structured-log + optional-sink path
// the engine layer uses, so the audit schema cannot drift. Returns the
// Executor for chaining, mirroring FlywayMigrator.WithAuditRecorder.
//
// Audit is opt-in: an Executor constructed without WithAudit emits nothing and
// behaves exactly as before. The caller owns emitter's lifecycle (Close).
func (e *Executor) WithAudit(emitter migration.Emitter, cfg AuditContext) *Executor {
	e.audit = emitter
	e.auditCfg = cfg
	return e
}

// emitTransition fires one state.transitioned audit event for the from->to
// edge that just persisted. No-op when audit is not configured. startedAt is
// captured before the persistence call so the span covers the transition's
// commit duration.
func (e *Executor) emitTransition(ctx context.Context, job *Job, from, to State, startedAt time.Time) {
	if e.audit == nil {
		return
	}

	ev := &migration.AuditEvent{
		Type:               migration.AuditEventTypeStateTransitioned,
		Target:             job.TenantID,
		AppliedByPrincipal: e.auditCfg.Principal,
		FromState:          string(from),
		ToState:            string(to),
		StartedAt:          startedAt,
		CompletedAt:        time.Now().UTC(),
		GitCommitSHA:       e.auditCfg.GitCommitSHA,
		PipelineRunID:      e.auditCfg.PipelineRunID,
		Attributes:         map[string]string{attrKeyJobID: job.ID},
	}

	// A transition into the terminal failed state is the audited failure of a
	// provisioning attempt. Step failures cannot be mapped to a finer engine
	// ErrorClass, so the catch-all internal_error is honest. The raw step error
	// is intentionally omitted (see attrKeyJobID) — it stays in the operational
	// log. All other transitions, including the forward edge into cleanup, are
	// successful state changes.
	if to == StateFailed {
		ev.Outcome = migration.AuditOutcomeFailed
		ev.ErrorClass = migration.ErrorClassInternal
	} else {
		ev.Outcome = migration.AuditOutcomeSuccess
	}

	e.audit.Emit(ctx, ev)
}
