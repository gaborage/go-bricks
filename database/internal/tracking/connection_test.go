package tracking

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	sqlmock "github.com/DATA-DOG/go-sqlmock"

	"github.com/gaborage/go-bricks/config"
	"github.com/gaborage/go-bricks/database/types"
	"github.com/gaborage/go-bricks/logger"
)

const (
	levelDebug         = "debug"
	levelError         = "error"
	levelInfo          = "info"
	levelWarn          = "warn"
	levelFatal         = "fatal"
	simpleSelect       = "SELECT"
	createMockErrorMsg = "failed to create sqlmock: %v"
	selectOne          = "SELECT 1"
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
	execCalls []struct {
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
}

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
	return types.NewRowFromSQL(new(sql.Row))
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

func (s *stubConnection) GetMigrationTable() string {
	if s.migrationTable == "" {
		return "schema_migrations"
	}
	return s.migrationTable
}

func (s *stubConnection) CreateMigrationTable(context.Context) error {
	return s.createMigrationErr
}

func TestNewDBQueryContextTracksOperations(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf(createMockErrorMsg, err)
	}
	defer db.Close()

	mock.ExpectQuery(selectOne).WithArgs(1).WillReturnRows(sqlmock.NewRows([]string{"col"}).AddRow(1))

	cfg := &config.DatabaseConfig{}
	cfg.Query.Log.Parameters = true
	cfg.Query.Log.MaxLength = 100

	recLogger := newRecordingLogger()
	tracked := NewDB(db, recLogger, "postgresql", cfg)
	ctx := logger.WithDBCounter(context.Background())

	rows, err := tracked.QueryContext(ctx, selectOne, 1)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if rows == nil {
		t.Fatalf("expected rows result")
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}

	events := recLogger.events()
	if len(events) != 1 {
		t.Fatalf("expected single event, got %d", len(events))
	}
	event := events[0]
	if event.Level != levelDebug {
		t.Fatalf("expected debug level, got %s", event.Level)
	}
	if event.Fields["query"] != selectOne {
		t.Fatalf("unexpected query field: %v", event.Fields["query"])
	}
	argsField, ok := event.Fields["args"].([]any)
	if !ok || len(argsField) != 1 || argsField[0] != "1" {
		t.Fatalf("expected logged args, got %v", event.Fields["args"])
	}
}

func TestDBExecContextLogsErrors(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf(createMockErrorMsg, err)
	}
	defer db.Close()

	execErr := errors.New("fail")
	mock.ExpectExec("UPDATE").WillReturnError(execErr)

	recLogger := newRecordingLogger()
	tracked := NewDB(db, recLogger, "postgresql", &config.DatabaseConfig{})
	ctx := logger.WithDBCounter(context.Background())

	_, err = tracked.ExecContext(ctx, "UPDATE", 1)
	if !errors.Is(err, execErr) {
		t.Fatalf("expected exec error, got %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
	events := recLogger.events()
	if len(events) != 1 || events[0].Level != levelError {
		t.Fatalf("expected error log, got %+v", events)
	}
}

func TestDBPrepareContextWrapsStatement(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf(createMockErrorMsg, err)
	}
	defer db.Close()

	mock.ExpectPrepare(simpleSelect).WillReturnError(nil)

	recLogger := newRecordingLogger()
	tracked := NewDB(db, recLogger, "postgresql", &config.DatabaseConfig{})

	stmt, err := tracked.PrepareContext(context.Background(), simpleSelect)
	if err != nil {
		t.Fatalf("expected prepare to succeed, got %v", err)
	}
	if _, ok := stmt.(*Statement); !ok {
		t.Fatalf("expected *Statement, got %T", stmt)
	}
}

func TestDBPrepareContextPropagatesError(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf(createMockErrorMsg, err)
	}
	defer db.Close()

	mock.ExpectPrepare(simpleSelect).WillReturnError(errors.New("prepare fail"))

	recLogger := newRecordingLogger()
	tracked := NewDB(db, recLogger, "postgresql", &config.DatabaseConfig{})

	_, err = tracked.PrepareContext(context.Background(), simpleSelect)
	if err == nil {
		t.Fatalf("expected prepare error")
	}
}

func TestNewConnectionDelegatesAndLogs(t *testing.T) {
	underlying := &stubConnection{databaseTypeValue: "postgresql"}
	recLogger := newRecordingLogger()
	conn := NewConnection(underlying, recLogger, &config.DatabaseConfig{}).(*Connection)

	ctx := logger.WithDBCounter(context.Background())
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

func TestConnectionExecErrorIsLogged(t *testing.T) {
	underlying := &stubConnection{databaseTypeValue: "postgresql", execErr: errors.New("boom")}
	recLogger := newRecordingLogger()
	conn := NewConnection(underlying, recLogger, &config.DatabaseConfig{}).(*Connection)

	ctx := logger.WithDBCounter(context.Background())
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
	if _, ok := tx.(*Transaction); !ok {
		t.Fatalf("expected tracked transaction, got %T", tx)
	}

	txWithOpts, err := conn.BeginTx(context.Background(), &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		t.Fatalf("expected begin tx success")
	}
	if _, ok := txWithOpts.(*Transaction); !ok {
		t.Fatalf("expected tracked transaction, got %T", txWithOpts)
	}
}

func TestConnectionBeginPropagatesError(t *testing.T) {
	underlying := &stubConnection{databaseTypeValue: "postgresql", beginErr: errors.New("begin fail")}
	recLogger := newRecordingLogger()
	conn := NewConnection(underlying, recLogger, &config.DatabaseConfig{}).(*Connection)

	_, err := conn.Begin(context.Background())
	if err == nil {
		t.Fatalf("expected begin error")
	}
}

func TestConnectionCreateMigrationTableLogs(t *testing.T) {
	underlying := &stubConnection{databaseTypeValue: "postgresql", createMigrationErr: errors.New("migrate fail")}
	recLogger := newRecordingLogger()
	conn := NewConnection(underlying, recLogger, &config.DatabaseConfig{}).(*Connection)

	err := conn.CreateMigrationTable(logger.WithDBCounter(context.Background()))
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

	if err := conn.Health(context.Background()); err != nil {
		t.Fatalf("expected health to succeed")
	}
	gotStats, err := conn.Stats()
	if err != nil || gotStats["ok"] != true {
		t.Fatalf("unexpected stats result: %v %v", gotStats, err)
	}
	if err := conn.Close(); err != nil {
		t.Fatalf("expected close to succeed")
	}
	if !underlying.closeCalled {
		t.Fatalf("expected underlying close to be invoked")
	}
	if conn.DatabaseType() != "postgresql" {
		t.Fatalf("unexpected database type")
	}
	if conn.GetMigrationTable() != "schema" {
		t.Fatalf("unexpected migration table")
	}
}
