// Package provisioning implements a durable, crash-recoverable state machine
// for dynamic per-tenant provisioning under the multi-tenant migration model
// defined in issue #379. Each transition is persisted via a StateStore before
// the next step runs, so a worker crash mid-flow can be resumed by reloading
// the persisted state and continuing from there.
//
// PostgreSQL is the only vendor with a default StateStore in v1; the Store
// interface is vendor-pluggable so Oracle (#385) and other backends can be
// added without changing the executor or step callbacks.
//
// Reuse decision: this package mirrors the *patterns* of the outbox/ package
// (status enum on a row, retry counter, vendor-pluggable Store, bundled DDL
// with idempotent auto-create, in-memory mock for tests) but diverges on the
// data model. Outbox is a fire-and-forget event queue with idempotency-token
// deduplication; provisioning needs finite-state graph semantics with
// blocking transitions and per-tenant scope. See ADR-021 for the full
// rationale.
package provisioning

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/gaborage/go-bricks/logger"
)

// State enumerates the per-tenant provisioning states defined in issue #379.
// Transitions are validated by the package's static graph (see allowedNext);
// illegal transitions return ErrIllegalTransition and never silently succeed.
type State string

const (
	// StatePending is the initial state when a job is first persisted.
	// The worker has not started any side-effecting work.
	StatePending State = "pending"

	// StateSchemaCreated marks the successful CREATE SCHEMA owned by the
	// migrator role (idempotent via CREATE SCHEMA IF NOT EXISTS).
	StateSchemaCreated State = "schema_created"

	// StateRoleCreated marks the successful creation of the per-tenant
	// runtime role with the locked-down privilege model from #378.
	StateRoleCreated State = "role_created"

	// StateMigrated marks the successful application of all pending Flyway
	// migrations against the tenant schema.
	StateMigrated State = "migrated"

	// StateSeeded marks the successful completion of reference-data seeding
	// and any external-system registrations the consumer wires in.
	StateSeeded State = "seeded"

	// StateReady is the terminal success state. The tenant is fully usable
	// by the runtime service. No further transitions are allowed.
	StateReady State = "ready"

	// StateCleanup is the rollback state entered when any forward step
	// returns an error. The cleanup step drops partially-provisioned
	// resources (schema, role).
	StateCleanup State = "cleanup"

	// StateFailed is the terminal failure state. Retry creates a new job
	// ID rather than reusing the existing one — this keeps the failure
	// trace intact for audit purposes.
	StateFailed State = "failed"
)

// IsTerminal reports whether s is a terminal state (no further transitions).
func (s State) IsTerminal() bool {
	return s == StateReady || s == StateFailed
}

// allowedNext encodes the directed transition graph from issue #379.
// Every non-terminal forward state additionally allows StateCleanup as a
// branch; the cleanup step drops partial work and transitions to
// StateFailed when done.
var allowedNext = map[State][]State{
	StatePending:       {StateSchemaCreated, StateCleanup, StateFailed},
	StateSchemaCreated: {StateRoleCreated, StateCleanup},
	StateRoleCreated:   {StateMigrated, StateCleanup},
	StateMigrated:      {StateSeeded, StateCleanup},
	StateSeeded:        {StateReady, StateCleanup},
	StateCleanup:       {StateFailed},
	StateReady:         nil, // terminal
	StateFailed:        nil, // terminal
}

// nextForward maps each state to the single forward-progress state that the
// Executor advances to when the corresponding step succeeds. Cleanup is the
// fallback when the forward step errors and is therefore not represented
// here. Terminal states map to themselves (the Executor never advances).
var nextForward = map[State]State{
	StatePending:       StateSchemaCreated,
	StateSchemaCreated: StateRoleCreated,
	StateRoleCreated:   StateMigrated,
	StateMigrated:      StateSeeded,
	StateSeeded:        StateReady,
}

// ValidateTransition reports whether moving from -> to is permitted by the
// state graph. Returns nil when the transition is valid, ErrIllegalTransition
// (wrapped with the offending edge) otherwise.
func ValidateTransition(from, to State) error {
	allowed, ok := allowedNext[from]
	if !ok {
		return fmt.Errorf("%w: unknown source state %q", ErrIllegalTransition, from)
	}
	for _, candidate := range allowed {
		if candidate == to {
			return nil
		}
	}
	return fmt.Errorf("%w: %s -> %s not permitted", ErrIllegalTransition, from, to)
}

// ErrIllegalTransition is returned by ValidateTransition and Executor when
// the caller attempts an edge not in the state graph.
var ErrIllegalTransition = errors.New("provisioning: illegal state transition")

// Job is the persisted record of a single per-tenant provisioning attempt.
// All fields except Metadata are owned by the StateStore; Metadata is opaque
// to the framework and gives consumers a place to record step-specific data
// that must survive a crash (e.g., the applied Flyway version range).
//
// SECURITY: Metadata is persisted in plaintext (JSONB on PostgreSQL). Do not
// store credentials, secret tokens, or other sensitive material in it — use
// a secret manager and record only opaque references (e.g. the secret
// name) when continuity across crashes is needed.
type Job struct {
	ID        string            // Idempotency key — re-running with the same ID converges on the same end state.
	TenantID  string            // Logical tenant identifier the job provisions.
	State     State             // Current state. Updated by every Transition.
	Attempts  int               // Number of forward-step attempts on the current state, used for retry bookkeeping.
	LastError string            // Most recent step error, surfaced in audit events. Empty on success paths.
	Metadata  map[string]string // Step-specific data that must survive a crash. Consumer-owned shape; see SECURITY note above.
	CreatedAt time.Time         // First persistence timestamp; set by Upsert.
	UpdatedAt time.Time         // Most recent persistence timestamp; updated on every Transition.
}

// Steps groups the consumer-supplied callbacks invoked by the Executor at
// each forward transition. The CreateSchema step runs to leave StatePending,
// the CreateRole step runs to leave StateSchemaCreated, and so on. Cleanup
// runs when any forward step errors; the executor moves the job to
// StateCleanup, invokes Cleanup, then transitions to StateFailed regardless
// of the cleanup outcome (cleanup errors are logged but do not block the
// failure transition).
//
// Each step receives the current Job and may update Job.Metadata in place;
// the updated Metadata is persisted by the Executor as part of the
// subsequent Transition call.
type Steps struct {
	CreateSchema func(ctx context.Context, job *Job) error
	CreateRole   func(ctx context.Context, job *Job) error
	Migrate      func(ctx context.Context, job *Job) error
	Seed         func(ctx context.Context, job *Job) error
	Cleanup      func(ctx context.Context, job *Job) error
}

// Steps field-name constants. Exported via Steps.Validate's error messages
// (and recorded in callers' test fixtures) so the literal "CreateSchema"
// etc. is referenced once per name across the package.
const (
	stepNameCreateSchema = "CreateSchema"
	stepNameCreateRole   = "CreateRole"
	stepNameMigrate      = "Migrate"
	stepNameSeed         = "Seed"
	stepNameCleanup      = "Cleanup"
)

// Validate reports whether all required step callbacks are non-nil.
// Returns an error naming the first missing field so misconfiguration
// fails fast at Executor construction rather than mid-flow.
func (s *Steps) Validate() error {
	missing := ""
	switch {
	case s.CreateSchema == nil:
		missing = stepNameCreateSchema
	case s.CreateRole == nil:
		missing = stepNameCreateRole
	case s.Migrate == nil:
		missing = stepNameMigrate
	case s.Seed == nil:
		missing = stepNameSeed
	case s.Cleanup == nil:
		missing = stepNameCleanup
	}
	if missing != "" {
		return fmt.Errorf("provisioning: Steps.%s is required", missing)
	}
	return nil
}

// Executor advances a persisted Job through the state machine, invoking the
// consumer-supplied Steps at each forward transition. Run is safe to call
// against a partially-completed job — the Executor loads the persisted
// state and resumes from there.
//
// Crash recovery contract: each step MUST be idempotent. If the worker
// crashes after a step's side effect succeeds but before the Transition
// is persisted, the next Run call re-invokes the same step. Schema and
// role creation are idempotent by construction (see migration/roles.go);
// Migrate is idempotent because Flyway tracks applied versions in
// flyway_schema_history; Seed is the consumer's responsibility to design
// idempotently (typical pattern: INSERT ... ON CONFLICT DO NOTHING).
type Executor struct {
	Store  StateStore
	Steps  Steps
	Logger logger.Logger
}

// NewExecutor constructs an Executor and validates its dependencies. Returns
// an error if store is nil, logger is nil, or any required step is nil.
func NewExecutor(store StateStore, steps Steps, log logger.Logger) (*Executor, error) {
	if store == nil {
		return nil, errors.New("provisioning: NewExecutor requires a non-nil StateStore")
	}
	if log == nil {
		return nil, errors.New("provisioning: NewExecutor requires a non-nil Logger")
	}
	if err := steps.Validate(); err != nil {
		return nil, err
	}
	return &Executor{Store: store, Steps: steps, Logger: log}, nil
}

// Run advances jobID from its persisted state to StateReady (or StateFailed
// if any forward step errors). Safe to call repeatedly with the same jobID:
// if the job is already at a terminal state, Run is a no-op and returns nil
// for StateReady or the last persisted LastError wrapped in a sentinel for
// StateFailed.
//
// The Executor calls each forward step at most once per Run for a given
// state; ctx is propagated to each step so callers can use context
// cancellation to bound total runtime.
func (e *Executor) Run(ctx context.Context, jobID string) error {
	job, err := e.Store.Get(ctx, jobID)
	if err != nil {
		return fmt.Errorf("provisioning: load job %q: %w", jobID, err)
	}

	for !job.State.IsTerminal() {
		if err := ctx.Err(); err != nil {
			return err
		}

		// StateCleanup is special: we run the Cleanup step and unconditionally
		// transition to StateFailed regardless of its outcome. The cleanup
		// error (if any) is logged but doesn't block the failure transition.
		if job.State == StateCleanup {
			if cleanupErr := e.Steps.Cleanup(ctx, job); cleanupErr != nil {
				e.Logger.Error().
					Err(cleanupErr).
					Str("job_id", job.ID).
					Str("tenant_id", job.TenantID).
					Msg("Cleanup step returned error; proceeding to failed anyway")
				if job.LastError == "" {
					job.LastError = "cleanup error: " + cleanupErr.Error()
				}
			}
			if err := e.Store.Transition(ctx, job.ID, StateCleanup, StateFailed, job.Metadata, job.LastError); err != nil {
				return fmt.Errorf("provisioning: transition to failed: %w", err)
			}
			job.State = StateFailed
			continue
		}

		// StateSeeded → StateReady is an acknowledgment-only edge: the Seed
		// step (which ran while exiting StateMigrated) already performed all
		// required work. Modeling it as an automatic advance keeps the Steps
		// struct one-callback-per-side-effect; consumers needing an
		// "activation" hook (e.g. notify a control plane) can do it inside
		// their Seed step rather than introducing a sixth callback.
		if job.State == StateSeeded {
			if err := e.Store.Transition(ctx, job.ID, StateSeeded, StateReady, job.Metadata, ""); err != nil {
				return fmt.Errorf("provisioning: transition seeded -> ready: %w", err)
			}
			job.LastError = ""
			job.State = StateReady
			continue
		}

		// Forward step.
		step := e.stepFor(job.State)
		if step == nil {
			return fmt.Errorf("provisioning: no step registered for state %q", job.State)
		}

		stepErr := step(ctx, job)
		if stepErr != nil {
			e.Logger.Warn().
				Err(stepErr).
				Str("job_id", job.ID).
				Str("tenant_id", job.TenantID).
				Str("state", string(job.State)).
				Msg("Forward step failed; entering cleanup")
			job.LastError = stepErr.Error()
			if err := e.Store.Transition(ctx, job.ID, job.State, StateCleanup, job.Metadata, job.LastError); err != nil {
				return fmt.Errorf("provisioning: transition to cleanup: %w", err)
			}
			job.State = StateCleanup
			continue
		}

		// Step succeeded — advance to the forward target.
		next := nextForward[job.State]
		if err := e.Store.Transition(ctx, job.ID, job.State, next, job.Metadata, ""); err != nil {
			return fmt.Errorf("provisioning: transition %s -> %s: %w", job.State, next, err)
		}
		job.LastError = ""
		job.State = next
	}

	if job.State == StateFailed {
		return fmt.Errorf("provisioning: job %q ended in failed state: %s", job.ID, job.LastError)
	}
	return nil
}

// stepFor returns the forward step that exits state s. Run dispatches the
// acknowledgment-only StateSeeded → StateReady edge and the StateCleanup →
// StateFailed edge inline (their semantics differ from forward-step states),
// so this function only handles states whose exit is gated by a callback.
func (e *Executor) stepFor(s State) func(context.Context, *Job) error {
	switch s {
	case StatePending:
		return e.Steps.CreateSchema
	case StateSchemaCreated:
		return e.Steps.CreateRole
	case StateRoleCreated:
		return e.Steps.Migrate
	case StateMigrated:
		return e.Steps.Seed
	case StateSeeded, StateCleanup, StateReady, StateFailed:
		// Handled inline by Run (Seeded/Cleanup) or terminal (Ready/Failed).
		// Returning nil surfaces a programmer error if Run ever dispatches
		// these to stepFor.
		return nil
	}
	return nil
}
