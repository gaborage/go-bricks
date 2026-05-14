// Package testing provides test utilities for the provisioning state
// machine. The MockStateStore satisfies provisioning.StateStore with a
// thread-safe in-memory backing and configurable failure injection so
// consumer-side step implementations can be tested without a real PG.
//
// Usage:
//
//	store := provtest.NewMockStateStore()
//	exec, err := provisioning.NewExecutor(store, mySteps, myLogger)
//	require.NoError(t, err)
//	_, err = store.Upsert(ctx, &provisioning.Job{ID: "j1", TenantID: "t1"})
//	require.NoError(t, err)
//	require.NoError(t, exec.Run(ctx, "j1"))
//	provtest.AssertJobReachedState(t, store, "j1", provisioning.StateReady)
//
// Tests that want to verify the persisted state graph without exercising
// real PostgreSQL can use this mock everywhere a StateStore is required.
package testing

import (
	"context"
	"sync"

	"github.com/gaborage/go-bricks/migration/provisioning"
)

// MockStateStore is a thread-safe StateStore implementation for unit tests.
// It mirrors the semantics of provisioning.MemoryStore (which is the
// production-side reference implementation) but adds configurable failure
// injection for testing error paths.
type MockStateStore struct {
	mu       sync.Mutex
	delegate *provisioning.MemoryStore

	// Configurable failures — when non-nil, the corresponding method returns
	// the error instead of performing the underlying operation.
	getErr        error
	upsertErr     error
	transitionErr error
	createErr     error

	// transitionLog records every successful Transition for ordering
	// assertions. Failed transitions are not recorded so the log shows
	// the actually-persisted path.
	transitionLog []TransitionEvent
}

// TransitionEvent records a single successful Transition call for assertion.
type TransitionEvent struct {
	JobID string
	From  provisioning.State
	To    provisioning.State
}

// NewMockStateStore returns a fresh MockStateStore.
func NewMockStateStore() *MockStateStore {
	return &MockStateStore{delegate: provisioning.NewMemoryStore()}
}

// WithGetError configures the mock to return err from every Get call.
func (m *MockStateStore) WithGetError(err error) *MockStateStore {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.getErr = err
	return m
}

// WithUpsertError configures the mock to return err from every Upsert call.
func (m *MockStateStore) WithUpsertError(err error) *MockStateStore {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.upsertErr = err
	return m
}

// WithTransitionError configures the mock to return err from every
// Transition call. Useful for testing that the Executor propagates store
// errors (e.g. transient PG connectivity failures) up to its caller.
func (m *MockStateStore) WithTransitionError(err error) *MockStateStore {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.transitionErr = err
	return m
}

// WithCreateTableError configures the mock to return err from CreateTable.
func (m *MockStateStore) WithCreateTableError(err error) *MockStateStore {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.createErr = err
	return m
}

// Get implements StateStore.
func (m *MockStateStore) Get(ctx context.Context, jobID string) (*provisioning.Job, error) {
	m.mu.Lock()
	err := m.getErr
	m.mu.Unlock()
	if err != nil {
		return nil, err
	}
	return m.delegate.Get(ctx, jobID)
}

// Upsert implements StateStore.
func (m *MockStateStore) Upsert(ctx context.Context, job *provisioning.Job) (*provisioning.Job, error) {
	m.mu.Lock()
	err := m.upsertErr
	m.mu.Unlock()
	if err != nil {
		return nil, err
	}
	return m.delegate.Upsert(ctx, job)
}

// Transition implements StateStore.
func (m *MockStateStore) Transition(
	ctx context.Context, jobID string, from, to provisioning.State, metadata map[string]string, lastError string,
) error {
	m.mu.Lock()
	err := m.transitionErr
	m.mu.Unlock()
	if err != nil {
		return err
	}
	if err := m.delegate.Transition(ctx, jobID, from, to, metadata, lastError); err != nil {
		return err
	}
	m.mu.Lock()
	m.transitionLog = append(m.transitionLog, TransitionEvent{JobID: jobID, From: from, To: to})
	m.mu.Unlock()
	return nil
}

// CreateTable implements StateStore.
func (m *MockStateStore) CreateTable(ctx context.Context) error {
	m.mu.Lock()
	err := m.createErr
	m.mu.Unlock()
	if err != nil {
		return err
	}
	return m.delegate.CreateTable(ctx)
}

// TransitionLog returns a snapshot of recorded successful transitions in
// insertion order. Useful for asserting the executor walked the expected
// path.
func (m *MockStateStore) TransitionLog() []TransitionEvent {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]TransitionEvent(nil), m.transitionLog...)
}

// Reset clears all configured errors and the transition log so a single
// MockStateStore can be reused across subtests.
func (m *MockStateStore) Reset() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.delegate = provisioning.NewMemoryStore()
	m.transitionLog = nil
	m.getErr = nil
	m.upsertErr = nil
	m.transitionErr = nil
	m.createErr = nil
}
