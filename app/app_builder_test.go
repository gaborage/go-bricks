package app

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/cache"
	cachetesting "github.com/gaborage/go-bricks/cache/testing"
	"github.com/gaborage/go-bricks/config"
	"github.com/gaborage/go-bricks/database"
	"github.com/gaborage/go-bricks/logger"
	"github.com/gaborage/go-bricks/messaging"
	testmocks "github.com/gaborage/go-bricks/testing/mocks"
)

const (
	shouldSkipWithPreviousError = "with previous error should skip"
	missingAppInstanceErrorMsg  = "missing app instance"
	// A well-formed DSN whose scheme config's inference does not recognize, so it
	// reaches ConfigureRuntimeHelpers still carrying an empty Type.
	unrecognizedSchemeDSN = "sqlserver://user:pass@localhost:1433/db"
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
		cfg := defaultTestConfig()
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

// TestAppBuilderWithConfigValidatesHandBuiltConfig pins that WithConfig runs
// config.Validate on every construction path, not just config.Load output.
func TestAppBuilderWithConfigValidatesHandBuiltConfig(t *testing.T) {
	cfg := defaultTestConfig()
	cfg.Database.Type = "mysql" // invalid vendor: Validate must reject at construction

	app, log, err := NewWithConfig(cfg, &Options{})

	require.Error(t, err)
	assert.Nil(t, app)
	assert.NotNil(t, log)
	assert.Contains(t, err.Error(), "invalid configuration")
	assert.Contains(t, err.Error(), "database.type")
}

// TestAppBuilderWithConfigStampsDefaultsOnHandBuiltConfig pins that a hand-built
// config Validate accepts still receives the same defaults config.Load stamps.
// WithConfig alone is under test — the stamping happens there, before any later
// builder stage runs.
func TestAppBuilderWithConfigStampsDefaultsOnHandBuiltConfig(t *testing.T) {
	cfg := defaultTestConfig()

	result := NewAppBuilder().WithConfig(cfg, &Options{})

	require.NoError(t, result.err)
	assert.Equal(t, int32(25), cfg.Database.Pool.Max.Connections, "pool defaults reach hand-built configs")
	assert.Positive(t, cfg.Messaging.Publisher.IdleTTL, "messaging defaults reach hand-built configs")
}

// TestAppBuilderWithConfigRejectsNilConfig pins that a nil config fails construction
// with a logger still available, rather than reaching the old nil-cfg CreateLogger check.
func TestAppBuilderWithConfigRejectsNilConfig(t *testing.T) {
	app, log, err := NewWithConfig(nil, &Options{})
	require.Error(t, err)
	assert.Nil(t, app)
	assert.NotNil(t, log)
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
		cfg := defaultTestConfig()

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
			cfg := defaultTestConfig()
			cfg.Log.Output.Format = format

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
			&config.LogConfig{SensitiveFields: []string{"pan", "cvv2", "ssn"}},
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
		assert.Contains(t, got.SensitiveFields, "ssn")
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

	t.Run("empty_string_entries_are_dropped", func(t *testing.T) {
		// An empty string in cfg.SensitiveFields would make strings.Contains
		// return true for every field name, silently masking the entire log
		// stream. The normalizer must drop empty (and whitespace-only) entries.
		defaultsLen := len(logger.DefaultFilterConfig().SensitiveFields)
		got := resolveLoggerFilterConfig(
			nil,
			&config.LogConfig{SensitiveFields: []string{"pan", "", "   ", "cvv2"}},
		)
		require.NotNil(t, got)
		assert.NotContains(t, got.SensitiveFields, "")
		assert.NotContains(t, got.SensitiveFields, "   ")
		assert.Contains(t, got.SensitiveFields, "pan")
		assert.Contains(t, got.SensitiveFields, "cvv2")
		// Only the two non-empty entries are appended on top of defaults.
		assert.Equal(t, defaultsLen+2, len(got.SensitiveFields))
	})

	t.Run("whitespace_padded_entries_are_trimmed", func(t *testing.T) {
		// Without trimming, "  cvv  " would never match field name "cvv"
		// because strings.Contains("cvv", "  cvv  ") is false. Operators
		// pasting from CSV/comments would see silent non-masking — exactly
		// the failure mode the filter is supposed to prevent.
		got := resolveLoggerFilterConfig(
			nil,
			&config.LogConfig{SensitiveFields: []string{"  ssn  ", "\tpan\n"}},
		)
		require.NotNil(t, got)
		assert.Contains(t, got.SensitiveFields, "ssn")
		assert.Contains(t, got.SensitiveFields, "pan")
		assert.NotContains(t, got.SensitiveFields, "  ssn  ")
		assert.NotContains(t, got.SensitiveFields, "\tpan\n")
	})

	t.Run("case_insensitive_dedup_within_config", func(t *testing.T) {
		// PAN/Pan/pan are the same field — emit one entry. Substring matching
		// would still work for all variants if we appended duplicates, but the
		// dedup keeps the slice tight (less work per logged field).
		defaultsLen := len(logger.DefaultFilterConfig().SensitiveFields)
		got := resolveLoggerFilterConfig(
			nil,
			&config.LogConfig{SensitiveFields: []string{"PAN", "Pan", "pan", "PAN "}},
		)
		require.NotNil(t, got)
		// First occurrence wins, preserving its case.
		assert.Contains(t, got.SensitiveFields, "PAN")
		assert.NotContains(t, got.SensitiveFields, "Pan")
		assert.NotContains(t, got.SensitiveFields, "pan")
		assert.Equal(t, defaultsLen+1, len(got.SensitiveFields))
	})

	t.Run("config_entry_already_in_defaults_is_skipped", func(t *testing.T) {
		// "password" already covered by DefaultFilterConfig; re-listing it in
		// YAML must not produce a duplicate entry.
		defaultsLen := len(logger.DefaultFilterConfig().SensitiveFields)
		got := resolveLoggerFilterConfig(
			nil,
			&config.LogConfig{SensitiveFields: []string{"password", "PASSWORD", "pan"}},
		)
		require.NotNil(t, got)
		// Defaults still contain "password" (case from DefaultFilterConfig).
		assert.Contains(t, got.SensitiveFields, "password")
		// Only "pan" is genuinely new; "password"/"PASSWORD" collapse to the default.
		assert.Equal(t, defaultsLen+1, len(got.SensitiveFields))
	})
}

func TestAppBuilderCreateLoggerWithFilterConfig(t *testing.T) {
	t.Run("options_filter_accepted", func(t *testing.T) {
		// Smoke test: builder wires Options.LoggerFilterConfig through without
		// error. End-to-end masking behavior is covered by logger.TestNewWithFilter.
		cfg := defaultTestConfig()
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
		cfg := defaultTestConfig()
		cfg.Log.SensitiveFields = []string{"pan", "cvv2", "otp"}
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
		cfg := defaultTestConfig()
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

// TestAppBuilderResolveDependenciesFailsClosedOnInvalidCacheConfig pins that a
// misconfigured cache aborts the build instead of leaving the app with a nil cache
// manager. Driven through the full builder because the propagation seam
// (bootstrap.dependencies -> ResolveDependencies -> Build) is where the bug lived.
func TestAppBuilderResolveDependenciesFailsClosedOnInvalidCacheConfig(t *testing.T) {
	tests := []struct {
		name      string
		mutate    func(cfg *config.Config)
		wantCause string
	}{
		{
			name:      "negative_maxsize",
			mutate:    func(cfg *config.Config) { cfg.Cache.Manager.MaxSize = -1 },
			wantCause: "maxsize cannot be negative",
		},
		{
			name:      "negative_idlettl",
			mutate:    func(cfg *config.Config) { cfg.Cache.Manager.IdleTTL = -time.Second },
			wantCause: "idlettl cannot be negative",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := defaultTestConfig()
			tt.mutate(cfg)

			builder := NewAppBuilder().WithConfig(cfg, &Options{}).CreateLogger().CreateBootstrap().ResolveDependencies()

			require.Error(t, builder.err)
			assert.Nil(t, builder.bundle, "a failed resolution must not publish a partial bundle")
			assert.ErrorContains(t, builder.err, tt.wantCause)

			app, log, err := builder.CreateApp().Build()
			require.Error(t, err, "startup must abort, not continue with a nil cache manager")
			assert.Nil(t, app)
			assert.NotNil(t, log)
		})
	}
}

// TestNewWithConfigFailsClosedOnInvalidCacheConfig is the consumer-facing half of the
// contract above: the same misconfiguration must reach the caller of the public
// constructor as an error rather than a running App. Every connector is stubbed so a
// regression cannot substitute a dial failure for the assertion under test.
func TestNewWithConfigFailsClosedOnInvalidCacheConfig(t *testing.T) {
	cfg := defaultTestConfig()
	cfg.Cache.Manager.MaxSize = -1

	app, log, err := NewWithConfig(cfg, &Options{
		DatabaseConnector: func(*config.DatabaseConfig, logger.Logger) (database.Interface, error) {
			return &testmocks.MockDatabase{}, nil
		},
		MessagingClientFactory: func(string, logger.Logger) messaging.AMQPClient {
			return testmocks.NewMockAMQPClient()
		},
		CacheConnector: func(context.Context, string) (cache.Cache, error) {
			return cachetesting.NewMockCache(), nil
		},
	})

	require.Error(t, err)
	assert.Nil(t, app)
	assert.NotNil(t, log)
	assert.ErrorContains(t, err, "cache manager")
	assert.ErrorContains(t, err, "maxsize cannot be negative")
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

// TestAppBuilderInitializeRegistryWiresDatabaseVerdict pins the one wiring step that
// arms ModuleRegistry's DatabaseRequirer gate. The gate's zero value is inert, so
// dropping this assignment would silently disable it with no compile error — this test
// is what notices.
func TestAppBuilderInitializeRegistryWiresDatabaseVerdict(t *testing.T) {
	tests := []struct {
		name         string
		mutate       func(cfg *config.Config)
		wantDBAbsent bool
	}{
		{name: "absent_database_arms_the_gate", wantDBAbsent: true, mutate: func(*config.Config) {}},
		{name: "configured_database_leaves_it_disarmed", wantDBAbsent: false, mutate: func(cfg *config.Config) {
			cfg.Database.Type = "postgresql"
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &config.Config{}
			tt.mutate(cfg)
			builder := &Builder{
				cfg:    cfg,
				app:    &App{},
				bundle: &dependencyBundle{deps: &ModuleDeps{Config: cfg}},
			}

			result := builder.InitializeRegistry()

			require.NoError(t, result.err)
			assert.Equal(t, tt.wantDBAbsent, result.app.registry.rootDBAbsent)
		})
	}
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

func TestAppBuilderConfigureRuntimeHelpersThreadsReadyTimeout(t *testing.T) {
	cfg := &config.Config{}
	cfg.Multitenant.Enabled = true // skip pre-initialization; only helper wiring is under test
	cfg.Messaging.Reconnect.ReadyTimeout = 20 * time.Second

	builder := &Builder{cfg: cfg, logger: logger.New("error", false), app: &App{}}
	result := builder.ConfigureRuntimeHelpers()

	require.NoError(t, result.err)
	assert.Equal(t, 20*time.Second, result.app.connectionPreWarmer.readinessTimeout)
}

// TestAppBuilderConfigureRuntimeHelpersRejectsUntypedConnectionString pins ADR-050: with
// the built-in connector, a connection string whose scheme inference (config/validation.go)
// didn't recognize it and that carries no explicit type can never dispatch, so the builder
// must fail fast rather than let startup succeed into a dead database.
func TestAppBuilderConfigureRuntimeHelpersRejectsUntypedConnectionString(t *testing.T) {
	cfg := &config.Config{}
	cfg.Database.ConnectionString = unrecognizedSchemeDSN

	builder := &Builder{cfg: cfg, logger: logger.New("error", false), app: &App{}}
	result := builder.ConfigureRuntimeHelpers()

	require.Error(t, result.err)
	assert.Contains(t, result.err.Error(), "connectionstring has no resolved database type")
	// Bracketed: the message's own prefix is "database configuration at …", so a bare
	// "database" substring holds for every path list and would pin nothing.
	assert.Contains(t, result.err.Error(), "[database]")
}

// TestAppBuilderConfigureRuntimeHelpersGuardsWhenOptionsLackDatabaseConnector pins the
// DatabaseConnector-nil half of the guard: a non-nil Options set for an unrelated reason
// (here, MessagingClientFactory) is the common consumer shape, and the guard must still
// fire on the built-in connector rather than being defeated by Options merely being non-nil.
func TestAppBuilderConfigureRuntimeHelpersGuardsWhenOptionsLackDatabaseConnector(t *testing.T) {
	cfg := &config.Config{}
	cfg.Database.ConnectionString = unrecognizedSchemeDSN

	opts := &Options{
		MessagingClientFactory: func(string, logger.Logger) messaging.AMQPClient {
			return testmocks.NewMockAMQPClient()
		},
	}
	builder := &Builder{cfg: cfg, opts: opts, logger: logger.New("error", false), app: &App{}}
	result := builder.ConfigureRuntimeHelpers()

	require.Error(t, result.err)
	assert.Contains(t, result.err.Error(), "connectionstring has no resolved database type")
	// Bracketed: the message's own prefix is "database configuration at …", so a bare
	// "database" substring holds for every path list and would pin nothing.
	assert.Contains(t, result.err.Error(), "[database]")
}

// TestAppBuilderConfigureRuntimeHelpersGuardsRecognizedSchemeWithoutValidation pins a
// builder invoked without WithConfig's config.Validate step (constructed directly, as
// this test does — WithConfig itself now validates on every NewWithConfig call): even
// a recognized scheme reaches the guard untyped and the message must report the state,
// not blame the scheme.
func TestAppBuilderConfigureRuntimeHelpersGuardsRecognizedSchemeWithoutValidation(t *testing.T) {
	cfg := &config.Config{}
	cfg.Database.ConnectionString = "postgres://user:pass@localhost:5432/db"

	builder := &Builder{cfg: cfg, logger: logger.New("error", false), app: &App{}}
	result := builder.ConfigureRuntimeHelpers()

	require.Error(t, result.err)
	assert.Contains(t, result.err.Error(),
		"database configuration at [database]: connectionstring has no resolved database type")
	assert.Contains(t, result.err.Error(), "set <path>.type to postgresql or oracle")
}

// TestAppBuilderConfigureRuntimeHelpersExemptsCustomConnector pins that a custom
// Options.DatabaseConnector owns DSN parsing and is exempt from the untyped-DSN guard.
func TestAppBuilderConfigureRuntimeHelpersExemptsCustomConnector(t *testing.T) {
	cfg := &config.Config{}
	cfg.Database.ConnectionString = unrecognizedSchemeDSN
	cfg.Multitenant.Enabled = true // skip pre-initialization; only the guard is under test

	opts := &Options{
		DatabaseConnector: func(*config.DatabaseConfig, logger.Logger) (database.Interface, error) {
			return &testmocks.MockDatabase{}, nil
		},
	}
	builder := &Builder{cfg: cfg, opts: opts, logger: logger.New("error", false), app: &App{}}
	result := builder.ConfigureRuntimeHelpers()

	require.NoError(t, result.err)
}

// TestAppBuilderConfigureRuntimeHelpersListsAllUntypedPaths pins the sorted, multi-path
// error shape. TWO entries in cfg.Databases is what makes slices.Sort load-bearing: Go
// randomizes map iteration, so without the sort "analytics" and "reporting" would flip
// between runs. Asserting the rendered slice pins the exact set AND the order.
func TestAppBuilderConfigureRuntimeHelpersListsAllUntypedPaths(t *testing.T) {
	cfg := &config.Config{}
	cfg.Databases = map[string]config.DatabaseConfig{
		"reporting": {ConnectionString: "sqlserver://h1:1433/db1"},
		"analytics": {ConnectionString: "sqlserver://h3:1433/db3"},
	}
	cfg.Multitenant.Enabled = true // tenant DSNs reach inference only when multitenancy is on
	cfg.Multitenant.Tenants = map[string]config.TenantEntry{
		"acme": {Database: config.DatabaseConfig{ConnectionString: "sqlserver://h2:1433/db2"}},
	}

	builder := &Builder{cfg: cfg, logger: logger.New("error", false), app: &App{}}
	result := builder.ConfigureRuntimeHelpers()

	require.Error(t, result.err)
	assert.Contains(t, result.err.Error(),
		"[databases.analytics databases.reporting multitenant.tenants.acme.database]")
}

// TestAppBuilderConfigureRuntimeHelpersIgnoresTenantsWhenMultitenantDisabled pins that a
// leftover tenants block under multitenant.enabled=false cannot abort startup: config skips
// tenant validation entirely there, so inference never ran and even a recognized scheme
// would otherwise be reported as untyped.
func TestAppBuilderConfigureRuntimeHelpersIgnoresTenantsWhenMultitenantDisabled(t *testing.T) {
	cfg := &config.Config{}
	cfg.Multitenant.Tenants = map[string]config.TenantEntry{
		"acme": {Database: config.DatabaseConfig{ConnectionString: "postgres://user:pass@localhost:5432/db"}},
	}

	// Empty bundle: single-tenant static config runs pre-initialization, which no-ops
	// on nil managers.
	builder := &Builder{cfg: cfg, logger: logger.New("error", false), app: &App{}, bundle: &dependencyBundle{}}
	result := builder.ConfigureRuntimeHelpers()

	require.NoError(t, result.err)
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

func TestAppBuilderCreateHealthProbesAppliesCacheCritical(t *testing.T) {
	tests := []struct {
		name             string
		cfg              *config.Config
		expectedCritical bool
	}{
		{name: "critical_enabled", cfg: &config.Config{Cache: config.CacheConfig{Critical: new(true)}}, expectedCritical: true},
		{name: "critical_explicit_false", cfg: &config.Config{Cache: config.CacheConfig{Critical: new(false)}}, expectedCritical: false},
		{name: "critical_unset_is_strict", cfg: &config.Config{}, expectedCritical: true},
		{name: "nil_config", cfg: nil, expectedCritical: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			builder := &Builder{
				logger: logger.New("error", false),
				app:    &App{cfg: tc.cfg, cacheManager: createTestCacheManager(t)},
			}

			result := builder.CreateHealthProbes()

			require.NoError(t, result.err)
			require.Len(t, result.app.healthProbes, 3)
			status := result.app.healthProbes[2].Run(context.Background())
			assert.Equal(t, componentCache, status.Name)
			assert.Equal(t, tc.expectedCritical, status.Critical)
		})
	}
}

// TestAppBuilderWarnsOnCacheCriticalityOptOut pins that the deliberately-weakened readiness
// posture is loud wherever the probe can actually fail: an enabled cache, or any custom
// CacheConnector (which never reads cache.enabled). The strict default (nil) must stay
// silent, otherwise every deployment boots with a warning it cannot act on.
func TestAppBuilderWarnsOnCacheCriticalityOptOut(t *testing.T) {
	const warnMarker = "cache.critical is explicitly false"

	customConnector := &Options{CacheConnector: func(context.Context, string) (cache.Cache, error) {
		return nil, assert.AnError
	}}

	tests := []struct {
		name       string
		cache      config.CacheConfig
		opts       *Options
		noCacheMgr bool
		expectLog  bool
	}{
		{name: "enabled_and_explicitly_false_warns", cache: config.CacheConfig{Enabled: true, Critical: new(false)}, expectLog: true},
		{name: "enabled_and_unset_is_silent", cache: config.CacheConfig{Enabled: true}, expectLog: false},
		{name: "enabled_and_explicitly_true_is_silent", cache: config.CacheConfig{Enabled: true, Critical: new(true)}, expectLog: false},
		{name: "disabled_and_explicitly_false_is_silent", cache: config.CacheConfig{Critical: new(false)}, expectLog: false},
		{name: "disabled_with_custom_connector_warns", cache: config.CacheConfig{Critical: new(false)}, opts: customConnector, expectLog: true},
		{name: "disabled_with_custom_connector_and_unset_is_silent", opts: customConnector, expectLog: false},
		{name: "no_cache_manager_with_custom_connector_is_silent", cache: config.CacheConfig{Critical: new(false)}, opts: customConnector, noCacheMgr: true, expectLog: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var cacheManager *cache.CacheManager
			if !tc.noCacheMgr {
				cacheManager = createTestCacheManager(t)
			}

			rec := &recLogger{}
			builder := &Builder{
				logger: rec,
				opts:   tc.opts,
				app:    &App{cfg: &config.Config{Cache: tc.cache}, cacheManager: cacheManager},
			}
			require.NoError(t, builder.CreateHealthProbes().err)

			event, logged := loggedEvent(rec, warnMarker)
			require.Equal(t, tc.expectLog, logged)
			if tc.expectLog {
				assert.Contains(t, event.msg, "a dead cache still reports ready",
					"the WARN must name the consequence, not just the key")
				assert.Equal(t, "warn", event.level)
				assert.Equal(t, "cache", event.str["resource"])
			}
		})
	}
}

// namedDBWithParamLogging returns a named-database entry (cfg.Databases) with
// query parameter logging enabled, for TestAppBuilderWarnsOnQueryParameterLogging.
func namedDBWithParamLogging() config.DatabaseConfig {
	return config.DatabaseConfig{Query: config.QueryConfig{Log: config.QueryLogConfig{Parameters: true}}}
}

// TestAppBuilderWarnsOnQueryParameterLogging pins that database.query.log.parameters
// is loud everywhere except development: bound parameter values (PANs on cardholder
// tables) are logged verbatim and bypass SensitiveDataFilter's field-name matching.
// This applies independently to the root database and to each named database
// (cfg.Databases, multi-DB single-tenant) — a named entry warns even when the root
// flag is off, and carries its own name in the message.
func TestAppBuilderWarnsOnQueryParameterLogging(t *testing.T) {
	// rootMarker carries the trailing ": " so it cannot match a named-database
	// message ("...enabled for named database ..."), keeping the two independent.
	const rootMarker = "database.query.log.parameters is enabled: "

	tests := []struct {
		name        string
		params      bool
		env         string
		databases   map[string]config.DatabaseConfig
		expectLog   bool
		expectNamed []string
	}{
		{name: "enabled_in_production_alias_warns", params: true, env: "prod", expectLog: true},
		{name: "disabled_is_silent", params: false, env: "prod", expectLog: false},
		{name: "enabled_in_development_is_silent", params: true, env: config.EnvDevelopment, expectLog: false},
		{
			name:        "named_db_enabled_in_production_warns",
			env:         "prod",
			databases:   map[string]config.DatabaseConfig{"reporting": namedDBWithParamLogging()},
			expectNamed: []string{"reporting"},
		},
		{
			name:      "named_db_enabled_in_development_is_silent",
			env:       config.EnvDevelopment,
			databases: map[string]config.DatabaseConfig{"reporting": namedDBWithParamLogging()},
		},
		{
			name:        "root_false_named_true_warns_only_named",
			params:      false,
			env:         "prod",
			databases:   map[string]config.DatabaseConfig{"reporting": namedDBWithParamLogging()},
			expectNamed: []string{"reporting"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &config.Config{}
			cfg.App.Env = tc.env
			cfg.Database.Query.Log.Parameters = tc.params
			cfg.Databases = tc.databases

			rec := &recLogger{}
			builder := &Builder{
				logger: rec,
				app:    &App{cfg: cfg},
			}
			require.NoError(t, builder.CreateHealthProbes().err)

			event, logged := loggedEvent(rec, rootMarker)
			require.Equal(t, tc.expectLog, logged, "root WARN presence")
			if tc.expectLog {
				assert.Equal(t, "warn", event.level)
				assert.Equal(t,
					"database.query.log.parameters is enabled: bound parameter values (possible PII/PAN) will be logged verbatim",
					event.msg)
			}

			for _, name := range tc.expectNamed {
				wantMsg := `database.query.log.parameters is enabled for named database "` + name +
					`": bound parameter values (possible PII/PAN) will be logged verbatim`
				namedEvent, namedLogged := loggedEvent(rec, wantMsg)
				require.True(t, namedLogged, "expected named WARN for %q", name)
				assert.Equal(t, "warn", namedEvent.level)
				assert.Equal(t, wantMsg, namedEvent.msg)
			}

			wantTotal := len(tc.expectNamed)
			if tc.expectLog {
				wantTotal++
			}
			assert.Len(t, rec.events, wantTotal, "unexpected WARN count")
		})
	}
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

// deadlineCapturingResource is a static TenantStore that records the context
// deadline observed during single-tenant pre-initialization for each component.
// It lets tests assert that DB, messaging, and cache pre-init each receive their
// own per-component startup budget rather than a single shared global timeout.
type deadlineCapturingResource struct {
	mu            sync.Mutex
	dbDeadline    time.Time
	dbHadDL       bool
	msgDeadline   time.Time
	msgHadDL      bool
	cacheDeadline time.Time
	cacheHadDL    bool
}

func (r *deadlineCapturingResource) DBConfig(ctx context.Context, _ string) (*config.DatabaseConfig, error) {
	r.mu.Lock()
	r.dbDeadline, r.dbHadDL = ctx.Deadline()
	r.mu.Unlock()
	return &config.DatabaseConfig{Type: dbTypePostgres, Host: localHost, Port: 5432}, nil
}

func (r *deadlineCapturingResource) BrokerURL(ctx context.Context, _ string) (string, error) {
	r.mu.Lock()
	r.msgDeadline, r.msgHadDL = ctx.Deadline()
	r.mu.Unlock()
	return "amqp://guest:guest@localhost:5672/", nil
}

func (r *deadlineCapturingResource) CacheConfig(_ context.Context, _ string) (*config.CacheConfig, error) {
	return &config.CacheConfig{Enabled: true, Redis: config.RedisConfig{Host: localHost, Port: 6379}}, nil
}

func (r *deadlineCapturingResource) IsDynamic() bool { return false }

func (r *deadlineCapturingResource) captureCacheDeadline(ctx context.Context) {
	r.mu.Lock()
	r.cacheDeadline, r.cacheHadDL = ctx.Deadline()
	r.mu.Unlock()
}

// TestPerformPreInitializationUsesPerComponentTimeouts verifies that single-tenant
// pre-initialization derives each component's context deadline from its own
// app.startup.{database,messaging,cache} budget, not from the shared
// app.startup.timeout fallback. The global Timeout is set small and the
// per-component budgets large and distinct so a regression to the shared timeout
// is unambiguous.
func TestPerformPreInitializationUsesPerComponentTimeouts(t *testing.T) {
	const (
		globalBudget = 2 * time.Second
		dbBudget     = 30 * time.Second
		msgBudget    = 45 * time.Second
		cacheBudget  = 8 * time.Second
	)

	cfg := defaultTestConfig()
	cfg.App.Startup = config.StartupConfig{
		Timeout:       globalBudget,
		Database:      dbBudget,
		Messaging:     msgBudget,
		Cache:         cacheBudget,
		Observability: 15 * time.Second,
	}

	resource := &deadlineCapturingResource{}
	opts := &Options{
		ResourceSource: resource,
		DatabaseConnector: func(*config.DatabaseConfig, logger.Logger) (database.Interface, error) {
			return &testmocks.MockDatabase{}, nil
		},
		MessagingClientFactory: func(string, logger.Logger) messaging.AMQPClient {
			return testmocks.NewMockAMQPClient()
		},
		CacheConnector: func(ctx context.Context, _ string) (cache.Cache, error) {
			resource.captureCacheDeadline(ctx)
			return cachetesting.NewMockCache(), nil
		},
	}

	start := time.Now()
	_, _, err := NewWithConfig(cfg, opts)
	require.NoError(t, err)

	resource.mu.Lock()
	defer resource.mu.Unlock()

	require.True(t, resource.dbHadDL, "database pre-init context must carry a deadline")
	require.True(t, resource.msgHadDL, "messaging pre-init context must carry a deadline")
	require.True(t, resource.cacheHadDL, "cache pre-init context must carry a deadline")

	const tolerance = 3 * time.Second
	assert.InDelta(t, dbBudget.Seconds(), resource.dbDeadline.Sub(start).Seconds(), tolerance.Seconds(),
		"database pre-init must use app.startup.database, not the global timeout")
	assert.InDelta(t, msgBudget.Seconds(), resource.msgDeadline.Sub(start).Seconds(), tolerance.Seconds(),
		"messaging pre-init must use app.startup.messaging, not the global timeout")
	assert.InDelta(t, cacheBudget.Seconds(), resource.cacheDeadline.Sub(start).Seconds(), tolerance.Seconds(),
		"cache pre-init must use app.startup.cache, not the global timeout")
}

// TestPerformPreInitializationZeroBudgetUsesParentContext pins that a component budget resolving to
// zero means "no explicit budget" and not "already expired". WithConfig's config.Validate call now
// stamps a real Startup budget on every config reaching NewWithConfig (see B1), so the zero-budget
// branch is exercised here via a Builder assembled directly, bypassing WithConfig — the same
// defense-in-depth path startupContext's own doc comment describes. context.WithTimeout(parent, 0)
// would otherwise hand the component a context that is dead on arrival, aborting every pool-backed
// pre-init before its connector runs.
func TestPerformPreInitializationZeroBudgetUsesParentContext(t *testing.T) {
	cfg := defaultTestConfig() // App.Startup left zero-valued; built directly, bypassing WithConfig

	resource := &deadlineCapturingResource{}
	opts := &Options{
		ResourceSource: resource,
		DatabaseConnector: func(*config.DatabaseConfig, logger.Logger) (database.Interface, error) {
			return &testmocks.MockDatabase{}, nil
		},
		MessagingClientFactory: func(string, logger.Logger) messaging.AMQPClient {
			return testmocks.NewMockAMQPClient()
		},
		CacheConnector: func(ctx context.Context, _ string) (cache.Cache, error) {
			resource.captureCacheDeadline(ctx)
			return cachetesting.NewMockCache(), nil
		},
	}

	builder := &Builder{cfg: cfg, opts: opts}
	result := builder.CreateLogger().CreateBootstrap().ResolveDependencies().
		CreateApp().InitializeRegistry().ConfigureRuntimeHelpers()
	require.NoError(t, result.err, "a zero startup budget must not expire pre-initialization instantly")

	resource.mu.Lock()
	defer resource.mu.Unlock()
	assert.False(t, resource.dbHadDL, "a zero database budget must install no deadline")
	assert.False(t, resource.msgHadDL, "a zero messaging budget must install no deadline")
	assert.False(t, resource.cacheHadDL, "a zero cache budget must install no deadline")
}

// TestPreInitCacheFailureIsNonFatal verifies that a cache pre-initialization
// failure does not abort startup. Both error shapes are exercised:
//   - a non-NotConfigured error hits the WARN ("non-fatal") branch
//   - a NotConfigured error hits the silent skip (Debug) branch
//
// In both cases NewWithConfig must still succeed, proving cache pre-init is
// best-effort while database/messaging remain startup-fatal.
func TestPreInitCacheFailureIsNonFatal(t *testing.T) {
	cases := []struct {
		name      string
		cacheErr  error
		wantCalls bool
	}{
		{
			name:      "non_configured_error_is_skipped_silently",
			cacheErr:  config.NewNotConfiguredError("cache", "CACHE_REDIS_HOST", "cache.redis.host"),
			wantCalls: true,
		},
		{
			name:      "other_error_is_logged_and_continues",
			cacheErr:  errors.New("dial timeout"),
			wantCalls: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := defaultTestConfig()
			cfg.App.Startup = config.StartupConfig{
				Timeout:       2 * time.Second,
				Database:      2 * time.Second,
				Messaging:     2 * time.Second,
				Cache:         2 * time.Second,
				Observability: 2 * time.Second,
			}

			var cacheCalled bool
			opts := &Options{
				ResourceSource: &deadlineCapturingResource{},
				DatabaseConnector: func(*config.DatabaseConfig, logger.Logger) (database.Interface, error) {
					return &testmocks.MockDatabase{}, nil
				},
				MessagingClientFactory: func(string, logger.Logger) messaging.AMQPClient {
					return testmocks.NewMockAMQPClient()
				},
				CacheConnector: func(context.Context, string) (cache.Cache, error) {
					cacheCalled = true
					return nil, tc.cacheErr
				},
			}

			app, _, err := NewWithConfig(cfg, opts)
			require.NoError(t, err, "cache pre-init failure must not abort startup")
			require.NotNil(t, app)
			assert.Equal(t, tc.wantCalls, cacheCalled, "cache connector should be invoked during pre-init")
		})
	}
}

// TestPreInitCacheSkipsAbsentCache pins that preInitCache never leases when the cache is
// absent under the fixed "" key, so the pool's errors counter starts at a true zero (see
// rootCacheAbsent). The verdict is computed from the Builder's own config and options —
// App no longer carries a precomputed copy that could drift from them.
func TestPreInitCacheSkipsAbsentCache(t *testing.T) {
	newConnectorCountingManager := func(t *testing.T, calls *atomic.Int32) *cache.CacheManager {
		t.Helper()
		mgr := createTestCacheManagerWithConnector(t, func(context.Context, string) (cache.Cache, error) {
			calls.Add(1)
			return nil, config.NewNotConfiguredError("cache", "CACHE_REDIS_HOST", "cache.redis.host")
		})
		t.Cleanup(func() { assert.NoError(t, mgr.Close()) })
		return mgr
	}

	t.Run("absent_skips_the_connector", func(t *testing.T) {
		var connectorCalls atomic.Int32
		builder := &Builder{
			cfg:    &config.Config{},
			logger: logger.New("error", false),
			bundle: &dependencyBundle{cacheManager: newConnectorCountingManager(t, &connectorCalls)},
		}
		require.True(t, rootCacheAbsent(builder.cfg, builder.opts), "the fixture must model an absent cache")

		builder.preInitCache(context.Background(), time.Second)

		assert.Equal(t, int32(0), connectorCalls.Load(), "the connector must never be reached")
	})

	t.Run("present_reaches_the_connector", func(t *testing.T) {
		var connectorCalls atomic.Int32
		builder := &Builder{
			cfg:    &config.Config{Cache: config.CacheConfig{Enabled: true}},
			logger: logger.New("error", false),
			bundle: &dependencyBundle{cacheManager: newConnectorCountingManager(t, &connectorCalls)},
		}
		require.False(t, rootCacheAbsent(builder.cfg, builder.opts), "the fixture must model a present cache")

		builder.preInitCache(context.Background(), time.Second)

		assert.Equal(t, int32(1), connectorCalls.Load(), "an unexempt cache must still be probed")
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
