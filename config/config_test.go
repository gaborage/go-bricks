package config

import (
	"os"
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
	assert.Equal(t, "development", cfg.App.Env)
	assert.False(t, cfg.App.Debug)
	assert.Equal(t, 100, cfg.App.RateLimit)
	assert.Equal(t, "default", cfg.App.Namespace)

	assert.Equal(t, "0.0.0.0", cfg.Server.Host)
	assert.Equal(t, 8080, cfg.Server.Port)
	assert.Equal(t, 15*time.Second, cfg.Server.ReadTimeout)
	assert.Equal(t, 30*time.Second, cfg.Server.WriteTimeout)
	assert.Equal(t, 5*time.Second, cfg.Server.MiddlewareTimeout)
	assert.Equal(t, 10*time.Second, cfg.Server.ShutdownTimeout)

	assert.Equal(t, "postgresql", cfg.Database.Type)
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
	os.Setenv("APP_ENV", "production")
	os.Setenv("SERVER_PORT", "9090")
	os.Setenv("DATABASE_DATABASE", "testdb")
	os.Setenv("DATABASE_USERNAME", "testuser")
	os.Setenv("LOG_LEVEL", "debug")

	cfg, err := Load()
	require.NoError(t, err)
	require.NotNil(t, cfg)

	// Verify key environment variables override defaults
	assert.Equal(t, "test-service", cfg.App.Name)
	assert.Equal(t, "production", cfg.App.Env)
	assert.Equal(t, 9090, cfg.Server.Port)
	assert.Equal(t, "testdb", cfg.Database.Database)
	assert.Equal(t, "testuser", cfg.Database.Username)
	assert.Equal(t, "debug", cfg.Log.Level)

	// Verify defaults still work for non-overridden values
	assert.Equal(t, "v1.0.0", cfg.App.Version)
	assert.Equal(t, "0.0.0.0", cfg.Server.Host)
	assert.Equal(t, "postgresql", cfg.Database.Type)
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
	assert.Equal(t, "development", k.String("app.env"))
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
}
