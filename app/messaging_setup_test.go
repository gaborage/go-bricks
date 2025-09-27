package app

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

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
