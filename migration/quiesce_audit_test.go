package migration

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEmitQuiesceEventNilEmitterIsNoOp(t *testing.T) {
	require.NotPanics(t, func() {
		emitQuiesceEvent(context.Background(), nil, "op", AuditEventTypeQuiesceSet,
			&quiesceRecord{expiresAt: time.Now()}, time.Now())
	})
}

func TestEmitQuiesceEventSetPopulatesAttributes(t *testing.T) {
	setupTestTracer(t)
	sink := newRecordingSink()
	em := NewEmitter(disabledLogger(), sink)

	now := time.Now().UTC()
	emitQuiesceEvent(context.Background(), em, "op@ci", AuditEventTypeQuiesceSet,
		&quiesceRecord{setAt: now, setBy: "op@ci", reason: "deploy", expiresAt: now.Add(time.Hour)}, now)

	require.NoError(t, em.Close(context.Background()))
	events := sink.snapshot()
	require.Len(t, events, 1)
	ev := events[0]
	assert.Equal(t, AuditEventTypeQuiesceSet, ev.Type)
	assert.Equal(t, DefaultQuiesceScope, ev.Target)
	assert.Equal(t, "op@ci", ev.AppliedByPrincipal)
	assert.Equal(t, DefaultQuiesceScope, ev.Attributes[attrKeyQuiesceScope])
	assert.Equal(t, "deploy", ev.Attributes[attrKeyQuiesceReason])
	assert.NotEmpty(t, ev.Attributes[attrKeyQuiesceExpiresAt])
}

func TestEmitQuiesceEventClearedOmitsExpiresAt(t *testing.T) {
	setupTestTracer(t)
	sink := newRecordingSink()
	em := NewEmitter(disabledLogger(), sink)

	now := time.Now().UTC()
	emitQuiesceEvent(context.Background(), em, "ops", AuditEventTypeQuiesceCleared,
		&quiesceRecord{setBy: "setter", expiresAt: now.Add(time.Hour)}, now)

	require.NoError(t, em.Close(context.Background()))
	events := sink.snapshot()
	require.Len(t, events, 1)
	_, hasExpiry := events[0].Attributes[attrKeyQuiesceExpiresAt]
	assert.False(t, hasExpiry, "quiesce.cleared omits the expires_at attribute")
	_, hasReason := events[0].Attributes[attrKeyQuiesceReason]
	assert.False(t, hasReason, "empty reason is omitted")
}
