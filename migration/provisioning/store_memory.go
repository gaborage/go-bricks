package provisioning

import (
	"context"
	"sync"
	"time"
)

// MemoryStore is an in-process StateStore implementation backed by a map.
// Used by unit tests and as a reference implementation for vendor authors
// adding new backends; not intended for production. All operations are
// goroutine-safe; transitions are serialized by a per-store mutex (not
// per-job) which is fine for test workloads.
type MemoryStore struct {
	mu   sync.Mutex
	jobs map[string]*Job
	// now lets tests inject deterministic timestamps. nil means time.Now.
	now func() time.Time
}

// NewMemoryStore returns an empty MemoryStore ready for use.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{jobs: make(map[string]*Job)}
}

// WithClock returns m with the supplied clock function installed. Useful
// for deterministic timestamps in tests. Passing nil restores time.Now.
func (m *MemoryStore) WithClock(now func() time.Time) *MemoryStore {
	m.now = now
	return m
}

func (m *MemoryStore) timestamp() time.Time {
	if m.now == nil {
		return time.Now().UTC()
	}
	return m.now()
}

// Get returns a deep copy of the persisted job to prevent callers from
// mutating the store's internal state by holding the returned pointer.
func (m *MemoryStore) Get(_ context.Context, jobID string) (*Job, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	j, ok := m.jobs[jobID]
	if !ok {
		return nil, ErrJobNotFound
	}
	return cloneJob(j), nil
}

// Upsert inserts job at StatePending if no record exists, or returns the
// existing record unchanged. The CreatedAt and UpdatedAt fields on the
// returned record are populated by the store. Rejects nil jobs and jobs
// missing ID or TenantID via ErrInvalidJob.
func (m *MemoryStore) Upsert(_ context.Context, job *Job) (*Job, error) {
	if err := validateJobForUpsert(job); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if existing, ok := m.jobs[job.ID]; ok {
		return cloneJob(existing), nil
	}
	now := m.timestamp()
	stored := cloneJob(job)
	stored.State = StatePending
	stored.Attempts = 0
	stored.LastError = ""
	stored.CreatedAt = now
	stored.UpdatedAt = now
	m.jobs[job.ID] = stored
	return cloneJob(stored), nil
}

// Transition advances jobID from -> to with optimistic-concurrency check
// against the persisted state. Returns ErrJobNotFound, ErrIllegalTransition,
// or ErrStaleRead per the StateStore contract.
func (m *MemoryStore) Transition(
	_ context.Context, jobID string, from, to State, metadata map[string]string, lastError string,
) error {
	if err := ValidateTransition(from, to); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	j, ok := m.jobs[jobID]
	if !ok {
		return ErrJobNotFound
	}
	if j.State != from {
		return ErrStaleRead
	}
	j.State = to
	j.UpdatedAt = m.timestamp()
	j.LastError = lastError
	// Attempts increments on every forward transition (cleanup excluded)
	// so consumers can correlate retries with audit events.
	if to != StateCleanup && to != StateFailed {
		j.Attempts++
	}
	if metadata != nil {
		j.Metadata = cloneMetadata(metadata)
	}
	return nil
}

// CreateTable is a no-op for MemoryStore — the in-memory map needs no
// schema. Implemented to satisfy the StateStore interface so MemoryStore
// is a drop-in substitute for backend stores in unit tests.
func (*MemoryStore) CreateTable(_ context.Context) error { return nil }

// cloneJob returns a deep copy of j so callers don't share state with the
// store's internal map.
func cloneJob(j *Job) *Job {
	out := *j
	out.Metadata = cloneMetadata(j.Metadata)
	return &out
}

// cloneMetadata copies m into a fresh map; nil in → nil out so callers that
// pass nil don't accidentally over-allocate.
func cloneMetadata(m map[string]string) map[string]string {
	if m == nil {
		return nil
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}
