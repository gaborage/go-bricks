package database

import (
	"testing"

	"github.com/stretchr/testify/assert"

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
