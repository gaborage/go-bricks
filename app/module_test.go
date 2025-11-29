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

	modules := registry.Modules()
	assert.Len(t, modules, 1)
	modules["beta"] = ModuleInfo{}
	assert.Equal(t, 1, registry.Count(), "get modules should return copy")

	info, ok := registry.Module("alpha")
	assert.True(t, ok)
	assert.Equal(t, "alpha", info.Descriptor.Name)

	registry.Clear()
	assert.Equal(t, 0, registry.Count())
}

// TestModuleRegistryRegisterSchedulerModule verifies JobRegistrar wiring
func TestModuleRegistryRegisterSchedulerModule(t *testing.T) {
	log := logger.New("debug", true)
	deps := &ModuleDeps{
		Logger: log,
		Config: &config.Config{},
	}

	registry := NewModuleRegistry(deps)

	// Register a module that implements JobRegistrar
	scheduler := &MockSchedulerModule{name: "scheduler"}
	scheduler.On("Init", deps).Return(nil)

	err := registry.Register(scheduler)
	assert.NoError(t, err)

	// Verify scheduler was wired into deps
	assert.NotNil(t, deps.Scheduler, "Scheduler should be wired into deps")
	assert.Equal(t, scheduler, deps.Scheduler, "Scheduler should be the registered module")

	scheduler.AssertExpectations(t)
}

// TestModuleRegistryRegisterNonSchedulerModule verifies non-scheduler modules don't affect deps.Scheduler
func TestModuleRegistryRegisterNonSchedulerModule(t *testing.T) {
	log := logger.New("debug", true)
	deps := &ModuleDeps{
		Logger: log,
		Config: &config.Config{},
	}

	registry := NewModuleRegistry(deps)

	// Register a normal module (not JobRegistrar)
	module := &MockModule{name: "normal-module"}
	module.On("Init", deps).Return(nil)

	err := registry.Register(module)
	assert.NoError(t, err)

	// Verify deps.Scheduler is still nil
	assert.Nil(t, deps.Scheduler, "Scheduler should remain nil for non-scheduler modules")

	module.AssertExpectations(t)
}

// TestRegisterJobsNoScheduler verifies RegisterJobs skips when no scheduler is registered
func TestRegisterJobsNoScheduler(t *testing.T) {
	log := logger.New("debug", true)
	deps := &ModuleDeps{
		Logger:    log,
		Config:    &config.Config{},
		Scheduler: nil, // No scheduler
	}

	registry := NewModuleRegistry(deps)

	// Register a module with JobProvider
	jobProvider := &MockJobProviderModule{name: "job-provider"}
	jobProvider.On("Init", deps).Return(nil)
	require.NoError(t, registry.Register(jobProvider))

	// Call RegisterJobs - should skip silently
	err := registry.RegisterJobs()
	assert.NoError(t, err)

	// Verify RegisterJobs was NOT called on the provider
	jobProvider.AssertNotCalled(t, "RegisterJobs")
}

// TestRegisterJobsNoJobProviders verifies RegisterJobs when no modules implement JobProvider
func TestRegisterJobsNoJobProviders(t *testing.T) {
	log := logger.New("debug", true)
	deps := &ModuleDeps{
		Logger: log,
		Config: &config.Config{},
	}

	registry := NewModuleRegistry(deps)

	// Register scheduler
	scheduler := &MockSchedulerModule{name: "scheduler"}
	scheduler.On("Init", deps).Return(nil)
	require.NoError(t, registry.Register(scheduler))

	// Register normal module (no JobProvider)
	module := &MockModule{name: "normal-module"}
	module.On("Init", deps).Return(nil)
	require.NoError(t, registry.Register(module))

	// Call RegisterJobs
	err := registry.RegisterJobs()
	assert.NoError(t, err)

	// No jobs should be registered
	scheduler.AssertExpectations(t)
	module.AssertExpectations(t)
}

// TestRegisterJobsSuccess verifies successful job registration from multiple providers
func TestRegisterJobsSuccess(t *testing.T) {
	log := logger.New("debug", true)
	deps := &ModuleDeps{
		Logger: log,
		Config: &config.Config{},
	}

	registry := NewModuleRegistry(deps)

	// Register scheduler
	scheduler := &MockSchedulerModule{name: "scheduler"}
	scheduler.On("Init", deps).Return(nil)
	require.NoError(t, registry.Register(scheduler))

	// Register two JobProvider modules
	provider1 := &MockJobProviderModule{name: "provider-1"}
	provider1.On("Init", deps).Return(nil)
	provider1.On("RegisterJobs", scheduler).Return(nil)
	require.NoError(t, registry.Register(provider1))

	provider2 := &MockJobProviderModule{name: "provider-2"}
	provider2.On("Init", deps).Return(nil)
	provider2.On("RegisterJobs", scheduler).Return(nil)
	require.NoError(t, registry.Register(provider2))

	// Call RegisterJobs
	err := registry.RegisterJobs()
	assert.NoError(t, err)

	// Verify both providers were called
	provider1.AssertExpectations(t)
	provider2.AssertExpectations(t)
}

// TestRegisterJobsWithError verifies error handling during job registration
func TestRegisterJobsWithError(t *testing.T) {
	log := logger.New("debug", true)
	deps := &ModuleDeps{
		Logger: log,
		Config: &config.Config{},
	}

	registry := NewModuleRegistry(deps)

	// Register scheduler
	scheduler := &MockSchedulerModule{name: "scheduler"}
	scheduler.On("Init", deps).Return(nil)
	require.NoError(t, registry.Register(scheduler))

	// Register JobProvider that returns error
	provider := &MockJobProviderModule{name: "failing-provider"}
	provider.On("Init", deps).Return(nil)
	provider.On("RegisterJobs", scheduler).Return(errors.New("registration failed"))
	require.NoError(t, registry.Register(provider))

	// Call RegisterJobs - should return error
	err := registry.RegisterJobs()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failing-provider")
	assert.Contains(t, err.Error(), "registration failed")

	provider.AssertExpectations(t)
}
