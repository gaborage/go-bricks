package database

import (
	"context"
	"database/sql"
	"errors"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/logger"
)

// test helpers
func setupTracked(t testing.TB, withCounter bool) (sqlmock.Sqlmock, *TrackedConnection, context.Context) {
	t.Helper()
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, mock.ExpectationsWereMet()) })
	t.Cleanup(func() { _ = db.Close() })

	sc := &simpleConnection{db: db}
	log := logger.New("debug", true)
	tracked := NewTrackedConnection(sc, log).(*TrackedConnection)
	var ctx context.Context
	if withCounter {
		ctx = logger.WithDBCounter(context.Background())
	} else {
		ctx = context.Background()
	}
	return mock, tracked, ctx
}

// setupTrackedWithOptions is intentionally omitted to keep compatibility with
// older sqlmock versions; use test-local setup for special options.

func assertDBCounter(ctx context.Context, t testing.TB, want int64) {
	t.Helper()
	assert.Equal(t, want, logger.GetDBCounter(ctx))
}

func assertDBElapsedPositive(ctx context.Context, t testing.TB) {
	t.Helper()
	assert.Greater(t, logger.GetDBElapsed(ctx), int64(0))
}

func TestNewTrackedConnection(t *testing.T) {
	t.Parallel()
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, mock.ExpectationsWereMet()) })
	t.Cleanup(func() { _ = db.Close() })

	simpleConn := &simpleConnection{db: db}
	log := logger.New("debug", true)

	tracked := NewTrackedConnection(simpleConn, log)

	assert.NotNil(t, tracked)
	assert.IsType(t, &TrackedConnection{}, tracked)

	trackedConn := tracked.(*TrackedConnection)
	assert.Equal(t, simpleConn, trackedConn.conn)
	assert.Equal(t, log, trackedConn.logger)
	assert.Equal(t, "postgresql", trackedConn.vendor)
}

func TestTrackedConnection_Query(t *testing.T) {
	t.Parallel()
	mock, tracked, ctx := setupTracked(t, true)

	rows := sqlmock.NewRows([]string{"id", "name"}).AddRow(1, "John")
	mock.ExpectQuery(regexp.QuoteMeta("SELECT * FROM users")).WillReturnRows(rows)

	resultRows, err := tracked.Query(ctx, "SELECT * FROM users")
	require.NoError(t, err)
	defer resultRows.Close()

	assertDBCounter(ctx, t, 1)
	assertDBElapsedPositive(ctx, t)
}

func TestTrackedConnection_QueryWithError(t *testing.T) {
	t.Parallel()
	mock, tracked, ctx := setupTracked(t, true)

	expectedErr := errors.New("query error")
	mock.ExpectQuery(regexp.QuoteMeta("SELECT * FROM invalid")).WillReturnError(expectedErr)

	rows, err := tracked.Query(ctx, "SELECT * FROM invalid")
	assert.Error(t, err)
	assert.ErrorIs(t, err, expectedErr)
	assert.Nil(t, rows)

	assertDBCounter(ctx, t, 1)
}

func TestTrackedConnection_QueryRow(t *testing.T) {
	t.Parallel()
	mock, tracked, ctx := setupTracked(t, true)

	rows := sqlmock.NewRows([]string{"name"}).AddRow("John")
	mock.ExpectQuery(regexp.QuoteMeta("SELECT name FROM users WHERE id = $1")).WithArgs(1).WillReturnRows(rows)

	row := tracked.QueryRow(ctx, "SELECT name FROM users WHERE id = $1", 1)
	assert.NotNil(t, row)

	assertDBCounter(ctx, t, 1)
}

func TestTrackedConnection_Exec(t *testing.T) {
	t.Parallel()
	mock, tracked, ctx := setupTracked(t, true)

	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO users")).WithArgs("John").WillReturnResult(sqlmock.NewResult(1, 1))

	result, err := tracked.Exec(ctx, "INSERT INTO users (name) VALUES ($1)", "John")
	require.NoError(t, err)
	assert.NotNil(t, result)

	lastID, err := result.LastInsertId()
	require.NoError(t, err)
	assert.Equal(t, int64(1), lastID)

	rowsAffected, err := result.RowsAffected()
	require.NoError(t, err)
	assert.Equal(t, int64(1), rowsAffected)

	assertDBCounter(ctx, t, 1)
}

func TestTrackedConnection_Prepare(t *testing.T) {
	t.Parallel()
	mock, tracked, ctx := setupTracked(t, true)

	mock.ExpectPrepare(regexp.QuoteMeta("SELECT * FROM users WHERE id = $1"))

	stmt, err := tracked.Prepare(ctx, "SELECT * FROM users WHERE id = $1")
	require.NoError(t, err)
	assert.NotNil(t, stmt)
	assert.IsType(t, &TrackedStatement{}, stmt)
	t.Cleanup(func() { _ = stmt.Close() })

	assertDBCounter(ctx, t, 1)
}

func TestTrackedConnection_CreateMigrationTable(t *testing.T) {
	t.Parallel()
	mock, tracked, ctx := setupTracked(t, true)

	mock.ExpectExec(regexp.QuoteMeta("CREATE TABLE IF NOT EXISTS flyway_schema_history")).WillReturnResult(sqlmock.NewResult(0, 0))

	err := tracked.CreateMigrationTable(ctx)
	require.NoError(t, err)

	assertDBCounter(ctx, t, 1)
}

func TestTrackedConnection_MultipleOperations(t *testing.T) {
	t.Parallel()
	mock, tracked, ctx := setupTracked(t, true)

	rows := sqlmock.NewRows([]string{"id", "name"}).AddRow(1, "John")
	mock.ExpectQuery(regexp.QuoteMeta("SELECT * FROM users")).WillReturnRows(rows)
	mock.ExpectExec(regexp.QuoteMeta("UPDATE users SET active = true")).WillReturnResult(sqlmock.NewResult(0, 2))

	countRows := sqlmock.NewRows([]string{"count"}).AddRow(10)
	mock.ExpectQuery(regexp.QuoteMeta("SELECT COUNT(*) FROM users")).WillReturnRows(countRows)

	queryRows, err1 := tracked.Query(ctx, "SELECT * FROM users")
	require.NoError(t, err1)
	defer queryRows.Close()

	_, err2 := tracked.Exec(ctx, "UPDATE users SET active = true")
	require.NoError(t, err2)

	_ = tracked.QueryRow(ctx, "SELECT COUNT(*) FROM users")

	assertDBCounter(ctx, t, 3)
	assertDBElapsedPositive(ctx, t)
}

func TestTrackedConnection_NonTrackedMethods(t *testing.T) {
	t.Parallel()
	db, mock, err := sqlmock.New(sqlmock.MonitorPingsOption(true))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, mock.ExpectationsWereMet()) })
	t.Cleanup(func() { _ = db.Close() })

	sc := &simpleConnection{db: db}
	log := logger.New("debug", true)
	ctx := logger.WithDBCounter(context.Background())
	tracked := NewTrackedConnection(sc, log)

	// Set up expectations for non-tracked methods in order they're called
	mock.ExpectPing()  // Health()
	mock.ExpectBegin() // Begin()
	mock.ExpectBegin() // BeginTx()

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

	// Verify DB counter was NOT incremented for non-tracked methods
	assertDBCounter(ctx, t, 0)
}

func TestTrackedConnection_ContextWithoutCounter(t *testing.T) {
	t.Parallel()
	mock, tracked, _ := setupTracked(t, false)

	rows := sqlmock.NewRows([]string{"result"}).AddRow(1)
	mock.ExpectQuery(regexp.QuoteMeta("SELECT 1")).WillReturnRows(rows)

	// This should not panic even without DB counter in context
	resultRows, err := tracked.Query(context.Background(), "SELECT 1")
	require.NoError(t, err)
	resultRows.Close()
}

func TestTrackDBOperation(t *testing.T) {
	t.Parallel()
	log := logger.New("debug", true)
	ctx := logger.WithDBCounter(context.Background())

	start := time.Now().Add(-10 * time.Millisecond) // Simulate 10ms operation

	// Test successful operation
	trackDBOperation(ctx, log, "postgresql", "SELECT * FROM test", start, nil)

	assertDBCounter(ctx, t, 1)
	// elapsed should be >= 10ms in nanoseconds
	assert.GreaterOrEqual(t, logger.GetDBElapsed(ctx), int64(10*time.Millisecond))
}

func TestTrackDBOperation_WithError(t *testing.T) {
	t.Parallel()
	log := logger.New("debug", true)
	ctx := logger.WithDBCounter(context.Background())

	start := time.Now()
	testErr := errors.New("database error")

	// This should not panic
	trackDBOperation(ctx, log, "oracle", "SELECT * FROM invalid", start, testErr)

	// Verify counter was still incremented despite error
	assertDBCounter(ctx, t, 1)
}

func TestTrackedStatement_Operations(t *testing.T) {
	t.Parallel()
	mock, tracked, ctx := setupTracked(t, true)

	// Prepare statement
	mock.ExpectPrepare(regexp.QuoteMeta("SELECT * FROM users WHERE id = $1"))

	// Mock statement operations
	rows := sqlmock.NewRows([]string{"id", "name"}).AddRow(1, "John")
	mock.ExpectQuery(regexp.QuoteMeta("SELECT * FROM users WHERE id = $1")).WithArgs(1).WillReturnRows(rows)

	// Prepare statement (should increment counter)
	stmt, err := tracked.Prepare(ctx, "SELECT * FROM users WHERE id = $1")
	require.NoError(t, err)
	assert.IsType(t, &TrackedStatement{}, stmt)
	t.Cleanup(func() { _ = stmt.Close() })

	// Execute operations through prepared statement (should increment counter for each)
	stmtRows, err := stmt.Query(ctx, 1)
	require.NoError(t, err)
	defer stmtRows.Close()

	// Verify DB counter was incremented for prepare + 1 operation = 2 total
	assertDBCounter(ctx, t, 2)
}

func TestTrackedTransaction_Operations(t *testing.T) {
	t.Parallel()
	mock, tracked, ctx := setupTracked(t, true)

	// Begin transaction
	mock.ExpectBegin()

	// Mock transaction operations
	rows := sqlmock.NewRows([]string{"id", "name"}).AddRow(1, "John")
	mock.ExpectQuery(regexp.QuoteMeta("SELECT * FROM users")).WillReturnRows(rows)

	countRows := sqlmock.NewRows([]string{"count"}).AddRow(5)
	mock.ExpectQuery(regexp.QuoteMeta("SELECT COUNT(*) FROM users")).WillReturnRows(countRows)

	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO users")).WithArgs("John").WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectPrepare(regexp.QuoteMeta("UPDATE users SET name = $1 WHERE id = $2"))

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
	t.Cleanup(func() { _ = stmt.Close() })

	// Verify DB counter was incremented for all operations = 4 total
	assertDBCounter(ctx, t, 4)
}

func TestTrackedTransaction_CommitRollback(t *testing.T) {
	t.Parallel()
	mock, tracked, _ := setupTracked(t, false)

	// Test successful commit
	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO users")).WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()

	tx, err := tracked.Begin(context.Background())
	require.NoError(t, err)

	_, err = tx.Exec(context.Background(), "INSERT INTO users (name) VALUES ('test')")
	require.NoError(t, err)

	err = tx.Commit()
	require.NoError(t, err)

	// Test rollback
	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO users")).WillReturnError(errors.New("constraint violation"))
	mock.ExpectRollback()

	tx2, err := tracked.Begin(context.Background())
	require.NoError(t, err)

	_, err = tx2.Exec(context.Background(), "INSERT INTO users (name) VALUES ('test')")
	require.Error(t, err)

	err = tx2.Rollback()
	require.NoError(t, err)
}

// Database tracking hardening tests

func TestTrackDBOperation_NilLogger(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	start := time.Now().Add(-10 * time.Millisecond)

	assert.NotPanics(t, func() {
		trackDBOperation(ctx, nil, "postgresql", "SELECT 1", start, nil)
	})
}

func TestTrackDBOperation_SqlErrNoRows(t *testing.T) {
	t.Parallel()
	log := logger.New("debug", true)
	ctx := context.Background()
	start := time.Now().Add(-10 * time.Millisecond)

	assert.NotPanics(t, func() {
		trackDBOperation(ctx, log, "postgresql", "SELECT * FROM users WHERE id = 999", start, sql.ErrNoRows)
	})
}

// Removed QueryTruncation test: behavior not observable without capturing logs

func TestTrackDBOperation_SlowQueryThreshold(t *testing.T) {
	t.Parallel()
	log := logger.New("debug", true)
	ctx := context.Background()

	slowStart := time.Now().Add(-SlowQueryThreshold - 10*time.Millisecond)

	assert.NotPanics(t, func() {
		trackDBOperation(ctx, log, "postgresql", "SELECT * FROM huge_table", slowStart, nil)
	})
}

func TestTrackDBOperation_RegularError(t *testing.T) {
	t.Parallel()
	log := logger.New("debug", true)
	ctx := context.Background()
	start := time.Now().Add(-10 * time.Millisecond)

	regularErr := errors.New("connection timeout")

	assert.NotPanics(t, func() {
		trackDBOperation(ctx, log, "oracle", "SELECT * FROM users", start, regularErr)
	})
}

// Removed constant and constant-usage tests: prefer behavior over implementation details
