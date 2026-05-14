package provisioning

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/logger"
)

func TestStateIsTerminal(t *testing.T) {
	terminal := []State{StateReady, StateFailed}
	for _, s := range terminal {
		assert.True(t, s.IsTerminal(), "%q should be terminal", s)
	}
	nonTerminal := []State{StatePending, StateSchemaCreated, StateRoleCreated, StateMigrated, StateSeeded, StateCleanup}
	for _, s := range nonTerminal {
		assert.False(t, s.IsTerminal(), "%q should not be terminal", s)
	}
}

func TestValidateTransitionForwardGraph(t *testing.T) {
	cases := []struct{ from, to State }{
		{StatePending, StateSchemaCreated},
		{StateSchemaCreated, StateRoleCreated},
		{StateRoleCreated, StateMigrated},
		{StateMigrated, StateSeeded},
		{StateSeeded, StateReady},
	}
	for _, c := range cases {
		t.Run(fmt.Sprintf("%s->%s", c.from, c.to), func(t *testing.T) {
			assert.NoError(t, ValidateTransition(c.from, c.to))
		})
	}
}

func TestValidateTransitionCleanupBranches(t *testing.T) {
	cases := []State{StatePending, StateSchemaCreated, StateRoleCreated, StateMigrated, StateSeeded}
	for _, from := range cases {
		from := from
		t.Run(string(from), func(t *testing.T) {
			assert.NoError(t, ValidateTransition(from, StateCleanup))
		})
	}
	assert.NoError(t, ValidateTransition(StateCleanup, StateFailed))
}

func TestValidateTransitionRejectsIllegalEdges(t *testing.T) {
	illegal := []struct{ from, to State }{
		// Skipping intermediate states is rejected.
		{StatePending, StateRoleCreated},
		{StatePending, StateReady},
		{StateSchemaCreated, StateMigrated},
		// Terminal states are dead ends.
		{StateReady, StatePending},
		{StateFailed, StateCleanup},
		// Backward transitions are rejected.
		{StateRoleCreated, StateSchemaCreated},
		// Unknown source state.
		{State("bogus"), StatePending},
	}
	for _, c := range illegal {
		c := c
		t.Run(fmt.Sprintf("%s->%s", c.from, c.to), func(t *testing.T) {
			err := ValidateTransition(c.from, c.to)
			require.Error(t, err)
			assert.True(t, errors.Is(err, ErrIllegalTransition))
		})
	}
}

func TestStepsValidate(t *testing.T) {
	full := okSteps(t)
	assert.NoError(t, full.Validate())

	mutators := []struct {
		name    string
		mutate  func(*Steps)
		missing string
	}{
		{"missing_create_schema", func(s *Steps) { s.CreateSchema = nil }, "CreateSchema"},
		{"missing_create_role", func(s *Steps) { s.CreateRole = nil }, "CreateRole"},
		{"missing_migrate", func(s *Steps) { s.Migrate = nil }, "Migrate"},
		{"missing_seed", func(s *Steps) { s.Seed = nil }, "Seed"},
		{"missing_cleanup", func(s *Steps) { s.Cleanup = nil }, "Cleanup"},
	}
	for _, m := range mutators {
		m := m
		t.Run(m.name, func(t *testing.T) {
			s := okSteps(t)
			m.mutate(&s)
			err := s.Validate()
			require.Error(t, err)
			assert.Contains(t, err.Error(), m.missing)
		})
	}
}

func TestNewExecutorValidatesDependencies(t *testing.T) {
	log := logger.New("disabled", true)
	store := NewMemoryStore()
	steps := okSteps(t)

	tests := []struct {
		name        string
		store       StateStore
		log         logger.Logger
		steps       Steps
		wantErrPart string
	}{
		{"nil_store", nil, log, steps, "non-nil StateStore"},
		{"nil_logger", store, nil, steps, "non-nil Logger"},
		{"missing_step", store, log, Steps{CreateSchema: steps.CreateSchema}, "Steps."},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewExecutor(tt.store, tt.steps, tt.log)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErrPart)
		})
	}

	_, err := NewExecutor(store, steps, log)
	assert.NoError(t, err)
}

func TestExecutorHappyPathReachesReady(t *testing.T) {
	store := NewMemoryStore()
	calls := newCallRecorder()
	exec := mustNewExecutor(t, store, recorderSteps(calls), logger.New("disabled", true))

	job := &Job{ID: "job-happy", TenantID: "tenant-a"}
	persisted, err := store.Upsert(context.Background(), job)
	require.NoError(t, err)
	assert.Equal(t, StatePending, persisted.State)

	require.NoError(t, exec.Run(context.Background(), job.ID))

	final, err := store.Get(context.Background(), job.ID)
	require.NoError(t, err)
	assert.Equal(t, StateReady, final.State)
	assert.Empty(t, final.LastError)

	// CreateSchema, CreateRole, Migrate, Seed each called once. Cleanup never called.
	assert.Equal(t, []string{"CreateSchema", "CreateRole", "Migrate", "Seed"}, calls.order(), "happy path step order")
	assert.Equal(t, 0, calls.count("Cleanup"))
}

func TestExecutorTerminalJobIsNoOp(t *testing.T) {
	store := NewMemoryStore()
	calls := newCallRecorder()
	exec := mustNewExecutor(t, store, recorderSteps(calls), logger.New("disabled", true))

	job := &Job{ID: "job-ready", TenantID: "tenant-a"}
	_, err := store.Upsert(context.Background(), job)
	require.NoError(t, err)

	require.NoError(t, exec.Run(context.Background(), job.ID))
	require.Equal(t, []string{"CreateSchema", "CreateRole", "Migrate", "Seed"}, calls.order())

	calls.reset()
	require.NoError(t, exec.Run(context.Background(), job.ID), "re-running a Ready job must be a no-op")
	assert.Equal(t, 0, len(calls.order()), "no steps should run on a Ready job")
}

func TestExecutorFailingStepEntersCleanupAndFails(t *testing.T) {
	store := NewMemoryStore()
	calls := newCallRecorder()
	steps := recorderSteps(calls)
	steps.CreateRole = func(_ context.Context, _ *Job) error {
		calls.record("CreateRole")
		return errors.New("role provisioning blew up")
	}
	exec := mustNewExecutor(t, store, steps, logger.New("disabled", true))

	job := &Job{ID: "job-fail", TenantID: "tenant-a"}
	_, err := store.Upsert(context.Background(), job)
	require.NoError(t, err)

	err = exec.Run(context.Background(), job.ID)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ended in failed state")
	assert.Contains(t, err.Error(), "role provisioning blew up")

	final, err := store.Get(context.Background(), job.ID)
	require.NoError(t, err)
	assert.Equal(t, StateFailed, final.State)
	assert.Equal(t, "role provisioning blew up", final.LastError)

	// CreateSchema ran, CreateRole ran (and failed), Cleanup ran, Migrate / Seed did not.
	assert.Equal(t, []string{"CreateSchema", "CreateRole", "Cleanup"}, calls.order())
}

func TestExecutorCleanupErrorStillReachesFailed(t *testing.T) {
	store := NewMemoryStore()
	calls := newCallRecorder()
	steps := recorderSteps(calls)
	steps.CreateRole = func(_ context.Context, _ *Job) error {
		calls.record("CreateRole")
		return errors.New("forward failure")
	}
	steps.Cleanup = func(_ context.Context, _ *Job) error {
		calls.record("Cleanup")
		return errors.New("cleanup also blew up")
	}
	exec := mustNewExecutor(t, store, steps, logger.New("disabled", true))

	job := &Job{ID: "job-cleanup-err", TenantID: "tenant-a"}
	_, err := store.Upsert(context.Background(), job)
	require.NoError(t, err)

	err = exec.Run(context.Background(), job.ID)
	require.Error(t, err)
	final, err := store.Get(context.Background(), job.ID)
	require.NoError(t, err)
	// The state still reaches Failed — the cleanup error is logged but the
	// transition is not blocked, because leaving a job stuck in StateCleanup
	// would be worse than recording a best-effort failure.
	assert.Equal(t, StateFailed, final.State, "cleanup error must not block the transition to failed")
}

func TestExecutorResumesFromPersistedState(t *testing.T) {
	store := NewMemoryStore()
	calls := newCallRecorder()
	exec := mustNewExecutor(t, store, recorderSteps(calls), logger.New("disabled", true))

	job := &Job{ID: "job-resume", TenantID: "tenant-a"}
	_, err := store.Upsert(context.Background(), job)
	require.NoError(t, err)

	// Manually advance the persisted job past CreateSchema and CreateRole
	// to simulate a previous run that crashed after Migrate succeeded.
	ctx := context.Background()
	require.NoError(t, store.Transition(ctx, job.ID, StatePending, StateSchemaCreated, nil, ""))
	require.NoError(t, store.Transition(ctx, job.ID, StateSchemaCreated, StateRoleCreated, nil, ""))
	require.NoError(t, store.Transition(ctx, job.ID, StateRoleCreated, StateMigrated, nil, ""))

	require.NoError(t, exec.Run(ctx, job.ID))

	final, err := store.Get(ctx, job.ID)
	require.NoError(t, err)
	assert.Equal(t, StateReady, final.State)
	// Only Seed should have been called by this Run — earlier states were
	// already advanced via the manual Transition calls above.
	assert.Equal(t, []string{"Seed"}, calls.order(),
		"resumed run must skip steps whose persisted state already reflects success")
}

func TestExecutorRespectsContextCancellation(t *testing.T) {
	store := NewMemoryStore()
	calls := newCallRecorder()
	exec := mustNewExecutor(t, store, recorderSteps(calls), logger.New("disabled", true))

	job := &Job{ID: "job-cancel", TenantID: "tenant-a"}
	_, err := store.Upsert(context.Background(), job)
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err = exec.Run(ctx, job.ID)
	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)

	final, err := store.Get(context.Background(), job.ID)
	require.NoError(t, err)
	assert.Equal(t, StatePending, final.State, "cancellation before any step must not advance state")
}

// ---- helpers ----

func okSteps(_ *testing.T) Steps {
	noop := func(context.Context, *Job) error { return nil }
	return Steps{
		CreateSchema: noop,
		CreateRole:   noop,
		Migrate:      noop,
		Seed:         noop,
		Cleanup:      noop,
	}
}

func mustNewExecutor(t *testing.T, store StateStore, steps Steps, log logger.Logger) *Executor {
	t.Helper()
	exec, err := NewExecutor(store, steps, log)
	require.NoError(t, err)
	return exec
}

type callRecorder struct {
	calls []string
}

func newCallRecorder() *callRecorder { return &callRecorder{} }

func (r *callRecorder) record(name string) { r.calls = append(r.calls, name) }
func (r *callRecorder) order() []string    { return append([]string(nil), r.calls...) }
func (r *callRecorder) reset()             { r.calls = nil }
func (r *callRecorder) count(name string) int {
	n := 0
	for _, c := range r.calls {
		if c == name {
			n++
		}
	}
	return n
}

func recorderSteps(r *callRecorder) Steps {
	return Steps{
		CreateSchema: func(context.Context, *Job) error { r.record("CreateSchema"); return nil },
		CreateRole:   func(context.Context, *Job) error { r.record("CreateRole"); return nil },
		Migrate:      func(context.Context, *Job) error { r.record("Migrate"); return nil },
		Seed:         func(context.Context, *Job) error { r.record("Seed"); return nil },
		Cleanup:      func(context.Context, *Job) error { r.record("Cleanup"); return nil },
	}
}
