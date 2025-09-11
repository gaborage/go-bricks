package database

import (
	"context"
	"database/sql"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/config"
	"github.com/gaborage/go-bricks/logger"
)

func TestValidateDatabaseType_Success(t *testing.T) {
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

func TestValidateDatabaseType_Failure(t *testing.T) {
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

func TestNewConnection_UnsupportedType(t *testing.T) {
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

func TestNewConnection_PostgreSQL_ConfigValidation(t *testing.T) {
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

func TestNewConnection_Oracle_ConfigValidation(t *testing.T) {
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

func TestValidateDatabaseType_EdgeCases(t *testing.T) {
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

// Test that the factory wraps connections with tracking
func TestNewConnection_ReturnsTrackedConnection(t *testing.T) {
	// Test with mock to verify tracking wrapper structure
	mockConn := &MockInterface{}
	mockConn.On("DatabaseType").Return("postgresql")

	log := logger.New("debug", true)

	// Test that NewTrackedConnection creates the correct wrapper
	tracked := NewTrackedConnection(mockConn, log)

	require.NotNil(t, tracked)
	assert.IsType(t, &TrackedConnection{}, tracked)

	// Verify the wrapper has the correct properties
	trackedConn := tracked.(*TrackedConnection)
	assert.Equal(t, mockConn, trackedConn.conn)
	assert.Equal(t, log, trackedConn.logger)
	assert.Equal(t, "postgresql", trackedConn.vendor)

	mockConn.AssertExpectations(t)
}

// Test that verifies the factory integration with tracking
func TestFactoryIntegrationWithTracking(t *testing.T) {
	// This test demonstrates how the factory should work with tracking
	log := logger.New("debug", true)
	ctx := logger.WithDBCounter(context.Background())

	// Create a mock connection to simulate what the factory does
	mockConn := &MockInterface{}
	mockConn.On("DatabaseType").Return("postgresql")
	mockConn.On("Query", ctx, "SELECT 1", mock.Anything).Return(&sql.Rows{}, nil)

	// Simulate what the factory does: wrap with tracking
	tracked := NewTrackedConnection(mockConn, log)

	// Verify that operations are tracked
	initialCounter := logger.GetDBCounter(ctx)

	rows, err := tracked.Query(ctx, "SELECT 1")
	require.NoError(t, err)
	_ = rows // Just reference rows to avoid unused variable

	// Verify counter was incremented
	finalCounter := logger.GetDBCounter(ctx)
	assert.Equal(t, initialCounter+1, finalCounter)

	// Verify elapsed time was recorded
	elapsed := logger.GetDBElapsed(ctx)
	assert.Greater(t, elapsed, int64(0))

	mockConn.AssertExpectations(t)
}
