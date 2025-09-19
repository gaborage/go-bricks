package config

import (
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testAPIKey = "test-key"

// Test structures for config injection

type BasicServiceConfig struct {
	APIKey   string        `config:"api.key" required:"true"`
	BaseURL  string        `config:"api.base.url" default:"https://api.example.com"`
	Timeout  time.Duration `config:"api.timeout" default:"30s"`
	MaxRetry int           `config:"api.max.retry" default:"3"`
	Enabled  bool          `config:"api.enabled" default:"true"`
}

type ComplexServiceConfig struct {
	// Required fields
	DatabaseURL string `config:"database.url" required:"true"`
	SecretKey   string `config:"auth.secret" required:"true"`

	// Optional with defaults (using different paths than built-in defaults)
	Host         string        `config:"custom.server.host" default:"localhost"`
	Port         int           `config:"custom.server.port" default:"8080"`
	ReadTimeout  time.Duration `config:"custom.server.read.timeout" default:"15s"`
	WriteTimeout time.Duration `config:"custom.server.write.timeout" default:"30s"`
	DebugMode    bool          `config:"custom.debug.enabled" default:"false"`
	WorkerCount  int64         `config:"custom.workers.count" default:"4"`
	MaxMemory    float64       `config:"custom.limits.max.memory" default:"512.0"`

	// No default values
	OptionalFeature string `config:"custom.features.optional"`
	OptionalFlag    bool   `config:"custom.features.flag"`
}

type CustomAPIServiceConfig struct {
	APIKey     string        `config:"custom.api.key" required:"true"`
	BaseURL    string        `config:"custom.api.base.url" default:"https://api.example.com"`
	Timeout    time.Duration `config:"custom.api.timeout" default:"30s"`
	MaxRetries int           `config:"custom.api.max.retries" default:"3"`
	EnableAuth bool          `config:"custom.api.enable.auth" default:"true"`
	RateLimit  int           `config:"custom.api.rate.limit" default:"100"`
	UserAgent  string        `config:"custom.api.user.agent" default:"GoBricks/1.0"`
}

type InvalidConfig struct {
	UnsupportedField chan int `config:"test.channel"`
}

type NoConfigTagsStruct struct {
	Field1 string
	Field2 int
}

func TestConfigInjectionBasicTypes(t *testing.T) {
	// Set up environment variables
	envVars := map[string]string{
		"API_KEY":       "secret-key-123",
		"API_BASE_URL":  "https://production.api.com",
		"API_TIMEOUT":   "45s",
		"API_MAX_RETRY": "5",
		"API_ENABLED":   "false",
	}

	for key, value := range envVars {
		require.NoError(t, os.Setenv(key, value))
	}
	defer func() {
		for key := range envVars {
			os.Unsetenv(key)
		}
	}()

	// Load config
	cfg, err := Load()
	require.NoError(t, err)

	// Test injection
	var serviceConfig BasicServiceConfig
	err = cfg.InjectInto(&serviceConfig)
	require.NoError(t, err)

	// Verify injected values
	assert.Equal(t, "secret-key-123", serviceConfig.APIKey)
	assert.Equal(t, "https://production.api.com", serviceConfig.BaseURL)
	assert.Equal(t, 45*time.Second, serviceConfig.Timeout)
	assert.Equal(t, 5, serviceConfig.MaxRetry)
	assert.False(t, serviceConfig.Enabled)
}

func TestConfigInjectionDefaultValues(t *testing.T) {
	// Load config without setting environment variables
	cfg, err := Load()
	require.NoError(t, err)

	var serviceConfig BasicServiceConfig
	err = cfg.InjectInto(&serviceConfig)

	// Should fail due to required field
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "required configuration key 'api.key' is missing")

	// Test with only required field set
	require.NoError(t, os.Setenv("API_KEY", testAPIKey))
	defer os.Unsetenv("API_KEY")

	cfg, err = Load()
	require.NoError(t, err)

	err = cfg.InjectInto(&serviceConfig)
	require.NoError(t, err)

	// Verify default values are used
	assert.Equal(t, testAPIKey, serviceConfig.APIKey)
	assert.Equal(t, "https://api.example.com", serviceConfig.BaseURL)
	assert.Equal(t, 30*time.Second, serviceConfig.Timeout)
	assert.Equal(t, 3, serviceConfig.MaxRetry)
	assert.True(t, serviceConfig.Enabled)
}

func TestConfigInjectionComplexTypes(t *testing.T) {
	envVars := map[string]string{
		"DATABASE_URL":                "postgres://valid-dns",
		"AUTH_SECRET":                 "super-secret-key",
		"CUSTOM_SERVER_HOST":          "0.0.0.0",
		"CUSTOM_SERVER_PORT":          "9090",
		"CUSTOM_SERVER_READ_TIMEOUT":  "20s",
		"CUSTOM_SERVER_WRITE_TIMEOUT": "60s",
		"CUSTOM_DEBUG_ENABLED":        "true",
		"CUSTOM_WORKERS_COUNT":        "8",
		"CUSTOM_LIMITS_MAX_MEMORY":    "1024.5",
		"CUSTOM_FEATURES_OPTIONAL":    "premium",
		"CUSTOM_FEATURES_FLAG":        "true",
	}

	for key, value := range envVars {
		require.NoError(t, os.Setenv(key, value))
	}
	defer func() {
		for key := range envVars {
			os.Unsetenv(key)
		}
	}()

	cfg, err := Load()
	require.NoError(t, err)

	var serviceConfig ComplexServiceConfig
	err = cfg.InjectInto(&serviceConfig)
	require.NoError(t, err)

	// Verify all values
	assert.Equal(t, "postgres://valid-dns", serviceConfig.DatabaseURL)
	assert.Equal(t, "super-secret-key", serviceConfig.SecretKey)
	assert.Equal(t, "0.0.0.0", serviceConfig.Host)
	assert.Equal(t, 9090, serviceConfig.Port)
	assert.Equal(t, 20*time.Second, serviceConfig.ReadTimeout)
	assert.Equal(t, 60*time.Second, serviceConfig.WriteTimeout)
	assert.True(t, serviceConfig.DebugMode)
	assert.Equal(t, int64(8), serviceConfig.WorkerCount)
	assert.Equal(t, 1024.5, serviceConfig.MaxMemory)
	assert.Equal(t, "premium", serviceConfig.OptionalFeature)
	assert.True(t, serviceConfig.OptionalFlag)
}

func TestConfigInjectionCustomNamespace(t *testing.T) {
	envVars := map[string]string{
		"CUSTOM_API_KEY":         "sk_live_abc123",
		"CUSTOM_API_BASE_URL":    "https://custom.example.com",
		"CUSTOM_API_TIMEOUT":     "42s",
		"CUSTOM_API_MAX_RETRIES": "7",
		"CUSTOM_API_ENABLE_AUTH": "false",
		"CUSTOM_API_RATE_LIMIT":  "250",
		"CUSTOM_API_USER_AGENT":  "GoBricks/2.0",
	}

	for key, value := range envVars {
		require.NoError(t, os.Setenv(key, value))
	}
	defer func() {
		for key := range envVars {
			os.Unsetenv(key)
		}
	}()

	cfg, err := Load()
	require.NoError(t, err)

	var serviceConfig CustomAPIServiceConfig
	require.NoError(t, cfg.InjectInto(&serviceConfig))

	assert.Equal(t, "sk_live_abc123", serviceConfig.APIKey)
	assert.Equal(t, "https://custom.example.com", serviceConfig.BaseURL)
	assert.Equal(t, 42*time.Second, serviceConfig.Timeout)
	assert.Equal(t, 7, serviceConfig.MaxRetries)
	assert.False(t, serviceConfig.EnableAuth)
	assert.Equal(t, 250, serviceConfig.RateLimit)
	assert.Equal(t, "GoBricks/2.0", serviceConfig.UserAgent)
}

func TestConfigInjectionRequiredFieldMissing(t *testing.T) {
	cfg, err := Load()
	require.NoError(t, err)

	var serviceConfig BasicServiceConfig
	err = cfg.InjectInto(&serviceConfig)

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "required configuration key 'api.key' is missing")
}

func TestConfigInjectionRequiredFieldEmpty(t *testing.T) {
	require.NoError(t, os.Setenv("API_KEY", "   "))
	defer os.Unsetenv("API_KEY")

	cfg, err := Load()
	require.NoError(t, err)

	var serviceConfig BasicServiceConfig
	err = cfg.InjectInto(&serviceConfig)

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "required configuration key 'api.key' is empty")
}

func TestConfigInjectionInvalidTypes(t *testing.T) {
	// Test invalid duration
	require.NoError(t, os.Setenv("API_KEY", "test"))
	require.NoError(t, os.Setenv("API_TIMEOUT", "invalid-duration"))
	defer func() {
		os.Unsetenv("API_KEY")
		os.Unsetenv("API_TIMEOUT")
	}()

	cfg, err := Load()
	require.NoError(t, err)

	var serviceConfig BasicServiceConfig
	err = cfg.InjectInto(&serviceConfig)

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "invalid duration value")
}

func TestConfigInjectionUnsupportedFieldType(t *testing.T) {
	// Set a value for the unsupported field to trigger the type error
	require.NoError(t, os.Setenv("TEST_CHANNEL", "some-value"))
	defer os.Unsetenv("TEST_CHANNEL")

	cfg, err := Load()
	require.NoError(t, err)

	var invalidConfig InvalidConfig
	err = cfg.InjectInto(&invalidConfig)

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported field type")
}

func TestConfigInjectionNotAStruct(t *testing.T) {
	cfg, err := Load()
	require.NoError(t, err)

	var notAStruct string
	err = cfg.InjectInto(&notAStruct)

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "target must be a pointer to a struct")
}

func TestConfigInjectionNotAPointer(t *testing.T) {
	cfg, err := Load()
	require.NoError(t, err)

	var serviceConfig BasicServiceConfig
	err = cfg.InjectInto(serviceConfig) // Not a pointer

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "target must be a pointer to a struct")
}

func TestConfigInjectionNoConfigTags(t *testing.T) {
	cfg, err := Load()
	require.NoError(t, err)

	var noTagsConfig NoConfigTagsStruct
	err = cfg.InjectInto(&noTagsConfig)

	// Should succeed but do nothing
	require.NoError(t, err)
	assert.Zero(t, noTagsConfig.Field1)
	assert.Zero(t, noTagsConfig.Field2)
}

func TestConfigInjectionNilConfig(t *testing.T) {
	var cfg *Config
	var serviceConfig BasicServiceConfig

	err := cfg.InjectInto(&serviceConfig)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "configuration not initialized")
}

func TestConfigInjectionIntegerOverflow(t *testing.T) {
	require.NoError(t, os.Setenv("API_KEY", "test"))
	require.NoError(t, os.Setenv("API_MAX_RETRY", "999999999999999999999")) // Very large number
	defer func() {
		os.Unsetenv("API_KEY")
		os.Unsetenv("API_MAX_RETRY")
	}()

	cfg, err := Load()
	require.NoError(t, err)

	var serviceConfig BasicServiceConfig
	err = cfg.InjectInto(&serviceConfig)

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "invalid integer value")
}

func TestConfigInjectionBooleanConversion(t *testing.T) {
	testCases := []struct {
		value    string
		expected bool
	}{
		{"true", true},
		{"false", false},
		{"1", true},
		{"0", false},
		{"yes", false}, // Should fail and use default
	}

	for _, tc := range testCases {
		t.Run(fmt.Sprintf("bool_%s", tc.value), func(t *testing.T) {
			require.NoError(t, os.Setenv("API_KEY", "test"))
			require.NoError(t, os.Setenv("API_ENABLED", tc.value))
			defer func() {
				os.Unsetenv("API_KEY")
				os.Unsetenv("API_ENABLED")
			}()

			cfg, err := Load()
			require.NoError(t, err)

			var serviceConfig BasicServiceConfig
			err = cfg.InjectInto(&serviceConfig)

			if tc.value == "yes" {
				// Invalid boolean should cause error
				assert.Error(t, err)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tc.expected, serviceConfig.Enabled)
			}
		})
	}
}

// Benchmark tests
func BenchmarkConfigInjection(b *testing.B) {
	require.NoError(b, os.Setenv("API_KEY", testAPIKey))
	defer os.Unsetenv("API_KEY")

	cfg, err := Load()
	require.NoError(b, err)

	b.ResetTimer()
	for b.Loop() {
		var serviceConfig BasicServiceConfig
		err := cfg.InjectInto(&serviceConfig)
		if err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkConfigInjectionComplex(b *testing.B) {
	envVars := map[string]string{
		"DATABASE_URL": "postgres://test",
		"AUTH_SECRET":  "secret",
	}

	for key, value := range envVars {
		require.NoError(b, os.Setenv(key, value))
	}
	defer func() {
		for key := range envVars {
			os.Unsetenv(key)
		}
	}()

	cfg, err := Load()
	require.NoError(b, err)

	b.ResetTimer()
	for b.Loop() {
		var serviceConfig ComplexServiceConfig
		err := cfg.InjectInto(&serviceConfig)
		if err != nil {
			b.Fatal(err)
		}
	}
}
