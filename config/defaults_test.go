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
			require.Error(t, err)
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

// =============================================================================
// Test Helper Functions
// =============================================================================

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
