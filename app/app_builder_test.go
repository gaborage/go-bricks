package app

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"sync"
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
	assert.NoError(t, builder.err)
}

func TestAppBuilderWithConfig(t *testing.T) {
	t.Run("valid config and options", func(t *testing.T) {
		cfg := defaultTestConfig()
		opts := &Options{}

		builder := NewAppBuilder().WithConfig(cfg, opts)
		assert.NotNil(t, builder)
		assert.Equal(t, cfg, builder.cfg)
		assert.Equal(t, opts, builder.opts)
		assert.NoError(t, builder.err)
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

		require.ErrorContains(t, result.err, "configuration required before creating logger")
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

		require.NoError(t, result.err)
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
			require.NoError(t, result.err)
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
			ErrorRedactor:   func(error) string { return "[redacted]" },
		}
		got := resolveLoggerFilterConfig(
			&Options{LoggerFilterConfig: custom},
			&config.LogConfig{SensitiveFields: []string{"cvv2"}},
		)
		require.NotNil(t, got)
		assert.Same(t, custom, got, "options config should be returned verbatim")
		assert.Equal(t, []string{"pan"}, got.SensitiveFields)
		assert.Equal(t, "XXX", got.MaskValue)
		require.NotNil(t, got.ErrorRedactor)
		assert.Equal(t, "[redacted]", got.ErrorRedactor(assert.AnError))
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
		assert.Len(t, got.SensitiveFields, len(defaults)+3)
	})

	t.Run("yaml_merge_path_leaves_error_redactor_nil", func(t *testing.T) {
		// The redactor is a function, so it has no YAML door: the merge path
		// must hand the logger a config whose redactor is nil, keeping Err
		// output unchanged for every consumer who never opted in from code.
		got := resolveLoggerFilterConfig(nil, &config.LogConfig{SensitiveFields: []string{"pan"}})
		require.NotNil(t, got)
		assert.Nil(t, got.ErrorRedactor)
	})

	t.Run("empty_config_sensitive_fields_returns_nil", func(t *testing.T) {
		// An empty slice is treated the same as no override — caller falls
		// through to DefaultFilterConfig via NewWithFilter(nil).
		got := resolveLoggerFilterConfig(nil, &config.LogConfig{SensitiveFields: []string{}})
		assert.Nil(t, got)
	})

	t.Run("options_present_but_filter_nil_uses_config", func(t *testing.T) {
		// A populated Options struct that doesn't set LoggerFilterConfig must not
		// short-circuit the config path — typical for apps that configure a
		// messaging factory via Options and masking via YAML.
		got := resolveLoggerFilterConfig(
			&Options{ // LoggerFilterConfig left zero
				MessagingClientFactory: func(string, logger.Logger) messaging.AMQPClient { return nil },
			},
			&config.LogConfig{SensitiveFields: []string{"pan"}},
		)
		require.NotNil(t, got)
		assert.Contains(t, got.SensitiveFields, "pan")
		// Defaults still merged in.
		assert.Contains(t, got.SensitiveFields, "password")
	})

	t.Run("config_entries_are_appended_verbatim", func(t *testing.T) {
		// This function only MERGES. Trimming, dropping empties and dedup happen
		// in logger.NewSensitiveDataFilter, so they apply to the Options
		// replace-door too — which bypasses this function entirely. What is
		// pinned here is that nothing is lost on the way: every default survives
		// and every configured entry arrives.
		defaults := logger.DefaultFilterConfig().SensitiveFields
		entries := []string{"pan", "", "   ", "  ssn  ", "PAN", "password"}
		got := resolveLoggerFilterConfig(nil, &config.LogConfig{SensitiveFields: entries})
		require.NotNil(t, got)

		for _, defaultField := range defaults {
			assert.Contains(t, got.SensitiveFields, defaultField, "default field %q must survive merge", defaultField)
		}
		assert.Equal(t, append(append([]string{}, defaults...), entries...), got.SensitiveFields)
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
		require.NoError(t, result.err)
		assert.NotNil(t, result.logger)
	})

	t.Run("config_sensitive_fields_accepted", func(t *testing.T) {
		cfg := defaultTestConfig()
		cfg.Log.SensitiveFields = []string{"pan", "cvv2", "otp"}
		result := NewAppBuilder().WithConfig(cfg, &Options{}).CreateLogger()
		require.NoError(t, result.err)
		assert.NotNil(t, result.logger)
	})

	t.Run("yaml_filter_with_empty_needle_does_not_mask_everything", func(t *testing.T) {
		// The other config door. This one used to be normalized by a loop in
		// resolveLoggerFilterConfig; that loop is gone and the rule now lives in
		// the filter constructor, so this asserts the property survived the move
		// rather than merely that the merge still happens.
		cfg := defaultTestConfig()
		cfg.Log.SensitiveFields = []string{"pan", "", "   "}

		var buildErr error
		output := captureAppStdout(t, func() {
			result := NewAppBuilder().WithConfig(cfg, &Options{}).CreateLogger()
			if buildErr = result.err; buildErr != nil {
				return
			}
			result.logger.Info().Interface("body", map[string]any{
				"name": "john",
				"pan":  "4111111111111111",
			}).Msg("payload")
		})
		require.NoError(t, buildErr)

		assert.Contains(t, output, `"name":"john"`, "a non-sensitive field must survive")
		assert.Contains(t, output, `"pan":"***"`, "the named needle must still mask")
	})

	t.Run("options_filter_with_empty_needle_does_not_mask_everything", func(t *testing.T) {
		// The replace-door hands a FilterConfig straight to the logger, so an
		// empty needle in it reaches the matcher. strings.Contains is true
		// against "" for every field name, which masks the whole log stream —
		// a config typo turning into total loss of log content.
		cfg := defaultTestConfig()
		opts := &Options{
			LoggerFilterConfig: &logger.FilterConfig{
				SensitiveFields: []string{"pan", ""},
				MaskValue:       "***",
			},
		}
		var buildErr error
		output := captureAppStdout(t, func() {
			result := NewAppBuilder().WithConfig(cfg, opts).CreateLogger()
			if buildErr = result.err; buildErr != nil {
				return
			}
			result.logger.Info().Interface("body", map[string]any{
				"name": "john",
				"pan":  "4111111111111111",
			}).Msg("payload")
		})
		require.NoError(t, buildErr)

		require.Contains(t, output, `"body":`)
		assert.Contains(t, output, `"name":"john"`, "a non-sensitive field must survive")
		assert.Contains(t, output, `"pan":"***"`, "the named needle must still mask")
	})
}

func TestAppBuilderErrorRedactorReachesFrameworkErrSites(t *testing.T) {
	// End-to-end through the builder: a redactor set on the code door must
	// govern an Err(err) call the framework itself makes, which is the whole
	// point of the seam — a consumer-side scrub helper never reaches those.
	const badIP = "not-an-ip"

	cfg := defaultTestConfig()
	opts := &Options{LoggerFilterConfig: redactingFilterConfig()}

	var buildErr error
	output := captureAppStdout(t, func() {
		result := NewAppBuilder().WithConfig(cfg, opts).CreateLogger()
		if buildErr = result.err; buildErr != nil {
			return
		}
		NewIPWhitelist([]string{badIP}, result.logger)
	})
	require.NoError(t, buildErr)

	assert.Contains(t, output, `"error":"[redacted]"`)
	assert.NotContains(t, output, "invalid CIDR address", "the raw error message must not reach the sink")
}

// redactingFilterConfig is the documented code-door shape: start from the
// defaults, then set the redactor.
func redactingFilterConfig() *logger.FilterConfig {
	base := logger.DefaultFilterConfig()
	base.ErrorRedactor = func(error) string { return "[redacted]" }
	return base
}

// captureAppStdout redirects os.Stdout for the duration of fn and returns
// everything written to it. The framework logger writes there directly, so this
// is how a built logger's output is observed.
func captureAppStdout(t *testing.T, fn func()) string {
	t.Helper()
	original := os.Stdout
	r, w, err := os.Pipe()
	require.NoError(t, err)
	// Restored via defer: a require failure inside fn calls Goexit, and without
	// this every later test in the package would write into a dangling pipe.
	defer func() { os.Stdout = original }()
	defer r.Close()
	os.Stdout = w

	fn()

	require.NoError(t, w.Close())

	var buf bytes.Buffer
	_, err = io.Copy(&buf, r)
	require.NoError(t, err)
	return buf.String()
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

		require.ErrorContains(t, result.err, "logger required before creating bootstrap")
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

		require.ErrorContains(t, result.err, "bootstrap required before resolving dependencies")
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
			assert.ErrorContains(t, builder.err, tt.wantCause) //nolint:testifylint // require would abort before the Build-propagation assertions

			app, log, err := builder.CreateApp().Build()
			require.Error(t, err, "startup must abort, not continue with a nil cache manager")
			assert.Nil(t, app)
			assert.NotNil(t, log)
		})
	}
}

// TestAppBuilderResolveDependenciesToleratesNegativeCleanupIntervalOnDisabledCache pins the
// exception wiki/cache.md documents beside the two fatal keys above: config.Validate leaves
// cache.manager.* alone for a disabled cache, so a negative cleanupinterval reaches the
// factory unvalidated — and the build succeeds, because NewCacheManager takes its default.
func TestAppBuilderResolveDependenciesToleratesNegativeCleanupIntervalOnDisabledCache(t *testing.T) {
	cfg := defaultTestConfig()
	require.False(t, cfg.Cache.Enabled, "the fixture must leave the cache disabled")
	cfg.Cache.Manager.CleanupInterval = -time.Second

	builder := NewAppBuilder().WithConfig(cfg, &Options{}).CreateLogger().CreateBootstrap().ResolveDependencies()

	require.NoError(t, builder.err)
	require.NotNil(t, builder.bundle)
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
	assert.ErrorContains(t, err, "cache manager") //nolint:testifylint // second clause of the same wrapped error follows
	require.ErrorContains(t, err, "maxsize cannot be negative")
}

// TestBuildClosesBundleManagersWhenALaterStepAborts pins the Builder half of the ADR-067 leak
// fix: an error raised after ResolveDependencies leaves all three managers built — each owning
// an idle-cleanup goroutine — and returns no App, so Build is the last hand able to close them.
// Driven through the real chain by the fatal database pre-initialization verdict; the bundle is
// read off the builder because Build deliberately hands the caller nothing on this path.
func TestBuildClosesBundleManagersWhenALaterStepAborts(t *testing.T) {
	cfg := defaultTestConfig()
	opts := &Options{
		DatabaseConnector: func(*config.DatabaseConfig, logger.Logger) (database.Interface, error) {
			return nil, errors.New("dial tcp 127.0.0.1:5432: connect: connection refused")
		},
		MessagingClientFactory: func(string, logger.Logger) messaging.AMQPClient {
			return testmocks.NewMockAMQPClient()
		},
		CacheConnector: func(context.Context, string) (cache.Cache, error) {
			return cachetesting.NewMockCache(), nil
		},
	}

	builder := NewAppBuilder()
	app, log, err := builder.
		WithConfig(cfg, opts).
		CreateLogger().
		CreateBootstrap().
		ResolveDependencies().
		CreateApp().
		InitializeRegistry().
		ConfigureRuntimeHelpers().
		CreateHealthProbes().
		RegisterClosers().
		RegisterReadyHandler().
		Build()

	require.Error(t, err, "an unreachable database must abort startup at pre-initialization")
	assert.Nil(t, app)
	assert.NotNil(t, log)
	assert.ErrorContains(t, err, "connection failed during startup") //nolint:testifylint // require would abort before the manager-close assertions

	bundle := builder.bundle
	require.NotNil(t, bundle, "the abort must come after the bundle was built, or this pins nothing")

	// Close is the only externally-observable side effect the managers expose: each fails closed
	// once it has run, and only once — an unclosed manager answers these calls.
	ctx := context.Background()
	_, _, dbErr := bundle.dbManager.Get(ctx, "")
	require.Error(t, dbErr)
	assert.Contains(t, dbErr.Error(), "manager closed", "the database manager must be closed, not stranded")

	_, _, msgErr := bundle.messagingManager.Publisher(ctx, "")
	require.ErrorIs(t, msgErr, messaging.ErrManagerClosed, "the messaging manager must be closed, not stranded")

	_, _, cacheErr := bundle.cacheManager.Get(ctx, "")
	require.ErrorIs(t, cacheErr, cache.ErrManagerClosed, "the cache manager must be closed, not stranded")
}

func TestAppBuilderCreateAppErrors(t *testing.T) {
	t.Run("missing dependencies", func(t *testing.T) {
		builder := NewAppBuilder()
		result := builder.CreateApp()

		require.ErrorContains(t, result.err, "dependencies required before creating app")
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

		require.ErrorContains(t, result.err, "app instance required before initializing registry")
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

		require.ErrorContains(t, result.err, "app instance required before configuring runtime helpers")
	})

	t.Run(shouldSkipWithPreviousError, func(t *testing.T) {
		builder := &Builder{err: assert.AnError}
		result := builder.ConfigureRuntimeHelpers()
		assert.Equal(t, assert.AnError, result.err)
	})
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
	app := &App{}
	app.installSlots(slotInputs{})
	builder := &Builder{cfg: cfg, logger: logger.New("error", false), app: app, bundle: &dependencyBundle{}}
	result := builder.ConfigureRuntimeHelpers()

	require.NoError(t, result.err)
}

func TestAppBuilderCreateHealthProbesErrors(t *testing.T) {
	t.Run(missingAppInstanceErrorMsg, func(t *testing.T) {
		builder := NewAppBuilder()
		result := builder.CreateHealthProbes()

		require.ErrorContains(t, result.err, "app instance required before creating health probes")
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
		{name: "critical_enabled", cfg: &config.Config{Cache: config.CacheConfig{Critical: true}}, expectedCritical: true},
		{name: "critical_explicit_false", cfg: &config.Config{Cache: config.CacheConfig{Critical: false}}, expectedCritical: false},
		{name: "critical_unset_is_non_critical", cfg: &config.Config{}, expectedCritical: false},
		{name: "nil_config", cfg: nil, expectedCritical: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			app := &App{cfg: tc.cfg, cacheManager: createTestCacheManager(t)}
			// CreateApp installs the slots CreateHealthProbes walks; this builder skips it.
			app.installSlots(slotInputs{})
			builder := &Builder{
				logger: logger.New("error", false),
				app:    app,
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

// TestAppBuilderExplicitFalseCacheCriticalIsSilent pins ADR-094's second half: an explicit
// cache.critical=false is a decision, not a smell, so the two shapes whose probe can fail —
// an enabled cache, and a custom CacheConnector, which never reads cache.enabled — boot with
// no readiness-posture WARN. The recorder is swept for the KEY rather than for one message,
// so a re-worded WARN cannot slip back in.
func TestAppBuilderExplicitFalseCacheCriticalIsSilent(t *testing.T) {
	customConnector := &Options{CacheConnector: func(context.Context, string) (cache.Cache, error) {
		return nil, assert.AnError
	}}
	// CreateHealthProbes wires the manager into the probe set without leasing from it, so
	// one instance serves both cases.
	cacheManager := createTestCacheManager(t)

	tests := []struct {
		name  string
		cache config.CacheConfig
		opts  *Options
	}{
		{name: "enabled_cache", cache: config.CacheConfig{Enabled: true, Critical: false}},
		{name: "custom_connector", cache: config.CacheConfig{Critical: false}, opts: customConnector},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := &recLogger{}
			app := &App{cfg: &config.Config{Cache: tc.cache}, cacheManager: cacheManager}
			// CreateApp installs the slots CreateHealthProbes walks; this builder skips it.
			app.installSlots(slotInputs{})
			builder := &Builder{logger: rec, opts: tc.opts, app: app}
			require.NoError(t, builder.CreateHealthProbes().err)

			assert.False(t, loggedMsgContains(rec, "cache.critical"), "no readiness-posture line may mention the key")
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
			app := &App{cfg: cfg}
			// CreateApp installs the slots CreateHealthProbes walks; this builder skips it.
			app.installSlots(slotInputs{})
			builder := &Builder{
				logger: rec,
				app:    app,
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

		require.ErrorContains(t, result.err, "app instance required before registering closers")
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

		require.ErrorContains(t, result.err, "app instance required before registering ready handler")
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

		assert.Equal(t, assert.AnError, err)
		assert.Nil(t, app)
		assert.NotNil(t, log) // Logger should always be available
	})

	t.Run("incomplete build without app", func(t *testing.T) {
		builder := NewAppBuilder()
		app, log, err := builder.Build()

		require.ErrorContains(t, err, "app building incomplete")
		assert.Nil(t, app)
		assert.NotNil(t, log) // Logger should always be available
	})
}

func TestAppBuilderError(t *testing.T) {
	t.Run("no error", func(t *testing.T) {
		builder := NewAppBuilder()
		err := builder.Error()
		assert.NoError(t, err)
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

		require.ErrorContains(t, result.err, "configuration required")
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

func TestAppBuilderErrorRecovery(t *testing.T) {
	t.Run("builder state remains consistent after error", func(t *testing.T) {
		builder := NewAppBuilder()

		// Trigger an error
		builder.CreateLogger() // Will fail due to missing config
		require.Error(t, builder.err)

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

// TestPerformPreInitializationStopsAtTheFirstFatalKind pins that a fatal pre-init aborts
// the walk: the messaging and cache slots that follow the database must not be reached, and
// the error must carry the failing kind's name.
func TestPerformPreInitializationStopsAtTheFirstFatalKind(t *testing.T) {
	order := []string{}
	builder := &Builder{
		cfg:    defaultTestConfig(),
		logger: logger.New("error", false),
		app:    &App{cfg: defaultTestConfig(), logger: logger.New("error", false)},
	}
	builder.app.slots = []resourceSlot{
		&recordingSlot{kind: componentDatabase, order: &order, fatalPreInit: true, preInitErr: assert.AnError},
		&recordingSlot{kind: componentMessaging, order: &order},
		&recordingSlot{kind: componentCache, order: &order},
	}

	builder.performPreInitialization()

	require.Error(t, builder.err)
	assert.Contains(t, builder.err.Error(), "database connection failed during startup")
	assert.Equal(t, []string{"preinit:database"}, order,
		"a fatal pre-init must stop the walk before the next kind")
}

// TestPerformPreInitializationContinuesPastABestEffortKind is the other half: a non-fatal
// failure is logged and the walk carries on.
func TestPerformPreInitializationContinuesPastABestEffortKind(t *testing.T) {
	order := []string{}
	rec := &recLogger{}
	builder := &Builder{
		cfg:    defaultTestConfig(),
		logger: rec,
		app:    &App{cfg: defaultTestConfig(), logger: rec},
	}
	builder.app.slots = []resourceSlot{
		&recordingSlot{kind: componentCache, order: &order, preInitErr: assert.AnError},
		&recordingSlot{kind: componentStreams, order: &order},
	}

	builder.performPreInitialization()

	require.NoError(t, builder.err)
	assert.Equal(t, []string{"preinit:cache", "preinit:streams"}, order)
	event, emitted := loggedEvent(rec, "Failed to pre-initialize cache connection (non-fatal)")
	require.True(t, emitted, "a best-effort failure must still be visible")
	assert.Equal(t, "warn", event.level)
}

// TestPerformPreInitializationSkipsWhenAppCarriesNoConfig pins the nil-config guard: the
// slots read cfg for their configured/budget answers, so a Builder whose App never received
// a config (WithConfig never ran) must skip the walk instead of reaching a slot that would
// dereference nil.
func TestPerformPreInitializationSkipsWhenAppCarriesNoConfig(t *testing.T) {
	order := []string{}
	builder := &Builder{
		logger: logger.New("error", false),
		app:    &App{logger: logger.New("error", false)},
	}
	builder.app.slots = []resourceSlot{
		&recordingSlot{kind: componentDatabase, order: &order},
	}

	builder.performPreInitialization()

	require.NoError(t, builder.err)
	assert.Empty(t, order, "no slot pre-init must run without a config")
}

// TestAppBuilderStepsRequireInstalledSlots pins that the two steps walking App.slots fail
// fast when CreateApp never installed them: an empty walk would register no probe at all and
// leave /ready answering an unconditional 200.
func TestAppBuilderStepsRequireInstalledSlots(t *testing.T) {
	cases := []struct {
		step    func(*Builder) *Builder
		name    string
		wantMsg string
	}{
		{
			name:    "create_health_probes",
			step:    (*Builder).CreateHealthProbes,
			wantMsg: "slots not installed before creating health probes",
		},
		{
			name:    "register_closers",
			step:    (*Builder).RegisterClosers,
			wantMsg: "slots not installed before registering closers",
		},
		{
			name:    "pre_initialization",
			step:    func(b *Builder) *Builder { b.performPreInitialization(); return b },
			wantMsg: "slots not installed before pre-initialization",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			builder := &Builder{
				logger: logger.New("error", false),
				app:    &App{cfg: defaultTestConfig(), cacheManager: createTestCacheManager(t)},
			}

			result := tc.step(builder)

			require.Error(t, result.err)
			assert.Contains(t, result.err.Error(), tc.wantMsg)
			assert.Empty(t, result.app.healthProbes, "a refused step must register nothing")
			assert.Empty(t, result.app.closers)
		})
	}
}
