package config

import (
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

const (
	testConnectionString        = "postgresql://user:pass@localhost/db"
	testMongoDBConnectionString = "mongodb://localhost:27017/testdb"
	testOracleHost              = "oracle.example.com"
	testAppName                 = "test-app"
	testAppVersion              = "v1.0.0"
	errMaxConnectionsPositive   = "max connections must be positive"
)

func TestValidateValidConfig(t *testing.T) {
	cfg := &Config{
		App: AppConfig{
			Name:      testAppName,
			Version:   testAppVersion,
			Env:       EnvDevelopment,
			RateLimit: 100,
		},
		Server: ServerConfig{
			Port:         8080,
			ReadTimeout:  15 * time.Second,
			WriteTimeout: 30 * time.Second,
		},
		Database: DatabaseConfig{
			Type:     "postgresql",
			Host:     "localhost",
			Port:     5432,
			Database: "testdb",
			Username: "testuser",
			MaxConns: 25,
		},
		Log: LogConfig{
			Level: "info",
		},
	}

	err := Validate(cfg)
	assert.NoError(t, err)
}

func TestValidateAppSuccess(t *testing.T) {
	tests := []struct {
		name string
		cfg  AppConfig
	}{
		{
			name: "development_environment",
			cfg: AppConfig{
				Name:      testAppName,
				Version:   testAppVersion,
				Env:       EnvDevelopment,
				RateLimit: 100,
			},
		},
		{
			name: "staging_environment",
			cfg: AppConfig{
				Name:      "staging-app",
				Version:   "v2.0.0",
				Env:       EnvStaging,
				RateLimit: 200,
			},
		},
		{
			name: "production_environment",
			cfg: AppConfig{
				Name:      "prod-app",
				Version:   "v3.0.0",
				Env:       EnvProduction,
				RateLimit: 500,
			},
		},
		{
			name: "minimum_rate_limit",
			cfg: AppConfig{
				Name:      "min-app",
				Version:   testAppVersion,
				Env:       EnvDevelopment,
				RateLimit: 1,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateApp(&tt.cfg)
			assert.NoError(t, err)
		})
	}
}

func TestValidateAppFailures(t *testing.T) {
	tests := []struct {
		name          string
		cfg           AppConfig
		expectedError string
	}{
		{
			name: "empty_name",
			cfg: AppConfig{
				Name:      "",
				Version:   testAppVersion,
				Env:       EnvDevelopment,
				RateLimit: 100,
			},
			expectedError: "app name is required",
		},
		{
			name: "empty_version",
			cfg: AppConfig{
				Name:      testAppName,
				Version:   "",
				Env:       EnvDevelopment,
				RateLimit: 100,
			},
			expectedError: "app version is required",
		},
		{
			name: "invalid_environment",
			cfg: AppConfig{
				Name:      testAppName,
				Version:   testAppVersion,
				Env:       "invalid",
				RateLimit: 100,
			},
			expectedError: "invalid environment: invalid",
		},
		{
			name: "zero_rate_limit",
			cfg: AppConfig{
				Name:      testAppName,
				Version:   testAppVersion,
				Env:       EnvDevelopment,
				RateLimit: 0,
			},
			expectedError: "rate limit must be positive",
		},
		{
			name: "negative_rate_limit",
			cfg: AppConfig{
				Name:      testAppName,
				Version:   testAppVersion,
				Env:       EnvDevelopment,
				RateLimit: -1,
			},
			expectedError: "rate limit must be positive",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateApp(&tt.cfg)
			assert.Error(t, err)
			assert.Contains(t, err.Error(), tt.expectedError)
		})
	}
}

func TestValidateServerSuccess(t *testing.T) {
	tests := []struct {
		name string
		cfg  ServerConfig
	}{
		{
			name: "standard_config",
			cfg: ServerConfig{
				Port:         8080,
				ReadTimeout:  15 * time.Second,
				WriteTimeout: 30 * time.Second,
			},
		},
		{
			name: "minimum_port",
			cfg: ServerConfig{
				Port:         1,
				ReadTimeout:  1 * time.Second,
				WriteTimeout: 1 * time.Second,
			},
		},
		{
			name: "maximum_port",
			cfg: ServerConfig{
				Port:         65535,
				ReadTimeout:  1 * time.Hour,
				WriteTimeout: 2 * time.Hour,
			},
		},
		{
			name: "common_ports",
			cfg: ServerConfig{
				Port:         3000,
				ReadTimeout:  10 * time.Second,
				WriteTimeout: 20 * time.Second,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateServer(&tt.cfg)
			assert.NoError(t, err)
		})
	}
}

func TestValidateServerFailures(t *testing.T) {
	tests := []struct {
		name          string
		cfg           ServerConfig
		expectedError string
	}{
		{
			name: "zero_port",
			cfg: ServerConfig{
				Port:         0,
				ReadTimeout:  15 * time.Second,
				WriteTimeout: 30 * time.Second,
			},
			expectedError: "invalid port: 0",
		},
		{
			name: "negative_port",
			cfg: ServerConfig{
				Port:         -1,
				ReadTimeout:  15 * time.Second,
				WriteTimeout: 30 * time.Second,
			},
			expectedError: "invalid port: -1",
		},
		{
			name: "port_too_high",
			cfg: ServerConfig{
				Port:         65536,
				ReadTimeout:  15 * time.Second,
				WriteTimeout: 30 * time.Second,
			},
			expectedError: "invalid port: 65536",
		},
		{
			name: "zero_read_timeout",
			cfg: ServerConfig{
				Port:         8080,
				ReadTimeout:  0,
				WriteTimeout: 30 * time.Second,
			},
			expectedError: "read timeout must be positive",
		},
		{
			name: "negative_read_timeout",
			cfg: ServerConfig{
				Port:         8080,
				ReadTimeout:  -1 * time.Second,
				WriteTimeout: 30 * time.Second,
			},
			expectedError: "read timeout must be positive",
		},
		{
			name: "zero_write_timeout",
			cfg: ServerConfig{
				Port:         8080,
				ReadTimeout:  15 * time.Second,
				WriteTimeout: 0,
			},
			expectedError: "write timeout must be positive",
		},
		{
			name: "negative_write_timeout",
			cfg: ServerConfig{
				Port:         8080,
				ReadTimeout:  15 * time.Second,
				WriteTimeout: -1 * time.Second,
			},
			expectedError: "write timeout must be positive",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateServer(&tt.cfg)
			assert.Error(t, err)
			assert.Contains(t, err.Error(), tt.expectedError)
		})
	}
}

func TestValidateDatabaseSuccess(t *testing.T) {
	tests := []struct {
		name string
		cfg  DatabaseConfig
	}{
		{
			name: "postgresql_config",
			cfg: DatabaseConfig{
				Type:     PostgreSQL,
				Host:     "localhost",
				Port:     5432,
				Database: "testdb",
				Username: "testuser",
				MaxConns: 25,
			},
		},
		{
			name: "oracle_config",
			cfg: DatabaseConfig{
				Type:     Oracle,
				Host:     testOracleHost,
				Port:     1521,
				Database: "XE",
				Username: "oracleuser",
				MaxConns: 50,
			},
		},
		{
			name: "minimum_values",
			cfg: DatabaseConfig{
				Type:     PostgreSQL,
				Host:     "db",
				Port:     1,
				Database: "d",
				Username: "u",
				MaxConns: 1,
			},
		},
		{
			name: "zero_max_conns_gets_default",
			cfg: DatabaseConfig{
				Type:     PostgreSQL,
				Host:     "localhost",
				Port:     5432,
				Database: "testdb",
				Username: "testuser",
				MaxConns: 0, // Should get set to default (25)
			},
		},
		{
			name: "maximum_port",
			cfg: DatabaseConfig{
				Type:     PostgreSQL,
				Host:     "localhost",
				Port:     65535,
				Database: "testdb",
				Username: "testuser",
				MaxConns: 100,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateDatabase(&tt.cfg)
			assert.NoError(t, err)
		})
	}
}

func TestValidateDatabaseFailures(t *testing.T) {
	tests := []struct {
		name          string
		cfg           DatabaseConfig
		expectedError string
	}{
		{
			name: "invalid_type",
			cfg: DatabaseConfig{
				Type:     "mysql",
				Host:     "localhost",
				Port:     3306,
				Database: "testdb",
				Username: "testuser",
				MaxConns: 25,
			},
			expectedError: "invalid database type: mysql",
		},
		{
			name: "empty_host",
			cfg: DatabaseConfig{
				Type:     PostgreSQL,
				Host:     "",
				Port:     5432,
				Database: "testdb",
				Username: "testuser",
				MaxConns: 25,
			},
			expectedError: "database host is required",
		},
		{
			name: "zero_port",
			cfg: DatabaseConfig{
				Type:     PostgreSQL,
				Host:     "localhost",
				Port:     0,
				Database: "testdb",
				Username: "testuser",
				MaxConns: 25,
			},
			expectedError: "invalid database port: 0",
		},
		{
			name: "negative_port",
			cfg: DatabaseConfig{
				Type:     PostgreSQL,
				Host:     "localhost",
				Port:     -1,
				Database: "testdb",
				Username: "testuser",
				MaxConns: 25,
			},
			expectedError: "invalid database port: -1",
		},
		{
			name: "port_too_high",
			cfg: DatabaseConfig{
				Type:     PostgreSQL,
				Host:     "localhost",
				Port:     65536,
				Database: "testdb",
				Username: "testuser",
				MaxConns: 25,
			},
			expectedError: "invalid database port: 65536",
		},
		{
			name: "empty_database",
			cfg: DatabaseConfig{
				Type:     PostgreSQL,
				Host:     "localhost",
				Port:     5432,
				Database: "",
				Username: "testuser",
				MaxConns: 25,
			},
			expectedError: "database name is required",
		},
		{
			name: "empty_username",
			cfg: DatabaseConfig{
				Type:     PostgreSQL,
				Host:     "localhost",
				Port:     5432,
				Database: "testdb",
				Username: "",
				MaxConns: 25,
			},
			expectedError: "database username is required",
		},
		{
			name: "negative_max_conns",
			cfg: DatabaseConfig{
				Type:     PostgreSQL,
				Host:     "localhost",
				Port:     5432,
				Database: "testdb",
				Username: "testuser",
				MaxConns: -1,
			},
			expectedError: errMaxConnectionsPositive,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateDatabase(&tt.cfg)
			assert.Error(t, err)
			assert.Contains(t, err.Error(), tt.expectedError)
		})
	}
}

func TestValidateLogSuccess(t *testing.T) {
	validLevels := []string{"trace", "debug", "info", "warn", "error", "fatal", "panic"}

	for _, level := range validLevels {
		t.Run("level_"+level, func(t *testing.T) {
			cfg := LogConfig{Level: level}
			err := validateLog(&cfg)
			assert.NoError(t, err)
		})
	}
}

func TestValidateLogFailures(t *testing.T) {
	tests := []struct {
		name          string
		cfg           LogConfig
		expectedError string
	}{
		{
			name: "invalid_level",
			cfg: LogConfig{
				Level: "invalid",
			},
			expectedError: "invalid log level: invalid",
		},
		{
			name: "empty_level",
			cfg: LogConfig{
				Level: "",
			},
			expectedError: "invalid log level:",
		},
		{
			name: "uppercase_level",
			cfg: LogConfig{
				Level: "INFO",
			},
			expectedError: "invalid log level: INFO",
		},
		{
			name: "mixed_case_level",
			cfg: LogConfig{
				Level: "Debug",
			},
			expectedError: "invalid log level: Debug",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateLog(&tt.cfg)
			assert.Error(t, err)
			assert.Contains(t, err.Error(), tt.expectedError)
		})
	}
}

func TestValidateNestedErrors(t *testing.T) {
	tests := []struct {
		name          string
		cfg           Config
		expectedError string
	}{
		{
			name: "app_config_error",
			cfg: Config{
				App: AppConfig{
					Name:      "",
					Version:   testAppVersion,
					Env:       EnvDevelopment,
					RateLimit: 100,
				},
				Server: ServerConfig{
					Port:         8080,
					ReadTimeout:  15 * time.Second,
					WriteTimeout: 30 * time.Second,
				},
				Database: DatabaseConfig{
					Type:     PostgreSQL,
					Host:     "localhost",
					Port:     5432,
					Database: "testdb",
					Username: "testuser",
					MaxConns: 25,
				},
				Log: LogConfig{Level: "info"},
			},
			expectedError: "app config:",
		},
		{
			name: "server_config_error",
			cfg: Config{
				App: AppConfig{
					Name:      testAppName,
					Version:   testAppVersion,
					Env:       EnvDevelopment,
					RateLimit: 100,
				},
				Server: ServerConfig{
					Port:         0,
					ReadTimeout:  15 * time.Second,
					WriteTimeout: 30 * time.Second,
				},
				Database: DatabaseConfig{
					Type:     PostgreSQL,
					Host:     "localhost",
					Port:     5432,
					Database: "testdb",
					Username: "testuser",
					MaxConns: 25,
				},
				Log: LogConfig{Level: "info"},
			},
			expectedError: "server config:",
		},
		{
			name: "database_config_error",
			cfg: Config{
				App: AppConfig{
					Name:      testAppName,
					Version:   testAppVersion,
					Env:       EnvDevelopment,
					RateLimit: 100,
				},
				Server: ServerConfig{
					Port:         8080,
					ReadTimeout:  15 * time.Second,
					WriteTimeout: 30 * time.Second,
				},
				Database: DatabaseConfig{
					Type:     "invalid",
					Host:     "localhost",
					Port:     5432,
					Database: "testdb",
					Username: "testuser",
					MaxConns: 25,
				},
				Log: LogConfig{Level: "info"},
			},
			expectedError: "database config:",
		},
		{
			name: "log_config_error",
			cfg: Config{
				App: AppConfig{
					Name:      testAppName,
					Version:   testAppVersion,
					Env:       EnvDevelopment,
					RateLimit: 100,
				},
				Server: ServerConfig{
					Port:         8080,
					ReadTimeout:  15 * time.Second,
					WriteTimeout: 30 * time.Second,
				},
				Database: DatabaseConfig{
					Type:     PostgreSQL,
					Host:     "localhost",
					Port:     5432,
					Database: "testdb",
					Username: "testuser",
					MaxConns: 25,
				},
				Log: LogConfig{Level: "invalid"},
			},
			expectedError: "log config:",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := Validate(&tt.cfg)
			assert.Error(t, err)
			assert.Contains(t, err.Error(), tt.expectedError)
		})
	}
}

func TestContains(t *testing.T) {
	tests := []struct {
		name     string
		slice    []string
		item     string
		expected bool
	}{
		{
			name:     "item_exists",
			slice:    []string{"a", "b", "c"},
			item:     "b",
			expected: true,
		},
		{
			name:     "item_not_exists",
			slice:    []string{"a", "b", "c"},
			item:     "d",
			expected: false,
		},
		{
			name:     "empty_slice",
			slice:    []string{},
			item:     "a",
			expected: false,
		},
		{
			name:     "empty_item",
			slice:    []string{"a", "", "c"},
			item:     "",
			expected: true,
		},
		{
			name:     "case_sensitive",
			slice:    []string{"a", "B", "c"},
			item:     "b",
			expected: false,
		},
		{
			name:     "duplicate_items",
			slice:    []string{"a", "b", "b", "c"},
			item:     "b",
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := slices.Contains(tt.slice, tt.item)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestIsDatabaseConfigured(t *testing.T) {
	tests := []struct {
		name     string
		config   DatabaseConfig
		expected bool
	}{
		{
			name:     "empty_config_not_configured",
			config:   DatabaseConfig{},
			expected: false,
		},
		{
			name: "host_only_is_configured",
			config: DatabaseConfig{
				Host: "localhost",
			},
			expected: true,
		},
		{
			name: "type_only_is_configured",
			config: DatabaseConfig{
				Type: "postgresql",
			},
			expected: true,
		},
		{
			name: "both_host_and_type_configured",
			config: DatabaseConfig{
				Host: "localhost",
				Type: "postgresql",
			},
			expected: true,
		},
		{
			name: "connection_string_is_configured",
			config: DatabaseConfig{
				ConnectionString: testConnectionString,
			},
			expected: true,
		},
		{
			name: "connection_string_with_empty_host_type",
			config: DatabaseConfig{
				ConnectionString: testConnectionString,
				Host:             "",
				Type:             "",
			},
			expected: true,
		},
		{
			name: "whitespace_host_not_configured",
			config: DatabaseConfig{
				Host: "   ",
			},
			expected: true, // Whitespace is still considered configured
		},
		{
			name: "whitespace_type_not_configured",
			config: DatabaseConfig{
				Type: "   ",
			},
			expected: true, // Whitespace is still considered configured
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := IsDatabaseConfigured(&tt.config)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestValidateDatabaseConditionalBehavior(t *testing.T) {
	tests := []struct {
		name          string
		config        DatabaseConfig
		expectError   bool
		errorContains string
	}{
		{
			name:        "empty_config_passes_validation",
			config:      DatabaseConfig{},
			expectError: false,
		},
		{
			name: "host_only_fails_validation",
			config: DatabaseConfig{
				Host: "localhost",
				// Missing required fields
			},
			expectError:   true,
			errorContains: "invalid database type",
		},
		{
			name: "type_only_fails_validation",
			config: DatabaseConfig{
				Type: "postgresql",
				// Missing required fields
			},
			expectError:   true,
			errorContains: "database host is required",
		},
		{
			name: "partial_config_missing_database_name",
			config: DatabaseConfig{
				Type: "postgresql",
				Host: "localhost",
				Port: 5432,
				// Missing Database, Username, MaxConns
			},
			expectError:   true,
			errorContains: "database name is required",
		},
		{
			name: "partial_config_missing_username",
			config: DatabaseConfig{
				Type:     "postgresql",
				Host:     "localhost",
				Port:     5432,
				Database: "testdb",
				// Missing Username, MaxConns
			},
			expectError:   true,
			errorContains: "database username is required",
		},
		{
			name: "partial_config_zero_max_conns_gets_default",
			config: DatabaseConfig{
				Type:     "postgresql",
				Host:     "localhost",
				Port:     5432,
				Database: "testdb",
				Username: "testuser",
				MaxConns: 0, // Gets set to default (25)
			},
			expectError: false, // Now passes with default
		},
		{
			name: "valid_postgresql_config_passes",
			config: DatabaseConfig{
				Type:     PostgreSQL,
				Host:     "localhost",
				Port:     5432,
				Database: "testdb",
				Username: "testuser",
				MaxConns: 25,
			},
			expectError: false,
		},
		{
			name: "valid_oracle_config_passes",
			config: DatabaseConfig{
				Type:     Oracle,
				Host:     testOracleHost,
				Port:     1521,
				Database: "XE",
				Username: "oracleuser",
				MaxConns: 50,
			},
			expectError: false,
		},
		{
			name: "connection_string_minimal_config_passes",
			config: DatabaseConfig{
				ConnectionString: testConnectionString,
				MaxConns:         25,
			},
			expectError: false,
		},
		{
			name: "connection_string_invalid_port_uses_optional_validation",
			config: DatabaseConfig{
				ConnectionString: testConnectionString,
				Port:             70000,
				MaxConns:         25,
			},
			expectError:   true,
			errorContains: "invalid database port",
		},
		{
			name: "connection_string_with_invalid_type",
			config: DatabaseConfig{
				ConnectionString: testConnectionString,
				Type:             "invalid",
				MaxConns:         25,
			},
			expectError:   true,
			errorContains: "invalid database type",
		},
		{
			name: "connection_string_missing_max_conns_applies_default",
			config: DatabaseConfig{
				ConnectionString: testConnectionString,
				MaxConns:         0,
			},
			expectError: false, // Should apply default of 25
		},
		{
			name: "invalid_database_type",
			config: DatabaseConfig{
				Type:     "mysql",
				Host:     "localhost",
				Port:     3306,
				Database: "testdb",
				Username: "testuser",
				MaxConns: 25,
			},
			expectError:   true,
			errorContains: "invalid database type: mysql",
		},
		{
			name: "invalid_port_range",
			config: DatabaseConfig{
				Type:     PostgreSQL,
				Host:     "localhost",
				Port:     70000, // Invalid port
				Database: "testdb",
				Username: "testuser",
				MaxConns: 25,
			},
			expectError:   true,
			errorContains: "invalid database port",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateDatabase(&tt.config)
			if tt.expectError {
				assert.Error(t, err)
				if tt.errorContains != "" {
					assert.Contains(t, err.Error(), tt.errorContains)
				}
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestValidateDatabaseDisabledConfig(t *testing.T) {
	cfg := &Config{
		App: AppConfig{
			Name:      testAppName,
			Version:   testAppVersion,
			Env:       EnvDevelopment,
			RateLimit: 100,
		},
		Server: ServerConfig{
			Port:         8080,
			ReadTimeout:  15 * time.Second,
			WriteTimeout: 30 * time.Second,
		},
		Database: DatabaseConfig{
			// Empty database config - should skip validation
		},
		Log: LogConfig{
			Level: "info",
		},
	}

	err := Validate(cfg)
	assert.NoError(t, err, "Validation should pass with empty database config")
}

func TestValidateDatabaseWithConnectionStringEdgeCases(t *testing.T) {
	tests := []struct {
		name          string
		config        DatabaseConfig
		expectError   bool
		errorContains string
	}{
		{
			name: "connection_string_with_negative_max_query_length",
			config: DatabaseConfig{
				ConnectionString: testConnectionString,
				MaxConns:         25,
				MaxQueryLength:   -1,
			},
			expectError:   true,
			errorContains: "max query length must be zero or positive",
		},
		{
			name: "connection_string_with_zero_max_query_length_applies_default",
			config: DatabaseConfig{
				ConnectionString: testConnectionString,
				MaxConns:         25,
				MaxQueryLength:   0,
			},
			expectError: false,
		},
		{
			name: "connection_string_with_negative_slow_query_threshold",
			config: DatabaseConfig{
				ConnectionString:   testConnectionString,
				MaxConns:           25,
				SlowQueryThreshold: -1 * time.Millisecond,
			},
			expectError:   true,
			errorContains: "slow query threshold must be zero or positive",
		},
		{
			name: "connection_string_with_zero_slow_query_threshold_applies_default",
			config: DatabaseConfig{
				ConnectionString:   testConnectionString,
				MaxConns:           25,
				SlowQueryThreshold: 0,
			},
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateDatabase(&tt.config)
			if tt.expectError {
				assert.Error(t, err)
				assert.Contains(t, err.Error(), tt.errorContains)
			} else {
				assert.NoError(t, err)
				// Verify defaults were applied
				if tt.config.MaxQueryLength == 0 {
					assert.Equal(t, defaultMaxQueryLength, tt.config.MaxQueryLength)
				}
				if tt.config.SlowQueryThreshold == 0 {
					assert.Equal(t, defaultSlowQueryThreshold, tt.config.SlowQueryThreshold)
				}
			}
		})
	}
}

func assertValidationError(t *testing.T, err error, errorContains string) {
	assert.Error(t, err)
	assert.Contains(t, err.Error(), errorContains)
}

func assertValidationSuccess(t *testing.T, err error, config *DatabaseConfig) {
	assert.NoError(t, err)
	// Verify defaults were applied
	if config.MaxConns == 0 {
		assert.Equal(t, int32(25), config.MaxConns)
	}
	if config.MaxQueryLength == 0 {
		assert.Equal(t, defaultMaxQueryLength, config.MaxQueryLength)
	}
	if config.SlowQueryThreshold == 0 {
		assert.Equal(t, defaultSlowQueryThreshold, config.SlowQueryThreshold)
	}
}

func TestApplyDatabasePoolDefaultsEdgeCases(t *testing.T) {
	tests := []struct {
		name          string
		config        DatabaseConfig
		expectError   bool
		errorContains string
	}{
		{
			name: "negative_max_conns_error",
			config: DatabaseConfig{
				Type:     PostgreSQL,
				Host:     "localhost",
				Port:     5432,
				Database: "testdb",
				Username: "testuser",
				MaxConns: -1,
			},
			expectError:   true,
			errorContains: errMaxConnectionsPositive,
		},
		{
			name: "zero_max_conns_applies_default",
			config: DatabaseConfig{
				Type:     PostgreSQL,
				Host:     "localhost",
				Port:     5432,
				Database: "testdb",
				Username: "testuser",
				MaxConns: 0,
			},
			expectError: false,
		},
		{
			name: "negative_max_query_length_error",
			config: DatabaseConfig{
				Type:           PostgreSQL,
				Host:           "localhost",
				Port:           5432,
				Database:       "testdb",
				Username:       "testuser",
				MaxConns:       25,
				MaxQueryLength: -1,
			},
			expectError:   true,
			errorContains: "max query length must be zero or positive",
		},
		{
			name: "zero_max_query_length_applies_default",
			config: DatabaseConfig{
				Type:           PostgreSQL,
				Host:           "localhost",
				Port:           5432,
				Database:       "testdb",
				Username:       "testuser",
				MaxConns:       25,
				MaxQueryLength: 0,
			},
			expectError: false,
		},
		{
			name: "negative_slow_query_threshold_error",
			config: DatabaseConfig{
				Type:               PostgreSQL,
				Host:               "localhost",
				Port:               5432,
				Database:           "testdb",
				Username:           "testuser",
				MaxConns:           25,
				SlowQueryThreshold: -1 * time.Millisecond,
			},
			expectError:   true,
			errorContains: "slow query threshold must be zero or positive",
		},
		{
			name: "zero_slow_query_threshold_applies_default",
			config: DatabaseConfig{
				Type:               PostgreSQL,
				Host:               "localhost",
				Port:               5432,
				Database:           "testdb",
				Username:           "testuser",
				MaxConns:           25,
				SlowQueryThreshold: 0,
			},
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateDatabase(&tt.config)
			if tt.expectError {
				assertValidationError(t, err, tt.errorContains)
			} else {
				assertValidationSuccess(t, err, &tt.config)
			}
		})
	}
}

func TestValidateMongoDBFields(t *testing.T) {
	tests := []struct {
		name          string
		config        DatabaseConfig
		expectError   bool
		errorContains string
	}{
		{
			name: "valid MongoDB config with read preference and write concern",
			config: DatabaseConfig{
				Type:           MongoDB,
				Host:           "localhost",
				Port:           27017,
				Database:       "testdb",
				Username:       "testuser",
				ReadPreference: "primary",
				WriteConcern:   "majority",
			},
			expectError: false,
		},
		{
			name: "valid MongoDB config with primaryPreferred",
			config: DatabaseConfig{
				Type:           MongoDB,
				Host:           "localhost",
				Port:           27017,
				Database:       "testdb",
				Username:       "testuser",
				ReadPreference: "primaryPreferred",
				WriteConcern:   "acknowledged",
			},
			expectError: false,
		},
		{
			name: "valid MongoDB config with case insensitive read preference",
			config: DatabaseConfig{
				Type:           MongoDB,
				Host:           "localhost",
				Port:           27017,
				Database:       "testdb",
				Username:       "testuser",
				ReadPreference: "SECONDARY",
				WriteConcern:   "MAJORITY",
			},
			expectError: false,
		},
		{
			name: "valid MongoDB config without optional fields",
			config: DatabaseConfig{
				Type:     MongoDB,
				Host:     "localhost",
				Port:     27017,
				Database: "testdb",
				Username: "testuser",
			},
			expectError: false,
		},
		{
			name: "invalid read preference",
			config: DatabaseConfig{
				Type:           MongoDB,
				Host:           "localhost",
				Port:           27017,
				Database:       "testdb",
				Username:       "testuser",
				ReadPreference: "invalid",
			},
			expectError:   true,
			errorContains: "invalid MongoDB read preference: invalid",
		},
		{
			name: "invalid write concern",
			config: DatabaseConfig{
				Type:         MongoDB,
				Host:         "localhost",
				Port:         27017,
				Database:     "testdb",
				Username:     "testuser",
				WriteConcern: "invalid",
			},
			expectError:   true,
			errorContains: "invalid MongoDB write concern: invalid",
		},
		{
			name: "non-MongoDB type should not validate MongoDB fields",
			config: DatabaseConfig{
				Type:           PostgreSQL,
				Host:           "localhost",
				Port:           5432,
				Database:       "testdb",
				Username:       "testuser",
				ReadPreference: "invalid", // This should be ignored for PostgreSQL
				WriteConcern:   "invalid", // This should be ignored for PostgreSQL
			},
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateDatabase(&tt.config)
			if tt.expectError {
				assertValidationError(t, err, tt.errorContains)
			} else {
				assertValidationSuccess(t, err, &tt.config)
			}
		})
	}
}

func TestValidateMongoDBReadPreference(t *testing.T) {
	tests := []struct {
		name        string
		preference  string
		expectError bool
	}{
		{"primary", "primary", false},
		{"primaryPreferred", "primaryPreferred", false},
		{"primarypreferred lowercase", "primarypreferred", false},
		{"secondary", "secondary", false},
		{"secondaryPreferred", "secondaryPreferred", false},
		{"secondarypreferred lowercase", "secondarypreferred", false},
		{"nearest", "nearest", false},
		{"NEAREST uppercase", "NEAREST", false},
		{"mixed case", "PrimaryPreferred", false},
		{"invalid preference", "invalid", true},
		{"empty string", "", true},
		{"typo in primary", "primari", true},
		{"typo in secondary", "secundary", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateMongoDBReadPreference(tt.preference)
			if tt.expectError {
				assert.Error(t, err)
				assert.Contains(t, err.Error(), "invalid MongoDB read preference")
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestValidateMongoDBWriteConcern(t *testing.T) {
	tests := []struct {
		name        string
		concern     string
		expectError bool
	}{
		{"majority", "majority", false},
		{"acknowledged", "acknowledged", false},
		{"unacknowledged", "unacknowledged", false},
		{"MAJORITY uppercase", "MAJORITY", false},
		{"Acknowledged mixed case", "Acknowledged", false},
		{"invalid concern", "invalid", true},
		{"empty string", "", true},
		{"typo in majority", "majorty", true},
		{"numeric string", "1", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateMongoDBWriteConcern(tt.concern)
			if tt.expectError {
				assert.Error(t, err)
				assert.Contains(t, err.Error(), "invalid MongoDB write concern")
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestValidateMongoDBWithConnectionString(t *testing.T) {
	tests := []struct {
		name          string
		config        DatabaseConfig
		expectError   bool
		errorContains string
	}{
		{
			name: "valid MongoDB with connection string and valid read preference",
			config: DatabaseConfig{
				Type:             MongoDB,
				ConnectionString: testMongoDBConnectionString,
				ReadPreference:   "primary",
				WriteConcern:     "majority",
			},
			expectError: false,
		},
		{
			name: "invalid read preference with connection string",
			config: DatabaseConfig{
				Type:             MongoDB,
				ConnectionString: testMongoDBConnectionString,
				ReadPreference:   "invalid",
			},
			expectError:   true,
			errorContains: "invalid MongoDB read preference",
		},
		{
			name: "invalid write concern with connection string",
			config: DatabaseConfig{
				Type:             MongoDB,
				ConnectionString: testMongoDBConnectionString,
				WriteConcern:     "invalid",
			},
			expectError:   true,
			errorContains: "invalid MongoDB write concern",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateDatabase(&tt.config)
			if tt.expectError {
				assertValidationError(t, err, tt.errorContains)
			} else {
				assertValidationSuccess(t, err, &tt.config)
			}
		})
	}
}
