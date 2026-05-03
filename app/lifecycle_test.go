package app

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/config"
	"github.com/gaborage/go-bricks/logger"
	"github.com/gaborage/go-bricks/messaging"
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
		healthProbes: []Prober{},
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
	err := registry.Register(scheduler)
	assert.NoError(t, err)

	// Register JobProvider module
	jobProvider := &MockJobProviderModule{name: "job-provider"}
	jobProvider.On("Init", deps).Return(nil)
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

// publisherDeclaringModule registers a single publisher during DeclareMessaging.
// Used to drive the structural fail-fast check in prepareRuntime — see issue #366.
type publisherDeclaringModule struct{}

func (publisherDeclaringModule) Name() string             { return "publisher-declaring-module" }
func (publisherDeclaringModule) Init(_ *ModuleDeps) error { return nil }
func (publisherDeclaringModule) Shutdown() error          { return nil }
func (publisherDeclaringModule) DeclareMessaging(decls *messaging.Declarations) {
	decls.RegisterExchange(&messaging.ExchangeDeclaration{
		Name:    "test.exchange",
		Type:    "topic",
		Durable: true,
	})
	decls.RegisterPublisher(&messaging.PublisherDeclaration{
		Exchange:   "test.exchange",
		RoutingKey: "test.routing",
		EventType:  "test.event",
	})
}

// newLifecycleCheckApp builds a minimal App suitable for exercising prepareRuntime's
// fail-fast check without standing up the full bootstrap pipeline.
func newLifecycleCheckApp(t *testing.T, cfg *config.Config) *App {
	t.Helper()
	testLogger := logger.New("info", false)
	deps := &ModuleDeps{Logger: testLogger, Config: cfg}
	return &App{
		cfg:      cfg,
		logger:   testLogger,
		registry: NewModuleRegistry(deps),
		server:   newMockServer(),
		closers:  []namedCloser{},
	}
}

// TestPrepareRuntimeFailsWhenDeclarationsExistAndMessagingUnconfigured guards
// the structural half of issue #366: declarations from MessagingDeclarer modules
// must not be silently dropped when messaging is unset.
func TestPrepareRuntimeFailsWhenDeclarationsExistAndMessagingUnconfigured(t *testing.T) {
	cfg := &config.Config{
		App:         config.AppConfig{Name: testApp, Env: "test", Version: "1.0.0"},
		Multitenant: config.MultitenantConfig{Enabled: false},
		// Messaging.Broker.URL intentionally empty.
	}
	app := newLifecycleCheckApp(t, cfg)
	require.NoError(t, app.RegisterModule(publisherDeclaringModule{}))

	err := app.prepareRuntime()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "messaging is not configured")
	assert.Contains(t, err.Error(), "publishers=1")
}

// TestPrepareRuntimeAllowsEmptyDeclarationsWithMessagingUnconfigured verifies
// that the check does not fire when no module declared messaging — startup
// without messaging configured remains valid for apps that don't use AMQP.
func TestPrepareRuntimeAllowsEmptyDeclarationsWithMessagingUnconfigured(t *testing.T) {
	cfg := &config.Config{
		App:         config.AppConfig{Name: testApp, Env: "test", Version: "1.0.0"},
		Multitenant: config.MultitenantConfig{Enabled: false},
		// Messaging.Broker.URL intentionally empty; no MessagingDeclarer module registered.
	}
	app := newLifecycleCheckApp(t, cfg)

	require.NoError(t, app.prepareRuntime())
}

// TestPrepareRuntimeSkipsCheckInMultiTenantMode verifies that the static check
// is skipped when multitenant.enabled=true — each tenant supplies its own
// broker URL via the resource source, so a global check would yield false
// positives.
func TestPrepareRuntimeSkipsCheckInMultiTenantMode(t *testing.T) {
	cfg := &config.Config{
		App:         config.AppConfig{Name: testApp, Env: "test", Version: "1.0.0"},
		Multitenant: config.MultitenantConfig{Enabled: true},
		// Messaging.Broker.URL intentionally empty.
	}
	app := newLifecycleCheckApp(t, cfg)
	require.NoError(t, app.RegisterModule(publisherDeclaringModule{}))

	require.NoError(t, app.prepareRuntime())
}

// TestPrepareRuntimeSucceedsWithDeclarationsAndConfiguredMessaging is the
// regression guard for the happy path: messaging configured, declarations
// present, prepareRuntime proceeds.
func TestPrepareRuntimeSucceedsWithDeclarationsAndConfiguredMessaging(t *testing.T) {
	cfg := &config.Config{
		App:         config.AppConfig{Name: testApp, Env: "test", Version: "1.0.0"},
		Multitenant: config.MultitenantConfig{Enabled: false},
		Messaging: config.MessagingConfig{
			Broker: config.BrokerConfig{URL: "amqp://localhost"},
		},
	}
	app := newLifecycleCheckApp(t, cfg)
	require.NoError(t, app.RegisterModule(publisherDeclaringModule{}))

	require.NoError(t, app.prepareRuntime())
}
