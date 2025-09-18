package postgresql

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
	"github.com/jackc/pgx/v5"
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

func TestConnectionMetadata(t *testing.T) {
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

func TestConnectionNewConnectionWithConnectionString(t *testing.T) {
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

func TestConnectionNewConnectionWithHostConfig(t *testing.T) {
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

func TestConnectionNewConnectionWithHostConfigNoSSLMode(t *testing.T) {
	// Configuration without sslmode should omit the parameter from the DSN
	cfg := &config.DatabaseConfig{
		Host:            "localhost",
		Port:            5432,
		Username:        "testuser",
		Password:        "testpass",
		Database:        "testdb",
		MaxConns:        25,
		MaxIdleConns:    10,
		ConnMaxLifetime: time.Hour,
		ConnMaxIdleTime: 30 * time.Minute,
	}

	log := logger.New("debug", true)

	// This will fail because we're not connecting to a real database, but should parse successfully
	_, err := NewConnection(cfg, log)

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to ping PostgreSQL database")
}

func TestConnectionNewConnectionSuccess(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, mock.ExpectationsWereMet()) })

	originalOpen := openPostgresDB
	originalPing := pingPostgresDB
	openPostgresDB = func(*pgx.ConnConfig) *sql.DB { return db }
	pingPostgresDB = func(context.Context, *sql.DB) error { return nil }
	t.Cleanup(func() {
		openPostgresDB = originalOpen
		pingPostgresDB = originalPing
	})

	cfg := &config.DatabaseConfig{
		Host:            "localhost",
		Port:            5432,
		Username:        "stubuser",
		Password:        "stubpass",
		Database:        "stubdb",
		MaxConns:        5,
		MaxIdleConns:    2,
		ConnMaxLifetime: 30 * time.Second,
		ConnMaxIdleTime: 15 * time.Second,
	}

	log := logger.New("debug", true)

	conn, err := NewConnection(cfg, log)
	require.NoError(t, err)
	require.NotNil(t, conn)

	mock.ExpectClose()
	require.NoError(t, conn.Close())
}

func TestConnectionNewConnectionInvalidConfig(t *testing.T) {
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

func TestStatementQueryAndQueryRow(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, mock.ExpectationsWereMet()) })
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()

	mock.ExpectPrepare(regexp.QuoteMeta("SELECT id FROM widgets WHERE active = $1")).
		ExpectQuery().
		WithArgs(true).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(42))

	stmt, err := db.PrepareContext(ctx, "SELECT id FROM widgets WHERE active = $1")
	require.NoError(t, err)
	ps := &Statement{stmt: stmt}

	rows, err := ps.Query(ctx, true)
	require.NoError(t, err)
	require.True(t, rows.Next())
	rows.Close()
	require.NoError(t, ps.Close())

	mock.ExpectPrepare(regexp.QuoteMeta("SELECT name FROM widgets WHERE id = $1")).
		ExpectQuery().
		WithArgs(7).
		WillReturnRows(sqlmock.NewRows([]string{"name"}).AddRow("gizmo"))

	stmtRow, err := db.PrepareContext(ctx, "SELECT name FROM widgets WHERE id = $1")
	require.NoError(t, err)
	psRow := &Statement{stmt: stmtRow}

	row := psRow.QueryRow(ctx, 7)
	var name string
	require.NoError(t, row.Scan(&name))
	assert.Equal(t, "gizmo", name)
	require.NoError(t, psRow.Close())
}

func TestTransactionQueryPrepareAndExec(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, mock.ExpectationsWereMet()) })
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()

	mock.ExpectBegin()
	nativeTx, err := db.BeginTx(ctx, nil)
	require.NoError(t, err)
	trx := &Transaction{tx: nativeTx}

	mock.ExpectQuery(regexp.QuoteMeta("SELECT id FROM parts WHERE sku = $1")).
		WithArgs("A-1").
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(9))

	resultRows, err := trx.Query(ctx, "SELECT id FROM parts WHERE sku = $1", "A-1")
	require.NoError(t, err)
	resultRows.Close()

	mock.ExpectQuery(regexp.QuoteMeta("SELECT name FROM parts WHERE id = $1")).
		WithArgs(9).
		WillReturnRows(sqlmock.NewRows([]string{"name"}).AddRow("widget"))

	row := trx.QueryRow(ctx, "SELECT name FROM parts WHERE id = $1", 9)
	var partName string
	require.NoError(t, row.Scan(&partName))
	assert.Equal(t, "widget", partName)

	mock.ExpectExec(regexp.QuoteMeta("UPDATE parts SET name = $1 WHERE id = $2")).
		WithArgs("updated", 9).
		WillReturnResult(sqlmock.NewResult(0, 1))

	_, err = trx.Exec(ctx, "UPDATE parts SET name = $1 WHERE id = $2", "updated", 9)
	require.NoError(t, err)

	mock.ExpectPrepare(regexp.QuoteMeta("INSERT INTO parts(id, name) VALUES ($1, $2)"))
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO parts(id, name) VALUES ($1, $2)")).
		WithArgs(10, "new").
		WillReturnResult(sqlmock.NewResult(1, 1))

	stmt, err := trx.Prepare(ctx, "INSERT INTO parts(id, name) VALUES ($1, $2)")
	require.NoError(t, err)
	_, err = stmt.Exec(ctx, 10, "new")
	require.NoError(t, err)
	require.NoError(t, stmt.Close())

	mock.ExpectCommit()
	require.NoError(t, trx.Commit())
}

func TestTransactionPrepareError(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, mock.ExpectationsWereMet()) })
	t.Cleanup(func() { _ = db.Close() })

	mock.ExpectBegin()
	nativeTx, err := db.BeginTx(context.Background(), nil)
	require.NoError(t, err)
	trx := &Transaction{tx: nativeTx}

	prepareErr := errors.New("prepare failed")
	mock.ExpectPrepare(regexp.QuoteMeta("INSERT INTO fail(id) VALUES ($1)")).
		WillReturnError(prepareErr)

	stmt, err := trx.Prepare(context.Background(), "INSERT INTO fail(id) VALUES ($1)")
	assert.Nil(t, stmt)
	assert.Error(t, err)
	assert.ErrorIs(t, err, prepareErr)

	mock.ExpectRollback()
	require.NoError(t, trx.Rollback())
}

func TestConnectionCreateMigrationTable(t *testing.T) {
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

// =============================================================================
// quoteDSN Tests
// =============================================================================

func TestQuoteDSN(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		// Empty string should return ''
		{
			name:     "empty string",
			input:    "",
			expected: "''",
		},

		// Simple alphanumeric values should not be quoted
		{
			name:     "simple alphanumeric",
			input:    "user123",
			expected: "user123",
		},
		{
			name:     "alphanumeric with underscore",
			input:    "my_user",
			expected: "my_user",
		},
		{
			name:     "alphanumeric with dash",
			input:    "test-user",
			expected: "test-user",
		},
		{
			name:     "alphanumeric with dot",
			input:    "user.name",
			expected: "user.name",
		},
		{
			name:     "all allowed characters",
			input:    "abcXYZ123._-",
			expected: "abcXYZ123._-",
		},

		// Values with spaces need quoting
		{
			name:     "value with space",
			input:    "my user",
			expected: "'my user'",
		},
		{
			name:     "value with multiple spaces",
			input:    "user with spaces",
			expected: "'user with spaces'",
		},

		// Values with special characters need quoting
		{
			name:     "value with at symbol",
			input:    "user@domain.com",
			expected: "'user@domain.com'",
		},
		{
			name:     "value with colon",
			input:    "user:password",
			expected: "'user:password'",
		},
		{
			name:     "value with slash",
			input:    "path/to/database",
			expected: "'path/to/database'",
		},
		{
			name:     "value with equals",
			input:    "key=value",
			expected: "'key=value'",
		},
		{
			name:     "value with semicolon",
			input:    "param;value",
			expected: "'param;value'",
		},

		// Values with backslashes need escaping
		{
			name:     "value with single backslash",
			input:    "path\\to\\database",
			expected: "'path\\\\to\\\\database'",
		},
		{
			name:     "value with double backslash",
			input:    "path\\\\server",
			expected: "'path\\\\\\\\server'",
		},

		// Values with single quotes need escaping
		{
			name:     "value with single quote",
			input:    "user's data",
			expected: "'user\\'s data'",
		},
		{
			name:     "value with multiple single quotes",
			input:    "it's a 'test'",
			expected: "'it\\'s a \\'test\\''",
		},

		// Values with both backslashes and single quotes
		{
			name:     "value with backslash and quote",
			input:    "path\\user's folder",
			expected: "'path\\\\user\\'s folder'",
		},

		// Complex real-world examples
		{
			name:     "complex password with special chars",
			input:    "P@ssw0rd!#$",
			expected: "'P@ssw0rd!#$'",
		},
		{
			name:     "windows path with spaces",
			input:    "C:\\Program Files\\PostgreSQL",
			expected: "'C:\\\\Program Files\\\\PostgreSQL'",
		},
		{
			name:     "database name with hyphens",
			input:    "my-app-database",
			expected: "my-app-database",
		},
		{
			name:     "complex connection string part",
			input:    "host=localhost port=5432",
			expected: "'host=localhost port=5432'",
		},

		// Edge cases
		{
			name:     "only spaces",
			input:    "   ",
			expected: "'   '",
		},
		{
			name:     "only special characters",
			input:    "@#$%^&*()",
			expected: "'@#$%^&*()'",
		},
		{
			name:     "single character needing quote",
			input:    "@",
			expected: "'@'",
		},
		{
			name:     "single character not needing quote",
			input:    "a",
			expected: "a",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := quoteDSN(tt.input)
			assert.Equal(t, tt.expected, result, "quoteDSN(%q) = %q, want %q", tt.input, result, tt.expected)
		})
	}
}
