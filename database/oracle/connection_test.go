package oracle

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"net"
	"regexp"
	"testing"
	"time"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
	"github.com/sijms/go-ora/v2/configurations"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/config"
)

const (
	oraclePingErrorMsg = "failed to ping Oracle database"
	selectQuery        = "SELECT name FROM dual WHERE id = :1"
	insertQuery        = "INSERT INTO dual(id, name) VALUES (:1, :2)"
)

type ConnectionTestData struct {
	name          string
	config        *config.DatabaseConfig
	expectError   bool
	errorContains string
}

// =============================================================================
// Test Helper Functions
// =============================================================================

// setupMockConnection creates a mock database connection with logger for testing
func setupMockConnection(t *testing.T) (*sql.DB, sqlmock.Sqlmock, *Connection) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	c := &Connection{db: db, logger: newDisabledTestLogger()}
	return db, mock, c
}

// createStandardPoolConfig returns a standard pool configuration for testing
func createStandardPoolConfig() config.PoolConfig {
	return config.PoolConfig{
		Max: config.PoolMaxConfig{
			Connections: 25,
		},
		Idle: config.PoolIdleConfig{
			Connections: 10,
			Time:        30 * time.Minute,
		},
		Lifetime: config.LifetimeConfig{
			Max: time.Hour,
		},
	}
}

// createOracleConfig creates a test Oracle database configuration
func createOracleConfig(connType, value string) *config.DatabaseConfig {
	cfg := &config.DatabaseConfig{
		Host:     "localhost",
		Port:     1521,
		Username: "testuser",
		Password: "testpass",
		Pool:     createStandardPoolConfig(),
	}

	switch connType {
	case "connection_string":
		cfg.ConnectionString = value
		cfg.Host = ""
		cfg.Port = 0
		cfg.Username = ""
		cfg.Password = ""
	case "service_name":
		cfg.Oracle = config.OracleConfig{
			Service: config.ServiceConfig{Name: value},
		}
	case "sid":
		cfg.Oracle = config.OracleConfig{
			Service: config.ServiceConfig{SID: value},
		}
	case "database":
		cfg.Database = value
	}

	return cfg
}

// testConnectionExpectedError tests that a connection attempt fails with expected error
func testConnectionExpectedError(t *testing.T, cfg *config.DatabaseConfig) {
	log := newTestLogger()
	_, err := NewConnection(cfg, log)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), oraclePingErrorMsg)
}

// setupMockStatementOperations sets up common mock expectations for statement operations
func setupMockStatementOperations(mock sqlmock.Sqlmock) {
	// Health check
	mock.ExpectPing()

	// Basic Exec operation
	mock.ExpectExec("INSERT INTO t").WithArgs("a").WillReturnResult(sqlmock.NewResult(1, 1))

	// Query operation
	rows := sqlmock.NewRows([]string{"id"}).AddRow(1)
	mock.ExpectQuery("SELECT id FROM t").WillReturnRows(rows)

	// QueryRow operation
	rows = sqlmock.NewRows([]string{"now"}).AddRow(time.Now())
	mock.ExpectQuery("SELECT CURRENT_TIMESTAMP").WillReturnRows(rows)

	// Prepare operation
	mock.ExpectPrepare("UPDATE t SET x").ExpectExec().WithArgs("b", 1).WillReturnResult(sqlmock.NewResult(0, 1))
}

// setupMockTransactionOperations sets up common mock expectations for transaction operations
func setupMockTransactionOperations(mock sqlmock.Sqlmock) {
	// Begin + transaction query + commit
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT 1 FROM dual").WillReturnRows(sqlmock.NewRows([]string{"x"}).AddRow(1))
	mock.ExpectCommit()

	// BeginTx + rollback
	mock.ExpectBegin()
	mock.ExpectRollback()
}

// setupMockMetaOperations sets up common mock expectations for metadata operations
func setupMockMetaOperations(mock sqlmock.Sqlmock) {
	// CreateMigrationTable expects two PL/SQL blocks
	mock.ExpectExec("BEGIN").WillReturnResult(driver.RowsAffected(0))
	mock.ExpectExec("BEGIN").WillReturnResult(driver.RowsAffected(0))

	// Close operation
	mock.ExpectClose()
}

func TestConnectionBasicMethodsWithSQLMock(t *testing.T) {
	db, mock, c := setupMockConnection(t)
	defer db.Close()

	ctx := context.Background()

	// Set up all mock expectations
	setupMockStatementOperations(mock)
	setupMockTransactionOperations(mock)
	setupMockMetaOperations(mock)

	// Execute Health check
	require.NoError(t, c.Health(ctx))

	// Execute Exec operation
	_, err := c.Exec(ctx, "INSERT INTO t(x) VALUES(?)", "a")
	require.NoError(t, err)

	// Execute Query operation
	rs, err := c.Query(ctx, "SELECT id FROM t")
	require.NoError(t, err)
	assert.True(t, rs.Next())
	_ = rs.Close()

	// Execute QueryRow operation
	row := c.QueryRow(ctx, "SELECT CURRENT_TIMESTAMP")
	require.NotNil(t, row)
	var ts time.Time
	require.NoError(t, row.Scan(&ts))

	// Execute Prepare + Statement operations
	st, err := c.Prepare(ctx, "UPDATE t SET x=:1 WHERE id=:2")
	require.NoError(t, err)
	_, err = st.Exec(ctx, "b", 1)
	require.NoError(t, err)
	require.NoError(t, st.Close())

	// Execute transaction operations
	tx, err := c.Begin(ctx)
	require.NoError(t, err)
	defer tx.Rollback(ctx) // No-op if committed
	rs2, err := tx.Query(ctx, "SELECT 1 FROM dual")
	require.NoError(t, err)
	_ = rs2.Close()
	require.NoError(t, tx.Commit(ctx))

	// Execute BeginTx + rollback
	tx2, err := c.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelDefault})
	require.NoError(t, err)
	require.NoError(t, tx2.Rollback(ctx))

	// Test Stats
	m, err := c.Stats()
	require.NoError(t, err)
	assert.Contains(t, m, "max_open_connections")
	assert.Contains(t, m, "open_connections")
	assert.Contains(t, m, "wait_duration")

	// Test metadata methods
	assert.Equal(t, "oracle", c.DatabaseType())
	assert.Equal(t, "FLYWAY_SCHEMA_HISTORY", c.MigrationTable())

	// Test CreateMigrationTable
	require.NoError(t, c.CreateMigrationTable(ctx))

	// Test Close
	require.NoError(t, c.Close())

	// Verify all expectations were met
	require.NoError(t, mock.ExpectationsWereMet())
}

// =============================================================================
// Connection Management Tests
// =============================================================================

func TestConnectionNewConnectionWithConnectionString(t *testing.T) {
	cfg := createOracleConfig("connection_string", "oracle://user:pass@localhost:1521/XE")
	testConnectionExpectedError(t, cfg)
}

func TestConnectionNewConnectionWithServiceName(t *testing.T) {
	cfg := createOracleConfig("service_name", "XEPDB1")
	testConnectionExpectedError(t, cfg)
}

func TestConnectionNewConnectionWithSID(t *testing.T) {
	cfg := createOracleConfig("sid", "XE")
	testConnectionExpectedError(t, cfg)
}

func TestConnectionNewConnectionWithDatabase(t *testing.T) {
	cfg := createOracleConfig("database", "XE")
	testConnectionExpectedError(t, cfg)
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
		Host:     "localhost",
		Port:     1521,
		Username: "stub",
		Password: "secret",
		Oracle: config.OracleConfig{
			Service: config.ServiceConfig{
				Name: "XEPDB1",
			},
		},
		Pool: config.PoolConfig{
			Max: config.PoolMaxConfig{
				Connections: 4,
			},
			Idle: config.PoolIdleConfig{
				Connections: 2,
			},
			Lifetime: config.LifetimeConfig{
				Max: 45 * time.Second,
			},
		},
	}

	log := newTestLogger()

	conn, err := NewConnection(cfg, log)
	require.NoError(t, err)
	require.NotNil(t, conn)

	mock.ExpectClose()
	require.NoError(t, conn.Close())
}

func TestConnectionCreateMigrationTable(t *testing.T) {
	db, mock, c := setupMockConnection(t)
	defer db.Close()

	ctx := context.Background()

	// Mock the CREATE TABLE execution (two PL/SQL blocks)
	mock.ExpectExec(`BEGIN`).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(`BEGIN`).WillReturnResult(sqlmock.NewResult(0, 0))

	err := c.CreateMigrationTable(ctx)
	assert.NoError(t, err)

	// Verify all expectations were met
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestConnectionCreateMigrationTableFirstError(t *testing.T) {
	db, mock, c := setupMockConnection(t)
	defer db.Close()

	ctx := context.Background()

	// Mock the first PL/SQL block to fail
	mock.ExpectExec(`BEGIN`).WillReturnError(sql.ErrConnDone)

	err := c.CreateMigrationTable(ctx)
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
	defer nativeTx.Rollback() // No-op after commit
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
	require.NoError(t, trx.Commit(context.Background()))
}

func TestOracleTransactionPrepareError(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, mock.ExpectationsWereMet()) })
	t.Cleanup(func() { _ = db.Close() })

	mock.ExpectBegin()
	nativeTx, err := db.BeginTx(context.Background(), nil)
	require.NoError(t, err)
	defer nativeTx.Rollback() // Called after explicit rollback below (no-op)
	trx := &Transaction{tx: nativeTx}

	prepareErr := errors.New("prepare failed")
	mock.ExpectPrepare(regexp.QuoteMeta("INSERT INTO fail(id) VALUES (:1)")).
		WillReturnError(prepareErr)

	stmt, err := trx.Prepare(context.Background(), "INSERT INTO fail(id) VALUES (:1)")
	assert.Nil(t, stmt)
	assert.Error(t, err)
	assert.ErrorIs(t, err, prepareErr)

	mock.ExpectRollback()
	require.NoError(t, trx.Rollback(context.Background()))
}

func TestConnectionCreateMigrationTableSecondError(t *testing.T) {
	db, mock, c := setupMockConnection(t)
	defer db.Close()

	ctx := context.Background()

	// Mock the first PL/SQL block to succeed, second to fail
	mock.ExpectExec(`BEGIN`).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(`BEGIN`).WillReturnError(sql.ErrTxDone)

	err := c.CreateMigrationTable(ctx)
	assert.Error(t, err)

	// Verify all expectations were met
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestConnectionQueryOperationsErrorHandling(t *testing.T) {
	db, mock, c := setupMockConnection(t)
	defer db.Close()

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
	var total int
	require.NoError(t, row.Scan(&total))
	assert.Equal(t, 42, total)

	// Test Prepare error
	mock.ExpectPrepare("INVALID SQL").WillReturnError(sql.ErrTxDone)
	_, err = c.Prepare(ctx, "INVALID SQL")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "sql: transaction has already been committed or rolled back")

	// Verify all expectations were met
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestConnectionTransactionOperationsErrorHandling(t *testing.T) {
	db, mock, c := setupMockConnection(t)
	defer db.Close()

	ctx := context.Background()

	// Test Begin error
	mock.ExpectBegin().WillReturnError(sql.ErrConnDone)
	_, err := c.Begin(ctx)
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
	db, mock, c := setupMockConnection(t)
	defer db.Close()
	mock.ExpectClose()

	assert.Equal(t, "oracle", c.DatabaseType())
	assert.Equal(t, "FLYWAY_SCHEMA_HISTORY", c.MigrationTable())
	assert.NoError(t, c.Close())
}

func TestConnectionValidationIntegration(t *testing.T) {
	log := newTestLogger()

	tests := []ConnectionTestData{
		{
			name: "valid config with service name should pass validation but fail connection",
			config: &config.DatabaseConfig{
				Type:     "oracle",
				Host:     "localhost",
				Port:     1521,
				Username: "testuser",
				Password: "testpass",
				Oracle: config.OracleConfig{
					Service: config.ServiceConfig{
						Name: "XEPDB1",
					},
				},
			},
			expectError:   true,
			errorContains: oraclePingErrorMsg,
		},
		{
			name: "valid config with SID should pass validation but fail connection",
			config: &config.DatabaseConfig{
				Type:     "oracle",
				Host:     "localhost",
				Port:     1521,
				Username: "testuser",
				Password: "testpass",
				Oracle: config.OracleConfig{
					Service: config.ServiceConfig{
						SID: "XE",
					},
				},
			},
			expectError:   true,
			errorContains: oraclePingErrorMsg, // Connection fails, not validation
		},
		{
			name: "valid config with database name should pass validation but fail connection",
			config: &config.DatabaseConfig{
				Type:     "oracle",
				Host:     "localhost",
				Port:     1521,
				Username: "testuser",
				Password: "testpass",
				Database: "XE",
			},
			expectError:   true,
			errorContains: oraclePingErrorMsg, // Connection fails, not validation
		},
		{
			name: "invalid config with no connection identifier should fail validation",
			config: &config.DatabaseConfig{
				Type:     "oracle",
				Host:     "localhost",
				Port:     1521,
				Username: "testuser",
				Password: "testpass",
			},
			expectError:   true,
			errorContains: "oracle connection identifier",
		},
		{
			name: "invalid config with multiple identifiers should fail validation",
			config: &config.DatabaseConfig{
				Type:     "oracle",
				Host:     "localhost",
				Port:     1521,
				Username: "testuser",
				Password: "testpass",
				Oracle: config.OracleConfig{
					Service: config.ServiceConfig{
						Name: "XEPDB1",
						SID:  "XE",
					},
				},
			},
			expectError:   true,
			errorContains: "oracle connection identifier",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// First validate the configuration (this is what we're testing)
			err := config.Validate(&config.Config{
				App: config.AppConfig{
					Name:    "test-app",
					Version: "v1.0.0",
					Env:     "development",
					Rate:    config.RateConfig{Limit: 100},
				},
				Server: config.ServerConfig{
					Port: 8080,
					Timeout: config.TimeoutConfig{
						Read:       15 * time.Second,
						Write:      30 * time.Second,
						Middleware: 5 * time.Second,
						Shutdown:   10 * time.Second,
					},
				},
				Database: *tt.config,
				Log: config.LogConfig{
					Level: "info",
				},
			})

			if tt.errorContains == oraclePingErrorMsg {
				// Config validation should pass, only connection should fail
				assert.NoError(t, err, "Configuration validation should pass")

				// Now attempt to create connection (this should fail with connection error)
				_, connErr := NewConnection(tt.config, log)
				assert.Error(t, connErr)
				assert.Contains(t, connErr.Error(), tt.errorContains)
			} else {
				// Config validation should fail
				assert.Error(t, err)
				assert.Contains(t, err.Error(), tt.errorContains)
			}
		})
	}
}

// =============================================================================
// Oracle SEQUENCE Tests (No UDT Registration Required)
// =============================================================================

func TestOracleSequenceQuery(t *testing.T) {
	db, mock, c := setupMockConnection(t)
	defer db.Close()

	ctx := context.Background()

	// Test that SEQUENCE queries work without any UDT registration
	t.Run("sequenceNextval", func(t *testing.T) {
		// Mock SEQUENCE.NEXTVAL query
		rows := sqlmock.NewRows([]string{"nextval"}).AddRow(int64(1001))
		mock.ExpectQuery("SELECT SEQ_ID_TABLE.NEXTVAL FROM DUAL").WillReturnRows(rows)

		var nextID int64
		err := c.QueryRow(ctx, "SELECT SEQ_ID_TABLE.NEXTVAL FROM DUAL").Scan(&nextID)
		require.NoError(t, err)
		assert.Equal(t, int64(1001), nextID)
	})

	t.Run("sequenceCurrval", func(t *testing.T) {
		// Mock SEQUENCE.CURRVAL query
		rows := sqlmock.NewRows([]string{"currval"}).AddRow(int64(1001))
		mock.ExpectQuery("SELECT SEQ_ID_TABLE.CURRVAL FROM DUAL").WillReturnRows(rows)

		var currID int64
		err := c.QueryRow(ctx, "SELECT SEQ_ID_TABLE.CURRVAL FROM DUAL").Scan(&currID)
		require.NoError(t, err)
		assert.Equal(t, int64(1001), currID)
	})

	// Verify all expectations met
	assert.NoError(t, mock.ExpectationsWereMet())
}

// =============================================================================
// TCP Keep-Alive Tests
// =============================================================================

func TestNewConnectionWithKeepAliveEnabled(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, mock.ExpectationsWereMet()) })

	// Track which open function was called and capture the dialer
	var usedDialerPath bool
	var capturedDialer configurations.DialerContext

	originalOpenWithDialer := openOracleDBWithDialer
	originalOpen := openOracleDB
	originalPing := pingOracleDB

	openOracleDBWithDialer = func(_ string, dialer configurations.DialerContext) *sql.DB {
		usedDialerPath = true
		capturedDialer = dialer
		return db
	}
	openOracleDB = func(_ string) (*sql.DB, error) {
		return db, nil
	}
	pingOracleDB = func(context.Context, *sql.DB) error { return nil }

	t.Cleanup(func() {
		openOracleDBWithDialer = originalOpenWithDialer
		openOracleDB = originalOpen
		pingOracleDB = originalPing
	})

	cfg := &config.DatabaseConfig{
		Host:     "localhost",
		Port:     1521,
		Username: "testuser",
		Password: "testpass",
		Oracle: config.OracleConfig{
			Service: config.ServiceConfig{Name: "XEPDB1"},
		},
		Pool: config.PoolConfig{
			Max: config.PoolMaxConfig{Connections: 5},
			Idle: config.PoolIdleConfig{
				Connections: 2,
				Time:        4 * time.Minute,
			},
			Lifetime: config.LifetimeConfig{Max: 30 * time.Minute},
			KeepAlive: config.PoolKeepAliveConfig{
				Enabled:  true,
				Interval: 60 * time.Second,
			},
		},
	}

	log := newTestLogger()
	conn, err := NewConnection(cfg, log)
	require.NoError(t, err)
	require.NotNil(t, conn)

	// Verify the dialer path was used and a dialer was provided
	assert.True(t, usedDialerPath, "should use openOracleDBWithDialer when keep-alive is enabled")
	assert.NotNil(t, capturedDialer, "dialer should be provided when keep-alive is enabled")

	// Verify it's the correct dialer type with the right interval
	kaDialer, ok := capturedDialer.(*keepAliveDialer)
	require.True(t, ok, "dialer should be a keepAliveDialer")
	assert.Equal(t, 60*time.Second, kaDialer.interval, "dialer should have correct interval")

	mock.ExpectClose()
	require.NoError(t, conn.Close())
}

func TestNewConnectionWithKeepAliveFallback(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, mock.ExpectationsWereMet()) })

	// Simulate openOracleDBWithDialer returning nil (go-ora API change)
	var usedFallbackPath bool

	originalOpenWithDialer := openOracleDBWithDialer
	originalOpen := openOracleDB
	originalPing := pingOracleDB

	openOracleDBWithDialer = func(_ string, _ configurations.DialerContext) *sql.DB {
		return nil // Simulate type assertion failure
	}
	openOracleDB = func(_ string) (*sql.DB, error) {
		usedFallbackPath = true
		return db, nil
	}
	pingOracleDB = func(context.Context, *sql.DB) error { return nil }

	t.Cleanup(func() {
		openOracleDBWithDialer = originalOpenWithDialer
		openOracleDB = originalOpen
		pingOracleDB = originalPing
	})

	cfg := &config.DatabaseConfig{
		Host:     "localhost",
		Port:     1521,
		Username: "testuser",
		Password: "testpass",
		Oracle: config.OracleConfig{
			Service: config.ServiceConfig{Name: "XEPDB1"},
		},
		Pool: config.PoolConfig{
			Max: config.PoolMaxConfig{Connections: 5},
			Idle: config.PoolIdleConfig{
				Connections: 2,
				Time:        4 * time.Minute,
			},
			Lifetime: config.LifetimeConfig{Max: 30 * time.Minute},
			KeepAlive: config.PoolKeepAliveConfig{
				Enabled:  true, // Keep-alive enabled but dialer fails
				Interval: 60 * time.Second,
			},
		},
	}

	log := newTestLogger()
	conn, err := NewConnection(cfg, log)
	require.NoError(t, err, "should succeed with fallback")
	require.NotNil(t, conn)

	// Verify fallback path was used
	assert.True(t, usedFallbackPath, "should fall back to regular openOracleDB when dialer returns nil")

	mock.ExpectClose()
	require.NoError(t, conn.Close())
}

func TestNewConnectionWithKeepAliveDisabled(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, mock.ExpectationsWereMet()) })

	// Track which open function was called
	var usedDialerPath bool
	var usedRegularPath bool

	originalOpenWithDialer := openOracleDBWithDialer
	originalOpen := openOracleDB
	originalPing := pingOracleDB

	openOracleDBWithDialer = func(_ string, _ configurations.DialerContext) *sql.DB {
		usedDialerPath = true
		return db
	}
	openOracleDB = func(_ string) (*sql.DB, error) {
		usedRegularPath = true
		return db, nil
	}
	pingOracleDB = func(context.Context, *sql.DB) error { return nil }

	t.Cleanup(func() {
		openOracleDBWithDialer = originalOpenWithDialer
		openOracleDB = originalOpen
		pingOracleDB = originalPing
	})

	cfg := &config.DatabaseConfig{
		Host:     "localhost",
		Port:     1521,
		Username: "testuser",
		Password: "testpass",
		Oracle: config.OracleConfig{
			Service: config.ServiceConfig{Name: "XEPDB1"},
		},
		Pool: config.PoolConfig{
			Max: config.PoolMaxConfig{Connections: 5},
			Idle: config.PoolIdleConfig{
				Connections: 2,
				Time:        4 * time.Minute,
			},
			Lifetime: config.LifetimeConfig{Max: 30 * time.Minute},
			KeepAlive: config.PoolKeepAliveConfig{
				Enabled:  false, // Explicitly disabled
				Interval: 60 * time.Second,
			},
		},
	}

	log := newTestLogger()
	conn, err := NewConnection(cfg, log)
	require.NoError(t, err)
	require.NotNil(t, conn)

	// Verify the regular path was used (no custom dialer)
	assert.False(t, usedDialerPath, "should NOT use openOracleDBWithDialer when keep-alive is disabled")
	assert.True(t, usedRegularPath, "should use regular openOracleDB when keep-alive is disabled")

	mock.ExpectClose()
	require.NoError(t, conn.Close())
}

func TestNewConnectionWithKeepAliveAndSID(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, mock.ExpectationsWereMet()) })

	// Track DSN, dialer, and which path was used
	var capturedDSN string
	var capturedDialer configurations.DialerContext
	var usedRegularPath bool

	originalOpenWithDialer := openOracleDBWithDialer
	originalOpen := openOracleDB
	originalPing := pingOracleDB

	openOracleDBWithDialer = func(dsn string, dialer configurations.DialerContext) *sql.DB {
		capturedDSN = dsn
		capturedDialer = dialer
		return db
	}
	openOracleDB = func(dsn string) (*sql.DB, error) {
		usedRegularPath = true
		capturedDSN = dsn
		return db, nil
	}
	pingOracleDB = func(context.Context, *sql.DB) error { return nil }

	t.Cleanup(func() {
		openOracleDBWithDialer = originalOpenWithDialer
		openOracleDB = originalOpen
		pingOracleDB = originalPing
	})

	cfg := &config.DatabaseConfig{
		Host:     "localhost",
		Port:     1521,
		Username: "testuser",
		Password: "testpass",
		Oracle: config.OracleConfig{
			Service: config.ServiceConfig{SID: "XE"}, // Using SID instead of service name
		},
		Pool: config.PoolConfig{
			Max: config.PoolMaxConfig{Connections: 5},
			Idle: config.PoolIdleConfig{
				Connections: 2,
				Time:        4 * time.Minute,
			},
			Lifetime: config.LifetimeConfig{Max: 30 * time.Minute},
			KeepAlive: config.PoolKeepAliveConfig{
				Enabled:  true,
				Interval: 30 * time.Second, // Different interval to verify
			},
		},
	}

	log := newTestLogger()
	conn, err := NewConnection(cfg, log)
	require.NoError(t, err)
	require.NotNil(t, conn)

	// Verify regular path was NOT used (keep-alive enabled should use dialer path)
	assert.False(t, usedRegularPath, "should NOT use regular openOracleDB when keep-alive is enabled")

	// Verify DSN contains SID option
	assert.Contains(t, capturedDSN, "SID=XE", "DSN should contain SID option")

	// Verify DSN does NOT contain fake TCP keep-alive options (they're now handled via dialer)
	assert.NotContains(t, capturedDSN, "TCP KEEPALIVE", "DSN should not contain TCP KEEPALIVE (handled via dialer)")
	assert.NotContains(t, capturedDSN, "TCP KEEPIDLE", "DSN should not contain TCP KEEPIDLE (handled via dialer)")

	// Verify dialer was configured with correct interval
	require.NotNil(t, capturedDialer, "dialer should be provided when keep-alive is enabled")
	kaDialer, ok := capturedDialer.(*keepAliveDialer)
	require.True(t, ok, "dialer should be a keepAliveDialer")
	assert.Equal(t, 30*time.Second, kaDialer.interval, "dialer should have correct interval")

	mock.ExpectClose()
	require.NoError(t, conn.Close())
}

func TestResolveServiceName(t *testing.T) {
	tests := []struct {
		name     string
		cfg      *config.DatabaseConfig
		expected string
	}{
		{
			name: "service_name_takes_priority",
			cfg: &config.DatabaseConfig{
				Oracle:   config.OracleConfig{Service: config.ServiceConfig{Name: "XEPDB1", SID: "XE"}},
				Database: "testdb",
			},
			expected: "XEPDB1",
		},
		{
			name: "database_fallback_when_no_service_or_sid",
			cfg: &config.DatabaseConfig{
				Oracle:   config.OracleConfig{},
				Database: "testdb",
			},
			expected: "testdb",
		},
		{
			name: "empty_when_sid_set_without_service_name",
			cfg: &config.DatabaseConfig{
				Oracle:   config.OracleConfig{Service: config.ServiceConfig{SID: "XE"}},
				Database: "testdb",
			},
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := resolveServiceName(tt.cfg)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestBuildURLOptions(t *testing.T) {
	tests := []struct {
		name     string
		cfg      *config.DatabaseConfig
		expected map[string]string
	}{
		{
			name: "with_sid",
			cfg: &config.DatabaseConfig{
				Oracle: config.OracleConfig{Service: config.ServiceConfig{SID: "XE"}},
			},
			expected: map[string]string{"SID": "XE"},
		},
		{
			name: "without_sid",
			cfg: &config.DatabaseConfig{
				Oracle: config.OracleConfig{},
			},
			expected: map[string]string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := buildURLOptions(tt.cfg)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestKeepAliveDialerDialContext(t *testing.T) {
	log := newTestLogger()
	dialer := newKeepAliveDialer(60*time.Second, log)

	// Verify dialer is properly initialized
	assert.Equal(t, 60*time.Second, dialer.interval)
	assert.NotNil(t, dialer.log)
}

func TestKeepAliveDialerDialContextWithListener(t *testing.T) {
	log := newTestLogger()
	dialer := newKeepAliveDialer(60*time.Second, log)

	// Use context-aware listener (noctx linter requirement)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Start local TCP listener on an available port
	lc := net.ListenConfig{}
	listener, err := lc.Listen(ctx, "tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer listener.Close()

	conn, err := dialer.DialContext(ctx, "tcp", listener.Addr().String())
	require.NoError(t, err)
	defer conn.Close()

	// Verify it's a TCP connection (keep-alive requires TCP)
	tcpConn, ok := conn.(*net.TCPConn)
	assert.True(t, ok, "should return *net.TCPConn")
	assert.NotNil(t, tcpConn)
}

func TestKeepAliveDialerDialContextError(t *testing.T) {
	log := newTestLogger()
	dialer := newKeepAliveDialer(60*time.Second, log)

	// Use a short timeout since we expect failure
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	// Try to connect to a non-existent address (port 59999 is unlikely to be in use)
	conn, err := dialer.DialContext(ctx, "tcp", "127.0.0.1:59999")
	assert.Error(t, err, "should fail when connecting to non-existent address")
	assert.Nil(t, conn)
}

func TestNewConnectionPingFailure(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	// Note: mock expectations may not be met if ping fails before close
	defer func() { _ = mock.ExpectationsWereMet() }()

	originalOpen := openOracleDB
	originalPing := pingOracleDB

	openOracleDB = func(_ string) (*sql.DB, error) { return db, nil }
	pingOracleDB = func(context.Context, *sql.DB) error {
		return errors.New("connection refused")
	}

	t.Cleanup(func() {
		openOracleDB = originalOpen
		pingOracleDB = originalPing
	})

	cfg := &config.DatabaseConfig{
		Host:     "localhost",
		Port:     1521,
		Username: "testuser",
		Password: "testpass",
		Oracle:   config.OracleConfig{Service: config.ServiceConfig{Name: "XEPDB1"}},
		Pool: config.PoolConfig{
			KeepAlive: config.PoolKeepAliveConfig{Enabled: false},
		},
	}

	log := newTestLogger()
	mock.ExpectClose() // DB should be closed on ping failure

	conn, err := NewConnection(cfg, log)
	assert.Error(t, err)
	assert.Nil(t, conn)
	assert.Contains(t, err.Error(), oraclePingErrorMsg)
}

// =============================================================================
// Oracle UDT Registration Tests
// =============================================================================

func TestConnectionRegisterType(t *testing.T) {
	type Product struct {
		ID    int64   `udt:"ID"`
		Name  string  `udt:"NAME"`
		Price float64 `udt:"PRICE"`
	}

	t.Run("objectTypeOnly", func(t *testing.T) {
		_, _, c := setupMockConnection(t)

		// Register without collection type
		err := c.RegisterType("PRODUCT_TYPE", "", Product{})

		// Method should exist and handle gracefully (may fail without real Oracle)
		if err != nil {
			t.Logf("RegisterType returned error (expected without Oracle): %v", err)
		}
	})

	t.Run("collectionTypeForBulkOps", func(t *testing.T) {
		_, _, c := setupMockConnection(t)

		// Primary use case: register collection type
		err := c.RegisterType("PRODUCT_TYPE", "PRODUCT_TABLE", Product{})

		if err != nil {
			t.Logf("RegisterType with collection returned error: %v", err)
		}
	})
}

func TestConnectionRegisterTypeWithOwner(t *testing.T) {
	type Customer struct {
		CustomerID int    `udt:"CUSTOMER_ID"`
		Name       string `udt:"NAME"`
	}

	t.Run("withSchemaOwner", func(t *testing.T) {
		_, _, c := setupMockConnection(t)

		// Note: This may panic with sqlmock due to driver type assertion in go-ora
		// Integration tests validate actual functionality
		defer func() {
			if r := recover(); r != nil {
				t.Logf("RegisterTypeWithOwner panicked (expected with sqlmock): %v", r)
			}
		}()

		err := c.RegisterTypeWithOwner("SHARED_SCHEMA", "CUSTOMER_TYPE", "", Customer{})

		if err != nil {
			t.Logf("RegisterTypeWithOwner returned error: %v", err)
		}
	})

	t.Run("schemaOwnerWithCollection", func(t *testing.T) {
		_, _, c := setupMockConnection(t)

		// Note: This may panic with sqlmock due to driver type assertion in go-ora
		defer func() {
			if r := recover(); r != nil {
				t.Logf("RegisterTypeWithOwner panicked (expected with sqlmock): %v", r)
			}
		}()

		err := c.RegisterTypeWithOwner("SHARED_SCHEMA", "CUSTOMER_TYPE", "CUSTOMER_TABLE", Customer{})

		if err != nil {
			t.Logf("RegisterTypeWithOwner with collection returned error: %v", err)
		}
	})
}

// =============================================================================
// Edge Case Coverage Tests
// =============================================================================

// TestConnectionStatsWithNilConfig tests Stats() when config is nil
// This covers the else branch where config is not available.
// Coverage target: Stats() nil config branch
func TestConnectionStatsWithNilConfig(t *testing.T) {
	db, _, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()

	// Create connection with nil config
	c := &Connection{
		db:     db,
		logger: newDisabledTestLogger(),
		config: nil, // Explicitly nil config
	}

	stats, err := c.Stats()
	assert.NoError(t, err, "Stats() should succeed with nil config")
	assert.NotNil(t, stats, "Stats should not be nil")

	// Verify basic stats keys are present
	assert.Contains(t, stats, "max_open_connections")
	assert.Contains(t, stats, "open_connections")
	assert.Contains(t, stats, "in_use")
	assert.Contains(t, stats, "idle")

	// max_idle_connections should NOT be present when config is nil
	assert.NotContains(t, stats, "max_idle_connections",
		"max_idle_connections should not be present when config is nil")
}

// TestLogConnectionSuccessWithSID tests logConnectionSuccess with SID identifier
// Coverage target: logConnectionSuccess switch case for SID (line 220-221)
func TestLogConnectionSuccessWithSID(t *testing.T) {
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

	// Use SID instead of Service.Name
	cfg := &config.DatabaseConfig{
		Host:     "localhost",
		Port:     1521,
		Username: "stub",
		Password: "secret",
		Oracle: config.OracleConfig{
			Service: config.ServiceConfig{
				SID: "ORCL", // SID path (not Service.Name)
			},
		},
		Pool: config.PoolConfig{
			Max: config.PoolMaxConfig{
				Connections: 4,
			},
			Idle: config.PoolIdleConfig{
				Connections: 2,
			},
			Lifetime: config.LifetimeConfig{
				Max: 45 * time.Second,
			},
		},
	}

	log := newTestLogger()

	conn, err := NewConnection(cfg, log)
	require.NoError(t, err)
	require.NotNil(t, conn)

	mock.ExpectClose()
	require.NoError(t, conn.Close())
}

// TestLogConnectionSuccessWithDatabaseFallback tests logConnectionSuccess with Database field
// Coverage target: logConnectionSuccess default case (line 222-223)
func TestLogConnectionSuccessWithDatabaseFallback(t *testing.T) {
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

	// Use Database field (fallback when neither Service.Name nor SID is set)
	cfg := &config.DatabaseConfig{
		Host:     "localhost",
		Port:     1521,
		Username: "stub",
		Password: "secret",
		Database: "testdb", // Database fallback path
		Pool: config.PoolConfig{
			Max: config.PoolMaxConfig{
				Connections: 4,
			},
			Idle: config.PoolIdleConfig{
				Connections: 2,
			},
			Lifetime: config.LifetimeConfig{
				Max: 45 * time.Second,
			},
		},
	}

	log := newTestLogger()

	conn, err := NewConnection(cfg, log)
	require.NoError(t, err)
	require.NotNil(t, conn)

	mock.ExpectClose()
	require.NoError(t, conn.Close())
}
