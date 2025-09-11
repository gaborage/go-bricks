package database

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/logger"
)

func TestNewTrackedConnection(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()

	simpleConn := &simpleConnection{db: db}
	log := logger.New("debug", true)

	tracked := NewTrackedConnection(simpleConn, log)

	assert.NotNil(t, tracked)
	assert.IsType(t, &TrackedConnection{}, tracked)

	// Verify the wrapper has the correct properties
	trackedConn := tracked.(*TrackedConnection)
	assert.Equal(t, simpleConn, trackedConn.conn)
	assert.Equal(t, log, trackedConn.logger)
	assert.Equal(t, "postgresql", trackedConn.vendor)

	require.NoError(t, mock.ExpectationsWereMet())
}

func TestTrackedConnection_Query(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()

	// Set up mock expectations
	rows := sqlmock.NewRows([]string{"id", "name"}).AddRow(1, "John")
	mock.ExpectQuery("SELECT \\* FROM users").WillReturnRows(rows)

	simpleConn := &simpleConnection{db: db}
	log := logger.New("debug", true)
	ctx := logger.WithDBCounter(context.Background())

	tracked := NewTrackedConnection(simpleConn, log)

	// Execute query
	resultRows, err := tracked.Query(ctx, "SELECT * FROM users")
	require.NoError(t, err)
	defer resultRows.Close()

	// Verify DB counter was incremented
	counter := logger.GetDBCounter(ctx)
	assert.Equal(t, int64(1), counter)

	// Verify elapsed time was recorded
	elapsed := logger.GetDBElapsed(ctx)
	assert.Greater(t, elapsed, int64(0))

	require.NoError(t, mock.ExpectationsWereMet())
}

func TestTrackedConnection_QueryWithError(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()

	expectedErr := errors.New("query error")
	mock.ExpectQuery("SELECT \\* FROM invalid").WillReturnError(expectedErr)

	simpleConn := &simpleConnection{db: db}
	log := logger.New("debug", true)
	ctx := logger.WithDBCounter(context.Background())

	tracked := NewTrackedConnection(simpleConn, log)

	rows, err := tracked.Query(ctx, "SELECT * FROM invalid")

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "query error")
	assert.Nil(t, rows)

	// Verify DB counter was still incremented even with error
	counter := logger.GetDBCounter(ctx)
	assert.Equal(t, int64(1), counter)

	require.NoError(t, mock.ExpectationsWereMet())
}

func TestTrackedConnection_QueryRow(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()

	rows := sqlmock.NewRows([]string{"name"}).AddRow("John")
	mock.ExpectQuery("SELECT name FROM users WHERE id = \\$1").WithArgs(1).WillReturnRows(rows)

	simpleConn := &simpleConnection{db: db}
	log := logger.New("debug", true)
	ctx := logger.WithDBCounter(context.Background())

	tracked := NewTrackedConnection(simpleConn, log)

	row := tracked.QueryRow(ctx, "SELECT name FROM users WHERE id = $1", 1)
	assert.NotNil(t, row)

	// Verify DB counter was incremented
	counter := logger.GetDBCounter(ctx)
	assert.Equal(t, int64(1), counter)

	require.NoError(t, mock.ExpectationsWereMet())
}

func TestTrackedConnection_Exec(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()

	mock.ExpectExec("INSERT INTO users").WithArgs("John").WillReturnResult(sqlmock.NewResult(1, 1))

	simpleConn := &simpleConnection{db: db}
	log := logger.New("debug", true)
	ctx := logger.WithDBCounter(context.Background())

	tracked := NewTrackedConnection(simpleConn, log)

	result, err := tracked.Exec(ctx, "INSERT INTO users (name) VALUES ($1)", "John")

	require.NoError(t, err)
	assert.NotNil(t, result)

	lastID, err := result.LastInsertId()
	require.NoError(t, err)
	assert.Equal(t, int64(1), lastID)

	rowsAffected, err := result.RowsAffected()
	require.NoError(t, err)
	assert.Equal(t, int64(1), rowsAffected)

	// Verify DB counter was incremented
	counter := logger.GetDBCounter(ctx)
	assert.Equal(t, int64(1), counter)

	require.NoError(t, mock.ExpectationsWereMet())
}

func TestTrackedConnection_Prepare(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()

	mock.ExpectPrepare("SELECT \\* FROM users WHERE id = \\$1")

	simpleConn := &simpleConnection{db: db}
	log := logger.New("debug", true)
	ctx := logger.WithDBCounter(context.Background())

	tracked := NewTrackedConnection(simpleConn, log)

	stmt, err := tracked.Prepare(ctx, "SELECT * FROM users WHERE id = $1")

	require.NoError(t, err)
	assert.NotNil(t, stmt)
	assert.IsType(t, &TrackedStatement{}, stmt)

	// Verify DB counter was incremented
	counter := logger.GetDBCounter(ctx)
	assert.Equal(t, int64(1), counter)

	require.NoError(t, mock.ExpectationsWereMet())
}

func TestTrackedConnection_CreateMigrationTable(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()

	mock.ExpectExec("CREATE TABLE IF NOT EXISTS flyway_schema_history").WillReturnResult(sqlmock.NewResult(0, 0))

	simpleConn := &simpleConnection{db: db}
	log := logger.New("debug", true)
	ctx := logger.WithDBCounter(context.Background())

	tracked := NewTrackedConnection(simpleConn, log)

	err = tracked.CreateMigrationTable(ctx)
	require.NoError(t, err)

	// Verify DB counter was incremented
	counter := logger.GetDBCounter(ctx)
	assert.Equal(t, int64(1), counter)

	require.NoError(t, mock.ExpectationsWereMet())
}

func TestTrackedConnection_MultipleOperations(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()

	// Mock multiple operations
	rows := sqlmock.NewRows([]string{"id", "name"}).AddRow(1, "John")
	mock.ExpectQuery("SELECT \\* FROM users").WillReturnRows(rows)
	mock.ExpectExec("UPDATE users SET active = true").WillReturnResult(sqlmock.NewResult(0, 2))

	countRows := sqlmock.NewRows([]string{"count"}).AddRow(10)
	mock.ExpectQuery("SELECT COUNT\\(\\*\\) FROM users").WillReturnRows(countRows)

	simpleConn := &simpleConnection{db: db}
	log := logger.New("debug", true)
	ctx := logger.WithDBCounter(context.Background())

	tracked := NewTrackedConnection(simpleConn, log)

	// Perform multiple operations
	queryRows, err1 := tracked.Query(ctx, "SELECT * FROM users")
	require.NoError(t, err1)
	defer queryRows.Close()

	_, err2 := tracked.Exec(ctx, "UPDATE users SET active = true")
	require.NoError(t, err2)

	_ = tracked.QueryRow(ctx, "SELECT COUNT(*) FROM users")

	// Verify DB counter was incremented for each operation
	counter := logger.GetDBCounter(ctx)
	assert.Equal(t, int64(3), counter)

	// Verify total elapsed time was recorded
	elapsed := logger.GetDBElapsed(ctx)
	assert.Greater(t, elapsed, int64(0))

	require.NoError(t, mock.ExpectationsWereMet())
}

func TestTrackedConnection_NonTrackedMethods(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.MonitorPingsOption(true))
	require.NoError(t, err)
	defer db.Close()

	// Set up expectations for non-tracked methods in order they're called
	mock.ExpectPing()  // Health()
	mock.ExpectBegin() // Begin()
	mock.ExpectBegin() // BeginTx()

	simpleConn := &simpleConnection{db: db}
	log := logger.New("debug", true)
	ctx := logger.WithDBCounter(context.Background())

	tracked := NewTrackedConnection(simpleConn, log)

	// Test non-tracked methods in expected order
	err1 := tracked.Health(ctx)
	require.NoError(t, err1)

	// These methods don't require mock expectations
	stats, err2 := tracked.Stats()
	require.NoError(t, err2)
	assert.NotNil(t, stats)

	table := tracked.GetMigrationTable()
	assert.Equal(t, "flyway_schema_history", table)

	dbType := tracked.DatabaseType()
	assert.Equal(t, "postgresql", dbType)

	_, err4 := tracked.Begin(ctx)
	require.NoError(t, err4)

	_, err5 := tracked.BeginTx(ctx, nil)
	require.NoError(t, err5)

	// Don't call Close() here since defer db.Close() will handle it

	// Verify DB counter was NOT incremented for non-tracked methods
	counter := logger.GetDBCounter(ctx)
	assert.Equal(t, int64(0), counter)

	require.NoError(t, mock.ExpectationsWereMet())
}

func TestTrackedConnection_ContextWithoutCounter(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()

	rows := sqlmock.NewRows([]string{"result"}).AddRow(1)
	mock.ExpectQuery("SELECT 1").WillReturnRows(rows)

	simpleConn := &simpleConnection{db: db}
	log := logger.New("debug", true)
	ctx := context.Background() // Context without DB counter

	tracked := NewTrackedConnection(simpleConn, log)

	// This should not panic even without DB counter in context
	resultRows, err := tracked.Query(ctx, "SELECT 1")
	require.NoError(t, err)
	defer resultRows.Close()

	require.NoError(t, mock.ExpectationsWereMet())
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
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()

	// Prepare statement
	mock.ExpectPrepare("SELECT \\* FROM users WHERE id = \\$1")

	// Mock statement operations
	rows := sqlmock.NewRows([]string{"id", "name"}).AddRow(1, "John")
	mock.ExpectQuery("SELECT \\* FROM users WHERE id = \\$1").WithArgs(1).WillReturnRows(rows)

	simpleConn := &simpleConnection{db: db}
	log := logger.New("debug", true)
	ctx := logger.WithDBCounter(context.Background())

	tracked := NewTrackedConnection(simpleConn, log)

	// Prepare statement (should increment counter)
	stmt, err := tracked.Prepare(ctx, "SELECT * FROM users WHERE id = $1")
	require.NoError(t, err)
	assert.IsType(t, &TrackedStatement{}, stmt)

	// Execute operations through prepared statement (should increment counter for each)
	stmtRows, err := stmt.Query(ctx, 1)
	require.NoError(t, err)
	defer stmtRows.Close()

	// Verify DB counter was incremented for prepare + 1 operation = 2 total
	counter := logger.GetDBCounter(ctx)
	assert.Equal(t, int64(2), counter)

	require.NoError(t, mock.ExpectationsWereMet())
}

func TestTrackedTransaction_Operations(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()

	// Begin transaction
	mock.ExpectBegin()

	// Mock transaction operations
	rows := sqlmock.NewRows([]string{"id", "name"}).AddRow(1, "John")
	mock.ExpectQuery("SELECT \\* FROM users").WillReturnRows(rows)

	countRows := sqlmock.NewRows([]string{"count"}).AddRow(5)
	mock.ExpectQuery("SELECT COUNT\\(\\*\\) FROM users").WillReturnRows(countRows)

	mock.ExpectExec("INSERT INTO users").WithArgs("John").WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectPrepare("UPDATE users SET name = \\$1 WHERE id = \\$2")

	simpleConn := &simpleConnection{db: db}
	log := logger.New("debug", true)
	ctx := logger.WithDBCounter(context.Background())

	tracked := NewTrackedConnection(simpleConn, log)

	// Begin transaction
	tx, err := tracked.Begin(ctx)
	require.NoError(t, err)
	assert.IsType(t, &TrackedTransaction{}, tx)

	// Execute operations within transaction (each should increment counter)
	txRows, err := tx.Query(ctx, "SELECT * FROM users")
	require.NoError(t, err)
	defer txRows.Close()

	_ = tx.QueryRow(ctx, "SELECT COUNT(*) FROM users")

	_, err = tx.Exec(ctx, "INSERT INTO users (name) VALUES ($1)", "John")
	require.NoError(t, err)

	// Prepare statement within transaction (should increment counter)
	stmt, err := tx.Prepare(ctx, "UPDATE users SET name = $1 WHERE id = $2")
	require.NoError(t, err)
	assert.IsType(t, &TrackedStatement{}, stmt)

	// Verify DB counter was incremented for all operations = 4 total
	counter := logger.GetDBCounter(ctx)
	assert.Equal(t, int64(4), counter)

	require.NoError(t, mock.ExpectationsWereMet())
}

func TestTrackedTransaction_CommitRollback(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()

	// Test successful commit
	mock.ExpectBegin()
	mock.ExpectExec("INSERT INTO users").WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()

	simpleConn := &simpleConnection{db: db}
	tracked := NewTrackedConnection(simpleConn, logger.New("debug", true))

	tx, err := tracked.Begin(context.Background())
	require.NoError(t, err)

	_, err = tx.Exec(context.Background(), "INSERT INTO users (name) VALUES ('test')")
	require.NoError(t, err)

	err = tx.Commit()
	require.NoError(t, err)

	// Test rollback
	mock.ExpectBegin()
	mock.ExpectExec("INSERT INTO users").WillReturnError(errors.New("constraint violation"))
	mock.ExpectRollback()

	tx2, err := tracked.Begin(context.Background())
	require.NoError(t, err)

	_, err = tx2.Exec(context.Background(), "INSERT INTO users (name) VALUES ('test')")
	require.Error(t, err)

	err = tx2.Rollback()
	require.NoError(t, err)

	require.NoError(t, mock.ExpectationsWereMet())
}
