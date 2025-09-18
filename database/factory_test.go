package database

import (
	"context"
	"database/sql"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/config"
	"github.com/gaborage/go-bricks/internal/database"
	"github.com/gaborage/go-bricks/logger"
)

func TestValidateDatabaseTypeSuccess(t *testing.T) {
	tests := []struct {
		name   string
		dbType string
	}{
		{
			name:   "postgresql",
			dbType: "postgresql",
		},
		{
			name:   "oracle",
			dbType: "oracle",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateDatabaseType(tt.dbType)
			assert.NoError(t, err)
		})
	}
}

func TestValidateDatabaseTypeFailure(t *testing.T) {
	tests := []struct {
		name          string
		dbType        string
		expectedError string
	}{
		{
			name:          "unsupported_mysql",
			dbType:        "mysql",
			expectedError: "unsupported database type: mysql",
		},
		{
			name:          "unsupported_sqlite",
			dbType:        "sqlite",
			expectedError: "unsupported database type: sqlite",
		},
		{
			name:          "empty_string",
			dbType:        "",
			expectedError: "unsupported database type:",
		},
		{
			name:          "case_sensitive",
			dbType:        "PostgreSQL",
			expectedError: "unsupported database type: PostgreSQL",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateDatabaseType(tt.dbType)
			assert.Error(t, err)
			assert.Contains(t, err.Error(), tt.expectedError)
		})
	}
}

func TestGetSupportedDatabaseTypes(t *testing.T) {
	types := GetSupportedDatabaseTypes()

	assert.Len(t, types, 2)
	assert.Contains(t, types, "postgresql")
	assert.Contains(t, types, "oracle")
}

func TestNewConnectionUnsupportedType(t *testing.T) {
	log := logger.New("debug", true)
	cfg := &config.DatabaseConfig{
		Type:     "unsupported",
		Host:     "localhost",
		Port:     5432,
		Database: "testdb",
		Username: "testuser",
	}

	conn, err := NewConnection(cfg, log)
	assert.Error(t, err)
	assert.Nil(t, conn)
	assert.Contains(t, err.Error(), "unsupported database type: unsupported")
}

func TestNewConnectionPostgreSQLConfigValidation(t *testing.T) {
	log := logger.New("debug", true)

	// Test with invalid config that should fail during connection
	cfg := &config.DatabaseConfig{
		Type:     "postgresql",
		Host:     "", // Empty host should cause connection to fail
		Port:     5432,
		Database: "testdb",
		Username: "testuser",
	}

	// The function should not panic even with bad config
	// It will try to connect and fail, but that's expected behavior
	conn, err := NewConnection(cfg, log)
	assert.Error(t, err)
	assert.Nil(t, conn)
}

func TestNewConnectionOracleConfigValidation(t *testing.T) {
	log := logger.New("debug", true)

	// Test with invalid config that should fail during connection
	cfg := &config.DatabaseConfig{
		Type:     "oracle",
		Host:     "", // Empty host should cause connection to fail
		Port:     1521,
		Database: "testdb",
		Username: "testuser",
	}

	// The function should not panic even with bad config
	conn, err := NewConnection(cfg, log)
	assert.Error(t, err)
	assert.Nil(t, conn)
}

func TestConstants(t *testing.T) {
	// Verify database type constants are correctly defined
	assert.Equal(t, "postgresql", PostgreSQL)
	assert.Equal(t, "oracle", Oracle)
}

func TestValidateDatabaseTypeEdgeCases(t *testing.T) {
	tests := []struct {
		name   string
		dbType string
		valid  bool
	}{
		{
			name:   "postgresql_lowercase",
			dbType: "postgresql",
			valid:  true,
		},
		{
			name:   "oracle_lowercase",
			dbType: "oracle",
			valid:  true,
		},
		{
			name:   "with_whitespace",
			dbType: " postgresql ",
			valid:  false,
		},
		{
			name:   "special_characters",
			dbType: "postgresql!",
			valid:  false,
		},
		{
			name:   "numeric",
			dbType: "123",
			valid:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateDatabaseType(tt.dbType)
			if tt.valid {
				assert.NoError(t, err)
			} else {
				assert.Error(t, err)
			}
		})
	}
}

// Test that the factory wraps connections with tracking using sqlmock
func TestNewConnectionReturnsTrackedConnection(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()

	// Create a simple connection wrapper for testing
	simpleConn := &simpleConnection{db: db}
	log := logger.New("debug", true)

	// Test that NewTrackedConnection creates the correct wrapper
	tracked := NewTrackedConnection(simpleConn, log)

	require.NotNil(t, tracked)
	assert.IsType(t, &TrackedConnection{}, tracked)

	// Verify the wrapper has the correct properties
	trackedConn := tracked.(*TrackedConnection)
	assert.Equal(t, simpleConn, trackedConn.conn)
	assert.Equal(t, log, trackedConn.logger)
	assert.Equal(t, "postgresql", trackedConn.vendor)

	require.NoError(t, mock.ExpectationsWereMet())
}

// Test that verifies the factory integration with tracking using sqlmock
func TestFactoryIntegrationWithTracking(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()

	log := logger.New("debug", true)
	ctx := logger.WithDBCounter(context.Background())

	// Set up mock expectations
	rows := sqlmock.NewRows([]string{"result"}).AddRow(1)
	mock.ExpectQuery("SELECT 1").WillReturnRows(rows)

	// Create connection and wrap with tracking
	simpleConn := &simpleConnection{db: db}
	tracked := NewTrackedConnection(simpleConn, log)

	// Verify that operations are tracked
	initialCounter := logger.GetDBCounter(ctx)

	dbRows, err := tracked.Query(ctx, "SELECT 1")
	require.NoError(t, err)
	defer dbRows.Close()

	// Verify counter was incremented
	finalCounter := logger.GetDBCounter(ctx)
	assert.Equal(t, initialCounter+1, finalCounter)

	// Verify elapsed time was recorded
	elapsed := logger.GetDBElapsed(ctx)
	assert.Greater(t, elapsed, int64(0))

	require.NoError(t, mock.ExpectationsWereMet())
}

// Simple connection implementation for testing with sqlmock
type simpleConnection struct {
	db *sql.DB
}

func (c *simpleConnection) Query(ctx context.Context, query string, args ...interface{}) (*sql.Rows, error) {
	return c.db.QueryContext(ctx, query, args...)
}

func (c *simpleConnection) QueryRow(ctx context.Context, query string, args ...interface{}) *sql.Row {
	return c.db.QueryRowContext(ctx, query, args...)
}

func (c *simpleConnection) Exec(ctx context.Context, query string, args ...interface{}) (sql.Result, error) {
	return c.db.ExecContext(ctx, query, args...)
}

func (c *simpleConnection) Prepare(ctx context.Context, query string) (database.Statement, error) {
	stmt, err := c.db.PrepareContext(ctx, query)
	if err != nil {
		return nil, err
	}
	return &simpleStatement{stmt: stmt}, nil
}

func (c *simpleConnection) Begin(ctx context.Context) (database.Tx, error) {
	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	return &simpleTransaction{tx: tx}, nil
}

func (c *simpleConnection) BeginTx(ctx context.Context, opts *sql.TxOptions) (database.Tx, error) {
	tx, err := c.db.BeginTx(ctx, opts)
	if err != nil {
		return nil, err
	}
	return &simpleTransaction{tx: tx}, nil
}

func (c *simpleConnection) Health(ctx context.Context) error {
	return c.db.PingContext(ctx)
}

func (c *simpleConnection) Stats() (map[string]interface{}, error) {
	stats := c.db.Stats()
	return map[string]interface{}{
		"open_connections": stats.OpenConnections,
		"in_use":           stats.InUse,
	}, nil
}

func (c *simpleConnection) Close() error {
	return c.db.Close()
}

func (c *simpleConnection) DatabaseType() string {
	return "postgresql"
}

func (c *simpleConnection) GetMigrationTable() string {
	return "flyway_schema_history"
}

func (c *simpleConnection) CreateMigrationTable(ctx context.Context) error {
	_, err := c.Exec(ctx, "CREATE TABLE IF NOT EXISTS flyway_schema_history (version VARCHAR(50))")
	return err
}

type simpleStatement struct {
	stmt *sql.Stmt
}

func (s *simpleStatement) Query(ctx context.Context, args ...interface{}) (*sql.Rows, error) {
	return s.stmt.QueryContext(ctx, args...)
}

func (s *simpleStatement) QueryRow(ctx context.Context, args ...interface{}) *sql.Row {
	return s.stmt.QueryRowContext(ctx, args...)
}

func (s *simpleStatement) Exec(ctx context.Context, args ...interface{}) (sql.Result, error) {
	return s.stmt.ExecContext(ctx, args...)
}

func (s *simpleStatement) Close() error {
	return s.stmt.Close()
}

type simpleTransaction struct {
	tx *sql.Tx
}

func (t *simpleTransaction) Query(ctx context.Context, query string, args ...interface{}) (*sql.Rows, error) {
	return t.tx.QueryContext(ctx, query, args...)
}

func (t *simpleTransaction) QueryRow(ctx context.Context, query string, args ...interface{}) *sql.Row {
	return t.tx.QueryRowContext(ctx, query, args...)
}

func (t *simpleTransaction) Exec(ctx context.Context, query string, args ...interface{}) (sql.Result, error) {
	return t.tx.ExecContext(ctx, query, args...)
}

func (t *simpleTransaction) Prepare(ctx context.Context, query string) (database.Statement, error) {
	stmt, err := t.tx.PrepareContext(ctx, query)
	if err != nil {
		return nil, err
	}
	return &simpleStatement{stmt: stmt}, nil
}

func (t *simpleTransaction) Commit() error {
	return t.tx.Commit()
}

func (t *simpleTransaction) Rollback() error {
	return t.tx.Rollback()
}
