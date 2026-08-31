package config

import (
	"encoding/binary"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
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

// TestValidateMultitenantTenantsCacheDefaults proves that an enabled tenant
// cache with a host but no port/poolsize is hardened at startup: Redis
// defaults (port 6379, poolsize 10, timeouts) are applied and persisted back
// to the tenants map, exactly as already done for tenant.Database. Without the
// fix, normalizeMultitenantTenants never touches tenant.Cache, so the raw
// zero-value Redis config reaches the cache client and fails at first request
// instead of at startup.
func TestValidateMultitenantTenantsCacheDefaults(t *testing.T) {
	cfg := &Config{
		App:    createValidAppConfig(),
		Server: createValidServerConfig(),
		Log:    createValidLogConfig(),
		Multitenant: MultitenantConfig{
			Enabled: true,
			Resolver: ResolverConfig{
				Type:   "header",
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
					Cache: CacheConfig{
						Enabled: true,
						// Type, Port and PoolSize intentionally left at zero
						// values: there are no koanf defaults for per-tenant
						// cache keys, so validation must apply them itself.
						Redis: RedisConfig{Host: "acme.redis"},
					},
				},
			},
		},
		Source: SourceConfig{Type: SourceTypeStatic},
	}

	require.NoError(t, Validate(cfg))

	tenant := cfg.Multitenant.Tenants["acme"]
	assert.Equal(t, CacheTypeRedis, tenant.Cache.Type,
		"tenant cache type must default to redis via Validate wiring")
	assert.Equal(t, 6379, tenant.Cache.Redis.Port,
		"tenant cache without explicit port must default to 6379 and persist to the tenants map")
	assert.Equal(t, 10, tenant.Cache.Redis.PoolSize,
		"tenant cache without explicit poolsize must default to 10 and persist to the tenants map")
}

// TestValidateMultitenantTenantsCacheMisconfigFailsFast proves the HARDEN
// posture: a genuinely misconfigured tenant cache (enabled but no host) is
// rejected at startup, not deferred to the first per-request cache access.
func TestValidateMultitenantTenantsCacheMisconfigFailsFast(t *testing.T) {
	cfg := &Config{
		App:    createValidAppConfig(),
		Server: createValidServerConfig(),
		Log:    createValidLogConfig(),
		Multitenant: MultitenantConfig{
			Enabled: true,
			Resolver: ResolverConfig{
				Type:   "header",
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
					Cache: CacheConfig{
						Enabled: true,
						// Host omitted: must fail fast at startup.
					},
				},
			},
		},
		Source: SourceConfig{Type: SourceTypeStatic},
	}

	err := Validate(cfg)
	require.Error(t, err, "enabled tenant cache without a host must fail at startup")
	assert.Contains(t, err.Error(), "cache.redis.host")
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

// normalizeAndCheckResolver runs both halves of the resolver split in phase
// order, for tables whose cases need a fill before the rejection they pin.
func normalizeAndCheckResolver(cfg *ResolverConfig) error {
	normalizeMultitenantResolver(cfg)
	return checkMultitenantResolver(cfg)
}

// normalizeTenantsAndCheckMultitenant runs the tenant half of normalize before
// checkMultitenant: check assumes normalize already ran (per-tenant cache
// defaults included), and these fixtures are hand-built. Only the tenant loop
// runs — the resolver/limits fills would change what the failure tables assert.
func normalizeTenantsAndCheckMultitenant(t *testing.T, mt *MultitenantConfig, db *DatabaseConfig, msg *MessagingConfig, source *SourceConfig) error {
	t.Helper()
	require.NoError(t, normalizeMultitenantTenants(mt.Tenants))
	return checkMultitenant(mt, db, msg, source)
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
	err := checkMultitenant(mtConfig, dbConfig, msgConfig, sourceConfig)
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
					Order:   []string{ResolverTypeSubdomain, ResolverTypeHeader},
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
			name: "valid_path_resolver",
			mtConfig: &MultitenantConfig{
				Enabled: true,
				Resolver: ResolverConfig{
					Type: ResolverTypePath,
					Path: PathResolverConfig{Segment: 2, Prefix: "/itsp"},
				},
				Limits:  LimitsConfig{Tenants: 100},
				Tenants: makeSampleTenants(),
			},
			dbConfig:  &DatabaseConfig{},
			msgConfig: &MessagingConfig{},
		},
		{
			name: "valid_path_resolver_no_prefix",
			mtConfig: &MultitenantConfig{
				Enabled: true,
				Resolver: ResolverConfig{
					Type: ResolverTypePath,
					Path: PathResolverConfig{Segment: 1},
				},
				Limits:  LimitsConfig{Tenants: 100},
				Tenants: makeSampleTenants(),
			},
			dbConfig:  &DatabaseConfig{},
			msgConfig: &MessagingConfig{},
		},
		{
			name: "valid_composite_resolver_with_path",
			mtConfig: &MultitenantConfig{
				Enabled: true,
				Resolver: ResolverConfig{
					Type:   "composite",
					Header: testTenantHeader,
					Domain: testDomain,
					Path:   PathResolverConfig{Segment: 2, Prefix: "/itsp"},
					Order:  []string{ResolverTypeSubdomain, ResolverTypePath, ResolverTypeHeader},
				},
				Limits:  LimitsConfig{Tenants: 100},
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
			err := normalizeTenantsAndCheckMultitenant(t, tt.mtConfig, tt.dbConfig, tt.msgConfig, sourceConfig)
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
			name: "path_resolver_missing_segment",
			mtConfig: &MultitenantConfig{
				Enabled: true,
				Resolver: ResolverConfig{
					Type: ResolverTypePath,
					Path: PathResolverConfig{Segment: 0},
				},
				Limits:  LimitsConfig{Tenants: 100},
				Tenants: makeSampleTenants(),
			},
			dbConfig:      &DatabaseConfig{},
			msgConfig:     &MessagingConfig{},
			expectedError: "multitenant.resolver.path.segment",
		},
		{
			name: "path_resolver_negative_segment",
			mtConfig: &MultitenantConfig{
				Enabled: true,
				Resolver: ResolverConfig{
					Type: ResolverTypePath,
					Path: PathResolverConfig{Segment: -1},
				},
				Limits:  LimitsConfig{Tenants: 100},
				Tenants: makeSampleTenants(),
			},
			dbConfig:      &DatabaseConfig{},
			msgConfig:     &MessagingConfig{},
			expectedError: "multitenant.resolver.path.segment",
		},
		{
			name: "path_resolver_prefix_missing_leading_slash",
			mtConfig: &MultitenantConfig{
				Enabled: true,
				Resolver: ResolverConfig{
					Type: ResolverTypePath,
					Path: PathResolverConfig{Segment: 2, Prefix: "itsp"},
				},
				Limits:  LimitsConfig{Tenants: 100},
				Tenants: makeSampleTenants(),
			},
			dbConfig:      &DatabaseConfig{},
			msgConfig:     &MessagingConfig{},
			expectedError: "multitenant.resolver.path.prefix",
		},
		{
			name: "composite_with_invalid_path_segment",
			mtConfig: &MultitenantConfig{
				Enabled: true,
				Resolver: ResolverConfig{
					Type:   "composite",
					Header: testTenantHeader,
					Domain: testDomain,
					Path:   PathResolverConfig{Segment: -2, Prefix: "/itsp"},
					Order:  []string{ResolverTypeSubdomain, ResolverTypePath, ResolverTypeHeader},
				},
				Limits:  LimitsConfig{Tenants: 100},
				Tenants: makeSampleTenants(),
			},
			dbConfig:      &DatabaseConfig{},
			msgConfig:     &MessagingConfig{},
			expectedError: "multitenant.resolver.path.segment",
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
			expectedError: "multitenant.tenants.*.messaging",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sourceConfig := tt.sourceConfig
			if sourceConfig == nil {
				sourceConfig = &SourceConfig{Type: SourceTypeStatic}
			}
			err := normalizeTenantsAndCheckMultitenant(t, tt.mtConfig, tt.dbConfig, tt.msgConfig, sourceConfig)
			assert.Error(t, err)
			assert.Contains(t, err.Error(), tt.expectedError)
		})
	}
}

// TestValidateMultitenantTenantsRejectsDottedTenantID proves a tenant ID
// containing '.' is rejected: it collides with koanf's path delimiter, so the
// constructed section path multitenant.tenants.<id>.database would become
// ambiguous.
func TestValidateMultitenantTenantsRejectsDottedTenantID(t *testing.T) {
	// The dotted-ID rule lives in checkMultitenant's tenant loop, which runs
	// after the resolver/limits checks — so the resolver must be valid on its
	// own for the rejection under test to be the one that surfaces.
	mt := &MultitenantConfig{
		Enabled:  true,
		Resolver: ResolverConfig{Type: "header", Header: testTenantHeader},
		Limits:   LimitsConfig{Tenants: 100},
		Tenants: map[string]TenantEntry{
			"tenant.a": {
				Database: DatabaseConfig{
					Type:     PostgreSQL,
					Host:     testTenantDBHost,
					Port:     5432,
					Database: "tenant_a",
					Username: "tenant_user",
				},
			},
		},
	}
	source := &SourceConfig{Type: SourceTypeStatic}

	err := checkMultitenant(mt, &DatabaseConfig{}, &MessagingConfig{}, source)
	assertValidationError(t, err, "cannot contain '.'")
}

func TestValidateMultitenantResolver(t *testing.T) {
	tests := []struct {
		name           string
		config         ResolverConfig
		expectError    bool
		errorContains  string
		expectedHeader string // Check default header is set
		expectedDomain string // Check the domain was normalized
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
				Order:   []string{ResolverTypeSubdomain, ResolverTypeHeader},
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
				Domain: "api.example.com",
			},
			expectError:    false,
			expectedDomain: testDomain,
		},
		{
			name: "subdomain_domain_with_surrounding_whitespace",
			config: ResolverConfig{
				Type:   "subdomain",
				Domain: "  api.example.com\t",
			},
			expectError:    false,
			expectedDomain: testDomain,
		},
		{
			name: "subdomain_domain_dot_only_rejected",
			config: ResolverConfig{
				Type:   "subdomain",
				Domain: ".", // Strips to "" after trimming the leading dot — newSubdomainResolver would build nil
			},
			expectError:   true,
			errorContains: "multitenant.resolver.domain",
		},
		{
			name: "composite_missing_domain",
			config: ResolverConfig{
				Type:   "composite",
				Header: testTenantHeader,
				Order:  []string{ResolverTypeSubdomain, ResolverTypeHeader},
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
				Domain: "api.example.com",
				Order:  []string{ResolverTypeSubdomain, ResolverTypeHeader},
			},
			expectError:    false,
			expectedDomain: testDomain,
		},
		{
			name: "header_resolver_stray_domain_left_alone",
			config: ResolverConfig{
				Type:   "header",
				Domain: "api.example.com",
			},
			expectError:    false,
			expectedDomain: "api.example.com",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := normalizeAndCheckResolver(&tt.config)
			if tt.expectError {
				assert.Error(t, err)
				assert.Contains(t, err.Error(), tt.errorContains)
			} else {
				assert.NoError(t, err)
				// Check if default header was set
				if tt.expectedHeader != "" {
					assert.Equal(t, tt.expectedHeader, tt.config.Header)
				}
				if tt.expectedDomain != "" {
					assert.Equal(t, tt.expectedDomain, tt.config.Domain)
				}
			}
		})
	}
}

func TestResolverOrderValidationRejectsUnknown(t *testing.T) {
	tests := []struct {
		name          string
		config        ResolverConfig
		expectError   bool
		errorContains string
		expectedOrder []string
	}{
		{
			name: "unknown_entry_rejected",
			config: ResolverConfig{
				Type:   "composite",
				Domain: testDomain,
				Order:  []string{"bogus"},
			},
			expectError:   true,
			errorContains: "multitenant.resolver.order",
		},
		{
			name: "duplicate_entry_rejected",
			config: ResolverConfig{
				Type:   "composite",
				Domain: testDomain,
				Order:  []string{"header", "header"},
			},
			expectError:   true,
			errorContains: "multitenant.resolver.order",
		},
		{
			name: "order_on_non_composite_type_rejected",
			config: ResolverConfig{
				Type:  "header",
				Order: []string{"header"},
			},
			expectError:   true,
			errorContains: "multitenant.resolver.order",
		},
		{
			name: "composite_without_order_is_rejected",
			config: ResolverConfig{
				Type:   "composite",
				Domain: testDomain,
				// Order intentionally unset — composite requires an explicit order,
				// there is no implicit default (the framework can't know which
				// sub-resolvers are attacker-reachable vs. gateway-asserted).
			},
			expectError:   true,
			errorContains: "multitenant.resolver.order",
		},
		{
			name: "order_with_path_but_no_segment_rejected",
			config: ResolverConfig{
				Type:  "composite",
				Order: []string{ResolverTypePath, ResolverTypeHeader},
				// Path.Segment intentionally unset — order names "path" but the
				// path sub-resolver has no segment configured, so it would build
				// as nil and silently degrade the composite to header-only.
			},
			expectError:   true,
			errorContains: "multitenant.resolver.path.segment",
		},
		{
			name: "order_with_subdomain_and_dot_domain_rejected",
			config: ResolverConfig{
				Type:   "composite",
				Order:  []string{ResolverTypeSubdomain, ResolverTypeHeader},
				Domain: ".", // Strips to "" after trimming the leading dot — newSubdomainResolver would build nil
			},
			expectError:   true,
			errorContains: "multitenant.resolver.domain",
		},
		{
			name: "valid_configured_order_preserved",
			config: ResolverConfig{
				Type:   "composite",
				Domain: testDomain,
				Order:  []string{ResolverTypeHeader, ResolverTypeSubdomain},
			},
			expectError:   false,
			expectedOrder: []string{ResolverTypeHeader, ResolverTypeSubdomain},
		},
		{
			name: "order_excluding_subdomain_does_not_require_domain",
			config: ResolverConfig{
				Type:  "composite",
				Order: []string{ResolverTypePath, ResolverTypeHeader},
				Path:  PathResolverConfig{Segment: 1},
			},
			expectError:   false,
			expectedOrder: []string{ResolverTypePath, ResolverTypeHeader},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := normalizeAndCheckResolver(&tt.config)
			if tt.expectError {
				assert.Error(t, err)
				assert.Contains(t, err.Error(), tt.errorContains)
				return
			}
			assert.NoError(t, err)
			assert.Equal(t, tt.expectedOrder, tt.config.Order)
		})
	}
}

func TestValidateMultitenantLimits(t *testing.T) {
	t.Run("defaults when zero", func(t *testing.T) {
		cfg := LimitsConfig{Tenants: 0}
		normalizeMultitenantLimits(&cfg)
		assert.Equal(t, 100, cfg.Tenants)
	})

	t.Run("defaults when negative", func(t *testing.T) {
		cfg := LimitsConfig{Tenants: -1}
		normalizeMultitenantLimits(&cfg)
		assert.Equal(t, 100, cfg.Tenants)
	})

	t.Run("supports upper bound", func(t *testing.T) {
		cfg := LimitsConfig{Tenants: 1000}
		err := checkMultitenantLimits(&cfg)
		assert.NoError(t, err)
		assert.Equal(t, 1000, cfg.Tenants)
	})

	t.Run("rejects exceeding upper bound", func(t *testing.T) {
		cfg := LimitsConfig{Tenants: 1001}
		err := checkMultitenantLimits(&cfg)
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
			err := checkMultitenant(tt.mtConfig, &DatabaseConfig{}, &MessagingConfig{}, tt.sourceConfig)
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
	err := checkCache(&cfg)
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

	err := checkCache(&cfg)
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

			err := checkCache(&cfg)
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

			err := checkCache(&cfg)
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

			err := checkCache(&cfg)
			if tt.valid {
				assert.NoError(t, err)
			} else {
				assert.Error(t, err)
			}
		})
	}
}

// =============================================================================
// KeyStore Validation Tests
// =============================================================================

func TestValidateKeyStoreEmpty(t *testing.T) {
	cfg := &KeyStoreConfig{}
	assert.NoError(t, checkKeyStore(cfg))
}

func TestValidateKeyStoreValid(t *testing.T) {
	cfg := &KeyStoreConfig{
		Keys: map[string]KeyPairConfig{
			"signing": {
				Public:  KeySourceConfig{File: "pub.der"},
				Private: KeySourceConfig{Value: "base64data"},
			},
		},
	}
	assert.NoError(t, checkKeyStore(cfg))
}

func TestValidateKeyStorePublicKeyRequired(t *testing.T) {
	cfg := &KeyStoreConfig{
		Keys: map[string]KeyPairConfig{
			"missing": {
				Public: KeySourceConfig{},
			},
		},
	}
	err := checkKeyStore(cfg)
	assert.ErrorContains(t, err, "key source required")
}

func TestValidateKeyStoreBothSourcesSet(t *testing.T) {
	cfg := &KeyStoreConfig{
		Keys: map[string]KeyPairConfig{
			"both": {
				Public: KeySourceConfig{File: "a.der", Value: "also"},
			},
		},
	}
	err := checkKeyStore(cfg)
	assert.ErrorContains(t, err, "both 'file' and 'value' set")
}

func TestValidateKeyStorePrivateOptional(t *testing.T) {
	cfg := &KeyStoreConfig{
		Keys: map[string]KeyPairConfig{
			"pub-only": {
				Public: KeySourceConfig{File: "pub.der"},
			},
		},
	}
	assert.NoError(t, checkKeyStore(cfg))
}

func TestValidateKeyStoreWiredIntoValidate(t *testing.T) {
	cfg := createValidFullConfig()
	cfg.KeyStore = KeyStoreConfig{
		Keys: map[string]KeyPairConfig{
			"bad": {
				Public: KeySourceConfig{File: "a.der", Value: "also"},
			},
		},
	}
	err := Validate(cfg)
	assert.ErrorContains(t, err, "keystore config")
	assert.ErrorContains(t, err, "both 'file' and 'value' set")
}

func TestValidateKeyStoreSecretValid(t *testing.T) {
	cfg := &KeyStoreConfig{
		Keys: map[string]KeyPairConfig{
			"mac-file":  {Secret: KeySourceConfig{File: "mac.bin"}},
			"mac-value": {Secret: KeySourceConfig{Value: "base64data"}},
		},
	}
	assert.NoError(t, checkKeyStore(cfg))
}

func TestValidateKeyStoreSecretRequiresSource(t *testing.T) {
	cfg := &KeyStoreConfig{
		Keys: map[string]KeyPairConfig{
			"empty-secret": {Secret: KeySourceConfig{}},
		},
	}
	// An entry with no material at all falls back to the public-key path.
	err := checkKeyStore(cfg)
	assert.ErrorContains(t, err, "key source required")
}

func TestValidateKeyStoreSecretBothSourcesSet(t *testing.T) {
	cfg := &KeyStoreConfig{
		Keys: map[string]KeyPairConfig{
			"mac": {Secret: KeySourceConfig{File: "mac.bin", Value: "also"}},
		},
	}
	err := checkKeyStore(cfg)
	assert.ErrorContains(t, err, "both 'file' and 'value' set")
	assert.ErrorContains(t, err, "keystore.keys.mac.secret")
}

func TestValidateKeyStoreMixedEntrySecretPlusPublic(t *testing.T) {
	cfg := &KeyStoreConfig{
		Keys: map[string]KeyPairConfig{
			"mixed": {
				Public: KeySourceConfig{File: "pub.der"},
				Secret: KeySourceConfig{File: "mac.bin"},
			},
		},
	}
	err := checkKeyStore(cfg)
	assert.ErrorContains(t, err, "both a symmetric 'secret' and asymmetric")
	assert.ErrorContains(t, err, "keystore.keys.mixed")
}

func TestValidateKeyStoreMixedEntrySecretPlusPrivate(t *testing.T) {
	cfg := &KeyStoreConfig{
		Keys: map[string]KeyPairConfig{
			"mixed": {
				Private: KeySourceConfig{Value: "privb64"},
				Secret:  KeySourceConfig{Value: "macb64"},
			},
		},
	}
	err := checkKeyStore(cfg)
	assert.ErrorContains(t, err, "both a symmetric 'secret' and asymmetric")
}

func TestValidateKeyStoreSecretMinLengthNil(t *testing.T) {
	cfg := &KeyStoreConfig{}
	assert.NoError(t, checkKeyStore(cfg), "nil is left for normalize to fill; check must not reject it")
}

func TestValidateKeyStoreSecretMinLengthNegative(t *testing.T) {
	cfg := &KeyStoreConfig{SecretMinLength: new(-1)}
	err := checkKeyStore(cfg)
	assert.ErrorContains(t, err, "keystore.secretminlength")
	assert.ErrorContains(t, err, "must be non-negative")
}

func TestValidateKeyStoreSecretMinLengthZeroAllowed(t *testing.T) {
	cfg := &KeyStoreConfig{
		SecretMinLength: new(0),
		Keys: map[string]KeyPairConfig{
			"mac": {Secret: KeySourceConfig{File: "mac.bin"}},
		},
	}
	assert.NoError(t, checkKeyStore(cfg))
}

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

// loadDeliveredEmptyFixture stages one TestValidateNoDeliveredEmptyDatabaseViaLoad
// case — a scratch working directory holding yaml (when non-empty) plus env — and
// returns Load()'s results from inside it. t.Cleanup rather than defer, so the
// environment is cleared at subtest end from within a helper.
func loadDeliveredEmptyFixture(t *testing.T, yaml string, env map[string]string) (*Config, error) {
	t.Helper()
	clearEnvironmentVariables()
	t.Cleanup(clearEnvironmentVariables)

	dir := t.TempDir()
	if yaml != "" {
		require.NoError(t, os.WriteFile(filepath.Join(dir, testConfigFileYAML), []byte(yaml), 0o600))
	}
	t.Chdir(dir)

	for k, v := range env {
		t.Setenv(k, v)
	}
	return Load()
}

// assertDeliveredEmptyError checks a rendered startup error against one case's
// expectations. wantOrdered pins the sorted-path contract: each entry must appear
// strictly before the next.
func assertDeliveredEmptyError(t *testing.T, got string, wantContains, wantNotContain, wantOrdered []string) {
	t.Helper()
	for _, s := range wantContains {
		assert.Contains(t, got, s)
	}
	for _, s := range wantNotContain {
		assert.NotContains(t, got, s)
	}
	for i := 1; i < len(wantOrdered); i++ {
		prev, cur := strings.Index(got, wantOrdered[i-1]), strings.Index(got, wantOrdered[i])
		assert.Less(t, prev, cur, "%s must be reported before %s", wantOrdered[i-1], wantOrdered[i])
	}
}

// TestLoadRejectsDeliveredEmptyAllowedIPs pins ADR-078. debug.allowedips defaults to the
// loopback pair, so an empty value does not relax a control — it REPLACES one with nothing,
// and with debug.bearertoken set ADR-049's registration gate is satisfied by the token alone
// and the IP whitelist is never installed. Every delivery shape that renders an empty string
// is rejected; the token's presence must not change the verdict, since the fail-open case is
// precisely the one where a token exists.
func TestLoadRejectsDeliveredEmptyAllowedIPs(t *testing.T) {
	const header = "app:\n  name: a\n  version: v1\nserver:\n  port: 8080\n"
	const withToken = "debug:\n  enabled: true\n  bearertoken: sekritsekritsekrit\n"
	const noToken = "debug:\n  enabled: true\n  allowedips: [\"10.0.0.0/8\"]\n"

	tests := []struct {
		name string
		yaml string
		env  map[string]string
	}{
		{name: "env_empty_with_token", yaml: header + withToken, env: map[string]string{"DEBUG_ALLOWEDIPS": ""}},
		{name: "env_whitespace_with_token", yaml: header + withToken, env: map[string]string{"DEBUG_ALLOWEDIPS": "   "}},
		{name: "yaml_empty_string_with_token", yaml: header + "debug:\n  enabled: true\n  bearertoken: sekritsekritsekrit\n  allowedips: \"\"\n"},
		// Without a token the deployment already failed, at registration (ADR-049). It now
		// fails EARLIER, at Load, which is the seam that can name the key.
		{name: "env_empty_without_token", yaml: header + noToken, env: map[string]string{"DEBUG_ALLOWEDIPS": ""}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := loadDeliveredEmptyFixture(t, tt.yaml, tt.env)

			var cfgErr *ConfigError
			require.ErrorAs(t, err, &cfgErr)
			assert.Equal(t, "debug.allowedips", cfgErr.Field)
			assert.Contains(t, cfgErr.Message, "delivered empty")
			// Both ways out, so an operator who MEANT token-only is not stuck.
			assert.Contains(t, cfgErr.Action, "DEBUG_ALLOWEDIPS")
			assert.Contains(t, cfgErr.Action, "debug.allowedips: []")
		})
	}
}

// TestLoadAllowedIPsDeliberateShapesUnchanged pins the other half: the two shapes an operator
// writes on purpose still mean what they meant. The empty LIST is the sanctioned token-only
// downgrade of ADR-049 — the spelling no broken template can produce — and its survival is
// what makes rejecting the empty STRING a fix rather than a removal of choice.
func TestLoadAllowedIPsDeliberateShapesUnchanged(t *testing.T) {
	const header = "app:\n  name: a\n  version: v1\nserver:\n  port: 8080\n"
	const token = "  bearertoken: sekritsekritsekrit\n"

	t.Run("empty_list_is_the_deliberate_clear", func(t *testing.T) {
		cfg, err := loadDeliveredEmptyFixture(t, header+"debug:\n  enabled: true\n"+token+"  allowedips: []\n", nil)

		require.NoError(t, err, "an empty sequence is how ADR-049's token-only posture is written")
		assert.Empty(t, cfg.Debug.AllowedIPs)
	})

	t.Run("unset_keeps_the_loopback_default", func(t *testing.T) {
		cfg, err := loadDeliveredEmptyFixture(t, header, nil)

		require.NoError(t, err)
		assert.Equal(t, []string{"127.0.0.1", "::1"}, cfg.Debug.AllowedIPs)
	})

	t.Run("explicit_list_unchanged", func(t *testing.T) {
		cfg, err := loadDeliveredEmptyFixture(t, header+"debug:\n  enabled: true\n"+token+"  allowedips: [\"10.0.0.0/8\"]\n", nil)

		require.NoError(t, err)
		assert.Equal(t, []string{"10.0.0.0/8"}, cfg.Debug.AllowedIPs)
	})
}

// TestDeliveredEmptyListCheckCoversOnlyAllowedIPs is the containment pin. The mechanism is
// list-driven so a future key joins by adding one name, which is exactly why the list needs a
// test that FAILS when a name is added silently: every other []string key must still accept a
// delivered-empty value, because clearing those tightens the posture (or fails elsewhere)
// rather than removing a control.
func TestDeliveredEmptyListCheckCoversOnlyAllowedIPs(t *testing.T) {
	assert.Equal(t, []string{"debug.allowedips"}, deliveredEmptyRejectingKeys,
		"adding a key here changes startup for every deployment that clears it — add its case below too")

	const header = "app:\n  name: a\n  version: v1\nserver:\n  port: 8080\n"

	tests := []struct {
		name   string
		envVar string
		assert func(t *testing.T, cfg *Config)
	}{
		{
			// Fails CLOSED downstream: an empty allowlist restricts the scheduler API to
			// localhost, so clearing it is a tightening and must stay legal.
			name:   "scheduler_security_cidrallowlist",
			envVar: "SCHEDULER_SECURITY_CIDRALLOWLIST",
			assert: func(t *testing.T, cfg *Config) { assert.Empty(t, cfg.Scheduler.Security.CIDRAllowlist) },
		},
		{
			// Empty means "trust no proxy", the stricter reading.
			name:   "scheduler_security_trustedproxies",
			envVar: "SCHEDULER_SECURITY_TRUSTEDPROXIES",
			assert: func(t *testing.T, cfg *Config) { assert.Empty(t, cfg.Scheduler.Security.TrustedProxies) },
		},
		{
			name:   "debug_trustedproxies",
			envVar: "DEBUG_TRUSTEDPROXIES",
			assert: func(t *testing.T, cfg *Config) { assert.Empty(t, cfg.Debug.TrustedProxies) },
		},
		{
			// Empty merges into the shipped defaults rather than disabling masking.
			name:   "log_sensitivefields",
			envVar: "LOG_SENSITIVEFIELDS",
			assert: func(t *testing.T, cfg *Config) { assert.Empty(t, cfg.Log.SensitiveFields) },
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := loadDeliveredEmptyFixture(t, header, map[string]string{tt.envVar: ""})

			require.NoError(t, err, "%s clears safely; only debug.allowedips fails open", tt.envVar)
			tt.assert(t, cfg)
		})
	}

	// multitenant.resolver.order is the fifth sibling and needs its own shape: clearing it is
	// already fatal under a composite resolver (ADR-039). The property is that it keeps ITS
	// error — this check must not preempt a rejection that already names the right remedy.
	t.Run("multitenant_resolver_order_keeps_its_own_error", func(t *testing.T) {
		_, err := loadDeliveredEmptyFixture(t,
			header+"multitenant:\n  enabled: true\n  resolver:\n    type: composite\n",
			map[string]string{"MULTITENANT_RESOLVER_ORDER": ""})

		require.Error(t, err)
		assert.Contains(t, err.Error(), "required when multitenant.resolver.type is 'composite'")
		assert.NotContains(t, err.Error(), "removes a control",
			"ADR-039 already rejects this with a better remedy; do not shadow it")
	})

	t.Run("multitenant_resolver_order_outside_composite_still_clears", func(t *testing.T) {
		cfg, err := loadDeliveredEmptyFixture(t, header, map[string]string{"MULTITENANT_RESOLVER_ORDER": ""})

		require.NoError(t, err)
		assert.Empty(t, cfg.Multitenant.Resolver.Order)
	})
}

// TestLoadRejectsAllowedIPsThatDecodeToNothing covers the shapes the first cut of ADR-078
// missed. The property is not "the string looks empty" but "the DECODER produces no entries",
// and those differ: splitAndTrimList drops empty parts, so a separator-only value trims
// non-empty yet yields nothing. A Helm `join ","` over unset values renders exactly that.
//
// YAML null is the second shape, and it is where this key parts company with ADR-074/077.
// There a null takes the default and is therefore absence; here it REPLACES the default, so
// the same spelling that is harmless for a numeric key removes a control for this one.
func TestLoadRejectsAllowedIPsThatDecodeToNothing(t *testing.T) {
	const header = "app:\n  name: a\n  version: v1\nserver:\n  port: 8080\n"
	const withToken = "debug:\n  enabled: true\n  bearertoken: sekritsekritsekrit\n"

	tests := []struct {
		name string
		yaml string
		env  map[string]string
	}{
		{name: "separator_only", yaml: header + withToken, env: map[string]string{"DEBUG_ALLOWEDIPS": ","}},
		{name: "repeated_separators", yaml: header + withToken, env: map[string]string{"DEBUG_ALLOWEDIPS": ",,,"}},
		{name: "separators_and_spaces", yaml: header + withToken, env: map[string]string{"DEBUG_ALLOWEDIPS": " , "}},
		{name: "yaml_bare_null", yaml: header + "debug:\n  enabled: true\n  bearertoken: sekritsekritsekrit\n  allowedips:\n"},
		{name: "yaml_explicit_null", yaml: header + "debug:\n  enabled: true\n  bearertoken: sekritsekritsekrit\n  allowedips: null\n"},
		{name: "yaml_tilde_null", yaml: header + "debug:\n  enabled: true\n  bearertoken: sekritsekritsekrit\n  allowedips: ~\n"},
		// Precedence: a real YAML list wiped by a null overlay is the same wipe.
		{name: "env_separator_over_real_yaml_list", yaml: header + "debug:\n  enabled: true\n  bearertoken: sekritsekritsekrit\n  allowedips: [\"10.0.0.0/8\"]\n", env: map[string]string{"DEBUG_ALLOWEDIPS": ","}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := loadDeliveredEmptyFixture(t, tt.yaml, tt.env)

			var cfgErr *ConfigError
			require.ErrorAs(t, err, &cfgErr, "this delivery decodes to zero entries and must not boot")
			assert.Equal(t, "debug.allowedips", cfgErr.Field)
		})
	}
}

// TestLoadAllowedIPsEntryShapesStillBoot is the counterweight: a value that decodes to at
// least one entry is NOT this check's business, even when the entry is junk. Those install
// the middleware and fail closed at parse time (no networks parsed ⇒ deny), so rejecting them
// here would move a working fail-closed path into a startup abort for no security gain.
// NOTE (ADR-080): junk that trims to a non-empty string is now rejected at startup by
// validateIPOrCIDRList — see TestDebugAllowedIPsRejectsUnparseableEntries below. What
// survives here is the empty-ish entry, which this check skips and NewIPWhitelist drops
// with a WARN, so the rationale below still describes the cases it still covers.
func TestLoadAllowedIPsEntryShapesStillBoot(t *testing.T) {
	const header = "app:\n  name: a\n  version: v1\nserver:\n  port: 8080\n"
	const withToken = "debug:\n  enabled: true\n  bearertoken: sekritsekritsekrit\n"

	tests := []struct {
		name string
		yaml string
		want []string
	}{
		{name: "list_of_empty_string", yaml: header + withToken + "  allowedips: [\"\"]\n", want: []string{""}},
		{name: "list_of_space", yaml: header + withToken + "  allowedips: [\" \"]\n", want: []string{" "}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := loadDeliveredEmptyFixture(t, tt.yaml, nil)

			require.NoError(t, err, "one junk entry still installs the whitelist, which then denies")
			assert.Equal(t, tt.want, cfg.Debug.AllowedIPs)
		})
	}
}

// TestLenientTrustedProxyKeysRejectEveryDefaultRouteSpelling closes the asymmetry that made the
// debug-endpoint bypass reachable: the SAME value that aborts startup on
// server.trustedproxies was accepted on debug.trustedproxies and
// scheduler.security.trustedproxies, because those two route to the lenient
// validateCIDRList while server routes to the strict ParseTrustedProxyCIDR.
//
// With every peer trusted, an attacker connects DIRECTLY and the forwarding-header
// path opens, which is what turns a header the caller wrote into the address an
// access-control check believes.
func TestLenientTrustedProxyKeysRejectEveryDefaultRouteSpelling(t *testing.T) {
	// Every one of these trusts an entire address family. "127.0.0.1/0" is a default route
	// wearing a host address; "::ffff:0.0.0.0/96" is one the SECURITY AUDIT found walking
	// past a mask-size check, because Mask.Size() reads 96/128 while Contains re-derives a
	// 4-byte mask and matches every IPv4 address. The rule is coverage, not spelling.
	for _, entry := range []string{"0.0.0.0/0", "::/0", "127.0.0.1/0", "2001:db8::1/0", "::ffff:0.0.0.0/96", "::ffff:0:0/96"} {
		t.Run("debug_"+safeSubtestName(entry), func(t *testing.T) {
			cfg := &DebugConfig{TrustedProxies: []string{entry}}
			err := checkDebug(cfg)
			require.Error(t, err, "a default route must not be accepted on debug.trustedproxies")
			assert.Contains(t, err.Error(), "debug.trustedproxies")
			assert.Contains(t, err.Error(), "trusts every address")
		})

		t.Run("scheduler_"+safeSubtestName(entry), func(t *testing.T) {
			cfg := &SchedulerConfig{Security: SchedulerSecurityConfig{TrustedProxies: []string{entry}}}
			err := checkScheduler(cfg)
			require.Error(t, err, "a default route must not be accepted on scheduler.security.trustedproxies")
			assert.Contains(t, err.Error(), "scheduler.security.trustedproxies")
			assert.Contains(t, err.Error(), "trusts every address")
		})
	}
}

// TestDebugAllowedIPsRejectsUnparseableEntries covers a key that had NO CIDR-syntax
// validation: only ADR-078's delivered-empty check ran, so a typo produced a silent
// runtime deny-all rather than a startup error. An operator locked out of their own
// debug endpoints by a malformed entry should be told at boot, not left to infer it.
//
// Bare addresses MUST be accepted: the shipped default is ["127.0.0.1","::1"], which the
// strict proxy parser rejects. This key is an allowlist, not a trust list, so unlike
// trustedproxies it may legitimately admit everything — ADR-049 recommends ["0.0.0.0/0"]
// for it — and a default route here is NOT an error.
func TestDebugAllowedIPsRejectsUnparseableEntries(t *testing.T) {
	t.Run("rejects_a_malformed_entry", func(t *testing.T) {
		cfg := &DebugConfig{AllowedIPs: []string{"127.0.0.1", "not-an-ip"}}
		err := checkDebug(cfg)
		require.Error(t, err, "a malformed allowlist entry must fail at startup")
		assert.Contains(t, err.Error(), "debug.allowedips")
	})

	for _, entry := range []string{"127.0.0.1", "::1", "10.0.0.0/8", "2001:db8::/32", "0.0.0.0/0"} {
		t.Run("accepts_"+safeSubtestName(entry), func(t *testing.T) {
			cfg := &DebugConfig{AllowedIPs: []string{entry}}
			assert.NoError(t, checkDebug(cfg), "%q is a legitimate allowlist entry", entry)
		})
	}
}

// collectKoanfPaths walks a struct tree and returns the dotted koanf path of every field
// whose own koanf tag equals want. It is the discovery half of the trusted-proxy rule:
// the list under test is authoritative only if nothing can exist outside it.
func collectKoanfPaths(t *testing.T, typ reflect.Type, want, prefix string, out *[]string) {
	t.Helper()
	for i := 0; i < typ.NumField(); i++ {
		f := typ.Field(i)
		tag := strings.Split(f.Tag.Get("koanf"), ",")[0]
		if tag == "" || tag == "-" {
			continue
		}
		path := tag
		if prefix != "" {
			path = prefix + "." + tag
		}
		if tag == want {
			*out = append(*out, path)
			continue
		}
		ft := f.Type
		for ft.Kind() == reflect.Pointer {
			ft = ft.Elem()
		}
		if ft.Kind() == reflect.Struct {
			collectKoanfPaths(t, ft, want, path, out)
		}
	}
}

// TestTrustedProxyFieldsCoverEveryKoanfTag is the generalized form of the default-route
// rule: rather than trusting a hand-written list of the keys that exist today, it DISCOVERS
// every `koanf:"trustedproxies"` field in the Config tree and asserts each one is covered.
//
// This is the tripwire the bypass needed and did not have. Three keys meant the same thing,
// two were validated leniently and one strictly, and nothing noticed for two releases. A
// fourth key added to types.go without being wired here fails this test rather than shipping
// the same hole again.
func TestTrustedProxyFieldsCoverEveryKoanfTag(t *testing.T) {
	var found []string
	collectKoanfPaths(t, reflect.TypeOf(Config{}), "trustedproxies", "", &found)

	require.NotEmpty(t, found, "the walk found no trustedproxies keys at all — the walk is broken, not the config")

	listed := make([]string, 0, len(trustedProxyKeys))
	for _, k := range trustedProxyKeys {
		listed = append(listed, k.field)
	}
	assert.ElementsMatch(t, listed, found,
		"every config key named trustedproxies must appear in trustedProxyKeys and reject a default route; "+
			"a key present in the tree but missing from the table is the exact shape of the bypass ADR-080 closes")
}

// TestEveryTrustedProxyKeyRejectsDefaultRoute is the behavioral half: each discovered key,
// set to a default route on an otherwise-valid config, must fail config.Validate. The
// discovery test above proves the list is complete; this proves each member is wired to a
// validator that actually enforces the rule, rather than merely being listed.
func TestEveryTrustedProxyKeyRejectsDefaultRoute(t *testing.T) {
	// The payloads matter as much as the keys. An earlier version of this test used only
	// "0.0.0.0/0" — which the strict server parser already rejected — so it passed while
	// server.trustedproxies still accepted a v4-mapped default route AND a split-coverage
	// pair. A test named for every key must feed every key the shapes that break it.
	payloads := map[string][]string{
		"explicit_default_route": {"0.0.0.0/0"},
		"ipv6_default_route":     {"::/0"},
		"v4_mapped_default":      {"::ffff:0.0.0.0/96"},
		"split_coverage":         {"0.0.0.0/1", "128.0.0.0/1"},
		"three_way_split":        {"0.0.0.0/1", "128.0.0.0/2", "192.0.0.0/2"},
	}

	for _, k := range trustedProxyKeys {
		for name, entries := range payloads {
			t.Run(k.field+"_"+name, func(t *testing.T) {
				cfg := createValidFullConfig()
				k.set(cfg, entries)

				err := Validate(cfg)
				require.Error(t, err, "%s must reject %v", k.field, entries)
				// "trusts every address" (one entry) or "together trust every address" (a set).
				assert.Contains(t, err.Error(), "every address")
			})
		}
	}
}

// trustedProxyKeys is every config key that decides WHICH PEERS may set forwarding headers,
// paired with the setter that reaches it. Keeping the name and the setter together means a
// key cannot be listed without being exercised.
//
// The two tests above use it from both directions: one discovers every
// `koanf:"trustedproxies"` field in the Config tree and fails if the table misses any, the
// other drives each entry through config.Validate and fails if the key is wired to a
// validator that does not enforce the rule. Between them, a fourth key cannot reintroduce
// the bypass by being forgotten OR by being wired leniently.
var trustedProxyKeys = []struct {
	field string
	set   func(*Config, []string)
}{
	{fieldServerTrustedProxies, func(c *Config, v []string) { c.Server.TrustedProxies = v }},
	{fieldDebugTrustedProxies, func(c *Config, v []string) { c.Debug.TrustedProxies = v }},
	{fieldSchedulerTrustedProxies, func(c *Config, v []string) { c.Scheduler.Security.TrustedProxies = v }},
}

// safeSubtestName makes a CIDR usable as a subtest name: Go treats "/" as the subtest
// separator, so "0.0.0.0/0" would render as a nested test and break -run targeting.
func safeSubtestName(s string) string {
	return strings.NewReplacer("/", "_", ":", "-").Replace(s)
}

// TestTrustedProxiesRejectTotalCoverageAcrossEntries pins the finding that reopened this
// PR after the first fix: no per-entry rule reaches a trust list that trusts everyone by
// SPLITTING the space. ["0.0.0.0/1","128.0.0.0/1"] is two properly-masked,
// non-default-route entries covering all of IPv4, and it was accepted — then a cross-family
// XFF entry turned it into a grant at both access-control doors.
//
// ADR-080 originally documented that exact list as a safe residual. It was not.
func TestTrustedProxiesRejectTotalCoverageAcrossEntries(t *testing.T) {
	for _, tc := range []struct {
		name    string
		entries []string
		reject  bool
	}{
		{name: "two_halves_of_ipv4", entries: []string{"0.0.0.0/1", "128.0.0.0/1"}, reject: true},
		{name: "three_way_split", entries: []string{"0.0.0.0/1", "128.0.0.0/2", "192.0.0.0/2"}, reject: true},
		{name: "ipv6_halves", entries: []string{"::/1", "8000::/1"}, reject: true},
		{name: "overlapping_halves", entries: []string{"0.0.0.0/1", "64.0.0.0/2", "128.0.0.0/1"}, reject: true},
		{name: "unordered_entries", entries: []string{"128.0.0.0/1", "0.0.0.0/1"}, reject: true},
		// A gap anywhere means the list does not trust every peer, which is the point of
		// having a list. These must keep working.
		{name: "half_of_ipv4", entries: []string{"0.0.0.0/1"}},
		{name: "rfc1918", entries: []string{"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16"}},
		{name: "all_but_one_slash32", entries: []string{"0.0.0.0/1", "128.0.0.0/2", "192.0.0.0/3", "224.0.0.0/4", "240.0.0.0/5"}},
		{name: "mixed_families_neither_total", entries: []string{"10.0.0.0/8", "2001:db8::/32"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := checkDebug(&DebugConfig{TrustedProxies: tc.entries})
			if !tc.reject {
				assert.NoError(t, err, "a list with a gap trusts fewer than all peers and is legitimate")
				return
			}
			require.Error(t, err, "a list covering an entire address family trusts every peer")
			assert.Contains(t, err.Error(), "trust every address")
		})
	}
}

// TestDebugAllowedIPsRejectsHostBits pins the audit's F5. An allowlist may legitimately
// admit everything, but "192.168.1.55/16" admits 65,536 hosts where the operator wrote one
// address — the same silent widening ParseTrustedProxyCIDR already refuses on the proxy
// keys, in the same words.
func TestDebugAllowedIPsRejectsHostBits(t *testing.T) {
	err := checkDebug(&DebugConfig{AllowedIPs: []string{"192.168.1.55/16"}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "host bits set")
	assert.Contains(t, err.Error(), "192.168.0.0/16", "the message must name what it actually widens to")

	assert.NoError(t, checkDebug(&DebugConfig{AllowedIPs: []string{"192.168.0.0/16", "127.0.0.1", "0.0.0.0/0"}}),
		"network addresses, bare hosts and a deliberate catch-all stay legal")
}

// TestParseTrustedProxyCIDRRejectsMappedDefaultRoute pins the exported per-entry parser
// directly. The set-level coverage check also catches this shape, so reverting the parser's
// own normalization breaks no other test — but ParseTrustedProxyCIDR is exported and
// server.trustedProxyOptions calls it per entry, so a consumer reaching it without the set
// check must still be told that "::ffff:0.0.0.0/96" is a default route.
//
// Mask.Size() reads it as 96 of 128 bits; Contains re-derives a 4-byte mask and matches
// every IPv4 address. NormalizeIPNet measures the one Contains will use.
func TestParseTrustedProxyCIDRRejectsMappedDefaultRoute(t *testing.T) {
	for _, entry := range []string{"::ffff:0.0.0.0/96", "::ffff:0:0/96", "0:0:0:0:0:ffff:0:0/96"} {
		t.Run(safeSubtestName(entry), func(t *testing.T) {
			_, err := ParseTrustedProxyCIDR(entry)
			require.Error(t, err, "%s matches every IPv4 address", entry)
			assert.ErrorIs(t, err, errTrustedProxyDefaultRoute)
		})
	}

	// A genuine /96 that is NOT v4-mapped stays legal — the rule is about what Contains
	// will match, not about the number 96.
	_, err := ParseTrustedProxyCIDR("2001:db8::/96")
	assert.NoError(t, err)
}

// mustCIDR parses a CIDR the test author wrote by hand.
func mustCIDR(t *testing.T, s string) *net.IPNet {
	t.Helper()
	_, n, err := net.ParseCIDR(s)
	require.NoError(t, err, "test CIDR must parse")
	return n
}

// ipv4ExceptLastAddress decomposes 0.0.0.0-255.255.255.254 into the 32 CIDR blocks that
// cover it exactly. It is total coverage minus a single address, built from first
// principles rather than from the function under test.
func ipv4ExceptLastAddress(t *testing.T) []*net.IPNet {
	t.Helper()
	nets := make([]*net.IPNet, 0, 32)
	var start uint32
	for ones := 1; ones <= 32; ones++ {
		ip := make(net.IP, net.IPv4len)
		binary.BigEndian.PutUint32(ip, start)
		nets = append(nets, mustCIDR(t, fmt.Sprintf("%s/%d", ip, ones)))
		start += 1 << (net.IPv4len*8 - ones)
	}
	return nets
}

// TestCoversAddressFamilyBoundaries pins the merge loop directly. Everything the config
// half now refuses rests on this predicate, and a boundary error here is silent in both
// directions: too eager and every legitimate multi-range proxy list is locked out, too
// lax and a set that trusts the whole internet is waved through. The one-address hole and
// the exactly-contiguous seam are the two cases that separate those failures.
func TestCoversAddressFamilyBoundaries(t *testing.T) {
	const v4Bits = net.IPv4len * 8

	t.Run("exactly_contiguous_at_the_seam_is_covered", func(t *testing.T) {
		nets := []*net.IPNet{mustCIDR(t, "0.0.0.0/1"), mustCIDR(t, "128.0.0.0/1")}
		assert.True(t, CoversAddressFamily(nets, v4Bits),
			"two halves meeting with no gap trust every address")
	})

	t.Run("one_address_hole_is_not_covered", func(t *testing.T) {
		assert.False(t, CoversAddressFamily(ipv4ExceptLastAddress(t), v4Bits),
			"255.255.255.255 is untrusted, so the list still distinguishes somebody")
	})

	t.Run("filling_the_one_address_hole_covers", func(t *testing.T) {
		nets := append(ipv4ExceptLastAddress(t), mustCIDR(t, "255.255.255.255/32"))
		assert.True(t, CoversAddressFamily(nets, v4Bits),
			"the same list plus the last address trusts everyone")
	})

	t.Run("nested_range_does_not_invent_a_gap", func(t *testing.T) {
		// 10.0.0.0/8 sits inside 0.0.0.0/1. If the sweep tracked the last endpoint seen
		// instead of the running maximum, reach would fall back to 10.255.255.255 and
		// 128.0.0.0 would read as a gap — accepting a list that trusts everyone.
		nets := []*net.IPNet{
			mustCIDR(t, "0.0.0.0/1"),
			mustCIDR(t, "10.0.0.0/8"),
			mustCIDR(t, "128.0.0.0/1"),
		}
		assert.True(t, CoversAddressFamily(nets, v4Bits),
			"a range nested in an earlier one cannot un-cover what is already covered")
	})
}

// TestDebugAllowedIPsValidationMatchesTheRuntimeParser pins two promises C60.22 makes to
// operators that nothing else covers. Both are about NOT failing a deployment the runtime
// would have served: the runtime parser has always stripped surrounding quotes, so a
// shell-quoting slip that works today must not become a startup failure; and the block is
// validated whether or not debug is enabled, so a typo surfaces at deploy time rather than
// during the incident in which someone flips it on.
func TestDebugAllowedIPsValidationMatchesTheRuntimeParser(t *testing.T) {
	for _, tc := range []struct {
		name    string
		enabled bool
		entries []string
		wantErr bool
	}{
		{name: "double_quoted_entry_is_accepted", enabled: true, entries: []string{`"127.0.0.1"`}},
		{name: "single_quoted_entry_is_accepted", enabled: true, entries: []string{`'127.0.0.1'`}},
		{name: "quoted_cidr_is_accepted", enabled: true, entries: []string{`"10.0.0.0/8"`}},
		{
			name: "malformed_entry_fails_even_when_debug_is_disabled", enabled: false,
			entries: []string{"not-an-ip"}, wantErr: true,
		},
		{name: "valid_entry_passes_when_debug_is_disabled", enabled: false, entries: []string{"127.0.0.1"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := createValidFullConfig()
			cfg.Debug.Enabled = tc.enabled
			cfg.Debug.AllowedIPs = tc.entries
			err := Validate(cfg)
			if tc.wantErr {
				require.Error(t, err, "%v must be refused at startup", tc.entries)
				assert.Contains(t, err.Error(), fieldDebugAllowedIPs)
				return
			}
			require.NoError(t, err, "the runtime parser serves %v, so validation must not refuse it", tc.entries)
		})
	}
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

// TestCheckMultitenantTenantEntryRejectsUnreachableIDs: a static tenant map key
// is a config section name like any other, so it obeys the same grammar the
// resolver applies to a resolved tenant ID.
func TestCheckMultitenantTenantEntryRejectsUnreachableIDs(t *testing.T) {
	tests := []struct {
		name      string
		tenantID  string
		wantField string
	}{
		{name: "underscore_in_id", tenantID: "acme_corp", wantField: "multitenant.tenants.acme_corp"},
		{name: "uppercase_in_id", tenantID: "Acme", wantField: "multitenant.tenants.Acme"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := checkMultitenantTenantEntry(tt.tenantID, &TenantEntry{})

			assertSectionNameRejected(t, err, tt.wantField)
		})
	}
}

func TestCheckMultitenantTenantEntryAcceptsReachableIDs(t *testing.T) {
	for _, id := range []string{"acme-corp", "acme", "t1"} {
		t.Run(id, func(t *testing.T) {
			require.NoError(t, checkMultitenantTenantEntry(id, &TenantEntry{}))
		})
	}
}

// TestCheckKeyStoreRejectsUnreachableKeyNames: a keystore entry's name reaches
// the same env transform, and is rejected before its sources are read.
func TestCheckKeyStoreRejectsUnreachableKeyNames(t *testing.T) {
	cfg := &KeyStoreConfig{Keys: map[string]KeyPairConfig{
		"my_key": {},
	}}

	err := checkKeyStore(cfg)

	assertSectionNameRejected(t, err, "keystore.keys.my_key")
}

// TestCheckKeyStoreRejectsADottedKeyName: a '.' is koanf's path delimiter, so a
// dotted name makes the constructed keystore.keys.<name> Field ambiguous — is
// "keystore.keys.my.key" the entry "my.key" or a "key" under "my"? The parent
// field is reported instead, exactly as the databases and static-tenant rules
// already do, and this must run BEFORE the reachability grammar so the
// ambiguous path is never built.
func TestCheckKeyStoreRejectsADottedKeyName(t *testing.T) {
	cfg := &KeyStoreConfig{Keys: map[string]KeyPairConfig{
		"my.key": {},
	}}

	err := checkKeyStore(cfg)

	require.Error(t, err)
	var cfgErr *ConfigError
	require.ErrorAs(t, err, &cfgErr)
	assert.Equal(t, "keystore.keys", cfgErr.Field, "the parent field, since a dotted name cannot carry an unambiguous path")
	assert.ErrorContains(t, err, "'.'")
}

// TestCheckKeyStoreAcceptsReachableKeyNames is the boundary's other side: a
// conforming name reaches validateKeyEntry, which then judges its sources.
func TestCheckKeyStoreAcceptsReachableKeyNames(t *testing.T) {
	cfg := &KeyStoreConfig{Keys: map[string]KeyPairConfig{
		"my-key": {Secret: KeySourceConfig{Value: "c2VjcmV0LWJ5dGVzLXRoYXQtYXJlLWxvbmctZW5vdWdo"}},
	}}

	require.NoError(t, checkKeyStore(cfg))
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

// TestCheckMultitenantRejectsTenantSiblingCollision is the same shape one section over.
func TestCheckMultitenantRejectsTenantSiblingCollision(t *testing.T) {
	mt := &MultitenantConfig{
		Enabled: true,
		Tenants: map[string]TenantEntry{"acme": {}, "acme_corp": {}},
		Resolver: ResolverConfig{
			Type:   "header",
			Header: "X-Tenant-ID",
		},
	}
	source := &SourceConfig{Type: SourceTypeStatic}

	err := checkMultitenant(mt, &DatabaseConfig{}, &MessagingConfig{}, source)

	assertSectionNameRejected(t, err, "multitenant.tenants.acme_corp")
}

// TestCheckMultitenantLeavesDynamicTenantIDsToTheResolver: a dynamic source's
// tenant IDs never reach this check — they arrive at request time and the
// resolver's own grammar gates them. The static path with the same ID still fails.
func TestCheckMultitenantLeavesDynamicTenantIDsToTheResolver(t *testing.T) {
	resolver := ResolverConfig{Type: "header", Header: "X-Tenant-ID"}

	dynamic := &MultitenantConfig{
		Enabled:  true,
		Tenants:  map[string]TenantEntry{"acme_corp": {}},
		Resolver: resolver,
	}
	require.NoError(t, checkMultitenant(dynamic, &DatabaseConfig{}, &MessagingConfig{}, &SourceConfig{Type: SourceTypeDynamic}),
		"a dynamic source's tenant map is not the config's to judge")

	static := &MultitenantConfig{
		Enabled:  true,
		Tenants:  map[string]TenantEntry{"acme_corp": {}},
		Resolver: resolver,
	}
	require.Error(t, checkMultitenant(static, &DatabaseConfig{}, &MessagingConfig{}, &SourceConfig{Type: SourceTypeStatic}),
		"the same ID under a static source is still rejected")
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
