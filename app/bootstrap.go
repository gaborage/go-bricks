package app

import (
	"github.com/gaborage/go-bricks/config"
	"github.com/gaborage/go-bricks/logger"
	"github.com/gaborage/go-bricks/observability"
)

// appBootstrap handles the initialization sequence for creating an App instance.
// It encapsulates the step-by-step process of setting up all dependencies.
type appBootstrap struct {
	cfg  *config.Config
	log  logger.Logger
	opts *Options
}

// newAppBootstrap creates a new bootstrap helper with the provided configuration.
func newAppBootstrap(cfg *config.Config, log logger.Logger, opts *Options) *appBootstrap {
	return &appBootstrap{cfg: cfg, log: log, opts: opts}
}

// coreComponents resolves and creates the core application components.
// Returns the signal handler, timeout provider, and server runner instances.
func (b *appBootstrap) coreComponents() (SignalHandler, TimeoutProvider, ServerRunner) {
	signalHandler, timeoutProvider := resolveSignalAndTimeout(b.opts)
	return signalHandler, timeoutProvider, resolveServer(b.cfg, b.log, b.opts)
}

// dependencies creates and configures all resource managers and dependencies.
// Returns a bundle containing the database manager, messaging manager, resource provider, and observability.
func (b *appBootstrap) dependencies() *dependencyBundle {
	// Create factory resolver and configuration builder
	resolver := NewFactoryResolver(b.opts)
	configBuilder := NewManagerConfigBuilder(b.cfg.Multitenant.Enabled, b.cfg.Multitenant.Limits.Tenants)
	factory := NewResourceManagerFactory(resolver, configBuilder, b.log)

	// Log factory configuration for debugging
	factory.LogFactoryInfo()

	// Resolve resource source
	resourceSource := resolver.ResourceSource(b.cfg)

	// Create managers using the factory
	dbManager := factory.CreateDatabaseManager(resourceSource)
	messagingManager := factory.CreateMessagingManager(resourceSource)

	// Create appropriate resource provider based on mode
	var provider ResourceProvider
	if b.cfg.Multitenant.Enabled {
		provider = NewMultiTenantResourceProvider(dbManager, messagingManager, nil)
	} else {
		provider = NewSingleTenantResourceProvider(dbManager, messagingManager, nil)
	}

	// Initialize observability provider (no-op if disabled)
	obsProvider := b.initializeObservability()

	// Create ModuleDeps using the resource provider and observability
	deps := &ModuleDeps{
		Logger:        b.log,
		Config:        b.cfg,
		Tracer:        obsProvider.TracerProvider().Tracer(b.cfg.App.Name),
		MeterProvider: obsProvider.MeterProvider(),
		GetDB:         provider.GetDB,
		GetMessaging:  provider.GetMessaging,
	}

	return &dependencyBundle{
		deps:             deps,
		dbManager:        dbManager,
		messagingManager: messagingManager,
		provider:         provider,
		observability:    obsProvider,
	}
}

// initializeObservability creates and configures the observability provider.
// Returns a no-op provider if observability is disabled or configuration is missing.
func (b *appBootstrap) initializeObservability() observability.Provider {
	// Create observability config
	var obsCfg observability.Config

	// Try to inject configuration
	if err := b.cfg.InjectInto(&obsCfg); err != nil {
		// Configuration missing or invalid - use defaults (observability disabled)
		b.log.Debug().Err(err).Msg("Observability configuration not found or invalid, using no-op provider")
		return observability.MustNewProvider(&observability.Config{Enabled: false})
	}

	// Create provider (will be no-op if Enabled is false)
	provider, err := observability.NewProvider(&obsCfg)
	if err != nil {
		b.log.Warn().Err(err).Msg("Failed to initialize observability, using no-op provider")
		return observability.MustNewProvider(&observability.Config{Enabled: false})
	}

	if obsCfg.Enabled {
		b.log.Info().
			Str("service", obsCfg.ServiceName).
			Str("environment", obsCfg.Environment).
			Msg("Observability initialized")
	} else {
		b.log.Debug().Msg("Observability disabled")
	}

	return provider
}
