package provisioning

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMemoryStoreUpsertNewRecordStartsPending(t *testing.T) {
	s := NewMemoryStore()
	job := &Job{ID: "j1", TenantID: "t1", State: StateReady, Attempts: 99}
	got, err := s.Upsert(context.Background(), job)
	require.NoError(t, err)
	assert.Equal(t, StatePending, got.State, "Upsert must coerce new records to StatePending")
	assert.Equal(t, 0, got.Attempts, "Upsert must reset Attempts on new records")
	assert.False(t, got.CreatedAt.IsZero())
	assert.False(t, got.UpdatedAt.IsZero())
}

func TestMemoryStoreUpsertExistingReturnsUnchanged(t *testing.T) {
	s := NewMemoryStore()
	ctx := context.Background()
	_, err := s.Upsert(ctx, &Job{ID: "j1", TenantID: "t1"})
	require.NoError(t, err)
	require.NoError(t, s.Transition(ctx, "j1", StatePending, StateSchemaCreated, nil, ""))

	// Re-upserting the same ID with a different state must not overwrite.
	got, err := s.Upsert(ctx, &Job{ID: "j1", TenantID: "t1", State: StateReady})
	require.NoError(t, err)
	assert.Equal(t, StateSchemaCreated, got.State, "Upsert on existing record must preserve persisted state")
}

func TestMemoryStoreGetReturnsErrorWhenMissing(t *testing.T) {
	_, err := NewMemoryStore().Get(context.Background(), "nope")
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrJobNotFound)
}

func TestMemoryStoreGetReturnsDeepCopy(t *testing.T) {
	s := NewMemoryStore()
	ctx := context.Background()
	_, err := s.Upsert(ctx, &Job{ID: "j1", TenantID: "t1", Metadata: map[string]string{"k": "v"}})
	require.NoError(t, err)

	got, err := s.Get(ctx, "j1")
	require.NoError(t, err)
	got.Metadata["k"] = "tampered"

	again, err := s.Get(ctx, "j1")
	require.NoError(t, err)
	assert.Equal(t, "v", again.Metadata["k"], "Get must return a deep copy so callers can't mutate store state")
}

func TestMemoryStoreTransitionGuardsStateGraph(t *testing.T) {
	s := NewMemoryStore()
	ctx := context.Background()
	_, err := s.Upsert(ctx, &Job{ID: "j1", TenantID: "t1"})
	require.NoError(t, err)

	// Illegal edge.
	err = s.Transition(ctx, "j1", StatePending, StateMigrated, nil, "")
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrIllegalTransition))
}

func TestMemoryStoreTransitionEnforcesOptimisticConcurrency(t *testing.T) {
	s := NewMemoryStore()
	ctx := context.Background()
	_, err := s.Upsert(ctx, &Job{ID: "j1", TenantID: "t1"})
	require.NoError(t, err)

	// First transition moves to SchemaCreated.
	require.NoError(t, s.Transition(ctx, "j1", StatePending, StateSchemaCreated, nil, ""))

	// Second writer with the stale `from` must be rejected.
	err = s.Transition(ctx, "j1", StatePending, StateSchemaCreated, nil, "")
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrStaleRead))
}

func TestMemoryStoreTransitionReturnsErrJobNotFound(t *testing.T) {
	err := NewMemoryStore().Transition(context.Background(), "missing", StatePending, StateSchemaCreated, nil, "")
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrJobNotFound))
}

func TestMemoryStoreTransitionUpdatesAttemptsAndMetadata(t *testing.T) {
	s := NewMemoryStore()
	ctx := context.Background()
	_, err := s.Upsert(ctx, &Job{ID: "j1", TenantID: "t1"})
	require.NoError(t, err)

	require.NoError(t, s.Transition(ctx, "j1", StatePending, StateSchemaCreated,
		map[string]string{"flyway_version_range": "1-3"}, ""))

	got, err := s.Get(ctx, "j1")
	require.NoError(t, err)
	assert.Equal(t, 1, got.Attempts, "forward transitions must increment Attempts")
	assert.Equal(t, "1-3", got.Metadata["flyway_version_range"])

	// Cleanup transition must NOT increment attempts (it represents
	// rollback, not progress).
	require.NoError(t, s.Transition(ctx, "j1", StateSchemaCreated, StateCleanup, nil, "step error"))
	got, err = s.Get(ctx, "j1")
	require.NoError(t, err)
	assert.Equal(t, 1, got.Attempts, "cleanup transitions must not bump Attempts")
	assert.Equal(t, "step error", got.LastError)
}

func TestMemoryStoreClockOverride(t *testing.T) {
	frozen := time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)
	s := NewMemoryStore().WithClock(func() time.Time { return frozen })

	got, err := s.Upsert(context.Background(), &Job{ID: "j1", TenantID: "t1"})
	require.NoError(t, err)
	assert.Equal(t, frozen, got.CreatedAt)
	assert.Equal(t, frozen, got.UpdatedAt)
}

func TestMemoryStoreCreateTableIsNoOp(t *testing.T) {
	assert.NoError(t, NewMemoryStore().CreateTable(context.Background()))
}

func TestMemoryStoreUpsertRejectsInvalidJob(t *testing.T) {
	s := NewMemoryStore()
	tests := []struct {
		name      string
		job       *Job
		wantField string
	}{
		{"nil_job", nil, "job is nil"},
		{"missing_id", &Job{TenantID: "t1"}, "job.ID"},
		{"missing_tenant_id", &Job{ID: "j1"}, "job.TenantID"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := s.Upsert(context.Background(), tt.job)
			require.Error(t, err)
			assert.True(t, errors.Is(err, ErrInvalidJob), "want wrapped ErrInvalidJob, got %v", err)
			assert.Contains(t, err.Error(), tt.wantField)
		})
	}
}
