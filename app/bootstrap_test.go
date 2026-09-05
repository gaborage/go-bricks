package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/metric"
	metricnoop "go.opentelemetry.io/otel/metric/noop"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	"go.opentelemetry.io/otel/trace"
	tracenoop "go.opentelemetry.io/otel/trace/noop"

	"github.com/gaborage/go-bricks/cache"
	"github.com/gaborage/go-bricks/config"
	"github.com/gaborage/go-bricks/database"
	"github.com/gaborage/go-bricks/logger"
	"github.com/gaborage/go-bricks/messaging"
	"github.com/gaborage/go-bricks/observability"
	testmocks "github.com/gaborage/go-bricks/testing/mocks"
)

const (
	testConfigFile              = "config.yaml"
	bootstrapLoggerUnchangedMsg = "Bootstrap logger should remain unchanged"
)

type testObservabilityProvider struct {
	loggerProvider *sdklog.LoggerProvider
	disableStdout  bool
	shutdownErr    error
}

func (m *testObservabilityProvider) TracerProvider() trace.TracerProvider {
	return tracenoop.NewTracerProvider()
}

func (m *testObservabilityProvider) MeterProvider() metric.MeterProvider {
	return metricnoop.NewMeterProvider()
}

func (m *testObservabilityProvider) LoggerProvider() *sdklog.LoggerProvider {
	return m.loggerProvider
}

func (m *testObservabilityProvider) ShouldDisableStdout() bool {
	return m.disableStdout
}

func (m *testObservabilityProvider) Shutdown(context.Context) error {
	return m.shutdownErr
}

func (m *testObservabilityProvider) ForceFlush(context.Context) error {
	return nil
}

func TestEnhanceLoggerWithOTelReplacesBootstrapLogger(t *testing.T) {
	bootstrap := &appBootstrap{
		log: logger.New("info", false),
	}
	original := bootstrap.log

	provider := &testObservabilityProvider{
		loggerProvider: sdklog.NewLoggerProvider(),
	}
	t.Cleanup(func() {
		// Shutdown the logger provider to prevent resource leaks
		err := provider.loggerProvider.Shutdown(context.Background())
		require.NoError(t, err, "LoggerProvider shutdown should succeed")
	})

	enhanced := bootstrap.enhanceLoggerWithOTel(provider)

	assert.NotNil(t, enhanced)
	assert.Same(t, enhanced, bootstrap.log)
	assert.NotSame(t, original, enhanced)
}

// TestEnhanceLoggerWithOTelNilProvider verifies that a nil provider
// returns the original logger without enhancement.
func TestEnhanceLoggerWithOTelNilProvider(t *testing.T) {
	bootstrap := &appBootstrap{
		log: logger.New("info", false),
	}
	original := bootstrap.log

	// Call with nil provider
	result := bootstrap.enhanceLoggerWithOTel(nil)

	// Should return original logger unchanged
	assert.NotNil(t, result)
	assert.Same(t, original, result, "Should return original logger when provider is nil")
	assert.Same(t, original, bootstrap.log, bootstrapLoggerUnchangedMsg)
}

// TestEnhanceLoggerWithOTelNilLoggerProvider verifies that a provider
// with nil LoggerProvider returns the original logger.
func TestEnhanceLoggerWithOTelNilLoggerProvider(t *testing.T) {
	bootstrap := &appBootstrap{
		log: logger.New("info", false),
	}
	original := bootstrap.log

	// Create provider with nil LoggerProvider
	provider := &testObservabilityProvider{
		loggerProvider: nil, // OTLP log export disabled
	}

	result := bootstrap.enhanceLoggerWithOTel(provider)

	// Should return original logger unchanged
	assert.NotNil(t, result)
	assert.Same(t, original, result, "Should return original logger when LoggerProvider is nil")
	assert.Same(t, original, bootstrap.log, bootstrapLoggerUnchangedMsg)
}

// mockLogger is a mock implementation of logger.Logger for testing
// the non-ZeroLogger code path.
type mockLogger struct {
	debugCalled bool
	warnCalled  bool
}

func (m *mockLogger) Debug() logger.LogEvent {
	m.debugCalled = true
	return &mockLogEvent{}
}

func (m *mockLogger) Info() logger.LogEvent {
	return &mockLogEvent{}
}

func (m *mockLogger) Warn() logger.LogEvent {
	m.warnCalled = true
	return &mockLogEvent{}
}

func (m *mockLogger) Error() logger.LogEvent {
	return &mockLogEvent{}
}

func (m *mockLogger) Fatal() logger.LogEvent {
	return &mockLogEvent{}
}

func (m *mockLogger) WithContext(any) logger.Logger {
	return m
}

func (m *mockLogger) WithFields(map[string]any) logger.Logger {
	return m
}

// mockLogEvent is a mock implementation of logger.LogEvent
type mockLogEvent struct{}

func (e *mockLogEvent) Msg(string) {
	// No-op
}

func (e *mockLogEvent) Msgf(string, ...any) {
	// No-op
}
func (e *mockLogEvent) Err(error) logger.LogEvent                 { return e }
func (e *mockLogEvent) Str(string, string) logger.LogEvent        { return e }
func (e *mockLogEvent) Int(string, int) logger.LogEvent           { return e }
func (e *mockLogEvent) Int64(string, int64) logger.LogEvent       { return e }
func (e *mockLogEvent) Uint64(string, uint64) logger.LogEvent     { return e }
func (e *mockLogEvent) Dur(string, time.Duration) logger.LogEvent { return e }
func (e *mockLogEvent) Interface(string, any) logger.LogEvent     { return e }
func (e *mockLogEvent) Bytes(string, []byte) logger.LogEvent      { return e }
func (e *mockLogEvent) Bool(string, bool) logger.LogEvent         { return e }
func (e *mockLogEvent) Enabled() bool                             { return true }

// TestEnhanceLoggerWithOTelNonZeroLogger verifies that when the logger
// is not a ZeroLogger instance, it logs a warning and returns the original logger.
func TestEnhanceLoggerWithOTelNonZeroLogger(t *testing.T) {
	mockLog := &mockLogger{}
	bootstrap := &appBootstrap{
		log: mockLog,
	}

	provider := &testObservabilityProvider{
		loggerProvider: sdklog.NewLoggerProvider(),
	}
	t.Cleanup(func() {
		// Shutdown the logger provider to prevent resource leaks
		err := provider.loggerProvider.Shutdown(context.Background())
		require.NoError(t, err, "LoggerProvider shutdown should succeed")
	})

	result := bootstrap.enhanceLoggerWithOTel(provider)

	// Should return original logger unchanged
	assert.Same(t, mockLog, result, "Should return original logger for non-ZeroLogger")
	assert.Same(t, mockLog, bootstrap.log, bootstrapLoggerUnchangedMsg)

	// Verify that a warning was logged
	assert.True(t, mockLog.warnCalled, "Should log a warning when logger is not a ZeroLogger")
}

// clearTestEnvironmentVariables clears environment variables that could interfere with config loading.
// This is necessary because environment variables have the highest priority in the config loader.
func clearTestEnvironmentVariables(t *testing.T) {
	t.Helper()
	// Clear DEBUG variable that conflicts with debug config struct
	originalDebug := os.Getenv("DEBUG")
	os.Unsetenv("DEBUG")
	t.Cleanup(func() {
		if originalDebug != "" {
			os.Setenv("DEBUG", originalDebug)
		}
	})
}

// TestBootstrapObservabilityIntegration tests the complete bootstrap flow
// with observability configuration loaded from a YAML file.
// This is an integration test that validates the end-to-end config loading.
func TestBootstrapObservabilityIntegration(t *testing.T) {
	// Clear environment variables that could interfere with config loading
	clearTestEnvironmentVariables(t)

	// Create a test YAML config file with observability enabled
	yamlContent := `
app:
  name: test-app
  version: 1.0.0
  env: development

server:
  host: localhost
  port: 8080

database:
  type: postgresql
  host: localhost
  port: 5432
  database: testdb
  username: testuser
  password: testpass

log:
  level: info
  pretty: false

debug:
  enabled: false

observability:
  enabled: true
  service:
    name: "integration-test-service"
    version: "1.0.0"
  environment: "test"
  trace:
    enabled: true
    endpoint: "stdout"
    protocol: "http"
    sample:
      rate: 1.0
    batch:
      timeout: 5s
      size: 512
    export:
      timeout: 30s
    max:
      queue:
        size: 2048
      batch:
        size: 512
  metrics:
    enabled: true
    endpoint: "stdout"
    interval: 10s
    export:
      timeout: 30s
`

	// Create temporary directory and config file
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, testConfigFile)
	err := os.WriteFile(configPath, []byte(yamlContent), 0o600)
	require.NoError(t, err)

	// Change to temp directory to load config
	originalDir, err := os.Getwd()
	require.NoError(t, err)
	defer func() {
		cerr := os.Chdir(originalDir)
		require.NoError(t, cerr)
	}()

	err = os.Chdir(tmpDir)
	require.NoError(t, err)

	// Load configuration
	cfg, err := config.Load()
	require.NoError(t, err)

	// Create logger
	log := logger.New("info", false)

	// Create bootstrap helper
	bootstrap := newAppBootstrap(cfg, log, &Options{})

	// Initialize observability
	obsProvider, err := bootstrap.initializeObservability(context.Background())
	require.NoError(t, err)
	require.NotNil(t, obsProvider)

	// Verify tracer provider is initialized
	tracerProvider := obsProvider.TracerProvider()
	require.NotNil(t, tracerProvider)

	// Create a test span to verify the provider works
	tracer := tracerProvider.Tracer("test")
	ctx, span := tracer.Start(context.Background(), "test-span")
	assert.NotNil(t, span)
	span.End()

	// Verify meter provider is initialized
	meterProvider := obsProvider.MeterProvider()
	require.NotNil(t, meterProvider)

	// Create a test metric to verify the provider works
	meter := meterProvider.Meter("test")
	counter, err := meter.Int64Counter("test.counter")
	require.NoError(t, err)
	counter.Add(ctx, 1)

	// Cleanup
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	err = obsProvider.Shutdown(shutdownCtx)
	assert.NoError(t, err)
}

// TestObservabilityConfigFromEnvironment tests that environment variables
// can override YAML configuration values in the bootstrap flow.
func TestObservabilityConfigFromEnvironment(t *testing.T) {
	// Clear environment variables that could interfere with config loading
	clearTestEnvironmentVariables(t)

	// Create a test YAML config file with base observability config
	yamlContent := `
app:
  name: test-app
  version: 1.0.0
  env: development

server:
  host: localhost
  port: 8080

database:
  type: postgresql
  host: localhost
  port: 5432
  database: testdb
  username: testuser
  password: testpass

log:
  level: info
  pretty: false

debug:
  enabled: false

observability:
  enabled: true
  service:
    name: "yaml-service"
    version: "1.0.0"
  trace:
    sample:
      rate: 0.5
`

	// Create temporary directory and config file
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, testConfigFile)
	err := os.WriteFile(configPath, []byte(yamlContent), 0o600)
	require.NoError(t, err)

	// Set environment variables to override config
	t.Setenv("OBSERVABILITY_SERVICE_NAME", "env-override-service")
	t.Setenv("OBSERVABILITY_SERVICE_VERSION", "2.0.0")
	t.Setenv("OBSERVABILITY_TRACE_SAMPLE_RATE", "0.9")

	// Change to temp directory to load config
	originalDir, err := os.Getwd()
	require.NoError(t, err)
	defer func() {
		cerr := os.Chdir(originalDir)
		require.NoError(t, cerr)
	}()

	err = os.Chdir(tmpDir)
	require.NoError(t, err)

	// Load configuration
	cfg, err := config.Load()
	require.NoError(t, err)

	// Create logger
	log := logger.New("info", false)

	// Create bootstrap helper
	bootstrap := newAppBootstrap(cfg, log, &Options{})

	// Initialize observability
	obsProvider, err := bootstrap.initializeObservability(context.Background())
	require.NoError(t, err)
	require.NotNil(t, obsProvider)

	// Verify provider is functional (indicates config was loaded successfully)
	tracerProvider := obsProvider.TracerProvider()
	require.NotNil(t, tracerProvider)

	// Cleanup
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	err = obsProvider.Shutdown(ctx)
	assert.NoError(t, err)

	// Note: We can't directly inspect the provider's internal config,
	// but the fact that it initializes successfully proves the environment
	// variables were loaded. The unit tests verify the actual override behavior.
}

// TestBootstrapObservabilityDisabled tests that the bootstrap flow
// handles disabled observability gracefully.
func TestBootstrapObservabilityDisabled(t *testing.T) {
	// Clear environment variables that could interfere with config loading
	clearTestEnvironmentVariables(t)

	// Create a test YAML config file with observability disabled
	yamlContent := `
app:
  name: test-app
  version: 1.0.0
  env: development

server:
  host: localhost
  port: 8080

database:
  type: postgresql
  host: localhost
  port: 5432
  database: testdb
  username: testuser
  password: testpass

log:
  level: info
  pretty: false

debug:
  enabled: false

observability:
  enabled: false
`

	// Create temporary directory and config file
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, testConfigFile)
	err := os.WriteFile(configPath, []byte(yamlContent), 0o600)
	require.NoError(t, err)

	// Change to temp directory to load config
	originalDir, err := os.Getwd()
	require.NoError(t, err)
	defer func() {
		cerr := os.Chdir(originalDir)
		require.NoError(t, cerr)
	}()

	err = os.Chdir(tmpDir)
	require.NoError(t, err)

	// Load configuration
	cfg, err := config.Load()
	require.NoError(t, err)

	// Create logger
	log := logger.New("info", false)

	// Create bootstrap helper
	bootstrap := newAppBootstrap(cfg, log, &Options{})

	// Initialize observability
	obsProvider, err := bootstrap.initializeObservability(context.Background())
	require.NoError(t, err)
	require.NotNil(t, obsProvider)

	// Should return noop providers
	tracerProvider := obsProvider.TracerProvider()
	require.NotNil(t, tracerProvider)

	meterProvider := obsProvider.MeterProvider()
	require.NotNil(t, meterProvider)

	// Cleanup (should not error even with noop provider)
	err = obsProvider.Shutdown(context.Background())
	assert.NoError(t, err)
}

// TestBootstrapObservabilityMissingConfig tests that the bootstrap flow
// handles missing observability configuration gracefully.
func TestBootstrapObservabilityMissingConfig(t *testing.T) {
	// Clear environment variables that could interfere with config loading
	clearTestEnvironmentVariables(t)

	// Create a test YAML config file without observability section
	yamlContent := `
app:
  name: test-app
  version: 1.0.0

debug:
  enabled: false
`

	// Create temporary directory and config file
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, testConfigFile)
	err := os.WriteFile(configPath, []byte(yamlContent), 0o600)
	require.NoError(t, err)

	// Change to temp directory to load config
	originalDir, err := os.Getwd()
	require.NoError(t, err)
	defer func() {
		cerr := os.Chdir(originalDir)
		require.NoError(t, cerr)
	}()

	err = os.Chdir(tmpDir)
	require.NoError(t, err)

	// Load configuration
	cfg, err := config.Load()
	require.NoError(t, err)

	// Create logger
	log := logger.New("info", false)

	// Create bootstrap helper
	bootstrap := newAppBootstrap(cfg, log, &Options{})

	// Initialize observability (should fallback to noop provider)
	obsProvider, err := bootstrap.initializeObservability(context.Background())
	require.NoError(t, err)
	require.NotNil(t, obsProvider, "Should return noop provider when config is missing")

	// Should return noop providers
	tracerProvider := obsProvider.TracerProvider()
	require.NotNil(t, tracerProvider)

	meterProvider := obsProvider.MeterProvider()
	require.NotNil(t, meterProvider)

	// Cleanup (should not error)
	err = obsProvider.Shutdown(context.Background())
	assert.NoError(t, err)
}

// TestInitializeObservabilityFailsClosedOnUndecodableSection pins the distinction the
// no-op fallback used to blur. observability.* is decoded separately from config.Config,
// so a delivered-empty numeric there passes config.Load and lands here (ADR-074) — and
// swallowing it took every trace, metric, OTLP log and migration audit event with it,
// announced by one WARN. A present-but-undecodable section now aborts startup; an ABSENT
// section keeps the documented no-op posture.
func TestInitializeObservabilityFailsClosedOnUndecodableSection(t *testing.T) {
	t.Run("present_but_undecodable_aborts", func(t *testing.T) {
		cfg := loadConfigFromYAML(t, `
app:
  name: "test"
  version: "1.0.0"
server:
  port: 8080
observability:
  enabled: true
  trace:
    batch:
      size: ""
`)
		bootstrap := newAppBootstrap(cfg, logger.New("info", false), &Options{})

		provider, err := bootstrap.initializeObservability(context.Background())

		require.Error(t, err)
		assert.ErrorContains(t, err, "present but invalid")
		assert.ErrorContains(t, err, "delivered empty")
		assert.Nil(t, provider, "a caller must not receive a silently degraded provider")
	})

	t.Run("absent_section_never_reaches_construction", func(t *testing.T) {
		cfg := loadConfigFromYAML(t, `
app:
  name: "test"
  version: "1.0.0"
server:
  port: 8080
`)
		bootstrap := newAppBootstrap(cfg, logger.New("info", false), &Options{})
		constructed := false
		bootstrap.newProvider = func(context.Context, *observability.Config) (observability.Provider, error) {
			constructed = true
			return observability.MustNewProvider(&observability.Config{Enabled: false}), nil
		}

		provider, err := bootstrap.initializeObservability(context.Background())

		require.NoError(t, err)
		require.NotNil(t, provider)
		assert.False(t, constructed,
			"an absent section takes the documented no-op branch; koanf returns no error for a "+
				"missing key, so inferring absence from a decode failure never fires")
	})

	t.Run("absent_section_stays_no_op", func(t *testing.T) {
		cfg := loadConfigFromYAML(t, `
app:
  name: "test"
  version: "1.0.0"
server:
  port: 8080
`)
		bootstrap := newAppBootstrap(cfg, logger.New("info", false), &Options{})

		provider, err := bootstrap.initializeObservability(context.Background())

		require.NoError(t, err)
		require.NotNil(t, provider)
	})
}

// TestInitializeObservabilityThreadsBudgetContext verifies that when
// app.startup.observability is positive, the construction seam receives a
// non-nil context whose deadline is set roughly the budget into the future.
// This is the context-threading replacement for the old goroutine-race budget
// enforcement: the deadline is what bounds resource detection and exporter
// setup inside NewProviderWithContext.
func TestInitializeObservabilityThreadsBudgetContext(t *testing.T) {
	const budget = 7 * time.Second

	want := &testObservabilityProvider{}
	var (
		gotCtx context.Context
		gotCfg *observability.Config
	)

	cfg := loadConfigFromYAML(t, `
app:
  name: test-app
  version: 1.0.0
  env: development
  startup:
    observability: 7s
server:
  host: localhost
  port: 8080
database:
  type: postgresql
  host: localhost
  port: 5432
  database: testdb
  username: testuser
  password: testpass
log:
  level: info
observability:
  enabled: true
  service:
    name: budget-test
`)

	bootstrap := newAppBootstrap(cfg, logger.New("info", false), &Options{})
	bootstrap.newProvider = func(ctx context.Context, c *observability.Config) (observability.Provider, error) {
		gotCtx = ctx
		gotCfg = c
		return want, nil
	}

	start := time.Now()
	got, err := bootstrap.initializeObservability(context.Background())
	require.NoError(t, err)

	assert.Same(t, want, got, "the provider returned by the seam should be installed verbatim")
	require.NotNil(t, gotCtx, "seam must receive a non-nil context")
	require.NotNil(t, gotCfg, "seam must receive the observability config")

	deadline, ok := gotCtx.Deadline()
	require.True(t, ok, "seam context must carry a deadline derived from the startup budget")

	const tolerance = 2 * time.Second
	assert.InDelta(t, budget.Seconds(), deadline.Sub(start).Seconds(), tolerance.Seconds(),
		"seam context deadline must be ~app.startup.observability from now")
}

// TestInitializeObservabilityPropagatesParentContext verifies the budget context
// is derived from the supplied startup parent (not a fresh context.Background()):
// canceling the parent must cancel the context the construction seam receives.
func TestInitializeObservabilityPropagatesParentContext(t *testing.T) {
	cfg := loadConfigFromYAML(t, `
app:
  name: test-app
  version: 1.0.0
  env: development
  startup:
    observability: 7s
server:
  host: localhost
  port: 8080
database:
  type: postgresql
  host: localhost
  port: 5432
  database: testdb
  username: testuser
  password: testpass
log:
  level: info
observability:
  enabled: true
  service:
    name: parent-ctx-test
`)

	var gotCtx context.Context
	bootstrap := newAppBootstrap(cfg, logger.New("info", false), &Options{})
	bootstrap.newProvider = func(ctx context.Context, _ *observability.Config) (observability.Provider, error) {
		gotCtx = ctx
		return &testObservabilityProvider{}, nil
	}

	parent, cancel := context.WithCancel(context.Background())
	cancel() // cancel the parent BEFORE construction

	_, err := bootstrap.initializeObservability(parent)
	require.NoError(t, err)

	require.NotNil(t, gotCtx, "seam must receive a non-nil context")
	require.ErrorIs(t, gotCtx.Err(), context.Canceled,
		"budget context must inherit cancellation from the startup parent, proving it is not a fresh context.Background()")
}

// TestInitializeObservabilityConstructorErrorFallsBackToNoop verifies that when
// the construction seam returns an error, initializeObservability installs the
// no-op fallback provider (functional tracer/meter providers, nil logger
// provider) and logs a WARN rather than crashing.
func TestInitializeObservabilityConstructorErrorFallsBackToNoop(t *testing.T) {
	cfg := loadConfigFromYAML(t, `
app:
  name: test-app
  version: 1.0.0
  env: development
  startup:
    observability: 5s
server:
  host: localhost
  port: 8080
database:
  type: postgresql
  host: localhost
  port: 5432
  database: testdb
  username: testuser
  password: testpass
log:
  level: info
observability:
  enabled: true
  service:
    name: error-fallback-test
`)

	mockLog := &mockLogger{}
	bootstrap := newAppBootstrap(cfg, mockLog, &Options{})
	bootstrap.newProvider = func(context.Context, *observability.Config) (observability.Provider, error) {
		return nil, errors.New("exporter dial failed")
	}

	got, err := bootstrap.initializeObservability(context.Background())
	require.NoError(t, err)

	require.NotNil(t, got, "a no-op provider must be installed on constructor error")
	assert.NotNil(t, got.TracerProvider(), "no-op provider must expose a tracer provider")
	assert.NotNil(t, got.MeterProvider(), "no-op provider must expose a meter provider")
	assert.Nil(t, got.LoggerProvider(), "no-op provider has no OTLP logger provider")
	assert.True(t, mockLog.warnCalled, "constructor failure must be logged as a WARN")

	// The fallback provider must shut down cleanly (no-op).
	assert.NoError(t, got.Shutdown(context.Background()))
}

// TestNewManagerConfigBuilderFromConfig pins the config-to-builder seam — the one
// place a validated key can silently revert to validated-but-ignored (#662) if an
// assignment is dropped or cross-wired. Every value is distinct to catch swaps.
func TestNewManagerConfigBuilderFromConfig(t *testing.T) {
	cfg := &config.Config{
		Multitenant: config.MultitenantConfig{
			Enabled: true,
			Limits:  config.LimitsConfig{Tenants: 42},
			Tenants: map[string]config.TenantEntry{"a": {}, "b": {}, "c": {}},
		},
		Messaging: config.MessagingConfig{
			Reconnect: config.ReconnectConfig{
				ConnectionTimeout:  31 * time.Second,
				MaxPublishAttempts: 6,
				ReadyTimeout:       7 * time.Second,
				Delay:              8 * time.Second,
				MaxDelay:           91 * time.Second,
				ReinitDelay:        3 * time.Second,
				ResendDelay:        11 * time.Second,
			},
			Publisher:      config.PublisherPoolConfig{MaxCached: 12, IdleTTL: 13 * time.Minute},
			PublishTimeout: 41 * time.Second,
		},
		Cache:    config.CacheConfig{Manager: config.CacheManagerConfig{MaxSize: 14}},
		Database: config.DatabaseConfig{Manager: config.DatabaseManagerConfig{MaxSize: 15}},
	}

	b := newManagerConfigBuilderFromConfig(cfg)

	assert.True(t, b.multiTenantEnabled)
	assert.Equal(t, 42, b.tenantLimit)
	assert.Equal(t, 3, b.staticTenantCount)
	assert.Equal(t, 31*time.Second, b.connectionTimeout)
	assert.Equal(t, 6, b.maxPublishAttempts)
	assert.Equal(t, 7*time.Second, b.readyTimeout)
	assert.Equal(t, 41*time.Second, b.publishTimeout)
	assert.Equal(t, 8*time.Second, b.reconnectDelay)
	assert.Equal(t, 91*time.Second, b.reconnectMaxDelay)
	assert.Equal(t, 3*time.Second, b.reInitDelay)
	assert.Equal(t, 11*time.Second, b.resendDelay)
	assert.Equal(t, cfg.Messaging.Publisher, b.publisherConfig)
	assert.Equal(t, cfg.Cache.Manager, b.cacheConfig)
	assert.Equal(t, cfg.Database.Manager, b.dbConfig)

	// Single-tenant: leftover tenants entries must not count (StaticTenantCount contract).
	cfg.Multitenant.Enabled = false
	assert.Zero(t, newManagerConfigBuilderFromConfig(cfg).staticTenantCount)
}

// dynamicResourceSource is a TenantStore whose IsDynamic verdict is settable; every
// other resource-source fake in this package hardcodes false.
type dynamicResourceSource struct {
	stubTenantResource
	dynamic bool
}

func (s *dynamicResourceSource) IsDynamic() bool { return s.dynamic }

func TestRootDatabaseAbsent(t *testing.T) {
	tests := []struct {
		name   string
		cfg    func() *config.Config
		opts   *Options
		absent bool
	}{
		{name: "no_database_configured", absent: true, cfg: func() *config.Config {
			return &config.Config{}
		}},
		{name: "type_configured", absent: false, cfg: func() *config.Config {
			cfg := &config.Config{}
			cfg.Database.Type = "postgresql"
			return cfg
		}},
		{name: "host_configured", absent: false, cfg: func() *config.Config {
			cfg := &config.Config{}
			cfg.Database.Host = "db.internal"
			return cfg
		}},
		// The three exempt modes below resolve database config at runtime, so an empty
		// root block is correct there and must not read as absence.
		{name: "multi_tenant_exempt", absent: false, cfg: func() *config.Config {
			cfg := &config.Config{}
			cfg.Multitenant.Enabled = true
			return cfg
		}},
		{name: "dynamic_config_source_exempt", absent: false, cfg: func() *config.Config {
			cfg := &config.Config{}
			cfg.Source.Type = config.SourceTypeDynamic
			return cfg
		}},
		{
			name:   "dynamic_resource_source_exempt",
			absent: false,
			cfg:    func() *config.Config { return &config.Config{} },
			opts:   &Options{ResourceSource: &dynamicResourceSource{dynamic: true}},
		},
		// A supplied but static resource source is not an exemption.
		{
			name:   "static_resource_source_not_exempt",
			absent: true,
			cfg:    func() *config.Config { return &config.Config{} },
			opts:   &Options{ResourceSource: &dynamicResourceSource{dynamic: false}},
		},
		{name: "nil_config_tolerated", absent: false, cfg: func() *config.Config { return nil }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.absent, rootDatabaseAbsent(tt.cfg(), tt.opts))
		})
	}
}

func TestRootCacheAbsent(t *testing.T) {
	stubConnector := func(context.Context, string) (cache.Cache, error) {
		return nil, nil
	}

	tests := []struct {
		name   string
		cfg    func() *config.Config
		opts   *Options
		absent bool
	}{
		{name: "cache_disabled_is_absent", absent: true, cfg: func() *config.Config {
			return &config.Config{}
		}},
		{name: "cache_enabled_is_present", absent: false, cfg: func() *config.Config {
			cfg := &config.Config{}
			cfg.Cache.Enabled = true
			return cfg
		}},
		{name: "multi_tenant_is_not_an_exemption", absent: true, cfg: func() *config.Config {
			cfg := &config.Config{}
			cfg.Multitenant.Enabled = true
			return cfg
		}},
		{name: "dynamic_config_source_exempt", absent: false, cfg: func() *config.Config {
			cfg := &config.Config{}
			cfg.Source.Type = config.SourceTypeDynamic
			return cfg
		}},
		{
			name:   "custom_cache_connector_exempt",
			absent: false,
			cfg:    func() *config.Config { return &config.Config{} },
			opts:   &Options{CacheConnector: stubConnector},
		},
		{
			name:   "static_resource_source_exempt",
			absent: false,
			cfg:    func() *config.Config { return &config.Config{} },
			opts:   &Options{ResourceSource: &dynamicResourceSource{dynamic: false}},
		},
		{
			name:   "dynamic_resource_source_exempt",
			absent: false,
			cfg:    func() *config.Config { return &config.Config{} },
			opts:   &Options{ResourceSource: &dynamicResourceSource{dynamic: true}},
		},
		{name: "nil_config_tolerated", absent: false, cfg: func() *config.Config { return nil }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.absent, rootCacheAbsent(tt.cfg(), tt.opts))
		})
	}
}

func TestWarnIfDatabaseAbsent(t *testing.T) {
	tests := []struct {
		name     string
		cfg      *config.Config
		wantWarn bool
	}{
		{name: "absent_database_warns", cfg: &config.Config{}, wantWarn: true},
		{name: "configured_database_stays_silent", wantWarn: false, cfg: func() *config.Config {
			cfg := &config.Config{}
			cfg.Database.Type = "postgresql"
			return cfg
		}()},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := &recLogger{}

			(&appBootstrap{cfg: tt.cfg, log: rec}).warnIfDatabaseAbsent()

			var got []recEvent
			for _, e := range rec.events {
				if strings.Contains(e.msg, "No database configured") {
					got = append(got, e)
				}
			}

			if !tt.wantWarn {
				assert.Empty(t, got)
				return
			}
			require.Len(t, got, 1, "the posture signal must be emitted exactly once")
			// Level is the assertion that matters: a downgrade to Debug would keep any
			// message-only check green while destroying the only production-visible
			// signal that a database config failed to reach the process.
			assert.Equal(t, "warn", got[0].level)
		})
	}
}

// TestCloseManagersOnDependencyErrorStopsCleanup pins the ADR-067 leak fix's building
// block: Close is the only externally-observable side effect available — DbManager and
// messaging.Manager expose no Closed() accessor — so this drives both managers through
// it via Get/Publisher, which fail closed only once Close (and the StopCleanup it
// joins) has actually run.
func TestCloseManagersOnDependencyErrorStopsCleanup(t *testing.T) {
	cfg := &config.Config{
		Database: config.DatabaseConfig{Type: "postgresql", Host: "localhost", Port: 5432},
		Messaging: config.MessagingConfig{
			Broker: config.BrokerConfig{URL: "amqp://guest:guest@localhost:5672/"},
		},
	}
	resourceSource := config.NewTenantStore(cfg)
	factoryResolver := createTestFactoryResolver(t)
	log := logger.New("error", false)
	factory := NewResourceManagerFactory(factoryResolver, NewManagerConfigBuilder(false, 0), log)

	dbManager := factory.CreateDatabaseManager(resourceSource)
	messagingManager := factory.CreateMessagingManager(resourceSource)
	require.NotNil(t, dbManager)
	require.NotNil(t, messagingManager)

	closeManagersOnDependencyError(dbManager, messagingManager)

	ctx := context.Background()
	_, _, dbErr := dbManager.Get(ctx, "")
	require.Error(t, dbErr, "Get after the fix must fail closed — proof Close (and the StopCleanup it joins) ran")
	assert.Contains(t, dbErr.Error(), "manager closed")

	_, _, msgErr := messagingManager.Publisher(ctx, "")
	require.ErrorIs(t, msgErr, messaging.ErrManagerClosed)
}

// TestCloseManagersOnDependencyErrorNilSafe pins that a nil manager (defensive: the
// factory never returns one today) is skipped rather than dereferenced.
func TestCloseManagersOnDependencyErrorNilSafe(t *testing.T) {
	assert.NotPanics(t, func() {
		closeManagersOnDependencyError(nil, nil)
	})
}

// TestDependenciesClosesManagersOnCacheConstructionFailure pins the actual wiring
// (not just the helper in isolation): dependencies()'s ADR-054 fail-closed cache path
// must close the dbManager/messagingManager it already built, each of which started an
// idle-cleanup goroutine at construction (ADR-067). dependencies() returns (nil, err)
// on this path and the bundle carries no manager handles, so the closeManagers seam
// (mirroring newProvider's test-override pattern) is the only way to capture the exact
// instances constructed and prove — via Get/Publisher failing closed — that Close ran.
func TestDependenciesClosesManagersOnCacheConstructionFailure(t *testing.T) {
	cfg := defaultTestConfig()
	cfg.Cache.Manager.MaxSize = -1 // ADR-054 fail-closed trigger

	opts := &Options{
		DatabaseConnector: func(*config.DatabaseConfig, logger.Logger) (database.Interface, error) {
			return &testmocks.MockDatabase{}, nil
		},
		MessagingClientFactory: func(string, logger.Logger) messaging.AMQPClient {
			return testmocks.NewMockAMQPClient()
		},
	}
	b := newAppBootstrap(cfg, logger.New("error", false), opts)

	var gotDB *database.DbManager
	var gotMsg *messaging.Manager
	closeCalls := 0
	b.closeManagers = func(db *database.DbManager, msg *messaging.Manager) {
		closeCalls++
		gotDB, gotMsg = db, msg
		closeManagersOnDependencyError(db, msg) // exercise the real Close path too
	}

	bundle, err := b.dependencies(context.Background())
	require.Error(t, err)
	assert.Nil(t, bundle)
	require.Equal(t, 1, closeCalls, "the fail-closed cache path must close the already-built managers exactly once")
	require.NotNil(t, gotDB)
	require.NotNil(t, gotMsg)

	ctx := context.Background()
	_, _, dbErr := gotDB.Get(ctx, "")
	require.Error(t, dbErr, "dependencies() must have called Close on its dbManager before returning")
	assert.Contains(t, dbErr.Error(), "manager closed")

	_, _, msgErr := gotMsg.Publisher(ctx, "")
	require.ErrorIs(t, msgErr, messaging.ErrManagerClosed,
		"dependencies() must have called Close on its messagingManager before returning")
}

// TestMarkConfiguredMirrorsRootResolver pins the contract behind the three flags: for a
// static single-tenant config a false flag coincides exactly with the framework's own root
// resolver answering that kind with not_configured, and every mode that resolves per key at
// runtime reads true, so a flag is never false while the accessor could still succeed.
func TestMarkConfiguredMirrorsRootResolver(t *testing.T) {
	withCfg := func(mutate func(*config.Config)) *config.Config {
		cfg := &config.Config{}
		mutate(cfg)
		return cfg
	}

	tests := []struct {
		name          string
		cfg           *config.Config
		opts          *Options
		wantDB        bool
		wantMessaging bool
		wantCache     bool
		crossCheck    bool // static single-tenant: the flags must agree with config.TenantStore
	}{
		{name: "nothing_configured", cfg: &config.Config{}, crossCheck: true},
		{name: "database_only", cfg: withCfg(func(c *config.Config) { c.Database.Host = "localhost" }), wantDB: true, crossCheck: true},
		{name: "messaging_only", cfg: withCfg(func(c *config.Config) { c.Messaging.Broker.URL = "amqp://localhost" }), wantMessaging: true, crossCheck: true},
		{name: "cache_enabled", cfg: withCfg(func(c *config.Config) { c.Cache.Enabled = true }), wantCache: true, crossCheck: true},
		{name: "cache_host_without_enabled_stays_false", cfg: withCfg(func(c *config.Config) { c.Cache.Redis.Host = "localhost" }), crossCheck: true},
		// DBConfigured speaks for DB only: a named database resolves through databases.<name>,
		// not the root block, so this deliberately reads false while DBByName would succeed.
		{name: "named_database_without_root_reads_false", crossCheck: true, cfg: withCfg(func(c *config.Config) {
			c.Databases = map[string]config.DatabaseConfig{"legacy": {Host: "localhost"}}
		})},
		{name: "custom_cache_connector_is_wired", cfg: &config.Config{}, opts: &Options{CacheConnector: func(context.Context, string) (cache.Cache, error) { return nil, nil }}, wantCache: true},
		{name: "multi_tenant_reads_true_for_every_kind", cfg: withCfg(func(c *config.Config) { c.Multitenant.Enabled = true }), wantDB: true, wantMessaging: true, wantCache: true},
		{name: "dynamic_config_source_reads_true", cfg: withCfg(func(c *config.Config) { c.Source.Type = config.SourceTypeDynamic }), wantDB: true, wantMessaging: true, wantCache: true},
		{name: "caller_resource_source_reads_true", cfg: &config.Config{}, opts: &Options{ResourceSource: &dynamicResourceSource{dynamic: false}}, wantDB: true, wantMessaging: true, wantCache: true},
		{name: "nil_config_reads_true", cfg: nil, wantDB: true, wantMessaging: true, wantCache: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			deps := &ModuleDeps{}
			markConfigured(deps, tt.cfg, tt.opts)

			assert.Equal(t, tt.wantDB, deps.DBConfigured, "DBConfigured")
			assert.Equal(t, tt.wantMessaging, deps.MessagingConfigured, "MessagingConfigured")
			assert.Equal(t, tt.wantCache, deps.CacheConfigured, "CacheConfigured")

			if !tt.crossCheck {
				return
			}
			ctx := context.Background()
			store := config.NewTenantStore(tt.cfg)
			_, dbErr := store.DBConfig(ctx, "")
			assert.Equal(t, !tt.wantDB, config.IsNotConfigured(dbErr), "DB flag must mirror the root resolver")
			_, msgErr := store.BrokerURL(ctx, "")
			assert.Equal(t, !tt.wantMessaging, config.IsNotConfigured(msgErr), "Messaging flag must mirror the root resolver")
			_, cacheErr := store.CacheConfig(ctx, "")
			assert.Equal(t, !tt.wantCache, config.IsNotConfigured(cacheErr), "Cache flag must mirror the root resolver")
		})
	}
}
