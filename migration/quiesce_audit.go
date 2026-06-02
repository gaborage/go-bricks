package migration

import (
	"context"
	"time"
)

// Quiesce audit attribute keys. Bare names (no "audit.attr." prefix — the
// emitter adds it) per the AuditEvent.Attributes convention. Only operator-
// supplied, non-sensitive values are attached (scope/reason/expiry); no DB
// error text reaches the event (it would bypass the field-name
// SensitiveDataFilter — see the state.transitioned precedent).
const (
	attrKeyQuiesceScope     = "quiesce.scope"
	attrKeyQuiesceReason    = "quiesce.reason"
	attrKeyQuiesceExpiresAt = "quiesce.expires_at"
)

// emitQuiesceEvent emits a quiesce.set / quiesce.cleared audit event through
// the shared Emitter. No-op when em is nil (audit opt-in). principal is the
// operator who performed the action (explicit, never inferred — empty surfaces
// the PrincipalUnspecified sentinel via Emit). startedAt brackets the control-
// plane write so the span covers its duration.
func emitQuiesceEvent(ctx context.Context, em Emitter, principal string, typ AuditEventType, rec *quiesceRecord, startedAt time.Time) {
	if em == nil {
		return
	}
	attrs := map[string]string{attrKeyQuiesceScope: DefaultQuiesceScope}
	if rec.reason != "" {
		attrs[attrKeyQuiesceReason] = rec.reason
	}
	if typ == AuditEventTypeQuiesceSet {
		attrs[attrKeyQuiesceExpiresAt] = rec.expiresAt.UTC().Format(rfc3339Nano)
	}
	em.Emit(ctx, &AuditEvent{
		Type:               typ,
		Target:             DefaultQuiesceScope,
		AppliedByPrincipal: principal,
		StartedAt:          startedAt,
		CompletedAt:        time.Now().UTC(),
		Outcome:            AuditOutcomeSuccess,
		Attributes:         attrs,
	})
}
