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

func TestTrackedConnectionQuery(t *testing.T) {
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

func TestTrackedConnectionQueryWithError(t *testing.T) {
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

func TestTrackedConnectionQueryRow(t *testing.T) {
	t.Parallel()
	mock, tracked, ctx := setupTracked(t, true)

	rows := sqlmock.NewRows([]string{"name"}).AddRow("John")
	mock.ExpectQuery(regexp.QuoteMeta("SELECT name FROM users WHERE id = $1")).WithArgs(1).WillReturnRows(rows)

	row := tracked.QueryRow(ctx, "SELECT name FROM users WHERE id = $1", 1)
	assert.NotNil(t, row)

	assertDBCounter(ctx, t, 1)
}

func TestTrackedConnectionExec(t *testing.T) {
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

func TestTrackedConnectionPrepare(t *testing.T) {
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

func TestTrackedConnectionCreateMigrationTable(t *testing.T) {
	t.Parallel()
	mock, tracked, ctx := setupTracked(t, true)

	mock.ExpectExec(regexp.QuoteMeta("CREATE TABLE IF NOT EXISTS flyway_schema_history")).WillReturnResult(sqlmock.NewResult(0, 0))

	err := tracked.CreateMigrationTable(ctx)
	require.NoError(t, err)

	assertDBCounter(ctx, t, 1)
}

func TestTrackedConnectionMultipleOperations(t *testing.T) {
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

func TestTrackedTxContextMethods(t *testing.T) {
	t.Parallel()
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, mock.ExpectationsWereMet()) })
	t.Cleanup(func() { _ = db.Close() })

	mock.ExpectBegin()
	nativeTx, err := db.BeginTx(context.Background(), nil)
	require.NoError(t, err)

	trackedTx := &TrackedTx{
		Tx:     nativeTx,
		logger: logger.New("debug", true),
		vendor: "postgresql",
	}

	ctx := logger.WithDBCounter(context.Background())

	rows := sqlmock.NewRows([]string{"id"}).AddRow(1)
	mock.ExpectQuery(regexp.QuoteMeta("SELECT id FROM sample WHERE flag = $1")).
		WithArgs(true).
		WillReturnRows(rows)

	resultRows, err := trackedTx.QueryContext(ctx, "SELECT id FROM sample WHERE flag = $1", true)
	require.NoError(t, err)
	require.NotNil(t, resultRows)
	resultRows.Close()

	nameRows := sqlmock.NewRows([]string{"name"}).AddRow("alice")
	mock.ExpectQuery(regexp.QuoteMeta("SELECT name FROM sample WHERE id = $1")).
		WithArgs(1).
		WillReturnRows(nameRows)

	row := trackedTx.QueryRowContext(ctx, "SELECT name FROM sample WHERE id = $1", 1)
	require.NotNil(t, row)
	var name string
	require.NoError(t, row.Scan(&name))
	assert.Equal(t, "alice", name)

	mock.ExpectExec(regexp.QuoteMeta("UPDATE sample SET name = $1 WHERE id = $2")).
		WithArgs("bob", 1).
		WillReturnResult(sqlmock.NewResult(0, 1))

	_, err = trackedTx.ExecContext(ctx, "UPDATE sample SET name = $1 WHERE id = $2", "bob", 1)
	require.NoError(t, err)

	mock.ExpectCommit()
	require.NoError(t, trackedTx.Commit())

	assert.Greater(t, logger.GetDBCounter(ctx), int64(0))
	assertDBElapsedPositive(ctx, t)
}

func TestTrackedConnectionBeginErrors(t *testing.T) {
	t.Parallel()
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, mock.ExpectationsWereMet()) })
	t.Cleanup(func() { _ = db.Close() })

	sc := &simpleConnection{db: db}
	log := logger.New("debug", true)
	tracked := NewTrackedConnection(sc, log).(*TrackedConnection)

	expectedBeginErr := errors.New("begin failed")
	mock.ExpectBegin().WillReturnError(expectedBeginErr)

	_, err = tracked.Begin(context.Background())
	require.Error(t, err)
	assert.ErrorIs(t, err, expectedBeginErr)

	expectedBeginTxErr := errors.New("begin tx failed")
	mock.ExpectBegin().WillReturnError(expectedBeginTxErr)

	_, err = tracked.BeginTx(context.Background(), &sql.TxOptions{Isolation: sql.LevelSerializable})
	require.Error(t, err)
	assert.ErrorIs(t, err, expectedBeginTxErr)
}

func TestTrackedConnectionNonTrackedMethods(t *testing.T) {
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

func TestTrackedConnectionContextWithoutCounter(t *testing.T) {
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

func TestTrackDBOperationWithError(t *testing.T) {
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

func TestTrackedStatementOperations(t *testing.T) {
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

func TestTrackedTransactionOperations(t *testing.T) {
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

func TestTrackedTransactionCommitRollback(t *testing.T) {
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

func TestTrackDBOperationNilLogger(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	start := time.Now().Add(-10 * time.Millisecond)

	assert.NotPanics(t, func() {
		trackDBOperation(ctx, nil, "postgresql", "SELECT 1", start, nil)
	})
}

func TestTrackDBOperationSqlErrNoRows(t *testing.T) {
	t.Parallel()
	log := logger.New("debug", true)
	ctx := context.Background()
	start := time.Now().Add(-10 * time.Millisecond)

	assert.NotPanics(t, func() {
		trackDBOperation(ctx, log, "postgresql", "SELECT * FROM users WHERE id = 999", start, sql.ErrNoRows)
	})
}

// Removed QueryTruncation test: behavior not observable without capturing logs

func TestTrackDBOperationSlowQueryThreshold(t *testing.T) {
	t.Parallel()
	log := logger.New("debug", true)
	ctx := context.Background()

	slowStart := time.Now().Add(-SlowQueryThreshold - 10*time.Millisecond)

	assert.NotPanics(t, func() {
		trackDBOperation(ctx, log, "postgresql", "SELECT * FROM huge_table", slowStart, nil)
	})
}

func TestTrackDBOperationRegularError(t *testing.T) {
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

// =============================================================================
// TrackedDB Tests - 0% Coverage Area
// =============================================================================

func TestNewTrackedDB(t *testing.T) {
	t.Parallel()
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, mock.ExpectationsWereMet()) })
	t.Cleanup(func() { _ = db.Close() })

	log := logger.New("debug", true)
	vendor := "postgresql"

	trackedDB := NewTrackedDB(db, log, vendor)

	assert.NotNil(t, trackedDB)
	assert.Equal(t, db, trackedDB.DB)
	assert.Equal(t, log, trackedDB.logger)
	assert.Equal(t, vendor, trackedDB.vendor)
}

func TestTrackedDBQueryContext(t *testing.T) {
	t.Parallel()
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, mock.ExpectationsWereMet()) })
	t.Cleanup(func() { _ = db.Close() })

	log := logger.New("debug", true)
	trackedDB := NewTrackedDB(db, log, "postgresql")

	// Setup expectations
	rows := sqlmock.NewRows([]string{"id", "name"}).AddRow(1, "John")
	mock.ExpectQuery(regexp.QuoteMeta("SELECT * FROM users")).WillReturnRows(rows)

	ctx := logger.WithDBCounter(context.Background())
	resultRows, err := trackedDB.QueryContext(ctx, "SELECT * FROM users")

	require.NoError(t, err)
	assert.NotNil(t, resultRows)
	resultRows.Close()

	assertDBCounter(ctx, t, 1)
	assertDBElapsedPositive(ctx, t)
}

func TestTrackedDBQueryContextError(t *testing.T) {
	t.Parallel()
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, mock.ExpectationsWereMet()) })
	t.Cleanup(func() { _ = db.Close() })

	log := logger.New("debug", true)
	trackedDB := NewTrackedDB(db, log, "postgresql")

	expectedErr := errors.New("query failed")
	mock.ExpectQuery(regexp.QuoteMeta("SELECT * FROM invalid")).WillReturnError(expectedErr)

	ctx := logger.WithDBCounter(context.Background())
	rows, err := trackedDB.QueryContext(ctx, "SELECT * FROM invalid")

	assert.Error(t, err)
	assert.ErrorIs(t, err, expectedErr)
	assert.Nil(t, rows)
	assertDBCounter(ctx, t, 1)
}

func TestTrackedDBQueryRowContext(t *testing.T) {
	t.Parallel()
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, mock.ExpectationsWereMet()) })
	t.Cleanup(func() { _ = db.Close() })

	log := logger.New("debug", true)
	trackedDB := NewTrackedDB(db, log, "oracle")

	rows := sqlmock.NewRows([]string{"name"}).AddRow("John")
	mock.ExpectQuery(regexp.QuoteMeta("SELECT name FROM users WHERE id = ?")).WithArgs(1).WillReturnRows(rows)

	ctx := logger.WithDBCounter(context.Background())
	row := trackedDB.QueryRowContext(ctx, "SELECT name FROM users WHERE id = ?", 1)

	assert.NotNil(t, row)

	// Verify we can scan the result
	var name string
	err = row.Scan(&name)
	require.NoError(t, err)
	assert.Equal(t, "John", name)
	assertDBCounter(ctx, t, 1)
}

func TestTrackedDBExecContext(t *testing.T) {
	t.Parallel()
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, mock.ExpectationsWereMet()) })
	t.Cleanup(func() { _ = db.Close() })

	log := logger.New("debug", true)
	trackedDB := NewTrackedDB(db, log, "postgresql")

	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO users (name) VALUES (?)")).
		WithArgs("Alice").
		WillReturnResult(sqlmock.NewResult(2, 1))

	ctx := logger.WithDBCounter(context.Background())
	result, err := trackedDB.ExecContext(ctx, "INSERT INTO users (name) VALUES (?)", "Alice")

	require.NoError(t, err)
	assert.NotNil(t, result)

	lastID, err := result.LastInsertId()
	require.NoError(t, err)
	assert.Equal(t, int64(2), lastID)

	rowsAffected, err := result.RowsAffected()
	require.NoError(t, err)
	assert.Equal(t, int64(1), rowsAffected)
	assertDBCounter(ctx, t, 1)
}

func TestTrackedDBExecContextError(t *testing.T) {
	t.Parallel()
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, mock.ExpectationsWereMet()) })
	t.Cleanup(func() { _ = db.Close() })

	log := logger.New("debug", true)
	trackedDB := NewTrackedDB(db, log, "oracle")

	expectedErr := errors.New("constraint violation")
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO users (name) VALUES (?)")).
		WithArgs("").
		WillReturnError(expectedErr)

	ctx := logger.WithDBCounter(context.Background())
	result, err := trackedDB.ExecContext(ctx, "INSERT INTO users (name) VALUES (?)", "")

	assert.Error(t, err)
	assert.ErrorIs(t, err, expectedErr)
	assert.Nil(t, result)
	assertDBCounter(ctx, t, 1)
}

func TestTrackedDBClose(t *testing.T) {
	t.Parallel()
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, mock.ExpectationsWereMet()) })

	log := logger.New("debug", true)
	trackedDB := NewTrackedDB(db, log, "postgresql")

	mock.ExpectClose()

	err = trackedDB.Close()
	require.NoError(t, err)
}

func TestTrackedDBPrepareContext(t *testing.T) {
	t.Parallel()
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, mock.ExpectationsWereMet()) })
	t.Cleanup(func() { _ = db.Close() })

	log := logger.New("debug", true)
	trackedDB := NewTrackedDB(db, log, "oracle")

	mock.ExpectPrepare(regexp.QuoteMeta("SELECT * FROM users WHERE id = ?"))

	ctx := logger.WithDBCounter(context.Background())
	stmt, err := trackedDB.PrepareContext(ctx, "SELECT * FROM users WHERE id = ?")

	require.NoError(t, err)
	assert.NotNil(t, stmt)
	assert.IsType(t, &TrackedStmt{}, stmt)
	t.Cleanup(func() { _ = stmt.Close() })
	assertDBCounter(ctx, t, 1)
}

func TestTrackedDBPrepareContextError(t *testing.T) {
	t.Parallel()
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, mock.ExpectationsWereMet()) })
	t.Cleanup(func() { _ = db.Close() })

	log := logger.New("debug", true)
	trackedDB := NewTrackedDB(db, log, "postgresql")

	expectedErr := errors.New("prepare failed")
	mock.ExpectPrepare(regexp.QuoteMeta("INVALID SQL")).WillReturnError(expectedErr)

	ctx := logger.WithDBCounter(context.Background())
	stmt, err := trackedDB.PrepareContext(ctx, "INVALID SQL")

	assert.Error(t, err)
	assert.ErrorIs(t, err, expectedErr)
	assert.Nil(t, stmt)
	assertDBCounter(ctx, t, 1)
}

// =============================================================================
// TrackedStmt Tests - Missing Coverage Areas
// =============================================================================

func TestTrackedStmtQuery(t *testing.T) {
	t.Parallel()
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, mock.ExpectationsWereMet()) })
	t.Cleanup(func() { _ = db.Close() })

	log := logger.New("debug", true)
	trackedDB := NewTrackedDB(db, log, "postgresql")

	// Prepare a statement
	mock.ExpectPrepare(regexp.QuoteMeta("SELECT * FROM users WHERE id = $1"))

	ctx := logger.WithDBCounter(context.Background())
	stmt, err := trackedDB.PrepareContext(ctx, "SELECT * FROM users WHERE id = $1")
	require.NoError(t, err)
	require.NotNil(t, stmt)

	trackedStmt, ok := stmt.(*TrackedStmt)
	require.True(t, ok)
	t.Cleanup(func() { _ = stmt.Close() })
	assertDBCounter(ctx, t, 1)

	// Test Query method
	rows := sqlmock.NewRows([]string{"id", "name"}).AddRow(1, "John")
	mock.ExpectQuery(regexp.QuoteMeta("SELECT * FROM users WHERE id = $1")).WithArgs(1).WillReturnRows(rows)

	resultRows, err := trackedStmt.Query(ctx, 1)
	require.NoError(t, err)
	assert.NotNil(t, resultRows)
	resultRows.Close()
	assertDBCounter(ctx, t, 2)
}

func TestTrackedStmtQueryRow(t *testing.T) {
	t.Parallel()
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, mock.ExpectationsWereMet()) })
	t.Cleanup(func() { _ = db.Close() })

	log := logger.New("debug", true)
	trackedDB := NewTrackedDB(db, log, "postgresql")

	// Prepare a statement
	mock.ExpectPrepare(regexp.QuoteMeta("SELECT name FROM users WHERE id = $1"))

	ctx := logger.WithDBCounter(context.Background())
	stmt, err := trackedDB.PrepareContext(ctx, "SELECT name FROM users WHERE id = $1")
	require.NoError(t, err)
	require.NotNil(t, stmt)

	trackedStmt, ok := stmt.(*TrackedStmt)
	require.True(t, ok)
	t.Cleanup(func() { _ = stmt.Close() })
	assertDBCounter(ctx, t, 1)

	// Test QueryRow method
	rowData := sqlmock.NewRows([]string{"name"}).AddRow("Jane")
	mock.ExpectQuery(regexp.QuoteMeta("SELECT name FROM users WHERE id = $1")).WithArgs(2).WillReturnRows(rowData)

	row := trackedStmt.QueryRow(ctx, 2)
	assert.NotNil(t, row)

	var name string
	err = row.Scan(&name)
	require.NoError(t, err)
	assert.Equal(t, "Jane", name)
	assertDBCounter(ctx, t, 2)
}

func TestTrackedStmtExec(t *testing.T) {
	t.Parallel()
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, mock.ExpectationsWereMet()) })
	t.Cleanup(func() { _ = db.Close() })

	log := logger.New("debug", true)
	trackedDB := NewTrackedDB(db, log, "postgresql")

	// Prepare a statement for an UPDATE operation
	mock.ExpectPrepare(regexp.QuoteMeta("UPDATE users SET name = $1 WHERE id = $2"))

	ctx := logger.WithDBCounter(context.Background())
	stmt, err := trackedDB.PrepareContext(ctx, "UPDATE users SET name = $1 WHERE id = $2")
	require.NoError(t, err)
	require.NotNil(t, stmt)

	trackedStmt, ok := stmt.(*TrackedStmt)
	require.True(t, ok)
	t.Cleanup(func() { _ = stmt.Close() })
	assertDBCounter(ctx, t, 1)

	// Test Exec method
	mock.ExpectExec(regexp.QuoteMeta("UPDATE users SET name = $1 WHERE id = $2")).WithArgs("NewName", 3).WillReturnResult(sqlmock.NewResult(1, 1))

	result, err := trackedStmt.Exec(ctx, "NewName", 3)
	require.NoError(t, err)
	assert.NotNil(t, result)

	affected, err := result.RowsAffected()
	require.NoError(t, err)
	assert.Equal(t, int64(1), affected)
	assertDBCounter(ctx, t, 2)
}

func TestTrackedConnectionClose(t *testing.T) {
	t.Parallel()
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, mock.ExpectationsWereMet()) })

	log := logger.New("debug", true)
	sc := &simpleConnection{db: db}
	conn := NewTrackedConnection(sc, log)

	mock.ExpectClose()

	err = conn.Close()
	require.NoError(t, err)
}

// =============================================================================
// TrackedStatement Tests - Missing Coverage Areas
// =============================================================================

func TestTrackedStatementQueryRowMissing(t *testing.T) {
	t.Parallel()
	mock, tracked, ctx := setupTracked(t, true)

	// Prepare statement
	mock.ExpectPrepare(regexp.QuoteMeta("SELECT name FROM users WHERE id = ?"))

	// Mock QueryRow operation
	rows := sqlmock.NewRows([]string{"name"}).AddRow("Alice")
	mock.ExpectQuery(regexp.QuoteMeta("SELECT name FROM users WHERE id = ?")).WithArgs(5).WillReturnRows(rows)

	stmt, err := tracked.Prepare(ctx, "SELECT name FROM users WHERE id = ?")
	require.NoError(t, err)
	t.Cleanup(func() { _ = stmt.Close() })

	// Test QueryRow (0% coverage)
	row := stmt.QueryRow(ctx, 5)
	assert.NotNil(t, row)

	var name string
	err = row.Scan(&name)
	require.NoError(t, err)
	assert.Equal(t, "Alice", name)

	assertDBCounter(ctx, t, 2) // prepare + queryrow
}

func TestTrackedStatementExecMissing(t *testing.T) {
	t.Parallel()
	mock, tracked, ctx := setupTracked(t, true)

	// Prepare statement
	mock.ExpectPrepare(regexp.QuoteMeta("UPDATE users SET active = ? WHERE id = ?"))

	// Mock Exec operation
	mock.ExpectExec(regexp.QuoteMeta("UPDATE users SET active = ? WHERE id = ?")).
		WithArgs(true, 10).
		WillReturnResult(sqlmock.NewResult(0, 1))

	stmt, err := tracked.Prepare(ctx, "UPDATE users SET active = ? WHERE id = ?")
	require.NoError(t, err)
	t.Cleanup(func() { _ = stmt.Close() })

	// Test Exec (0% coverage)
	result, err := stmt.Exec(ctx, true, 10)
	require.NoError(t, err)
	assert.NotNil(t, result)

	rowsAffected, err := result.RowsAffected()
	require.NoError(t, err)
	assert.Equal(t, int64(1), rowsAffected)

	assertDBCounter(ctx, t, 2) // prepare + exec
}
