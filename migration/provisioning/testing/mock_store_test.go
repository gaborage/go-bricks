package testing

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/migration/provisioning"
)

func TestMockStateStoreDelegatesToMemoryStore(t *testing.T) {
	m := NewMockStateStore()
	ctx := context.Background()

	got, err := m.Upsert(ctx, &provisioning.Job{ID: "j1", TenantID: "t1"})
	require.NoError(t, err)
	assert.Equal(t, provisioning.StatePending, got.State)
}

func TestMockStateStoreErrorInjection(t *testing.T) {
	want := errors.New("forced get error")
	m := NewMockStateStore().WithGetError(want)
	_, err := m.Get(context.Background(), "any")
	assert.ErrorIs(t, err, want)
}

func TestMockStateStoreTransitionLog(t *testing.T) {
	m := NewMockStateStore()
	ctx := context.Background()
	_, err := m.Upsert(ctx, &provisioning.Job{ID: "j1", TenantID: "t1"})
	require.NoError(t, err)
	require.NoError(t, m.Transition(ctx, "j1",
		provisioning.StatePending, provisioning.StateSchemaCreated, nil, ""))
	require.NoError(t, m.Transition(ctx, "j1",
		provisioning.StateSchemaCreated, provisioning.StateRoleCreated, nil, ""))

	AssertTransitionPath(t, m, "j1", []provisioning.State{
		provisioning.StateSchemaCreated,
		provisioning.StateRoleCreated,
	})
}

func TestMockStateStoreFailedTransitionsAreNotLogged(t *testing.T) {
	m := NewMockStateStore()
	ctx := context.Background()
	_, err := m.Upsert(ctx, &provisioning.Job{ID: "j1", TenantID: "t1"})
	require.NoError(t, err)

	// Illegal edge — must be rejected.
	err = m.Transition(ctx, "j1",
		provisioning.StatePending, provisioning.StateMigrated, nil, "")
	require.Error(t, err)

	// Log stays empty.
	assert.Empty(t, m.TransitionLog())
}

func TestMockStateStoreReset(t *testing.T) {
	m := NewMockStateStore().WithGetError(errors.New("preconfigured"))
	ctx := context.Background()
	_, err := m.Upsert(ctx, &provisioning.Job{ID: "j1", TenantID: "t1"})
	require.NoError(t, err)
	require.NoError(t, m.Transition(ctx, "j1",
		provisioning.StatePending, provisioning.StateSchemaCreated, nil, ""))

	m.Reset()
	assert.Empty(t, m.TransitionLog(), "Reset clears transition log")
	_, err = m.Get(ctx, "j1")
	assert.ErrorIs(t, err, provisioning.ErrJobNotFound, "Reset clears stored jobs")
}

func TestAssertJobReachedState(t *testing.T) {
	m := NewMockStateStore()
	ctx := context.Background()
	_, err := m.Upsert(ctx, &provisioning.Job{ID: "j1", TenantID: "t1"})
	require.NoError(t, err)
	require.NoError(t, m.Transition(ctx, "j1",
		provisioning.StatePending, provisioning.StateSchemaCreated, nil, ""))

	AssertJobReachedState(t, m, "j1", provisioning.StateSchemaCreated)
}
