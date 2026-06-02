//go:build integration

package provisioning

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/migration"
)

// TestProvisioningWorkerHonorsQuiesceFlag is the #380 acceptance test: a real
// Executor backed by a real PostgresStore parks a pending job while the
// PostgreSQL-backed quiesce flag is set, and resumes it to ready once cleared.
func TestProvisioningWorkerHonorsQuiesceFlag(t *testing.T) {
	env := newPGEnv(t)
	ctx, cancel := testCtx(t)
	defer cancel()

	store := env.newStore(t, "quiesce")
	ctrl, err := migration.NewPostgresQuiesceController(env.adminDB, "quiesce_flags_worker")
	require.NoError(t, err)
	require.NoError(t, ctrl.CreateTable(ctx))

	exec := mustNewExecutor(t, store, okSteps(t), env.logger).WithQuiesce(ctrl)
	upsertPending(t, store, "job-pg-q")

	// Flag set → the worker parks the pending job at its current state.
	_, err = ctrl.Set(ctx, migration.QuiesceSetOptions{By: "deployer", TTL: time.Hour})
	require.NoError(t, err)
	require.ErrorIs(t, exec.Run(ctx, "job-pg-q"), ErrQuiesced)

	parked, err := store.Get(ctx, "job-pg-q")
	require.NoError(t, err)
	assert.Equal(t, StatePending, parked.State, "a quiesced job must not advance or fail")

	// Flag cleared → the worker resumes the same job to ready.
	_, err = ctrl.Clear(ctx, "ops-oncall")
	require.NoError(t, err)
	require.NoError(t, exec.Run(ctx, "job-pg-q"))

	done, err := store.Get(ctx, "job-pg-q")
	require.NoError(t, err)
	assert.Equal(t, StateReady, done.State)
}
