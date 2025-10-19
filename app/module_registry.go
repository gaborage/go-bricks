package app

import (
	"fmt"

	"github.com/gaborage/go-bricks/logger"
	"github.com/gaborage/go-bricks/messaging"
	"github.com/gaborage/go-bricks/server"
)

// ModuleRegistry manages the registration and lifecycle of application modules.
// It handles module initialization, route registration, messaging setup, and shutdown.
type ModuleRegistry struct {
	modules []Module
	deps    *ModuleDeps
	logger  logger.Logger
}

// NewModuleRegistry creates a new module registry with the given dependencies.
// It initializes an empty registry ready to accept module registrations.
func NewModuleRegistry(deps *ModuleDeps) *ModuleRegistry {
	return &ModuleRegistry{
		modules: make([]Module, 0),
		deps:    deps,
		logger:  deps.Logger,
	}
}

// Register adds a module to the registry and initializes it.
// It calls the module's Init method with the injected dependencies.
// Special handling: If the module implements scheduler.JobRegistrar, it is automatically
// wired into ModuleDeps.Scheduler for other modules to use.
func (r *ModuleRegistry) Register(module Module) error {
	moduleName := module.Name()

	r.logger.Info().
		Str("module", moduleName).
		Msg("Registering module")

	if err := module.Init(r.deps); err != nil {
		return err
	}

	// Special case: If this module is a JobRegistrar (scheduler module),
	// make it available to other modules via deps.Scheduler
	if jobRegistrar, ok := module.(JobRegistrar); ok {
		r.deps.Scheduler = jobRegistrar
		r.logger.Info().
			Str("module", moduleName).
			Msg("Scheduler module registered - available to other modules via deps.Scheduler")
	}

	// Add to lifecycle registry
	r.modules = append(r.modules, module)

	// Register with metadata registry for introspection
	DefaultModuleRegistry.RegisterModule(moduleName, module, getModulePackage(module))

	return nil
}

// RegisterRoutes calls RegisterRoutes on all registered modules.
// It should be called after all modules have been registered.
func (r *ModuleRegistry) RegisterRoutes(registrar server.RouteRegistrar) {
	// Create handler registry
	handlerRegistry := server.NewHandlerRegistry(r.deps.Config)

	for _, module := range r.modules {
		r.logger.Info().
			Str("module", module.Name()).
			Msg("Registering module routes")

		module.RegisterRoutes(handlerRegistry, registrar)
	}
}

// DeclareMessaging calls DeclareMessaging on all registered modules to populate a shared declarations store.
// This method builds the declaration store that will be used for all tenant registries.
func (r *ModuleRegistry) DeclareMessaging(decls *messaging.Declarations) error {
	if decls == nil {
		return fmt.Errorf("declarations store is nil")
	}

	for _, module := range r.modules {
		r.logger.Info().
			Str("module", module.Name()).
			Msg("Collecting module messaging declarations")

		module.DeclareMessaging(decls)
	}

	// Validate all declarations after collection
	r.logger.Info().Msg("Validating messaging declarations")
	if err := decls.Validate(); err != nil {
		r.logger.Error().Err(err).Msg("Declaration validation failed")
		return fmt.Errorf("declaration validation failed: %w", err)
	}

	stats := decls.Stats()
	r.logger.Info().
		Int("exchanges", stats.Exchanges).
		Int("queues", stats.Queues).
		Int("bindings", stats.Bindings).
		Int("publishers", stats.Publishers).
		Int("consumers", stats.Consumers).
		Msg("Messaging declarations collected and validated successfully")

	return nil
}

// Shutdown gracefully shuts down all registered modules.
// It calls each module's Shutdown method and logs any errors.
// Messaging shutdown is handled by the messaging manager.
func (r *ModuleRegistry) Shutdown() error {
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
