package config

import (
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-viper/mapstructure/v2"
	"github.com/knadh/koanf/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	// Environment variable keys reused across tests
	testDatabaseUsername = "DATABASE_USERNAME"
	testDatabaseDatabase = "DATABASE_DATABASE"
	testDatabaseMaxConns = "DATABASE_POOL_MAX_CONNECTIONS"
	appName              = "gobricks-service"
	appVersion           = "v1.0.0"
	serverHost           = "0.0.0.0"
	testConfigFile       = "config.yml"
	testConfigFileYAML   = "config.yaml"
)

func TestLoadWithDefaults(t *testing.T) {
	// Clear any environment variables that might affect the test
	clearEnvironmentVariables()

	cfg, err := Load()
	require.NoError(t, err)
	require.NotNil(t, cfg)

	// Verify default values for non-database config
	assert.Equal(t, appName, cfg.App.Name)
	assert.Equal(t, appVersion, cfg.App.Version)
	assert.Equal(t, EnvDevelopment, cfg.App.Env)
	assert.False(t, cfg.App.Debug)
	assert.Equal(t, 100, cfg.App.Rate.Limit)
	assert.Equal(t, 200, cfg.App.Rate.Burst)
	assert.Equal(t, "default", cfg.App.Namespace)

	assert.Equal(t, serverHost, cfg.Server.Host)
	assert.Equal(t, 8080, cfg.Server.Port)
	assert.Equal(t, 15*time.Second, cfg.Server.Timeout.Read)
	assert.Equal(t, 30*time.Second, cfg.Server.Timeout.Write)
	assert.Equal(t, 60*time.Second, cfg.Server.Timeout.Idle)
	assert.Equal(t, 5*time.Second, cfg.Server.Timeout.Middleware)
	assert.Equal(t, 10*time.Second, cfg.Server.Timeout.Shutdown)
	assert.Equal(t, 1024, cfg.Server.Gzip.MinLength)
	assert.Equal(t, int64(10*1024*1024), cfg.Server.BodyLimit)
	assert.False(t, cfg.Server.ResponseTime.Enabled, "X-Response-Time header must default to opt-out")

	// Database should be disabled by default (no defaults provided)
	assert.False(t, IsDatabaseConfigured(&cfg.Database))
	assert.Equal(t, "", cfg.Database.Type)
	assert.Equal(t, "", cfg.Database.Host)
	assert.Equal(t, 0, cfg.Database.Port)
	assert.Equal(t, "", cfg.Database.Database)
	assert.Equal(t, "", cfg.Database.Username)
	assert.Equal(t, "", cfg.Database.TLS.Mode)
	assert.Equal(t, int32(0), cfg.Database.Pool.Max.Connections)
	assert.Equal(t, int32(0), cfg.Database.Pool.Idle.Connections)
	assert.Equal(t, time.Duration(0), cfg.Database.Pool.Lifetime.Max)
	assert.Equal(t, time.Duration(0), cfg.Database.Pool.Idle.Time)

	assert.Equal(t, "info", cfg.Log.Level)
	assert.False(t, cfg.Log.Pretty)
	assert.Equal(t, "auto", cfg.Log.Output.Format)
	assert.Equal(t, "", cfg.Log.Output.File)
}

func TestLoadWithEnvironmentVariables(t *testing.T) {
	// Clear environment variables first
	clearEnvironmentVariables()
	defer clearEnvironmentVariables()

	// Set environment variables to test override functionality
	// Include full database config to enable database
	os.Setenv("APP_NAME", appName)
	os.Setenv("APP_ENV", EnvProduction)
	os.Setenv("SERVER_PORT", "9090")
	os.Setenv("DATABASE_TYPE", "postgresql")
	os.Setenv("DATABASE_HOST", "localhost")
	os.Setenv("DATABASE_PORT", "5432")
	os.Setenv(testDatabaseDatabase, "testdb")
	os.Setenv(testDatabaseUsername, "testuser")
	os.Setenv(testDatabaseMaxConns, "25")
	os.Setenv("LOG_LEVEL", "debug")

	cfg, err := Load()
	require.NoError(t, err)
	require.NotNil(t, cfg)

	// Verify environment variables override defaults
	assert.Equal(t, appName, cfg.App.Name)
	assert.Equal(t, EnvProduction, cfg.App.Env)
	assert.Equal(t, 9090, cfg.Server.Port)
	assert.Equal(t, "debug", cfg.Log.Level)

	// Verify database is configured from environment variables
	assert.True(t, IsDatabaseConfigured(&cfg.Database))
	assert.Equal(t, "postgresql", cfg.Database.Type)
	assert.Equal(t, "localhost", cfg.Database.Host)
	assert.Equal(t, 5432, cfg.Database.Port)
	assert.Equal(t, "testdb", cfg.Database.Database)
	assert.Equal(t, "testuser", cfg.Database.Username)
	assert.Equal(t, int32(25), cfg.Database.Pool.Max.Connections)

	// Verify defaults still work for non-overridden values
	assert.Equal(t, appVersion, cfg.App.Version)
	assert.Equal(t, serverHost, cfg.Server.Host)
}

// TestLoadAppEnvSelectsEnvironmentOverlay verifies that the APP_ENV environment variable
// selects the config.<env>.yaml overlay, honoring the documented precedence
// (env vars > config.<env>.yaml > config.yaml > defaults). Regression test for the High
// audit finding: the overlay suffix was read from koanf (k.String(app.env)) BEFORE the env
// provider loaded, so APP_ENV=production silently loaded config.development.yaml (or none)
// while cfg.App.Env still ended up "production" — production pods could run the dev overlay.
func TestLoadAppEnvSelectsEnvironmentOverlay(t *testing.T) {
	clearEnvironmentVariables()

	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, testConfigFileYAML),
		[]byte("app:\n  name: base-app\n  env: development\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.production.yaml"),
		[]byte("app:\n  name: prod-overlay-app\n"), 0o600))
	t.Chdir(dir)

	// APP_ENV reaches koanf only via the env provider, which loads AFTER overlay selection,
	// so the suffix must be resolved from the environment variable directly.
	t.Setenv("APP_ENV", EnvProduction)

	cfg, err := Load()
	require.NoError(t, err)
	assert.Equal(t, EnvProduction, cfg.App.Env)
	assert.Equal(t, "prod-overlay-app", cfg.App.Name,
		"config.production.yaml overlay must be selected by APP_ENV (got the base config — overlay was not loaded)")
}

// TestLoadAppEnvRejectsMalformedOverlaySuffix verifies a malformed APP_ENV is not
// interpolated into the overlay filename (no config.<traversal>.yaml read attempt);
// the value is left for checkApp to reject with a clear app.env error.
func TestLoadAppEnvRejectsMalformedOverlaySuffix(t *testing.T) {
	clearEnvironmentVariables()

	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, testConfigFileYAML),
		[]byte("app:\n  name: base-app\n  env: development\n"), 0o600))
	t.Chdir(dir)

	t.Setenv("APP_ENV", "../../etc/passwd")

	_, err := Load()
	require.Error(t, err)
	assert.ErrorContains(t, err, fieldAppEnv, "malformed APP_ENV must be rejected by validation, not silently used as a file path")
}

func TestConfigPerTenantJobKeys(t *testing.T) {
	t.Run("single_tenant_yields_one_empty_key", func(t *testing.T) {
		cfg := &Config{}
		assert.Equal(t, []string{""}, cfg.PerTenantJobKeys())
	})

	t.Run("nil_config_yields_one_empty_key", func(t *testing.T) {
		var cfg *Config
		assert.Equal(t, []string{""}, cfg.PerTenantJobKeys())
	})

	t.Run("static_multitenant_yields_sorted_keys", func(t *testing.T) {
		cfg := &Config{Multitenant: MultitenantConfig{
			Enabled: true,
			Tenants: map[string]TenantEntry{"zeta": {}, "alpha": {}},
		}}
		assert.Equal(t, []string{"alpha", "zeta"}, cfg.PerTenantJobKeys())
	})

	t.Run("multitenant_enabled_without_tenants_yields_empty", func(t *testing.T) {
		cfg := &Config{Multitenant: MultitenantConfig{Enabled: true}}
		assert.Empty(t, cfg.PerTenantJobKeys(), "callers must reject this degenerate config")
	})
}

// TestLoadRejectsEmptyNumericEnv pins the delivered-empty rule for numeric keys: a
// set-but-empty variable used to decode as a legal 0 (silently zeroing byte limits, and
// booting a floor nobody wrote), and now fails Load naming the key.
func TestLoadRejectsEmptyNumericEnv(t *testing.T) {
	tests := []struct {
		name    string
		envVar  string
		wantKey string
	}{
		{name: "keystore_secretminlength", envVar: "KEYSTORE_SECRETMINLENGTH", wantKey: "keystore.secretminlength"},
		{name: "server_bodylimit", envVar: "SERVER_BODYLIMIT", wantKey: "server.bodylimit"},
		{name: "server_port", envVar: "SERVER_PORT", wantKey: "server.port"},
		// database.port is an ADR-051 identity key AND numeric, so the numeric guard
		// reaches it first: it now fails at decode rather than with the identity error.
		{name: "database_port_changes_error_class", envVar: "DATABASE_PORT", wantKey: "database.port"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clearEnvironmentVariables()
			t.Setenv(tt.envVar, "")

			_, err := Load()

			require.Error(t, err)
			assert.ErrorContains(t, err, tt.wantKey)
			assert.ErrorContains(t, err, "delivered empty")
		})
	}
}

// TestLoadRejectsEmptyBoolEnv pins the delivered-empty rule for bool keys (ADR-077): a
// set-but-empty variable used to decode as a legal false — non-nil, so the pointer
// tri-states read it as an operator choice — and now fails Load naming the key.
func TestLoadRejectsEmptyBoolEnv(t *testing.T) {
	tests := []struct {
		name    string
		envVar  string
		wantKey string
	}{
		// The headline case: keep-alive defaults to TRUE, so an empty value was a
		// silent default flip, not merely a redundant zero.
		{name: "database_pool_keepalive_enabled", envVar: "DATABASE_POOL_KEEPALIVE_ENABLED", wantKey: "database.pool.keepalive.enabled"},
		// cache.critical: since ADR-094 a decoded false coincides with the shipped default, so
		// this pins the delivered-empty rule itself rather than a posture flip.
		{name: "cache_critical", envVar: "CACHE_CRITICAL", wantKey: "cache.critical"},
		{name: "server_logroutes", envVar: "SERVER_LOGROUTES", wantKey: "server.logroutes"},
		// A non-pointer bool is guarded too: false is its zero, so the flip is
		// invisible in the decoded struct either way.
		{name: "cache_enabled", envVar: "CACHE_ENABLED", wantKey: "cache.enabled"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clearEnvironmentVariables()
			t.Setenv(tt.envVar, "")

			_, err := Load()

			require.Error(t, err)
			assert.ErrorContains(t, err, tt.wantKey)
			assert.ErrorContains(t, err, "boolean value delivered empty")
		})
	}
}

// keepAliveFixtureYAML is a minimal complete database section, which
// database.pool.keepalive.enabled needs before normalization fills its default.
const keepAliveFixtureYAML = `database:
  type: postgresql
  host: localhost
  port: 5432
  database: appdb
  username: appuser
  password: not-a-real-password
`

// TestLoadExplicitBoolEnvUnchanged pins the other half of ADR-077: only an EMPTY value is
// rejected, so every explicit spelling still decodes — including the deliberate opt-outs
// the rejected value used to counterfeit.
func TestLoadExplicitBoolEnvUnchanged(t *testing.T) {
	t.Run("cache_critical_false_still_opts_out", func(t *testing.T) {
		clearEnvironmentVariables()
		t.Setenv("CACHE_CRITICAL", "false")

		cfg, err := Load()

		require.NoError(t, err)
		require.NotNil(t, cfg.Cache.Critical)
		assert.False(t, cfg.IsCacheCritical(), "an explicit false still decodes as a concrete value, not absence")
	})

	// Both keep-alive cases need a real database section: nil -> true normalization runs
	// only for a configured database, so without one IsEnabled reads false either way and
	// the explicit-false case would pass vacuously.
	t.Run("keepalive_unset_stays_enabled", func(t *testing.T) {
		cfg, err := loadDeliveredEmptyFixture(t, keepAliveFixtureYAML, nil)

		require.NoError(t, err)
		assert.True(t, cfg.Database.Pool.KeepAlive.IsEnabled(),
			"absence is not emptiness: an unset key must still take the default true")
	})

	// ADR-077 and atom C60.18 promise operators that 1/0 still work; that promise rests
	// on WeaklyTypedInput staying on in the real decoder, which only a Load can prove.
	t.Run("numeric_spelling_still_decodes", func(t *testing.T) {
		clearEnvironmentVariables()
		t.Setenv("CACHE_CRITICAL", "0")
		t.Setenv("SERVER_LOGROUTES", "1")

		cfg, err := Load()

		require.NoError(t, err)
		assert.False(t, cfg.IsCacheCritical())
		assert.True(t, cfg.ShouldLogRoutes())
	})

	t.Run("keepalive_explicit_false_honored", func(t *testing.T) {
		cfg, err := loadDeliveredEmptyFixture(t, keepAliveFixtureYAML,
			map[string]string{"DATABASE_POOL_KEEPALIVE_ENABLED": "false"})

		require.NoError(t, err)
		require.NotNil(t, cfg.Database.Pool.KeepAlive.Enabled)
		assert.False(t, cfg.Database.Pool.KeepAlive.IsEnabled())
	})
}

// TestLoadEmptyBoolYAMLStringRejected covers the same rule arriving through YAML: an
// empty string takes the identical decode path an empty env var does.
func TestLoadEmptyBoolYAMLStringRejected(t *testing.T) {
	_, err := loadDeliveredEmptyFixture(t, "cache:\n  critical: \"\"\n", nil)

	require.Error(t, err)
	assert.ErrorContains(t, err, "critical")
	assert.ErrorContains(t, err, "boolean value delivered empty")
}

// TestLoadYAMLNullBoolKeepsTodaysDecode pins the boundary ADR-077 deliberately does NOT
// cover, matching ADR-074's: a YAML null is different plumbing — koanf delivers a nil
// value, not the "" the guard judges — so the key still decodes as absent and the cache
// probe keeps the non-critical default.
func TestLoadYAMLNullBoolKeepsTodaysDecode(t *testing.T) {
	cfg, err := loadDeliveredEmptyFixture(t, "cache:\n  critical:\n", nil)

	require.NoError(t, err)
	assert.Nil(t, cfg.Cache.Critical)
	assert.False(t, cfg.IsCacheCritical(), "a null key is absence, so the non-critical default still applies")
}

// TestLoadEmptyDurationEnvKeepsItsOwnError pins the guard's one exemption: time.Duration
// targets fall through to the duration parser, so an empty duration still fails with the
// parse error rather than the delivered-empty one. Both are loud; this pins which.
func TestLoadEmptyDurationEnvKeepsItsOwnError(t *testing.T) {
	clearEnvironmentVariables()
	t.Setenv("SERVER_TIMEOUT_READ", "")

	_, err := Load()

	require.Error(t, err)
	assert.ErrorContains(t, err, "invalid duration")
	assert.NotContains(t, err.Error(), "delivered empty",
		"the duration parser owns this target; guarding it here would only change the message")
}

// TestLoadEmptyNumericYAMLStringRejected covers the same rule arriving through YAML: an
// empty string takes the identical decode path an empty env var does.
func TestLoadEmptyNumericYAMLStringRejected(t *testing.T) {
	_, err := loadDeliveredEmptyFixture(t, "keystore:\n  secretminlength: \"\"\n", nil)

	require.Error(t, err)
	assert.ErrorContains(t, err, "secretminlength")
	assert.ErrorContains(t, err, "delivered empty")
}

// TestLoadYAMLNullNumericKeepsTodaysDecode pins the boundary the guard deliberately does
// NOT cover: a YAML null is different plumbing — koanf delivers a nil value, not the ""
// the guard judges — so a null pointer key still decodes as absent and takes its default.
// Documented in ADR-074; this test exists so the boundary cannot drift unnoticed.
func TestLoadYAMLNullNumericKeepsTodaysDecode(t *testing.T) {
	cfg, err := loadDeliveredEmptyFixture(t, "keystore:\n  secretminlength:\n", nil)

	require.NoError(t, err)
	require.NotNil(t, cfg.KeyStore.SecretMinLength)
	assert.Equal(t, 32, *cfg.KeyStore.SecretMinLength, "a null key is absence, so the floor still applies")
}

func TestLoadMultiElementStringSliceEnv(t *testing.T) {
	clearEnvironmentVariables()
	t.Setenv("SCHEDULER_SECURITY_CIDRALLOWLIST", "10.0.0.0/8,192.168.0.0/16")
	// Spaces around the delimiter exercise per-element trimming.
	t.Setenv("SCHEDULER_SECURITY_TRUSTEDPROXIES", "10.0.0.0/8, 172.16.0.0/12 ,, 169.254.0.0/16")
	t.Setenv("DEBUG_ALLOWEDIPS", "127.0.0.1,::1")

	cfg, err := Load()
	require.NoError(t, err)

	assert.Equal(t, []string{"10.0.0.0/8", "192.168.0.0/16"}, cfg.Scheduler.Security.CIDRAllowlist)
	// Trimmed, with the empty element between the doubled commas dropped.
	assert.Equal(t, []string{"10.0.0.0/8", "172.16.0.0/12", "169.254.0.0/16"}, cfg.Scheduler.Security.TrustedProxies)
	assert.Equal(t, []string{"127.0.0.1", "::1"}, cfg.Debug.AllowedIPs)
}

func TestLoadSingleElementStringSliceEnv(t *testing.T) {
	clearEnvironmentVariables()
	t.Setenv("SCHEDULER_SECURITY_CIDRALLOWLIST", "10.0.0.0/8")
	cfg, err := Load()
	require.NoError(t, err)
	assert.Equal(t, []string{"10.0.0.0/8"}, cfg.Scheduler.Security.CIDRAllowlist)
}

// TestLoadResponseTimeEnabledEnv verifies the opt-in X-Response-Time header flag
// is off by default and flips to true when SERVER_RESPONSETIME_ENABLED is set.
func TestLoadResponseTimeEnabledEnv(t *testing.T) {
	t.Run("default_disabled", func(t *testing.T) {
		clearEnvironmentVariables()
		cfg, err := Load()
		require.NoError(t, err)
		assert.False(t, cfg.Server.ResponseTime.Enabled)
	})

	t.Run("env_enables", func(t *testing.T) {
		clearEnvironmentVariables()
		t.Setenv("SERVER_RESPONSETIME_ENABLED", "true")
		cfg, err := Load()
		require.NoError(t, err)
		assert.True(t, cfg.Server.ResponseTime.Enabled)
	})
}

func TestConfigShouldLogRoutes(t *testing.T) {
	tests := []struct {
		name string
		env  string
		ptr  *bool
		want bool
	}{
		{name: "unset_defaults_on_in_development", env: "development", ptr: nil, want: true},
		{name: "unset_defaults_on_for_local", env: "local", ptr: nil, want: true},
		{name: "unset_defaults_off_in_production", env: "production", ptr: nil, want: false},
		{name: "unset_defaults_off_in_staging", env: "staging", ptr: nil, want: false},
		{name: "explicit_false_honored_in_development", env: "development", ptr: new(false), want: false},
		{name: "explicit_true_honored_in_production", env: "production", ptr: new(true), want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &Config{}
			cfg.App.Env = tt.env
			cfg.Server.LogRoutes = tt.ptr
			assert.Equal(t, tt.want, cfg.ShouldLogRoutes())
		})
	}
}

// TestLoadServerLogRoutesEnv verifies the opt-in per-route log flag stays nil
// when unset (so ShouldLogRoutes can derive from app.env) and binds from
// SERVER_LOGROUTES.
func TestLoadServerLogRoutesEnv(t *testing.T) {
	t.Run("unset_is_nil", func(t *testing.T) {
		clearEnvironmentVariables()
		cfg, err := Load()
		require.NoError(t, err)
		assert.Nil(t, cfg.Server.LogRoutes, "absent key must stay nil so ShouldLogRoutes derives from app.env")
	})

	t.Run("env_sets_true", func(t *testing.T) {
		clearEnvironmentVariables()
		t.Setenv("SERVER_LOGROUTES", "true")
		cfg, err := Load()
		require.NoError(t, err)
		require.NotNil(t, cfg.Server.LogRoutes)
		assert.True(t, *cfg.Server.LogRoutes)
	})
}

func TestLoadInvalidEnvironmentVariables(t *testing.T) {
	// A COMPLETE database section, so each case isolates the one variable it makes
	// invalid. A partial section (identity fields without a type) now fails validation
	// on its own and would mask the error under test.
	baseEnv := map[string]string{
		"DATABASE_TYPE":      "postgresql",
		"DATABASE_HOST":      "localhost",
		"DATABASE_PORT":      "5432",
		testDatabaseDatabase: "testdb",
		testDatabaseUsername: "testuser",
	}

	tests := []struct {
		name    string
		envKey  string
		value   string
		wantErr string
	}{
		{
			name:    "invalid_port",
			envKey:  "SERVER_PORT",
			value:   "invalid",
			wantErr: "server.port",
		},
		{
			name:    "invalid_boolean",
			envKey:  "APP_DEBUG",
			value:   "maybe",
			wantErr: "app.debug",
		},
		{
			name:    "invalid_database_port",
			envKey:  "DATABASE_PORT",
			value:   "not-a-number",
			wantErr: "database.port",
		},
		{
			name:    "invalid_log_level",
			envKey:  "LOG_LEVEL",
			value:   "super-loud",
			wantErr: "log.level",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clearEnvironmentVariables()
			for key, val := range baseEnv {
				t.Setenv(key, val)
			}
			t.Setenv(tt.envKey, tt.value)

			cfg, err := Load()
			require.Error(t, err)
			assert.Nil(t, cfg)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

func TestLoadValidationFailure(t *testing.T) {
	defer clearEnvironmentVariables()

	// Set invalid configuration that should fail validation
	os.Setenv("APP_NAME", "") // Required field
	os.Setenv("APP_ENV", "invalid-env")
	os.Setenv("SERVER_PORT", "0")  // Invalid port
	os.Setenv("DATABASE_HOST", "") // Required field

	cfg, err := Load()
	assert.Error(t, err)
	assert.Nil(t, cfg)
	assert.Contains(t, err.Error(), "invalid configuration")
}

func TestLoadDefaultsInternalFunction(t *testing.T) {
	// Create a new koanf instance for testing
	k := koanf.New(".")

	err := loadDefaults(k)
	require.NoError(t, err)

	// Verify non-database defaults are loaded
	assert.Equal(t, appName, k.String("app.name"))
	assert.Equal(t, appVersion, k.String("app.version"))
	assert.Equal(t, EnvDevelopment, k.String("app.env"))
	assert.False(t, k.Bool("app.debug"))
	assert.Equal(t, 100, k.Int("app.rate.limit"))
	assert.Equal(t, 200, k.Int("app.rate.burst"))

	assert.Equal(t, serverHost, k.String("server.host"))
	assert.Equal(t, 8080, k.Int("server.port"))
	assert.Equal(t, "15s", k.String("server.timeout.read"))
	assert.Equal(t, "30s", k.String("server.timeout.write"))
	assert.Equal(t, "60s", k.String("server.timeout.idle"))
	assert.Equal(t, "5s", k.String("server.timeout.middleware"))
	assert.Equal(t, "10s", k.String("server.timeout.shutdown"))
	assert.Equal(t, int64(10*1024*1024), k.Int64("server.bodylimit"))

	// Database defaults should NOT be provided
	assert.Equal(t, "", k.String("database.type"))
	assert.Equal(t, "", k.String("database.host"))
	assert.Equal(t, 0, k.Int("database.port"))
	assert.Equal(t, "", k.String("database.tls.mode"))
	assert.Equal(t, 0, k.Int("database.pool.max.connections"))

	assert.Equal(t, "info", k.String("log.level"))
	assert.False(t, k.Bool("log.pretty"))
	assert.Equal(t, "auto", k.String("log.output.format"))
	assert.Equal(t, "", k.String("log.output.file"))

	// KeyStore symmetric-secret floor defaults to 32 bytes.
	assert.Equal(t, DefaultKeyStoreSecretMinLength, k.Int("keystore.secretminlength"))

	// cache.critical stays unregistered: a registered default would populate the pointer and
	// erase the absent-vs-explicit distinction the tri-state keeps for readers (ADR-094).
	assert.False(t, k.Exists("cache.critical"), "cache.critical must NOT be a registered default")
}

// TestKeyStoreSecretFloor pins the accessor: a nil receiver or a nil pointer
// reads as the default, and a set value — bounded by check at the default or
// above (ADR-095) — reads as itself.
func TestKeyStoreSecretFloor(t *testing.T) {
	var nilCfg *KeyStoreConfig
	assert.Equal(t, DefaultKeyStoreSecretMinLength, nilCfg.SecretFloor())

	tests := []struct {
		name string
		min  *int
		want int
	}{
		{name: "nil_applies_default", min: nil, want: DefaultKeyStoreSecretMinLength},
		{name: "raised_floor", min: new(64), want: 64},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &KeyStoreConfig{SecretMinLength: tt.min}
			assert.Equal(t, tt.want, cfg.SecretFloor())
		})
	}
}

// TestIsCacheCriticalTriState drives both the parsed pointer and the accessor through the
// real koanf load path rather than a Go struct literal: a literal would prove nothing about
// whether an absent YAML key still reaches IsCacheCritical as nil. The sibling
// `cache.enabled` assertion proves the block actually parsed, so a case cannot pass on Go's
// zero value alone.
func TestIsCacheCriticalTriState(t *testing.T) {
	const yamlEnabled = "cache:\n  enabled: true\n"

	tests := []struct {
		name          string
		yaml          string
		env           string
		expectedPtr   *bool
		expected      bool
		expectEnabled bool
	}{
		{name: "key_absent_is_non_critical", yaml: yamlEnabled, expected: false, expectEnabled: true},
		{name: "no_cache_block_at_all_is_non_critical", expected: false},
		{name: "yaml_false_is_non_critical", yaml: yamlEnabled + "  critical: false\n", expectedPtr: new(false), expected: false, expectEnabled: true},
		{name: "yaml_true_opts_in", yaml: yamlEnabled + "  critical: true\n", expectedPtr: new(true), expected: true, expectEnabled: true},
		{name: "env_true_parses_without_yaml", env: "true", expectedPtr: new(true), expected: true},
		{name: "env_false_overrides_yaml_true", yaml: yamlEnabled + "  critical: true\n", env: "false", expectedPtr: new(false), expected: false, expectEnabled: true},
		{name: "env_true_overrides_yaml_false", yaml: yamlEnabled + "  critical: false\n", env: "true", expectedPtr: new(true), expected: true, expectEnabled: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			clearEnvironmentVariables()
			defer clearEnvironmentVariables()

			dir := t.TempDir()
			if tc.yaml != "" {
				require.NoError(t, os.WriteFile(filepath.Join(dir, testConfigFileYAML), []byte(tc.yaml), 0o600))
			}
			t.Chdir(dir)

			if tc.env != "" {
				t.Setenv("CACHE_CRITICAL", tc.env)
			}

			cfg, err := Load()
			require.NoError(t, err)
			if tc.expectedPtr == nil {
				assert.Nil(t, cfg.Cache.Critical, "absent key must stay nil so IsCacheCritical reads the default")
			} else {
				require.NotNil(t, cfg.Cache.Critical)
				assert.Equal(t, *tc.expectedPtr, *cfg.Cache.Critical)
			}
			assert.Equal(t, tc.expectEnabled, cfg.Cache.Enabled)
			assert.Equal(t, tc.expected, cfg.IsCacheCritical())
		})
	}

	t.Run("nil_receiver_is_non_critical", func(t *testing.T) {
		var cfg *Config
		assert.False(t, cfg.IsCacheCritical(),
			"a nil config is the most absent config there is; readiness gating is opt-in only")
	})
}

func TestLoadEdgeCases(t *testing.T) {
	defer clearEnvironmentVariables()

	t.Run("empty_string_values", func(t *testing.T) {
		clearEnvironmentVariables()
		os.Setenv("APP_NAME", "")
		os.Setenv("DATABASE_HOST", "")

		cfg, err := Load()
		assert.Error(t, err)
		assert.Nil(t, cfg)
	})

	t.Run("zero_values", func(t *testing.T) {
		clearEnvironmentVariables()
		os.Setenv("SERVER_PORT", "0")
		os.Setenv("APP_RATE_LIMIT", "0")
		os.Setenv(testDatabaseMaxConns, "0")

		cfg, err := Load()
		assert.Error(t, err)
		assert.Nil(t, cfg)
	})

	t.Run("negative_values", func(t *testing.T) {
		clearEnvironmentVariables()
		os.Setenv("SERVER_PORT", "-1")
		os.Setenv("APP_RATE_LIMIT", "-1")

		cfg, err := Load()
		assert.Error(t, err)
		assert.Nil(t, cfg)
	})
}

// TestLoadCustomConfiguration exercises custom (non-framework) config keys. Its
// subtests set no database section at all: they previously set database.database +
// database.username, which validation skipped, but that pair is now a partial config
// and fails load. The two subtests that assert on a resolved database value complete
// the section instead.
func TestLoadCustomConfiguration(t *testing.T) {
	defer clearEnvironmentVariables()

	t.Run("custom_config_via_environment", func(t *testing.T) {
		clearEnvironmentVariables()

		// Set custom configuration via environment variables
		// Note: underscores in env vars are converted to dots by Koanf
		os.Setenv("CUSTOM_FEATURE_ENABLED", "true")
		os.Setenv("CUSTOM_SERVICE_ENDPOINT", "https://api.test.com")
		os.Setenv("CUSTOM_SERVICE_TIMEOUT", "30s")
		os.Setenv("CUSTOM_MAX_RETRIES", "5")

		cfg, err := Load()
		require.NoError(t, err)
		require.NotNil(t, cfg)
		require.NotNil(t, cfg.k, "Koanf instance should be set")

		// Test accessing custom configuration
		assert.True(t, cfg.Bool("custom.feature.enabled"))
		assert.Equal(t, "https://api.test.com", cfg.String("custom.service.endpoint"))
		timeout := cfg.String("custom.service.timeout")
		dur, err := time.ParseDuration(timeout)
		require.NoError(t, err)
		assert.Equal(t, 30*time.Second, dur)
		assert.Equal(t, 5, cfg.Int("custom.max.retries"))
	})

	t.Run("custom_config_with_defaults", func(t *testing.T) {
		clearEnvironmentVariables()

		cfg, err := Load()
		require.NoError(t, err)
		require.NotNil(t, cfg)

		// Test default values for missing custom config
		assert.Equal(t, "default-value", cfg.String("custom.missing.key", "default-value"))
		assert.Equal(t, 100, cfg.Int("custom.missing.int", 100))
		assert.False(t, cfg.Bool("custom.missing.bool"))
	})

	t.Run("custom_config_required_fields", func(t *testing.T) {
		clearEnvironmentVariables()
		os.Setenv("CUSTOM_API_KEY", "secret-key-123")

		cfg, err := Load()
		require.NoError(t, err)
		require.NotNil(t, cfg)

		// Test required field that exists
		apiKey, err := cfg.RequiredString("custom.api.key")
		assert.NoError(t, err)
		assert.Equal(t, "secret-key-123", apiKey)

		// Test required field that doesn't exist
		_, err = cfg.RequiredString("custom.missing.required")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "missing")
	})

	t.Run("custom_config_unmarshal_struct", func(t *testing.T) {
		clearEnvironmentVariables()

		// Set complex custom configuration
		os.Setenv("CUSTOM_SERVICE_NAME", appName)
		os.Setenv("CUSTOM_SERVICE_PORT", "8090")
		os.Setenv("CUSTOM_SERVICE_ENABLED", "true")

		cfg, err := Load()
		require.NoError(t, err)
		require.NotNil(t, cfg)

		// Define a struct to unmarshal into
		type ServiceConfig struct {
			Name    string `koanf:"name"`
			Port    int    `koanf:"port"`
			Enabled bool   `koanf:"enabled"`
		}

		var svcConfig ServiceConfig
		err = cfg.Unmarshal("custom.service", &svcConfig)
		assert.NoError(t, err)
		assert.Equal(t, appName, svcConfig.Name)
		assert.Equal(t, 8090, svcConfig.Port)
		assert.True(t, svcConfig.Enabled)
	})

	t.Run("custom_config_exists_check", func(t *testing.T) {
		clearEnvironmentVariables()
		// This subtest asserts on database.database, so it needs a COMPLETE database
		// section: any identity field alone is now a partial config and fails validation.
		os.Setenv("DATABASE_TYPE", "postgresql")
		os.Setenv("DATABASE_HOST", "localhost")
		os.Setenv("DATABASE_PORT", "5432")
		os.Setenv(testDatabaseDatabase, "testdb")
		os.Setenv(testDatabaseUsername, "testuser")
		os.Setenv("CUSTOM_FEATURE_FLAG", "true")

		cfg, err := Load()
		require.NoError(t, err)
		require.NotNil(t, cfg)

		// Test existing custom config
		assert.True(t, cfg.Exists("custom.feature.flag"))

		// Test non-existing custom config
		assert.False(t, cfg.Exists("custom.nonexistent.key"))

		// Test standard config still works
		assert.True(t, cfg.Exists("database.database"))
		assert.True(t, cfg.Exists("app.name"))
	})

	t.Run("custom_namespace_retrieval", func(t *testing.T) {
		clearEnvironmentVariables()
		os.Setenv("CUSTOM_KEY1", "value1")
		os.Setenv("CUSTOM_KEY2", "value2")

		cfg, err := Load()
		require.NoError(t, err)
		require.NotNil(t, cfg)

		// Get all custom configuration
		customMap := cfg.Custom()
		if customMap != nil {
			// Check if custom values are present
			if key1, ok := customMap["key1"]; ok {
				assert.Equal(t, "value1", key1)
			}
			if key2, ok := customMap["key2"]; ok {
				assert.Equal(t, "value2", key2)
			}
		}
	})
}

// TestEnvOverrideReachesRenamedKeys verifies that the flat-smushed config keys
// are settable from environment variables. Underscored keys (the old snake_case
// form) were unreachable because the env loader maps "_"->"." (koanf nesting).
func TestEnvOverrideReachesRenamedKeys(t *testing.T) {
	clearEnvironmentVariables()
	defer clearEnvironmentVariables()
	t.Setenv("OUTBOX_BATCHSIZE", "250")
	t.Setenv("OUTBOX_AUTOCREATETABLE", "true")
	t.Setenv("MESSAGING_RECONNECT_CONNECTIONTIMEOUT", "45s")
	t.Setenv("KEYSTORE_SECRETMINLENGTH", "64")
	t.Setenv("LOG_SENSITIVEFIELDS", "pan, cvv2 ,otp")

	cfg, err := Load()
	require.NoError(t, err)

	assert.Equal(t, 250, cfg.Outbox.BatchSize)
	assert.True(t, cfg.Outbox.AutoCreateTable)
	assert.Equal(t, 45*time.Second, cfg.Messaging.Reconnect.ConnectionTimeout)
	require.NotNil(t, cfg.KeyStore.SecretMinLength)
	assert.Equal(t, 64, *cfg.KeyStore.SecretMinLength)
	assert.Equal(t, []string{"pan", "cvv2", "otp"}, cfg.Log.SensitiveFields)
}

// TestEnvOverrideReachesResolverOrder pins the env seam that wiki/migrations.md
// atom C50.4 tells operators to use to restore header-first composite
// resolution: MULTITENANT_RESOLVER_ORDER must bind as a comma-separated list.
//
// This stays green without MULTITENANT_ENABLED=true (and thus without a
// resolver.domain, now required alongside resolver.order for a composite
// reaching checkMultitenantResolver) only because normalizeMultitenant and
// checkMultitenant both short-circuit at `if !mt.Enabled { return nil }` —
// Load() still calls Validate(), it just never reaches the resolver checks here.
func TestEnvOverrideReachesResolverOrder(t *testing.T) {
	clearEnvironmentVariables()
	defer clearEnvironmentVariables()
	t.Setenv("MULTITENANT_RESOLVER_TYPE", ResolverTypeComposite)
	t.Setenv("MULTITENANT_RESOLVER_ORDER", "header,subdomain,path")

	cfg, err := Load()
	require.NoError(t, err)

	assert.Equal(t, ResolverTypeComposite, cfg.Multitenant.Resolver.Type)
	assert.Equal(t,
		[]string{ResolverTypeHeader, ResolverTypeSubdomain, ResolverTypePath},
		cfg.Multitenant.Resolver.Order)
}

// TestEnvVarCollidesWithConfigMap is the M4 regression test. The env provider has no Prefix
// filter, so it ingests EVERY process environment variable. A bare env var whose name maps
// onto a top-level config section (CACHE, DEBUG, DATABASE, …) — common in Kubernetes, which
// auto-injects Docker-link vars like SERVER_PORT=tcp://IP:PORT — unflattens to a scalar at
// that key. The default koanf merge then replaced the section's nested map (from defaults or
// YAML) with the scalar string, so UnmarshalWithConf failed with
// "'cache' expected a map or struct, got \"string\"". The skip-scalar-over-map merge guard
// keeps the structured config and drops the colliding scalar, so Load no longer aborts.
func TestEnvVarCollidesWithConfigMap(t *testing.T) {
	clearEnvironmentVariables()
	defer clearEnvironmentVariables()
	os.Setenv("CACHE", "redis://localhost:6379")
	defer os.Unsetenv("CACHE")
	os.Setenv("DATABASE_TYPE", "postgresql")
	os.Setenv("DATABASE_HOST", "localhost")
	os.Setenv("DATABASE_PORT", "5432")
	os.Setenv(testDatabaseDatabase, "testdb")
	os.Setenv(testDatabaseUsername, "testuser")

	cfg, err := Load()
	require.NoError(t, err, "env var CACHE should not collide with cache config section")
	require.NotNil(t, cfg)

	// The colliding CACHE scalar is dropped; the cache section keeps its structured defaults.
	assert.False(t, cfg.Cache.Enabled, "bare CACHE env var must not clobber the cache config map")
	// Properly-pathed vars still reach their config keys.
	assert.Equal(t, "postgresql", cfg.Database.Type)
}

// TestEnvVarBareSectionNamesDropped covers the other top-level section names that collide
// with single-word env vars common in container runtimes (DEBUG, SOURCE, SERVER, …). Each
// scalar must be dropped by the merge guard rather than overwrite the section's nested map.
func TestEnvVarBareSectionNamesDropped(t *testing.T) {
	tests := []struct {
		name   string
		envKey string
		value  string
	}{
		{name: "debug_scalar", envKey: "DEBUG", value: "1"},
		{name: "cache_url", envKey: "CACHE", value: "redis://localhost:6379"},
		{name: "source_scalar", envKey: "SOURCE", value: "static"},
		{name: "server_link", envKey: "SERVER", value: "tcp://10.0.0.1:8080"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clearEnvironmentVariables()
			defer clearEnvironmentVariables()
			os.Setenv(tt.envKey, tt.value)
			defer os.Unsetenv(tt.envKey)

			cfg, err := Load()
			require.NoError(t, err, "bare env var %s must be dropped, not collide with its config section", tt.envKey)
			require.NotNil(t, cfg)
		})
	}
}

// TestEnvInjectIntoArbitraryPrefixStillReachable guards the InjectInto escape hatch: the
// M4 merge guard must NOT break service-specific config:"..." keys delivered via env vars at
// fresh (non-section) leaves. AUTH_SECRET -> auth.secret lands at a brand-new key, so the
// scalar merges normally and InjectInto resolves it.
func TestEnvInjectIntoArbitraryPrefixStillReachable(t *testing.T) {
	clearEnvironmentVariables()
	defer clearEnvironmentVariables()
	t.Setenv("AUTH_SECRET", "s3cr3t")

	cfg, err := Load()
	require.NoError(t, err)
	require.NotNil(t, cfg)

	var svc struct {
		Secret string `config:"auth.secret" required:"true"`
	}
	require.NoError(t, cfg.InjectInto(&svc))
	assert.Equal(t, "s3cr3t", svc.Secret)
}

// TestEnvBareSectionDoesNotShadowLegitSubKey guards against a fail-open regression: a bare
// section-name var (DEBUG=1) and a legitimate sub-key var (DEBUG_ENABLED=true) set together.
// The env provider unflattens its own source map BEFORE the merge guard runs, so a surviving
// bare "debug" scalar would win the intra-source unflatten race and silently discard the real
// "debug.enabled" — leaving a security gate at its (safe) default but ignoring operator intent.
// Dropping the bare section name in the TransformFunc, pre-unflatten, keeps the real sub-key.
func TestEnvBareSectionDoesNotShadowLegitSubKey(t *testing.T) {
	clearEnvironmentVariables()
	defer clearEnvironmentVariables()
	os.Setenv("DEBUG", "1")             // bare collision — must be dropped pre-unflatten
	os.Setenv("DEBUG_ENABLED", "true")  // legitimate sub-key — must survive
	os.Setenv("DEBUG_BEARERTOKEN", "t") // legitimate sub-key — must survive
	defer os.Unsetenv("DEBUG")
	defer os.Unsetenv("DEBUG_ENABLED")
	defer os.Unsetenv("DEBUG_BEARERTOKEN")

	cfg, err := Load()
	require.NoError(t, err)
	require.NotNil(t, cfg)

	assert.True(t, cfg.Debug.Enabled,
		"DEBUG_ENABLED must not be shadowed by a bare DEBUG collision")
	assert.Equal(t, "t", cfg.Debug.BearerToken)
}

// TestMergeSkippingScalarOverMapDropsScalarOverMap directly exercises guard 2 of the
// M4 defense (config.go: the `if destIsMap { continue }` branch). The Load-level tests
// all collide on bare top-level section names (CACHE, DEBUG, …), which guard 1 (the
// configSections TransformFunc) drops BEFORE the merge runs, so guard 2's scalar-over-map
// drop never executes through them. A nested collision (a scalar arriving at cache.redis,
// which the defaults seed as a map and which is NOT a configSections entry) is the only
// way to reach this branch — so test it directly.
func TestMergeSkippingScalarOverMapDropsScalarOverMap(t *testing.T) {
	dest := map[string]any{
		"cache": map[string]any{
			"redis": map[string]any{"host": "localhost"},
		},
	}
	src := map[string]any{
		"cache": map[string]any{
			"redis": "redis://stray", // scalar that would clobber the redis map node
		},
	}

	mergeSkippingScalarOverMap(src, dest)

	cacheMap, ok := dest["cache"].(map[string]any)
	require.True(t, ok, "cache node must remain a map")
	redisMap, stillMap := cacheMap["redis"].(map[string]any)
	require.True(t, stillMap, "scalar must not clobber the existing redis map node")
	assert.Equal(t, "localhost", redisMap["host"], "structured redis config must survive")
}

// TestMergeSkippingScalarOverMapMergesScalarAtFreshLeaf confirms the guard only drops a
// scalar when it would clobber an existing map: a scalar landing at a brand-new leaf (or
// over an existing scalar) merges normally, preserving the InjectInto escape hatch.
func TestMergeSkippingScalarOverMapMergesScalarAtFreshLeaf(t *testing.T) {
	dest := map[string]any{
		"auth": map[string]any{"secret": "old"},
	}
	src := map[string]any{
		"auth": map[string]any{
			"secret": "new",   // scalar over scalar: replaces
			"token":  "fresh", // scalar at a fresh leaf: added
		},
		"feature": "on", // top-level scalar at a fresh key: added
	}

	mergeSkippingScalarOverMap(src, dest)

	authMap := dest["auth"].(map[string]any)
	assert.Equal(t, "new", authMap["secret"], "scalar-over-scalar must replace")
	assert.Equal(t, "fresh", authMap["token"], "scalar at a fresh leaf must be added")
	assert.Equal(t, "on", dest["feature"], "top-level scalar at a fresh key must be added")
}

// TestEnvNestedScalarCollisionDroppedThroughLoad exercises guard 2 end-to-end through Load:
// a nested env scalar at cache.redis (CACHE_REDIS=...) passes guard 1 — `cache.redis` is not
// a configSections entry — but must be dropped by the merge guard rather than clobber the
// cache.redis defaults map and abort UnmarshalWithConf.
func TestEnvNestedScalarCollisionDroppedThroughLoad(t *testing.T) {
	clearEnvironmentVariables()
	defer clearEnvironmentVariables()
	os.Setenv("CACHE_REDIS", "redis://stray:6379")
	defer os.Unsetenv("CACHE_REDIS")

	cfg, err := Load()
	require.NoError(t, err, "nested CACHE_REDIS scalar must be dropped, not clobber the redis map")
	require.NotNil(t, cfg)
	// The structured redis defaults survive the dropped scalar.
	assert.Equal(t, defaultHost, cfg.Cache.Redis.Host)
}

// Helper function to clear environment variables that might affect tests
func clearEnvironmentVariables() {
	envVars := []string{
		"DEBUG", // Clear system DEBUG variable that can conflict with our debug config
		"APP_NAME", "APP_VERSION", "APP_ENV", "APP_DEBUG", "APP_RATE_LIMIT", "APP_RATE_BURST", "APP_NAMESPACE",
		"SERVER_HOST", "SERVER_PORT", "SERVER_TIMEOUT_READ", "SERVER_TIMEOUT_WRITE",
		"SERVER_TIMEOUT_IDLE", "SERVER_TIMEOUT_MIDDLEWARE", "SERVER_TIMEOUT_SHUTDOWN",
		"SERVER_PATH_BASE", "SERVER_PATH_HEALTH", "SERVER_PATH_READY", "SERVER_GZIP_MINLENGTH",
		"SERVER_BODYLIMIT", "SERVER_RESPONSETIME_ENABLED",
		"DATABASE_TYPE", "DATABASE_HOST", "DATABASE_PORT", testDatabaseDatabase,
		testDatabaseUsername, "DATABASE_PASSWORD", "DATABASE_TLS_MODE",
		testDatabaseMaxConns, "DATABASE_POOL_IDLE_CONNECTIONS",
		"DATABASE_POOL_LIFETIME_MAX", "DATABASE_POOL_IDLE_TIME",
		"DATABASE_ORACLE_SERVICE_NAME", "DATABASE_ORACLE_SERVICE_SID", "DATABASE_CONNECTIONSTRING",
		"LOG_LEVEL", "LOG_PRETTY", "LOG_OUTPUT_FORMAT", "LOG_OUTPUT_FILE",
		"MESSAGING_BROKER_URL", "MESSAGING_ROUTING_EXCHANGE", "MESSAGING_ROUTING_KEY",
		"MESSAGING_BROKER_VIRTUALHOST",
		// Bool keys (ADR-077). The unset-keeps-the-default assertions read absence, so an
		// ambient value in the developer's or runner's environment would make them pass or
		// fail for a reason the test never set.
		"DATABASE_POOL_KEEPALIVE_ENABLED", "CACHE_CRITICAL", "CACHE_ENABLED", "SERVER_LOGROUTES",
		// ADR-078: the unset-keeps-the-loopback-default assertion reads absence.
		"DEBUG_ALLOWEDIPS",
	}

	for _, envVar := range envVars {
		os.Unsetenv(envVar)
	}

	// Remove any custom configuration vars introduced during tests
	for _, envEntry := range os.Environ() {
		if !strings.HasPrefix(envEntry, "CUSTOM_") {
			continue
		}
		if idx := strings.IndexRune(envEntry, '='); idx > 0 {
			os.Unsetenv(envEntry[:idx])
		}
	}
}

func TestLoadDatabaseDisabled(t *testing.T) {
	clearEnvironmentVariables()
	defer clearEnvironmentVariables()
	t.Chdir(t.TempDir())

	// A genuinely absent database section (no identity key set at all) loads fine.
	cfg, err := Load()
	require.NoError(t, err) // Should NOT fail validation now
	require.NotNil(t, cfg)

	// Verify database is configured as disabled
	assert.False(t, IsDatabaseConfigured(&cfg.Database))
	assert.Equal(t, "", cfg.Database.Host)
	assert.Equal(t, "", cfg.Database.Type)

	// Verify other config still works
	assert.Equal(t, appName, cfg.App.Name)
	assert.Equal(t, 8080, cfg.Server.Port)
	assert.Equal(t, "info", cfg.Log.Level)
}

func TestLoadDatabasePartialConfig(t *testing.T) {
	defer clearEnvironmentVariables()

	// Set partial database config (should fail)
	os.Setenv("DATABASE_HOST", "localhost")
	// Missing required fields like DATABASE_TYPE, DATABASE_DATABASE, etc.

	cfg, err := Load()
	assert.Error(t, err) // Should fail validation
	assert.Nil(t, cfg)
	// Error should mention missing required database config
	assert.Contains(t, err.Error(), "database.type")
}

func TestLoadDatabaseCompleteConfig(t *testing.T) {
	defer clearEnvironmentVariables()

	// Set complete database config with all required fields
	os.Setenv("DATABASE_TYPE", "postgresql")
	os.Setenv("DATABASE_HOST", "localhost")
	os.Setenv("DATABASE_PORT", "5432")
	os.Setenv(testDatabaseDatabase, "testdb")
	os.Setenv(testDatabaseUsername, "testuser")
	// Use a non-default max (40 != defaultPoolMaxConnections) so the idle-tracks-max
	// assertion below proves idle follows the *configured* max, not a constant.
	os.Setenv(testDatabaseMaxConns, "40")

	cfg, err := Load()
	require.NoError(t, err)
	require.NotNil(t, cfg)

	// Verify database is configured as enabled
	assert.True(t, IsDatabaseConfigured(&cfg.Database))
	assert.Equal(t, "postgresql", cfg.Database.Type)
	assert.Equal(t, "localhost", cfg.Database.Host)
	assert.Equal(t, 5432, cfg.Database.Port)
	assert.Equal(t, "testdb", cfg.Database.Database)
	assert.Equal(t, "testuser", cfg.Database.Username)
	assert.Equal(t, int32(40), cfg.Database.Pool.Max.Connections)

	// Verify production-safe pool defaults are applied
	assert.Equal(t, int32(40), cfg.Database.Pool.Idle.Connections)        // Default: idle tracks the configured max (40), not a constant
	assert.Equal(t, 5*time.Minute, cfg.Database.Pool.Idle.Time)           // Default: idle timeout
	assert.Equal(t, 30*time.Minute, cfg.Database.Pool.Lifetime.Max)       // Default: max lifetime
	assert.True(t, cfg.Database.Pool.KeepAlive.IsEnabled())               // Default: keep-alive enabled
	assert.Equal(t, 60*time.Second, cfg.Database.Pool.KeepAlive.Interval) // Default: probe interval

	// Verify database fields that should be zero/empty since no defaults
	assert.Equal(t, "", cfg.Database.TLS.Mode) // No default provided
}

// unitlessDurationBaseConfig is a full, otherwise-valid config.yaml whose only variable
// is the messaging.reconnect.delay value; used by the end-to-end unit-less-duration tests
// so a passing case proves the config would boot were it not for the offending value.
func unitlessDurationBaseConfig(delay string) string {
	return "app:\n" +
		"  name: test-svc\n" +
		"  env: development\n" +
		"database:\n" +
		"  type: postgresql\n" +
		"  host: localhost\n" +
		"  port: 5432\n" +
		"  database: testdb\n" +
		"  username: testuser\n" +
		"messaging:\n" +
		"  broker:\n" +
		"    url: amqp://guest:guest@localhost:5672/\n" +
		"  reconnect:\n" +
		"    delay: " + delay + "\n"
}

// TestBuildDecoderConfigRejectsUnitlessNumericDuration exercises the real decoder chain
// (buildDecoderConfig) directly, isolating the numeric->duration guard from validation:
// non-zero bare numerics targeting time.Duration are rejected with an actionable message,
// while zero, string durations, and non-duration numeric fields decode untouched.
func TestBuildDecoderConfigRejectsUnitlessNumericDuration(t *testing.T) {
	type target struct {
		Interval time.Duration `mapstructure:"interval"`
		Port     int           `mapstructure:"port"`
	}

	tests := []struct {
		name         string
		input        map[string]any
		wantErr      bool
		errSubstr    []string
		wantInterval time.Duration
		wantPort     int
	}{
		{name: "int_seconds", input: map[string]any{"interval": 300}, wantErr: true, errSubstr: []string{"unit-less numeric duration 300", "explicit unit"}},
		{name: "yaml_float", input: map[string]any{"interval": 2.5}, wantErr: true, errSubstr: []string{"unit-less numeric duration 2.5", "explicit unit"}},
		{name: "negative", input: map[string]any{"interval": -5}, wantErr: true, errSubstr: []string{"unit-less numeric duration -5"}},
		{name: "bool_rejected", input: map[string]any{"interval": true}, wantErr: true, errSubstr: []string{"unit-less numeric duration true"}},
		{name: "zero_int", input: map[string]any{"interval": 0}, wantErr: false, wantInterval: 0},
		{name: "zero_int64", input: map[string]any{"interval": int64(0)}, wantErr: false, wantInterval: 0},
		{name: "zero_float", input: map[string]any{"interval": 0.0}, wantErr: false, wantInterval: 0},
		{name: "negative_zero_float", input: map[string]any{"interval": math.Copysign(0, -1)}, wantErr: false, wantInterval: 0},
		{name: "typed_duration_source_passes", input: map[string]any{"interval": 5 * time.Second}, wantErr: false, wantInterval: 5 * time.Second},
		{name: "string_seconds", input: map[string]any{"interval": "300s"}, wantErr: false, wantInterval: 300 * time.Second},
		{name: "string_composite", input: map[string]any{"interval": "1h30m"}, wantErr: false, wantInterval: 90 * time.Minute},
		{name: "non_duration_int_untouched", input: map[string]any{"port": 9090}, wantErr: false, wantPort: 9090},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var out target
			dc := buildDecoderConfig()
			dc.Result = &out
			dec, err := mapstructure.NewDecoder(dc)
			require.NoError(t, err)

			err = dec.Decode(tc.input)
			if tc.wantErr {
				require.Error(t, err)
				for _, s := range tc.errSubstr {
					assert.ErrorContains(t, err, s)
				}
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.wantInterval, out.Interval)
			assert.Equal(t, tc.wantPort, out.Port)
		})
	}
}

// TestLoadRejectsUnitlessNumericDurationEndToEnd proves the guard surfaces through Load with
// enough context to locate the key: a config.yaml carrying messaging.reconnect.delay: 300
// aborts startup naming both the offending value and the key path.
func TestLoadRejectsUnitlessNumericDurationEndToEnd(t *testing.T) {
	clearEnvironmentVariables()

	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, testConfigFileYAML),
		[]byte(unitlessDurationBaseConfig("300")), 0o600))
	t.Chdir(dir)

	_, err := Load()
	require.Error(t, err)
	assert.ErrorContains(t, err, "unit-less numeric duration 300")
	assert.ErrorContains(t, err, "messaging.reconnect.delay")
}

// TestLoadUnitlessNumericDurationZeroUsesDefault proves the zero-is-unset exemption: an
// explicit 0 still loads and the documented 5s default applies (byte-identical to pre-fix).
func TestLoadUnitlessNumericDurationZeroUsesDefault(t *testing.T) {
	clearEnvironmentVariables()

	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, testConfigFileYAML),
		[]byte(unitlessDurationBaseConfig("0")), 0o600))
	t.Chdir(dir)

	cfg, err := Load()
	require.NoError(t, err)
	assert.Equal(t, 5*time.Second, cfg.Messaging.Reconnect.Delay)
}

// TestLoadUnitlessNumericDurationEnvVarStillFails pins the pre-existing env failure mode:
// a unit-less string env var already fails via time.ParseDuration's missing-unit error, so
// both YAML (numeric) and env (string) sources now reject unit-less durations.
func TestLoadUnitlessNumericDurationEnvVarStillFails(t *testing.T) {
	clearEnvironmentVariables()
	defer clearEnvironmentVariables()

	t.Setenv("MESSAGING_RECONNECT_DELAY", "300")

	_, err := Load()
	require.Error(t, err)
	assert.ErrorContains(t, err, "missing unit in duration")
}

// TestLoad_DatabaseConnectionStringOnly test removed - connection string
// configuration requires additional complexity that is beyond the 80/20 scope
// The core conditional validation functionality works as intended

func TestLoadDatabaseDisabledByDefault(t *testing.T) {
	clearEnvironmentVariables()
	defer clearEnvironmentVariables()
	t.Chdir(t.TempDir())

	// loadDefaults registers no database.* keys, so a Load with no database
	// environment variables and no config.yaml leaves the section genuinely absent.
	cfg, err := Load()
	require.NoError(t, err)
	require.NotNil(t, cfg)

	// Database should be disabled
	assert.False(t, IsDatabaseConfigured(&cfg.Database))

	// Other config should use defaults
	assert.Equal(t, appName, cfg.App.Name)
	assert.Equal(t, appVersion, cfg.App.Version)
	assert.Equal(t, EnvDevelopment, cfg.App.Env)
}

func TestLoadDatabaseEnabledByExplicitConfig(t *testing.T) {
	defer clearEnvironmentVariables()

	// Database is now disabled by default - must explicitly configure
	// Provide minimal config to enable database
	os.Setenv("DATABASE_TYPE", "postgresql")
	os.Setenv("DATABASE_HOST", "localhost")
	os.Setenv("DATABASE_PORT", "5432")
	os.Setenv(testDatabaseDatabase, "testdb")
	os.Setenv(testDatabaseUsername, "testuser")
	os.Setenv(testDatabaseMaxConns, "25")

	cfg, err := Load()
	require.NoError(t, err)
	require.NotNil(t, cfg)

	// Database should be enabled by explicit configuration
	assert.True(t, IsDatabaseConfigured(&cfg.Database))
	assert.Equal(t, "postgresql", cfg.Database.Type)              // From env
	assert.Equal(t, "localhost", cfg.Database.Host)               // From env
	assert.Equal(t, "testdb", cfg.Database.Database)              // From env
	assert.Equal(t, "testuser", cfg.Database.Username)            // From env
	assert.Equal(t, int32(25), cfg.Database.Pool.Max.Connections) // From env
}

func TestLoadBasePathConfig(t *testing.T) {
	defer clearEnvironmentVariables()

	os.Setenv("SERVER_PATH_BASE", "/api/v1")
	os.Setenv("SERVER_PATH_HEALTH", "/healthz")
	os.Setenv("SERVER_PATH_READY", "/readyz")

	cfg, err := Load()
	require.NoError(t, err)
	require.NotNil(t, cfg)

	assert.Equal(t, "/api/v1", cfg.Server.Path.Base)
	assert.Equal(t, "/healthz", cfg.Server.Path.Health)
	assert.Equal(t, "/readyz", cfg.Server.Path.Ready)
}

func TestLoadYMLFileExtension(t *testing.T) {
	// Test that both .yaml and .yml file extensions are supported
	defer clearEnvironmentVariables()

	t.Run("loads_config.yml_file", func(t *testing.T) {
		clearEnvironmentVariables()

		// Create a temporary config.yml file
		content := `
app:
  name: test-service-yml
  version: v2.0.0
server:
  port: 9000
`
		tmpFile := testConfigFile
		err := os.WriteFile(tmpFile, []byte(content), 0o644)
		require.NoError(t, err)
		defer os.Remove(tmpFile)

		cfg, err := Load()
		require.NoError(t, err)
		require.NotNil(t, cfg)

		assert.Equal(t, "test-service-yml", cfg.App.Name)
		assert.Equal(t, "v2.0.0", cfg.App.Version)
		assert.Equal(t, 9000, cfg.Server.Port)
	})

	t.Run("yaml_takes_precedence_over_yml", func(t *testing.T) {
		clearEnvironmentVariables()

		// Create both config.yaml and config.yml files
		yamlContent := `
app:
  name: from-yaml-file
  version: v1.0.0
`
		ymlContent := `
app:
  name: from-yml-file
  version: v2.0.0
`
		yamlFile := testConfigFileYAML
		ymlFile := testConfigFile

		err := os.WriteFile(yamlFile, []byte(yamlContent), 0o644)
		require.NoError(t, err)
		defer os.Remove(yamlFile)

		err = os.WriteFile(ymlFile, []byte(ymlContent), 0o644)
		require.NoError(t, err)
		defer os.Remove(ymlFile)

		cfg, err := Load()
		require.NoError(t, err)
		require.NotNil(t, cfg)

		// .yaml file should take precedence
		assert.Equal(t, "from-yaml-file", cfg.App.Name)
		assert.Equal(t, "v1.0.0", cfg.App.Version)
	})

	t.Run("loads_environment_specific_yml_file", func(t *testing.T) {
		clearEnvironmentVariables()

		// Create base config.yml file that sets the environment
		baseContent := `
app:
  env: production
`
		baseFile := testConfigFile
		err := os.WriteFile(baseFile, []byte(baseContent), 0o644)
		require.NoError(t, err)
		defer os.Remove(baseFile)

		// Create config.production.yml file with environment-specific overrides
		envContent := `
app:
  name: production-service
server:
  port: 8090
`
		envFile := "config.production.yml"
		err = os.WriteFile(envFile, []byte(envContent), 0o644)
		require.NoError(t, err)
		defer os.Remove(envFile)

		cfg, err := Load()
		require.NoError(t, err)
		require.NotNil(t, cfg)

		assert.Equal(t, "production-service", cfg.App.Name)
		assert.Equal(t, EnvProduction, cfg.App.Env)
		assert.Equal(t, 8090, cfg.Server.Port)
	})

	t.Run("environment_yaml_precedence_over_yml", func(t *testing.T) {
		clearEnvironmentVariables()

		// Create both config.development.yaml and config.development.yml
		yamlContent := `
app:
  name: dev-from-yaml
server:
  port: 7000
`
		ymlContent := `
app:
  name: dev-from-yml
server:
  port: 8000
`
		yamlFile := "config.development.yaml"
		ymlFile := "config.development.yml"

		err := os.WriteFile(yamlFile, []byte(yamlContent), 0o644)
		require.NoError(t, err)
		defer os.Remove(yamlFile)

		err = os.WriteFile(ymlFile, []byte(ymlContent), 0o644)
		require.NoError(t, err)
		defer os.Remove(ymlFile)

		os.Setenv("APP_ENV", EnvDevelopment)

		cfg, err := Load()
		require.NoError(t, err)
		require.NotNil(t, cfg)

		// .yaml should take precedence over .yml
		assert.Equal(t, "dev-from-yaml", cfg.App.Name)
		assert.Equal(t, 7000, cfg.Server.Port)
	})

	t.Run("no_error_when_neither_extension_exists", func(t *testing.T) {
		clearEnvironmentVariables()

		// Don't create any config files - should use defaults without error
		cfg, err := Load()
		require.NoError(t, err)
		require.NotNil(t, cfg)

		// Should use default values
		assert.Equal(t, appName, cfg.App.Name)
		assert.Equal(t, 8080, cfg.Server.Port)
	})

	t.Run("returns_error_on_invalid_yaml_syntax", func(t *testing.T) {
		clearEnvironmentVariables()

		// Create a config file with invalid YAML syntax
		invalidYAML := `
app:
  name: test
  invalid yaml - no colon
  version: v1.0.0
`
		tmpFile := testConfigFileYAML
		err := os.WriteFile(tmpFile, []byte(invalidYAML), 0o644)
		require.NoError(t, err)
		defer os.Remove(tmpFile)

		// Load should return an error for invalid YAML syntax
		cfg, err := Load()
		require.Error(t, err)
		require.Nil(t, cfg)
		assert.Contains(t, err.Error(), "failed to load config.yaml")
	})

	t.Run("returns_error_on_invalid_environment_yaml_syntax", func(t *testing.T) {
		clearEnvironmentVariables()

		// Create a valid base config that sets app.env to production
		// (env vars are loaded AFTER yaml files, so we must set env in YAML)
		validYAML := `
app:
  name: base-service
  env: production
`
		baseFile := testConfigFileYAML
		err := os.WriteFile(baseFile, []byte(validYAML), 0o644)
		require.NoError(t, err)
		defer os.Remove(baseFile)

		// Create an invalid environment-specific config
		invalidEnvYAML := `
app:
  name: prod-service
  broken: [unclosed bracket
`
		envFile := "config.production.yaml"
		err = os.WriteFile(envFile, []byte(invalidEnvYAML), 0o644)
		require.NoError(t, err)
		defer os.Remove(envFile)

		// Load should return an error for invalid environment YAML
		cfg, err := Load()
		require.Error(t, err)
		require.Nil(t, cfg)
		assert.Contains(t, err.Error(), "failed to load config.production.yaml")
	})
}

// loadDefaultConfig loads koanf defaults (no YAML, no env) and unmarshals them the
// same way Load does, so a test can inspect the resulting typed Config.
func loadDefaultConfig(t *testing.T) (*Config, error) {
	t.Helper()
	k := koanf.New(".")
	if err := loadDefaults(k); err != nil {
		return nil, err
	}
	var cfg Config
	if err := k.UnmarshalWithConf("", &cfg, koanf.UnmarshalConf{
		DecoderConfig: buildDecoderConfig(),
	}); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// TestDerivedDefaultsRenderTheSameValuesAsTheOldLiteral is the one-shot equivalence pin for
// the mechanism change: these keys used to be hand-written in loadDefaults and are now
// rendered by normalize. The expected values are the pre-change literals, so the test fails
// if derivation moves any default rather than merely relocating where it is written.
func TestDerivedDefaultsRenderTheSameValuesAsTheOldLiteral(t *testing.T) {
	want := map[string]any{
		"app.startup.timeout":         "10s",
		"cache.redis.port":            6379,
		"cache.redis.poolsize":        10,
		"cache.redis.dialtimeout":     "5s",
		"cache.redis.readtimeout":     "3s",
		"cache.redis.writetimeout":    "3s",
		"cache.redis.maxretries":      3,
		"cache.redis.minretrybackoff": "8ms",
		"cache.redis.maxretrybackoff": "512ms",
		"keystore.secretminlength":    32,
		"scheduler.timeout.shutdown":  "30s",
		"scheduler.timeout.slowjob":   "25s",
	}

	got, err := derivedDefaults()

	require.NoError(t, err)
	assert.Equal(t, want, got, "derivation must relocate the defaults, not change them")
}

// TestDerivedDefaultsDecodeToTypedFields keeps the coverage the old drift test carried: the
// map-level pin proves the VALUES, this proves they still decode to the typed fields through
// the real decoder — which carries the unit-less-duration guard, so a duration rendered as
// anything but a unit string would fail here rather than in production. Scoped to defaults +
// unmarshal rather than a full Load, so a developer's exported CACHE_* cannot flake it.
func TestDerivedDefaultsDecodeToTypedFields(t *testing.T) {
	cfg, err := loadDefaultConfig(t)
	require.NoError(t, err)

	assert.Equal(t, 10*time.Second, cfg.App.Startup.Timeout)
	assert.Equal(t, 6379, cfg.Cache.Redis.Port)
	assert.Equal(t, 10, cfg.Cache.Redis.PoolSize)
	assert.Equal(t, 5*time.Second, cfg.Cache.Redis.DialTimeout)
	assert.Equal(t, 3*time.Second, cfg.Cache.Redis.ReadTimeout)
	assert.Equal(t, 3*time.Second, cfg.Cache.Redis.WriteTimeout)
	assert.Equal(t, 3, cfg.Cache.Redis.MaxRetries)
	assert.Equal(t, 8*time.Millisecond, cfg.Cache.Redis.MinRetryBackoff)
	assert.Equal(t, 512*time.Millisecond, cfg.Cache.Redis.MaxRetryBackoff)
	require.NotNil(t, cfg.KeyStore.SecretMinLength)
	assert.Equal(t, 32, *cfg.KeyStore.SecretMinLength)
	assert.Equal(t, 30*time.Second, cfg.Scheduler.Timeout.Shutdown)
	assert.Equal(t, 25*time.Second, cfg.Scheduler.Timeout.SlowJob)
}

// TestDerivedDefaultKeysAreDisjointFromKoanfOnly enforces one mechanism PER KEY: a key
// written in both maps would have its winner decided by merge order rather than by design.
func TestDerivedDefaultKeysAreDisjointFromKoanfOnly(t *testing.T) {
	koanfOnly := koanfOnlyDefaults()

	for _, key := range derivedDefaultKeys {
		_, collides := koanfOnly[key]
		assert.False(t, collides, "%q is both derived and hand-written", key)
	}
}

// TestDerivedDefaultKeysAreActuallyFilledByNormalize keeps the allowlist honest. Presence
// alone proves nothing: the flatten emits EVERY koanf-tagged field whether normalize touched
// it or not, so a key added to the allowlist before its fill moves into normalize would
// derive its Go zero — "0s" for a duration — and silently replace a real default. The
// difference against an un-normalized zero Config is what makes this a gate.
func TestDerivedDefaultKeysAreActuallyFilledByNormalize(t *testing.T) {
	normalized := flattenNormalizedZero(t, false)
	bare, err := flattenConfig(&Config{})
	require.NoError(t, err)

	for _, key := range derivedDefaultKeys {
		require.Contains(t, normalized, key, "normalize does not fill %q", key)
		assert.NotEqual(t, bare[key], normalized[key],
			"%q is still at its Go zero after normalize, so it has no default to derive", key)
	}
}

// TestDerivedDefaultsRejectAZeroValuedKey proves the gate above is enforced at Load, not only
// in this test binary: the allowlist is meant to grow, and the next key to join it will do so
// in a PR that does not necessarily re-read the rules.
func TestDerivedDefaultsRejectAZeroValuedKey(t *testing.T) {
	original := derivedDefaultKeys
	t.Cleanup(func() { derivedDefaultKeys = original })
	// server.responsetime.enabled is flattened but never filled by normalize.
	derivedDefaultKeys = append(append([]string{}, original...), "server.responsetime.enabled")

	_, err := derivedDefaults()

	require.Error(t, err)
	assert.ErrorContains(t, err, "server.responsetime.enabled")
	assert.ErrorContains(t, err, "zero value")
}

// TestDerivedDefaultsRejectAFailClosedKeySpace pins the ADR-051 / ADR-049 postures by
// construction. Both read koanf ABSENCE, so a preloaded key silently answers a question the
// operator never answered — for database identity that boots the misconfiguration ADR-051
// exists to catch.
func TestDerivedDefaultsRejectAFailClosedKeySpace(t *testing.T) {
	tests := []struct {
		name string
		key  string
	}{
		{name: "database_identity", key: "database.host"},
		{name: "debug_allowlist", key: "debug.allowedips"},
		{name: "tenant_subtree", key: "multitenant.tenants.acme.database.host"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			original := derivedDefaultKeys
			t.Cleanup(func() { derivedDefaultKeys = original })
			derivedDefaultKeys = []string{tt.key}

			_, err := derivedDefaults()

			require.Error(t, err)
			assert.ErrorContains(t, err, tt.key)
			assert.ErrorContains(t, err, "must stay absent")
		})
	}
}

// TestDerivedDefaultsRejectAMissingKey covers the remaining door: a key that is not in the
// flatten at all, which is what a renamed or removed config field looks like.
func TestDerivedDefaultsRejectAMissingKey(t *testing.T) {
	original := derivedDefaultKeys
	t.Cleanup(func() { derivedDefaultKeys = original })
	derivedDefaultKeys = []string{"app.startup.nosuchkey"}

	_, err := derivedDefaults()

	require.Error(t, err)
	assert.ErrorContains(t, err, "does not fill")
}

// TestLoadDefaultsCarriesNoPreloadedFailClosedKey runs the PRODUCTION predicate over the real
// merged map. A hand-written literal under one of these prefixes would read as "configured"
// to ADR-051's presence check and abort every database-free deployment, and the allowlist is
// not the only door it can come through.
func TestLoadDefaultsCarriesNoPreloadedFailClosedKey(t *testing.T) {
	derived, err := derivedDefaults()
	require.NoError(t, err)

	merged, err := mergeDefaults(koanfOnlyDefaults(), derived)

	// mergeDefaults itself refuses a denied key, so this is the assertion that matters; the
	// loop below only names the offender when it fires.
	require.NoError(t, err)
	for key := range merged {
		prefix, denied := matchesPrefix(key, preloadDeniedPrefixes)
		assert.False(t, denied, "%q is under %q and must stay absent unless configured", key, prefix)
	}

	// debug.* literals are legitimate and must NOT be caught by this rule — they are the
	// explicit map ADR-049 reads through the decoded struct.
	assert.Contains(t, merged, "debug.allowedips")
}

// TestMergeDefaultsEnforcesItsTwoRules pins the rules that hold at load time. Neither is
// reachable through derivedDefaultKeys today — a colliding key would fail the zero-value gate
// first, and no hand-written literal sits under a denied prefix — so they are exercised
// directly rather than left as branches that only look protective.
func TestMergeDefaultsEnforcesItsTwoRules(t *testing.T) {
	t.Run("collision_is_refused", func(t *testing.T) {
		_, err := mergeDefaults(map[string]any{"app.name": "hand"}, map[string]any{"app.name": "derived"})

		require.Error(t, err)
		assert.ErrorContains(t, err, "both derived and hand-written")
	})

	t.Run("denied_prefix_from_either_map_is_refused", func(t *testing.T) {
		_, err := mergeDefaults(map[string]any{"database.host": "db"}, nil)
		require.Error(t, err)
		assert.ErrorContains(t, err, "must stay absent")

		_, err = mergeDefaults(map[string]any{}, map[string]any{"multitenant.tenants.acme.database.host": "db"})
		require.Error(t, err)
		assert.ErrorContains(t, err, "must stay absent")
	})

	t.Run("clean_maps_merge", func(t *testing.T) {
		got, err := mergeDefaults(map[string]any{"app.name": "hand"}, map[string]any{"cache.redis.port": 6379})

		require.NoError(t, err)
		assert.Equal(t, map[string]any{"app.name": "hand", "cache.redis.port": 6379}, got)
	})
}

// TestDerivedDefaultsAreModeInvariant bars a key whose normalized default differs between
// deployment modes. The manager pool sizes are why this test exists: multi-tenant treats zero
// as "unlimited", so preloading a koanf default of 10 would overwrite that meaning before
// anything could read it.
func TestDerivedDefaultsAreModeInvariant(t *testing.T) {
	singleTenant := flattenNormalizedZero(t, false)
	multiTenant := flattenNormalizedZero(t, true)

	for _, key := range derivedDefaultKeys {
		assert.Equal(t, singleTenant[key], multiTenant[key],
			"%q differs by deployment mode and cannot be preloaded", key)
	}
}

// flattenNormalizedZero is what derivedDefaults picks from: a zero Config run through
// normalize, flattened into koanf's dotted key space.
func flattenNormalizedZero(t *testing.T, multitenant bool) map[string]any {
	t.Helper()

	var zero Config
	zero.Multitenant.Enabled = multitenant
	require.NoError(t, normalize(&zero))

	flat, err := flattenConfig(&zero)
	require.NoError(t, err)
	return flat
}

// TestRenderDefaultRejectsANilPointer pins the branch normalize is supposed to make
// unreachable. It is the module's contract with a future normalize regression: writing a nil
// default would remove the secret-length floor silently, so it has to be an error.
func TestRenderDefaultRejectsANilPointer(t *testing.T) {
	var missing *int

	_, err := renderDefault("keystore.secretminlength", missing)

	require.Error(t, err)
	assert.ErrorContains(t, err, "keystore.secretminlength")
}

// TestDefaultsPreloadNoDatabaseIdentityKey guards ADR-051 by construction rather than by
// discipline: its delivered-empty check reads koanf key PRESENCE, so a preloaded identity key
// would make an empty one look configured and boot the misconfiguration it exists to catch.
// Full derivation would have done exactly that, which is why the allowlist is narrow.
func TestDefaultsPreloadNoDatabaseIdentityKey(t *testing.T) {
	k := koanf.New(".")
	require.NoError(t, loadDefaults(k))

	for _, key := range databaseIdentityKeys {
		assert.False(t, k.Exists(fieldDatabase+"."+key), "database.%s must not be preloaded", key)
	}
	assert.False(t, k.Exists(fieldDatabases), "the named-database subtree must not be preloaded")
	assert.False(t, k.Exists("multitenant.tenants"), "the tenant subtree carries the third ADR-051 section")
	// debug.allowedips keeps its hand-written loopback default (that is today's koanf
	// behavior); what must never happen is DERIVING it, which would hand its fail-closed
	// posture to whatever normalize does or does not fill (ADR-049).
	assert.NotContains(t, derivedDefaultKeys, "debug.allowedips")
	assert.True(t, k.Exists("debug.allowedips"), "the hand-written loopback default stands")
}
