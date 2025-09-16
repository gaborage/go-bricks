package postgresql

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"testing"
	"time"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/config"
	"github.com/gaborage/go-bricks/logger"
)

func TestConnection_BasicMethods_WithSQLMock(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()

	c := &Connection{db: db, logger: logger.New("disabled", true)}

	ctx := context.Background()

	// Health
	mock.ExpectPing()
	require.NoError(t, c.Health(ctx))

	// Exec
	mock.ExpectExec("INSERT INTO items").WithArgs("a").WillReturnResult(sqlmock.NewResult(1, 1))
	_, err = c.Exec(ctx, "INSERT INTO items(name) VALUES($1)", "a")
	require.NoError(t, err)

	// Query
	rows := sqlmock.NewRows([]string{"id", "name"}).AddRow(1, "x")
	mock.ExpectQuery("SELECT id, name FROM items").WillReturnRows(rows)
	rs, err := c.Query(ctx, "SELECT id, name FROM items")
	require.NoError(t, err)
	assert.True(t, rs.Next())
	_ = rs.Close()

	// QueryRow
	rows = sqlmock.NewRows([]string{"now"}).AddRow(time.Now())
	mock.ExpectQuery(`SELECT NOW\(\)`).WillReturnRows(rows)
	_ = c.QueryRow(ctx, "SELECT NOW()")

	// Prepare + Statement Exec
	mock.ExpectPrepare("UPDATE items SET name").ExpectExec().WithArgs("b", 1).WillReturnResult(sqlmock.NewResult(0, 1))
	st, err := c.Prepare(ctx, "UPDATE items SET name=$1 WHERE id=$2")
	require.NoError(t, err)
	_, err = st.Exec(ctx, "b", 1)
	require.NoError(t, err)
	require.NoError(t, st.Close())

	// Begin + Tx methods
	mock.ExpectBegin()
	mock.ExpectExec("DELETE FROM items").WithArgs(1).WillReturnResult(driver.RowsAffected(1))
	mock.ExpectCommit()
	tx, err := c.Begin(ctx)
	require.NoError(t, err)
	_, err = tx.Exec(ctx, "DELETE FROM items WHERE id=$1", 1)
	require.NoError(t, err)
	require.NoError(t, tx.Commit())

	// BeginTx + rollback
	mock.ExpectBegin()
	mock.ExpectRollback()
	opts := &sql.TxOptions{Isolation: sql.LevelDefault}
	tx2, err := c.BeginTx(ctx, opts)
	// ensure tx2 is non-nil and rollback works
	require.NoError(t, err)
	require.NoError(t, tx2.Rollback())

	// Stats
	m, err := c.Stats()
	require.NoError(t, err)
	assert.Contains(t, m, "max_open_connections")
	assert.Contains(t, m, "open_connections")
	assert.Contains(t, m, "wait_duration")

	// Close
	mock.ExpectClose()
	require.NoError(t, c.Close())

	// Verify all expectations
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestConnection_Metadata(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()
	mock.ExpectClose()

	c := &Connection{db: db, logger: logger.New("disabled", true)}

	assert.Equal(t, "postgresql", c.DatabaseType())
	assert.Equal(t, "flyway_schema_history", c.GetMigrationTable())
	assert.NoError(t, c.Close())
}

// =============================================================================
// Connection Management Tests
// =============================================================================

func TestConnection_NewConnection_WithConnectionString(t *testing.T) {
	// Test configuration with connection string
	cfg := &config.DatabaseConfig{
		ConnectionString: "postgres://user:pass@localhost/testdb?sslmode=disable",
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
	assert.Contains(t, err.Error(), "failed to ping PostgreSQL database")
}

func TestConnection_NewConnection_WithHostConfig(t *testing.T) {
	// Test configuration with individual host parameters
	cfg := &config.DatabaseConfig{
		Host:            "localhost",
		Port:            5432,
		Username:        "testuser",
		Password:        "testpass",
		Database:        "testdb",
		SSLMode:         "disable",
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
	assert.Contains(t, err.Error(), "failed to ping PostgreSQL database")
}

func TestConnection_NewConnection_InvalidConfig(t *testing.T) {
	// Test with invalid configuration that causes parsing to fail
	cfg := &config.DatabaseConfig{
		ConnectionString: "invalid-connection-string-format",
	}

	log := logger.New("debug", true)

	_, err := NewConnection(cfg, log)

	// Should fail at config parsing stage
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to parse PostgreSQL config")
}

func TestConnection_CreateMigrationTable(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()

	c := &Connection{db: db, logger: logger.New("debug", true)}

	ctx := context.Background()

	// Mock the CREATE TABLE execution
	mock.ExpectExec(`CREATE TABLE IF NOT EXISTS flyway_schema_history`).
		WillReturnResult(sqlmock.NewResult(0, 0))

	err = c.CreateMigrationTable(ctx)
	assert.NoError(t, err)

	// Verify all expectations were met
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestConnection_QueryOperations_ErrorHandling(t *testing.T) {
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

func TestConnection_TransactionOperations_ErrorHandling(t *testing.T) {
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
