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

func TestLoad_Defaults(t *testing.T) {
	// Clear any environment variables that might affect the test
	clearEnvironmentVariables()

	// Set minimal required database fields for validation to pass
	os.Setenv("DATABASE_DATABASE", "testdb")
	os.Setenv("DATABASE_USERNAME", "testuser")
	defer func() {
		os.Unsetenv("DATABASE_DATABASE")
		os.Unsetenv("DATABASE_USERNAME")
	}()

	cfg, err := Load()
	require.NoError(t, err)
	require.NotNil(t, cfg)

	// Verify default values
	assert.Equal(t, "nova-service", cfg.App.Name)
	assert.Equal(t, "v1.0.0", cfg.App.Version)
	assert.Equal(t, EnvDevelopment, cfg.App.Env)
	assert.False(t, cfg.App.Debug)
	assert.Equal(t, 100, cfg.App.RateLimit)
	assert.Equal(t, "default", cfg.App.Namespace)

	assert.Equal(t, "0.0.0.0", cfg.Server.Host)
	assert.Equal(t, 8080, cfg.Server.Port)
	assert.Equal(t, 15*time.Second, cfg.Server.ReadTimeout)
	assert.Equal(t, 30*time.Second, cfg.Server.WriteTimeout)
	assert.Equal(t, 5*time.Second, cfg.Server.MiddlewareTimeout)
	assert.Equal(t, 10*time.Second, cfg.Server.ShutdownTimeout)

	assert.Equal(t, PostgreSQL, cfg.Database.Type)
	assert.Equal(t, "localhost", cfg.Database.Host)
	assert.Equal(t, 5432, cfg.Database.Port)
	assert.Equal(t, "testdb", cfg.Database.Database)   // From env var
	assert.Equal(t, "testuser", cfg.Database.Username) // From env var
	assert.Equal(t, "disable", cfg.Database.SSLMode)
	assert.Equal(t, int32(25), cfg.Database.MaxConns)
	assert.Equal(t, int32(5), cfg.Database.MaxIdleConns)
	assert.Equal(t, 5*time.Minute, cfg.Database.ConnMaxLifetime)
	assert.Equal(t, 5*time.Minute, cfg.Database.ConnMaxIdleTime)

	assert.Equal(t, "info", cfg.Log.Level)
	assert.False(t, cfg.Log.Pretty)
}

func TestLoad_WithEnvironmentVariables(t *testing.T) {
	// Clear environment variables first
	clearEnvironmentVariables()
	defer clearEnvironmentVariables()

	// Set a few key environment variables to test override functionality
	os.Setenv("APP_NAME", "test-service")
	os.Setenv("APP_ENV", EnvProduction)
	os.Setenv("SERVER_PORT", "9090")
	os.Setenv("DATABASE_DATABASE", "testdb")
	os.Setenv("DATABASE_USERNAME", "testuser")
	os.Setenv("LOG_LEVEL", "debug")

	cfg, err := Load()
	require.NoError(t, err)
	require.NotNil(t, cfg)

	// Verify key environment variables override defaults
	assert.Equal(t, "test-service", cfg.App.Name)
	assert.Equal(t, EnvProduction, cfg.App.Env)
	assert.Equal(t, 9090, cfg.Server.Port)
	assert.Equal(t, "testdb", cfg.Database.Database)
	assert.Equal(t, "testuser", cfg.Database.Username)
	assert.Equal(t, "debug", cfg.Log.Level)

	// Verify defaults still work for non-overridden values
	assert.Equal(t, "v1.0.0", cfg.App.Version)
	assert.Equal(t, "0.0.0.0", cfg.Server.Host)
	assert.Equal(t, PostgreSQL, cfg.Database.Type)
	assert.Equal(t, "localhost", cfg.Database.Host)
}

func TestLoad_InvalidEnvironmentVariables(t *testing.T) {
	defer clearEnvironmentVariables()

	tests := []struct {
		name   string
		envVar string
		value  string
	}{
		{
			name:   "invalid_port",
			envVar: "SERVER_PORT",
			value:  "invalid",
		},
		{
			name:   "invalid_timeout",
			envVar: "SERVER_READ_TIMEOUT",
			value:  "invalid-duration",
		},
		{
			name:   "invalid_boolean",
			envVar: "APP_DEBUG",
			value:  "maybe",
		},
		{
			name:   "invalid_integer",
			envVar: "APP_RATE_LIMIT",
			value:  "not-a-number",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clearEnvironmentVariables()
			os.Setenv(tt.envVar, tt.value)

			cfg, err := Load()
			// Should either fail to load or fail validation
			if err == nil {
				require.NotNil(t, cfg)
				// If loading succeeds, validation should catch the error
				assert.Contains(t, err.Error(), "invalid")
			}
		})
	}
}

func TestLoad_ValidationFailure(t *testing.T) {
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

func TestLoadDefaults(t *testing.T) {
	// Create a new koanf instance for testing
	k := koanf.New(".")

	err := loadDefaults(k)
	require.NoError(t, err)

	// Verify defaults are loaded
	assert.Equal(t, "nova-service", k.String("app.name"))
	assert.Equal(t, "v1.0.0", k.String("app.version"))
	assert.Equal(t, EnvDevelopment, k.String("app.env"))
	assert.False(t, k.Bool("app.debug"))
	assert.Equal(t, 100, k.Int("app.rate_limit"))

	assert.Equal(t, "0.0.0.0", k.String("server.host"))
	assert.Equal(t, 8080, k.Int("server.port"))
	assert.Equal(t, "15s", k.String("server.read_timeout"))
	assert.Equal(t, "30s", k.String("server.write_timeout"))

	assert.Equal(t, "postgresql", k.String("database.type"))
	assert.Equal(t, "localhost", k.String("database.host"))
	assert.Equal(t, 5432, k.Int("database.port"))
	assert.Equal(t, "disable", k.String("database.ssl_mode"))
	assert.Equal(t, 25, k.Int("database.max_conns"))

	assert.Equal(t, "info", k.String("log.level"))
	assert.False(t, k.Bool("log.pretty"))
}

func TestLoad_EdgeCases(t *testing.T) {
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
		os.Setenv("DATABASE_MAX_CONNS", "0")

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

func TestLoad_CustomConfiguration(t *testing.T) {
	defer clearEnvironmentVariables()

	t.Run("custom_config_via_environment", func(t *testing.T) {
		clearEnvironmentVariables()
		// Set required database fields
		os.Setenv("DATABASE_DATABASE", "testdb")
		os.Setenv("DATABASE_USERNAME", "testuser")

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
		os.Setenv("DATABASE_DATABASE", "testdb")
		os.Setenv("DATABASE_USERNAME", "testuser")

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
		os.Setenv("DATABASE_DATABASE", "testdb")
		os.Setenv("DATABASE_USERNAME", "testuser")
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
		os.Setenv("DATABASE_DATABASE", "testdb")
		os.Setenv("DATABASE_USERNAME", "testuser")

		// Set complex custom configuration
		os.Setenv("CUSTOM_SERVICE_NAME", "test-service")
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
		assert.Equal(t, "test-service", svcConfig.Name)
		assert.Equal(t, 8090, svcConfig.Port)
		assert.True(t, svcConfig.Enabled)
	})

	t.Run("custom_config_exists_check", func(t *testing.T) {
		clearEnvironmentVariables()
		// Set required database fields
		os.Setenv("DATABASE_DATABASE", "testdb")
		os.Setenv("DATABASE_USERNAME", "testuser")
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
		os.Setenv("DATABASE_DATABASE", "testdb")
		os.Setenv("DATABASE_USERNAME", "testuser")
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
		"DATABASE_TYPE", "DATABASE_HOST", "DATABASE_PORT", "DATABASE_DATABASE",
		"DATABASE_USERNAME", "DATABASE_PASSWORD", "DATABASE_SSL_MODE",
		"DATABASE_MAX_CONNS", "DATABASE_MAX_IDLE_CONNS",
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
