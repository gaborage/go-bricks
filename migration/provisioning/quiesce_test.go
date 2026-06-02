package provisioning

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/logger"
	"github.com/gaborage/go-bricks/migration"
)

// fakeQuiesceGate is a test migration.QuiesceGate with injectable result/error.
type fakeQuiesceGate struct {
	set bool
	err error
}

func (g *fakeQuiesceGate) IsSet(context.Context) (bool, error) { return g.set, g.err }
func (g *fakeQuiesceGate) Query(context.Context) (*migration.QuiesceStatus, error) {
	return &migration.QuiesceStatus{Active: g.set}, nil
}

func upsertPending(t *testing.T, store StateStore, id string) {
	t.Helper()
	_, err := store.Upsert(context.Background(), &Job{ID: id, TenantID: "tenant-x"})
	require.NoError(t, err)
}

func TestExecutorParksWhenQuiesced(t *testing.T) {
	store := NewMemoryStore()
	exec := mustNewExecutor(t, store, okSteps(t), logger.New("disabled", true)).
		WithQuiesce(&fakeQuiesceGate{set: true})
	upsertPending(t, store, "job-q")

	err := exec.Run(context.Background(), "job-q")
	require.ErrorIs(t, err, ErrQuiesced)

	final, err := store.Get(context.Background(), "job-q")
	require.NoError(t, err)
	assert.Equal(t, StatePending, final.State,
		"a quiesced job must park at its current state, never advance or fail")
}

func TestExecutorResumesWhenQuiesceCleared(t *testing.T) {
	store := NewMemoryStore()
	gate := &fakeQuiesceGate{set: true}
	exec := mustNewExecutor(t, store, okSteps(t), logger.New("disabled", true)).WithQuiesce(gate)
	upsertPending(t, store, "job-resume")

	require.ErrorIs(t, exec.Run(context.Background(), "job-resume"), ErrQuiesced)

	gate.set = false
	require.NoError(t, exec.Run(context.Background(), "job-resume"))
	final, _ := store.Get(context.Background(), "job-resume")
	assert.Equal(t, StateReady, final.State)
}

func TestExecutorQuiesceCheckErrorFailsOpen(t *testing.T) {
	store := NewMemoryStore()
	exec := mustNewExecutor(t, store, okSteps(t), logger.New("disabled", true)).
		WithQuiesce(&fakeQuiesceGate{err: errors.New("control-plane down")})
	upsertPending(t, store, "job-failopen")

	require.NoError(t, exec.Run(context.Background(), "job-failopen"),
		"a quiesce CHECK error must fail open (proceed), not strand provisioning")
	final, _ := store.Get(context.Background(), "job-failopen")
	assert.Equal(t, StateReady, final.State)
}

func TestExecutorNilQuiesceGateRunsNormally(t *testing.T) {
	store := NewMemoryStore()
	exec := mustNewExecutor(t, store, okSteps(t), logger.New("disabled", true))
	upsertPending(t, store, "job-nogate")

	require.NoError(t, exec.Run(context.Background(), "job-nogate"))
	final, _ := store.Get(context.Background(), "job-nogate")
	assert.Equal(t, StateReady, final.State)
}

// TestExecutorQuiesceDoesNotStrandCleanup verifies that quiesce never parks a
// job mid-rollback: a job already in StateCleanup must finish cleanup (reach
// StateFailed) even while quiesced, so partially-provisioned resources are
// never orphaned.
func TestExecutorQuiesceDoesNotStrandCleanup(t *testing.T) {
	store := NewMemoryStore()
	upsertPending(t, store, "job-cleanup")
	// Drive the job into the cleanup state (as a failed forward step would).
	require.NoError(t, store.Transition(context.Background(), "job-cleanup",
		StatePending, StateCleanup, nil, "boom"))

	exec := mustNewExecutor(t, store, okSteps(t), logger.New("disabled", true)).
		WithQuiesce(&fakeQuiesceGate{set: true})

	// Run must complete the rollback, not park in cleanup.
	err := exec.Run(context.Background(), "job-cleanup")
	require.Error(t, err) // ends in failed state
	assert.NotErrorIs(t, err, ErrQuiesced, "a cleanup-state job must not be parked by quiesce")

	final, err := store.Get(context.Background(), "job-cleanup")
	require.NoError(t, err)
	assert.Equal(t, StateFailed, final.State,
		"cleanup must finish even when quiesced; partial resources must not be stranded")
}

// TestExecutorHonorsRealQuiesceController exercises the gate against the real
// migration.MemoryQuiesceController (not the fake), confirming the QuiesceGate
// contract holds end-to-end.
func TestExecutorHonorsRealQuiesceController(t *testing.T) {
	store := NewMemoryStore()
	ctrl := migration.NewMemoryQuiesceController()
	exec := mustNewExecutor(t, store, okSteps(t), logger.New("disabled", true)).WithQuiesce(ctrl)
	upsertPending(t, store, "job-real")

	_, err := ctrl.Set(context.Background(), migration.QuiesceSetOptions{By: "op", TTL: 0})
	require.NoError(t, err)
	require.ErrorIs(t, exec.Run(context.Background(), "job-real"), ErrQuiesced)

	_, err = ctrl.Clear(context.Background(), "op")
	require.NoError(t, err)
	require.NoError(t, exec.Run(context.Background(), "job-real"))
	final, _ := store.Get(context.Background(), "job-real")
	assert.Equal(t, StateReady, final.State)
}
