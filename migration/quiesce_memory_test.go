package migration

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func fixedClock(ts time.Time) func() time.Time { return func() time.Time { return ts } }

func TestMemoryQuiesceControllerSetThenIsSet(t *testing.T) {
	c := NewMemoryQuiesceController()
	set, err := c.IsSet(context.Background())
	require.NoError(t, err)
	assert.False(t, set, "a fresh controller is not quiesced")

	_, err = c.Set(context.Background(), QuiesceSetOptions{By: "op@ci", Reason: "deploy-42", TTL: time.Hour})
	require.NoError(t, err)

	set, err = c.IsSet(context.Background())
	require.NoError(t, err)
	assert.True(t, set)
}

func TestMemoryQuiesceControllerClear(t *testing.T) {
	c := NewMemoryQuiesceController()
	_, err := c.Set(context.Background(), QuiesceSetOptions{By: "op", TTL: time.Hour})
	require.NoError(t, err)

	st, err := c.Clear(context.Background(), "op2")
	require.NoError(t, err)
	assert.False(t, st.Active)
	require.NotNil(t, st.ClearedAt)

	set, _ := c.IsSet(context.Background())
	assert.False(t, set)
}

func TestMemoryQuiesceControllerTTLAutoRelease(t *testing.T) {
	now := time.Now().UTC()
	c := NewMemoryQuiesceController().WithClock(fixedClock(now))
	_, err := c.Set(context.Background(), QuiesceSetOptions{By: "op", TTL: time.Minute})
	require.NoError(t, err)

	c.WithClock(fixedClock(now.Add(2 * time.Minute))) // past expiry
	set, err := c.IsSet(context.Background())
	require.NoError(t, err)
	assert.False(t, set, "expired flag must auto-release (read-side, no sweeper)")

	st, _ := c.Query(context.Background())
	assert.True(t, st.Expired)
	assert.False(t, st.Active)
}

func TestMemoryQuiesceControllerSetRenewsTTL(t *testing.T) {
	now := time.Now().UTC()
	c := NewMemoryQuiesceController().WithClock(fixedClock(now))
	_, err := c.Set(context.Background(), QuiesceSetOptions{By: "op", TTL: time.Minute})
	require.NoError(t, err)

	c.WithClock(fixedClock(now.Add(50 * time.Second))) // near expiry, renew
	_, err = c.Set(context.Background(), QuiesceSetOptions{By: "op", TTL: time.Minute})
	require.NoError(t, err)

	c.WithClock(fixedClock(now.Add(90 * time.Second))) // past original expiry, within renewed
	set, _ := c.IsSet(context.Background())
	assert.True(t, set, "Set renews expires_at (heartbeat)")
}

func TestMemoryQuiesceControllerTTLDefaultAndClamp(t *testing.T) {
	now := time.Now().UTC()
	c := NewMemoryQuiesceController().WithClock(fixedClock(now))

	st, err := c.Set(context.Background(), QuiesceSetOptions{By: "op", TTL: 0})
	require.NoError(t, err)
	assert.Equal(t, now.Add(DefaultQuiesceTTL), st.ExpiresAt, "zero TTL defaults")

	st, err = c.Set(context.Background(), QuiesceSetOptions{By: "op", TTL: 100 * time.Hour})
	require.NoError(t, err)
	assert.Equal(t, now.Add(MaxQuiesceTTL), st.ExpiresAt, "over-ceiling TTL is clamped")
}

func TestMemoryQuiesceControllerClearWhenInactive(t *testing.T) {
	c := NewMemoryQuiesceController()
	_, err := c.Clear(context.Background(), "op")
	assert.ErrorIs(t, err, ErrQuiesceNotSet)
}

func TestMemoryQuiesceControllerQueryShape(t *testing.T) {
	now := time.Now().UTC()
	c := NewMemoryQuiesceController().WithClock(fixedClock(now))
	_, err := c.Set(context.Background(), QuiesceSetOptions{By: "op@ci", Reason: "deploy-42", TTL: time.Hour})
	require.NoError(t, err)

	st, err := c.Query(context.Background())
	require.NoError(t, err)
	assert.True(t, st.Active)
	assert.Equal(t, "op@ci", st.SetBy)
	assert.Equal(t, "deploy-42", st.Reason)
	assert.Equal(t, now.Add(time.Hour), st.ExpiresAt)
	assert.Nil(t, st.ClearedAt)
	assert.False(t, st.Expired)
}

func TestMemoryQuiesceControllerEmitsAuditEvents(t *testing.T) {
	setupTestTracer(t)
	sink := newRecordingSink()
	em := NewEmitter(disabledLogger(), sink)

	c := NewMemoryQuiesceController().WithAudit(em)
	_, err := c.Set(context.Background(), QuiesceSetOptions{By: "op@ci", Reason: "deploy-42", TTL: time.Hour})
	require.NoError(t, err)
	_, err = c.Clear(context.Background(), "op2@ci")
	require.NoError(t, err)

	require.NoError(t, em.Close(context.Background())) // drains the async sink

	events := sink.snapshot()
	require.Len(t, events, 2)

	assert.Equal(t, AuditEventTypeQuiesceSet, events[0].Type)
	assert.Equal(t, DefaultQuiesceScope, events[0].Target)
	assert.Equal(t, "op@ci", events[0].AppliedByPrincipal)
	assert.Equal(t, "deploy-42", events[0].Attributes[attrKeyQuiesceReason])
	assert.NotEmpty(t, events[0].Attributes[attrKeyQuiesceExpiresAt])

	assert.Equal(t, AuditEventTypeQuiesceCleared, events[1].Type)
	assert.Equal(t, "op2@ci", events[1].AppliedByPrincipal, "the clearer is the audited principal")
}

func TestMemoryQuiesceControllerEmptyPrincipalSurfacesSentinel(t *testing.T) {
	setupTestTracer(t)
	sink := newRecordingSink()
	em := NewEmitter(disabledLogger(), sink)

	c := NewMemoryQuiesceController().WithAudit(em)
	_, err := c.Set(context.Background(), QuiesceSetOptions{TTL: time.Hour}) // no By
	require.NoError(t, err)

	require.NoError(t, em.Close(context.Background()))
	events := sink.snapshot()
	require.Len(t, events, 1)
	assert.Equal(t, PrincipalUnspecified, events[0].AppliedByPrincipal,
		"empty By must surface <unspecified>, never an inferred principal")
}

func TestMemoryQuiesceControllerNoAuditEmitterIsSafe(t *testing.T) {
	c := NewMemoryQuiesceController() // no WithAudit
	_, err := c.Set(context.Background(), QuiesceSetOptions{By: "op", TTL: time.Hour})
	require.NoError(t, err)
	_, err = c.Clear(context.Background(), "op")
	require.NoError(t, err)
}

func TestMemoryQuiesceControllerFreshQueryAndCreateTable(t *testing.T) {
	c := NewMemoryQuiesceController()
	st, err := c.Query(context.Background())
	require.NoError(t, err)
	assert.False(t, st.Active)
	assert.False(t, st.Expired)
	require.NoError(t, c.CreateTable(context.Background()), "memory CreateTable is a no-op")
}
