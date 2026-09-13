package tracking

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/config"
	"github.com/gaborage/go-bricks/database/types"
	obtest "github.com/gaborage/go-bricks/observability/testing"
)

const dbSessionSpanName = "db.session"

// stubSession is stubConnection (connection_test.go) seen through the smaller
// types.Session surface: it already records every call and carries every error
// knob tracking.Session needs, so re-implementing it here only duplicated it.
type stubSession = stubConnection

var _ types.Session = (*stubSession)(nil)

// stubSessionCapableConnection embeds stubConnection (connection_test.go) and
// additionally implements sessionOpener, so tracking.Connection.Session can be
// exercised end-to-end against a fake underlying types.Interface.
type stubSessionCapableConnection struct {
	*stubConnection
	sessionResult types.Session
	sessionErr    error
}

func (s *stubSessionCapableConnection) Session(context.Context) (types.Session, error) {
	if s.sessionErr != nil {
		return nil, s.sessionErr
	}
	return s.sessionResult, nil
}

// newTrackedStubSession wraps a stub session with default tracking settings and
// no server metadata (TestSessionStatementCarriesServerAttributes covers the
// server-attribute parity case, which must build through tracking.Connection).
func newTrackedStubSession(log *recordingLogger, sess types.Session) types.Session {
	return NewSession(sess, &Context{
		Logger:   log,
		Vendor:   "postgresql",
		Settings: NewSettings(&config.DatabaseConfig{}),
	})
}

func TestSessionQueryTracksLikePoolStatement(t *testing.T) {
	traceExporter, meterProvider, cleanup := setupTestObservabilityProviders(t)
	defer cleanup()

	recLogger := newRecordingLogger()
	underlying := &stubSession{databaseTypeValue: "postgresql"}
	sess := NewSession(underlying, &Context{
		Logger:   recLogger,
		Vendor:   "postgresql",
		Settings: NewSettings(&config.DatabaseConfig{}),
	})

	rows, err := sess.Query(context.Background(), TestQuerySelectUsersParams)
	require.NoError(t, err)
	closeSilently(rows)
	require.Len(t, underlying.queryCalls, 1, "the query must reach the wrapped session")
	assert.Equal(t, TestQuerySelectUsersParams, underlying.queryCalls[0].query)

	spans := traceExporter.GetSpans()
	require.Len(t, spans, 1, "a session query should emit a span exactly like a pool query")
	assert.Equal(t, dbSelectMetric, spans[0].Name)

	rm := meterProvider.Collect(t)
	obtest.AssertMetricExists(t, rm, metricDBDuration)

	events := recLogger.events()
	require.Len(t, events, 1, "a session query should emit a log event exactly like a pool query")
	assert.Equal(t, levelDebug, events[0].Level)
	assert.Equal(t, TestQuerySelectUsersParams, events[0].Fields[logFieldQuery])
}

// TestSessionStatementCarriesServerAttributes pins tracking parity between the
// acquisition span and the session's own statement spans: both must carry
// server.address, server.port and db.namespace. A Session built from only
// logger/vendor/settings silently drops all three.
func TestSessionStatementCarriesServerAttributes(t *testing.T) {
	traceExporter, _, cleanup := setupTestObservabilityProviders(t)
	defer cleanup()

	underlying := &stubSessionCapableConnection{
		stubConnection: &stubConnection{databaseTypeValue: "postgresql"},
		sessionResult:  &stubSession{databaseTypeValue: "postgresql"},
	}
	conn := NewConnection(underlying, newRecordingLogger(), &config.DatabaseConfig{}).(*Connection)
	conn.SetServerInfo("db.example.internal", 5432, "appdb.public")

	ctx := context.Background()
	sess, err := conn.Session(ctx)
	require.NoError(t, err)

	rows, err := sess.Query(ctx, TestQuerySelectUsersParams)
	require.NoError(t, err)
	closeSilently(rows)

	spans := obtest.NewSpanCollector(t, traceExporter)
	for _, name := range []string{dbSessionSpanName, dbSelectMetric} {
		span := spans.WithName(name).AssertCount(1).First()
		obtest.AssertSpanAttribute(t, &span, "server.address", "db.example.internal")
		obtest.AssertSpanAttribute(t, &span, "server.port", 5432)
		obtest.AssertSpanAttribute(t, &span, "db.namespace", "appdb.public")
	}
}

func TestSessionExecTracksRowsAffected(t *testing.T) {
	recLogger := newRecordingLogger()
	underlying := &stubSession{databaseTypeValue: "postgresql"}
	sess := newTrackedStubSession(recLogger, underlying)

	result, err := sess.Exec(context.Background(), "INSERT INTO t VALUES (1)")
	require.NoError(t, err)
	rows, err := result.RowsAffected()
	require.NoError(t, err)
	assert.Equal(t, int64(1), rows)
	assert.Len(t, underlying.execCalls, 1)
}

func TestSessionExecPropagatesError(t *testing.T) {
	wantErr := errors.New("exec failed")
	underlying := &stubSession{databaseTypeValue: "postgresql", execErr: wantErr}
	sess := newTrackedStubSession(newRecordingLogger(), underlying)

	result, err := sess.Exec(context.Background(), "INSERT INTO t VALUES (1)")
	require.ErrorIs(t, err, wantErr)
	assert.Nil(t, result)
	assert.Len(t, underlying.execCalls, 1)
}

// TestSessionQueryRowTracksAndDelegates covers tracking.Session.QueryRow, whose
// rowtracker callback only fires once Scan/Err is called.
func TestSessionQueryRowTracksAndDelegates(t *testing.T) {
	traceExporter, _, cleanup := setupTestObservabilityProviders(t)
	defer cleanup()

	recLogger := newRecordingLogger()
	underlying := &stubSession{databaseTypeValue: "postgresql"}
	sess := newTrackedStubSession(recLogger, underlying)

	row := sess.QueryRow(context.Background(), TestQuerySelectUsersParams)
	require.Len(t, underlying.queryRowCalls, 1, "QueryRow must reach the wrapped session")
	require.NoError(t, row.Scan(new(int)))

	spans := traceExporter.GetSpans()
	require.Len(t, spans, 1, "scanning the row should emit the statement span")
	assert.Equal(t, dbSelectMetric, spans[0].Name)
}

func TestSessionQueryRowPropagatesScanError(t *testing.T) {
	wantErr := errors.New("scan failed")
	underlying := &stubSession{databaseTypeValue: "postgresql", queryRowErr: wantErr}
	sess := newTrackedStubSession(newRecordingLogger(), underlying)

	row := sess.QueryRow(context.Background(), TestQuerySelectUsersParams)
	require.ErrorIs(t, row.Scan(new(int)), wantErr)
	require.ErrorIs(t, row.Err(), wantErr)
}

func TestSessionBeginWrapsTransaction(t *testing.T) {
	recLogger := newRecordingLogger()
	sess := newTrackedStubSession(recLogger, &stubSession{databaseTypeValue: "postgresql"})

	tx, err := sess.Begin(context.Background())
	require.NoError(t, err)
	if _, ok := tx.(*Transaction); !ok {
		t.Fatalf("expected tracked transaction, got %T", tx)
	}

	txWithOpts, err := sess.BeginTx(context.Background(), &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	require.NoError(t, err)
	if _, ok := txWithOpts.(*Transaction); !ok {
		t.Fatalf("expected tracked transaction, got %T", txWithOpts)
	}
}

// TestSessionBeginErrorsReturnNilTx drives the `if err != nil` arms of both
// Begin and BeginTx; the stub fails each through its own knob.
func TestSessionBeginErrorsReturnNilTx(t *testing.T) {
	wantErr := errors.New("begin failed")
	sess := newTrackedStubSession(newRecordingLogger(),
		&stubSession{databaseTypeValue: "postgresql", beginErr: wantErr, beginTxErr: wantErr})

	tx, err := sess.Begin(context.Background())
	require.ErrorIs(t, err, wantErr)
	if tx != nil {
		t.Fatalf("Begin must return a nil types.Tx on failure, got %T", tx)
	}

	txWithOpts, err := sess.BeginTx(context.Background(), &sql.TxOptions{})
	require.ErrorIs(t, err, wantErr)
	if txWithOpts != nil {
		t.Fatalf("BeginTx must return a nil types.Tx on failure, got %T", txWithOpts)
	}
}

func TestSessionCloseAndDatabaseTypeDelegate(t *testing.T) {
	underlying := &stubSession{databaseTypeValue: "oracle"}
	sess := NewSession(underlying, &Context{
		Logger:   newRecordingLogger(),
		Vendor:   "oracle",
		Settings: NewSettings(&config.DatabaseConfig{}),
	})

	assert.Equal(t, "oracle", sess.DatabaseType())
	require.NoError(t, sess.Close())
	assert.True(t, underlying.closeCalled)
}

func TestSessionClosePropagatesError(t *testing.T) {
	wantErr := errors.New("close failed")
	underlying := &stubSession{databaseTypeValue: "oracle", closeErr: wantErr}
	sess := newTrackedStubSession(newRecordingLogger(), underlying)

	require.ErrorIs(t, sess.Close(), wantErr)
	assert.True(t, underlying.closeCalled)
}

func TestConnectionSessionTracksAcquisitionAsSessionOp(t *testing.T) {
	traceExporter, _, cleanup := setupTestObservabilityProviders(t)
	defer cleanup()

	recLogger := newRecordingLogger()
	underlying := &stubSessionCapableConnection{
		stubConnection: &stubConnection{databaseTypeValue: "postgresql"},
		sessionResult:  &stubSession{databaseTypeValue: "postgresql"},
	}
	conn := NewConnection(underlying, recLogger, &config.DatabaseConfig{}).(*Connection)

	sess, err := conn.Session(context.Background())
	require.NoError(t, err)
	if _, ok := sess.(*Session); !ok {
		t.Fatalf("expected tracked session, got %T", sess)
	}

	spans := traceExporter.GetSpans()
	require.Len(t, spans, 1, "session acquisition should be tracked like BEGIN")
	assert.Equal(t, dbSessionSpanName, spans[0].Name)
}

func TestConnectionSessionPropagatesUnderlyingError(t *testing.T) {
	recLogger := newRecordingLogger()
	wantErr := errors.New("acquire failed")
	underlying := &stubSessionCapableConnection{
		stubConnection: &stubConnection{databaseTypeValue: "postgresql"},
		sessionErr:     wantErr,
	}
	conn := NewConnection(underlying, recLogger, &config.DatabaseConfig{}).(*Connection)

	sess, err := conn.Session(context.Background())
	require.ErrorIs(t, err, wantErr)
	if sess != nil {
		t.Fatalf("typed-nil session returned: %T", sess)
	}
}

func TestConnectionSessionErrorsWhenUnderlyingDoesNotSupportSessions(t *testing.T) {
	recLogger := newRecordingLogger()
	underlying := &stubConnection{databaseTypeValue: "postgresql"}
	conn := NewConnection(underlying, recLogger, &config.DatabaseConfig{}).(*Connection)

	sess, err := conn.Session(context.Background())
	require.Error(t, err)
	if sess != nil {
		t.Fatalf("typed-nil session returned: %T", sess)
	}
}
