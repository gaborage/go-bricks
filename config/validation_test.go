package config

import (
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

const (
	testConnectionString         = "postgresql://user:pass@localhost/db"
	testOracleConnectionString   = "oracle://user:pass@localhost:1521/XEPDB1"
	testOracleHost               = "oracle.example.com"
	testAppName                  = "test-app"
	testAppVersion               = "v1.0.0"
	errMaxConnectionsNonNegative = "database.pool.max.connections must be non-negative"
	testAMQPHost                 = "amqp://localhost:5672/"
	testTenantHeader             = "X-Tenant-ID"
	testDomain                   = ".api.example.com"
	testTenantDBHost             = "tenant-a.db.local"
	serverPort                   = "server.port"
	databaseType                 = "database.type"
	databasePort                 = "database.port"
	dbLocalField                 = "db.local"
	cacheTypeField               = "cache.type"
	redisPortField               = "cache.redis.port"
	logLevel                     = "log.level"
	oracleConnectionIdentifier   = "oracle connection identifier"
)

func makeSampleTenants() map[string]TenantEntry {
	return map[string]TenantEntry{
		tenantA: {
			Database: DatabaseConfig{
				Type:     PostgreSQL,
				Host:     testTenantDBHost,
				Port:     5432,
				Database: "tenant_a",
				Username: "tenant_user",
			},
			Messaging: TenantMessagingConfig{URL: testAMQPHost},
		},
	}
}

func TestValidateValidConfig(t *testing.T) {
	cfg := createValidFullConfig()
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
				Name:    testAppName,
				Version: testAppVersion,
				Env:     EnvDevelopment,
				Rate:    RateConfig{Limit: 100},
			},
		},
		{
			name: "staging_environment",
			cfg: AppConfig{
				Name:    "staging-app",
				Version: "v2.0.0",
				Env:     EnvStaging,
				Rate:    RateConfig{Limit: 200},
			},
		},
		{
			name: "production_environment",
			cfg: AppConfig{
				Name:    "prod-app",
				Version: "v3.0.0",
				Env:     EnvProduction,
				Rate:    RateConfig{Limit: 500},
			},
		},
		{
			name: "minimum_rate_limit",
			cfg: AppConfig{
				Name:    "min-app",
				Version: testAppVersion,
				Env:     EnvDevelopment,
				Rate:    RateConfig{Limit: 1},
			},
		},
		{
			name: "zero_rate_limit_disabled",
			cfg: AppConfig{
				Name:    "no-limit-app",
				Version: testAppVersion,
				Env:     EnvDevelopment,
				Rate:    RateConfig{Limit: 0},
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
				Name:    "",
				Version: testAppVersion,
				Env:     EnvDevelopment,
				Rate:    RateConfig{Limit: 100},
			},
			expectedError: "app.name",
		},
		{
			name: "empty_version",
			cfg: AppConfig{
				Name:    testAppName,
				Version: "",
				Env:     EnvDevelopment,
				Rate:    RateConfig{Limit: 100},
			},
			expectedError: "app.version",
		},
		{
			name: "invalid_environment",
			cfg: AppConfig{
				Name:    testAppName,
				Version: testAppVersion,
				Env:     "invalid",
				Rate:    RateConfig{Limit: 100},
			},
			expectedError: "app.env",
		},
		{
			name: "negative_rate_limit",
			cfg: AppConfig{
				Name:    testAppName,
				Version: testAppVersion,
				Env:     EnvDevelopment,
				Rate:    RateConfig{Limit: -1},
			},
			expectedError: "app.rate.limit must be non-negative",
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
				Port: 8080,
				Timeout: TimeoutConfig{
					Read:       15 * time.Second,
					Write:      30 * time.Second,
					Middleware: 5 * time.Second,
					Shutdown:   10 * time.Second,
				},
			},
		},
		{
			name: "minimum_port",
			cfg: ServerConfig{
				Port: 1,
				Timeout: TimeoutConfig{
					Read:       1 * time.Second,
					Write:      2 * time.Second,
					Middleware: 1 * time.Second,
					Shutdown:   1 * time.Second,
				},
			},
		},
		{
			name: "maximum_port",
			cfg: ServerConfig{
				Port: 65535,
				Timeout: TimeoutConfig{
					Read:       1 * time.Hour,
					Write:      2 * time.Hour,
					Middleware: 30 * time.Second,
					Shutdown:   1 * time.Minute,
				},
			},
		},
		{
			name: "common_ports",
			cfg: ServerConfig{
				Port: 3000,
				Timeout: TimeoutConfig{
					Read:       10 * time.Second,
					Write:      20 * time.Second,
					Middleware: 5 * time.Second,
					Shutdown:   10 * time.Second,
				},
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
				Port: 0,
				Timeout: TimeoutConfig{
					Read:       15 * time.Second,
					Write:      30 * time.Second,
					Middleware: 5 * time.Second,
					Shutdown:   10 * time.Second,
				},
			},
			expectedError: serverPort,
		},
		{
			name: "negative_port",
			cfg: ServerConfig{
				Port: -1,
				Timeout: TimeoutConfig{
					Read:       15 * time.Second,
					Write:      30 * time.Second,
					Middleware: 5 * time.Second,
					Shutdown:   10 * time.Second,
				},
			},
			expectedError: serverPort,
		},
		{
			name: "port_too_high",
			cfg: ServerConfig{
				Port: 65536,
				Timeout: TimeoutConfig{
					Read:       15 * time.Second,
					Write:      30 * time.Second,
					Middleware: 5 * time.Second,
					Shutdown:   10 * time.Second,
				},
			},
			expectedError: serverPort,
		},
		{
			name: "zero_read_timeout",
			cfg: ServerConfig{
				Port: 8080,
				Timeout: TimeoutConfig{
					Read:  0,
					Write: 30 * time.Second,
				},
			},
			expectedError: "server.timeout.read must be positive",
		},
		{
			name: "negative_read_timeout",
			cfg: ServerConfig{
				Port: 8080,
				Timeout: TimeoutConfig{
					Read:  -1 * time.Second,
					Write: 30 * time.Second,
				},
			},
			expectedError: "server.timeout.read must be positive",
		},
		{
			name: "zero_write_timeout",
			cfg: ServerConfig{
				Port: 8080,
				Timeout: TimeoutConfig{
					Read:  15 * time.Second,
					Write: 0,
				},
			},
			expectedError: "server.timeout.write must be positive",
		},
		{
			name: "negative_write_timeout",
			cfg: ServerConfig{
				Port: 8080,
				Timeout: TimeoutConfig{
					Read:  15 * time.Second,
					Write: -1 * time.Second,
				},
			},
			expectedError: "server.timeout.write must be positive",
		},
		{
			name: "middleware_timeout_equal_to_write_timeout",
			cfg: ServerConfig{
				Port: 8080,
				Timeout: TimeoutConfig{
					Read:       15 * time.Second,
					Write:      10 * time.Second,
					Middleware: 10 * time.Second,
					Shutdown:   5 * time.Second,
				},
			},
			expectedError: "server.timeout.middleware must be less than server.timeout.write",
		},
		{
			name: "middleware_timeout_greater_than_write_timeout",
			cfg: ServerConfig{
				Port: 8080,
				Timeout: TimeoutConfig{
					Read:       15 * time.Second,
					Write:      5 * time.Second,
					Middleware: 10 * time.Second,
					Shutdown:   5 * time.Second,
				},
			},
			expectedError: "server.timeout.middleware must be less than server.timeout.write",
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
				Pool: PoolConfig{
					Max: PoolMaxConfig{
						Connections: 25,
					},
				},
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
				Pool: PoolConfig{
					Max: PoolMaxConfig{
						Connections: 50,
					},
				},
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
				Pool: PoolConfig{
					Max: PoolMaxConfig{
						Connections: 1,
					},
				},
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
				Pool: PoolConfig{
					Max: PoolMaxConfig{
						Connections: 0, // Should get set to default (25)
					},
				},
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
				Pool: PoolConfig{
					Max: PoolMaxConfig{
						Connections: 100,
					},
				},
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
				Pool: PoolConfig{
					Max: PoolMaxConfig{
						Connections: 25,
					},
				},
			},
			expectedError: databaseType,
		},
		{
			name: "empty_host",
			cfg: DatabaseConfig{
				Type:     PostgreSQL,
				Host:     "",
				Port:     5432,
				Database: "testdb",
				Username: "testuser",
				Pool: PoolConfig{
					Max: PoolMaxConfig{
						Connections: 25,
					},
				},
			},
			expectedError: "database.host",
		},
		{
			name: "zero_port",
			cfg: DatabaseConfig{
				Type:     PostgreSQL,
				Host:     "localhost",
				Port:     0,
				Database: "testdb",
				Username: "testuser",
				Pool: PoolConfig{
					Max: PoolMaxConfig{
						Connections: 25,
					},
				},
			},
			expectedError: databasePort,
		},
		{
			name: "negative_port",
			cfg: DatabaseConfig{
				Type:     PostgreSQL,
				Host:     "localhost",
				Port:     -1,
				Database: "testdb",
				Username: "testuser",
				Pool: PoolConfig{
					Max: PoolMaxConfig{
						Connections: 25,
					},
				},
			},
			expectedError: databasePort,
		},
		{
			name: "port_too_high",
			cfg: DatabaseConfig{
				Type:     PostgreSQL,
				Host:     "localhost",
				Port:     65536,
				Database: "testdb",
				Username: "testuser",
				Pool: PoolConfig{
					Max: PoolMaxConfig{
						Connections: 25,
					},
				},
			},
			expectedError: databasePort,
		},
		{
			name: "empty_database",
			cfg: DatabaseConfig{
				Type:     PostgreSQL,
				Host:     "localhost",
				Port:     5432,
				Database: "",
				Username: "testuser",
				Pool: PoolConfig{
					Max: PoolMaxConfig{
						Connections: 25,
					},
				},
			},
			expectedError: "database.database",
		},
		{
			name: "empty_username",
			cfg: DatabaseConfig{
				Type:     PostgreSQL,
				Host:     "localhost",
				Port:     5432,
				Database: "testdb",
				Username: "",
				Pool: PoolConfig{
					Max: PoolMaxConfig{
						Connections: 25,
					},
				},
			},
			expectedError: "database.username",
		},
		{
			name: "negative_max_conns",
			cfg: DatabaseConfig{
				Type:     PostgreSQL,
				Host:     "localhost",
				Port:     5432,
				Database: "testdb",
				Username: "testuser",
				Pool: PoolConfig{
					Max: PoolMaxConfig{
						Connections: -1,
					},
				},
			},
			expectedError: errMaxConnectionsNonNegative,
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
			expectedError: logLevel,
		},
		{
			name: "empty_level",
			cfg: LogConfig{
				Level: "",
			},
			expectedError: logLevel,
		},
		{
			name: "uppercase_level",
			cfg: LogConfig{
				Level: "INFO",
			},
			expectedError: logLevel,
		},
		{
			name: "mixed_case_level",
			cfg: LogConfig{
				Level: "Debug",
			},
			expectedError: logLevel,
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
		cfg           *Config
		expectedError string
	}{
		{
			name: "app_config_error",
			cfg: &Config{
				App: AppConfig{
					Name:    "",
					Version: testAppVersion,
					Env:     EnvDevelopment,
					Rate:    RateConfig{Limit: 100},
				},
				Server: ServerConfig{
					Port: 8080,
					Timeout: TimeoutConfig{
						Read:  15 * time.Second,
						Write: 30 * time.Second,
					},
				},
				Database: DatabaseConfig{
					Type:     PostgreSQL,
					Host:     "localhost",
					Port:     5432,
					Database: "testdb",
					Username: "testuser",
					Pool: PoolConfig{
						Max: PoolMaxConfig{
							Connections: 25,
						},
					},
				},
				Log: LogConfig{Level: "info"},
			},
			expectedError: "app config:",
		},
		{
			name: "server_config_error",
			cfg: &Config{
				App: AppConfig{
					Name:    testAppName,
					Version: testAppVersion,
					Env:     EnvDevelopment,
					Rate:    RateConfig{Limit: 100},
				},
				Server: ServerConfig{
					Port: 0,
					Timeout: TimeoutConfig{
						Read:  15 * time.Second,
						Write: 30 * time.Second,
					},
				},
				Database: DatabaseConfig{
					Type:     PostgreSQL,
					Host:     "localhost",
					Port:     5432,
					Database: "testdb",
					Username: "testuser",
					Pool: PoolConfig{
						Max: PoolMaxConfig{
							Connections: 25,
						},
					},
				},
				Log: LogConfig{Level: "info"},
			},
			expectedError: "server config:",
		},
		{
			name: "database_config_error",
			cfg: &Config{
				App: AppConfig{
					Name:    testAppName,
					Version: testAppVersion,
					Env:     EnvDevelopment,
					Rate:    RateConfig{Limit: 100},
				},
				Server: ServerConfig{
					Port: 8080,
					Timeout: TimeoutConfig{
						Read:       15 * time.Second,
						Write:      30 * time.Second,
						Middleware: 5 * time.Second,
						Shutdown:   10 * time.Second,
					},
				},
				Database: DatabaseConfig{
					Type:     "invalid",
					Host:     "localhost",
					Port:     5432,
					Database: "testdb",
					Username: "testuser",
					Pool: PoolConfig{
						Max: PoolMaxConfig{
							Connections: 25,
						},
					},
				},
				Log: LogConfig{Level: "info"},
			},
			expectedError: "database config:",
		},
		{
			name: "log_config_error",
			cfg: &Config{
				App: AppConfig{
					Name:    testAppName,
					Version: testAppVersion,
					Env:     EnvDevelopment,
					Rate:    RateConfig{Limit: 100},
				},
				Server: ServerConfig{
					Port: 8080,
					Timeout: TimeoutConfig{
						Read:       15 * time.Second,
						Write:      30 * time.Second,
						Middleware: 5 * time.Second,
						Shutdown:   10 * time.Second,
					},
				},
				Database: DatabaseConfig{
					Type:     PostgreSQL,
					Host:     "localhost",
					Port:     5432,
					Database: "testdb",
					Username: "testuser",
					Pool: PoolConfig{
						Max: PoolMaxConfig{
							Connections: 25,
						},
					},
				},
				Log: LogConfig{Level: "invalid"},
			},
			expectedError: "log config:",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := Validate(tt.cfg)
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
			errorContains: databaseType,
		},
		{
			name: "type_only_fails_validation",
			config: DatabaseConfig{
				Type: "postgresql",
				// Missing required fields
			},
			expectError:   true,
			errorContains: "database.host",
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
			errorContains: "database.database",
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
			errorContains: "database.username",
		},
		{
			name: "partial_config_zero_max_conns_gets_default",
			config: DatabaseConfig{
				Type:     "postgresql",
				Host:     "localhost",
				Port:     5432,
				Database: "testdb",
				Username: "testuser",
				Pool: PoolConfig{
					Max: PoolMaxConfig{
						Connections: 0, // Should get set to default (25)
					},
				},
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
				Pool: PoolConfig{
					Max: PoolMaxConfig{
						Connections: 25,
					},
				},
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
				Pool: PoolConfig{
					Max: PoolMaxConfig{
						Connections: 50,
					},
				},
			},
			expectError: false,
		},
		{
			name: "connection_string_minimal_config_passes",
			config: DatabaseConfig{
				ConnectionString: testConnectionString,
				Pool: PoolConfig{
					Max: PoolMaxConfig{
						Connections: 25,
					},
				},
			},
			expectError: false,
		},
		{
			name: "connection_string_invalid_port_uses_optional_validation",
			config: DatabaseConfig{
				ConnectionString: testConnectionString,
				Port:             70000,
				Pool: PoolConfig{
					Max: PoolMaxConfig{
						Connections: 25,
					},
				},
			},
			expectError:   true,
			errorContains: databasePort,
		},
		{
			name: "connection_string_with_invalid_type",
			config: DatabaseConfig{
				ConnectionString: testConnectionString,
				Type:             "invalid",
				Pool: PoolConfig{
					Max: PoolMaxConfig{
						Connections: 25,
					},
				},
			},
			expectError:   true,
			errorContains: databaseType,
		},
		{
			name: "connection_string_missing_max_conns_applies_default",
			config: DatabaseConfig{
				ConnectionString: testConnectionString,
				Pool: PoolConfig{
					Max: PoolMaxConfig{
						Connections: 0,
					},
				},
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
				Pool: PoolConfig{
					Max: PoolMaxConfig{
						Connections: 25,
					},
				},
			},
			expectError:   true,
			errorContains: databaseType,
		},
		{
			name: "invalid_port_range",
			config: DatabaseConfig{
				Type:     PostgreSQL,
				Host:     "localhost",
				Port:     70000, // Invalid port
				Database: "testdb",
				Username: "testuser",
				Pool: PoolConfig{
					Max: PoolMaxConfig{
						Connections: 25,
					},
				},
			},
			expectError:   true,
			errorContains: databasePort,
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
			Name:    testAppName,
			Version: testAppVersion,
			Env:     EnvDevelopment,
			Rate:    RateConfig{Limit: 100},
		},
		Server: ServerConfig{
			Port: 8080,
			Timeout: TimeoutConfig{
				Read:       15 * time.Second,
				Write:      30 * time.Second,
				Middleware: 5 * time.Second,
				Shutdown:   10 * time.Second,
			},
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
				Pool: PoolConfig{
					Max: PoolMaxConfig{
						Connections: 25,
					},
				},
				Query: QueryConfig{
					Log: QueryLogConfig{
						MaxLength: -1,
					},
				},
			},
			expectError:   true,
			errorContains: "database.query.log.maxlength must be non-negative",
		},
		{
			name: "connection_string_with_zero_max_query_length_applies_default",
			config: DatabaseConfig{
				ConnectionString: testConnectionString,
				Pool: PoolConfig{
					Max: PoolMaxConfig{
						Connections: 25,
					},
				},
				Query: QueryConfig{
					Log: QueryLogConfig{
						MaxLength: 0,
					},
				},
			},
			expectError: false,
		},
		{
			name: "connection_string_with_negative_slow_query_threshold",
			config: DatabaseConfig{
				ConnectionString: testConnectionString,
				Pool: PoolConfig{
					Max: PoolMaxConfig{
						Connections: 25,
					},
				},
				Query: QueryConfig{
					Slow: SlowQueryConfig{
						Threshold: -1 * time.Millisecond,
					},
				},
			},
			expectError:   true,
			errorContains: "database.query.slow.threshold must be non-negative",
		},
		{
			name: "connection_string_with_zero_slow_query_threshold_applies_default",
			config: DatabaseConfig{
				ConnectionString: testConnectionString,
				Pool: PoolConfig{
					Max: PoolMaxConfig{
						Connections: 25,
					},
				},
				Query: QueryConfig{
					Slow: SlowQueryConfig{
						Threshold: 0,
					},
				},
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
				if tt.config.Query.Log.MaxLength == 0 {
					assert.Equal(t, defaultMaxQueryLength, tt.config.Query.Log.MaxLength)
				}
				if tt.config.Query.Slow.Threshold == 0 {
					assert.Equal(t, defaultSlowQueryThreshold, tt.config.Query.Slow.Threshold)
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
	if config.Pool.Max.Connections == 0 {
		assert.Equal(t, int32(25), config.Pool.Max.Connections)
	}
	if config.Query.Log.MaxLength == 0 {
		assert.Equal(t, defaultMaxQueryLength, config.Query.Log.MaxLength)
	}
	if config.Query.Slow.Threshold == 0 {
		assert.Equal(t, defaultSlowQueryThreshold, config.Query.Slow.Threshold)
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
				Pool: PoolConfig{
					Max: PoolMaxConfig{
						Connections: -1,
					},
				},
			},
			expectError:   true,
			errorContains: errMaxConnectionsNonNegative,
		},
		{
			name: "zero_max_conns_applies_default",
			config: DatabaseConfig{
				Type:     PostgreSQL,
				Host:     "localhost",
				Port:     5432,
				Database: "testdb",
				Username: "testuser",
				Pool: PoolConfig{
					Max: PoolMaxConfig{
						Connections: 0,
					},
				},
			},
			expectError: false,
		},
		{
			name: "negative_max_query_length_error",
			config: DatabaseConfig{
				Type:     PostgreSQL,
				Host:     "localhost",
				Port:     5432,
				Database: "testdb",
				Username: "testuser",
				Pool: PoolConfig{
					Max: PoolMaxConfig{
						Connections: 25,
					},
				},
				Query: QueryConfig{
					Log: QueryLogConfig{
						MaxLength: -1,
					},
				},
			},
			expectError:   true,
			errorContains: "database.query.log.maxlength must be non-negative",
		},
		{
			name: "zero_max_query_length_applies_default",
			config: DatabaseConfig{
				Type:     PostgreSQL,
				Host:     "localhost",
				Port:     5432,
				Database: "testdb",
				Username: "testuser",
				Pool: PoolConfig{
					Max: PoolMaxConfig{
						Connections: 25,
					},
				},
				Query: QueryConfig{
					Log: QueryLogConfig{
						MaxLength: 0,
					},
				},
			},
			expectError: false,
		},
		{
			name: "negative_slow_query_threshold_error",
			config: DatabaseConfig{
				Type:     PostgreSQL,
				Host:     "localhost",
				Port:     5432,
				Database: "testdb",
				Username: "testuser",
				Pool: PoolConfig{
					Max: PoolMaxConfig{
						Connections: 25,
					},
				},
				Query: QueryConfig{
					Slow: SlowQueryConfig{
						Threshold: -1 * time.Millisecond,
					},
				},
			},
			expectError:   true,
			errorContains: "database.query.slow.threshold must be non-negative",
		},
		{
			name: "zero_slow_query_threshold_applies_default",
			config: DatabaseConfig{
				Type:     PostgreSQL,
				Host:     "localhost",
				Port:     5432,
				Database: "testdb",
				Username: "testuser",
				Pool: PoolConfig{
					Max: PoolMaxConfig{
						Connections: 25,
					},
				},
				Query: QueryConfig{
					Slow: SlowQueryConfig{
						Threshold: 0,
					},
				},
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

func TestApplyDatabasePoolDefaultsKeepAlive(t *testing.T) {
	tests := []struct {
		name             string
		config           DatabaseConfig
		expectedEnabled  bool
		expectedInterval time.Duration
	}{
		{
			name: "zero_values_apply_defaults",
			config: DatabaseConfig{
				Type:     PostgreSQL,
				Host:     "localhost",
				Port:     5432,
				Database: "testdb",
				Username: "testuser",
				Pool: PoolConfig{
					Max:       PoolMaxConfig{Connections: 25},
					KeepAlive: PoolKeepAliveConfig{}, // Zero values
				},
			},
			expectedEnabled:  defaultKeepAliveEnabled,
			expectedInterval: defaultKeepAliveInterval,
		},
		{
			name: "explicit_disabled_with_zero_interval_applies_defaults",
			config: DatabaseConfig{
				Type:     PostgreSQL,
				Host:     "localhost",
				Port:     5432,
				Database: "testdb",
				Username: "testuser",
				Pool: PoolConfig{
					Max: PoolMaxConfig{Connections: 25},
					KeepAlive: PoolKeepAliveConfig{
						Enabled:  false, // Explicitly disabled
						Interval: 0,     // But zero interval triggers defaults
					},
				},
			},
			// When Interval=0, defaults are applied for BOTH fields
			// This is intentional: Interval=0 means "not configured"
			expectedEnabled:  defaultKeepAliveEnabled,
			expectedInterval: defaultKeepAliveInterval,
		},
		{
			name: "explicit_interval_preserves_values",
			config: DatabaseConfig{
				Type:     PostgreSQL,
				Host:     "localhost",
				Port:     5432,
				Database: "testdb",
				Username: "testuser",
				Pool: PoolConfig{
					Max: PoolMaxConfig{Connections: 25},
					KeepAlive: PoolKeepAliveConfig{
						Enabled:  true,
						Interval: 30 * time.Second,
					},
				},
			},
			expectedEnabled:  true,
			expectedInterval: 30 * time.Second,
		},
		{
			name: "explicit_disabled_with_interval_preserved",
			config: DatabaseConfig{
				Type:     PostgreSQL,
				Host:     "localhost",
				Port:     5432,
				Database: "testdb",
				Username: "testuser",
				Pool: PoolConfig{
					Max: PoolMaxConfig{Connections: 25},
					KeepAlive: PoolKeepAliveConfig{
						Enabled:  false,
						Interval: 120 * time.Second,
					},
				},
			},
			expectedEnabled:  false,
			expectedInterval: 120 * time.Second,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateDatabase(&tt.config)
			assert.NoError(t, err)
			assert.Equal(t, tt.expectedEnabled, tt.config.Pool.KeepAlive.Enabled,
				"KeepAlive.Enabled mismatch")
			assert.Equal(t, tt.expectedInterval, tt.config.Pool.KeepAlive.Interval,
				"KeepAlive.Interval mismatch")
		})
	}
}

func TestApplyDatabasePoolDefaultsIdleAndLifetime(t *testing.T) {
	tests := []struct {
		name                    string
		config                  DatabaseConfig
		expectedIdleTime        time.Duration
		expectedLifetimeMax     time.Duration
		expectedIdleConnections int32
	}{
		{
			name: "zero_values_apply_all_defaults",
			config: DatabaseConfig{
				Type:     PostgreSQL,
				Host:     "localhost",
				Port:     5432,
				Database: "testdb",
				Username: "testuser",
				Pool:     PoolConfig{}, // All zero values
			},
			expectedIdleTime:        defaultPoolIdleTime,
			expectedLifetimeMax:     defaultPoolLifetimeMax,
			expectedIdleConnections: defaultPoolIdleConnections,
		},
		{
			name: "explicit_values_preserved",
			config: DatabaseConfig{
				Type:     PostgreSQL,
				Host:     "localhost",
				Port:     5432,
				Database: "testdb",
				Username: "testuser",
				Pool: PoolConfig{
					Max:      PoolMaxConfig{Connections: 50},
					Idle:     PoolIdleConfig{Connections: 5, Time: 10 * time.Minute},
					Lifetime: LifetimeConfig{Max: 1 * time.Hour},
				},
			},
			expectedIdleTime:        10 * time.Minute,
			expectedLifetimeMax:     1 * time.Hour,
			expectedIdleConnections: 5,
		},
		{
			name: "partial_config_applies_missing_defaults",
			config: DatabaseConfig{
				Type:     PostgreSQL,
				Host:     "localhost",
				Port:     5432,
				Database: "testdb",
				Username: "testuser",
				Pool: PoolConfig{
					Max:  PoolMaxConfig{Connections: 25},
					Idle: PoolIdleConfig{Time: 3 * time.Minute}, // Only idle time set
				},
			},
			expectedIdleTime:        3 * time.Minute,
			expectedLifetimeMax:     defaultPoolLifetimeMax,     // Default applied
			expectedIdleConnections: defaultPoolIdleConnections, // Default applied
		},
		{
			name: "only_idle_connections_set",
			config: DatabaseConfig{
				Type:     PostgreSQL,
				Host:     "localhost",
				Port:     5432,
				Database: "testdb",
				Username: "testuser",
				Pool: PoolConfig{
					Idle: PoolIdleConfig{Connections: 10},
				},
			},
			expectedIdleTime:        defaultPoolIdleTime,    // Default applied
			expectedLifetimeMax:     defaultPoolLifetimeMax, // Default applied
			expectedIdleConnections: 10,                     // Explicit value preserved
		},
		{
			name: "only_lifetime_set",
			config: DatabaseConfig{
				Type:     PostgreSQL,
				Host:     "localhost",
				Port:     5432,
				Database: "testdb",
				Username: "testuser",
				Pool: PoolConfig{
					Lifetime: LifetimeConfig{Max: 15 * time.Minute},
				},
			},
			expectedIdleTime:        defaultPoolIdleTime,        // Default applied
			expectedLifetimeMax:     15 * time.Minute,           // Explicit value preserved
			expectedIdleConnections: defaultPoolIdleConnections, // Default applied
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateDatabase(&tt.config)
			assert.NoError(t, err)
			assert.Equal(t, tt.expectedIdleTime, tt.config.Pool.Idle.Time,
				"Pool.Idle.Time mismatch")
			assert.Equal(t, tt.expectedLifetimeMax, tt.config.Pool.Lifetime.Max,
				"Pool.Lifetime.Max mismatch")
			assert.Equal(t, tt.expectedIdleConnections, tt.config.Pool.Idle.Connections,
				"Pool.Idle.Connections mismatch")
		})
	}
}

func TestApplyDatabasePoolDefaultsNegativeValues(t *testing.T) {
	tests := []struct {
		name          string
		config        DatabaseConfig
		errorContains string
	}{
		{
			name: "negative_idle_time_rejected",
			config: DatabaseConfig{
				Type:     PostgreSQL,
				Host:     "localhost",
				Port:     5432,
				Database: "testdb",
				Username: "testuser",
				Pool: PoolConfig{
					Max:  PoolMaxConfig{Connections: 25},
					Idle: PoolIdleConfig{Time: -1 * time.Minute},
				},
			},
			errorContains: "database.pool.idle.time",
		},
		{
			name: "negative_lifetime_rejected",
			config: DatabaseConfig{
				Type:     PostgreSQL,
				Host:     "localhost",
				Port:     5432,
				Database: "testdb",
				Username: "testuser",
				Pool: PoolConfig{
					Max:      PoolMaxConfig{Connections: 25},
					Lifetime: LifetimeConfig{Max: -1 * time.Hour},
				},
			},
			errorContains: "database.pool.lifetime.max",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateDatabase(&tt.config)
			assertValidationError(t, err, tt.errorContains)
		})
	}
}

func TestApplyMessagingDefaults(t *testing.T) {
	tests := []struct {
		name                      string
		config                    MessagingConfig
		expectedReconnectDelay    time.Duration
		expectedReinitDelay       time.Duration
		expectedResendDelay       time.Duration
		expectedConnectionTimeout time.Duration
		expectedMaxDelay          time.Duration
		expectedMaxPublishers     int
		expectedPublisherIdleTTL  time.Duration
	}{
		{
			name: "zero_values_apply_all_defaults",
			config: MessagingConfig{
				Broker: BrokerConfig{URL: testAMQPHost},
			},
			expectedReconnectDelay:    defaultReconnectDelay,
			expectedReinitDelay:       defaultReinitDelay,
			expectedResendDelay:       defaultResendDelay,
			expectedConnectionTimeout: defaultConnectionTimeout,
			expectedMaxDelay:          defaultMaxReconnectDelay,
			expectedMaxPublishers:     defaultMaxPublishers,
			expectedPublisherIdleTTL:  defaultPublisherIdleTTL,
		},
		{
			name: "explicit_values_preserved",
			config: MessagingConfig{
				Broker: BrokerConfig{URL: testAMQPHost},
				Reconnect: ReconnectConfig{
					Delay:             10 * time.Second,
					ReinitDelay:       5 * time.Second,
					ResendDelay:       8 * time.Second,
					ConnectionTimeout: 45 * time.Second,
					MaxDelay:          120 * time.Second,
				},
				Publisher: PublisherPoolConfig{
					MaxCached: 100,
					IdleTTL:   5 * time.Minute,
				},
			},
			expectedReconnectDelay:    10 * time.Second,
			expectedReinitDelay:       5 * time.Second,
			expectedResendDelay:       8 * time.Second,
			expectedConnectionTimeout: 45 * time.Second,
			expectedMaxDelay:          120 * time.Second,
			expectedMaxPublishers:     100,
			expectedPublisherIdleTTL:  5 * time.Minute,
		},
		{
			name: "partial_config_applies_missing_defaults",
			config: MessagingConfig{
				Broker: BrokerConfig{URL: testAMQPHost},
				Reconnect: ReconnectConfig{
					Delay: 15 * time.Second, // Only delay set
				},
			},
			expectedReconnectDelay:    15 * time.Second,   // Preserved
			expectedReinitDelay:       defaultReinitDelay, // Defaulted
			expectedResendDelay:       defaultResendDelay, // Defaulted
			expectedConnectionTimeout: defaultConnectionTimeout,
			expectedMaxDelay:          defaultMaxReconnectDelay,
			expectedMaxPublishers:     defaultMaxPublishers,
			expectedPublisherIdleTTL:  defaultPublisherIdleTTL,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateMessaging(&tt.config)
			assert.NoError(t, err)
			assert.Equal(t, tt.expectedReconnectDelay, tt.config.Reconnect.Delay, "Reconnect.Delay mismatch")
			assert.Equal(t, tt.expectedReinitDelay, tt.config.Reconnect.ReinitDelay, "Reconnect.ReinitDelay mismatch")
			assert.Equal(t, tt.expectedResendDelay, tt.config.Reconnect.ResendDelay, "Reconnect.ResendDelay mismatch")
			assert.Equal(t, tt.expectedConnectionTimeout, tt.config.Reconnect.ConnectionTimeout, "Reconnect.ConnectionTimeout mismatch")
			assert.Equal(t, tt.expectedMaxDelay, tt.config.Reconnect.MaxDelay, "Reconnect.MaxDelay mismatch")
			assert.Equal(t, tt.expectedMaxPublishers, tt.config.Publisher.MaxCached, "Publisher.MaxCached mismatch")
			assert.Equal(t, tt.expectedPublisherIdleTTL, tt.config.Publisher.IdleTTL, "Publisher.IdleTTL mismatch")
		})
	}
}

func TestApplyMessagingDefaultsNegativeValues(t *testing.T) {
	tests := []struct {
		name          string
		config        MessagingConfig
		errorContains string
	}{
		{
			name: "negative_reconnect_delay_rejected",
			config: MessagingConfig{
				Broker:    BrokerConfig{URL: testAMQPHost},
				Reconnect: ReconnectConfig{Delay: -1 * time.Second},
			},
			errorContains: "messaging.reconnect.delay",
		},
		{
			name: "negative_max_publishers_rejected",
			config: MessagingConfig{
				Broker:    BrokerConfig{URL: testAMQPHost},
				Publisher: PublisherPoolConfig{MaxCached: -1},
			},
			errorContains: "messaging.publisher.max_cached",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateMessaging(&tt.config)
			assertValidationError(t, err, tt.errorContains)
		})
	}
}

func TestApplyCacheManagerDefaults(t *testing.T) {
	tests := []struct {
		name                    string
		config                  CacheConfig
		expectedMaxSize         int
		expectedIdleTTL         time.Duration
		expectedCleanupInterval time.Duration
	}{
		{
			name: "zero_values_apply_all_defaults",
			config: CacheConfig{
				Enabled: true,
				Type:    "redis",
				Redis:   RedisConfig{Host: "localhost", Port: 6379, PoolSize: 10},
			},
			expectedMaxSize:         defaultCacheMaxSize,
			expectedIdleTTL:         defaultCacheIdleTTL,
			expectedCleanupInterval: defaultCacheCleanupInterval,
		},
		{
			name: "explicit_values_preserved",
			config: CacheConfig{
				Enabled: true,
				Type:    "redis",
				Redis:   RedisConfig{Host: "localhost", Port: 6379, PoolSize: 10},
				Manager: CacheManagerConfig{
					MaxSize:         200,
					IdleTTL:         30 * time.Minute,
					CleanupInterval: 10 * time.Minute,
				},
			},
			expectedMaxSize:         200,
			expectedIdleTTL:         30 * time.Minute,
			expectedCleanupInterval: 10 * time.Minute,
		},
		{
			name: "partial_config_applies_missing_defaults",
			config: CacheConfig{
				Enabled: true,
				Type:    "redis",
				Redis:   RedisConfig{Host: "localhost", Port: 6379, PoolSize: 10},
				Manager: CacheManagerConfig{
					MaxSize: 50, // Only max_size set
				},
			},
			expectedMaxSize:         50,                          // Preserved
			expectedIdleTTL:         defaultCacheIdleTTL,         // Defaulted
			expectedCleanupInterval: defaultCacheCleanupInterval, // Defaulted
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateCache(&tt.config)
			assert.NoError(t, err)
			assert.Equal(t, tt.expectedMaxSize, tt.config.Manager.MaxSize, "Manager.MaxSize mismatch")
			assert.Equal(t, tt.expectedIdleTTL, tt.config.Manager.IdleTTL, "Manager.IdleTTL mismatch")
			assert.Equal(t, tt.expectedCleanupInterval, tt.config.Manager.CleanupInterval, "Manager.CleanupInterval mismatch")
		})
	}
}

func TestApplyCacheManagerDefaultsNegativeValues(t *testing.T) {
	tests := []struct {
		name          string
		config        CacheConfig
		errorContains string
	}{
		{
			name: "negative_max_size_rejected",
			config: CacheConfig{
				Enabled: true,
				Type:    "redis",
				Redis:   RedisConfig{Host: "localhost", Port: 6379, PoolSize: 10},
				Manager: CacheManagerConfig{MaxSize: -1},
			},
			errorContains: "cache.manager.max_size",
		},
		{
			name: "negative_idle_ttl_rejected",
			config: CacheConfig{
				Enabled: true,
				Type:    "redis",
				Redis:   RedisConfig{Host: "localhost", Port: 6379, PoolSize: 10},
				Manager: CacheManagerConfig{IdleTTL: -1 * time.Minute},
			},
			errorContains: "cache.manager.idle_ttl",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateCache(&tt.config)
			assertValidationError(t, err, tt.errorContains)
		})
	}
}

func TestApplyStartupDefaults(t *testing.T) {
	tests := []struct {
		name                  string
		config                StartupConfig
		expectedTimeout       time.Duration
		expectedDatabase      time.Duration
		expectedMessaging     time.Duration
		expectedCache         time.Duration
		expectedObservability time.Duration
	}{
		{
			name:                  "zero_values_apply_all_defaults",
			config:                StartupConfig{},
			expectedTimeout:       defaultStartupTimeout,
			expectedDatabase:      defaultStartupDatabaseTimeout,
			expectedMessaging:     defaultStartupMessagingTimeout,
			expectedCache:         defaultStartupCacheTimeout,
			expectedObservability: defaultStartupObservabilityTimeout,
		},
		{
			name: "explicit_values_preserved",
			config: StartupConfig{
				Timeout:       20 * time.Second,
				Database:      30 * time.Second,
				Messaging:     25 * time.Second,
				Cache:         10 * time.Second,
				Observability: 45 * time.Second,
			},
			expectedTimeout:       20 * time.Second,
			expectedDatabase:      30 * time.Second,
			expectedMessaging:     25 * time.Second,
			expectedCache:         10 * time.Second,
			expectedObservability: 45 * time.Second,
		},
		{
			name: "partial_config_applies_missing_defaults",
			config: StartupConfig{
				Database: 30 * time.Second, // Only database set
			},
			expectedTimeout:       defaultStartupTimeout, // Defaulted
			expectedDatabase:      30 * time.Second,      // Preserved
			expectedMessaging:     defaultStartupMessagingTimeout,
			expectedCache:         defaultStartupCacheTimeout,
			expectedObservability: defaultStartupObservabilityTimeout,
		},
		{
			name: "global_timeout_used_as_fallback",
			config: StartupConfig{
				Timeout: 30 * time.Second, // Global set, all components unset
			},
			expectedTimeout:       30 * time.Second, // Preserved
			expectedDatabase:      30 * time.Second, // Inherits from global
			expectedMessaging:     30 * time.Second, // Inherits from global
			expectedCache:         30 * time.Second, // Inherits from global
			expectedObservability: 30 * time.Second, // Inherits from global
		},
		{
			name: "explicit_component_overrides_global",
			config: StartupConfig{
				Timeout:  30 * time.Second,
				Database: 45 * time.Second, // Explicit override
				Cache:    8 * time.Second,  // Explicit override
			},
			expectedTimeout:       30 * time.Second, // Preserved
			expectedDatabase:      45 * time.Second, // Explicit, preserved
			expectedMessaging:     30 * time.Second, // Inherits from global
			expectedCache:         8 * time.Second,  // Explicit, preserved
			expectedObservability: 30 * time.Second, // Inherits from global
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := applyStartupDefaults(&tt.config)
			assert.NoError(t, err)
			assert.Equal(t, tt.expectedTimeout, tt.config.Timeout, "Timeout mismatch")
			assert.Equal(t, tt.expectedDatabase, tt.config.Database, "Database mismatch")
			assert.Equal(t, tt.expectedMessaging, tt.config.Messaging, "Messaging mismatch")
			assert.Equal(t, tt.expectedCache, tt.config.Cache, "Cache mismatch")
			assert.Equal(t, tt.expectedObservability, tt.config.Observability, "Observability mismatch")
		})
	}
}

func TestApplyStartupDefaultsNegativeValues(t *testing.T) {
	tests := []struct {
		name          string
		config        StartupConfig
		errorContains string
	}{
		{
			name:          "negative_timeout_rejected",
			config:        StartupConfig{Timeout: -1 * time.Second},
			errorContains: "app.startup.timeout",
		},
		{
			name:          "negative_database_rejected",
			config:        StartupConfig{Database: -1 * time.Second},
			errorContains: "app.startup.database",
		},
		{
			name:          "negative_observability_rejected",
			config:        StartupConfig{Observability: -1 * time.Second},
			errorContains: "app.startup.observability",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := applyStartupDefaults(&tt.config)
			assertValidationError(t, err, tt.errorContains)
		})
	}
}

func TestValidateOracleFields(t *testing.T) {
	tests := []struct {
		name          string
		config        DatabaseConfig
		expectError   bool
		errorContains string
	}{
		{
			name: "valid Oracle config with service name",
			config: DatabaseConfig{
				Type:     Oracle,
				Host:     testOracleHost,
				Port:     1521,
				Username: "oracleuser",
				Oracle: OracleConfig{
					Service: ServiceConfig{
						Name: "XEPDB1",
					},
				},
			},
			expectError: false,
		},
		{
			name: "valid Oracle config with SID",
			config: DatabaseConfig{
				Type:     Oracle,
				Host:     testOracleHost,
				Port:     1521,
				Username: "oracleuser",
				Oracle: OracleConfig{
					Service: ServiceConfig{
						SID: "XE",
					},
				},
			},
			expectError: false,
		},
		{
			name: "valid Oracle config with database name",
			config: DatabaseConfig{
				Type:     Oracle,
				Host:     testOracleHost,
				Port:     1521,
				Database: "XE",
				Username: "oracleuser",
			},
			expectError: false,
		},
		{
			name: "Oracle config with no connection identifier",
			config: DatabaseConfig{
				Type:     Oracle,
				Host:     testOracleHost,
				Port:     1521,
				Username: "oracleuser",
				// No Service.Name, SID, or Database
			},
			expectError:   true,
			errorContains: oracleConnectionIdentifier,
		},
		{
			name: "Oracle config with service name and SID",
			config: DatabaseConfig{
				Type:     Oracle,
				Host:     testOracleHost,
				Port:     1521,
				Username: "oracleuser",
				Oracle: OracleConfig{
					Service: ServiceConfig{
						Name: "XEPDB1",
						SID:  "XE",
					},
				},
			},
			expectError:   true,
			errorContains: oracleConnectionIdentifier,
		},
		{
			name: "Oracle config with service name and database name",
			config: DatabaseConfig{
				Type:     Oracle,
				Host:     testOracleHost,
				Port:     1521,
				Database: "XE",
				Username: "oracleuser",
				Oracle: OracleConfig{
					Service: ServiceConfig{
						Name: "XEPDB1",
					},
				},
			},
			expectError:   true,
			errorContains: oracleConnectionIdentifier,
		},
		{
			name: "Oracle config with SID and database name",
			config: DatabaseConfig{
				Type:     Oracle,
				Host:     testOracleHost,
				Port:     1521,
				Database: "XE",
				Username: "oracleuser",
				Oracle: OracleConfig{
					Service: ServiceConfig{
						SID: "XE",
					},
				},
			},
			expectError:   true,
			errorContains: oracleConnectionIdentifier,
		},
		{
			name: "Oracle config with all three connection identifiers",
			config: DatabaseConfig{
				Type:     Oracle,
				Host:     testOracleHost,
				Port:     1521,
				Database: "XE",
				Username: "oracleuser",
				Oracle: OracleConfig{
					Service: ServiceConfig{
						Name: "XEPDB1",
						SID:  "XE",
					},
				},
			},
			expectError:   true,
			errorContains: oracleConnectionIdentifier,
		},
		{
			name: "non-Oracle type should not validate Oracle fields",
			config: DatabaseConfig{
				Type:     PostgreSQL,
				Host:     "localhost",
				Port:     5432,
				Database: "testdb",
				Username: "testuser",
				Oracle: OracleConfig{
					Service: ServiceConfig{
						Name: "XEPDB1", // This should be ignored for PostgreSQL
						SID:  "XE",     // This should be ignored for PostgreSQL
					},
				},
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

func TestValidateOracleWithConnectionString(t *testing.T) {
	tests := []struct {
		name          string
		config        DatabaseConfig
		expectError   bool
		errorContains string
	}{
		{
			name: "valid Oracle with connection string and valid service name",
			config: DatabaseConfig{
				Type:             Oracle,
				ConnectionString: testOracleConnectionString,
				Oracle: OracleConfig{
					Service: ServiceConfig{
						Name: "XEPDB1",
					},
				},
			},
			expectError: false,
		},
		{
			name: "Oracle with connection string but multiple identifiers",
			config: DatabaseConfig{
				Type:             Oracle,
				ConnectionString: testOracleConnectionString,
				Oracle: OracleConfig{
					Service: ServiceConfig{
						Name: "XEPDB1",
						SID:  "XE",
					},
				},
			},
			expectError:   true,
			errorContains: oracleConnectionIdentifier,
		},
		{
			name: "Oracle with connection string but no identifiers",
			config: DatabaseConfig{
				Type:             Oracle,
				ConnectionString: testOracleConnectionString,
				// No Service.Name, SID, or Database
			},
			expectError:   true,
			errorContains: oracleConnectionIdentifier,
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

// =============================================================================
// Test Helper Functions
// =============================================================================

// createValidAppConfig returns a valid AppConfig for testing
func createValidAppConfig() AppConfig {
	return AppConfig{
		Name:    testAppName,
		Version: testAppVersion,
		Env:     EnvDevelopment,
		Rate:    RateConfig{Limit: 100},
	}
}

// createValidServerConfig returns a valid ServerConfig for testing
func createValidServerConfig() ServerConfig {
	return ServerConfig{
		Port: 8080,
		Timeout: TimeoutConfig{
			Read:       15 * time.Second,
			Write:      30 * time.Second,
			Middleware: 5 * time.Second,
			Shutdown:   10 * time.Second,
		},
	}
}

// createValidDatabaseConfig returns a valid DatabaseConfig for testing
func createValidDatabaseConfig() DatabaseConfig {
	return DatabaseConfig{
		Type:     PostgreSQL,
		Host:     "localhost",
		Port:     5432,
		Database: "testdb",
		Username: "testuser",
		Pool: PoolConfig{
			Max: PoolMaxConfig{
				Connections: 25,
			},
		},
	}
}

// createValidLogConfig returns a valid LogConfig for testing
func createValidLogConfig() LogConfig {
	return LogConfig{
		Level: "info",
	}
}

// createValidFullConfig returns a complete valid Config for testing
func createValidFullConfig() *Config {
	return &Config{
		App:      createValidAppConfig(),
		Server:   createValidServerConfig(),
		Database: createValidDatabaseConfig(),
		Log:      createValidLogConfig(),
	}
}

// =============================================================================
// Multitenant Validation Tests
// =============================================================================

func TestIsMessagingConfigured(t *testing.T) {
	tests := []struct {
		name     string
		config   MessagingConfig
		expected bool
	}{
		{
			name:     "empty_config_not_configured",
			config:   MessagingConfig{},
			expected: false,
		},
		{
			name: "broker_url_configured",
			config: MessagingConfig{
				Broker: BrokerConfig{
					URL: testAMQPHost,
				},
			},
			expected: true,
		},
		{
			name: "broker_url_with_virtualhost",
			config: MessagingConfig{
				Broker: BrokerConfig{
					URL:         testAMQPHost,
					VirtualHost: "/test",
				},
			},
			expected: true,
		},
		{
			name: "empty_broker_url_not_configured",
			config: MessagingConfig{
				Broker: BrokerConfig{
					URL: "",
				},
			},
			expected: false,
		},
		{
			name: "whitespace_broker_url_is_configured",
			config: MessagingConfig{
				Broker: BrokerConfig{
					URL: "   ",
				},
			},
			expected: true, // Whitespace is still considered configured
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := IsMessagingConfigured(&tt.config)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestValidateMultitenantDisabled(t *testing.T) {
	mtConfig := &MultitenantConfig{
		Enabled: false,
	}
	dbConfig := &DatabaseConfig{
		Type: PostgreSQL,
		Host: "localhost",
		Port: 5432,
	}
	msgConfig := &MessagingConfig{
		Broker: BrokerConfig{
			URL: testAMQPHost,
		},
	}

	sourceConfig := &SourceConfig{Type: SourceTypeStatic}
	err := validateMultitenant(mtConfig, dbConfig, msgConfig, sourceConfig)
	assert.NoError(t, err, "Validation should pass when multitenant is disabled")
}

func TestValidateMultitenantSuccess(t *testing.T) {
	tests := []struct {
		name         string
		mtConfig     *MultitenantConfig
		dbConfig     *DatabaseConfig
		msgConfig    *MessagingConfig
		sourceConfig *SourceConfig
	}{
		{
			name: "valid_header_resolver",
			mtConfig: &MultitenantConfig{
				Enabled: true,
				Resolver: ResolverConfig{
					Type:   "header",
					Header: testTenantHeader,
				},
				Limits: LimitsConfig{
					Tenants: 100,
				},
				Tenants: makeSampleTenants(),
			},
			dbConfig:  &DatabaseConfig{},  // Empty for multitenant
			msgConfig: &MessagingConfig{}, // Empty for multitenant
		},
		{
			name: "valid_subdomain_resolver",
			mtConfig: &MultitenantConfig{
				Enabled: true,
				Resolver: ResolverConfig{
					Type:   "subdomain",
					Domain: testDomain,
				},
				Limits: LimitsConfig{
					Tenants: 50,
				},
				Tenants: makeSampleTenants(),
			},
			dbConfig:  &DatabaseConfig{},
			msgConfig: &MessagingConfig{},
		},
		{
			name: "valid_composite_resolver",
			mtConfig: &MultitenantConfig{
				Enabled: true,
				Resolver: ResolverConfig{
					Type:    "composite",
					Header:  testTenantHeader,
					Domain:  testDomain,
					Proxies: true,
				},
				Limits: LimitsConfig{
					Tenants: 1000,
				},
				Tenants: makeSampleTenants(),
			},
			dbConfig:  &DatabaseConfig{},
			msgConfig: &MessagingConfig{},
		},
		{
			name: "tenants_without_messaging",
			mtConfig: &MultitenantConfig{
				Enabled: true,
				Resolver: ResolverConfig{
					Type:   "header",
					Header: testTenantHeader,
				},
				Limits: LimitsConfig{
					Tenants: 100,
				},
				Tenants: map[string]TenantEntry{
					tenantA: {
						Database: DatabaseConfig{
							Type:     PostgreSQL,
							Host:     testTenantDBHost,
							Port:     5432,
							Database: "tenant_a",
							Username: "tenant_user",
						},
						Messaging: TenantMessagingConfig{URL: ""}, // No messaging
					},
					"tenant-b": {
						Database: DatabaseConfig{
							Type:     PostgreSQL,
							Host:     "tenant-b.db.local",
							Port:     5432,
							Database: "tenant_b",
							Username: "tenant_user",
						},
						Messaging: TenantMessagingConfig{URL: ""}, // No messaging
					},
				},
			},
			dbConfig:  &DatabaseConfig{},
			msgConfig: &MessagingConfig{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sourceConfig := tt.sourceConfig
			if sourceConfig == nil {
				sourceConfig = &SourceConfig{Type: SourceTypeStatic}
			}
			err := validateMultitenant(tt.mtConfig, tt.dbConfig, tt.msgConfig, sourceConfig)
			assert.NoError(t, err)
		})
	}
}

func TestValidateMultitenantFailures(t *testing.T) {
	tests := []struct {
		name          string
		mtConfig      *MultitenantConfig
		dbConfig      *DatabaseConfig
		msgConfig     *MessagingConfig
		sourceConfig  *SourceConfig
		expectedError string
	}{
		{
			name: "invalid_resolver_type",
			mtConfig: &MultitenantConfig{
				Enabled: true,
				Resolver: ResolverConfig{
					Type: "invalid",
				},
				Tenants: makeSampleTenants(),
			},
			dbConfig:      &DatabaseConfig{},
			msgConfig:     &MessagingConfig{},
			expectedError: "multitenant.resolver.type",
		},
		{
			name: "invalid_limits_too_many_tenants",
			mtConfig: &MultitenantConfig{
				Enabled: true,
				Resolver: ResolverConfig{
					Type: "header",
				},
				Limits: LimitsConfig{
					Tenants: 1001, // Exceeds maximum
				},
				Tenants: makeSampleTenants(),
			},
			dbConfig:      &DatabaseConfig{},
			msgConfig:     &MessagingConfig{},
			expectedError: "multitenant.limits.tenants",
		},
		{
			name: "database_configured_with_multitenant",
			mtConfig: &MultitenantConfig{
				Enabled: true,
				Resolver: ResolverConfig{
					Type: "header",
				},
				Limits: LimitsConfig{
					Tenants: 100,
				},
				Tenants: makeSampleTenants(),
			},
			dbConfig: &DatabaseConfig{
				Host: "localhost", // This makes it configured
				Type: PostgreSQL,
			},
			msgConfig:     &MessagingConfig{},
			expectedError: "database",
		},
		{
			name: "messaging_configured_with_multitenant",
			mtConfig: &MultitenantConfig{
				Enabled: true,
				Resolver: ResolverConfig{
					Type: "header",
				},
				Limits: LimitsConfig{
					Tenants: 100,
				},
				Tenants: makeSampleTenants(),
			},
			dbConfig: &DatabaseConfig{},
			msgConfig: &MessagingConfig{
				Broker: BrokerConfig{
					URL: testAMQPHost, // This makes it configured
				},
			},
			expectedError: "messaging",
		},
		{
			name: "inconsistent_messaging_configuration",
			mtConfig: &MultitenantConfig{
				Enabled:  true,
				Resolver: ResolverConfig{Type: "header"},
				Limits:   LimitsConfig{Tenants: 100},
				Tenants: map[string]TenantEntry{
					tenantA: {
						Database: DatabaseConfig{
							Type:     PostgreSQL,
							Host:     testTenantDBHost,
							Port:     5432,
							Database: "tenant_a",
							Username: "tenant_user",
						},
						Messaging: TenantMessagingConfig{URL: "amqp://tenant-a"}, // Has messaging
					},
					"tenant-b": {
						Database: DatabaseConfig{
							Type:     PostgreSQL,
							Host:     "tenant-b.db.local",
							Port:     5432,
							Database: "tenant_b",
							Username: "tenant_user",
						},
						Messaging: TenantMessagingConfig{URL: ""}, // No messaging
					},
				},
			},
			dbConfig:      &DatabaseConfig{},
			msgConfig:     &MessagingConfig{},
			expectedError: "multitenant.tenants messaging",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sourceConfig := tt.sourceConfig
			if sourceConfig == nil {
				sourceConfig = &SourceConfig{Type: SourceTypeStatic}
			}
			err := validateMultitenant(tt.mtConfig, tt.dbConfig, tt.msgConfig, sourceConfig)
			assert.Error(t, err)
			assert.Contains(t, err.Error(), tt.expectedError)
		})
	}
}

func TestValidateMultitenantResolver(t *testing.T) {
	tests := []struct {
		name           string
		config         ResolverConfig
		expectError    bool
		errorContains  string
		expectedHeader string // Check default header is set
	}{
		{
			name: "valid_header_resolver",
			config: ResolverConfig{
				Type:   "header",
				Header: "X-Custom-Tenant",
			},
			expectError: false,
		},
		{
			name: "header_resolver_gets_default_header",
			config: ResolverConfig{
				Type: "header",
				// No header specified, should get default
			},
			expectError:    false,
			expectedHeader: testTenantHeader,
		},
		{
			name: "valid_subdomain_resolver",
			config: ResolverConfig{
				Type:   "subdomain",
				Domain: testDomain,
			},
			expectError: false,
		},
		{
			name: "valid_composite_resolver",
			config: ResolverConfig{
				Type:    "composite",
				Header:  testTenantHeader,
				Domain:  testDomain,
				Proxies: true,
			},
			expectError: false,
		},
		{
			name: "invalid_resolver_type",
			config: ResolverConfig{
				Type: "invalid",
			},
			expectError:   true,
			errorContains: "multitenant.resolver.type",
		},
		{
			name: "subdomain_missing_domain",
			config: ResolverConfig{
				Type: "subdomain",
				// Missing domain
			},
			expectError:   true,
			errorContains: "multitenant.resolver.domain",
		},
		{
			name: "subdomain_domain_without_leading_dot",
			config: ResolverConfig{
				Type:   "subdomain",
				Domain: "api.example.com", // Will be normalized to .api.example.com
			},
			expectError: false, // Now accepts and normalizes domains without leading dot
		},
		{
			name: "composite_missing_domain",
			config: ResolverConfig{
				Type:   "composite",
				Header: testTenantHeader,
				// Missing domain
			},
			expectError:   true,
			errorContains: "multitenant.resolver.domain",
		},
		{
			name: "composite_domain_without_leading_dot",
			config: ResolverConfig{
				Type:   "composite",
				Header: testTenantHeader,
				Domain: "api.example.com", // Will be normalized to .api.example.com
			},
			expectError: false, // Now accepts and normalizes domains without leading dot
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateMultitenantResolver(&tt.config)
			if tt.expectError {
				assert.Error(t, err)
				assert.Contains(t, err.Error(), tt.errorContains)
			} else {
				assert.NoError(t, err)
				// Check if default header was set
				if tt.expectedHeader != "" {
					assert.Equal(t, tt.expectedHeader, tt.config.Header)
				}
			}
		})
	}
}

func TestValidateMultitenantLimits(t *testing.T) {
	t.Run("defaults when zero", func(t *testing.T) {
		cfg := LimitsConfig{Tenants: 0}
		err := validateMultitenantLimits(&cfg)
		assert.NoError(t, err)
		assert.Equal(t, 100, cfg.Tenants)
	})

	t.Run("defaults when negative", func(t *testing.T) {
		cfg := LimitsConfig{Tenants: -1}
		err := validateMultitenantLimits(&cfg)
		assert.NoError(t, err)
		assert.Equal(t, 100, cfg.Tenants)
	})

	t.Run("supports upper bound", func(t *testing.T) {
		cfg := LimitsConfig{Tenants: 1000}
		err := validateMultitenantLimits(&cfg)
		assert.NoError(t, err)
		assert.Equal(t, 1000, cfg.Tenants)
	})

	t.Run("rejects exceeding upper bound", func(t *testing.T) {
		cfg := LimitsConfig{Tenants: 1001}
		err := validateMultitenantLimits(&cfg)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "multitenant.limits.tenants cannot exceed 1000")
	})
}

func TestValidateSourceConfig(t *testing.T) {
	tests := []struct {
		name        string
		sourceType  string
		expectError bool
	}{
		{
			name:        "valid_static",
			sourceType:  SourceTypeStatic,
			expectError: false,
		},
		{
			name:        "valid_dynamic",
			sourceType:  SourceTypeDynamic,
			expectError: false,
		},
		{
			name:        "invalid_type",
			sourceType:  "invalid",
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &SourceConfig{Type: tt.sourceType}
			err := validateSourceConfig(cfg)
			if tt.expectError {
				assert.Error(t, err)
				assert.Contains(t, err.Error(), "source.type")
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestValidateMultitenantDynamicSource(t *testing.T) {
	tests := []struct {
		name         string
		mtConfig     *MultitenantConfig
		sourceConfig *SourceConfig
		expectError  bool
		errorText    string
	}{
		{
			name: "dynamic_source_without_tenants",
			mtConfig: &MultitenantConfig{
				Enabled: true,
				Resolver: ResolverConfig{
					Type:   "header",
					Header: testTenantHeader,
				},
				Limits: LimitsConfig{
					Tenants: 100,
				},
				// No tenants - loaded dynamically
			},
			sourceConfig: &SourceConfig{Type: SourceTypeDynamic},
			expectError:  false,
		},
		{
			name: "dynamic_source_with_tenants",
			mtConfig: &MultitenantConfig{
				Enabled: true,
				Resolver: ResolverConfig{
					Type:   "header",
					Header: testTenantHeader,
				},
				Limits: LimitsConfig{
					Tenants: 100,
				},
				Tenants: makeSampleTenants(), // Tenants provided but ignored
			},
			sourceConfig: &SourceConfig{Type: SourceTypeDynamic},
			expectError:  false, // Should not error, just ignored
		},
		{
			name: "static_source_empty_tenants",
			mtConfig: &MultitenantConfig{
				Enabled: true,
				Resolver: ResolverConfig{
					Type:   "header",
					Header: testTenantHeader,
				},
				Limits: LimitsConfig{
					Tenants: 100,
				},
				Tenants: map[string]TenantEntry{}, // Empty map
			},
			sourceConfig: &SourceConfig{Type: SourceTypeStatic},
			expectError:  true,
			errorText:    "empty map provided",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateMultitenant(tt.mtConfig, &DatabaseConfig{}, &MessagingConfig{}, tt.sourceConfig)
			if tt.expectError {
				assert.Error(t, err)
				assert.Contains(t, err.Error(), tt.errorText)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestValidateCacheDisabled(t *testing.T) {
	cfg := CacheConfig{Enabled: false}
	err := validateCache(&cfg)
	assert.NoError(t, err)
}

func TestValidateCacheSuccess(t *testing.T) {
	cfg := CacheConfig{
		Enabled: true,
		Type:    "redis",
		Redis: RedisConfig{
			Host:            "localhost",
			Port:            6379,
			Password:        "secret",
			Database:        0,
			PoolSize:        10,
			DialTimeout:     5 * time.Second,
			ReadTimeout:     3 * time.Second,
			WriteTimeout:    3 * time.Second,
			MaxRetries:      3,
			MinRetryBackoff: 8 * time.Millisecond,
			MaxRetryBackoff: 512 * time.Millisecond,
		},
	}

	err := validateCache(&cfg)
	assert.NoError(t, err)
}

func TestValidateCacheTypeFailures(t *testing.T) {
	tests := []struct {
		name          string
		cacheType     string
		expectedError string
	}{
		{
			name:          "invalid_type",
			cacheType:     "memcached",
			expectedError: cacheTypeField,
		},
		{
			name:          "empty_type",
			cacheType:     "",
			expectedError: cacheTypeField,
		},
		{
			name:          "uppercase_type",
			cacheType:     "REDIS",
			expectedError: cacheTypeField,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := CacheConfig{
				Enabled: true,
				Type:    tt.cacheType,
			}

			err := validateCache(&cfg)
			assert.Error(t, err)
			assert.Contains(t, err.Error(), tt.expectedError)
		})
	}
}

func TestValidateRedisCacheFailures(t *testing.T) {
	tests := []struct {
		name          string
		redis         RedisConfig
		expectedError string
	}{
		{
			name: "missing_host",
			redis: RedisConfig{
				Host: "",
				Port: 6379,
			},
			expectedError: "cache.redis.host",
		},
		{
			name: "invalid_port_zero",
			redis: RedisConfig{
				Host: "localhost",
				Port: 0,
			},
			expectedError: redisPortField,
		},
		{
			name: "invalid_port_negative",
			redis: RedisConfig{
				Host: "localhost",
				Port: -1,
			},
			expectedError: redisPortField,
		},
		{
			name: "invalid_port_too_high",
			redis: RedisConfig{
				Host: "localhost",
				Port: 99999,
			},
			expectedError: redisPortField,
		},
		{
			name: "invalid_database_negative",
			redis: RedisConfig{
				Host:     "localhost",
				Port:     6379,
				Database: -1,
			},
			expectedError: "cache.redis.database",
		},
		{
			name: "invalid_database_too_high",
			redis: RedisConfig{
				Host:     "localhost",
				Port:     6379,
				Database: 16,
			},
			expectedError: "cache.redis.database",
		},
		{
			name: "invalid_pool_size_zero",
			redis: RedisConfig{
				Host:     "localhost",
				Port:     6379,
				PoolSize: 0,
			},
			expectedError: "cache.redis.poolsize",
		},
		{
			name: "invalid_pool_size_negative",
			redis: RedisConfig{
				Host:     "localhost",
				Port:     6379,
				PoolSize: -1,
			},
			expectedError: "cache.redis.poolsize",
		},
		{
			name: "invalid_dial_timeout_negative",
			redis: RedisConfig{
				Host:        "localhost",
				Port:        6379,
				PoolSize:    10,
				DialTimeout: -1 * time.Second,
			},
			expectedError: "cache.redis.dialtimeout",
		},
		{
			name: "invalid_read_timeout_too_negative",
			redis: RedisConfig{
				Host:        "localhost",
				Port:        6379,
				PoolSize:    10,
				ReadTimeout: -2 * time.Second,
			},
			expectedError: "cache.redis.readtimeout",
		},
		{
			name: "invalid_write_timeout_too_negative",
			redis: RedisConfig{
				Host:         "localhost",
				Port:         6379,
				PoolSize:     10,
				WriteTimeout: -2 * time.Second,
			},
			expectedError: "cache.redis.writetimeout",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := CacheConfig{
				Enabled: true,
				Type:    "redis",
				Redis:   tt.redis,
			}

			err := validateCache(&cfg)
			assert.Error(t, err)
			assert.Contains(t, err.Error(), tt.expectedError)
		})
	}
}

func TestValidateRedisCacheEdgeCases(t *testing.T) {
	tests := []struct {
		name  string
		redis RedisConfig
		valid bool
	}{
		{
			name: "read_timeout_disabled",
			redis: RedisConfig{
				Host:        "localhost",
				Port:        6379,
				PoolSize:    10,
				ReadTimeout: -1,
			},
			valid: true,
		},
		{
			name: "write_timeout_disabled",
			redis: RedisConfig{
				Host:         "localhost",
				Port:         6379,
				PoolSize:     10,
				WriteTimeout: -1,
			},
			valid: true,
		},
		{
			name: "dial_timeout_zero",
			redis: RedisConfig{
				Host:        "localhost",
				Port:        6379,
				PoolSize:    10,
				DialTimeout: 0,
			},
			valid: true,
		},
		{
			name: "database_max_valid",
			redis: RedisConfig{
				Host:     "localhost",
				Port:     6379,
				PoolSize: 10,
				Database: 15,
			},
			valid: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := CacheConfig{
				Enabled: true,
				Type:    "redis",
				Redis:   tt.redis,
			}

			err := validateCache(&cfg)
			if tt.valid {
				assert.NoError(t, err)
			} else {
				assert.Error(t, err)
			}
		})
	}
}

func TestValidateNamedDatabasesSuccess(t *testing.T) {
	tests := []struct {
		name      string
		databases map[string]DatabaseConfig
		mt        MultitenantConfig
	}{
		{
			name:      "nil_databases_map",
			databases: nil,
			mt:        MultitenantConfig{Enabled: false},
		},
		{
			name:      "empty_databases_map",
			databases: map[string]DatabaseConfig{},
			mt:        MultitenantConfig{Enabled: false},
		},
		{
			name: "single_postgresql_database",
			databases: map[string]DatabaseConfig{
				"legacy": {
					Type:     PostgreSQL,
					Host:     "legacy.db.local",
					Port:     5432,
					Database: "legacy_db",
					Username: "legacy_user",
				},
			},
			mt: MultitenantConfig{Enabled: false},
		},
		{
			name: "multiple_databases_mixed_vendors",
			databases: map[string]DatabaseConfig{
				"legacy": {
					Type:     Oracle,
					Host:     testOracleHost,
					Port:     1521,
					Username: "oracle_user",
					Oracle:   OracleConfig{Service: ServiceConfig{Name: "LEGACYDB"}},
				},
				"analytics": {
					Type:     PostgreSQL,
					Host:     "analytics.db.local",
					Port:     5432,
					Database: "analytics_db",
					Username: "analytics_user",
				},
			},
			mt: MultitenantConfig{Enabled: false},
		},
		{
			name: "named_databases_with_multitenant_no_conflict",
			databases: map[string]DatabaseConfig{
				"shared_analytics": {
					Type:     PostgreSQL,
					Host:     "shared.db.local",
					Port:     5432,
					Database: "shared_db",
					Username: "shared_user",
				},
			},
			mt: MultitenantConfig{
				Enabled: true,
				Tenants: map[string]TenantEntry{
					tenantA: { // Different from named database key
						Database: DatabaseConfig{
							Type:     PostgreSQL,
							Host:     testTenantDBHost,
							Port:     5432,
							Database: "tenant_a",
							Username: "tenant_user",
						},
					},
				},
			},
		},
		{
			name: "database_with_connection_string",
			databases: map[string]DatabaseConfig{
				"external": {
					ConnectionString: testConnectionString,
				},
			},
			mt: MultitenantConfig{Enabled: false},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateNamedDatabases(tt.databases, &tt.mt)
			assert.NoError(t, err)
		})
	}
}

func TestValidateNamedDatabasesFailures(t *testing.T) {
	tests := []struct {
		name        string
		databases   map[string]DatabaseConfig
		mt          MultitenantConfig
		errContains string
	}{
		{
			name: "empty_database_name",
			databases: map[string]DatabaseConfig{
				"": {
					Type:     PostgreSQL,
					Host:     dbLocalField,
					Port:     5432,
					Database: "db",
					Username: "user",
				},
			},
			mt:          MultitenantConfig{Enabled: false},
			errContains: "cannot be empty",
		},
		{
			name: "reserved_prefix_in_name",
			databases: map[string]DatabaseConfig{
				"named:legacy": {
					Type:     PostgreSQL,
					Host:     dbLocalField,
					Port:     5432,
					Database: "db",
					Username: "user",
				},
			},
			mt:          MultitenantConfig{Enabled: false},
			errContains: "reserved prefix",
		},
		{
			name: "conflict_with_tenant_id",
			databases: map[string]DatabaseConfig{
				tenantA: { // Same as tenant ID
					Type:     PostgreSQL,
					Host:     dbLocalField,
					Port:     5432,
					Database: "db",
					Username: "user",
				},
			},
			mt: MultitenantConfig{
				Enabled: true,
				Tenants: map[string]TenantEntry{
					tenantA: {
						Database: DatabaseConfig{
							Type:     PostgreSQL,
							Host:     testTenantDBHost,
							Port:     5432,
							Database: "tenant_a",
							Username: "tenant_user",
						},
					},
				},
			},
			errContains: "conflicts with tenant ID",
		},
		{
			name: "incomplete_database_config",
			databases: map[string]DatabaseConfig{
				"incomplete": {
					// Missing required fields
				},
			},
			mt:          MultitenantConfig{Enabled: false},
			errContains: "incomplete",
		},
		{
			name: "invalid_database_type",
			databases: map[string]DatabaseConfig{
				"invalid": {
					Type:     "mysql", // Not supported
					Host:     dbLocalField,
					Port:     3306,
					Database: "db",
					Username: "user",
				},
			},
			mt:          MultitenantConfig{Enabled: false},
			errContains: "not supported",
		},
		{
			name: "invalid_port",
			databases: map[string]DatabaseConfig{
				"badport": {
					Type:     PostgreSQL,
					Host:     dbLocalField,
					Port:     -1,
					Database: "db",
					Username: "user",
				},
			},
			mt:          MultitenantConfig{Enabled: false},
			errContains: "port",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateNamedDatabases(tt.databases, &tt.mt)
			assert.Error(t, err)
			assert.Contains(t, err.Error(), tt.errContains)
		})
	}
}

func TestValidateNamedDatabasesNoConflictWhenMultitenantDisabled(t *testing.T) {
	// When multitenant is disabled, no conflict check is needed
	databases := map[string]DatabaseConfig{
		tenantA: { // Would conflict if multitenant were enabled
			Type:     PostgreSQL,
			Host:     dbLocalField,
			Port:     5432,
			Database: "db",
			Username: "user",
		},
	}
	mt := MultitenantConfig{
		Enabled: false,
		Tenants: map[string]TenantEntry{
			tenantA: {
				Database: DatabaseConfig{},
			},
		},
	}

	err := validateNamedDatabases(databases, &mt)
	assert.NoError(t, err, "no conflict when multitenant is disabled")
}
