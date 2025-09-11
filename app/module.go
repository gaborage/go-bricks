package app

import (
	"context"

	"github.com/labstack/echo/v4"

	"github.com/gaborage/go-bricks/database"
	"github.com/gaborage/go-bricks/logger"
	"github.com/gaborage/go-bricks/messaging"
)

// Module defines the interface that all application modules must implement.
// It provides hooks for initialization, route registration, messaging setup, and cleanup.
type Module interface {
	Name() string
	Init(deps *ModuleDeps) error
	RegisterRoutes(e *echo.Echo)
	RegisterMessaging(registry *messaging.Registry)
	Shutdown() error
}

// ModuleDeps contains the dependencies that are injected into each module.
// It provides access to core services like database, logging, and messaging.
type ModuleDeps struct {
	DB        database.Interface
	Logger    logger.Logger
	Messaging messaging.Client
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
	r.logger.Info().
		Str("module", module.Name()).
		Msg("Registering module")

	if err := module.Init(r.deps); err != nil {
		return err
	}

	r.modules = append(r.modules, module)
	return nil
}

// RegisterRoutes calls RegisterRoutes on all registered modules.
// It should be called after all modules have been registered.
func (r *ModuleRegistry) RegisterRoutes(e *echo.Echo) {
	for _, module := range r.modules {
		r.logger.Info().
			Str("module", module.Name()).
			Msg("Registering module routes")

		module.RegisterRoutes(e)
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
