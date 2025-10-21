package app

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"

	"github.com/gaborage/go-bricks/config"
	"github.com/gaborage/go-bricks/logger"
)

const (
	testApp = "test-app"
)

func TestShutdownTiming(t *testing.T) {
	// Test that the shutdown process completes within reasonable time
	cfg := &config.Config{
		App: config.AppConfig{
			Name:    testApp,
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
		cfg:          cfg,
		logger:       testLogger,
		registry:     NewModuleRegistry(deps),
		closers:      []namedCloser{},
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

// TestPrepareRuntimeWithScheduler verifies that RegisterJobs is called during prepareRuntime
func TestPrepareRuntimeWithScheduler(t *testing.T) {
	cfg := &config.Config{
		App: config.AppConfig{
			Name:    testApp,
			Env:     "test",
			Version: "1.0.0",
		},
		Server: config.ServerConfig{
			Port: 8080,
		},
		Debug: config.DebugConfig{
			Enabled: false,
		},
		Multitenant: config.MultitenantConfig{
			Enabled: false,
		},
	}

	testLogger := logger.New("info", false)

	deps := &ModuleDeps{
		Logger: testLogger,
		Config: cfg,
	}

	registry := NewModuleRegistry(deps)

	// Register scheduler module
	scheduler := &MockSchedulerModule{name: "scheduler"}
	scheduler.On("Init", deps).Return(nil)
	scheduler.On("DeclareMessaging", mock.Anything).Return()
	scheduler.On("RegisterRoutes", mock.Anything, mock.Anything).Return()
	err := registry.Register(scheduler)
	assert.NoError(t, err)

	// Register JobProvider module
	jobProvider := &MockJobProviderModule{name: "job-provider"}
	jobProvider.On("Init", deps).Return(nil)
	jobProvider.On("DeclareMessaging", mock.Anything).Return()
	jobProvider.On("RegisterRoutes", mock.Anything, mock.Anything).Return()
	jobProvider.On("RegisterJobs", scheduler).Return(nil)
	err = registry.Register(jobProvider)
	assert.NoError(t, err)

	// Create minimal app with mocked server
	server := newMockServer()

	app := &App{
		cfg:      cfg,
		logger:   testLogger,
		registry: registry,
		server:   server,
		closers:  []namedCloser{},
	}

	// Call prepareRuntime
	err = app.prepareRuntime()
	assert.NoError(t, err)

	// Verify RegisterJobs was called on the provider
	jobProvider.AssertExpectations(t)
	scheduler.AssertExpectations(t)
}
