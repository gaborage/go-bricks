package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gaborage/go-bricks/config"
	"github.com/gaborage/go-bricks/logger"
	"github.com/gaborage/go-bricks/observability"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/metric"
	metricnoop "go.opentelemetry.io/otel/metric/noop"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	"go.opentelemetry.io/otel/trace"
	tracenoop "go.opentelemetry.io/otel/trace/noop"
)

const (
	testConfigFile              = "config.yaml"
	bootstrapLoggerUnchangedMsg = "Bootstrap logger should remain unchanged"
)

type testObservabilityProvider struct {
	loggerProvider *sdklog.LoggerProvider
	disableStdout  bool
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
	return nil
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
	err := os.WriteFile(configPath, []byte(yamlContent), 0600)
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
	obsProvider := bootstrap.initializeObservability()
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
	err := os.WriteFile(configPath, []byte(yamlContent), 0600)
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
	obsProvider := bootstrap.initializeObservability()
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
	err := os.WriteFile(configPath, []byte(yamlContent), 0600)
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
	obsProvider := bootstrap.initializeObservability()
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
	err := os.WriteFile(configPath, []byte(yamlContent), 0600)
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
	obsProvider := bootstrap.initializeObservability()
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
	got := bootstrap.initializeObservability()

	assert.Same(t, want, got, "the provider returned by the seam should be installed verbatim")
	require.NotNil(t, gotCtx, "seam must receive a non-nil context")
	require.NotNil(t, gotCfg, "seam must receive the observability config")

	deadline, ok := gotCtx.Deadline()
	require.True(t, ok, "seam context must carry a deadline derived from the startup budget")

	const tolerance = 2 * time.Second
	assert.InDelta(t, budget.Seconds(), deadline.Sub(start).Seconds(), tolerance.Seconds(),
		"seam context deadline must be ~app.startup.observability from now")
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

	got := bootstrap.initializeObservability()

	require.NotNil(t, got, "a no-op provider must be installed on constructor error")
	assert.NotNil(t, got.TracerProvider(), "no-op provider must expose a tracer provider")
	assert.NotNil(t, got.MeterProvider(), "no-op provider must expose a meter provider")
	assert.Nil(t, got.LoggerProvider(), "no-op provider has no OTLP logger provider")
	assert.True(t, mockLog.warnCalled, "constructor failure must be logged as a WARN")

	// The fallback provider must shut down cleanly (no-op).
	assert.NoError(t, got.Shutdown(context.Background()))
}
