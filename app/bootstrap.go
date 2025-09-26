package app

import (
	"github.com/gaborage/go-bricks/config"
	"github.com/gaborage/go-bricks/logger"
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
// Returns a bundle containing the database manager, messaging manager, and resource provider.
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

	// Create ModuleDeps using the resource provider
	deps := &ModuleDeps{
		Logger:       b.log,
		Config:       b.cfg,
		GetDB:        provider.GetDB,
		GetMessaging: provider.GetMessaging,
	}

	return &dependencyBundle{
		deps:             deps,
		dbManager:        dbManager,
		messagingManager: messagingManager,
		provider:         provider,
	}
}
