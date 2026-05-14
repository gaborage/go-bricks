package app

import (
	"testing"

	"github.com/gaborage/go-bricks/config"
	"github.com/gaborage/go-bricks/logger"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	shouldSkipWithPreviousError = "with previous error should skip"
	missingAppInstanceErrorMsg  = "missing app instance"
)

func TestNewAppBuilder(t *testing.T) {
	builder := NewAppBuilder()
	assert.NotNil(t, builder)
	assert.Nil(t, builder.cfg)
	assert.Nil(t, builder.opts)
	assert.Nil(t, builder.logger)
	assert.Nil(t, builder.err)
}

func TestAppBuilderWithConfig(t *testing.T) {
	t.Run("valid config and options", func(t *testing.T) {
		cfg := &config.Config{
			App: config.AppConfig{
				Name:    "test-app",
				Env:     "test",
				Version: "1.0.0",
			},
		}
		opts := &Options{}

		builder := NewAppBuilder().WithConfig(cfg, opts)
		assert.NotNil(t, builder)
		assert.Equal(t, cfg, builder.cfg)
		assert.Equal(t, opts, builder.opts)
		assert.Nil(t, builder.err)
	})

	t.Run(shouldSkipWithPreviousError, func(t *testing.T) {
		builder := &Builder{err: assert.AnError}
		result := builder.WithConfig(&config.Config{}, &Options{})
		assert.Equal(t, builder, result)
		assert.Equal(t, assert.AnError, result.err)
	})
}

func TestAppBuilderCreateLoggerErrors(t *testing.T) {
	t.Run("missing configuration", func(t *testing.T) {
		builder := NewAppBuilder()
		result := builder.CreateLogger()

		assert.NotNil(t, result.err)
		assert.Contains(t, result.err.Error(), "configuration required before creating logger")
		assert.Nil(t, result.logger)
	})

	t.Run(shouldSkipWithPreviousError, func(t *testing.T) {
		builder := &Builder{err: assert.AnError}
		result := builder.CreateLogger()
		assert.Equal(t, assert.AnError, result.err)
		assert.Nil(t, result.logger)
	})

	t.Run("valid config creates logger", func(t *testing.T) {
		cfg := &config.Config{
			App: config.AppConfig{
				Name:    "test-app",
				Env:     "test",
				Version: "1.0.0",
			},
			Log: config.LogConfig{
				Level:  "info",
				Pretty: false,
			},
		}

		builder := NewAppBuilder().WithConfig(cfg, &Options{})
		result := builder.CreateLogger()

		assert.Nil(t, result.err)
		assert.NotNil(t, result.logger)
	})
}

func TestAppBuilderCreateLoggerWithFormat(t *testing.T) {
	// Verify the builder wiring accepts every format value without erroring
	// and produces a logger. Pretty-mode resolution itself is covered
	// exhaustively in logger.TestResolvePretty.
	cases := []string{"", "auto", "console", "json", "pretty", "structured", "AUTO"}

	for _, format := range cases {
		t.Run("format="+format, func(t *testing.T) {
			cfg := &config.Config{
				App: config.AppConfig{Name: "test-app", Env: "test", Version: "1.0.0"},
				Log: config.LogConfig{
					Level:  "info",
					Output: config.OutputConfig{Format: format},
				},
			}

			result := NewAppBuilder().WithConfig(cfg, &Options{}).CreateLogger()
			assert.Nil(t, result.err)
			assert.NotNil(t, result.logger)
		})
	}
}

func TestResolveLoggerFilterConfig(t *testing.T) {
	t.Run("no_options_no_config_returns_nil", func(t *testing.T) {
		got := resolveLoggerFilterConfig(nil, &config.LogConfig{})
		assert.Nil(t, got)
	})

	t.Run("nil_options_and_nil_cfg_returns_nil", func(t *testing.T) {
		got := resolveLoggerFilterConfig(nil, nil)
		assert.Nil(t, got)
	})

	t.Run("options_filter_takes_precedence", func(t *testing.T) {
		custom := &logger.FilterConfig{
			SensitiveFields: []string{"pan"},
			MaskValue:       "XXX",
		}
		got := resolveLoggerFilterConfig(
			&Options{LoggerFilterConfig: custom},
			&config.LogConfig{SensitiveFields: []string{"cvv2"}},
		)
		require.NotNil(t, got)
		assert.Same(t, custom, got, "options config should be returned verbatim")
		assert.Equal(t, []string{"pan"}, got.SensitiveFields)
		assert.Equal(t, "XXX", got.MaskValue)
	})

	t.Run("options_filter_can_opt_out_entirely", func(t *testing.T) {
		// Setting SensitiveFields to nil/empty bypasses all masking.
		// Consumers in non-regulated contexts can use this to drop the default list.
		empty := &logger.FilterConfig{SensitiveFields: nil}
		got := resolveLoggerFilterConfig(&Options{LoggerFilterConfig: empty}, &config.LogConfig{})
		require.NotNil(t, got)
		assert.Empty(t, got.SensitiveFields)
	})

	t.Run("config_sensitive_fields_extend_defaults", func(t *testing.T) {
		// Additive: every default field is preserved AND custom fields appended.
		got := resolveLoggerFilterConfig(
			nil,
			&config.LogConfig{SensitiveFields: []string{"pan", "cvv2", "otp"}},
		)
		require.NotNil(t, got)

		defaults := logger.DefaultFilterConfig().SensitiveFields
		// Defaults are preserved.
		for _, defaultField := range defaults {
			assert.Contains(t, got.SensitiveFields, defaultField, "default field %q must survive merge", defaultField)
		}
		// Custom fields are appended.
		assert.Contains(t, got.SensitiveFields, "pan")
		assert.Contains(t, got.SensitiveFields, "cvv2")
		assert.Contains(t, got.SensitiveFields, "otp")
		// Length sanity check.
		assert.Equal(t, len(defaults)+3, len(got.SensitiveFields))
	})

	t.Run("empty_config_sensitive_fields_returns_nil", func(t *testing.T) {
		// An empty slice is treated the same as no override — caller falls
		// through to DefaultFilterConfig via NewWithFilter(nil).
		got := resolveLoggerFilterConfig(nil, &config.LogConfig{SensitiveFields: []string{}})
		assert.Nil(t, got)
	})

	t.Run("options_present_but_filter_nil_uses_config", func(t *testing.T) {
		// A populated Options struct that doesn't set LoggerFilterConfig must not
		// short-circuit the config path — typical for apps that configure
		// Database/Server via Options and masking via YAML.
		got := resolveLoggerFilterConfig(
			&Options{Database: nil}, // LoggerFilterConfig left zero
			&config.LogConfig{SensitiveFields: []string{"pan"}},
		)
		require.NotNil(t, got)
		assert.Contains(t, got.SensitiveFields, "pan")
		// Defaults still merged in.
		assert.Contains(t, got.SensitiveFields, "password")
	})
}

func TestAppBuilderCreateLoggerWithFilterConfig(t *testing.T) {
	t.Run("options_filter_accepted", func(t *testing.T) {
		// Smoke test: builder wires Options.LoggerFilterConfig through without
		// error. End-to-end masking behavior is covered by logger.TestNewWithFilter.
		cfg := &config.Config{
			App: config.AppConfig{Name: "test-app", Env: "test", Version: "1.0.0"},
			Log: config.LogConfig{Level: "info"},
		}
		opts := &Options{
			LoggerFilterConfig: &logger.FilterConfig{
				SensitiveFields: []string{"pan", "cvv2"},
				MaskValue:       "***",
			},
		}
		result := NewAppBuilder().WithConfig(cfg, opts).CreateLogger()
		assert.Nil(t, result.err)
		assert.NotNil(t, result.logger)
	})

	t.Run("config_sensitive_fields_accepted", func(t *testing.T) {
		cfg := &config.Config{
			App: config.AppConfig{Name: "test-app", Env: "test", Version: "1.0.0"},
			Log: config.LogConfig{
				Level:           "info",
				SensitiveFields: []string{"pan", "cvv2", "otp"},
			},
		}
		result := NewAppBuilder().WithConfig(cfg, &Options{}).CreateLogger()
		assert.Nil(t, result.err)
		assert.NotNil(t, result.logger)
	})
}

func TestOtlpLogsActive(t *testing.T) {
	t.Run("nil_config_returns_false", func(t *testing.T) {
		assert.False(t, otlpLogsActive(nil))
	})

	t.Run("observability_disabled_returns_false", func(t *testing.T) {
		cfg := loadConfigFromYAML(t, minimumValidConfig+`
observability:
  enabled: false
`)
		assert.False(t, otlpLogsActive(cfg))
	})

	t.Run("observability_enabled_logs_default_returns_true", func(t *testing.T) {
		cfg := loadConfigFromYAML(t, minimumValidConfig+`
observability:
  enabled: true
  service:
    name: test
`)
		assert.True(t, otlpLogsActive(cfg))
	})

	t.Run("observability_enabled_logs_explicitly_disabled_returns_false", func(t *testing.T) {
		cfg := loadConfigFromYAML(t, minimumValidConfig+`
observability:
  enabled: true
  service:
    name: test
  logs:
    enabled: false
`)
		assert.False(t, otlpLogsActive(cfg))
	})
}

func TestAppBuilderCreateBootstrapErrors(t *testing.T) {
	t.Run("missing logger", func(t *testing.T) {
		cfg := &config.Config{}
		builder := NewAppBuilder().WithConfig(cfg, &Options{})
		result := builder.CreateBootstrap()

		assert.NotNil(t, result.err)
		assert.Contains(t, result.err.Error(), "logger required before creating bootstrap")
		assert.Nil(t, result.bootstrap)
	})

	t.Run(shouldSkipWithPreviousError, func(t *testing.T) {
		builder := &Builder{err: assert.AnError}
		result := builder.CreateBootstrap()
		assert.Equal(t, assert.AnError, result.err)
		assert.Nil(t, result.bootstrap)
	})
}

func TestAppBuilderResolveDependenciesErrors(t *testing.T) {
	t.Run("missing bootstrap", func(t *testing.T) {
		builder := NewAppBuilder()
		result := builder.ResolveDependencies()

		assert.NotNil(t, result.err)
		assert.Contains(t, result.err.Error(), "bootstrap required before resolving dependencies")
		assert.Nil(t, result.bundle)
	})

	t.Run(shouldSkipWithPreviousError, func(t *testing.T) {
		builder := &Builder{err: assert.AnError}
		result := builder.ResolveDependencies()
		assert.Equal(t, assert.AnError, result.err)
		assert.Nil(t, result.bundle)
	})
}

func TestAppBuilderCreateAppErrors(t *testing.T) {
	t.Run("missing dependencies", func(t *testing.T) {
		builder := NewAppBuilder()
		result := builder.CreateApp()

		assert.NotNil(t, result.err)
		assert.Contains(t, result.err.Error(), "dependencies required before creating app")
		assert.Nil(t, result.app)
	})

	t.Run(shouldSkipWithPreviousError, func(t *testing.T) {
		builder := &Builder{err: assert.AnError}
		result := builder.CreateApp()
		assert.Equal(t, assert.AnError, result.err)
		assert.Nil(t, result.app)
	})
}

func TestAppBuilderInitializeRegistryErrors(t *testing.T) {
	t.Run(missingAppInstanceErrorMsg, func(t *testing.T) {
		builder := NewAppBuilder()
		result := builder.InitializeRegistry()

		assert.NotNil(t, result.err)
		assert.Contains(t, result.err.Error(), "app instance required before initializing registry")
	})

	t.Run(shouldSkipWithPreviousError, func(t *testing.T) {
		builder := &Builder{err: assert.AnError}
		result := builder.InitializeRegistry()
		assert.Equal(t, assert.AnError, result.err)
	})
}

func TestAppBuilderConfigureRuntimeHelpersErrors(t *testing.T) {
	t.Run(missingAppInstanceErrorMsg, func(t *testing.T) {
		builder := NewAppBuilder()
		result := builder.ConfigureRuntimeHelpers()

		assert.NotNil(t, result.err)
		assert.Contains(t, result.err.Error(), "app instance required before configuring runtime helpers")
	})

	t.Run(shouldSkipWithPreviousError, func(t *testing.T) {
		builder := &Builder{err: assert.AnError}
		result := builder.ConfigureRuntimeHelpers()
		assert.Equal(t, assert.AnError, result.err)
	})
}

func TestAppBuilderCreateHealthProbesErrors(t *testing.T) {
	t.Run(missingAppInstanceErrorMsg, func(t *testing.T) {
		builder := NewAppBuilder()
		result := builder.CreateHealthProbes()

		assert.NotNil(t, result.err)
		assert.Contains(t, result.err.Error(), "app instance required before creating health probes")
	})

	t.Run(shouldSkipWithPreviousError, func(t *testing.T) {
		builder := &Builder{err: assert.AnError}
		result := builder.CreateHealthProbes()
		assert.Equal(t, assert.AnError, result.err)
	})
}

func TestAppBuilderRegisterClosersErrors(t *testing.T) {
	t.Run(missingAppInstanceErrorMsg, func(t *testing.T) {
		builder := NewAppBuilder()
		result := builder.RegisterClosers()

		assert.NotNil(t, result.err)
		assert.Contains(t, result.err.Error(), "app instance required before registering closers")
	})

	t.Run(shouldSkipWithPreviousError, func(t *testing.T) {
		builder := &Builder{err: assert.AnError}
		result := builder.RegisterClosers()
		assert.Equal(t, assert.AnError, result.err)
	})
}

func TestAppBuilderRegisterReadyHandlerErrors(t *testing.T) {
	t.Run(missingAppInstanceErrorMsg, func(t *testing.T) {
		builder := NewAppBuilder()
		result := builder.RegisterReadyHandler()

		assert.NotNil(t, result.err)
		assert.Contains(t, result.err.Error(), "app instance required before registering ready handler")
	})

	t.Run(shouldSkipWithPreviousError, func(t *testing.T) {
		builder := &Builder{err: assert.AnError}
		result := builder.RegisterReadyHandler()
		assert.Equal(t, assert.AnError, result.err)
	})
}

func TestAppBuilderBuildErrors(t *testing.T) {
	t.Run("with build error", func(t *testing.T) {
		builder := &Builder{err: assert.AnError}
		app, log, err := builder.Build()

		assert.Error(t, err)
		assert.Equal(t, assert.AnError, err)
		assert.Nil(t, app)
		assert.NotNil(t, log) // Logger should always be available
	})

	t.Run("incomplete build without app", func(t *testing.T) {
		builder := NewAppBuilder()
		app, log, err := builder.Build()

		assert.Error(t, err)
		assert.Contains(t, err.Error(), "app building incomplete")
		assert.Nil(t, app)
		assert.NotNil(t, log) // Logger should always be available
	})
}

func TestAppBuilderError(t *testing.T) {
	t.Run("no error", func(t *testing.T) {
		builder := NewAppBuilder()
		err := builder.Error()
		assert.Nil(t, err)
	})

	t.Run("with error", func(t *testing.T) {
		expectedError := assert.AnError
		builder := &Builder{err: expectedError}
		err := builder.Error()
		assert.Equal(t, expectedError, err)
	})
}

func TestAppBuilderChainValidation(t *testing.T) {
	t.Run("error propagates through chain", func(t *testing.T) {
		builder := NewAppBuilder()

		// Skip config setup to trigger first error
		result := builder.
			CreateLogger().        // Should fail here
			CreateBootstrap().     // Should skip due to previous error
			ResolveDependencies(). // Should skip due to previous error
			CreateApp()            // Should skip due to previous error

		assert.NotNil(t, result.err)
		assert.Contains(t, result.err.Error(), "configuration required")
		assert.Nil(t, result.logger)
		assert.Nil(t, result.bootstrap)
		assert.Nil(t, result.bundle)
		assert.Nil(t, result.app)
	})
}

func TestAppBuilderErrorRecovery(t *testing.T) {
	t.Run("builder state remains consistent after error", func(t *testing.T) {
		builder := NewAppBuilder()

		// Trigger an error
		builder.CreateLogger() // Will fail due to missing config
		require.NotNil(t, builder.err)

		// Subsequent calls should not crash and maintain error state
		builder.CreateBootstrap()
		builder.ResolveDependencies()
		builder.CreateApp()

		// Error should still be the original error
		assert.Contains(t, builder.err.Error(), "configuration required")

		// Build should return the same error
		app, log, buildErr := builder.Build()
		assert.Nil(t, app)
		assert.NotNil(t, log) // Logger should always be available
		assert.Equal(t, builder.err, buildErr)
	})
}
