package app

import (
	"context"
	"reflect"
	"sync"

	"github.com/labstack/echo/v4"

	"github.com/gaborage/go-bricks/config"
	"github.com/gaborage/go-bricks/database"
	"github.com/gaborage/go-bricks/logger"
	"github.com/gaborage/go-bricks/messaging"
	"github.com/gaborage/go-bricks/server"
)

// Module defines the interface that all application modules must implement.
// It provides hooks for initialization, route registration, messaging setup, and cleanup.
type Module interface {
	Name() string
	Init(deps *ModuleDeps) error
	RegisterRoutes(hr *server.HandlerRegistry, e *echo.Echo)
	RegisterMessaging(registry *messaging.Registry)
	Shutdown() error
}

// ModuleDeps contains the dependencies that are injected into each module.
// It provides access to core services like database, logging, and messaging.
type ModuleDeps struct {
	DB        database.Interface
	Logger    logger.Logger
	Messaging messaging.Client
	Config    *config.Config
}

// Describer is an optional interface that modules can implement to provide
// additional metadata for documentation generation and introspection.
type Describer interface {
	DescribeRoutes() []server.RouteDescriptor
	DescribeModule() ModuleDescriptor
}

// ModuleDescriptor captures module-level metadata
type ModuleDescriptor struct {
	Name        string   // Module name
	Version     string   // Module version
	Description string   // Module description
	Tags        []string // Module tags for grouping
	BasePath    string   // Base path for all module routes
}

// ModuleInfo contains both the module instance and its metadata
type ModuleInfo struct {
	Module     Module           // The actual module instance
	Descriptor ModuleDescriptor // Module metadata
	Package    string           // Go package path
}

// IsDescriber checks if a module implements the Describer interface
func IsDescriber(m Module) (Describer, bool) {
	d, ok := m.(Describer)
	return d, ok
}

// MetadataRegistry tracks discovered modules for introspection
type MetadataRegistry struct {
	mu      sync.RWMutex
	modules map[string]ModuleInfo
}

// DefaultModuleRegistry is the global module metadata registry
var DefaultModuleRegistry = &MetadataRegistry{
	modules: make(map[string]ModuleInfo),
}

// RegisterModule adds a module to the metadata registry
func (r *MetadataRegistry) RegisterModule(name string, module Module, pkg string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	info := ModuleInfo{
		Module:  module,
		Package: pkg,
	}

	// Get descriptor if module implements Describer
	if describer, ok := IsDescriber(module); ok {
		info.Descriptor = describer.DescribeModule()
	} else {
		// Default descriptor
		info.Descriptor = ModuleDescriptor{
			Name: name,
		}
	}

	r.modules[name] = info
}

// GetModules returns a copy of all registered module information
func (r *MetadataRegistry) GetModules() map[string]ModuleInfo {
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := make(map[string]ModuleInfo, len(r.modules))
	for k, v := range r.modules {
		result[k] = v
	}
	return result
}

// GetModule returns information for a specific module
func (r *MetadataRegistry) GetModule(name string) (ModuleInfo, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	info, exists := r.modules[name]
	return info, exists
}

// Clear removes all registered modules (useful for testing)
func (r *MetadataRegistry) Clear() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.modules = make(map[string]ModuleInfo)
}

// Count returns the number of registered modules
func (r *MetadataRegistry) Count() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.modules)
}

// ModuleRegistry manages the registration and lifecycle of application modules.
// It handles module initialization, route registration, messaging setup, and shutdown.
type ModuleRegistry struct {
	modules           []Module
	deps              *ModuleDeps
	logger            logger.Logger
	messagingRegistry *messaging.Registry
}

// NewModuleRegistry creates a new module registry with the given dependencies.
// It initializes an empty registry ready to accept module registrations.
func NewModuleRegistry(deps *ModuleDeps) *ModuleRegistry {
	var messagingRegistry *messaging.Registry

	// Initialize messaging registry if AMQP client is available
	if amqpClient, ok := deps.Messaging.(messaging.AMQPClient); ok && deps.Messaging != nil {
		messagingRegistry = messaging.NewRegistry(amqpClient, deps.Logger)
	}

	return &ModuleRegistry{
		modules:           make([]Module, 0),
		deps:              deps,
		logger:            deps.Logger,
		messagingRegistry: messagingRegistry,
	}
}

// Register adds a module to the registry and initializes it.
// It calls the module's Init method with the injected dependencies.
func (r *ModuleRegistry) Register(module Module) error {
	moduleName := module.Name()

	r.logger.Info().
		Str("module", moduleName).
		Msg("Registering module")

	if err := module.Init(r.deps); err != nil {
		return err
	}

	// Add to lifecycle registry
	r.modules = append(r.modules, module)

	// Register with metadata registry for introspection
	DefaultModuleRegistry.RegisterModule(moduleName, module, getModulePackage(module))

	return nil
}

// RegisterRoutes calls RegisterRoutes on all registered modules.
// It should be called after all modules have been registered.
func (r *ModuleRegistry) RegisterRoutes(e *echo.Echo) {
	// Create handler registry
	handlerRegistry := server.NewHandlerRegistry(r.deps.Config)

	for _, module := range r.modules {
		r.logger.Info().
			Str("module", module.Name()).
			Msg("Registering module routes")

		module.RegisterRoutes(handlerRegistry, e)
	}
}

// RegisterMessaging calls RegisterMessaging on all registered modules.
// It should be called after all modules have been registered but before starting the server.
func (r *ModuleRegistry) RegisterMessaging() error {
	if r.messagingRegistry == nil {
		r.logger.Debug().Msg("No messaging registry available, skipping messaging registration")
		return nil
	}

	for _, module := range r.modules {
		r.logger.Info().
			Str("module", module.Name()).
			Msg("Registering module messaging")

		module.RegisterMessaging(r.messagingRegistry)
	}

	// Declare all messaging infrastructure after all modules have registered
	r.logger.Info().Msg("Declaring messaging infrastructure")
	ctx := context.Background() // Use background context for infrastructure setup
	if err := r.messagingRegistry.DeclareInfrastructure(ctx); err != nil {
		return err
	}

	// Start consumers after infrastructure is declared
	r.logger.Info().Msg("Starting message consumers")
	return r.messagingRegistry.StartConsumers(ctx)
}

// Shutdown gracefully shuts down all registered modules.
// It calls each module's Shutdown method and logs any errors.
func (r *ModuleRegistry) Shutdown() error {
	// Stop consumers first
	if r.messagingRegistry != nil {
		r.logger.Info().Msg("Stopping message consumers")
		r.messagingRegistry.StopConsumers()
	}

	// Then shutdown modules
	for _, module := range r.modules {
		r.logger.Info().
			Str("module", module.Name()).
			Msg("Shutting down module")

		if err := module.Shutdown(); err != nil {
			r.logger.Error().
				Err(err).
				Str("module", module.Name()).
				Msg("Failed to shutdown module")
		}
	}
	return nil
}

// getModulePackage extracts the package path from a module instance
func getModulePackage(module Module) string {
	// Use reflection to get the package path
	moduleType := reflect.TypeOf(module)
	if moduleType.Kind() == reflect.Ptr {
		moduleType = moduleType.Elem()
	}
	return moduleType.PkgPath()
}
