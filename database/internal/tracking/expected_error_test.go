package tracking

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/codes"

	"github.com/gaborage/go-bricks/logger"
	testconsts "github.com/gaborage/go-bricks/testing"
)

const lockNotAvailableCode = "55P03"

func lockNotAvailableErr(message string) error {
	return &pgconn.PgError{Code: lockNotAvailableCode, Message: message}
}

// isLockNotAvailable mirrors database.IsLockNotAvailable's PostgreSQL arm; the
// tracking package cannot import database without a cycle.
func isLockNotAvailable(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == lockNotAvailableCode
}

// trackWithRecorder runs TrackDBOperation once against a fresh recording spy and
// returns the single event it produced.
func trackWithRecorder(ctx context.Context, t *testing.T, err error) *eventRecord {
	t.Helper()
	recLogger := newRecordingLogger()
	settings := Settings{slowQueryThreshold: time.Second}
	start := time.Now().Add(-10 * time.Millisecond)
	TrackDBOperation(ctx, &Context{Logger: recLogger, Vendor: "postgresql", Settings: settings}, selectOne, nil, start, 0, err)

	events := recLogger.events()
	require.Len(t, events, 1)
	return events[0]
}

// severityHookCtx returns ctx carrying a severity hook plus a reader for the
// levels it received — the real escalation seam (logger adapter -> trackSeverity).
func severityHookCtx(ctx context.Context) (hooked context.Context, levels func() []zerolog.Level) {
	var seen []zerolog.Level
	hooked = logger.WithSeverityHook(ctx, func(level zerolog.Level) { seen = append(seen, level) })
	return hooked, func() []zerolog.Level { return seen }
}

func TestWithExpectedErrorIgnoresNilInputs(t *testing.T) {
	//nolint:staticcheck // SA1012: deliberately exercising the nil-ctx guard
	assert.Nil(t, WithExpectedError(nil, isLockNotAvailable))

	ctx := context.Background()
	assert.Equal(t, ctx, WithExpectedError(ctx, nil))
	assert.False(t, isDeclaredExpected(ctx, lockNotAvailableErr("x")))
	assert.False(t, isBenignError(WithExpectedError(ctx, isLockNotAvailable), nil))
}

func TestTrackDBOperationDeclaredExpectedErrorLogsDebug(t *testing.T) {
	ctx := WithExpectedError(logger.WithRequestCounters(context.Background()), isLockNotAvailable)
	lockErr := lockNotAvailableErr("could not obtain lock on row in relation \"outbox_leader\"")

	event := trackWithRecorder(ctx, t, lockErr)

	assert.Equal(t, levelDebug, event.Level)
	assert.Equal(t, msgDBOperationExpectedError, event.Msg)
	assert.NotEqual(t, msgDBOperationError, event.Msg)
	assert.Equal(t, dbErrorClass(lockErr), event.Fields["error_type"], "error_type must be kept on the expected-error line")
	require.NoError(t, event.Err, "the raw driver error must not be attached via .Err()")
	assertNoFieldsLeak(t, event.Fields, lockErr.Error())
	assert.Equal(t, int64(1), logger.GetDBCounter(ctx))
}

func TestTrackDBOperationDeclaredExpectedErrorLeavesSpanUnset(t *testing.T) {
	traceExporter, _, cleanup := setupTestObservabilityProviders(t)
	defer cleanup()

	ctx := WithExpectedError(context.Background(), isLockNotAvailable)
	tc := &Context{Logger: newDisabledTestLogger(), Vendor: "postgresql", Settings: NewSettings(nil)}
	TrackDBOperation(ctx, tc, "SELECT id FROM outbox_leader FOR UPDATE NOWAIT", nil, time.Now(), 0, lockNotAvailableErr("locked"))

	spans := traceExporter.GetSpans()
	require.Len(t, spans, 1)
	assert.Equal(t, codes.Unset, spans[0].Status.Code)
	assert.Empty(t, spans[0].Events, "a declared-expected error must not record an exception event")
}

// TestTrackDBOperationUndeclaredLockTimeoutStaysLoud is the negative pin: a 55P03
// raised by an expired lock_timeout, on a statement WITHOUT a declaration, is a
// real failure — ERROR line, span Error, severity escalated.
func TestTrackDBOperationUndeclaredLockTimeoutStaysLoud(t *testing.T) {
	traceExporter, _, cleanup := setupTestObservabilityProviders(t)
	defer cleanup()

	timeoutErr := lockNotAvailableErr("canceling statement due to lock timeout")

	event := trackWithRecorder(context.Background(), t, timeoutErr)
	assert.Equal(t, levelError, event.Level)
	assert.Equal(t, msgDBOperationError, event.Msg)

	spans := traceExporter.GetSpans()
	require.Len(t, spans, 1)
	assert.Equal(t, codes.Error, spans[0].Status.Code)

	ctx, levels := severityHookCtx(context.Background())
	tc := &Context{Logger: logger.New(testconsts.TestLoggerLevelDisabled, false), Vendor: "postgresql", Settings: NewSettings(nil)}
	TrackDBOperation(ctx, tc, selectOne, nil, time.Now(), 0, timeoutErr)
	assert.Equal(t, []zerolog.Level{zerolog.ErrorLevel}, levels())
}

func TestWithExpectedErrorNestedDeclarationsCompose(t *testing.T) {
	outerErr := errors.New("outer expected")
	innerErr := errors.New("inner expected")
	otherErr := errors.New("unexpected")

	outer := WithExpectedError(context.Background(), func(err error) bool { return errors.Is(err, outerErr) })
	inner := WithExpectedError(outer, func(err error) bool { return errors.Is(err, innerErr) })

	tests := []struct {
		name      string
		ctx       context.Context
		err       error
		wantLevel string
	}{
		{name: "inner_ctx_outer_predicate", ctx: inner, err: outerErr, wantLevel: levelDebug},
		{name: "inner_ctx_inner_predicate", ctx: inner, err: innerErr, wantLevel: levelDebug},
		{name: "inner_ctx_unmatched", ctx: inner, err: otherErr, wantLevel: levelError},
		{name: "outer_ctx_does_not_see_inner", ctx: outer, err: innerErr, wantLevel: levelError},
		{name: "undeclared_ctx", ctx: context.Background(), err: outerErr, wantLevel: levelError},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			event := trackWithRecorder(tt.ctx, t, tt.err)
			assert.Equal(t, tt.wantLevel, event.Level)
		})
	}
}

// TestTrackDBOperationDeclarationKeepsBenignMessages pins that ErrNoRows and
// ErrTxDone keep their own DEBUG messages even under a declaration that matches.
func TestTrackDBOperationDeclarationKeepsBenignMessages(t *testing.T) {
	ctx := WithExpectedError(context.Background(), func(error) bool { return true })

	noRows := trackWithRecorder(ctx, t, sql.ErrNoRows)
	assert.Equal(t, levelDebug, noRows.Level)
	assert.Equal(t, msgDBOperationNoRows, noRows.Msg)

	txDone := trackWithRecorder(ctx, t, sql.ErrTxDone)
	assert.Equal(t, levelDebug, txDone.Level)
	assert.Equal(t, msgDBTxFinalized, txDone.Msg)
}
