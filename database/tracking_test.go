package database

import (
	"context"
	"database/sql"
	"errors"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/config"
	"github.com/gaborage/go-bricks/database/internal/tracking"
	"github.com/gaborage/go-bricks/database/types"
	"github.com/gaborage/go-bricks/logger"
)

// Test SQL constants to avoid duplication
const (
	selectAllUsers             = "SELECT * FROM users"
	selectAllInvalid           = "SELECT * FROM invalid"
	selectNameFromUsersWithID  = "SELECT name FROM users WHERE id = $1"
	insertIntoUsersSQL         = "INSERT INTO users"
	selectAllUsersWithID       = "SELECT * FROM users WHERE id = $1"
	selectCountUsers           = "SELECT COUNT(*) FROM users"
	selectOne                  = "SELECT 1"
	updateUsersSetName         = "UPDATE users SET name = $1 WHERE id = $2"
	selectNameFromUsersOracle  = "SELECT name FROM users WHERE id = ?"
	updateUsersSetActiveOracle = "UPDATE users SET active = ? WHERE id = ?"
)

// test helpers
func setupTracked(t testing.TB, withCounter bool) (sqlmock.Sqlmock, *TrackedConnection, context.Context) {
	t.Helper()
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, mock.ExpectationsWereMet()) })
	t.Cleanup(func() { _ = db.Close() })

	sc := &simpleConnection{db: db}
	log := newTestLogger()
	tracked := NewTrackedConnection(sc, log, nil).(*TrackedConnection)
	var ctx context.Context
	if withCounter {
		ctx = logger.WithRequestCounters(context.Background())
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
	// Note: sqlmock operations can complete in <1ns, so >= 0 is correct
	assert.GreaterOrEqual(t, logger.GetDBElapsed(ctx), int64(0))
}

func TestNewTrackedConnection(t *testing.T) {
	t.Parallel()
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, mock.ExpectationsWereMet()) })
	t.Cleanup(func() { _ = db.Close() })

	simpleConn := &simpleConnection{db: db}
	log := newTestLogger()

	tracked := NewTrackedConnection(simpleConn, log, nil)

	// Verify wrapper functionality instead of internal implementation
	assert.NotNil(t, tracked)
	assert.Implements(t, (*types.Interface)(nil), tracked)

	// Verify the wrapper delegates correctly (functionality test)
	assert.Equal(t, "postgresql", tracked.DatabaseType())
}

func TestTrackedConnectionQuery(t *testing.T) {
	t.Parallel()
	mock, tracked, ctx := setupTracked(t, true)

	rows := sqlmock.NewRows([]string{"id", "name"}).AddRow(1, "John")
	mock.ExpectQuery(regexp.QuoteMeta(selectAllUsers)).WillReturnRows(rows)

	resultRows, err := tracked.Query(ctx, selectAllUsers)
	require.NoError(t, err)
	defer resultRows.Close()

	assertDBCounter(ctx, t, 1)
	assertDBElapsedPositive(ctx, t)
}

func TestTrackedConnectionQueryWithError(t *testing.T) {
	t.Parallel()
	mock, tracked, ctx := setupTracked(t, true)

	expectedErr := errors.New("query error")
	mock.ExpectQuery(regexp.QuoteMeta(selectAllInvalid)).WillReturnError(expectedErr)

	rows, err := tracked.Query(ctx, selectAllInvalid)
	assert.Nil(t, rows)

	assertDBCounter(ctx, t, 1)
	require.ErrorIs(t, err, expectedErr)
}

func TestTrackedConnectionQueryRow(t *testing.T) {
	t.Parallel()
	mock, tracked, ctx := setupTracked(t, true)

	rows := sqlmock.NewRows([]string{"name"}).AddRow("John")
	mock.ExpectQuery(regexp.QuoteMeta(selectNameFromUsersWithID)).WithArgs(1).WillReturnRows(rows)

	row := tracked.QueryRow(ctx, selectNameFromUsersWithID, 1)
	assert.NotNil(t, row)
	var name string
	require.NoError(t, row.Scan(&name))
	assert.Equal(t, "John", name)

	assertDBCounter(ctx, t, 1)
}

func TestTrackedConnectionExec(t *testing.T) {
	t.Parallel()
	mock, tracked, ctx := setupTracked(t, true)

	mock.ExpectExec(regexp.QuoteMeta(insertIntoUsersSQL)).WithArgs("John").WillReturnResult(sqlmock.NewResult(1, 1))

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

	mock.ExpectPrepare(regexp.QuoteMeta(selectAllUsersWithID))

	stmt, err := tracked.Prepare(ctx, selectAllUsersWithID)
	require.NoError(t, err)
	assert.NotNil(t, stmt)
	assert.NotNil(t, stmt)
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
	mock.ExpectQuery(regexp.QuoteMeta(selectAllUsers)).WillReturnRows(rows)
	mock.ExpectExec(regexp.QuoteMeta("UPDATE users SET active = true")).WillReturnResult(sqlmock.NewResult(0, 2))

	countRows := sqlmock.NewRows([]string{"count"}).AddRow(10)
	mock.ExpectQuery(regexp.QuoteMeta(selectCountUsers)).WillReturnRows(countRows)

	queryRows, err1 := tracked.Query(ctx, selectAllUsers)
	require.NoError(t, err1)
	defer queryRows.Close()

	_, err2 := tracked.Exec(ctx, "UPDATE users SET active = true")
	require.NoError(t, err2)

	row := tracked.QueryRow(ctx, selectCountUsers)
	require.NotNil(t, row)
	var count int
	require.NoError(t, row.Scan(&count))
	assert.Equal(t, 10, count)

	assertDBCounter(ctx, t, 3)
	assertDBElapsedPositive(ctx, t)
}

func TestTrackedTxContextMethods(t *testing.T) {
	// Simplified test - functionality tested in internal packages
	t.Skip("Skipping complex transaction test for simplicity - functionality tested in internal packages")
}

func TestTrackedConnectionBeginErrors(t *testing.T) {
	t.Parallel()
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, mock.ExpectationsWereMet()) })
	t.Cleanup(func() { _ = db.Close() })

	sc := &simpleConnection{db: db}
	log := newTestLogger()
	tracked := NewTrackedConnection(sc, log, nil).(*TrackedConnection)

	expectedBeginErr := errors.New("begin failed")
	mock.ExpectBegin().WillReturnError(expectedBeginErr)

	_, err = tracked.Begin(context.Background())
	require.Error(t, err)
	assert.ErrorIs(t, err, expectedBeginErr) //nolint:testifylint // the BeginTx phase is asserted after this

	expectedBeginTxErr := errors.New("begin tx failed")
	mock.ExpectBegin().WillReturnError(expectedBeginTxErr)

	_, err = tracked.BeginTx(context.Background(), &sql.TxOptions{Isolation: sql.LevelSerializable})
	require.Error(t, err)
	assert.ErrorIs(t, err, expectedBeginTxErr)
}

func TestTrackedConnectionUtilityAndBeginMethods(t *testing.T) {
	t.Parallel()
	db, mock, err := sqlmock.New(sqlmock.MonitorPingsOption(true))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, mock.ExpectationsWereMet()) })
	t.Cleanup(func() { _ = db.Close() })

	sc := &simpleConnection{db: db}
	log := newTestLogger()
	ctx := logger.WithRequestCounters(context.Background())
	tracked := NewTrackedConnection(sc, log, nil)

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

	table := tracked.MigrationTable()
	assert.Equal(t, "flyway_schema_history", table)

	dbType := tracked.DatabaseType()
	assert.Equal(t, "postgresql", dbType)

	tx4, err4 := tracked.Begin(ctx)
	require.NoError(t, err4)
	defer tx4.Rollback(ctx) // No-op: test transaction

	tx5, err5 := tracked.BeginTx(ctx, nil)
	require.NoError(t, err5)
	defer tx5.Rollback(ctx) // No-op: test transaction

	// Note: The improved implementation now correctly tracks Begin/BeginTx operations
	// This is a fix - the original implementation was missing tracking for transaction starts
	assertDBCounter(ctx, t, 2)
}

func TestTrackedConnectionContextWithoutCounter(t *testing.T) {
	t.Parallel()
	mock, tracked, _ := setupTracked(t, false)

	rows := sqlmock.NewRows([]string{"result"}).AddRow(1)
	mock.ExpectQuery(regexp.QuoteMeta(selectOne)).WillReturnRows(rows)

	// This should not panic even without DB counter in context
	func() {
		resultRows, err := tracked.Query(context.Background(), selectOne)
		require.NoError(t, err)
		defer resultRows.Close()
	}()
}

func TestTrackDBOperation(t *testing.T) {
	t.Parallel()
	log := newTestLogger()
	ctx := logger.WithRequestCounters(context.Background())

	start := time.Now().Add(-10 * time.Millisecond) // Simulate 10ms operation
	settings := tracking.NewSettings(nil)

	// Test successful operation
	tc := &tracking.Context{Logger: log, Vendor: "postgresql", Settings: settings}
	tracking.TrackDBOperation(ctx, tc, "SELECT * FROM test", nil, start, 0, nil)

	assertDBCounter(ctx, t, 1)
	// elapsed should be >= 10ms in nanoseconds
	assert.GreaterOrEqual(t, logger.GetDBElapsed(ctx), int64(10*time.Millisecond))
}

func TestTrackDBOperationWithError(t *testing.T) {
	t.Parallel()
	log := newTestLogger()
	ctx := logger.WithRequestCounters(context.Background())

	start := time.Now()
	testErr := errors.New("database error")
	settings := tracking.NewSettings(nil)

	// This should not panic
	tc := &tracking.Context{Logger: log, Vendor: "oracle", Settings: settings}
	tracking.TrackDBOperation(ctx, tc, selectAllInvalid, nil, start, 0, testErr)

	// Verify counter was still incremented despite error
	assertDBCounter(ctx, t, 1)
}

func TestTrackedStatementOperations(t *testing.T) {
	t.Parallel()
	mock, tracked, ctx := setupTracked(t, true)

	// Prepare statement
	mock.ExpectPrepare(regexp.QuoteMeta(selectAllUsersWithID))

	// Mock statement operations
	rows := sqlmock.NewRows([]string{"id", "name"}).AddRow(1, "John")
	mock.ExpectQuery(regexp.QuoteMeta(selectAllUsersWithID)).WithArgs(1).WillReturnRows(rows)

	// Prepare statement (should increment counter)
	stmt, err := tracked.Prepare(ctx, selectAllUsersWithID)
	require.NoError(t, err)
	assert.NotNil(t, stmt)
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
	mock.ExpectQuery(regexp.QuoteMeta(selectAllUsers)).WillReturnRows(rows)

	countRows := sqlmock.NewRows([]string{"count"}).AddRow(5)
	mock.ExpectQuery(regexp.QuoteMeta(selectCountUsers)).WillReturnRows(countRows)

	mock.ExpectExec(regexp.QuoteMeta(insertIntoUsersSQL)).WithArgs("John").WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectPrepare(regexp.QuoteMeta(updateUsersSetName))

	// Begin transaction
	tx, err := tracked.Begin(ctx)
	require.NoError(t, err)
	defer tx.Rollback(ctx) // No-op: test transaction
	assert.NotNil(t, tx)

	// Execute operations within transaction (each should increment counter)
	txRows, err := tx.Query(ctx, selectAllUsers)
	require.NoError(t, err)
	defer txRows.Close()

	row := tx.QueryRow(ctx, selectCountUsers)
	require.NotNil(t, row)
	var count int
	require.NoError(t, row.Scan(&count))
	assert.Equal(t, 5, count)

	_, err = tx.Exec(ctx, "INSERT INTO users (name) VALUES ($1)", "John")
	require.NoError(t, err)

	// Prepare statement within transaction (should increment counter)
	stmt, err := tx.Prepare(ctx, updateUsersSetName)
	require.NoError(t, err)
	assert.NotNil(t, stmt)
	t.Cleanup(func() { _ = stmt.Close() })

	// Verify DB counter was incremented for all operations
	assertDBCounter(ctx, t, 5)
}

func TestTrackedTransactionCommitRollback(t *testing.T) {
	t.Parallel()
	mock, tracked, _ := setupTracked(t, false)

	// Test successful commit
	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta(insertIntoUsersSQL)).WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()

	tx, err := tracked.Begin(context.Background())
	require.NoError(t, err)
	defer tx.Rollback(context.Background()) // No-op after commit

	_, err = tx.Exec(context.Background(), "INSERT INTO users (name) VALUES ('test')")
	require.NoError(t, err)

	err = tx.Commit(context.Background())
	require.NoError(t, err)

	// Test rollback
	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta(insertIntoUsersSQL)).WillReturnError(errors.New("constraint violation"))
	mock.ExpectRollback()

	tx2, err := tracked.Begin(context.Background())
	require.NoError(t, err)

	_, err = tx2.Exec(context.Background(), "INSERT INTO users (name) VALUES ('test')")
	require.Error(t, err)

	err = tx2.Rollback(context.Background())
	require.NoError(t, err)
}

// Database tracking hardening tests

func TestTrackDBOperationNilLogger(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	start := time.Now().Add(-10 * time.Millisecond)
	settings := tracking.NewSettings(nil)

	assert.NotPanics(t, func() {
		tc := &tracking.Context{Logger: nil, Vendor: "postgresql", Settings: settings}
		tracking.TrackDBOperation(ctx, tc, selectOne, nil, start, 0, nil)
	})
}

func TestTrackDBOperationSqlErrNoRows(t *testing.T) {
	t.Parallel()
	log := newTestLogger()
	ctx := context.Background()
	start := time.Now().Add(-10 * time.Millisecond)
	settings := tracking.NewSettings(nil)

	assert.NotPanics(t, func() {
		tc := &tracking.Context{Logger: log, Vendor: "postgresql", Settings: settings}
		tracking.TrackDBOperation(ctx, tc, "SELECT * FROM users WHERE id = 999", nil, start, 0, sql.ErrNoRows)
	})
}

// Removed QueryTruncation test: behavior not observable without capturing logs

func TestTrackDBOperationSlowQueryThreshold(t *testing.T) {
	t.Parallel()
	log := newTestLogger()
	ctx := context.Background()
	settings := tracking.NewSettings(&config.DatabaseConfig{Query: config.QueryConfig{Slow: config.SlowQueryConfig{Threshold: tracking.DefaultSlowQueryThreshold}}})

	slowStart := time.Now().Add(-settings.SlowQueryThreshold() - 10*time.Millisecond)

	assert.NotPanics(t, func() {
		tc := &tracking.Context{Logger: log, Vendor: "postgresql", Settings: settings}
		tracking.TrackDBOperation(ctx, tc, "SELECT * FROM huge_table", nil, slowStart, 0, nil)
	})
}

func TestTrackDBOperationRegularError(t *testing.T) {
	t.Parallel()
	log := newTestLogger()
	ctx := context.Background()
	start := time.Now().Add(-10 * time.Millisecond)

	regularErr := errors.New("connection timeout")
	settings := tracking.NewSettings(nil)

	assert.NotPanics(t, func() {
		tc := &tracking.Context{Logger: log, Vendor: "oracle", Settings: settings}
		tracking.TrackDBOperation(ctx, tc, selectAllUsers, nil, start, 0, regularErr)
	})
}

// Removed constant and constant-usage tests: prefer behavior over implementation details

// TestWithRepositoryMethodReExport verifies the public database.WithRepositoryMethod
// / database.RepositoryMethodFromContext re-exports are wired to the tracking
// implementation and round-trip a method name (issue #510).
func TestWithRepositoryMethodReExport(t *testing.T) {
	t.Parallel()

	ctx := WithRepositoryMethod(context.Background(), "InsertTransaction")
	method, ok := RepositoryMethodFromContext(ctx)
	assert.True(t, ok)
	assert.Equal(t, "InsertTransaction", method)

	// Absent by default.
	_, ok = RepositoryMethodFromContext(context.Background())
	assert.False(t, ok)
}

func TestWithExpectedErrorReExport(t *testing.T) {
	t.Parallel()

	ctx := WithExpectedError(context.Background(), IsLockNotAvailable)
	assert.NotEqual(t, context.Background(), ctx)
	assert.Nil(t, WithExpectedError(nil, IsLockNotAvailable))
}

// TestTrackedQueryRowScanHonorsExpectedError proves a declaration survives the
// deferred tracking of a QueryRow, which classifies the error inside Scan: the
// declared 55P03 does not escalate request severity, the undeclared one does, and
// the caller receives the driver error unchanged either way.
func TestTrackedQueryRowScanHonorsExpectedError(t *testing.T) {
	t.Parallel()
	const lockSQL = "SELECT id FROM leader WHERE id = $1 FOR UPDATE NOWAIT"

	tests := []struct {
		name      string
		declare   bool
		wantLevel []zerolog.Level
	}{
		{name: "declared_is_benign", declare: true, wantLevel: nil},
		{name: "undeclared_escalates", declare: false, wantLevel: []zerolog.Level{zerolog.ErrorLevel}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mock, tracked, ctx := setupTracked(t, true)
			lockErr := &pgconn.PgError{Code: "55P03", Message: "could not obtain lock on row"}
			mock.ExpectQuery(regexp.QuoteMeta(lockSQL)).WithArgs(1).WillReturnError(lockErr)

			var levels []zerolog.Level
			ctx = logger.WithSeverityHook(ctx, func(level zerolog.Level) { levels = append(levels, level) })
			if tt.declare {
				ctx = WithExpectedError(ctx, IsLockNotAvailable)
			}

			var id int
			err := tracked.QueryRow(ctx, lockSQL, 1).Scan(&id)

			require.ErrorIs(t, err, lockErr)
			assert.True(t, IsLockNotAvailable(err))
			assert.Equal(t, tt.wantLevel, levels)
		})
	}
}

func TestTrackedConnectionClose(t *testing.T) {
	t.Parallel()
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, mock.ExpectationsWereMet()) })

	log := newTestLogger()
	sc := &simpleConnection{db: db}
	conn := NewTrackedConnection(sc, log, nil)

	mock.ExpectClose()

	err = conn.Close()
	require.NoError(t, err)
}

// =============================================================================
// Prepared-statement tests through a tracked connection
// =============================================================================

func TestTrackedStatementQueryRowMissing(t *testing.T) {
	t.Parallel()
	mock, tracked, ctx := setupTracked(t, true)

	// Prepare statement
	mock.ExpectPrepare(regexp.QuoteMeta(selectNameFromUsersOracle))

	// Mock QueryRow operation
	rows := sqlmock.NewRows([]string{"name"}).AddRow("Alice")
	mock.ExpectQuery(regexp.QuoteMeta(selectNameFromUsersOracle)).WithArgs(5).WillReturnRows(rows)

	stmt, err := tracked.Prepare(ctx, selectNameFromUsersOracle)
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
	mock.ExpectPrepare(regexp.QuoteMeta(updateUsersSetActiveOracle))

	// Mock Exec operation
	mock.ExpectExec(regexp.QuoteMeta(updateUsersSetActiveOracle)).
		WithArgs(true, 10).
		WillReturnResult(sqlmock.NewResult(0, 1))

	stmt, err := tracked.Prepare(ctx, updateUsersSetActiveOracle)
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
