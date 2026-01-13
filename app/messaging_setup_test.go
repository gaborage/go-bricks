package app

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/cache"
	"github.com/gaborage/go-bricks/database"
	"github.com/gaborage/go-bricks/logger"
	"github.com/gaborage/go-bricks/messaging"
	"github.com/gaborage/go-bricks/server"
)

func TestCollectDeclarations(t *testing.T) {
	t.Run("collects declarations from empty registry", func(t *testing.T) {
		log := logger.New("debug", true)
		initializer := NewMessagingInitializer(log, nil, false)

		// Create empty registry
		deps := &ModuleDeps{
			Logger: log,
		}
		registry := NewModuleRegistry(deps)

		// Test CollectDeclarations with empty registry
		declarations, err := initializer.CollectDeclarations(registry)
		assert.NoError(t, err)
		assert.NotNil(t, declarations)
	})

	t.Run("collects declarations from registry with simple module", func(t *testing.T) {
		log := logger.New("debug", true)
		initializer := NewMessagingInitializer(log, nil, false)

		// Create a simple module that doesn't declare messaging
		module := &simpleTestModule{}
		deps := &ModuleDeps{
			Logger: log,
		}

		// Create registry and register module
		registry := NewModuleRegistry(deps)
		err := registry.Register(module)
		require.NoError(t, err)

		// Test CollectDeclarations
		declarations, err := initializer.CollectDeclarations(registry)
		assert.NoError(t, err)
		assert.NotNil(t, declarations)
	})
}

func TestSetupMultiTenantLazyInit(t *testing.T) {
	t.Run("sets declarations on provider", func(t *testing.T) {
		log := logger.New("debug", true)
		initializer := NewMessagingInitializer(log, nil, true)

		// Create a multi-tenant resource provider
		provider := &MultiTenantResourceProvider{}

		// Create declarations
		declarations := messaging.NewDeclarations()

		// Call setupMultiTenantLazyInit
		err := initializer.setupMultiTenantLazyInit(provider, declarations)

		assert.NoError(t, err)
		assert.Equal(t, declarations, provider.declarations)
	})
}

func TestPrepareRuntimeConsumers(t *testing.T) {
	t.Run("returns error when manager is nil", func(t *testing.T) {
		log := logger.New("debug", true)
		initializer := NewMessagingInitializer(log, nil, false)

		declarations := messaging.NewDeclarations()
		ctx := context.Background()

		err := initializer.PrepareRuntimeConsumers(ctx, declarations)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "messaging manager not configured")
	})
}

// Test helper modules
type simpleTestModule struct{}

func (m *simpleTestModule) Name() string             { return "simple-test-module" }
func (m *simpleTestModule) Init(_ *ModuleDeps) error { return nil }
func (m *simpleTestModule) RegisterRoutes(_ *server.HandlerRegistry, _ server.RouteRegistrar) {
	// no-op
}
func (m *simpleTestModule) DeclareMessaging(_ *messaging.Declarations) {
	// no-op
}
func (m *simpleTestModule) Shutdown() error { return nil }

func TestSetupLazyConsumerInit(t *testing.T) {
	t.Run("returns error when manager is nil", func(t *testing.T) {
		log := logger.New("debug", true)
		initializer := NewMessagingInitializer(log, nil, false)
		declarations := messaging.NewDeclarations()

		err := initializer.SetupLazyConsumerInit(nil, declarations)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "messaging manager not configured")
	})

	t.Run("sets up single tenant lazy init", func(t *testing.T) {
		log := logger.New("debug", true)
		manager := &messaging.Manager{}
		initializer := NewMessagingInitializer(log, manager, false)

		provider := &SingleTenantResourceProvider{}
		declarations := messaging.NewDeclarations()

		err := initializer.SetupLazyConsumerInit(provider, declarations)
		assert.NoError(t, err)
		assert.Equal(t, declarations, provider.declarations)
	})

	t.Run("sets up multi tenant lazy init", func(t *testing.T) {
		log := logger.New("debug", true)
		manager := &messaging.Manager{}
		initializer := NewMessagingInitializer(log, manager, true)

		provider := &MultiTenantResourceProvider{}
		declarations := messaging.NewDeclarations()

		err := initializer.SetupLazyConsumerInit(provider, declarations)
		assert.NoError(t, err)
		assert.Equal(t, declarations, provider.declarations)
	})

	t.Run("warns for unknown provider type", func(t *testing.T) {
		log := logger.New("debug", true)
		manager := &messaging.Manager{}
		initializer := NewMessagingInitializer(log, manager, false)

		// Use an unknown provider type that implements ResourceProvider
		provider := &unknownResourceProvider{}
		declarations := messaging.NewDeclarations()

		err := initializer.SetupLazyConsumerInit(provider, declarations)
		assert.NoError(t, err) // Should not error, just warn
	})
}

func TestPrepareRuntimeConsumersComprehensive(t *testing.T) {
	t.Run("multi-tenant mode logs and returns success", func(t *testing.T) {
		log := logger.New("debug", true)
		manager := &messaging.Manager{}
		initializer := NewMessagingInitializer(log, manager, true)

		declarations := messaging.NewDeclarations()
		ctx := context.Background()

		err := initializer.PrepareRuntimeConsumers(ctx, declarations)
		assert.NoError(t, err)
	})

	// Note: single-tenant mode with real manager is covered by integration tests
	// since it requires proper manager initialization with resource sources
}

func TestIsAvailable(t *testing.T) {
	t.Run("returns true when manager is available", func(t *testing.T) {
		log := logger.New("debug", true)
		manager := &messaging.Manager{}
		initializer := NewMessagingInitializer(log, manager, false)

		assert.True(t, initializer.IsAvailable())
	})

	t.Run("returns false when manager is nil", func(t *testing.T) {
		log := logger.New("debug", true)
		initializer := NewMessagingInitializer(log, nil, false)

		assert.False(t, initializer.IsAvailable())
	})
}

func TestLogDeploymentMode(t *testing.T) {
	t.Run("logs multi-tenant mode", func(_ *testing.T) {
		log := logger.New("debug", true)
		manager := &messaging.Manager{}
		initializer := NewMessagingInitializer(log, manager, true)

		// This test primarily ensures the function runs without panic
		// In a real scenario, you might capture logs to verify content
		initializer.LogDeploymentMode()
	})

	t.Run("logs single-tenant mode", func(_ *testing.T) {
		log := logger.New("debug", true)
		manager := &messaging.Manager{}
		initializer := NewMessagingInitializer(log, manager, false)

		// This test primarily ensures the function runs without panic
		initializer.LogDeploymentMode()
	})
}

func TestNewMessagingInitializer(t *testing.T) {
	t.Run("creates initializer with correct fields", func(t *testing.T) {
		log := logger.New("debug", true)
		manager := &messaging.Manager{}
		multiTenant := true

		initializer := NewMessagingInitializer(log, manager, multiTenant)

		assert.NotNil(t, initializer)
		assert.Equal(t, multiTenant, initializer.multiTenant)
		assert.Equal(t, manager, initializer.manager)
		assert.Equal(t, log, initializer.logger)
	})
}

// Mock types for testing
type unknownResourceProvider struct{}

func (u *unknownResourceProvider) DB(_ context.Context) (database.Interface, error) {
	return nil, nil
}

func (u *unknownResourceProvider) DBByName(_ context.Context, _ string) (database.Interface, error) {
	return nil, nil
}

func (u *unknownResourceProvider) Messaging(_ context.Context) (messaging.AMQPClient, error) {
	return nil, nil
}

func (u *unknownResourceProvider) Cache(_ context.Context) (cache.Cache, error) {
	return nil, nil
}
