package app

import (
	"fmt"

	"github.com/gaborage/go-bricks/jose"
	"github.com/gaborage/go-bricks/logger"
	"github.com/gaborage/go-bricks/messaging"
	"github.com/gaborage/go-bricks/server"
)

// ModuleRegistry manages the registration and lifecycle of application modules.
// It handles module initialization, route registration, messaging setup, and shutdown.
type ModuleRegistry struct {
	modules         []Module
	deps            *ModuleDeps
	logger          logger.Logger
	registeredNames map[string]Module // Tracks registered modules to prevent duplicates
}

// NewModuleRegistry creates a new module registry with the given dependencies.
// It initializes an empty registry ready to accept module registrations.
func NewModuleRegistry(deps *ModuleDeps) *ModuleRegistry {
	return &ModuleRegistry{
		modules:         make([]Module, 0),
		deps:            deps,
		logger:          deps.Logger,
		registeredNames: make(map[string]Module),
	}
}

// Register adds a module to the registry and initializes it.
// It calls the module's Init method with the injected dependencies.
// Returns an error if a module with the same name is already registered.
// Special handling: If the module implements app.JobRegistrar, it is automatically
// wired into ModuleDeps.Scheduler for other modules to use.
//
// IMPORTANT: Duplicate module errors are unrecoverable and must be handled with log.Fatal().
func (r *ModuleRegistry) Register(module Module) error {
	moduleName := module.Name()

	// Deduplication: check if module already registered
	if existing, ok := r.registeredNames[moduleName]; ok {
		return fmt.Errorf(
			"module registry: duplicate module '%s' detected (already registered %T)",
			moduleName, existing,
		)
	}

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

	// Special case: If this module is an OutboxProvider (outbox module),
	// make it available to other modules via deps.Outbox
	if outboxProvider, ok := module.(OutboxProvider); ok {
		r.deps.Outbox = outboxProvider.OutboxPublisher()
		r.logger.Info().
			Str("module", moduleName).
			Msg("Outbox module registered - available to other modules via deps.Outbox")
	}

	// Special case: If this module is a KeyStoreProvider (keystore module),
	// make it available to other modules via deps.KeyStore
	if ksProvider, ok := module.(KeyStoreProvider); ok {
		if r.deps.KeyStore != nil {
			return fmt.Errorf(
				"module registry: multiple KeyStore providers detected (module %q attempted to override existing provider)",
				moduleName,
			)
		}
		r.deps.KeyStore = ksProvider.KeyStore()
		r.logger.Info().
			Str("module", moduleName).
			Msg("KeyStore module registered - available to other modules via deps.KeyStore")
	}

	// Add to deduplication map BEFORE appending to modules slice
	r.registeredNames[moduleName] = module

	// Add to lifecycle registry
	r.modules = append(r.modules, module)

	// Register with metadata registry for introspection
	DefaultModuleRegistry.RegisterModule(moduleName, module, getModulePackage(module))

	r.logger.Info().
		Str("module", moduleName).
		Msg("Registered module")

	return nil
}

// RegisterRoutes calls RegisterRoutes on modules that implement RouteRegisterer.
// Modules without routes are silently skipped.
//
// If a KeyStore-providing module has registered (and r.deps.KeyStore is therefore
// populated), a jose.KeyStoreResolver is wired into the handler registry so any route
// declaring jose: tags can resolve its kids at registration time. Logger, tracer, and
// meter from deps are also threaded into the registry so JOSE failures get audit-grade
// structured logs and OTEL telemetry. Routes without jose tags are unaffected.
func (r *ModuleRegistry) RegisterRoutes(registrar server.RouteRegistrar) {
	opts := []server.HandlerRegistryOption{}
	if r.deps.KeyStore != nil {
		opts = append(opts, server.WithJOSEResolver(jose.NewKeyStoreResolver(r.deps.KeyStore)))
	}
	if r.deps.Logger != nil {
		opts = append(opts, server.WithJOSELogger(r.deps.Logger))
	}
	if r.deps.Tracer != nil {
		opts = append(opts, server.WithJOSETracer(r.deps.Tracer))
	}
	if r.deps.MeterProvider != nil {
		opts = append(opts, server.WithJOSEMeterProvider(r.deps.MeterProvider))
	}
	handlerRegistry := server.NewHandlerRegistry(r.deps.Config, opts...)

	for _, module := range r.modules {
		if rr, ok := module.(RouteRegisterer); ok {
			r.logger.Info().
				Str("module", module.Name()).
				Msg("Registering module routes")

			rr.RegisterRoutes(handlerRegistry, registrar)
		}
	}
}

// DeclareMessaging calls DeclareMessaging on modules that implement MessagingDeclarer.
// Modules without messaging declarations are silently skipped.
func (r *ModuleRegistry) DeclareMessaging(decls *messaging.Declarations) error {
	if decls == nil {
		return fmt.Errorf("declarations store is nil")
	}

	for _, module := range r.modules {
		if md, ok := module.(MessagingDeclarer); ok {
			r.logger.Info().
				Str("module", module.Name()).
				Msg("Collecting module messaging declarations")

			md.DeclareMessaging(decls)
		}
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

// RegisterJobs calls RegisterJobs on modules that implement JobProvider interface.
// This method is called after all modules have been initialized, making module registration
// order irrelevant for job scheduling. If no scheduler is registered, this method skips silently.
func (r *ModuleRegistry) RegisterJobs() error {
	// Skip if no scheduler registered
	if r.deps.Scheduler == nil {
		r.logger.Debug().Msg("No scheduler registered, skipping job registration")
		return nil
	}

	jobProviderCount := 0
	for _, module := range r.modules {
		if jobProvider, ok := module.(JobProvider); ok {
			jobProviderCount++
			r.logger.Info().
				Str("module", module.Name()).
				Msg("Registering module jobs")

			if err := jobProvider.RegisterJobs(r.deps.Scheduler); err != nil {
				return fmt.Errorf("module '%s' job registration failed: %w", module.Name(), err)
			}
		}
	}

	if jobProviderCount > 0 {
		r.logger.Info().
			Int("modules", jobProviderCount).
			Msg("Job registration completed successfully")
	}

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
