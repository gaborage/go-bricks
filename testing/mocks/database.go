package mocks

import (
	"context"
	"database/sql"

	"github.com/stretchr/testify/mock"

	"github.com/gaborage/go-bricks/database/types"
)

// MockDatabase provides a testify-based mock implementation of the database.Interface.
// It allows for sophisticated testing scenarios with expectation setting and behavior verification.
//
// Example usage:
//
//	mockDB := &mocks.MockDatabase{}
//	mockDB.On("Query", mock.Anything, "SELECT * FROM users", mock.Anything).Return(rows, nil)
//	mockDB.On("Health", mock.Anything).Return(nil)
//
//	// Use mockDB in your tests
//	result, err := service.GetUsers(ctx, mockDB)
type MockDatabase struct {
	mock.Mock
}

// Query implements types.Interface
func (m *MockDatabase) Query(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	callArgs := append([]any{ctx, query}, args...)
	arguments := m.Called(callArgs...)
	return arguments.Get(0).(*sql.Rows), arguments.Error(1)
}

// QueryRow implements types.Interface
func (m *MockDatabase) QueryRow(ctx context.Context, query string, args ...any) *sql.Row {
	callArgs := append([]any{ctx, query}, args...)
	arguments := m.Called(callArgs...)
	return arguments.Get(0).(*sql.Row)
}

// Exec implements types.Interface
func (m *MockDatabase) Exec(ctx context.Context, query string, args ...any) (sql.Result, error) {
	callArgs := append([]any{ctx, query}, args...)
	arguments := m.Called(callArgs...)
	if arguments.Get(0) == nil {
		return nil, arguments.Error(1)
	}
	return arguments.Get(0).(sql.Result), arguments.Error(1)
}

// Prepare implements types.Interface
func (m *MockDatabase) Prepare(ctx context.Context, query string) (types.Statement, error) {
	arguments := m.Called(ctx, query)
	if arguments.Get(0) == nil {
		return nil, arguments.Error(1)
	}
	return arguments.Get(0).(types.Statement), arguments.Error(1)
}

// Begin implements types.Interface
func (m *MockDatabase) Begin(ctx context.Context) (types.Tx, error) {
	arguments := m.Called(ctx)
	if arguments.Get(0) == nil {
		return nil, arguments.Error(1)
	}
	return arguments.Get(0).(types.Tx), arguments.Error(1)
}

// BeginTx implements types.Interface
func (m *MockDatabase) BeginTx(ctx context.Context, opts *sql.TxOptions) (types.Tx, error) {
	arguments := m.Called(ctx, opts)
	if arguments.Get(0) == nil {
		return nil, arguments.Error(1)
	}
	return arguments.Get(0).(types.Tx), arguments.Error(1)
}

// noopWithError is a helper to avoid code duplication in Health
func (m *MockDatabase) noopWithError(ctx context.Context) error {
	arguments := m.Called(ctx)
	return arguments.Error(0)
}

// Health implements types.Interface
func (m *MockDatabase) Health(ctx context.Context) error {
	return m.noopWithError(ctx)
}

// Stats implements types.Interface
func (m *MockDatabase) Stats() (map[string]any, error) {
	arguments := m.Called()
	if arguments.Get(0) == nil {
		return nil, arguments.Error(1)
	}
	return arguments.Get(0).(map[string]any), arguments.Error(1)
}

// Close implements types.Interface
func (m *MockDatabase) Close() error {
	arguments := m.Called()
	return arguments.Error(0)
}

// noop is a helper to avoid code duplication in DatabaseType and GetMigrationTable
func (m *MockDatabase) noop() string {
	arguments := m.Called()
	return arguments.String(0)
}

// DatabaseType implements types.Interface
func (m *MockDatabase) DatabaseType() string {
	return m.noop()
}

// GetMigrationTable implements types.Interface
func (m *MockDatabase) GetMigrationTable() string {
	return m.noop()
}

// CreateMigrationTable implements types.Interface
func (m *MockDatabase) CreateMigrationTable(ctx context.Context) error {
	return m.noopWithError(ctx)
}

// Helper methods for common testing scenarios

// ExpectHealthCheck sets up a health check expectation
func (m *MockDatabase) ExpectHealthCheck(healthy bool) *mock.Call {
	if healthy {
		return m.On("noopWithError", mock.Anything).Return(nil)
	}
	return m.On("noopWithError", mock.Anything).Return(sql.ErrConnDone)
}

// ExpectQuery sets up a query expectation with the provided rows and error
func (m *MockDatabase) ExpectQuery(query string, rows *sql.Rows, err error) *mock.Call {
	return m.On("Query", mock.Anything, query, mock.Anything).Return(rows, err)
}

// ExpectExec sets up an exec expectation with the provided result and error
func (m *MockDatabase) ExpectExec(query string, result sql.Result, err error) *mock.Call {
	return m.On("Exec", mock.Anything, query, mock.Anything).Return(result, err)
}

// ExpectTransaction sets up a transaction expectation with the provided mock transaction
func (m *MockDatabase) ExpectTransaction(tx types.Tx, err error) *mock.Call {
	return m.On("Begin", mock.Anything).Return(tx, err)
}

// ExpectDatabaseType sets up a database type expectation
func (m *MockDatabase) ExpectDatabaseType(dbType string) *mock.Call {
	return m.On("noop").Return(dbType)
}

// ExpectStats sets up a stats expectation with the provided stats and error
func (m *MockDatabase) ExpectStats(stats map[string]any, err error) *mock.Call {
	return m.On("Stats").Return(stats, err)
}
