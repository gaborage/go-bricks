package app

import (
	"errors"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/config"
	"github.com/gaborage/go-bricks/logger"
)

func TestNewModuleRegistry(t *testing.T) {
	log := logger.New("debug", true)
	mockMessaging := &MockMessagingClient{}

	deps := &ModuleDeps{
		DB:        &MockDatabase{},
		Logger:    log,
		Messaging: mockMessaging,
		Config:    &config.Config{},
	}

	registry := NewModuleRegistry(deps)

	assert.NotNil(t, registry)
	assert.Equal(t, deps, registry.deps)
	assert.Equal(t, log, registry.logger)
	assert.Empty(t, registry.modules)
	// messaging registry should be nil since MockMessagingClient doesn't implement AMQPClient
	assert.Nil(t, registry.messagingRegistry)
}

func TestModuleRegistry_Register_Success(t *testing.T) {
	log := logger.New("debug", true)
	deps := &ModuleDeps{
		DB:        &MockDatabase{},
		Logger:    log,
		Messaging: &MockMessagingClient{},
		Config:    &config.Config{},
	}

	registry := NewModuleRegistry(deps)

	module := &MockModule{name: "test-module"}
	module.On("Init", deps).Return(nil)

	err := registry.Register(module)
	assert.NoError(t, err)
	assert.Len(t, registry.modules, 1)
	assert.Equal(t, module, registry.modules[0])

	module.AssertExpectations(t)
}

func TestModuleRegistry_Register_InitError(t *testing.T) {
	log := logger.New("debug", true)
	deps := &ModuleDeps{
		DB:        &MockDatabase{},
		Logger:    log,
		Messaging: &MockMessagingClient{},
		Config:    &config.Config{},
	}

	registry := NewModuleRegistry(deps)

	module := &MockModule{name: "failing-module"}
	expectedErr := errors.New("init failed")
	module.On("Init", deps).Return(expectedErr)

	err := registry.Register(module)
	assert.Error(t, err)
	assert.Equal(t, expectedErr, err)
	assert.Empty(t, registry.modules)

	module.AssertExpectations(t)
}

func TestModuleRegistry_RegisterRoutes(t *testing.T) {
	log := logger.New("debug", true)
	deps := &ModuleDeps{
		DB:        &MockDatabase{},
		Logger:    log,
		Messaging: &MockMessagingClient{},
		Config:    &config.Config{},
	}

	registry := NewModuleRegistry(deps)

	// Add multiple modules
	module1 := &MockModule{name: "module1"}
	module2 := &MockModule{name: "module2"}

	// Setup init expectations
	module1.On("Init", deps).Return(nil)
	module2.On("Init", deps).Return(nil)

	// Register modules
	require.NoError(t, registry.Register(module1))
	require.NoError(t, registry.Register(module2))

	// Create Echo instance
	e := echo.New()

	// Setup route registration expectations
	module1.On("RegisterRoutes", mock.AnythingOfType("*server.HandlerRegistry"), e).Return()
	module2.On("RegisterRoutes", mock.AnythingOfType("*server.HandlerRegistry"), e).Return()

	// Call RegisterRoutes
	registry.RegisterRoutes(e)

	module1.AssertExpectations(t)
	module2.AssertExpectations(t)
}

func TestModuleRegistry_RegisterMessaging_NoRegistry(t *testing.T) {
	log := logger.New("debug", true)
	deps := &ModuleDeps{
		DB:        &MockDatabase{},
		Logger:    log,
		Messaging: &MockMessagingClient{}, // Not an AMQPClient
	}

	registry := NewModuleRegistry(deps)

	// Add a module
	module := &MockModule{name: "test-module"}
	module.On("Init", deps).Return(nil)
	require.NoError(t, registry.Register(module))

	// RegisterMessaging should skip when no messaging registry is available
	err := registry.RegisterMessaging()
	assert.NoError(t, err)

	module.AssertExpectations(t)
}

func TestModuleRegistry_Shutdown_NoModules(t *testing.T) {
	log := logger.New("debug", true)
	registry := NewModuleRegistry(&ModuleDeps{Logger: log})

	err := registry.Shutdown()
	assert.NoError(t, err)
}

func TestModuleRegistry_Shutdown_WithModules(t *testing.T) {
	log := logger.New("debug", true)
	deps := &ModuleDeps{
		Logger: log,
		Config: &config.Config{},
	}
	registry := NewModuleRegistry(deps)

	// Add modules
	module1 := &MockModule{name: "module1"}
	module2 := &MockModule{name: "module2"}

	// Setup init expectations and register modules
	module1.On("Init", deps).Return(nil)
	module2.On("Init", deps).Return(nil)
	require.NoError(t, registry.Register(module1))
	require.NoError(t, registry.Register(module2))

	// Setup shutdown expectations
	module1.On("Shutdown").Return(nil)
	module2.On("Shutdown").Return(nil)

	err := registry.Shutdown()
	assert.NoError(t, err)

	module1.AssertExpectations(t)
	module2.AssertExpectations(t)
}

func TestModuleRegistry_Shutdown_WithErrors(t *testing.T) {
	log := logger.New("debug", true)
	deps := &ModuleDeps{
		Logger: log,
		Config: &config.Config{},
	}
	registry := NewModuleRegistry(deps)

	// Add modules
	module1 := &MockModule{name: "failing-module1"}
	module2 := &MockModule{name: "failing-module2"}

	// Setup init expectations and register modules
	module1.On("Init", deps).Return(nil)
	module2.On("Init", deps).Return(nil)
	require.NoError(t, registry.Register(module1))
	require.NoError(t, registry.Register(module2))

	// Setup shutdown expectations - modules fail to shutdown
	module1.On("Shutdown").Return(errors.New("shutdown failed 1"))
	module2.On("Shutdown").Return(errors.New("shutdown failed 2"))

	// Should not return error even if modules fail to shutdown
	err := registry.Shutdown()
	assert.NoError(t, err)

	module1.AssertExpectations(t)
	module2.AssertExpectations(t)
}

func TestModuleRegistry_Shutdown_SingleModule(t *testing.T) {
	log := logger.New("debug", true)
	deps := &ModuleDeps{
		Logger: log,
		Config: &config.Config{},
	}
	registry := NewModuleRegistry(deps)

	// Add a module
	module := &MockModule{name: "test-module"}
	module.On("Init", deps).Return(nil)
	require.NoError(t, registry.Register(module))

	// Setup expectations
	module.On("Shutdown").Return(nil)

	err := registry.Shutdown()
	assert.NoError(t, err)

	module.AssertExpectations(t)
}
