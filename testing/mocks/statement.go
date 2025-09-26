package mocks

import (
	"context"
	"database/sql"

	"github.com/stretchr/testify/mock"

	"github.com/gaborage/go-bricks/database/types"
)

// MockStatement provides a testify-based mock implementation of the database Statement interface.
// It allows for testing prepared statement scenarios and error conditions.
//
// Example usage:
//
//	mockStmt := &mocks.MockStatement{}
//	mockStmt.On("Query", mock.Anything, mock.Anything).Return(rows, nil)
//	mockStmt.On("Close").Return(nil)
//
//	// Use mockStmt in your tests
//	rows, err := stmt.Query(ctx, args...)
type MockStatement struct {
	mock.Mock
}

// Query implements types.Statement
func (m *MockStatement) Query(ctx context.Context, args ...any) (*sql.Rows, error) {
	arguments := m.Called(ctx, args)
	return arguments.Get(0).(*sql.Rows), arguments.Error(1)
}

// QueryRow implements types.Statement
func (m *MockStatement) QueryRow(ctx context.Context, args ...any) types.Row {
	arguments := m.Called(ctx, args)
	return arguments.Get(0).(types.Row)
}

// Exec implements types.Statement
func (m *MockStatement) Exec(ctx context.Context, args ...any) (sql.Result, error) {
	arguments := m.Called(ctx, args)
	return arguments.Get(0).(sql.Result), arguments.Error(1)
}

// Close implements types.Statement
func (m *MockStatement) Close() error {
	arguments := m.Called()
	return arguments.Error(0)
}

// Helper methods for common testing scenarios

// ExpectQuery sets up a query expectation with the provided rows and error
func (m *MockStatement) ExpectQuery(rows *sql.Rows, err error) *mock.Call {
	return m.On("Query", mock.Anything, mock.Anything).Return(rows, err)
}

// ExpectQueryRow sets up a query row expectation with the provided row
func (m *MockStatement) ExpectQueryRow(row types.Row) *mock.Call {
	return m.On("QueryRow", mock.Anything, mock.Anything).Return(row)
}

// ExpectExec sets up an exec expectation with the provided result and error
func (m *MockStatement) ExpectExec(result sql.Result, err error) *mock.Call {
	return m.On("Exec", mock.Anything, mock.Anything).Return(result, err)
}

// ExpectClose sets up a close expectation with the provided error
func (m *MockStatement) ExpectClose(err error) *mock.Call {
	return m.On("Close").Return(err)
}
