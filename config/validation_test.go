package config

import (
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	testConnectionString         = "postgresql://user:pass@localhost/db"
	testBarePostgresConnString   = "postgres://localhost:5432/db"
	testOracleConnectionString   = "oracle://user:pass@localhost:1521/XEPDB1"
	testUnknownSchemeConnString  = "sqlserver://user:pass@localhost:1433/db"
	testOracleHost               = "oracle.example.com"
	testTLSCertFile              = "/etc/ssl/client.crt"
	testTLSKeyFile               = "/etc/ssl/client.key"
	testTLSCAFile                = "/etc/ssl/ca.pem"
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
	appStartupTimeoutField       = "app.startup.timeout"
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
			// Cache enabled with only a host: port/poolsize/timeouts are left
			// at zero values so the shared sample exercises normalizeMultitenant's
			// per-tenant cache validation and default-application path. Without
			// it, every multitenant test would silently skip tenant.Cache and
			// mask the M6 fast-fail/defaulting behavior.
			Cache: CacheConfig{
				Enabled: true,
				Redis:   RedisConfig{Host: "tenant-a.redis.local"},
			},
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
		{
			name: "alias_local_accepted",
			cfg: AppConfig{
				Name:    testAppName,
				Version: testAppVersion,
				Env:     "local",
				Rate:    RateConfig{Limit: 100},
			},
		},
		{
			name: "short_code_tst_accepted",
			cfg: AppConfig{
				Name:    testAppName,
				Version: testAppVersion,
				Env:     "tst",
				Rate:    RateConfig{Limit: 100},
			},
		},
		{
			name: "short_code_prd_accepted",
			cfg: AppConfig{
				Name:    testAppName,
				Version: testAppVersion,
				Env:     "prd",
				Rate:    RateConfig{Limit: 100},
			},
		},
		{
			name: "custom_env_with_hyphen",
			cfg: AppConfig{
				Name:    testAppName,
				Version: testAppVersion,
				Env:     "production-eu",
				Rate:    RateConfig{Limit: 100},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := checkApp(&tt.cfg)
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
			name: "empty_environment",
			cfg: AppConfig{
				Name:    testAppName,
				Version: testAppVersion,
				Env:     "",
				Rate:    RateConfig{Limit: 100},
			},
			expectedError: "app.env",
		},
		{
			name: "uppercase_environment_rejected",
			cfg: AppConfig{
				Name:    testAppName,
				Version: testAppVersion,
				Env:     "Production",
				Rate:    RateConfig{Limit: 100},
			},
			expectedError: "app.env",
		},
		{
			name: "environment_with_space_rejected",
			cfg: AppConfig{
				Name:    testAppName,
				Version: testAppVersion,
				Env:     "stg eu",
				Rate:    RateConfig{Limit: 100},
			},
			expectedError: "app.env",
		},
		{
			name: "leading_digit_rejected",
			cfg: AppConfig{
				Name:    testAppName,
				Version: testAppVersion,
				Env:     "1prod",
				Rate:    RateConfig{Limit: 100},
			},
			expectedError: "app.env",
		},
		{
			name: "environment_too_long_rejected",
			cfg: AppConfig{
				Name:    testAppName,
				Version: testAppVersion,
				Env:     "this-is-an-extremely-long-environment-name-that-exceeds-the-cap",
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
			err := checkApp(&tt.cfg)
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
			err := checkLog(&cfg)
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
			err := checkLog(&tt.cfg)
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

func assertValidationError(t *testing.T, err error, errorContains string) {
	// require, not assert: a nil err would otherwise panic on err.Error() below and
	// take the whole test binary down instead of failing this one case.
	require.Error(t, err)
	assert.Contains(t, err.Error(), errorContains)
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
					MaxSize: 50, // Only maxsize set
				},
			},
			expectedMaxSize:         50,                          // Preserved
			expectedIdleTTL:         defaultCacheIdleTTL,         // Defaulted
			expectedCleanupInterval: defaultCacheCleanupInterval, // Defaulted
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := normalizeCache(&tt.config, false)
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
			errorContains: "cache.manager.maxsize",
		},
		{
			name: "negative_idle_ttl_rejected",
			config: CacheConfig{
				Enabled: true,
				Type:    "redis",
				Redis:   RedisConfig{Host: "localhost", Port: 6379, PoolSize: 10},
				Manager: CacheManagerConfig{IdleTTL: -1 * time.Minute},
			},
			errorContains: "cache.manager.idlettl",
		},
		{
			name: "negative_cleanup_interval_rejected",
			config: CacheConfig{
				Enabled: true,
				Type:    "redis",
				Redis:   RedisConfig{Host: "localhost", Port: 6379, PoolSize: 10},
				Manager: CacheManagerConfig{CleanupInterval: -1 * time.Minute},
			},
			errorContains: "cache.manager.cleanupinterval",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := normalizeCache(&tt.config, false)
			assertValidationError(t, err, tt.errorContains)
		})
	}
}

// TestApplyCacheManagerDefaultsModeAware mirrors TestApplyDatabaseManagerDefaults:
// multi-tenant preserves an unset MaxSize (so app.ManagerConfigBuilder scales the
// pool to the tenant limit), single-tenant stamps the flat default, and negatives
// are rejected in both modes (#668). IdleTTL/CleanupInterval stay mode-independent.
func TestApplyCacheManagerDefaultsModeAware(t *testing.T) {
	t.Run("single_tenant_zero_values_apply_all_defaults", func(t *testing.T) {
		cfg := &CacheConfig{}
		require.NoError(t, applyCacheManagerDefaults(cfg, false))
		assert.Equal(t, defaultCacheMaxSize, cfg.Manager.MaxSize)
		assert.Equal(t, defaultCacheIdleTTL, cfg.Manager.IdleTTL)
		assert.Equal(t, defaultCacheCleanupInterval, cfg.Manager.CleanupInterval)
	})

	t.Run("multi_tenant_zero_preserves_maxsize", func(t *testing.T) {
		cfg := &CacheConfig{}
		require.NoError(t, applyCacheManagerDefaults(cfg, true))
		assert.Zero(t, cfg.Manager.MaxSize)
		assert.Equal(t, defaultCacheIdleTTL, cfg.Manager.IdleTTL)
		assert.Equal(t, defaultCacheCleanupInterval, cfg.Manager.CleanupInterval)
	})

	t.Run("explicit_values_preserved_single", func(t *testing.T) {
		cfg := &CacheConfig{Manager: CacheManagerConfig{MaxSize: 42, IdleTTL: 30 * time.Minute, CleanupInterval: 90 * time.Second}}
		require.NoError(t, applyCacheManagerDefaults(cfg, false))
		assert.Equal(t, 42, cfg.Manager.MaxSize)
		assert.Equal(t, 30*time.Minute, cfg.Manager.IdleTTL)
		assert.Equal(t, 90*time.Second, cfg.Manager.CleanupInterval)
	})

	t.Run("explicit_values_preserved_multi", func(t *testing.T) {
		cfg := &CacheConfig{Manager: CacheManagerConfig{MaxSize: 42, IdleTTL: 30 * time.Minute, CleanupInterval: 90 * time.Second}}
		require.NoError(t, applyCacheManagerDefaults(cfg, true))
		assert.Equal(t, 42, cfg.Manager.MaxSize)
		assert.Equal(t, 30*time.Minute, cfg.Manager.IdleTTL)
		assert.Equal(t, 90*time.Second, cfg.Manager.CleanupInterval)
	})

	t.Run("negative_maxsize_rejected_single", func(t *testing.T) {
		cfg := &CacheConfig{Manager: CacheManagerConfig{MaxSize: -1}}
		assertValidationError(t, applyCacheManagerDefaults(cfg, false), "cache.manager.maxsize")
	})

	t.Run("negative_maxsize_rejected_multi", func(t *testing.T) {
		cfg := &CacheConfig{Manager: CacheManagerConfig{MaxSize: -1}}
		assertValidationError(t, applyCacheManagerDefaults(cfg, true), "cache.manager.maxsize")
	})

	t.Run("negative_idlettl_rejected", func(t *testing.T) {
		cfg := &CacheConfig{Manager: CacheManagerConfig{IdleTTL: -1 * time.Minute}}
		assertValidationError(t, applyCacheManagerDefaults(cfg, false), "cache.manager.idlettl")
	})

	t.Run("negative_cleanupinterval_rejected", func(t *testing.T) {
		cfg := &CacheConfig{Manager: CacheManagerConfig{CleanupInterval: -1 * time.Minute}}
		assertValidationError(t, applyCacheManagerDefaults(cfg, false), "cache.manager.cleanupinterval")
	})
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
			errorContains: appStartupTimeoutField,
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

// =============================================================================
// KeyStore Validation Tests
// =============================================================================

func TestValidateDebugTrustedProxiesRejectsAllInvalid(t *testing.T) {
	cfg := &DebugConfig{TrustedProxies: []string{"garbage", "also-bad"}}
	assertValidationError(t, checkDebug(cfg), "debug.trustedproxies")
}

// TestValidateDebugTrustedProxiesWiredIntoValidate guards that the top-level Validate()
// actually invokes checkDebug — not just that checkDebug works in isolation — so a
// future refactor cannot silently drop the debug-config validation hook.
func TestValidateDebugTrustedProxiesWiredIntoValidate(t *testing.T) {
	cfg := createValidFullConfig()
	cfg.Debug.TrustedProxies = []string{"bad-cidr"}
	err := Validate(cfg)
	assert.ErrorContains(t, err, "debug config:")
	assert.ErrorContains(t, err, "debug.trustedproxies")
}

func TestValidateDebugTrustedProxiesAcceptsValidCases(t *testing.T) {
	tests := []struct {
		name string
		cfg  *DebugConfig
	}{
		{name: "empty_is_valid", cfg: &DebugConfig{}},
		{name: "single_valid", cfg: &DebugConfig{TrustedProxies: []string{"10.0.0.0/8"}}},
		{name: "partial_invalid_keeps_valid", cfg: &DebugConfig{TrustedProxies: []string{"10.0.0.0/8", "bad"}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.NoError(t, checkDebug(tt.cfg))
		})
	}
}

func TestApplyDatabaseManagerDefaults(t *testing.T) {
	t.Run("single_tenant_zero_values_apply_all_defaults", func(t *testing.T) {
		cfg := &DatabaseManagerConfig{}
		require.NoError(t, applyDatabaseManagerDefaults(cfg, false))
		assert.Equal(t, defaultDatabaseManagerMaxSize, cfg.MaxSize)
		assert.Equal(t, defaultDatabaseManagerIdleTTL, cfg.IdleTTL)
		assert.Equal(t, defaultDatabaseManagerCleanupInterval, cfg.CleanupInterval)
	})

	t.Run("multi_tenant_zero_preserves_maxsize_uses_30m", func(t *testing.T) {
		cfg := &DatabaseManagerConfig{}
		require.NoError(t, applyDatabaseManagerDefaults(cfg, true))
		assert.Zero(t, cfg.MaxSize)
		assert.Equal(t, defaultDatabaseManagerIdleTTLMultiTenant, cfg.IdleTTL)
		assert.Equal(t, defaultDatabaseManagerCleanupInterval, cfg.CleanupInterval)
	})

	t.Run("explicit_values_preserved_single", func(t *testing.T) {
		cfg := &DatabaseManagerConfig{MaxSize: 42, IdleTTL: 2 * time.Hour, CleanupInterval: 90 * time.Second}
		require.NoError(t, applyDatabaseManagerDefaults(cfg, false))
		assert.Equal(t, 42, cfg.MaxSize)
		assert.Equal(t, 2*time.Hour, cfg.IdleTTL)
		assert.Equal(t, 90*time.Second, cfg.CleanupInterval)
	})

	t.Run("explicit_values_preserved_multi", func(t *testing.T) {
		cfg := &DatabaseManagerConfig{MaxSize: 42, IdleTTL: 2 * time.Hour, CleanupInterval: 90 * time.Second}
		require.NoError(t, applyDatabaseManagerDefaults(cfg, true))
		assert.Equal(t, 42, cfg.MaxSize)
		assert.Equal(t, 2*time.Hour, cfg.IdleTTL)
		assert.Equal(t, 90*time.Second, cfg.CleanupInterval)
	})

	t.Run("negative_maxsize_rejected_single", func(t *testing.T) {
		cfg := &DatabaseManagerConfig{MaxSize: -1}
		assertValidationError(t, applyDatabaseManagerDefaults(cfg, false), "database.manager.maxsize")
	})

	t.Run("negative_maxsize_rejected_multi", func(t *testing.T) {
		cfg := &DatabaseManagerConfig{MaxSize: -1}
		assertValidationError(t, applyDatabaseManagerDefaults(cfg, true), "database.manager.maxsize")
	})

	t.Run("negative_idlettl_rejected", func(t *testing.T) {
		cfg := &DatabaseManagerConfig{IdleTTL: -1 * time.Second}
		assertValidationError(t, applyDatabaseManagerDefaults(cfg, false), "database.manager.idlettl")
	})

	t.Run("negative_cleanupinterval_rejected", func(t *testing.T) {
		cfg := &DatabaseManagerConfig{CleanupInterval: -1 * time.Second}
		assertValidationError(t, applyDatabaseManagerDefaults(cfg, false), "database.manager.cleanupinterval")
	})
}

func TestValidateAppliesDatabaseManagerDefaultsOnlyToPrimaryDatabase(t *testing.T) {
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
		Log: LogConfig{Level: "info"},
		Database: DatabaseConfig{
			Type: PostgreSQL, Host: "localhost", Port: 5432, Database: "app",
			Username: "user", Password: "longenough-pw",
		},
		Databases: map[string]DatabaseConfig{
			"legacy": {
				Type: PostgreSQL, Host: "localhost", Port: 5432, Database: "legacy",
				Username: "user", Password: "longenough-pw",
			},
		},
	}

	require.NoError(t, Validate(cfg))

	assert.Equal(t, defaultDatabaseManagerMaxSize, cfg.Database.Manager.MaxSize)
	assert.Equal(t, defaultDatabaseManagerIdleTTL, cfg.Database.Manager.IdleTTL)
	assert.Equal(t, defaultDatabaseManagerCleanupInterval, cfg.Database.Manager.CleanupInterval)
	assert.Equal(t, DatabaseManagerConfig{}, cfg.Databases["legacy"].Manager)
}

// TestValidateMultiTenantAppliesDatabaseManagerDefaultsEndToEnd is the wiring-level
// counterpart to the applyDatabaseManagerDefaults unit tests: it pins that Validate()
// threads the multitenant flag through, guarding the #661 MaxCached regression class.
func TestValidateMultiTenantAppliesDatabaseManagerDefaultsEndToEnd(t *testing.T) {
	newCfg := func() *Config {
		return &Config{
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
			Log: LogConfig{Level: "info"},
			Multitenant: MultitenantConfig{
				Enabled: true,
				Resolver: ResolverConfig{
					Type:   ResolverTypeHeader,
					Header: testTenantHeader,
				},
				Tenants: map[string]TenantEntry{
					"acme": {
						Database: DatabaseConfig{
							Type:     PostgreSQL,
							Host:     "acme.db",
							Port:     5432,
							Database: "acme",
							Username: "acme_user",
						},
					},
				},
			},
			Source: SourceConfig{Type: SourceTypeStatic},
		}
	}

	t.Run("unset_manager_keys_get_mode_aware_defaults", func(t *testing.T) {
		cfg := newCfg()
		require.NoError(t, Validate(cfg))

		assert.Zero(t, cfg.Database.Manager.MaxSize)
		assert.Equal(t, defaultDatabaseManagerIdleTTLMultiTenant, cfg.Database.Manager.IdleTTL)
		assert.Equal(t, defaultDatabaseManagerCleanupInterval, cfg.Database.Manager.CleanupInterval)
	})

	t.Run("negative_maxsize_rejected_through_validate", func(t *testing.T) {
		cfg := newCfg()
		cfg.Database.Manager.MaxSize = -1
		err := Validate(cfg)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "database.manager.maxsize")
	})

	t.Run("tenant_database_manager_block_rejected", func(t *testing.T) {
		cfg := newCfg()
		tenant := cfg.Multitenant.Tenants["acme"]
		tenant.Database.Manager = DatabaseManagerConfig{IdleTTL: 5 * time.Minute}
		cfg.Multitenant.Tenants["acme"] = tenant
		err := Validate(cfg)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "acme")
		assert.Contains(t, err.Error(), "manager")
	})
}

// TestValidateMultiTenantAppliesCacheManagerDefaultsEndToEnd pins that Validate()
// threads the multitenant flag into applyCacheManagerDefaults, guarding the #668
// regression where a >100-tenant fleet's cache pool was silently capped at 100.
func TestValidateMultiTenantAppliesCacheManagerDefaultsEndToEnd(t *testing.T) {
	newCfg := func(multitenant bool) *Config {
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
			Log: LogConfig{Level: "info"},
			Cache: CacheConfig{
				Enabled: true,
				Type:    "redis",
				Redis:   RedisConfig{Host: "localhost", Port: 6379, PoolSize: 10},
			},
		}
		if multitenant {
			cfg.Multitenant = MultitenantConfig{
				Enabled:  true,
				Resolver: ResolverConfig{Type: ResolverTypeHeader, Header: testTenantHeader},
				Tenants: map[string]TenantEntry{
					"acme": {
						Database: DatabaseConfig{
							Type:     PostgreSQL,
							Host:     "acme.db",
							Port:     5432,
							Database: "acme",
							Username: "acme_user",
						},
					},
				},
			}
			cfg.Source = SourceConfig{Type: SourceTypeStatic}
		} else {
			cfg.Database = DatabaseConfig{
				Type: PostgreSQL, Host: "localhost", Port: 5432, Database: "app",
				Username: "user", Password: "longenough-pw",
			}
		}
		return cfg
	}

	t.Run("multi_tenant_unset_maxsize_preserved", func(t *testing.T) {
		cfg := newCfg(true)
		require.NoError(t, Validate(cfg))
		assert.Zero(t, cfg.Cache.Manager.MaxSize)
		assert.Equal(t, defaultCacheIdleTTL, cfg.Cache.Manager.IdleTTL)
		assert.Equal(t, defaultCacheCleanupInterval, cfg.Cache.Manager.CleanupInterval)
	})

	t.Run("multi_tenant_negative_maxsize_rejected_through_validate", func(t *testing.T) {
		cfg := newCfg(true)
		cfg.Cache.Manager.MaxSize = -1
		err := Validate(cfg)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "cache.manager.maxsize")
	})

	t.Run("single_tenant_unset_maxsize_stamps_100", func(t *testing.T) {
		cfg := newCfg(false)
		require.NoError(t, Validate(cfg))
		assert.Equal(t, defaultCacheMaxSize, cfg.Cache.Manager.MaxSize)
	})
}

// assertSectionNameRejected collapses the tail every env-reachability case
// shares: a *ConfigError naming the offending KEY PATH and an action telling
// the operator to rename it.
func assertSectionNameRejected(t *testing.T, err error, wantField string) {
	t.Helper()
	require.Error(t, err)
	var cfgErr *ConfigError
	require.ErrorAs(t, err, &cfgErr)
	assert.Equal(t, wantField, cfgErr.Field)
	assert.ErrorContains(t, err, "rename")
}

// TestValidateRejectsAnUnreachableSectionThroughThePublicDoor drives Validate
// itself, not the per-section checker. ADR-090 and [C61.22] both promise that a
// hand-built Config is judged the same way — every construction path calls
// Validate (ADR-064) — and only this test would notice if a phase reorder or an
// early return left the rule unreached.
func TestValidateRejectsAnUnreachableSectionThroughThePublicDoor(t *testing.T) {
	cfg := createValidFullConfig()
	cfg.Databases = map[string]DatabaseConfig{"report_db": createValidDatabaseConfig()}

	err := Validate(cfg)

	require.Error(t, err)
	var cfgErr *ConfigError
	require.ErrorAs(t, err, &cfgErr, "the section error survives Validate's wrapping")
	assert.Equal(t, "databases.report_db", cfgErr.Field)
}

// TestCheckSectionNameGrammar exercises the shared rule directly, at the
// character-class boundary, so the three call sites are left proving only that
// they WIRE it — and its own branch is covered without going through a section.
func TestCheckSectionNameGrammar(t *testing.T) {
	tests := []struct {
		name     string
		section  string
		accepted bool
	}{
		{name: "lowercase", section: "reporting", accepted: true},
		{name: "digits", section: "db2", accepted: true},
		{name: "hyphen", section: "report-db", accepted: true},
		{name: "digits_only", section: "2", accepted: true},
		{name: "hyphen_only", section: "-", accepted: true},
		{name: "empty", section: ""},
		{name: "underscore", section: "report_db"},
		{name: "uppercase", section: "Reporting"},
		{name: "dot", section: "report.db"},
		{name: "space", section: "report db"},
		{name: "non_ascii", section: "reporté"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := checkSectionName("databases."+tt.section, tt.section)
			if tt.accepted {
				require.NoError(t, err)
				return
			}
			assertSectionNameRejected(t, err, "databases."+tt.section)
		})
	}
}

// TestValidateRejectsSectionNamesUnreachableByEnv is the reproducer: the env
// transform lowercases and maps '_' to '.', so a name carrying '_' or an
// uppercase letter cannot be addressed by any environment variable — its
// variable lands on a different key. Names are judged against ^[a-z0-9-]+$ at
// check, which makes the transform injective over every key that survives
// startup.
func TestValidateRejectsSectionNamesUnreachableByEnv(t *testing.T) {
	tests := []struct {
		name      string
		dbName    string
		wantField string
	}{
		{name: "underscore_in_name", dbName: "report_db", wantField: "databases.report_db"},
		{name: "uppercase_in_name", dbName: "Reporting", wantField: "databases.Reporting"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			databases := map[string]DatabaseConfig{
				tt.dbName: {
					Type:     PostgreSQL,
					Host:     dbLocalField,
					Port:     5432,
					Database: "db",
					Username: "user",
				},
			}
			mt := MultitenantConfig{Enabled: false}

			err := checkNamedDatabases(databases, &mt)

			assertSectionNameRejected(t, err, tt.wantField)
		})
	}
}

// TestValidateNamedDatabaseReportsADottedReservedNameAgainstTheParent: a name can
// break two rules at once. The reserved-prefix error embeds the name in its Field
// (databases.<name>), which is exactly what a dotted name makes ambiguous — so the
// dot rule has to win, whichever other rule also matches.
func TestValidateNamedDatabaseReportsADottedReservedNameAgainstTheParent(t *testing.T) {
	databases := map[string]DatabaseConfig{
		NamedDatabasePrefix + ".foo": {
			Type:     PostgreSQL,
			Host:     dbLocalField,
			Port:     5432,
			Database: "db",
			Username: "user",
		},
	}
	mt := MultitenantConfig{Enabled: false}

	err := checkNamedDatabases(databases, &mt)

	require.Error(t, err)
	var cfgErr *ConfigError
	require.ErrorAs(t, err, &cfgErr)
	assert.Equal(t, fieldDatabases, cfgErr.Field, "a dotted name never embeds itself in the Field, whatever else it violates")
	assert.ErrorContains(t, err, "'.'")
}

// TestValidateAcceptsEnvReachableSectionNames pins the other half: the rule
// admits every name the transform round-trips, hyphen included.
func TestValidateAcceptsEnvReachableSectionNames(t *testing.T) {
	for _, name := range []string{"report-db", "reporting", "db2"} {
		t.Run(name, func(t *testing.T) {
			databases := map[string]DatabaseConfig{
				name: {
					Type:     PostgreSQL,
					Host:     dbLocalField,
					Port:     5432,
					Database: "db",
					Username: "user",
				},
			}
			mt := MultitenantConfig{Enabled: false}

			require.NoError(t, checkNamedDatabases(databases, &mt))
		})
	}
}

// TestValidateRejectsSiblingCollisionBeforeItCanHappen pins the silent shape the
// rule exists to prevent: with both a report and a report_db section,
// DATABASES_REPORT_DB_PORT used to land on report while report_db silently kept
// its YAML value. The failure is now at validation, so no resolved value is ever
// read from the wrong section.
func TestValidateRejectsSiblingCollisionBeforeItCanHappen(t *testing.T) {
	entry := DatabaseConfig{
		Type:     PostgreSQL,
		Host:     dbLocalField,
		Port:     5432,
		Database: "db",
		Username: "user",
	}
	databases := map[string]DatabaseConfig{"report": entry, "report_db": entry}
	mt := MultitenantConfig{Enabled: false}

	err := checkNamedDatabases(databases, &mt)

	assertSectionNameRejected(t, err, "databases.report_db")
}

// TestEnvVarToKeyIsUnchangedForEveryReachableName is seam 4: the rule constrains
// which names are legal, never how a variable maps to a key. Every mapping an
// existing deployment relies on must be byte-identical.
func TestEnvVarToKeyIsUnchangedForEveryReachableName(t *testing.T) {
	tests := []struct {
		envVar string
		key    string
	}{
		{envVar: "LOG_SENSITIVEFIELDS", key: "log.sensitivefields"},
		{envVar: "DATABASES_REPORTING_PORT", key: "databases.reporting.port"},
		{envVar: "MULTITENANT_TENANTS_ACME_DATABASE_HOST", key: "multitenant.tenants.acme.database.host"},
		{envVar: "KEYSTORE_KEYS_SIGNING_PUBLIC_FILE", key: "keystore.keys.signing.public.file"},
		{envVar: "MESSAGING_RECONNECT_CONNECTIONTIMEOUT", key: "messaging.reconnect.connectiontimeout"},
		{envVar: "DATABASE_POOL_MAX_CONNECTIONS", key: "database.pool.max.connections"},
	}

	for _, tt := range tests {
		t.Run(tt.envVar, func(t *testing.T) {
			assert.Equal(t, tt.key, envVarToKey(tt.envVar))
		})
	}
}
