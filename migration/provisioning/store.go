package provisioning

import (
	"context"
	"errors"
	"fmt"
)

// StateStore persists Job records and enforces optimistic-concurrency
// transitions. Implementations are typically backed by the same database as
// the tenant's business data; the framework ships a PostgreSQL reference
// implementation at NewPostgresStore.
//
// Per ADR-021, this interface mirrors the *patterns* of outbox.Store
// (idempotent CreateTable, vendor-pluggable, in-memory mock for tests) but
// the method shape is finite-state-machine-oriented rather than
// queue-oriented: there is no FetchPending / MarkPublished pair — the
// dispatcher feeding the executor is out of scope for #379.
type StateStore interface {
	// Get loads the job by ID. Returns ErrJobNotFound when no record exists.
	Get(ctx context.Context, jobID string) (*Job, error)

	// Upsert writes a new job at StatePending if no record exists for jobID,
	// or returns the existing record unchanged if one does. This is the
	// idempotency contract referenced in #379: re-running with the same
	// jobID converges on the same end state without duplicate side effects,
	// because the executor will see the persisted state and resume (or
	// no-op if terminal).
	//
	// Implementations must populate Job.CreatedAt and Job.UpdatedAt on the
	// returned record. State, Attempts, LastError, and Metadata on the
	// passed-in job are used only when the record is newly inserted.
	Upsert(ctx context.Context, job *Job) (*Job, error)

	// Transition advances jobID's State from -> to atomically. Optimistic
	// concurrency: implementations MUST verify the row's current state is
	// `from` before applying the update; if it isn't, return ErrStaleRead
	// without mutating the row.
	//
	// metadata replaces the persisted Metadata in full (not merged) so
	// callers controlling the in-memory job can decide whether to carry
	// step-specific fields forward. lastError replaces the persisted
	// LastError (empty string clears a prior error).
	//
	// The transition is also validated against the static state graph;
	// illegal edges return ErrIllegalTransition.
	Transition(ctx context.Context, jobID string, from, to State, metadata map[string]string, lastError string) error

	// CreateTable creates the persistence table(s) and any required indexes,
	// idempotently. Safe to call on a database where the table already
	// exists. Operators who prefer to manage schema externally can skip
	// this and run the equivalent DDL via their migration tooling — see
	// the implementation's exported DDL constants.
	CreateTable(ctx context.Context) error
}

// Sentinel errors returned by StateStore implementations and the package's
// validation helpers. Use errors.Is to test.
var (
	// ErrJobNotFound is returned by Get when no record exists for the
	// supplied jobID.
	ErrJobNotFound = errors.New("provisioning: job not found")

	// ErrStaleRead is returned by Transition when the persisted state
	// doesn't match the caller's expected `from`. Indicates a concurrent
	// writer changed the job between the caller's read and transition.
	ErrStaleRead = errors.New("provisioning: stale read; concurrent transition detected")

	// ErrInvalidJob is returned by Upsert when the supplied Job is nil
	// or has an empty ID/TenantID. Wrapped with the failing field name.
	ErrInvalidJob = errors.New("provisioning: invalid job")
)

// validateJobForUpsert rejects nil jobs and jobs missing ID or TenantID
// before they reach a StateStore. Without this guard, an empty ID would
// silently collapse multiple tenants onto the same `""` storage key,
// breaking the per-job-ID idempotency contract. Shared between
// MemoryStore.Upsert and PostgresStore.Upsert so both stores behave
// identically.
func validateJobForUpsert(job *Job) error {
	if job == nil {
		return fmt.Errorf("%w: job is nil", ErrInvalidJob)
	}
	if job.ID == "" {
		return fmt.Errorf("%w: job.ID is required", ErrInvalidJob)
	}
	if job.TenantID == "" {
		return fmt.Errorf("%w: job.TenantID is required", ErrInvalidJob)
	}
	return nil
}
