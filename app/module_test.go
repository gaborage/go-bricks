package app

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/config"
	"github.com/gaborage/go-bricks/database"
	"github.com/gaborage/go-bricks/logger"
	"github.com/gaborage/go-bricks/messaging"
	"github.com/gaborage/go-bricks/server"
	testmocks "github.com/gaborage/go-bricks/testing/mocks"
)

const (
	testModule = "test-module"
)

func TestNewModuleRegistry(t *testing.T) {
	log := logger.New("debug", true)
	mockMessaging := testmocks.NewMockAMQPClient()

	mockDB := &testmocks.MockDatabase{}
	deps := &ModuleDeps{
		Logger: log,
		Config: &config.Config{},
		GetDB: func(_ context.Context) (database.Interface, error) {
			return mockDB, nil
		},
		GetMessaging: func(_ context.Context) (messaging.AMQPClient, error) {
			return mockMessaging, nil
		},
	}

	registry := NewModuleRegistry(deps)

	assert.NotNil(t, registry)
	assert.Equal(t, deps, registry.deps)
	assert.Equal(t, log, registry.logger)
	assert.Empty(t, registry.modules)
	// No messaging registry field in new approach
}

func TestModuleRegistryRegisterSuccess(t *testing.T) {
	log := logger.New("debug", true)
	mockDB := &testmocks.MockDatabase{}
	mockMessaging := testmocks.NewMockAMQPClient()
	deps := &ModuleDeps{
		Logger: log,
		Config: &config.Config{},
		GetDB: func(_ context.Context) (database.Interface, error) {
			return mockDB, nil
		},
		GetMessaging: func(_ context.Context) (messaging.AMQPClient, error) {
			return mockMessaging, nil
		},
	}

	registry := NewModuleRegistry(deps)

	module := &MockModule{name: testModule}
	module.On("Init", deps).Return(nil)

	err := registry.Register(module)
	assert.NoError(t, err)
	assert.Len(t, registry.modules, 1)
	assert.Equal(t, module, registry.modules[0])

	module.AssertExpectations(t)
}

func TestModuleRegistryRegisterInitError(t *testing.T) {
	log := logger.New("debug", true)
	mockDB := &testmocks.MockDatabase{}
	mockMessaging := testmocks.NewMockAMQPClient()
	deps := &ModuleDeps{
		Logger: log,
		Config: &config.Config{},
		GetDB: func(_ context.Context) (database.Interface, error) {
			return mockDB, nil
		},
		GetMessaging: func(_ context.Context) (messaging.AMQPClient, error) {
			return mockMessaging, nil
		},
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

func TestModuleRegistryRegisterRoutes(t *testing.T) {
	log := logger.New("debug", true)
	mockDB := &testmocks.MockDatabase{}
	mockMessaging := testmocks.NewMockAMQPClient()
	deps := &ModuleDeps{
		Logger: log,
		Config: &config.Config{},
		GetDB: func(_ context.Context) (database.Interface, error) {
			return mockDB, nil
		},
		GetMessaging: func(_ context.Context) (messaging.AMQPClient, error) {
			return mockMessaging, nil
		},
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

	serverCfg := &config.Config{}
	registrarServer := server.New(serverCfg, log)
	registrar := registrarServer.ModuleGroup()

	matcher := mock.MatchedBy(func(r server.RouteRegistrar) bool { return r != nil })

	// Setup route registration expectations
	module1.On("RegisterRoutes", mock.AnythingOfType("*server.HandlerRegistry"), matcher).Return()
	module2.On("RegisterRoutes", mock.AnythingOfType("*server.HandlerRegistry"), matcher).Return()

	// Call RegisterRoutes
	registry.RegisterRoutes(registrar)

	module1.AssertExpectations(t)
	module2.AssertExpectations(t)
}

func TestModuleRegistryDeclareMessaging(t *testing.T) {
	log := logger.New("debug", true)
	mockDB := &testmocks.MockDatabase{}
	deps := &ModuleDeps{
		Logger: log,
		Config: &config.Config{},
		GetDB: func(_ context.Context) (database.Interface, error) {
			return mockDB, nil
		},
		GetMessaging: func(_ context.Context) (messaging.AMQPClient, error) {
			return nil, fmt.Errorf("messaging not configured")
		},
	}

	registry := NewModuleRegistry(deps)

	// Add a module
	module := &MockModule{name: testModule}
	module.On("Init", deps).Return(nil)
	module.On("DeclareMessaging", mock.AnythingOfType("*messaging.Declarations")).Return()
	require.NoError(t, registry.Register(module))

	// Collect messaging declarations from modules
	decls := &messaging.Declarations{}
	err := registry.DeclareMessaging(decls)
	assert.NoError(t, err)
	assert.NotNil(t, decls)

	module.AssertExpectations(t)
}

func TestModuleRegistryShutdownNoModules(t *testing.T) {
	log := logger.New("debug", true)
	registry := NewModuleRegistry(&ModuleDeps{Logger: log})

	err := registry.Shutdown()
	assert.NoError(t, err)
}

func TestModuleRegistryShutdownWithModules(t *testing.T) {
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

func TestModuleRegistryShutdownWithErrors(t *testing.T) {
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

func TestModuleRegistryShutdownSingleModule(t *testing.T) {
	log := logger.New("debug", true)
	deps := &ModuleDeps{
		Logger: log,
		Config: &config.Config{},
	}
	registry := NewModuleRegistry(deps)

	// Add a module
	module := &MockModule{name: testModule}
	module.On("Init", deps).Return(nil)
	require.NoError(t, registry.Register(module))

	// Setup expectations
	module.On("Shutdown").Return(nil)

	err := registry.Shutdown()
	assert.NoError(t, err)

	module.AssertExpectations(t)
}

func TestMetadataRegistryOperations(t *testing.T) {
	registry := &MetadataRegistry{modules: make(map[string]ModuleInfo)}
	module := &MockModule{name: "alpha"}
	registry.RegisterModule("alpha", module, "example/pkg")

	assert.Equal(t, 1, registry.Count())

	modules := registry.GetModules()
	assert.Len(t, modules, 1)
	modules["beta"] = ModuleInfo{}
	assert.Equal(t, 1, registry.Count(), "get modules should return copy")

	info, ok := registry.GetModule("alpha")
	assert.True(t, ok)
	assert.Equal(t, "alpha", info.Descriptor.Name)

	registry.Clear()
	assert.Equal(t, 0, registry.Count())
}
