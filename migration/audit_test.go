package migration

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/gaborage/go-bricks/config"
	"github.com/gaborage/go-bricks/logger"
)

// recordingSink is a test AuditSink that captures every Record call.
type recordingSink struct {
	mu     sync.Mutex
	events []AuditEvent
	err    error
	delay  time.Duration
	hits   chan struct{}
}

func newRecordingSink() *recordingSink {
	return &recordingSink{hits: make(chan struct{}, 256)}
}

func (s *recordingSink) Record(_ context.Context, event *AuditEvent) error {
	if s.delay > 0 {
		time.Sleep(s.delay)
	}
	s.mu.Lock()
	s.events = append(s.events, *event)
	s.mu.Unlock()
	select {
	case s.hits <- struct{}{}:
	default:
	}
	return s.err
}

func (s *recordingSink) snapshot() []AuditEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]AuditEvent, len(s.events))
	copy(out, s.events)
	return out
}

// waitForFirst blocks until at least one event has been recorded or the
// timeout elapses; t.Fatal on timeout. Used to bridge the async sink
// fan-out into synchronous test assertions.
func (s *recordingSink) waitForFirst(t *testing.T, timeout time.Duration) {
	t.Helper()
	deadline := time.After(timeout)
	for {
		s.mu.Lock()
		got := len(s.events)
		s.mu.Unlock()
		if got >= 1 {
			return
		}
		select {
		case <-s.hits:
		case <-deadline:
			t.Fatalf("audit sink: expected at least one event within %s, got %d", timeout, got)
		}
	}
}

// setupTestTracer installs an in-memory tracer provider for the duration of
// the test and returns the exporter so callers can inspect emitted spans.
func setupTestTracer(t *testing.T) *tracetest.InMemoryExporter {
	t.Helper()
	exporter := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	previous := otel.GetTracerProvider()
	otel.SetTracerProvider(tp)
	t.Cleanup(func() {
		_ = tp.Shutdown(context.Background())
		otel.SetTracerProvider(previous)
	})
	return exporter
}

// disabledLogger returns a no-op logger matching the convention used across
// the rest of the migration tests.
func disabledLogger() logger.Logger {
	return logger.New("disabled", true)
}

const (
	testTarget    = "tenant_acme"
	testPrincipal = "ci@example.com"
	testVendor    = "postgresql"
)

func baseEvent() *AuditEvent {
	now := time.Now()
	return &AuditEvent{
		Type:               AuditEventTypeMigrationApplied,
		Target:             testTarget,
		AppliedByPrincipal: testPrincipal,
		StartedAt:          now,
		CompletedAt:        now.Add(50 * time.Millisecond),
		Outcome:            AuditOutcomeSuccess,
		GitCommitSHA:       "deadbeef",
		PipelineRunID:      "run-42",
		Attributes:         map[string]string{"migration.vendor": testVendor},
	}
}

func TestAuditEventTypesArePublishedConstants(t *testing.T) {
	assert.Equal(t, AuditEventType("migration.applied"), AuditEventTypeMigrationApplied)
	assert.Equal(t, AuditEventType("state.transitioned"), AuditEventTypeStateTransitioned)
	assert.Equal(t, AuditEventType("quiesce.set"), AuditEventTypeQuiesceSet)
	assert.Equal(t, AuditEventType("quiesce.cleared"), AuditEventTypeQuiesceCleared)
}

func TestErrorClassValuesMatchADR019Taxonomy(t *testing.T) {
	want := map[ErrorClass]string{
		ErrorClassChecksumMismatch:     "checksum_mismatch",
		ErrorClassLockTimeout:          "lock_timeout",
		ErrorClassSchemaHistoryCorrupt: "schema_history_corrupt",
		ErrorClassTargetNotReady:       "target_not_ready",
		ErrorClassTargetUnreachable:    "target_unreachable",
		ErrorClassQuiesceBlocked:       "quiesce_blocked",
		ErrorClassInternal:             "internal_error",
	}
	for got, expected := range want {
		assert.Equal(t, expected, string(got))
	}
}

func TestEmitterEmitsSpanWithExpectedAttributes(t *testing.T) {
	exporter := setupTestTracer(t)
	emitter := newAuditEmitter(disabledLogger(), nil)

	emitter.emit(context.Background(), baseEvent())

	spans := exporter.GetSpans()
	require.Len(t, spans, 1)
	span := spans[0]

	assert.Equal(t, "migration.audit.migration.applied", span.Name)
	attrs := map[string]string{}
	for _, kv := range span.Attributes {
		attrs[string(kv.Key)] = kv.Value.AsString()
	}
	assert.Equal(t, "migration.applied", attrs["audit.type"])
	assert.Equal(t, testTarget, attrs["audit.target"])
	assert.Equal(t, testPrincipal, attrs["audit.principal"])
	assert.Equal(t, "success", attrs["audit.outcome"])
	assert.Equal(t, "migration", attrs["code.namespace"])
	assert.Equal(t, "deadbeef", attrs["audit.git_commit_sha"])
	assert.Equal(t, "run-42", attrs["audit.pipeline_run_id"])
	assert.Equal(t, testVendor, attrs["audit.attr.migration.vendor"])
}

func TestEmitterFanOutsToSink(t *testing.T) {
	setupTestTracer(t)
	sink := newRecordingSink()
	emitter := newAuditEmitter(disabledLogger(), sink)
	t.Cleanup(func() { _ = emitter.Close(context.Background()) })

	emitter.emit(context.Background(), baseEvent())

	sink.waitForFirst(t, time.Second)
	events := sink.snapshot()
	require.Len(t, events, 1)
	assert.Equal(t, AuditEventTypeMigrationApplied, events[0].Type)
	assert.Equal(t, testPrincipal, events[0].AppliedByPrincipal)
}

func TestEmitterSinkErrorIsNotPropagated(t *testing.T) {
	setupTestTracer(t)
	sink := newRecordingSink()
	sink.err = errors.New("kafka unavailable")
	emitter := newAuditEmitter(disabledLogger(), sink)
	t.Cleanup(func() { _ = emitter.Close(context.Background()) })

	// emit must not panic, must not block, must not propagate sink failure.
	emitter.emit(context.Background(), baseEvent())

	sink.waitForFirst(t, time.Second)
	assert.Len(t, sink.snapshot(), 1, "sink should have received the event despite returning an error")
}

func TestEmitterMissingPrincipalSubstitutesSentinel(t *testing.T) {
	setupTestTracer(t)
	sink := newRecordingSink()
	emitter := newAuditEmitter(disabledLogger(), sink)
	t.Cleanup(func() { _ = emitter.Close(context.Background()) })

	ev := baseEvent()
	ev.AppliedByPrincipal = ""
	emitter.emit(context.Background(), ev)

	sink.waitForFirst(t, time.Second)
	events := sink.snapshot()
	require.Len(t, events, 1)
	assert.Equal(t, PrincipalUnspecified, events[0].AppliedByPrincipal,
		"missing principal should be replaced with the sentinel so the gap is auditable")
}

func TestEmitterFailedOutcomeMarksSpanError(t *testing.T) {
	exporter := setupTestTracer(t)
	emitter := newAuditEmitter(disabledLogger(), nil)

	ev := baseEvent()
	ev.Outcome = AuditOutcomeFailed
	ev.ErrorClass = ErrorClassChecksumMismatch
	emitter.emit(context.Background(), ev)

	spans := exporter.GetSpans()
	require.Len(t, spans, 1)
	// codes.Error is 1 in the OTel SDK; check via the span status code.
	assert.Equal(t, "Error", spans[0].Status.Code.String())
	assert.Equal(t, string(ErrorClassChecksumMismatch), spans[0].Status.Description)
}

func TestEmitterDoesNotPanicWithoutSink(t *testing.T) {
	setupTestTracer(t)
	emitter := newAuditEmitter(disabledLogger(), nil)

	require.NotPanics(t, func() {
		emitter.emit(context.Background(), baseEvent())
	})
}

func TestEmitterCloseDrainsQueue(t *testing.T) {
	setupTestTracer(t)
	sink := newRecordingSink()
	emitter := newAuditEmitter(disabledLogger(), sink)

	// Emit a handful of events, then close — the consumer goroutine should
	// finish draining before Close returns.
	for i := 0; i < 5; i++ {
		emitter.emit(context.Background(), baseEvent())
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	require.NoError(t, emitter.Close(ctx))
	assert.Len(t, sink.snapshot(), 5)
}

func TestMigrateForEmitsAuditEventOnSuccess(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script stub not supported on windows CI")
	}
	setupTestTracer(t)

	cfg := &config.Config{
		Database: config.DatabaseConfig{
			Type: "postgresql", Host: "h", Port: 15432,
			Username: "user", Password: "longenough-pw", Database: "db",
		},
		App: config.AppConfig{Env: "test"},
	}
	sink := newRecordingSink()
	fm := NewFlywayMigrator(cfg, logger.New("disabled", true)).WithAuditSink(sink)
	t.Cleanup(func() { _ = fm.Close(context.Background()) })

	stub := createFlywayStub(t, "postgresql")
	mcfg := &Config{
		FlywayPath:    stub,
		ConfigPath:    filepath.Join(t.TempDir(), "flyway.conf"),
		MigrationPath: filepath.Join(t.TempDir(), "migrations"),
		Timeout:       10 * time.Second,
		Environment:   cfg.App.Env,
		Audit: AuditContext{
			Principal:     "deployer@example.com",
			GitCommitSHA:  "cafebabe",
			PipelineRunID: "gha-987",
			Target:        "tenant_acme",
		},
	}
	require.NoError(t, os.WriteFile(mcfg.ConfigPath, []byte(""), 0o644))
	require.NoError(t, os.MkdirAll(mcfg.MigrationPath, 0o755))

	require.NoError(t, fm.Migrate(context.Background(), mcfg))

	sink.waitForFirst(t, 2*time.Second)
	events := sink.snapshot()
	require.Len(t, events, 1)

	got := events[0]
	assert.Equal(t, AuditEventTypeMigrationApplied, got.Type)
	assert.Equal(t, "tenant_acme", got.Target, "AuditContext.Target overrides db.Database")
	assert.Equal(t, "deployer@example.com", got.AppliedByPrincipal)
	assert.Equal(t, AuditOutcomeSuccess, got.Outcome)
	assert.Equal(t, "cafebabe", got.GitCommitSHA)
	assert.Equal(t, "gha-987", got.PipelineRunID)
	assert.Empty(t, got.ErrorClass, "successful runs should not set ErrorClass")
	assert.Equal(t, "postgresql", got.Attributes["migration.vendor"])
	assert.False(t, got.StartedAt.IsZero())
	assert.False(t, got.CompletedAt.IsZero())
	assert.True(t, got.CompletedAt.After(got.StartedAt) || got.CompletedAt.Equal(got.StartedAt))
}

func TestMigrateForEmitsClassifiedErrorOnFailure(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script stub not supported on windows CI")
	}
	setupTestTracer(t)

	cfg := &config.Config{
		Database: config.DatabaseConfig{
			Type: "postgresql", Host: "h", Port: 15432,
			Username: "user", Password: "longenough-pw", Database: "db",
		},
		App: config.AppConfig{Env: "test"},
	}
	sink := newRecordingSink()
	fm := NewFlywayMigrator(cfg, logger.New("disabled", true)).WithAuditSink(sink)
	t.Cleanup(func() { _ = fm.Close(context.Background()) })

	// Failing stub that prints a recognizable checksum-mismatch line.
	dir := t.TempDir()
	stub := filepath.Join(dir, "flyway-fail.sh")
	require.NoError(t, os.WriteFile(stub, []byte("#!/bin/sh\necho 'ERROR: Migration checksum mismatch for migration version 5.0'\nexit 1\n"), 0o755))

	mcfg := &Config{
		FlywayPath:    stub,
		ConfigPath:    filepath.Join(t.TempDir(), "flyway.conf"),
		MigrationPath: filepath.Join(t.TempDir(), "migrations"),
		Timeout:       10 * time.Second,
		Environment:   cfg.App.Env,
		Audit:         AuditContext{Principal: "deployer@example.com"},
	}
	require.NoError(t, os.WriteFile(mcfg.ConfigPath, []byte(""), 0o644))
	require.NoError(t, os.MkdirAll(mcfg.MigrationPath, 0o755))

	err := fm.Migrate(context.Background(), mcfg)
	require.Error(t, err, "stub exits non-zero so Migrate must report failure")

	sink.waitForFirst(t, 2*time.Second)
	events := sink.snapshot()
	require.Len(t, events, 1)
	assert.Equal(t, AuditOutcomeFailed, events[0].Outcome)
	assert.Equal(t, ErrorClassChecksumMismatch, events[0].ErrorClass)
}

func TestInfoAndValidateDoNotEmitMigrationApplied(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script stub not supported on windows CI")
	}
	setupTestTracer(t)

	cfg := &config.Config{
		Database: config.DatabaseConfig{
			Type: "postgresql", Host: "h", Port: 15432,
			Username: "user", Password: "longenough-pw", Database: "db",
		},
		App: config.AppConfig{Env: "test"},
	}
	sink := newRecordingSink()
	fm := NewFlywayMigrator(cfg, logger.New("disabled", true)).WithAuditSink(sink)

	stub := createFlywayStub(t, "postgresql")
	mcfg := &Config{
		FlywayPath:    stub,
		ConfigPath:    filepath.Join(t.TempDir(), "flyway.conf"),
		MigrationPath: filepath.Join(t.TempDir(), "migrations"),
		Timeout:       10 * time.Second,
		Environment:   cfg.App.Env,
		Audit:         AuditContext{Principal: "deployer@example.com"},
	}
	require.NoError(t, os.WriteFile(mcfg.ConfigPath, []byte(""), 0o644))
	require.NoError(t, os.MkdirAll(mcfg.MigrationPath, 0o755))

	require.NoError(t, fm.Info(context.Background(), mcfg))
	require.NoError(t, fm.Validate(context.Background(), mcfg))

	// Close drains the sink queue deterministically — any event enqueued
	// (incorrectly) by Info/Validate would arrive before Close returns. A
	// fixed sleep here would be flaky under load and meaningless on a fast
	// machine.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	require.NoError(t, fm.Close(ctx))

	assert.Empty(t, sink.snapshot(),
		"Info/Validate must not emit migration.applied per ADR-019 (only the migrate verb does)")
}

func TestEmitterCloseIsIdempotent(t *testing.T) {
	setupTestTracer(t)
	sink := newRecordingSink()
	emitter := newAuditEmitter(disabledLogger(), sink)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	require.NoError(t, emitter.Close(ctx))
	// Second Close must be a no-op, not a "close of closed channel" panic.
	require.NoError(t, emitter.Close(ctx))
}

func TestEmitterEmitAfterCloseDoesNotPanic(t *testing.T) {
	setupTestTracer(t)
	sink := newRecordingSink()
	emitter := newAuditEmitter(disabledLogger(), sink)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	require.NoError(t, emitter.Close(ctx))

	// emit after Close must not panic; the OTel emission still happens, the
	// sink delivery is dropped silently.
	require.NotPanics(t, func() {
		emitter.emit(context.Background(), baseEvent())
	})
}

func TestEmitterConcurrentEmitAndCloseDoesNotPanic(t *testing.T) {
	// Race-detector flushes out the "send on closed channel" panic class if
	// the enqueueForSink ↔ Close synchronization regresses.
	setupTestTracer(t)
	sink := newRecordingSink()
	emitter := newAuditEmitter(disabledLogger(), sink)

	// Hammer emit from many goroutines while a concurrent Close races.
	var emitWG sync.WaitGroup
	const writers = 8
	const perWriter = 64
	for i := 0; i < writers; i++ {
		emitWG.Add(1)
		go func() {
			defer emitWG.Done()
			for j := 0; j < perWriter; j++ {
				emitter.emit(context.Background(), baseEvent())
			}
		}()
	}

	// Close mid-stream — must not panic regardless of which emits land first.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	require.NotPanics(t, func() {
		_ = emitter.Close(ctx)
	})
	emitWG.Wait()
}

func TestEmitterQueueFullDropsRatherThanBlocks(t *testing.T) {
	setupTestTracer(t)
	sink := newRecordingSink()
	sink.delay = 50 * time.Millisecond // slow sink so the queue can saturate
	emitter := newAuditEmitter(disabledLogger(), sink)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = emitter.Close(ctx)
	})

	// Emit auditSinkQueueCap + 8 events as fast as possible. The emitter
	// must never block, even though the sink is slow — overflow events are
	// dropped silently (with a counter increment).
	overflow := 8
	start := time.Now()
	for i := 0; i < auditSinkQueueCap+overflow; i++ {
		emitter.emit(context.Background(), baseEvent())
	}
	elapsed := time.Since(start)

	// If emit blocked on the sink we'd see at least (auditSinkQueueCap+8) *
	// 50ms ~= 13s; the non-blocking design completes the enqueue loop in
	// well under one second.
	require.Less(t, elapsed, time.Second,
		"emit() must not block on a slow sink; took %s", elapsed)
}
