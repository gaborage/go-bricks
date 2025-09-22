package oracle

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"regexp"
	"testing"
	"time"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/config"
	"github.com/gaborage/go-bricks/logger"
)

const (
	oraclePingErrorMsg = "failed to ping Oracle database"
	selectQuery        = "SELECT name FROM dual WHERE id = :1"
	insertQuery        = "INSERT INTO dual(id, name) VALUES (:1, :2)"
)

func TestConnectionBasicMethodsWithSQLMock(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()

	c := &Connection{db: db, logger: logger.New("disabled", true)}

	ctx := context.Background()

	// Health
	mock.ExpectPing()
	require.NoError(t, c.Health(ctx))

	// Exec
	mock.ExpectExec("INSERT INTO t").WithArgs("a").WillReturnResult(sqlmock.NewResult(1, 1))
	_, err = c.Exec(ctx, "INSERT INTO t(x) VALUES(?)", "a")
	require.NoError(t, err)

	// Query
	rows := sqlmock.NewRows([]string{"id"}).AddRow(1)
	mock.ExpectQuery("SELECT id FROM t").WillReturnRows(rows)
	rs, err := c.Query(ctx, "SELECT id FROM t")
	require.NoError(t, err)
	assert.True(t, rs.Next())
	_ = rs.Close()

	// QueryRow
	rows = sqlmock.NewRows([]string{"now"}).AddRow(time.Now())
	mock.ExpectQuery("SELECT CURRENT_TIMESTAMP").WillReturnRows(rows)
	_ = c.QueryRow(ctx, "SELECT CURRENT_TIMESTAMP")

	// Prepare + Statement Exec
	mock.ExpectPrepare("UPDATE t SET x").ExpectExec().WithArgs("b", 1).WillReturnResult(sqlmock.NewResult(0, 1))
	st, err := c.Prepare(ctx, "UPDATE t SET x=:1 WHERE id=:2")
	require.NoError(t, err)
	_, err = st.Exec(ctx, "b", 1)
	require.NoError(t, err)
	require.NoError(t, st.Close())

	// Begin + Tx methods
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT 1 FROM dual").WillReturnRows(sqlmock.NewRows([]string{"x"}).AddRow(1))
	mock.ExpectCommit()
	tx, err := c.Begin(ctx)
	require.NoError(t, err)
	rs2, err := tx.Query(ctx, "SELECT 1 FROM dual")
	require.NoError(t, err)
	_ = rs2.Close()
	require.NoError(t, tx.Commit())

	// BeginTx + rollback
	mock.ExpectBegin()
	mock.ExpectRollback()
	tx2, err := c.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelDefault})
	require.NoError(t, err)
	require.NoError(t, tx2.Rollback())

	// Stats
	m, err := c.Stats()
	require.NoError(t, err)
	assert.Contains(t, m, "max_open_connections")
	assert.Contains(t, m, "open_connections")
	assert.Contains(t, m, "wait_duration")

	// Meta
	assert.Equal(t, "oracle", c.DatabaseType())
	assert.Equal(t, "FLYWAY_SCHEMA_HISTORY", c.GetMigrationTable())

	// CreateMigrationTable should run two Execs
	mock.ExpectExec("BEGIN").WillReturnResult(driver.RowsAffected(0))
	mock.ExpectExec("BEGIN").WillReturnResult(driver.RowsAffected(0))
	require.NoError(t, c.CreateMigrationTable(ctx))

	// Close
	mock.ExpectClose()
	require.NoError(t, c.Close())

	// Verify expectations
	require.NoError(t, mock.ExpectationsWereMet())
}

// =============================================================================
// Connection Management Tests
// =============================================================================

func TestConnectionNewConnectionWithConnectionString(t *testing.T) {
	// Test configuration with connection string
	cfg := &config.DatabaseConfig{
		ConnectionString: "oracle://user:pass@localhost:1521/XE",
		MaxConns:         25,
		MaxIdleConns:     10,
		ConnMaxLifetime:  time.Hour,
		ConnMaxIdleTime:  30 * time.Minute,
	}

	log := logger.New("debug", true)

	// This will fail because we're not connecting to a real database
	// but we can test the configuration parsing part
	_, err := NewConnection(cfg, log)

	// We expect an error because the database doesn't exist, but the error should be connection-related, not config-related
	assert.Error(t, err)
	assert.Contains(t, err.Error(), oraclePingErrorMsg)
}

func TestConnectionNewConnectionWithServiceName(t *testing.T) {
	// Test configuration with ServiceName
	cfg := &config.DatabaseConfig{
		Host:            "localhost",
		Port:            1521,
		Username:        "testuser",
		Password:        "testpass",
		Service:         config.ServiceConfig{Name: "XEPDB1"},
		MaxConns:        25,
		MaxIdleConns:    10,
		ConnMaxLifetime: time.Hour,
		ConnMaxIdleTime: 30 * time.Minute,
	}

	log := logger.New("debug", true)

	// This will fail because we're not connecting to a real database
	_, err := NewConnection(cfg, log)

	// We expect an error because the database doesn't exist
	assert.Error(t, err)
	assert.Contains(t, err.Error(), oraclePingErrorMsg)
}

func TestConnectionNewConnectionWithSID(t *testing.T) {
	// Test configuration with SID
	cfg := &config.DatabaseConfig{
		Host:            "localhost",
		Port:            1521,
		Username:        "testuser",
		Password:        "testpass",
		SID:             "XE",
		MaxConns:        25,
		MaxIdleConns:    10,
		ConnMaxLifetime: time.Hour,
		ConnMaxIdleTime: 30 * time.Minute,
	}

	log := logger.New("debug", true)

	// This will fail because we're not connecting to a real database
	_, err := NewConnection(cfg, log)

	// We expect an error because the database doesn't exist
	assert.Error(t, err)
	assert.Contains(t, err.Error(), oraclePingErrorMsg)
}

func TestConnectionNewConnectionWithDatabase(t *testing.T) {
	// Test configuration with Database (fallback when no ServiceName or SID)
	cfg := &config.DatabaseConfig{
		Host:            "localhost",
		Port:            1521,
		Username:        "testuser",
		Password:        "testpass",
		Database:        "XE",
		MaxConns:        25,
		MaxIdleConns:    10,
		ConnMaxLifetime: time.Hour,
		ConnMaxIdleTime: 30 * time.Minute,
	}

	log := logger.New("debug", true)

	// This will fail because we're not connecting to a real database
	_, err := NewConnection(cfg, log)

	// We expect an error because the database doesn't exist
	assert.Error(t, err)
	assert.Contains(t, err.Error(), oraclePingErrorMsg)
}

func TestConnectionNewConnectionSuccess(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, mock.ExpectationsWereMet()) })

	originalOpen := openOracleDB
	originalPing := pingOracleDB
	openOracleDB = func(string) (*sql.DB, error) { return db, nil }
	pingOracleDB = func(context.Context, *sql.DB) error { return nil }
	t.Cleanup(func() {
		openOracleDB = originalOpen
		pingOracleDB = originalPing
	})

	cfg := &config.DatabaseConfig{
		Host:            "localhost",
		Port:            1521,
		Username:        "stub",
		Password:        "secret",
		Service:         config.ServiceConfig{Name: "XEPDB1"},
		MaxConns:        4,
		MaxIdleConns:    2,
		ConnMaxLifetime: 45 * time.Second,
	}

	log := logger.New("debug", true)

	conn, err := NewConnection(cfg, log)
	require.NoError(t, err)
	require.NotNil(t, conn)

	mock.ExpectClose()
	require.NoError(t, conn.Close())
}

func TestConnectionCreateMigrationTable(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()

	c := &Connection{db: db, logger: logger.New("debug", true)}

	ctx := context.Background()

	// Mock the CREATE TABLE execution (two PL/SQL blocks)
	mock.ExpectExec(`BEGIN`).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(`BEGIN`).
		WillReturnResult(sqlmock.NewResult(0, 0))

	err = c.CreateMigrationTable(ctx)
	assert.NoError(t, err)

	// Verify all expectations were met
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestConnectionCreateMigrationTableFirstError(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()

	c := &Connection{db: db, logger: logger.New("debug", true)}

	ctx := context.Background()

	// Mock the first PL/SQL block to fail
	mock.ExpectExec(`BEGIN`).
		WillReturnError(sql.ErrConnDone)

	err = c.CreateMigrationTable(ctx)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to create Oracle migration table")

	// Verify all expectations were met
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestOracleStatementQueryAndQueryRow(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, mock.ExpectationsWereMet()) })
	t.Cleanup(func() { _ = db.Close() })

	mock.ExpectPrepare(regexp.QuoteMeta("SELECT id FROM dual WHERE flag = :1")).
		ExpectQuery().
		WithArgs(true).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(5))

	stmt, err := db.PrepareContext(context.Background(), "SELECT id FROM dual WHERE flag = :1")
	require.NoError(t, err)
	ps := &Statement{stmt: stmt}

	rows, err := ps.Query(context.Background(), true)
	require.NoError(t, err)
	require.True(t, rows.Next())
	rows.Close()
	require.NoError(t, ps.Close())

	mock.ExpectPrepare(regexp.QuoteMeta(selectQuery)).
		ExpectQuery().
		WithArgs(5).
		WillReturnRows(sqlmock.NewRows([]string{"name"}).AddRow("beta"))

	stmtRow, err := db.PrepareContext(context.Background(), selectQuery)
	require.NoError(t, err)
	psRow := &Statement{stmt: stmtRow}

	row := psRow.QueryRow(context.Background(), 5)
	var name string
	require.NoError(t, row.Scan(&name))
	assert.Equal(t, "beta", name)
	require.NoError(t, psRow.Close())
}

func TestOracleTransactionQueryPrepareExec(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, mock.ExpectationsWereMet()) })
	t.Cleanup(func() { _ = db.Close() })

	mock.ExpectBegin()
	nativeTx, err := db.BeginTx(context.Background(), nil)
	require.NoError(t, err)
	trx := &Transaction{tx: nativeTx}

	mock.ExpectQuery(regexp.QuoteMeta("SELECT id FROM dual WHERE code = :1")).
		WithArgs("X").
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(11))

	rows, err := trx.Query(context.Background(), "SELECT id FROM dual WHERE code = :1", "X")
	require.NoError(t, err)
	rows.Close()

	mock.ExpectQuery(regexp.QuoteMeta(selectQuery)).
		WithArgs(11).
		WillReturnRows(sqlmock.NewRows([]string{"name"}).AddRow("gamma"))

	row := trx.QueryRow(context.Background(), selectQuery, 11)
	var name string
	require.NoError(t, row.Scan(&name))
	assert.Equal(t, "gamma", name)

	mock.ExpectExec(regexp.QuoteMeta("UPDATE dual SET name = :1 WHERE id = :2")).
		WithArgs("delta", 11).
		WillReturnResult(sqlmock.NewResult(0, 1))

	_, err = trx.Exec(context.Background(), "UPDATE dual SET name = :1 WHERE id = :2", "delta", 11)
	require.NoError(t, err)

	mock.ExpectPrepare(regexp.QuoteMeta(insertQuery))
	mock.ExpectExec(regexp.QuoteMeta(insertQuery)).
		WithArgs(12, "epsilon").
		WillReturnResult(sqlmock.NewResult(1, 1))

	stmt, err := trx.Prepare(context.Background(), insertQuery)
	require.NoError(t, err)
	_, err = stmt.Exec(context.Background(), 12, "epsilon")
	require.NoError(t, err)
	require.NoError(t, stmt.Close())

	mock.ExpectCommit()
	require.NoError(t, trx.Commit())
}

func TestOracleTransactionPrepareError(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, mock.ExpectationsWereMet()) })
	t.Cleanup(func() { _ = db.Close() })

	mock.ExpectBegin()
	nativeTx, err := db.BeginTx(context.Background(), nil)
	require.NoError(t, err)
	trx := &Transaction{tx: nativeTx}

	prepareErr := errors.New("prepare failed")
	mock.ExpectPrepare(regexp.QuoteMeta("INSERT INTO fail(id) VALUES (:1)")).
		WillReturnError(prepareErr)

	stmt, err := trx.Prepare(context.Background(), "INSERT INTO fail(id) VALUES (:1)")
	assert.Nil(t, stmt)
	assert.Error(t, err)
	assert.ErrorIs(t, err, prepareErr)

	mock.ExpectRollback()
	require.NoError(t, trx.Rollback())
}

func TestConnectionCreateMigrationTableSecondError(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()

	c := &Connection{db: db, logger: logger.New("debug", true)}

	ctx := context.Background()

	// Mock the first PL/SQL block to succeed, second to fail
	mock.ExpectExec(`BEGIN`).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(`BEGIN`).
		WillReturnError(sql.ErrTxDone)

	err = c.CreateMigrationTable(ctx)
	assert.Error(t, err)

	// Verify all expectations were met
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestConnectionQueryOperationsErrorHandling(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()

	c := &Connection{db: db, logger: logger.New("debug", true)}
	ctx := context.Background()

	// Test Query error
	mock.ExpectQuery("SELECT").WillReturnError(sql.ErrConnDone)
	rows, err := c.Query(ctx, "SELECT * FROM test")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "sql: connection is already closed")
	assert.Nil(t, rows)

	// Test QueryRow (doesn't return error, but we can test it executes)
	countRows := sqlmock.NewRows([]string{"count"}).AddRow(42)
	mock.ExpectQuery("SELECT COUNT").WillReturnRows(countRows)
	row := c.QueryRow(ctx, "SELECT COUNT(*) FROM test")
	assert.NotNil(t, row)

	// Test Prepare error
	mock.ExpectPrepare("INVALID SQL").WillReturnError(sql.ErrTxDone)
	_, err = c.Prepare(ctx, "INVALID SQL")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "sql: transaction has already been committed or rolled back")

	// Verify all expectations were met
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestConnectionTransactionOperationsErrorHandling(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()

	c := &Connection{db: db, logger: logger.New("debug", true)}
	ctx := context.Background()

	// Test Begin error
	mock.ExpectBegin().WillReturnError(sql.ErrConnDone)
	_, err = c.Begin(ctx)
	assert.Error(t, err)

	// Test BeginTx error
	mock.ExpectBegin().WillReturnError(sql.ErrTxDone)
	_, err = c.BeginTx(ctx, &sql.TxOptions{})
	assert.Error(t, err)

	// Test successful Prepare error in statement
	mock.ExpectPrepare("SELECT").WillReturnError(sql.ErrConnDone)
	_, err = c.Prepare(ctx, "SELECT * FROM test")
	assert.Error(t, err)

	// Verify all expectations were met
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestConnectionMetadata(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()
	mock.ExpectClose()

	c := &Connection{db: db, logger: logger.New("disabled", true)}

	assert.Equal(t, "oracle", c.DatabaseType())
	assert.Equal(t, "FLYWAY_SCHEMA_HISTORY", c.GetMigrationTable())
	assert.NoError(t, c.Close())
}
