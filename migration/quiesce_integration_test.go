//go:build integration

package migration

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPostgresQuiesceControllerLifecycle(t *testing.T) {
	env := newIntegrationEnv(t)
	ctx, cancel := testCtx(t)
	defer cancel()

	sink := newRecordingSink()
	em := NewEmitter(disabledLogger(), sink)
	ctrl, err := NewPostgresQuiesceController(env.adminDB(t), "quiesce_flags_lifecycle")
	require.NoError(t, err)
	ctrl.WithAudit(em)
	require.NoError(t, ctrl.CreateTable(ctx))
	require.NoError(t, ctrl.CreateTable(ctx), "CreateTable must be idempotent")

	set, err := ctrl.IsSet(ctx)
	require.NoError(t, err)
	assert.False(t, set, "fresh control plane is not quiesced")

	_, err = ctrl.Set(ctx, QuiesceSetOptions{By: "op@ci", Reason: "deploy-42", TTL: time.Hour})
	require.NoError(t, err)

	set, err = ctrl.IsSet(ctx)
	require.NoError(t, err)
	assert.True(t, set)

	st, err := ctrl.Query(ctx)
	require.NoError(t, err)
	assert.True(t, st.Active)
	assert.Equal(t, "op@ci", st.SetBy)
	assert.Equal(t, "deploy-42", st.Reason)

	_, err = ctrl.Clear(ctx, "op2@ci")
	require.NoError(t, err)
	set, err = ctrl.IsSet(ctx)
	require.NoError(t, err)
	assert.False(t, set)

	_, err = ctrl.Clear(ctx, "op2@ci")
	assert.ErrorIs(t, err, ErrQuiesceNotSet, "clearing an inactive flag returns ErrQuiesceNotSet")

	// Audit: the successful Set + Clear each emitted; the no-op second Clear did not.
	require.NoError(t, em.Close(ctx))
	events := sink.snapshot()
	require.Len(t, events, 2)
	assert.Equal(t, AuditEventTypeQuiesceSet, events[0].Type)
	assert.Equal(t, "op@ci", events[0].AppliedByPrincipal)
	assert.Equal(t, AuditEventTypeQuiesceCleared, events[1].Type)
	assert.Equal(t, "op2@ci", events[1].AppliedByPrincipal)
}

func TestPostgresQuiesceControllerTTLAutoRelease(t *testing.T) {
	env := newIntegrationEnv(t)
	ctx, cancel := testCtx(t)
	defer cancel()

	now := time.Now().UTC()
	ctrl, err := NewPostgresQuiesceController(env.adminDB(t), "quiesce_flags_ttl")
	require.NoError(t, err)
	ctrl.WithClock(fixedClock(now))
	require.NoError(t, ctrl.CreateTable(ctx))

	_, err = ctrl.Set(ctx, QuiesceSetOptions{By: "op", TTL: time.Minute})
	require.NoError(t, err)

	// Advance the clock past expiry without any sweeper: a crashed migration
	// job's uncleared flag must auto-release on the read side.
	ctrl.WithClock(fixedClock(now.Add(2 * time.Minute)))
	set, err := ctrl.IsSet(ctx)
	require.NoError(t, err)
	assert.False(t, set, "expired flag auto-releases; provisioning is never permanently blocked")

	st, err := ctrl.Query(ctx)
	require.NoError(t, err)
	assert.True(t, st.Expired)
	assert.False(t, st.Active)
}

func TestPostgresQuiesceControllerOperatorOverrideAcrossInstances(t *testing.T) {
	env := newIntegrationEnv(t)
	ctx, cancel := testCtx(t)
	defer cancel()

	const table = "quiesce_flags_override"
	setter, err := NewPostgresQuiesceController(env.adminDB(t), table)
	require.NoError(t, err)
	require.NoError(t, setter.CreateTable(ctx))
	_, err = setter.Set(ctx, QuiesceSetOptions{By: "deployer", TTL: time.Hour})
	require.NoError(t, err)

	// A different controller instance (the original setter "dead") can clear —
	// the override is keyed on scope, not the setter's session.
	clearer, err := NewPostgresQuiesceController(env.adminDB(t), table)
	require.NoError(t, err)
	_, err = clearer.Clear(ctx, "ops-oncall")
	require.NoError(t, err)

	set, err := clearer.IsSet(ctx)
	require.NoError(t, err)
	assert.False(t, set)
}

func TestPostgresQuiesceControllerSetRenewsTTL(t *testing.T) {
	env := newIntegrationEnv(t)
	ctx, cancel := testCtx(t)
	defer cancel()

	now := time.Now().UTC()
	ctrl, err := NewPostgresQuiesceController(env.adminDB(t), "quiesce_flags_renew")
	require.NoError(t, err)
	ctrl.WithClock(fixedClock(now))
	require.NoError(t, ctrl.CreateTable(ctx))

	_, err = ctrl.Set(ctx, QuiesceSetOptions{By: "op", TTL: time.Minute})
	require.NoError(t, err)

	ctrl.WithClock(fixedClock(now.Add(50 * time.Second)))
	_, err = ctrl.Set(ctx, QuiesceSetOptions{By: "op", TTL: time.Minute})
	require.NoError(t, err)

	ctrl.WithClock(fixedClock(now.Add(90 * time.Second)))
	set, err := ctrl.IsSet(ctx)
	require.NoError(t, err)
	assert.True(t, set, "Set renews expires_at (heartbeat for long deploys)")
}

func TestPostgresQuiesceControllerClearAfterExpiry(t *testing.T) {
	env := newIntegrationEnv(t)
	ctx, cancel := testCtx(t)
	defer cancel()

	now := time.Now().UTC()
	ctrl, err := NewPostgresQuiesceController(env.adminDB(t), "quiesce_flags_clearexp")
	require.NoError(t, err)
	ctrl.WithClock(fixedClock(now))
	require.NoError(t, ctrl.CreateTable(ctx))

	_, err = ctrl.Set(ctx, QuiesceSetOptions{By: "op", TTL: time.Minute})
	require.NoError(t, err)

	// Past expiry (auto-released): the operator override must still clear it.
	ctrl.WithClock(fixedClock(now.Add(2 * time.Minute)))
	st, err := ctrl.Clear(ctx, "ops-oncall")
	require.NoError(t, err, "expired-but-uncleared flag stays clearable via the PG override")
	require.NotNil(t, st.ClearedAt)

	_, err = ctrl.Clear(ctx, "ops-oncall")
	assert.ErrorIs(t, err, ErrQuiesceNotSet)
}
