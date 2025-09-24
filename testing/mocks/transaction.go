package mocks

import (
	"context"
	"database/sql"

	"github.com/stretchr/testify/mock"

	"github.com/gaborage/go-bricks/database/types"
)

// MockTx provides a testify-based mock implementation of the database Tx interface.
// It allows for testing transaction scenarios including commit, rollback, and error conditions.
//
// Example usage:
//
//	mockTx := &mocks.MockTx{}
//	mockTx.On("Exec", mock.Anything, "INSERT INTO users", mock.Anything).Return(result, nil)
//	mockTx.On("Commit").Return(nil)
//
//	// Use mockTx in your tests
//	err := tx.Commit()
type MockTx struct {
	mock.Mock
}

// Query implements types.Tx
func (m *MockTx) Query(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	callArgs := append([]any{ctx, query}, args...)
	arguments := m.Called(callArgs...)
	return arguments.Get(0).(*sql.Rows), arguments.Error(1)
}

// QueryRow implements types.Tx
func (m *MockTx) QueryRow(ctx context.Context, query string, args ...any) *sql.Row {
	callArgs := append([]any{ctx, query}, args...)
	arguments := m.Called(callArgs...)
	return arguments.Get(0).(*sql.Row)
}

// Exec implements types.Tx
func (m *MockTx) Exec(ctx context.Context, query string, args ...any) (sql.Result, error) {
	callArgs := append([]any{ctx, query}, args...)
	arguments := m.Called(callArgs...)
	if arguments.Get(0) == nil {
		return nil, arguments.Error(1)
	}
	return arguments.Get(0).(sql.Result), arguments.Error(1)
}

// Prepare implements types.Tx
func (m *MockTx) Prepare(ctx context.Context, query string) (types.Statement, error) {
	arguments := m.Called(ctx, query)
	return arguments.Get(0).(types.Statement), arguments.Error(1)
}

// Commit implements types.Tx
func (m *MockTx) Commit() error {
	arguments := m.Called()
	return arguments.Error(0)
}

// Rollback implements types.Tx
func (m *MockTx) Rollback() error {
	arguments := m.Called()
	return arguments.Error(0)
}

// Helper methods for common testing scenarios

// ExpectQuery sets up a query expectation with the provided rows and error
func (m *MockTx) ExpectQuery(query string, rows *sql.Rows, err error) *mock.Call {
	return m.On("Query", mock.Anything, query, mock.Anything).Return(rows, err)
}

// ExpectExec sets up an exec expectation with the provided result and error
func (m *MockTx) ExpectExec(query string, result sql.Result, err error) *mock.Call {
	return m.On("Exec", mock.Anything, query, mock.Anything).Return(result, err)
}

// ExpectPrepare sets up a prepare expectation with the provided statement and error
func (m *MockTx) ExpectPrepare(query string, stmt types.Statement, err error) *mock.Call {
	return m.On("Prepare", mock.Anything, query).Return(stmt, err)
}

// ExpectCommit sets up a commit expectation with the provided error
func (m *MockTx) ExpectCommit(err error) *mock.Call {
	return m.On("Commit").Return(err)
}

// ExpectRollback sets up a rollback expectation with the provided error
func (m *MockTx) ExpectRollback(err error) *mock.Call {
	return m.On("Rollback").Return(err)
}

// ExpectSuccessfulTransaction sets up expectations for a successful transaction
func (m *MockTx) ExpectSuccessfulTransaction() {
	m.On("Commit").Return(nil)
}

// ExpectFailedTransaction sets up expectations for a failed transaction that should be rolled back
func (m *MockTx) ExpectFailedTransaction(commitErr error) {
	m.On("Commit").Return(commitErr)
	m.On("Rollback").Return(nil)
}
