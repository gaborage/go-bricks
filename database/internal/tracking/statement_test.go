package tracking

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/gaborage/go-bricks/logger"
)

type stubStatement struct {
	queryArgs    [][]any
	queryErr     error
	queryRowArgs [][]any
	execArgs     [][]any
	execErr      error
	closeCalled  bool
	rowsResult   *sql.Rows
	execResult   sql.Result
}

func (s *stubStatement) Query(_ context.Context, args ...any) (*sql.Rows, error) {
	s.queryArgs = append(s.queryArgs, append([]any(nil), args...))
	if s.rowsResult == nil {
		s.rowsResult = new(sql.Rows)
	}
	return s.rowsResult, s.queryErr
}

func (s *stubStatement) QueryRow(_ context.Context, args ...any) *sql.Row {
	s.queryRowArgs = append(s.queryRowArgs, append([]any(nil), args...))
	return new(sql.Row)
}

func (s *stubStatement) Exec(_ context.Context, args ...any) (sql.Result, error) {
	s.execArgs = append(s.execArgs, append([]any(nil), args...))
	if s.execResult == nil {
		s.execResult = stubResult(1)
	}
	return s.execResult, s.execErr
}

func (s *stubStatement) Close() error {
	s.closeCalled = true
	return nil
}

type stubResult int64

func (s stubResult) LastInsertId() (int64, error) { return int64(s), nil }
func (s stubResult) RowsAffected() (int64, error) { return int64(s), nil }

func TestNewStatementWrapsUnderlying(t *testing.T) {
	underlying := &stubStatement{}
	settings := Settings{slowQueryThreshold: time.Second}
	recLogger := newRecordingLogger()

	statement := NewStatement(underlying, recLogger, "postgresql", "SELECT 1", settings)

	wrapped, ok := statement.(*Statement)
	if !ok {
		t.Fatalf("expected *Statement, got %T", statement)
	}
	if wrapped.stmt != underlying {
		t.Fatalf("expected underlying statement to be stored")
	}
}

func TestStatementQueryDelegatesAndLogs(t *testing.T) {
	ctx := logger.WithDBCounter(context.Background())
	underlying := &stubStatement{}
	settings := Settings{slowQueryThreshold: time.Second, logQueryParameters: true, maxQueryLength: 50}
	recLogger := newRecordingLogger()
	statement := NewStatement(underlying, recLogger, "postgresql", "SELECT 1", settings)

	rows, err := statement.Query(ctx, "param")
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if rows == nil {
		t.Fatalf("expected rows result")
	}
	if len(underlying.queryArgs) != 1 || len(underlying.queryArgs[0]) != 1 || underlying.queryArgs[0][0] != "param" {
		t.Fatalf("expected underlying Query to receive arguments")
	}

	events := recLogger.events()
	if len(events) != 1 {
		t.Fatalf("expected single log event, got %d", len(events))
	}
	event := events[0]
	if event.Level != levelDebug {
		t.Fatalf("expected debug level, got %s", event.Level)
	}
	if event.Fields["query"] != "STMT_QUERY: SELECT 1" {
		t.Fatalf("unexpected query field: %v", event.Fields["query"])
	}
	argsField, ok := event.Fields["args"].([]any)
	if !ok || len(argsField) != 1 || argsField[0] != "param" {
		t.Fatalf("expected args to be logged, got %v", event.Fields["args"])
	}
}

func TestStatementExecPropagatesErrors(t *testing.T) {
	ctx := logger.WithDBCounter(context.Background())
	underlying := &stubStatement{execErr: errors.New("boom")}
	settings := Settings{slowQueryThreshold: time.Second}
	recLogger := newRecordingLogger()
	statement := NewStatement(underlying, recLogger, "postgresql", "UPDATE", settings)

	_, err := statement.Exec(ctx, 1)
	if err == nil {
		t.Fatalf("expected error from Exec")
	}

	events := recLogger.events()
	if len(events) != 1 {
		t.Fatalf("expected single log event, got %d", len(events))
	}
	if events[0].Level != "error" {
		t.Fatalf("expected error level, got %s", events[0].Level)
	}
	if events[0].Err == nil {
		t.Fatalf("expected error to be recorded")
	}
}

func TestStatementQueryRowLogsWithoutError(t *testing.T) {
	ctx := logger.WithDBCounter(context.Background())
	underlying := &stubStatement{}
	settings := Settings{slowQueryThreshold: time.Second}
	recLogger := newRecordingLogger()
	statement := NewStatement(underlying, recLogger, "postgresql", "SELECT ROW", settings)

	row := statement.QueryRow(ctx, 42)
	if row == nil {
		t.Fatalf("expected row result")
	}
	if len(underlying.queryRowArgs) != 1 || underlying.queryRowArgs[0][0] != 42 {
		t.Fatalf("expected QueryRow arguments to be forwarded")
	}

	events := recLogger.events()
	if len(events) != 1 || events[0].Level != levelDebug {
		t.Fatalf("expected debug event, got %+v", events)
	}
	if events[0].Fields["query"] != "STMT_QUERY_ROW: SELECT ROW" {
		t.Fatalf("unexpected query label: %v", events[0].Fields["query"])
	}
}

func TestStatementCloseDelegates(t *testing.T) {
	underlying := &stubStatement{}
	statement := NewStatement(underlying, newRecordingLogger(), "postgresql", "SELECT", Settings{})

	if err := statement.Close(); err != nil {
		t.Fatalf("expected close to succeed")
	}
	if !underlying.closeCalled {
		t.Fatalf("expected underlying Close to be invoked")
	}
}
