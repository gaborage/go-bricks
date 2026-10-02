package tracking

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	sqlmock "github.com/DATA-DOG/go-sqlmock"

	"github.com/gaborage/go-bricks/config"
	"github.com/gaborage/go-bricks/database/internal/wrapper"
	"github.com/gaborage/go-bricks/database/types"
	"github.com/gaborage/go-bricks/logger"
)

const (
	levelDebug                 = "debug"
	levelError                 = "error"
	levelInfo                  = "info"
	levelWarn                  = "warn"
	levelFatal                 = "fatal"
	simpleSelect               = "SELECT"
	createMockErrorMsg         = "failed to create sqlmock: %v"
	selectOne                  = "SELECT 1"
	unmetExpectationsErrMsg    = "unmet expectations: %v"
	unexpectedDebugLevelErrMsg = "expected debug level, got %s"
	unexpectedQueryFieldErrMsg = "unexpected query field, got %v"
)

type stubConnection struct {
	queryCalls []struct {
		query string
		args  []any
	}
	queryErr      error
	queryRowCalls []struct {
		query string
		args  []any
	}
	queryRowErr error
	execCalls   []struct {
		query string
		args  []any
	}
	execErr            error
	prepareErr         error
	preparedStatement  types.Statement
	beginErr           error
	beginResult        types.Tx
	beginTxErr         error
	beginTxResult      types.Tx
	healthErr          error
	statsResult        map[string]any
	statsErr           error
	closeErr           error
	closeCalled        bool
	migrationTable     string
	databaseTypeValue  string
	createMigrationErr error
	sessionResult      types.Session
	sessionErr         error
}

var _ types.Interface = (*stubConnection)(nil)

func closeSilently(rows *sql.Rows) {
	if rows == nil {
		return
	}
	defer func() {
		_ = recover()
	}()
	_ = rows.Close()
}

func (s *stubConnection) Query(_ context.Context, query string, args ...any) (*sql.Rows, error) {
	s.queryCalls = append(s.queryCalls, struct {
		query string
		args  []any
	}{query: query, args: append([]any(nil), args...)})
	if s.queryErr != nil {
		return nil, s.queryErr
	}
	return new(sql.Rows), nil
}

func (s *stubConnection) QueryRow(_ context.Context, query string, args ...any) types.Row {
	s.queryRowCalls = append(s.queryRowCalls, struct {
		query string
		args  []any
	}{query: query, args: append([]any(nil), args...)})
	// stubRow (statement_test.go) is Scan-safe; a zero *sql.Row panics on Scan.
	return &stubRow{scanErr: s.queryRowErr, err: s.queryRowErr}
}

func (s *stubConnection) Exec(_ context.Context, query string, args ...any) (sql.Result, error) {
	s.execCalls = append(s.execCalls, struct {
		query string
		args  []any
	}{query: query, args: append([]any(nil), args...)})
	if s.execErr != nil {
		return nil, s.execErr
	}
	return stubResult(1), nil
}

func (s *stubConnection) Prepare(_ context.Context, _ string) (types.Statement, error) {
	if s.prepareErr != nil {
		return nil, s.prepareErr
	}
	if s.preparedStatement == nil {
		s.preparedStatement = &stubStatement{}
	}
	return s.preparedStatement, nil
}

func (s *stubConnection) Begin(_ context.Context) (types.Tx, error) {
	if s.beginErr != nil {
		return nil, s.beginErr
	}
	if s.beginResult == nil {
		s.beginResult = &stubTx{}
	}
	return s.beginResult, nil
}

func (s *stubConnection) BeginTx(_ context.Context, _ *sql.TxOptions) (types.Tx, error) {
	if s.beginTxErr != nil {
		return nil, s.beginTxErr
	}
	if s.beginTxResult == nil {
		s.beginTxResult = &stubTx{}
	}
	return s.beginTxResult, nil
}

func (s *stubConnection) Health(context.Context) error { return s.healthErr }

func (s *stubConnection) Stats() (map[string]any, error) {
	return s.statsResult, s.statsErr
}

func (s *stubConnection) Close() error {
	s.closeCalled = true
	return s.closeErr
}

func (s *stubConnection) DatabaseType() string {
	if s.databaseTypeValue == "" {
		return "stub"
	}
	return s.databaseTypeValue
}

func (s *stubConnection) MigrationTable() string {
	if s.migrationTable == "" {
		return "schema_migrations"
	}
	return s.migrationTable
}

func (s *stubConnection) Session(context.Context) (types.Session, error) {
	if s.sessionErr != nil {
		return nil, s.sessionErr
	}
	return s.sessionResult, nil
}

func (s *stubConnection) CreateMigrationTable(context.Context) error {
	return s.createMigrationErr
}

func TestNewConnectionDelegatesAndLogs(t *testing.T) {
	underlying := &stubConnection{databaseTypeValue: "postgresql"}
	recLogger := newRecordingLogger()
	conn := NewConnection(underlying, recLogger, &config.DatabaseConfig{}).(*Connection)

	ctx := logger.WithRequestCounters(context.Background())
	rows, err := conn.Query(ctx, simpleSelect, 1)
	if err != nil {
		t.Fatalf("expected query to succeed")
	}
	closeSilently(rows)
	if len(underlying.queryCalls) != 1 || underlying.queryCalls[0].query != simpleSelect {
		t.Fatalf("expected underlying query to be invoked")
	}
	events := recLogger.events()
	if len(events) != 1 || events[0].Fields["query"] != simpleSelect {
		t.Fatalf("expected log entry for query, got %+v", events)
	}
}

func TestConnectionQueryRowTracksOperations(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf(createMockErrorMsg, err)
	}
	defer db.Close()

	// Set up mock to return a row that can be scanned
	mock.ExpectQuery(selectOne).WithArgs(1).WillReturnRows(sqlmock.NewRows([]string{"result"}).AddRow(99))

	recLogger := newRecordingLogger()
	underlying := &sqlmockConnection{Connection: &wrapper.Connection{DB: db, Logger: recLogger, Name: "PostgreSQL"}}
	conn := NewConnection(underlying, recLogger, &config.DatabaseConfig{}).(*Connection)

	ctx := logger.WithRequestCounters(context.Background())
	row := conn.QueryRow(ctx, selectOne, 1)
	if row == nil {
		t.Fatalf("expected row result")
	}

	// Scan the row to trigger the rowtracker callback which logs the operation
	var result int
	err = row.Scan(&result)
	if err != nil {
		t.Fatalf("expected no error on scan, got %v", err)
	}
	if result != 99 {
		t.Fatalf("expected result 99, got %d", result)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf(unmetExpectationsErrMsg, err)
	}

	events := recLogger.events()
	if len(events) != 1 {
		t.Fatalf("expected one event from the Connection.QueryRow wrapper, got %d", len(events))
	}
	event := events[0]
	if event.Level != levelDebug {
		t.Fatalf(unexpectedDebugLevelErrMsg, event.Level)
	}
	if event.Fields["query"] != selectOne {
		t.Fatalf(unexpectedQueryFieldErrMsg, event.Fields["query"])
	}
}

// sqlmockConnection backs a types.Interface with a sqlmock *sql.DB through the
// vendor-agnostic wrapper, so Connection can be tested against real database/sql rows.
type sqlmockConnection struct {
	*wrapper.Connection
}

var _ types.Interface = (*sqlmockConnection)(nil)

func (m *sqlmockConnection) DatabaseType() string { return "postgresql" }

func (m *sqlmockConnection) MigrationTable() string { return "flyway_schema_history" }

func (m *sqlmockConnection) Session(context.Context) (types.Session, error) {
	return nil, errors.New("sqlmockConnection does not open sessions")
}

func (m *sqlmockConnection) CreateMigrationTable(context.Context) error { return nil }

func TestConnectionExecErrorIsLogged(t *testing.T) {
	underlying := &stubConnection{databaseTypeValue: "postgresql", execErr: errors.New("boom")}
	recLogger := newRecordingLogger()
	conn := NewConnection(underlying, recLogger, &config.DatabaseConfig{}).(*Connection)

	ctx := logger.WithRequestCounters(context.Background())
	_, err := conn.Exec(ctx, "UPDATE", 2)
	if err == nil {
		t.Fatalf("expected error to propagate")
	}
	events := recLogger.events()
	if len(events) != 1 || events[0].Level != levelError {
		t.Fatalf("expected error log, got %+v", events)
	}
}

func TestConnectionPrepareWrapsStatement(t *testing.T) {
	underlying := &stubConnection{databaseTypeValue: "postgresql"}
	recLogger := newRecordingLogger()
	conn := NewConnection(underlying, recLogger, &config.DatabaseConfig{}).(*Connection)

	stmt, err := conn.Prepare(context.Background(), selectOne)
	if err != nil {
		t.Fatalf("expected prepare to succeed")
	}
	if _, ok := stmt.(*Statement); !ok {
		t.Fatalf("expected tracked statement, got %T", stmt)
	}
}

func TestConnectionPreparePropagatesError(t *testing.T) {
	underlying := &stubConnection{databaseTypeValue: "postgresql", prepareErr: errors.New("prepare fail")}
	recLogger := newRecordingLogger()
	conn := NewConnection(underlying, recLogger, &config.DatabaseConfig{}).(*Connection)

	_, err := conn.Prepare(context.Background(), selectOne)
	if err == nil {
		t.Fatalf("expected prepare error")
	}
	if len(recLogger.events()) != 1 {
		t.Fatalf("expected log entry for prepare failure")
	}
}

func TestConnectionBeginWrapsTransaction(t *testing.T) {
	underlying := &stubConnection{databaseTypeValue: "postgresql"}
	recLogger := newRecordingLogger()
	conn := NewConnection(underlying, recLogger, &config.DatabaseConfig{}).(*Connection)

	tx, err := conn.Begin(context.Background())
	if err != nil {
		t.Fatalf("expected begin success")
	}
	defer tx.Rollback(context.Background()) // No-op: test transaction
	if _, ok := tx.(*Transaction); !ok {
		t.Fatalf("expected tracked transaction, got %T", tx)
	}

	txWithOpts, err := conn.BeginTx(context.Background(), &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		t.Fatalf("expected begin tx success")
	}
	defer txWithOpts.Rollback(context.Background()) // No-op: test transaction
	if _, ok := txWithOpts.(*Transaction); !ok {
		t.Fatalf("expected tracked transaction, got %T", txWithOpts)
	}
}

func TestConnectionBeginPropagatesError(t *testing.T) {
	underlying := &stubConnection{databaseTypeValue: "postgresql", beginErr: errors.New("begin fail")}
	recLogger := newRecordingLogger()
	conn := NewConnection(underlying, recLogger, &config.DatabaseConfig{}).(*Connection)

	ctx := context.Background()
	tx, err := conn.Begin(ctx)
	if tx != nil {
		defer tx.Rollback(ctx) // Safety: should never execute since Begin is expected to fail
	}
	if err == nil {
		t.Fatalf("expected begin error")
	}
}

func TestConnectionCreateMigrationTableLogs(t *testing.T) {
	underlying := &stubConnection{databaseTypeValue: "postgresql", createMigrationErr: errors.New("migrate fail")}
	recLogger := newRecordingLogger()
	conn := NewConnection(underlying, recLogger, &config.DatabaseConfig{}).(*Connection)

	err := conn.CreateMigrationTable(logger.WithRequestCounters(context.Background()))
	if err == nil {
		t.Fatalf("expected migration error")
	}
	if len(recLogger.events()) != 1 || recLogger.events()[0].Level != levelError {
		t.Fatalf("expected error log for migration failure")
	}
}

func TestConnectionPassthroughMethods(t *testing.T) {
	stats := map[string]any{"ok": true}
	underlying := &stubConnection{
		databaseTypeValue: "postgresql",
		statsResult:       stats,
		healthErr:         nil,
		closeErr:          nil,
		migrationTable:    "schema",
	}
	recLogger := newRecordingLogger()
	conn := NewConnection(underlying, recLogger, &config.DatabaseConfig{}).(*Connection)

	if conn.Health(context.Background()) != nil {
		t.Fatalf("expected health to succeed")
	}
	gotStats, err := conn.Stats()
	if err != nil || gotStats["ok"] != true {
		t.Fatalf("unexpected stats result: %v %v", gotStats, err)
	}
	if conn.Close() != nil {
		t.Fatalf("expected close to succeed")
	}
	if !underlying.closeCalled {
		t.Fatalf("expected underlying close to be invoked")
	}
	if conn.DatabaseType() != "postgresql" {
		t.Fatalf("unexpected database type")
	}
	if conn.MigrationTable() != "schema" {
		t.Fatalf("unexpected migration table")
	}
}

func TestConnectionSetServerInfo(t *testing.T) {
	underlying := &stubConnection{databaseTypeValue: "postgresql"}
	recLogger := newRecordingLogger()
	conn := NewConnection(underlying, recLogger, &config.DatabaseConfig{}).(*Connection)

	// Set server metadata
	conn.SetServerInfo("localhost", 5432, "mydb.public")

	// Verify metadata was set by checking internal fields
	if conn.serverAddress != "localhost" {
		t.Fatalf("expected serverAddress to be 'localhost', got %s", conn.serverAddress)
	}
	if conn.serverPort != 5432 {
		t.Fatalf("expected serverPort to be 5432, got %d", conn.serverPort)
	}
	if conn.namespace != "mydb.public" {
		t.Fatalf("expected namespace to be 'mydb.public', got %s", conn.namespace)
	}
}
