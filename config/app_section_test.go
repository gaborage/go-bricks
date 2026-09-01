package config

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

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

// createValidAppConfig returns a valid AppConfig for testing
func createValidAppConfig() AppConfig {
	return AppConfig{
		Name:    testAppName,
		Version: testAppVersion,
		Env:     EnvDevelopment,
		Rate:    RateConfig{Limit: 100},
	}
}
