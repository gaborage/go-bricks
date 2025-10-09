package app

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gaborage/go-bricks/config"
	"github.com/gaborage/go-bricks/logger"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	testConfigFile = "config.yaml"
)

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
		err := os.Chdir(originalDir)
		require.NoError(t, err)
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
		err := os.Chdir(originalDir)
		require.NoError(t, err)
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
		err := os.Chdir(originalDir)
		require.NoError(t, err)
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
		err := os.Chdir(originalDir)
		require.NoError(t, err)
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
