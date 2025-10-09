package tracking

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/gaborage/go-bricks/database/types"
	"github.com/gaborage/go-bricks/logger"
)

type stubTx struct {
	queryArgs      [][]any
	queryErr       error
	queryRowArgs   [][]any
	execArgs       [][]any
	execErr        error
	prepareErr     error
	preparedStmt   *stubStatement
	commitCalled   bool
	rollbackCalled bool
}

func (s *stubTx) Query(_ context.Context, _ string, args ...any) (*sql.Rows, error) {
	s.queryArgs = append(s.queryArgs, append([]any(nil), args...))
	return new(sql.Rows), s.queryErr
}

func (s *stubTx) QueryRow(_ context.Context, _ string, args ...any) types.Row {
	s.queryRowArgs = append(s.queryRowArgs, append([]any(nil), args...))
	return types.NewRowFromSQL(new(sql.Row))
}

func (s *stubTx) Exec(_ context.Context, _ string, args ...any) (sql.Result, error) {
	s.execArgs = append(s.execArgs, append([]any(nil), args...))
	return stubResult(1), s.execErr
}

func (s *stubTx) Prepare(_ context.Context, _ string) (types.Statement, error) {
	if s.prepareErr != nil {
		return nil, s.prepareErr
	}
	if s.preparedStmt == nil {
		s.preparedStmt = &stubStatement{}
	}
	return s.preparedStmt, nil
}

func (s *stubTx) Commit() error {
	s.commitCalled = true
	return nil
}

func (s *stubTx) Rollback() error {
	s.rollbackCalled = true
	return nil
}

func TestNewTransactionWrapsUnderlying(t *testing.T) {
	t.Parallel()
	underlying := &stubTx{}
	tx := NewTransaction(underlying, newRecordingLogger(), "postgresql", Settings{})

	wrapped, ok := tx.(*Transaction)
	if !ok {
		t.Fatalf("expected *Transaction, got %T", tx)
	}
	stored, ok := wrapped.tx.(*stubTx)
	if !ok || stored != underlying {
		t.Fatalf("expected underlying transaction to be stored")
	}
}

func TestTransactionQueryLogs(t *testing.T) {
	t.Parallel()
	ctx := logger.WithDBCounter(context.Background())
	underlying := &stubTx{}
	recLogger := newRecordingLogger()
	settings := Settings{slowQueryThreshold: time.Second}
	tx := NewTransaction(underlying, recLogger, "postgresql", settings)

	rows, err := tx.Query(ctx, "SELECT", 1)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if rows == nil {
		t.Fatalf("expected rows result")
	}
	if len(underlying.queryArgs) != 1 || underlying.queryArgs[0][0] != 1 {
		t.Fatalf("expected Query args to be forwarded")
	}
	events := recLogger.events()
	if len(events) != 1 || events[0].Level != "debug" {
		t.Fatalf("expected debug event, got %+v", events)
	}
	if events[0].Fields["query"] != "SELECT" {
		t.Fatalf("unexpected query field: %v", events[0].Fields["query"])
	}
}

func TestTransactionExecLogsError(t *testing.T) {
	t.Parallel()
	ctx := logger.WithDBCounter(context.Background())
	underlying := &stubTx{execErr: errors.New("fail")}
	recLogger := newRecordingLogger()
	tx := NewTransaction(underlying, recLogger, "postgresql", Settings{slowQueryThreshold: time.Second})

	_, err := tx.Exec(ctx, "UPDATE", 2)
	if err == nil {
		t.Fatalf("expected error from Exec")
	}
	events := recLogger.events()
	if len(events) != 1 || events[0].Level != levelError {
		t.Fatalf("expected error log, got %+v", events)
	}
}

func TestTransactionPrepareReturnsTrackedStatement(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	underlying := &stubTx{}
	recLogger := newRecordingLogger()
	settings := Settings{slowQueryThreshold: time.Second}
	tx := NewTransaction(underlying, recLogger, "postgresql", settings)

	stmt, err := tx.Prepare(ctx, "SELECT 1")
	if err != nil {
		t.Fatalf("expected prepare to succeed")
	}
	if _, ok := stmt.(*Statement); !ok {
		t.Fatalf("expected returned statement to be tracked, got %T", stmt)
	}
	events := recLogger.events()
	if len(events) != 1 || events[0].Fields["query"] != "TX_PREPARE: SELECT 1" {
		t.Fatalf("expected TX_PREPARE log, got %+v", events)
	}
}

func TestTransactionPreparePropagatesError(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	underlying := &stubTx{prepareErr: errors.New("prepare fail")}
	recLogger := newRecordingLogger()
	tx := NewTransaction(underlying, recLogger, "postgresql", Settings{slowQueryThreshold: time.Second})

	_, err := tx.Prepare(ctx, "SELECT 1")
	if err == nil {
		t.Fatalf("expected prepare error")
	}
	if len(recLogger.events()) != 1 {
		t.Fatalf("expected log event for prepare error")
	}
}

func TestTransactionCommitAndRollbackDelegate(t *testing.T) {
	t.Parallel()
	underlying := &stubTx{}
	tx := NewTransaction(underlying, newRecordingLogger(), "postgresql", Settings{})

	if err := tx.Commit(); err != nil {
		t.Fatalf("expected commit to succeed")
	}
	if err := tx.Rollback(); err != nil {
		t.Fatalf("expected rollback to succeed")
	}
	if !underlying.commitCalled || !underlying.rollbackCalled {
		t.Fatalf("expected both commit and rollback to be invoked")
	}
}
