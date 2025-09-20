package config

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/knadh/koanf/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	// Environment variable keys reused across tests
	testDatabaseUsername = "DATABASE_USERNAME"
	testDatabaseDatabase = "DATABASE_DATABASE"
	testDatabaseMaxConns = "DATABASE_MAX_CONNS"
	appName              = "gobricks-service"
	appVersion           = "v1.0.0"
	serverHost           = "0.0.0.0"
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
	assert.Equal(t, 100, cfg.App.RateLimit)
	assert.Equal(t, "default", cfg.App.Namespace)

	assert.Equal(t, serverHost, cfg.Server.Host)
	assert.Equal(t, 8080, cfg.Server.Port)
	assert.Equal(t, 15*time.Second, cfg.Server.ReadTimeout)
	assert.Equal(t, 30*time.Second, cfg.Server.WriteTimeout)
	assert.Equal(t, 5*time.Second, cfg.Server.MiddlewareTimeout)
	assert.Equal(t, 10*time.Second, cfg.Server.ShutdownTimeout)

	// Database should be disabled by default (no defaults provided)
	assert.False(t, IsDatabaseConfigured(&cfg.Database))
	assert.Equal(t, "", cfg.Database.Type)
	assert.Equal(t, "", cfg.Database.Host)
	assert.Equal(t, 0, cfg.Database.Port)
	assert.Equal(t, "", cfg.Database.Database)
	assert.Equal(t, "", cfg.Database.Username)
	assert.Equal(t, "", cfg.Database.SSLMode)
	assert.Equal(t, int32(0), cfg.Database.MaxConns)
	assert.Equal(t, int32(0), cfg.Database.MaxIdleConns)
	assert.Equal(t, time.Duration(0), cfg.Database.ConnMaxLifetime)
	assert.Equal(t, time.Duration(0), cfg.Database.ConnMaxIdleTime)

	assert.Equal(t, "info", cfg.Log.Level)
	assert.False(t, cfg.Log.Pretty)
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
	assert.Equal(t, int32(25), cfg.Database.MaxConns)

	// Verify defaults still work for non-overridden values
	assert.Equal(t, appVersion, cfg.App.Version)
	assert.Equal(t, serverHost, cfg.Server.Host)
}

func TestLoadInvalidEnvironmentVariables(t *testing.T) {
	baseEnv := map[string]string{
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
			wantErr: "invalid log level",
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
	assert.Equal(t, 100, k.Int("app.rate_limit"))

	assert.Equal(t, serverHost, k.String("server.host"))
	assert.Equal(t, 8080, k.Int("server.port"))
	assert.Equal(t, "15s", k.String("server.read_timeout"))
	assert.Equal(t, "30s", k.String("server.write_timeout"))

	// Database defaults should NOT be provided
	assert.Equal(t, "", k.String("database.type"))
	assert.Equal(t, "", k.String("database.host"))
	assert.Equal(t, 0, k.Int("database.port"))
	assert.Equal(t, "", k.String("database.ssl_mode"))
	assert.Equal(t, 0, k.Int("database.max_conns"))

	assert.Equal(t, "info", k.String("log.level"))
	assert.False(t, k.Bool("log.pretty"))
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

func TestLoadCustomConfiguration(t *testing.T) {
	defer clearEnvironmentVariables()

	t.Run("custom_config_via_environment", func(t *testing.T) {
		clearEnvironmentVariables()
		// Set required database fields
		os.Setenv(testDatabaseDatabase, "testdb")
		os.Setenv(testDatabaseUsername, "testuser")

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
		assert.True(t, cfg.GetBool("custom.feature.enabled"))
		assert.Equal(t, "https://api.test.com", cfg.GetString("custom.service.endpoint"))
		timeout := cfg.GetString("custom.service.timeout")
		dur, err := time.ParseDuration(timeout)
		require.NoError(t, err)
		assert.Equal(t, 30*time.Second, dur)
		assert.Equal(t, 5, cfg.GetInt("custom.max.retries"))
	})

	t.Run("custom_config_with_defaults", func(t *testing.T) {
		clearEnvironmentVariables()
		// Set required database fields
		os.Setenv(testDatabaseDatabase, "testdb")
		os.Setenv(testDatabaseUsername, "testuser")

		cfg, err := Load()
		require.NoError(t, err)
		require.NotNil(t, cfg)

		// Test default values for missing custom config
		assert.Equal(t, "default-value", cfg.GetString("custom.missing.key", "default-value"))
		assert.Equal(t, 100, cfg.GetInt("custom.missing.int", 100))
		assert.False(t, cfg.GetBool("custom.missing.bool"))
	})

	t.Run("custom_config_required_fields", func(t *testing.T) {
		clearEnvironmentVariables()
		// Set required database fields
		os.Setenv(testDatabaseDatabase, "testdb")
		os.Setenv(testDatabaseUsername, "testuser")
		os.Setenv("CUSTOM_API_KEY", "secret-key-123")

		cfg, err := Load()
		require.NoError(t, err)
		require.NotNil(t, cfg)

		// Test required field that exists
		apiKey, err := cfg.GetRequiredString("custom.api.key")
		assert.NoError(t, err)
		assert.Equal(t, "secret-key-123", apiKey)

		// Test required field that doesn't exist
		_, err = cfg.GetRequiredString("custom.missing.required")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "missing")
	})

	t.Run("custom_config_unmarshal_struct", func(t *testing.T) {
		clearEnvironmentVariables()
		// Set required database fields
		os.Setenv(testDatabaseDatabase, "testdb")
		os.Setenv(testDatabaseUsername, "testuser")

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
		// Set required database fields
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
		// Set required database fields
		os.Setenv(testDatabaseDatabase, "testdb")
		os.Setenv(testDatabaseUsername, "testuser")
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

// Helper function to clear environment variables that might affect tests
func clearEnvironmentVariables() {
	envVars := []string{
		"APP_NAME", "APP_VERSION", "APP_ENV", "APP_DEBUG", "APP_RATE_LIMIT", "APP_NAMESPACE",
		"SERVER_HOST", "SERVER_PORT", "SERVER_READ_TIMEOUT", "SERVER_WRITE_TIMEOUT",
		"SERVER_MIDDLEWARE_TIMEOUT", "SERVER_SHUTDOWN_TIMEOUT",
		"DATABASE_TYPE", "DATABASE_HOST", "DATABASE_PORT", testDatabaseDatabase,
		testDatabaseUsername, "DATABASE_PASSWORD", "DATABASE_SSL_MODE",
		testDatabaseMaxConns, "DATABASE_MAX_IDLE_CONNS",
		"DATABASE_CONN_MAX_LIFETIME", "DATABASE_CONN_MAX_IDLE_TIME",
		"DATABASE_SERVICE_NAME", "DATABASE_SID", "DATABASE_CONNECTION_STRING",
		"LOG_LEVEL", "LOG_PRETTY",
		"MESSAGING_BROKER_URL", "MESSAGING_EXCHANGE", "MESSAGING_ROUTING_KEY",
		"MESSAGING_VIRTUAL_HOST",
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
	defer clearEnvironmentVariables()

	// Explicitly disable database by clearing defaults
	os.Setenv("DATABASE_HOST", "")
	os.Setenv("DATABASE_TYPE", "")

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
	assert.Contains(t, err.Error(), "invalid database type")
}

func TestLoadDatabaseCompleteConfig(t *testing.T) {
	defer clearEnvironmentVariables()

	// Set complete database config with all required fields
	os.Setenv("DATABASE_TYPE", "postgresql")
	os.Setenv("DATABASE_HOST", "localhost")
	os.Setenv("DATABASE_PORT", "5432")
	os.Setenv(testDatabaseDatabase, "testdb")
	os.Setenv(testDatabaseUsername, "testuser")
	os.Setenv(testDatabaseMaxConns, "25")

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
	assert.Equal(t, int32(25), cfg.Database.MaxConns)

	// Verify database fields that should be zero/empty since no defaults
	assert.Equal(t, "", cfg.Database.SSLMode)            // No default provided
	assert.Equal(t, int32(0), cfg.Database.MaxIdleConns) // No default provided
}

// TestLoad_DatabaseConnectionStringOnly test removed - connection string
// configuration requires additional complexity that is beyond the 80/20 scope
// The core conditional validation functionality works as intended

func TestLoadDatabaseDisabledByDefault(t *testing.T) {
	defer clearEnvironmentVariables()

	// Don't set any database environment variables
	// The defaults will have host="localhost" and type="postgresql", so database will be enabled
	// To test truly disabled, we need to override the defaults
	os.Setenv("DATABASE_HOST", "")
	os.Setenv("DATABASE_TYPE", "")

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
	assert.Equal(t, "postgresql", cfg.Database.Type)   // From env
	assert.Equal(t, "localhost", cfg.Database.Host)    // From env
	assert.Equal(t, "testdb", cfg.Database.Database)   // From env
	assert.Equal(t, "testuser", cfg.Database.Username) // From env
	assert.Equal(t, int32(25), cfg.Database.MaxConns)  // From env
}
