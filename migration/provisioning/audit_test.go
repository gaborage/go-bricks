package provisioning

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/logger"
	"github.com/gaborage/go-bricks/migration"
)

// captureEmitter is a test migration.Emitter that records every emitted event
// by value, so assertions can inspect the full state.transitioned sequence
// synchronously (no async sink fan-out).
type captureEmitter struct {
	mu     sync.Mutex
	events []migration.AuditEvent
}

func (c *captureEmitter) Emit(_ context.Context, ev *migration.AuditEvent) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.events = append(c.events, *ev)
}

func (c *captureEmitter) Close(context.Context) error { return nil }

func (c *captureEmitter) snapshot() []migration.AuditEvent {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]migration.AuditEvent, len(c.events))
	copy(out, c.events)
	return out
}

const (
	auditTestTenant    = "tenant-acme"
	auditTestPrincipal = "deployer@example.com"
)

func auditCtxFixture() AuditContext {
	return AuditContext{
		Principal:     auditTestPrincipal,
		GitCommitSHA:  "deadbeef",
		PipelineRunID: "run-42",
	}
}

type transitionEdge struct{ from, to string }

func edgesOf(events []migration.AuditEvent) []transitionEdge {
	edges := make([]transitionEdge, 0, len(events))
	for i := range events {
		edges = append(edges, transitionEdge{events[i].FromState, events[i].ToState})
	}
	return edges
}

// runWithCapturedAudit runs a job through the Executor with a synchronous
// capture emitter and returns the emitted events together with Run's error so
// callers can assert both the audit trail and the run outcome.
func runWithCapturedAudit(t *testing.T, steps Steps, jobID string) ([]migration.AuditEvent, error) {
	t.Helper()
	store := NewMemoryStore()
	emitter := &captureEmitter{}
	exec := mustNewExecutor(t, store, steps, logger.New("disabled", true)).
		WithAudit(emitter, auditCtxFixture())

	_, err := store.Upsert(context.Background(), &Job{ID: jobID, TenantID: auditTestTenant})
	require.NoError(t, err)
	runErr := exec.Run(context.Background(), jobID)
	return emitter.snapshot(), runErr
}

func TestExecutorEmitsStateTransitionedOnHappyPath(t *testing.T) {
	events, err := runWithCapturedAudit(t, okSteps(t), "job-happy")
	require.NoError(t, err)

	for _, ev := range events {
		assert.Equal(t, migration.AuditEventTypeStateTransitioned, ev.Type)
		assert.Equal(t, auditTestTenant, ev.Target, "Target must be the tenant identifier, never a DSN")
		assert.Equal(t, auditTestPrincipal, ev.AppliedByPrincipal)
		assert.Equal(t, migration.AuditOutcomeSuccess, ev.Outcome)
		assert.Equal(t, "deadbeef", ev.GitCommitSHA)
		assert.Equal(t, "run-42", ev.PipelineRunID)
		assert.Empty(t, ev.ErrorClass, "success transitions carry no error class")
	}

	assert.Equal(t, []transitionEdge{
		{"pending", "schema_created"},
		{"schema_created", "role_created"},
		{"role_created", "migrated"},
		{"migrated", "seeded"},
		{"seeded", "ready"},
	}, edgesOf(events), "every forward transition must emit one state.transitioned event in order")
}

func TestExecutorEmitsFailedTerminalTransitionOnStepFailure(t *testing.T) {
	stepErr := errors.New("migrate boom")
	steps := okSteps(t)
	steps.Migrate = func(context.Context, *Job) error { return stepErr }

	events, err := runWithCapturedAudit(t, steps, "job-fail")
	require.Error(t, err, "a failed forward step must surface a Run error")

	// The audited path is robust to topology: assert the full edge sequence
	// rather than indexing into specific positions.
	assert.Equal(t, []transitionEdge{
		{"pending", "schema_created"},
		{"schema_created", "role_created"},
		{"role_created", "cleanup"},
		{"cleanup", "failed"},
	}, edgesOf(events))

	byTo := make(map[string]migration.AuditEvent, len(events))
	for i := range events {
		byTo[events[i].ToState] = events[i]
	}

	// The forward edge into cleanup succeeds as a transition.
	assert.Equal(t, migration.AuditOutcomeSuccess, byTo["cleanup"].Outcome)

	// The terminal cleanup -> failed transition is the audited failure,
	// classified by ErrorClass only.
	failed := byTo["failed"]
	assert.Equal(t, migration.AuditOutcomeFailed, failed.Outcome)
	assert.Equal(t, migration.ErrorClassInternal, failed.ErrorClass)

	// Security regression guard: the raw step error (which can wrap driver
	// errors embedding DSNs/credentials) must NEVER reach an audit attribute,
	// which bypasses the logger's field-name-based SensitiveDataFilter.
	for i := range events {
		for k, v := range events[i].Attributes {
			assert.NotContains(t, v, stepErr.Error(),
				"audit attribute %q leaked the raw step error", k)
		}
	}
}

func TestExecutorStateTransitionCarriesJobIDAttribute(t *testing.T) {
	events, err := runWithCapturedAudit(t, okSteps(t), "job-attr")
	require.NoError(t, err)
	require.NotEmpty(t, events)
	for _, ev := range events {
		assert.Equal(t, "job-attr", ev.Attributes[attrKeyJobID])
	}
}

func TestExecutorWithoutAuditEmitterRunsCleanly(t *testing.T) {
	store := NewMemoryStore()
	exec := mustNewExecutor(t, store, okSteps(t), logger.New("disabled", true))
	_, err := store.Upsert(context.Background(), &Job{ID: "job-noaudit", TenantID: auditTestTenant})
	require.NoError(t, err)

	require.NoError(t, exec.Run(context.Background(), "job-noaudit"),
		"a nil audit emitter must not panic or alter Run semantics")
}

// provSink is a local migration.AuditRecorder for the end-to-end wiring tests
// that exercise the real migration.Emitter (async sink fan-out) rather than the
// synchronous capture fake.
type provSink struct {
	mu     sync.Mutex
	events []migration.AuditEvent
}

func (s *provSink) Record(_ context.Context, ev *migration.AuditEvent) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, *ev)
	return nil
}

func (s *provSink) snapshot() []migration.AuditEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]migration.AuditEvent, len(s.events))
	copy(out, s.events)
	return out
}

// TestExecutorEmitsThroughRealEmitterReachSink wires the Executor to a real
// migration.Emitter (not the capture fake) and asserts the full happy-path
// transition sequence reaches a configured AuditRecorder — the integration
// seam #382 requires.
func TestExecutorEmitsThroughRealEmitterReachSink(t *testing.T) {
	sink := &provSink{}
	emitter := migration.NewEmitter(logger.New("disabled", true), sink)

	store := NewMemoryStore()
	exec := mustNewExecutor(t, store, okSteps(t), logger.New("disabled", true)).
		WithAudit(emitter, auditCtxFixture())
	_, err := store.Upsert(context.Background(), &Job{ID: "job-real", TenantID: auditTestTenant})
	require.NoError(t, err)
	require.NoError(t, exec.Run(context.Background(), "job-real"))

	// Close drains the async sink fan-out so every enqueued event is delivered
	// before we assert (and tears the emitter down — no separate cleanup).
	require.NoError(t, emitter.Close(context.Background()))

	events := sink.snapshot()
	require.Len(t, events, 5)
	for _, ev := range events {
		assert.Equal(t, migration.AuditEventTypeStateTransitioned, ev.Type)
		assert.Equal(t, auditTestPrincipal, ev.AppliedByPrincipal)
	}
	assert.Equal(t, "ready", events[len(events)-1].ToState)
}

// TestExecutorEmitsFailedTransitionThroughRealEmitterWithSentinelPrincipal
// exercises the failure path AND the empty-principal sentinel substitution
// end-to-end through the real emitter and sink. An empty AuditContext.Principal
// must surface migration.PrincipalUnspecified per ADR-019 — the orchestrator
// never injects a default of its own.
func TestExecutorEmitsFailedTransitionThroughRealEmitterWithSentinelPrincipal(t *testing.T) {
	sink := &provSink{}
	emitter := migration.NewEmitter(logger.New("disabled", true), sink)

	steps := okSteps(t)
	steps.Migrate = func(context.Context, *Job) error { return errors.New("migrate boom") }
	store := NewMemoryStore()
	exec := mustNewExecutor(t, store, steps, logger.New("disabled", true)).
		WithAudit(emitter, AuditContext{}) // empty principal on purpose
	_, err := store.Upsert(context.Background(), &Job{ID: "job-fail-real", TenantID: auditTestTenant})
	require.NoError(t, err)
	require.Error(t, exec.Run(context.Background(), "job-fail-real"))

	require.NoError(t, emitter.Close(context.Background()))

	events := sink.snapshot()
	require.NotEmpty(t, events)
	for _, ev := range events {
		assert.Equal(t, migration.PrincipalUnspecified, ev.AppliedByPrincipal,
			"empty Principal must surface the <unspecified> sentinel, not an inferred value")
	}
	last := events[len(events)-1]
	assert.Equal(t, "failed", last.ToState)
	assert.Equal(t, migration.AuditOutcomeFailed, last.Outcome)
	assert.Equal(t, migration.ErrorClassInternal, last.ErrorClass)
}
