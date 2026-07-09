package app

import (
	"context"
	"fmt"
	"strings"
	"time"

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

	pretty := logger.ResolvePretty(
		b.cfg.Log.Output.Format,
		b.cfg.Log.Pretty,
		otlpLogsActive(b.cfg),
		logger.StdoutIsTerminal(),
	)
	b.logger = logger.NewWithFilter(b.cfg.Log.Level, pretty, resolveLoggerFilterConfig(b.opts, &b.cfg.Log))
	b.logger.Info().
		Str("app", b.cfg.App.Name).
		Str("env", b.cfg.App.Env).
		Str("version", b.cfg.App.Version).
		Msg("Starting application")

	return b
}

// resolveLoggerFilterConfig picks the sensitive-data filter applied to the
// framework logger. Precedence:
//
//  1. opts.LoggerFilterConfig — full replacement; consumer is in control.
//  2. cfg.SensitiveFields — extends logger.DefaultFilterConfig with custom
//     field names (substring-matched, case-insensitive). Values are
//     trimmed and case-insensitively de-duplicated against the defaults
//     and each other; empty entries are dropped. An un-normalized empty
//     string would make strings.Contains match every field, silently
//     masking the entire log stream.
//  3. nil — logger.NewWithFilter falls back to logger.DefaultFilterConfig
//     (identical to the legacy logger.New path; preserved for back-compat).
func resolveLoggerFilterConfig(opts *Options, cfg *config.LogConfig) *logger.FilterConfig {
	if opts != nil && opts.LoggerFilterConfig != nil {
		return opts.LoggerFilterConfig
	}
	if cfg != nil && len(cfg.SensitiveFields) > 0 {
		base := logger.DefaultFilterConfig()
		seen := make(map[string]struct{}, len(base.SensitiveFields)+len(cfg.SensitiveFields))
		for _, f := range base.SensitiveFields {
			seen[strings.ToLower(strings.TrimSpace(f))] = struct{}{}
		}
		for _, raw := range cfg.SensitiveFields {
			field := strings.TrimSpace(raw)
			if field == "" {
				continue
			}
			key := strings.ToLower(field)
			if _, exists := seen[key]; exists {
				continue
			}
			seen[key] = struct{}{}
			base.SensitiveFields = append(base.SensitiveFields, field)
		}
		return base
	}
	return nil
}

// otlpLogsActive reports whether OTLP log export will run after bootstrap.
// Reads raw koanf keys because observability.Config is unmarshaled later in
// initializeObservability, and the logger is created first. Mirrors the
// defaulting rule in observability.Config.applyLogsDefaults: when
// observability is enabled, logs default to enabled unless explicitly off.
func otlpLogsActive(cfg *config.Config) bool {
	return cfg != nil &&
		cfg.Bool("observability.enabled", false) &&
		cfg.Bool("observability.logs.enabled", true)
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

	b.bundle = b.bootstrap.dependencies(context.Background())
	if b.bundle != nil && b.bundle.deps != nil && b.bundle.deps.Logger != nil {
		// Synchronize the builder's logger with the enhanced instance returned from bootstrap.
		b.logger = b.bundle.deps.Logger
	}
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
		observability:    b.bundle.observability,
		dbManager:        b.bundle.dbManager,
		messagingManager: b.bundle.messagingManager,
		cacheManager:     b.bundle.cacheManager,
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
	// Thread the operator's readiness budget (messaging.reconnect.readytimeout)
	// into the pre-warm wait. Set post-construction: NewConnectionPreWarmer is
	// shipped API and must keep its signature byte-identical (apidiff gate).
	b.app.connectionPreWarmer.readinessTimeout = b.cfg.Messaging.Reconnect.ReadyTimeout

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
//
// Each component is pre-initialized under its OWN context budget sourced from
// app.startup.{database,messaging,cache}, honoring the documented three-level
// fallback hierarchy (component value > app.startup.timeout > built-in default,
// resolved earlier in config.applyStartupDefaults). Database and messaging
// failures are fatal (a misconfigured backing store should fail fast at startup);
// cache pre-init is best-effort, matching the manager-creation contract where a
// failing cache is disabled rather than crashing the app.
func (b *Builder) performPreInitialization() {
	if b.err != nil {
		return
	}

	// Single parent context for the whole pre-init phase; each component derives
	// its own budget from it via context.WithTimeout so the three share one
	// cancellation lineage. The context is threaded as a parameter (never stored
	// on the builder), matching the framework's startup-at-Background precedent.
	parent := context.Background()
	startup := b.cfg.App.Startup
	b.logger.Debug().Msg("Performing pre-initialization for static single-tenant mode")

	if !b.preInitDatabase(parent, startup.Database) {
		return
	}
	if !b.preInitMessaging(parent, startup.Messaging) {
		return
	}
	b.preInitCache(parent, startup.Cache)
}

// preInitDatabase pre-initializes the database connection under its own startup
// budget. Returns false (and sets b.err) on a fatal failure so the caller stops.
func (b *Builder) preInitDatabase(parent context.Context, timeout time.Duration) bool {
	if b.bundle.dbManager == nil {
		return true
	}
	return b.preInitFatalComponent(
		parent,
		"database",
		timeout,
		config.IsDatabaseConfigured(&b.cfg.Database),
		func(ctx context.Context) error {
			_, release, err := b.bundle.dbManager.Get(ctx, "")
			if err != nil {
				return err
			}
			release() // startup probe only verifies connectivity; release the lease immediately
			return nil
		},
	)
}

// preInitMessaging pre-initializes the messaging publisher under its own startup
// budget. Returns false (and sets b.err) on a fatal failure so the caller stops.
func (b *Builder) preInitMessaging(parent context.Context, timeout time.Duration) bool {
	if b.bundle.messagingManager == nil {
		return true
	}
	return b.preInitFatalComponent(
		parent,
		"messaging",
		timeout,
		config.IsMessagingConfigured(&b.cfg.Messaging),
		func(ctx context.Context) error {
			_, release, err := b.bundle.messagingManager.Publisher(ctx, "")
			if err != nil {
				return err
			}
			release() // startup probe only verifies connectivity; release the lease immediately
			return nil
		},
	)
}

// preInitFatalComponent pre-initializes a startup-fatal component (database or
// messaging) under its own per-component budget derived from the supplied parent.
// The component is skipped when not configured; a connection failure is fatal
// (sets b.err) and stops the remaining pre-init steps by returning false.
func (b *Builder) preInitFatalComponent(
	parent context.Context,
	name string,
	timeout time.Duration,
	configured bool,
	connect func(ctx context.Context) error,
) bool {
	if !configured {
		b.logger.Debug().Msgf("Skipping %s pre-initialization: not configured", name)
		return true
	}

	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()

	if err := connect(ctx); err != nil {
		b.err = fmt.Errorf("%s connection failed during startup: %w", name, err)
		return false
	}
	b.logger.Debug().Msgf("Pre-initialized %s connection", name)
	return true
}

// preInitCache pre-initializes the cache connection under its own startup budget.
// Cache is optional: a not-configured cache is skipped silently and any other
// failure is logged as a warning without aborting startup, mirroring the
// manager-creation contract (a failing cache is disabled, not fatal).
func (b *Builder) preInitCache(parent context.Context, timeout time.Duration) {
	if b.bundle.cacheManager == nil {
		return
	}

	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()

	_, release, err := b.bundle.cacheManager.Get(ctx, "")
	if err != nil {
		if config.IsNotConfigured(err) {
			b.logger.Debug().Msg("Skipping cache pre-initialization: not configured")
			return
		}
		b.logger.Warn().Err(err).Msg("Failed to pre-initialize cache connection (non-fatal)")
		return
	}
	release() // startup probe only verifies connectivity; release the lease immediately
	b.logger.Debug().Msg("Pre-initialized cache connection")
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
		b.app.cacheManager,
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

	// Register closers with explicit nil checks to avoid typed nil interface issues
	if b.app.dbManager != nil {
		b.app.registerCloser("database manager", b.app.dbManager)
	}
	if b.app.messagingManager != nil {
		b.app.registerCloser("messaging manager", b.app.messagingManager)
	}
	if b.app.cacheManager != nil {
		b.app.registerCloser("cache manager", b.app.cacheManager)
	}
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

// Error returns any error encountered during the building process.
func (b *Builder) Error() error {
	return b.err
}
