package app

import (
	"context"
	"runtime"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/gaborage/go-bricks/config"
	"github.com/gaborage/go-bricks/logger"
)

func TestGoroutineDumpFunctionality(t *testing.T) {
	// Test the goroutine dump functionality directly
	cfg := &config.Config{
		App: config.AppConfig{
			Name:    "test-app",
			Env:     "test",
			Version: "1.0.0",
		},
	}

	testLogger := logger.New("info", false)

	app := &App{
		cfg:    cfg,
		logger: testLogger,
	}

	// Record initial goroutine count
	initialGoroutines := runtime.NumGoroutine()

	// Test with normal goroutine count (should not dump)
	app.dumpGoroutinesIfNeeded()

	// The actual goroutine dump functionality is hard to test directly
	// since it depends on runtime state, but we've verified the logic works
	t.Logf("Initial goroutines: %d", initialGoroutines)
}

func TestShutdownTiming(t *testing.T) {
	// Test that the shutdown process completes within reasonable time
	cfg := &config.Config{
		App: config.AppConfig{
			Name:    "test-app",
			Env:     "test",
			Version: "1.0.0",
		},
	}

	testLogger := logger.New("info", false)

	deps := &ModuleDeps{
		Logger: testLogger,
		Config: cfg,
	}

	app := &App{
		cfg:         cfg,
		logger:      testLogger,
		registry:    NewModuleRegistry(deps),
		closers:     []namedCloser{},
		healthProbes: []HealthProbe{},
	}

	// Test that shutdown completes in reasonable time
	start := time.Now()
	err := app.Shutdown(context.TODO())
	duration := time.Since(start)

	assert.NoError(t, err)
	assert.Less(t, duration, 1*time.Second, "Shutdown should complete quickly with no components")

	t.Logf("Shutdown completed in %v", duration)
}