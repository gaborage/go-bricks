package tracking

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/config"
	"github.com/gaborage/go-bricks/database/types"
	obtest "github.com/gaborage/go-bricks/observability/testing"
)

// dbBeginSpanName is the span name both BEGIN and BEGIN_TX map to (see
// extractDBOperation); shared with session_test.go.
const dbBeginSpanName = "db.begin"

const (
	querierServerAddress = "db.querier.internal"
	querierServerPort    = 6432
	querierNamespace     = "querierdb.public"
)

// newStmtTrackerUnder builds a stmtTracker over the shared stubConnection double
// (connection_test.go) with default settings and no server metadata.
func newStmtTrackerUnder(log *recordingLogger, q stmtExecutor) *stmtTracker {
	return &stmtTracker{
		q: q,
		tc: &Context{
			Logger:   log,
			Vendor:   "postgresql",
			Settings: NewSettings(&config.DatabaseConfig{}),
		},
	}
}

func TestStmtTrackerQueryDelegatesAndTracks(t *testing.T) {
	tests := []struct {
		name      string
		queryErr  error
		wantLevel string
	}{
		{name: "success", wantLevel: levelDebug},
		{name: "delegate_error", queryErr: errors.New("query failed"), wantLevel: levelError},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			traceExporter, _, cleanup := setupTestObservabilityProviders(t)
			defer cleanup()

			recLogger := newRecordingLogger()
			underlying := &stubConnection{databaseTypeValue: "postgresql", queryErr: tt.queryErr}
			tracker := newStmtTrackerUnder(recLogger, underlying)

			rows, err := tracker.Query(context.Background(), TestQuerySelectUsersParams, 7)
			closeSilently(rows)

			require.Len(t, underlying.queryCalls, 1, "the query must reach the wrapped executor")
			assert.Equal(t, TestQuerySelectUsersParams, underlying.queryCalls[0].query)
			assert.Equal(t, []any{7}, underlying.queryCalls[0].args, "args must pass through unchanged")
			if tt.queryErr != nil {
				require.ErrorIs(t, err, tt.queryErr)
				assert.Nil(t, rows)
			} else {
				require.NoError(t, err)
				require.NotNil(t, rows)
			}

			spans := traceExporter.GetSpans()
			require.Len(t, spans, 1, "Query must record exactly one operation")
			assert.Equal(t, dbSelectMetric, spans[0].Name)

			events := recLogger.events()
			require.Len(t, events, 1)
			assert.Equal(t, tt.wantLevel, events[0].Level)
			assert.Equal(t, TestQuerySelectUsersParams, events[0].Fields[logFieldQuery])
		})
	}
}

// TestStmtTrackerQueryRowTracksOnScanNotBefore pins the deferred-tracking
// contract: QueryRow returns a rowtracker-wrapped Row and records nothing until
// the caller scans it.
func TestStmtTrackerQueryRowTracksOnScanNotBefore(t *testing.T) {
	traceExporter, _, cleanup := setupTestObservabilityProviders(t)
	defer cleanup()

	recLogger := newRecordingLogger()
	underlying := &stubConnection{databaseTypeValue: "postgresql"}
	tracker := newStmtTrackerUnder(recLogger, underlying)

	row := tracker.QueryRow(context.Background(), TestQuerySelectUsersParams, 1)
	require.Len(t, underlying.queryRowCalls, 1, "QueryRow must reach the wrapped executor")
	assert.Equal(t, []any{1}, underlying.queryRowCalls[0].args)
	assert.Empty(t, traceExporter.GetSpans(), "no span before the row is scanned")
	assert.Empty(t, recLogger.events(), "no log event before the row is scanned")

	require.NoError(t, row.Scan(new(int)))

	spans := traceExporter.GetSpans()
	require.Len(t, spans, 1, "scanning the row records the operation")
	assert.Equal(t, dbSelectMetric, spans[0].Name)
	events := recLogger.events()
	require.Len(t, events, 1)
	assert.Equal(t, levelDebug, events[0].Level)
}

func TestStmtTrackerQueryRowRecordsScanError(t *testing.T) {
	wantErr := errors.New("scan failed")
	recLogger := newRecordingLogger()
	underlying := &stubConnection{databaseTypeValue: "postgresql", queryRowErr: wantErr}
	tracker := newStmtTrackerUnder(recLogger, underlying)

	row := tracker.QueryRow(context.Background(), TestQuerySelectUsersParams)
	require.ErrorIs(t, row.Scan(new(int)), wantErr)

	events := recLogger.events()
	require.Len(t, events, 1, "the scan failure is what gets recorded")
	assert.Equal(t, levelError, events[0].Level)
	assert.Equal(t, msgDBOperationError, events[0].Msg)
}

// TestStmtTrackerExecTracksBothArms covers both Exec arms: the delegate call
// receives the right args either way, a delegate error is wrapped and
// returned with a nil sql.Result, and a successful call returns the
// delegate's real result unchanged. extractRowsAffected's own behavior (nil
// result, the RowsAffected() error arm, the happy path) is pinned directly by
// TestExtractRowsAffected in utils_test.go, not by this test.
func TestStmtTrackerExecTracksBothArms(t *testing.T) {
	tests := []struct {
		name      string
		execErr   error
		wantLevel string
	}{
		{name: "success", wantLevel: levelDebug},
		{name: "delegate_error", execErr: errors.New("exec failed"), wantLevel: levelError},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recLogger := newRecordingLogger()
			underlying := &stubConnection{databaseTypeValue: "postgresql", execErr: tt.execErr}
			tracker := newStmtTrackerUnder(recLogger, underlying)

			result, err := tracker.Exec(context.Background(), "INSERT INTO t VALUES ($1)", 3)

			require.Len(t, underlying.execCalls, 1, "the statement must reach the wrapped executor")
			assert.Equal(t, []any{3}, underlying.execCalls[0].args)
			if tt.execErr != nil {
				require.ErrorIs(t, err, tt.execErr)
				assert.Nil(t, result, "no result to extract rows-affected from")
			} else {
				require.NoError(t, err)
				affected, affErr := result.RowsAffected()
				require.NoError(t, affErr)
				assert.Equal(t, int64(1), affected)
			}

			events := recLogger.events()
			require.Len(t, events, 1)
			assert.Equal(t, tt.wantLevel, events[0].Level)
		})
	}
}

// TestStmtTrackerUsesTrackingContextFields pins that every field of the tracking
// Context reaches a sink: Logger (the event), Vendor (the vendor log field and the
// normalized db.system.name attribute), all three Settings knobs (slow-query
// threshold -> WARN, max query length -> truncation, log parameters -> args), and
// the server metadata attributes.
func TestStmtTrackerUsesTrackingContextFields(t *testing.T) {
	traceExporter, _, cleanup := setupTestObservabilityProviders(t)
	defer cleanup()

	cfg := &config.DatabaseConfig{}
	cfg.Query.Slow.Threshold = time.Nanosecond
	cfg.Query.Log.MaxLength = 10
	cfg.Query.Log.Parameters = true

	recLogger := newRecordingLogger()
	underlying := &stubConnection{databaseTypeValue: "oracle"}
	tracker := &stmtTracker{
		q: underlying,
		tc: &Context{
			Logger:        recLogger,
			Vendor:        "oracle",
			Settings:      NewSettings(cfg),
			ServerAddress: querierServerAddress,
			ServerPort:    querierServerPort,
			Namespace:     querierNamespace,
		},
	}

	rows, err := tracker.Query(context.Background(), TestQuerySelectUsersParams, 42)
	require.NoError(t, err)
	closeSilently(rows)

	events := recLogger.events()
	require.Len(t, events, 1)
	assert.Equal(t, levelWarn, events[0].Level, "Settings.SlowQueryThreshold must select the slow-query event")
	assert.Equal(t, "oracle", events[0].Fields["vendor"], "Context.Vendor must reach the log field")
	assert.Len(t, events[0].Fields[logFieldQuery], 10, "Settings.MaxQueryLength must truncate the logged query")
	assert.NotEmpty(t, events[0].Fields["args"], "Settings.LogQueryParameters must emit the args field")

	span := obtest.NewSpanCollector(t, traceExporter).WithName(dbSelectMetric).AssertCount(1).First()
	obtest.AssertSpanAttribute(t, &span, attrDBSystem, dbVendorOracle)
	obtest.AssertSpanAttribute(t, &span, "server.address", querierServerAddress)
	obtest.AssertSpanAttribute(t, &span, "server.port", querierServerPort)
	obtest.AssertSpanAttribute(t, &span, "db.namespace", querierNamespace)
}

// TestTrackBeginWrapsTxAndRecordsOp pins trackBegin's success arm for both op
// names Connection and Session pass it: the op is recorded under the db.begin
// span and the raw Tx comes back wrapped in a tracked *Transaction that inherits
// the tracking Context's Logger/Vendor/Settings.
func TestTrackBeginWrapsTxAndRecordsOp(t *testing.T) {
	tests := []struct {
		name string
		op   string
	}{
		{name: "begin", op: "BEGIN"},
		{name: "begin_tx", op: "BEGIN_TX"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			traceExporter, _, cleanup := setupTestObservabilityProviders(t)
			defer cleanup()

			recLogger := newRecordingLogger()
			settings := NewSettings(&config.DatabaseConfig{})
			tc := &Context{Logger: recLogger, Vendor: "postgresql", Settings: settings}
			raw := &stubTx{}

			tx, err := trackBegin(context.Background(), tc, tt.op, func(context.Context) (types.Tx, error) {
				return raw, nil
			})
			require.NoError(t, err)

			tracked, ok := tx.(*Transaction)
			require.True(t, ok, "trackBegin must wrap the Tx for tracking, got %T", tx)
			assert.Same(t, raw, tracked.tx, "the wrapper must delegate to the begun Tx")
			assert.Same(t, recLogger, tracked.logger)
			assert.Equal(t, "postgresql", tracked.vendor)
			assert.Equal(t, settings, tracked.settings)

			spans := traceExporter.GetSpans()
			require.Len(t, spans, 1)
			assert.Equal(t, dbBeginSpanName, spans[0].Name)

			events := recLogger.events()
			require.Len(t, events, 1)
			assert.Equal(t, levelDebug, events[0].Level)
			assert.Equal(t, tt.op, events[0].Fields[logFieldQuery], "the op name is what gets recorded")
		})
	}
}

func TestTrackBeginErrorReturnsNilTx(t *testing.T) {
	traceExporter, _, cleanup := setupTestObservabilityProviders(t)
	defer cleanup()

	wantErr := errors.New("begin failed")
	recLogger := newRecordingLogger()
	tc := &Context{
		Logger:   recLogger,
		Vendor:   "postgresql",
		Settings: NewSettings(&config.DatabaseConfig{}),
	}

	tx, err := trackBegin(context.Background(), tc, "BEGIN", func(context.Context) (types.Tx, error) {
		return nil, wantErr
	})
	require.ErrorIs(t, err, wantErr)
	if tx != nil {
		t.Fatalf("a failed begin must return a nil types.Tx, got %T", tx)
	}

	spans := traceExporter.GetSpans()
	require.Len(t, spans, 1, "a failed begin is still recorded")
	assert.Equal(t, dbBeginSpanName, spans[0].Name)
	events := recLogger.events()
	require.Len(t, events, 1)
	assert.Equal(t, levelError, events[0].Level)
	assert.Equal(t, msgDBOperationError, events[0].Msg)
	assert.Equal(t, "BEGIN", events[0].Fields[logFieldQuery])
}
