package app

import (
	"context"
	"fmt"

	"github.com/gaborage/go-bricks/config"
	"github.com/gaborage/go-bricks/logger"
)

// Builder orchestrates the step-by-step construction of an App instance
// using a fluent interface pattern. Each step is responsible for a single
// aspect of initialization, making the process clear and testable.
type Builder struct {
	// Configuration
	cfg  *config.Config
	opts *Options

	// Core components
	logger    logger.Logger
	bootstrap *appBootstrap
	bundle    *dependencyBundle
	app       *App

	// State tracking
	err error
}

// NewAppBuilder creates a new app builder instance.
func NewAppBuilder() *Builder {
	return &Builder{}
}

// WithConfig sets the configuration and options for the app.
func (b *Builder) WithConfig(cfg *config.Config, opts *Options) *Builder {
	if b.err != nil {
		return b
	}

	b.cfg = cfg
	b.opts = opts
	return b
}

// CreateLogger creates and configures the application logger.
func (b *Builder) CreateLogger() *Builder {
	if b.err != nil {
		return b
	}

	if b.cfg == nil {
		b.err = fmt.Errorf("configuration required before creating logger")
		return b
	}

	b.logger = logger.New(b.cfg.Log.Level, b.cfg.Log.Pretty)
	b.logger.Info().
		Str("app", b.cfg.App.Name).
		Str("env", b.cfg.App.Env).
		Str("version", b.cfg.App.Version).
		Msg("Starting application")

	return b
}

// CreateBootstrap creates the bootstrap helper for dependency resolution.
func (b *Builder) CreateBootstrap() *Builder {
	if b.err != nil {
		return b
	}

	if b.logger == nil {
		b.err = fmt.Errorf("logger required before creating bootstrap")
		return b
	}

	b.bootstrap = newAppBootstrap(b.cfg, b.logger, b.opts)
	return b
}

// ResolveDependencies creates and configures all application dependencies.
func (b *Builder) ResolveDependencies() *Builder {
	if b.err != nil {
		return b
	}

	if b.bootstrap == nil {
		b.err = fmt.Errorf("bootstrap required before resolving dependencies")
		return b
	}

	b.bundle = b.bootstrap.dependencies()
	return b
}

// CreateApp creates the core App instance with basic configuration.
func (b *Builder) CreateApp() *Builder {
	if b.err != nil {
		return b
	}

	if b.bundle == nil {
		b.err = fmt.Errorf("dependencies required before creating app")
		return b
	}

	signalHandler, timeoutProvider, srv := b.bootstrap.coreComponents()

	b.app = &App{
		cfg:              b.cfg,
		server:           srv,
		logger:           b.logger,
		registry:         nil, // Will be set in next step
		signalHandler:    signalHandler,
		timeoutProvider:  timeoutProvider,
		dbManager:        b.bundle.dbManager,
		messagingManager: b.bundle.messagingManager,
		resourceProvider: b.bundle.provider,
	}

	return b
}

// InitializeRegistry creates and configures the module registry.
func (b *Builder) InitializeRegistry() *Builder {
	if b.err != nil {
		return b
	}

	if b.app == nil {
		b.err = fmt.Errorf("app instance required before initializing registry")
		return b
	}

	registry := NewModuleRegistry(b.bundle.deps)
	b.app.registry = registry
	return b
}

// ConfigureRuntimeHelpers prepares helper components used during runtime.
func (b *Builder) ConfigureRuntimeHelpers() *Builder {
	if b.err != nil {
		return b
	}

	if b.app == nil {
		b.err = fmt.Errorf("app instance required before configuring runtime helpers")
		return b
	}

	b.app.messagingInitializer = NewMessagingInitializer(b.logger, b.app.messagingManager, b.cfg.Multitenant.Enabled)
	b.app.connectionPreWarmer = NewConnectionPreWarmer(b.logger, b.app.dbManager, b.app.messagingManager)

	// Determine if we should skip pre-initialization
	// Skip for multi-tenant mode (resources loaded per-tenant)
	skipPreInit := b.cfg.Multitenant.Enabled

	// Skip for dynamic source configuration
	if b.cfg.Source.Type == config.SourceTypeDynamic {
		skipPreInit = true
		b.logger.Info().Msg("Dynamic source type detected - skipping pre-initialization")
	}

	// Skip if custom resource source declares itself as dynamic
	if b.opts != nil && b.opts.ResourceSource != nil && b.opts.ResourceSource.IsDynamic() {
		skipPreInit = true
		b.logger.Info().Msg("Dynamic resource store detected - skipping pre-initialization")
	}

	// Only pre-initialize for single-tenant mode with static configuration
	if !skipPreInit {
		b.performPreInitialization()
	}

	return b
}

// performPreInitialization attempts to establish connections during app startup.
// This reduces cold-start latency for single-tenant applications.
func (b *Builder) performPreInitialization() {
	if b.err != nil {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), b.cfg.App.Startup.Timeout)
	defer cancel()
	b.logger.Debug().Msg("Performing pre-initialization for static single-tenant mode")

	// Pre-initialize database if configured
	if b.bundle.dbManager != nil && config.IsDatabaseConfigured(&b.cfg.Database) {
		if _, err := b.bundle.dbManager.Get(ctx, ""); err != nil {
			b.err = fmt.Errorf("database connection failed during startup: %w", err)
			return
		}
		b.logger.Debug().Msg("Pre-initialized database connection")
	} else if b.bundle.dbManager != nil {
		b.logger.Debug().Msg("Skipping database pre-initialization: not configured")
	}

	// Pre-initialize messaging if configured
	if b.bundle.messagingManager != nil && config.IsMessagingConfigured(&b.cfg.Messaging) {
		if _, err := b.bundle.messagingManager.GetPublisher(ctx, ""); err != nil {
			b.err = fmt.Errorf("messaging connection failed during startup: %w", err)
			return
		}
		b.logger.Debug().Msg("Pre-initialized messaging publisher")
	} else if b.bundle.messagingManager != nil {
		b.logger.Debug().Msg("Skipping messaging pre-initialization: not configured")
	}
}

// CreateHealthProbes creates health check probes for all managers.
func (b *Builder) CreateHealthProbes() *Builder {
	if b.err != nil {
		return b
	}

	if b.app == nil {
		b.err = fmt.Errorf("app instance required before creating health probes")
		return b
	}

	b.app.healthProbes = createHealthProbesForManagers(
		b.app.dbManager,
		b.app.messagingManager,
		b.logger,
	)

	return b
}

// RegisterClosers registers all components that need cleanup on shutdown.
func (b *Builder) RegisterClosers() *Builder {
	if b.err != nil {
		return b
	}

	if b.app == nil {
		b.err = fmt.Errorf("app instance required before registering closers")
		return b
	}

	b.app.registerCloser("database manager", b.app.dbManager)
	b.app.registerCloser("messaging manager", b.app.messagingManager)
	return b
}

// RegisterReadyHandler registers the health check handler with the server.
func (b *Builder) RegisterReadyHandler() *Builder {
	if b.err != nil {
		return b
	}

	if b.app == nil {
		b.err = fmt.Errorf("app instance required before registering ready handler")
		return b
	}

	b.app.server.RegisterReadyHandler(b.app.readyCheck)
	return b
}

// Build returns the completed App instance, logger, or any error encountered during building.
// The logger is always returned, even on error, to enable proper error logging.
func (b *Builder) Build() (*App, logger.Logger, error) {
	// Ensure we always have a logger available
	log := b.logger
	if log == nil {
		// Fallback to a bootstrap logger if no logger was created
		log = createBootstrapLogger()
	}

	if b.err != nil {
		return nil, log, b.err
	}

	if b.app == nil {
		return nil, log, fmt.Errorf("app building incomplete")
	}

	return b.app, log, nil
}

// GetError returns any error encountered during the building process.
func (b *Builder) GetError() error {
	return b.err
}
