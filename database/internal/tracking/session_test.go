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

// stubSession implements types.Session for exercising tracking.Session.
type stubSession struct {
	queryCalls []struct {
		query string
		args  []any
	}
	execCalls []struct {
		query string
		args  []any
	}
	execErr       error
	beginTxErr    error
	beginTxResult types.Tx
	closeErr      error
	closeCalled   bool
	databaseType  string
}

func (s *stubSession) Query(_ context.Context, query string, args ...any) (*sql.Rows, error) {
	s.queryCalls = append(s.queryCalls, struct {
		query string
		args  []any
	}{query: query, args: append([]any(nil), args...)})
	return new(sql.Rows), nil
}

func (s *stubSession) QueryRow(_ context.Context, _ string, _ ...any) types.Row {
	return types.NewRowFromSQL(new(sql.Row))
}

func (s *stubSession) Exec(_ context.Context, query string, args ...any) (sql.Result, error) {
	s.execCalls = append(s.execCalls, struct {
		query string
		args  []any
	}{query: query, args: append([]any(nil), args...)})
	if s.execErr != nil {
		return nil, s.execErr
	}
	return stubResult(1), nil
}

func (s *stubSession) Begin(ctx context.Context) (types.Tx, error) {
	return s.BeginTx(ctx, nil)
}

func (s *stubSession) BeginTx(_ context.Context, _ *sql.TxOptions) (types.Tx, error) {
	if s.beginTxErr != nil {
		return nil, s.beginTxErr
	}
	if s.beginTxResult == nil {
		s.beginTxResult = &stubTx{}
	}
	return s.beginTxResult, nil
}

func (s *stubSession) Close() error {
	s.closeCalled = true
	return s.closeErr
}

func (s *stubSession) DatabaseType() string {
	return s.databaseType
}

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

func TestSessionQueryTracksLikePoolStatement(t *testing.T) {
	traceExporter, meterProvider, cleanup := setupTestObservabilityProviders(t)
	defer cleanup()

	recLogger := newRecordingLogger()
	sess := NewSession(&stubSession{databaseType: "postgresql"}, recLogger, "postgresql", NewSettings(&config.DatabaseConfig{}))

	_, err := sess.Query(context.Background(), TestQuerySelectUsersParams)
	require.NoError(t, err)

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

func TestSessionExecTracksRowsAffected(t *testing.T) {
	recLogger := newRecordingLogger()
	underlying := &stubSession{databaseType: "postgresql"}
	sess := NewSession(underlying, recLogger, "postgresql", NewSettings(&config.DatabaseConfig{}))

	result, err := sess.Exec(context.Background(), "INSERT INTO t VALUES (1)")
	require.NoError(t, err)
	rows, err := result.RowsAffected()
	require.NoError(t, err)
	assert.Equal(t, int64(1), rows)
	assert.Len(t, underlying.execCalls, 1)
}

func TestSessionBeginWrapsTransaction(t *testing.T) {
	recLogger := newRecordingLogger()
	sess := NewSession(&stubSession{databaseType: "postgresql"}, recLogger, "postgresql", NewSettings(&config.DatabaseConfig{})).(*Session)

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

func TestSessionCloseAndDatabaseTypeDelegate(t *testing.T) {
	underlying := &stubSession{databaseType: "oracle"}
	sess := NewSession(underlying, newRecordingLogger(), "oracle", NewSettings(&config.DatabaseConfig{}))

	assert.Equal(t, "oracle", sess.DatabaseType())
	require.NoError(t, sess.Close())
	assert.True(t, underlying.closeCalled)
}

func TestConnectionSessionTracksAcquisitionAsSessionOp(t *testing.T) {
	traceExporter, _, cleanup := setupTestObservabilityProviders(t)
	defer cleanup()

	recLogger := newRecordingLogger()
	underlying := &stubSessionCapableConnection{
		stubConnection: &stubConnection{databaseTypeValue: "postgresql"},
		sessionResult:  &stubSession{databaseType: "postgresql"},
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
	assert.Nil(t, sess)
}

func TestConnectionSessionErrorsWhenUnderlyingDoesNotSupportSessions(t *testing.T) {
	recLogger := newRecordingLogger()
	underlying := &stubConnection{databaseTypeValue: "postgresql"}
	conn := NewConnection(underlying, recLogger, &config.DatabaseConfig{}).(*Connection)

	sess, err := conn.Session(context.Background())
	require.Error(t, err)
	assert.Nil(t, sess)
}
