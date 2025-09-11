package database

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"

	"github.com/gaborage/go-bricks/internal/database"
	"github.com/gaborage/go-bricks/logger"
)

// MockInterface implements database.Interface for testing
type MockInterface struct {
	mock.Mock
}

// MockStatement implements database.Statement for testing
type MockStatement struct {
	mock.Mock
}

// MockTransaction implements database.Tx for testing
type MockTransaction struct {
	mock.Mock
}

func (m *MockInterface) Query(ctx context.Context, query string, args ...interface{}) (*sql.Rows, error) {
	arguments := m.Called(ctx, query, args)
	return arguments.Get(0).(*sql.Rows), arguments.Error(1)
}

func (m *MockInterface) QueryRow(ctx context.Context, query string, args ...interface{}) *sql.Row {
	arguments := m.Called(ctx, query, args)
	return arguments.Get(0).(*sql.Row)
}

func (m *MockInterface) Exec(ctx context.Context, query string, args ...interface{}) (sql.Result, error) {
	arguments := m.Called(ctx, query, args)
	if arguments.Get(0) == nil {
		return nil, arguments.Error(1)
	}
	return arguments.Get(0).(sql.Result), arguments.Error(1)
}

func (m *MockInterface) Prepare(ctx context.Context, query string) (database.Statement, error) {
	arguments := m.Called(ctx, query)
	if arguments.Get(0) == nil {
		return nil, arguments.Error(1)
	}
	return arguments.Get(0).(database.Statement), arguments.Error(1)
}

func (m *MockInterface) Begin(ctx context.Context) (database.Tx, error) {
	arguments := m.Called(ctx)
	if arguments.Get(0) == nil {
		return nil, arguments.Error(1)
	}
	return arguments.Get(0).(database.Tx), arguments.Error(1)
}

func (m *MockInterface) BeginTx(ctx context.Context, opts *sql.TxOptions) (database.Tx, error) {
	arguments := m.Called(ctx, opts)
	if arguments.Get(0) == nil {
		return nil, arguments.Error(1)
	}
	return arguments.Get(0).(database.Tx), arguments.Error(1)
}

func (m *MockInterface) Health(ctx context.Context) error {
	arguments := m.Called(ctx)
	return arguments.Error(0)
}

func (m *MockInterface) Stats() (map[string]interface{}, error) {
	arguments := m.Called()
	return arguments.Get(0).(map[string]interface{}), arguments.Error(1)
}

func (m *MockInterface) Close() error {
	arguments := m.Called()
	return arguments.Error(0)
}

func (m *MockInterface) DatabaseType() string {
	arguments := m.Called()
	return arguments.String(0)
}

func (m *MockInterface) GetMigrationTable() string {
	arguments := m.Called()
	return arguments.String(0)
}

func (m *MockInterface) CreateMigrationTable(ctx context.Context) error {
	arguments := m.Called(ctx)
	return arguments.Error(0)
}

// MockResult implements sql.Result for testing
type MockResult struct {
	lastInsertID int64
	rowsAffected int64
}

func (m MockResult) LastInsertId() (int64, error) {
	return m.lastInsertID, nil
}

func (m MockResult) RowsAffected() (int64, error) {
	return m.rowsAffected, nil
}

// MockStatement methods
func (ms *MockStatement) Query(ctx context.Context, args ...interface{}) (*sql.Rows, error) {
	arguments := ms.Called(ctx, args)
	return arguments.Get(0).(*sql.Rows), arguments.Error(1)
}

func (ms *MockStatement) QueryRow(ctx context.Context, args ...interface{}) *sql.Row {
	arguments := ms.Called(ctx, args)
	return arguments.Get(0).(*sql.Row)
}

func (ms *MockStatement) Exec(ctx context.Context, args ...interface{}) (sql.Result, error) {
	arguments := ms.Called(ctx, args)
	if arguments.Get(0) == nil {
		return nil, arguments.Error(1)
	}
	return arguments.Get(0).(sql.Result), arguments.Error(1)
}

func (ms *MockStatement) Close() error {
	arguments := ms.Called()
	return arguments.Error(0)
}

// MockTransaction methods
func (mt *MockTransaction) Query(ctx context.Context, query string, args ...interface{}) (*sql.Rows, error) {
	arguments := mt.Called(ctx, query, args)
	return arguments.Get(0).(*sql.Rows), arguments.Error(1)
}

func (mt *MockTransaction) QueryRow(ctx context.Context, query string, args ...interface{}) *sql.Row {
	arguments := mt.Called(ctx, query, args)
	return arguments.Get(0).(*sql.Row)
}

func (mt *MockTransaction) Exec(ctx context.Context, query string, args ...interface{}) (sql.Result, error) {
	arguments := mt.Called(ctx, query, args)
	if arguments.Get(0) == nil {
		return nil, arguments.Error(1)
	}
	return arguments.Get(0).(sql.Result), arguments.Error(1)
}

func (mt *MockTransaction) Prepare(ctx context.Context, query string) (database.Statement, error) {
	arguments := mt.Called(ctx, query)
	if arguments.Get(0) == nil {
		return nil, arguments.Error(1)
	}
	return arguments.Get(0).(database.Statement), arguments.Error(1)
}

func (mt *MockTransaction) Commit() error {
	arguments := mt.Called()
	return arguments.Error(0)
}

func (mt *MockTransaction) Rollback() error {
	arguments := mt.Called()
	return arguments.Error(0)
}

func TestNewTrackedConnection(t *testing.T) {
	mockConn := &MockInterface{}
	mockConn.On("DatabaseType").Return("postgresql")

	log := logger.New("debug", true)

	tracked := NewTrackedConnection(mockConn, log)

	assert.NotNil(t, tracked)
	assert.IsType(t, &TrackedConnection{}, tracked)

	trackedConn := tracked.(*TrackedConnection)
	assert.Equal(t, mockConn, trackedConn.conn)
	assert.Equal(t, log, trackedConn.logger)
	assert.Equal(t, "postgresql", trackedConn.vendor)

	mockConn.AssertExpectations(t)
}

func TestTrackedConnection_Query(t *testing.T) {
	mockConn := &MockInterface{}
	mockConn.On("DatabaseType").Return("postgresql")

	// Mock the Query call
	expectedRows := &sql.Rows{}
	mockConn.On("Query", mock.Anything, "SELECT * FROM users", mock.Anything).Return(expectedRows, nil)

	log := logger.New("debug", true)
	ctx := logger.WithDBCounter(context.Background())

	tracked := NewTrackedConnection(mockConn, log)

	// Execute query
	rows, err := tracked.Query(ctx, "SELECT * FROM users")

	// Assertions
	assert.NoError(t, err)
	assert.Equal(t, expectedRows, rows)

	// Verify DB counter was incremented
	counter := logger.GetDBCounter(ctx)
	assert.Equal(t, int64(1), counter)

	// Verify elapsed time was recorded
	elapsed := logger.GetDBElapsed(ctx)
	assert.Greater(t, elapsed, int64(0))

	mockConn.AssertExpectations(t)
}

func TestTrackedConnection_QueryWithError(t *testing.T) {
	mockConn := &MockInterface{}
	mockConn.On("DatabaseType").Return("postgresql")

	expectedErr := errors.New("query error")
	mockConn.On("Query", mock.Anything, "SELECT * FROM invalid", mock.Anything).Return((*sql.Rows)(nil), expectedErr)

	log := logger.New("debug", true)
	ctx := logger.WithDBCounter(context.Background())

	tracked := NewTrackedConnection(mockConn, log)

	rows, err := tracked.Query(ctx, "SELECT * FROM invalid")

	assert.Error(t, err)
	assert.Equal(t, expectedErr, err)
	assert.Nil(t, rows)

	// Verify DB counter was still incremented even with error
	counter := logger.GetDBCounter(ctx)
	assert.Equal(t, int64(1), counter)

	mockConn.AssertExpectations(t)
}

func TestTrackedConnection_QueryRow(t *testing.T) {
	mockConn := &MockInterface{}
	mockConn.On("DatabaseType").Return("oracle")

	expectedRow := &sql.Row{}
	mockConn.On("QueryRow", mock.Anything, "SELECT name FROM users WHERE id = ?", mock.Anything).Return(expectedRow)

	log := logger.New("debug", true)
	ctx := logger.WithDBCounter(context.Background())

	tracked := NewTrackedConnection(mockConn, log)

	row := tracked.QueryRow(ctx, "SELECT name FROM users WHERE id = ?", 1)

	assert.Equal(t, expectedRow, row)

	// Verify DB counter was incremented
	counter := logger.GetDBCounter(ctx)
	assert.Equal(t, int64(1), counter)

	mockConn.AssertExpectations(t)
}

func TestTrackedConnection_Exec(t *testing.T) {
	mockConn := &MockInterface{}
	mockConn.On("DatabaseType").Return("postgresql")

	expectedResult := MockResult{lastInsertID: 1, rowsAffected: 1}
	mockConn.On("Exec", mock.Anything, "INSERT INTO users (name) VALUES (?)", mock.Anything).Return(expectedResult, nil)

	log := logger.New("debug", true)
	ctx := logger.WithDBCounter(context.Background())

	tracked := NewTrackedConnection(mockConn, log)

	result, err := tracked.Exec(ctx, "INSERT INTO users (name) VALUES (?)", "John")

	assert.NoError(t, err)
	assert.Equal(t, expectedResult, result)

	// Verify DB counter was incremented
	counter := logger.GetDBCounter(ctx)
	assert.Equal(t, int64(1), counter)

	mockConn.AssertExpectations(t)
}

func TestTrackedConnection_Prepare(t *testing.T) {
	mockConn := &MockInterface{}
	mockConn.On("DatabaseType").Return("postgresql")

	expectedStmt := &MockStatement{}
	mockConn.On("Prepare", mock.Anything, "SELECT * FROM users WHERE id = ?").Return(expectedStmt, nil)

	log := logger.New("debug", true)
	ctx := logger.WithDBCounter(context.Background())

	tracked := NewTrackedConnection(mockConn, log)

	stmt, err := tracked.Prepare(ctx, "SELECT * FROM users WHERE id = ?")

	assert.NoError(t, err)
	assert.NotNil(t, stmt)
	assert.IsType(t, &TrackedStatement{}, stmt)

	// Verify DB counter was incremented
	counter := logger.GetDBCounter(ctx)
	assert.Equal(t, int64(1), counter)

	mockConn.AssertExpectations(t)
}

func TestTrackedConnection_CreateMigrationTable(t *testing.T) {
	mockConn := &MockInterface{}
	mockConn.On("DatabaseType").Return("postgresql")
	mockConn.On("CreateMigrationTable", mock.Anything).Return(nil)

	log := logger.New("debug", true)
	ctx := logger.WithDBCounter(context.Background())

	tracked := NewTrackedConnection(mockConn, log)

	err := tracked.CreateMigrationTable(ctx)

	assert.NoError(t, err)

	// Verify DB counter was incremented
	counter := logger.GetDBCounter(ctx)
	assert.Equal(t, int64(1), counter)

	mockConn.AssertExpectations(t)
}

func TestTrackedConnection_MultipleOperations(t *testing.T) {
	mockConn := &MockInterface{}
	mockConn.On("DatabaseType").Return("postgresql")

	// Mock multiple operations
	expectedRows := &sql.Rows{}
	expectedResult := MockResult{rowsAffected: 2}
	expectedRow := &sql.Row{}

	mockConn.On("Query", mock.Anything, "SELECT * FROM users", mock.Anything).Return(expectedRows, nil)
	mockConn.On("Exec", mock.Anything, "UPDATE users SET active = true", mock.Anything).Return(expectedResult, nil)
	mockConn.On("QueryRow", mock.Anything, "SELECT COUNT(*) FROM users", mock.Anything).Return(expectedRow)

	log := logger.New("debug", true)
	ctx := logger.WithDBCounter(context.Background())

	tracked := NewTrackedConnection(mockConn, log)

	// Perform multiple operations
	rows1, err1 := tracked.Query(ctx, "SELECT * FROM users")
	_ = rows1 // Reference to avoid unused variable warning
	_, err2 := tracked.Exec(ctx, "UPDATE users SET active = true")
	_ = tracked.QueryRow(ctx, "SELECT COUNT(*) FROM users")

	assert.NoError(t, err1)
	assert.NoError(t, err2)

	// Verify DB counter was incremented for each operation
	counter := logger.GetDBCounter(ctx)
	assert.Equal(t, int64(3), counter)

	// Verify total elapsed time was recorded
	elapsed := logger.GetDBElapsed(ctx)
	assert.Greater(t, elapsed, int64(0))

	mockConn.AssertExpectations(t)
}

func TestTrackedConnection_NonTrackedMethods(t *testing.T) {
	mockConn := &MockInterface{}
	mockConn.On("DatabaseType").Return("oracle")
	mockConn.On("Health", mock.Anything).Return(nil)
	mockConn.On("Stats").Return(map[string]interface{}{"connections": 5}, nil)
	mockConn.On("Close").Return(nil)
	mockConn.On("GetMigrationTable").Return("flyway_schema_history")
	mockConn.On("Begin", mock.Anything).Return(&MockTransaction{}, nil)
	mockConn.On("BeginTx", mock.Anything, (*sql.TxOptions)(nil)).Return(&MockTransaction{}, nil)

	log := logger.New("debug", true)
	ctx := logger.WithDBCounter(context.Background())

	tracked := NewTrackedConnection(mockConn, log)

	// Test non-tracked methods
	err1 := tracked.Health(ctx)
	stats, err2 := tracked.Stats()
	err3 := tracked.Close()
	table := tracked.GetMigrationTable()
	dbType := tracked.DatabaseType()
	_, err4 := tracked.Begin(ctx)
	_, err5 := tracked.BeginTx(ctx, nil)

	assert.NoError(t, err1)
	assert.NoError(t, err2)
	assert.NoError(t, err3)
	assert.NoError(t, err4)
	assert.NoError(t, err5)
	assert.Equal(t, map[string]interface{}{"connections": 5}, stats)
	assert.Equal(t, "flyway_schema_history", table)
	assert.Equal(t, "oracle", dbType)

	// Verify DB counter was NOT incremented for non-tracked methods
	counter := logger.GetDBCounter(ctx)
	assert.Equal(t, int64(0), counter)

	mockConn.AssertExpectations(t)
}

func TestTrackedConnection_ContextWithoutCounter(t *testing.T) {
	mockConn := &MockInterface{}
	mockConn.On("DatabaseType").Return("postgresql")

	expectedRows := &sql.Rows{}
	mockConn.On("Query", mock.Anything, "SELECT 1", mock.Anything).Return(expectedRows, nil)

	log := logger.New("debug", true)
	ctx := context.Background() // Context without DB counter

	tracked := NewTrackedConnection(mockConn, log)

	// This should not panic even without DB counter in context
	rows, err := tracked.Query(ctx, "SELECT 1")

	assert.NoError(t, err)
	assert.Equal(t, expectedRows, rows)

	mockConn.AssertExpectations(t)
}

func TestTrackDBOperation(t *testing.T) {
	log := logger.New("debug", true)
	ctx := logger.WithDBCounter(context.Background())

	start := time.Now().Add(-10 * time.Millisecond) // Simulate 10ms operation

	// Test successful operation
	trackDBOperation(ctx, log, "postgresql", "SELECT * FROM test", start, nil)

	// Verify counter was incremented
	counter := logger.GetDBCounter(ctx)
	assert.Equal(t, int64(1), counter)

	// Verify elapsed time was recorded (should be >= 10ms in nanoseconds)
	elapsed := logger.GetDBElapsed(ctx)
	assert.Greater(t, elapsed, int64(10*time.Millisecond))
}

func TestTrackDBOperation_WithError(t *testing.T) {
	log := logger.New("debug", true)
	ctx := logger.WithDBCounter(context.Background())

	start := time.Now()
	testErr := errors.New("database error")

	// This should not panic
	trackDBOperation(ctx, log, "oracle", "SELECT * FROM invalid", start, testErr)

	// Verify counter was still incremented despite error
	counter := logger.GetDBCounter(ctx)
	assert.Equal(t, int64(1), counter)
}

func TestTrackedStatement_Operations(t *testing.T) {
	mockConn := &MockInterface{}
	mockConn.On("DatabaseType").Return("postgresql")

	mockStmt := &MockStatement{}
	mockConn.On("Prepare", mock.Anything, "SELECT * FROM users WHERE id = ?").Return(mockStmt, nil)

	// Mock statement operations
	expectedRows := &sql.Rows{}
	expectedRow := &sql.Row{}
	expectedResult := MockResult{lastInsertID: 1, rowsAffected: 1}

	mockStmt.On("Query", mock.Anything, mock.Anything).Return(expectedRows, nil)
	mockStmt.On("QueryRow", mock.Anything, mock.Anything).Return(expectedRow)
	mockStmt.On("Exec", mock.Anything, mock.Anything).Return(expectedResult, nil)

	log := logger.New("debug", true)
	ctx := logger.WithDBCounter(context.Background())

	tracked := NewTrackedConnection(mockConn, log)

	// Prepare statement (should increment counter)
	stmt, err := tracked.Prepare(ctx, "SELECT * FROM users WHERE id = ?")
	assert.NoError(t, err)
	assert.IsType(t, &TrackedStatement{}, stmt)

	// Execute operations through prepared statement (should increment counter for each)
	rows, err := stmt.Query(ctx, 1)
	assert.NoError(t, err)
	_ = rows // Reference to avoid unused variable

	_ = stmt.QueryRow(ctx, 2)

	_, err = stmt.Exec(ctx, 3)
	assert.NoError(t, err)

	// Verify DB counter was incremented for prepare + 3 operations = 4 total
	counter := logger.GetDBCounter(ctx)
	assert.Equal(t, int64(4), counter)

	mockConn.AssertExpectations(t)
	mockStmt.AssertExpectations(t)
}

func TestTrackedTransaction_Operations(t *testing.T) {
	mockConn := &MockInterface{}
	mockConn.On("DatabaseType").Return("postgresql")

	mockTx := &MockTransaction{}
	mockConn.On("Begin", mock.Anything).Return(mockTx, nil)

	// Mock transaction operations
	expectedRows := &sql.Rows{}
	expectedRow := &sql.Row{}
	expectedResult := MockResult{rowsAffected: 2}
	mockStmt := &MockStatement{}

	mockTx.On("Query", mock.Anything, "SELECT * FROM users", mock.Anything).Return(expectedRows, nil)
	mockTx.On("QueryRow", mock.Anything, "SELECT COUNT(*) FROM users", mock.Anything).Return(expectedRow)
	mockTx.On("Exec", mock.Anything, "INSERT INTO users (name) VALUES (?)", mock.Anything).Return(expectedResult, nil)
	mockTx.On("Prepare", mock.Anything, "UPDATE users SET name = ? WHERE id = ?").Return(mockStmt, nil)

	log := logger.New("debug", true)
	ctx := logger.WithDBCounter(context.Background())

	tracked := NewTrackedConnection(mockConn, log)

	// Begin transaction
	tx, err := tracked.Begin(ctx)
	assert.NoError(t, err)
	assert.IsType(t, &TrackedTransaction{}, tx)

	// Execute operations within transaction (each should increment counter)
	rows, err := tx.Query(ctx, "SELECT * FROM users")
	assert.NoError(t, err)
	_ = rows // Reference to avoid unused variable

	_ = tx.QueryRow(ctx, "SELECT COUNT(*) FROM users")

	_, err = tx.Exec(ctx, "INSERT INTO users (name) VALUES (?)", "John")
	assert.NoError(t, err)

	// Prepare statement within transaction (should increment counter)
	stmt, err := tx.Prepare(ctx, "UPDATE users SET name = ? WHERE id = ?")
	assert.NoError(t, err)
	assert.IsType(t, &TrackedStatement{}, stmt)

	// Verify DB counter was incremented for all operations = 4 total
	counter := logger.GetDBCounter(ctx)
	assert.Equal(t, int64(4), counter)

	mockConn.AssertExpectations(t)
	mockTx.AssertExpectations(t)
}
