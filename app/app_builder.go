package app

import (
	"context"
	"errors"
	"fmt"
	"slices"

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
	if cfg == nil {
		b.err = errors.New("configuration required")
		return b
	}
	// Every construction path validates — config.Load output included (Validate
	// is idempotent, so its second run is a no-op). This is what lets the
	// managers drop their mirrored defaults: no config reaches them unstamped.
	if err := config.Validate(cfg); err != nil {
		b.err = fmt.Errorf("invalid configuration: %w", err)
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
		b.err = errors.New("configuration required before creating logger")
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
//     field names (substring-matched, case-insensitive). Trimming,
//     de-duplication and the dropping of empty entries happen in
//     logger.NewSensitiveDataFilter, where a list becomes a filter, so they
//     apply to route 1 as well — this function only merges.
//  3. nil — logger.NewWithFilter falls back to logger.DefaultFilterConfig
//     (identical to the legacy logger.New path; preserved for back-compat).
func resolveLoggerFilterConfig(opts *Options, cfg *config.LogConfig) *logger.FilterConfig {
	if opts != nil && opts.LoggerFilterConfig != nil {
		return opts.LoggerFilterConfig
	}
	if cfg != nil && len(cfg.SensitiveFields) > 0 {
		base := logger.DefaultFilterConfig()
		base.SensitiveFields = append(base.SensitiveFields, cfg.SensitiveFields...)
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
		b.err = errors.New("logger required before creating bootstrap")
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
		b.err = errors.New("bootstrap required before resolving dependencies")
		return b
	}

	bundle, err := b.bootstrap.dependencies(context.Background())
	if err != nil {
		b.err = fmt.Errorf("dependency resolution failed: %w", err)
		return b
	}

	// Synchronize the builder's logger with the enhanced instance returned from bootstrap.
	// No nil guard: a bundle only reaches here on success, dependencies always populates
	// deps, and enhanceLoggerWithOTel falls back to the bootstrap logger rather than nil.
	b.bundle = bundle
	b.logger = bundle.deps.Logger
	return b
}

// CreateApp creates the core App instance with basic configuration.
func (b *Builder) CreateApp() *Builder {
	if b.err != nil {
		return b
	}

	if b.bundle == nil {
		b.err = errors.New("dependencies required before creating app")
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

	// Slots are installed here, before ConfigureRuntimeHelpers runs pre-initialization over
	// them. cacheAbsent is the one verdict App cannot re-derive: it reads Options.
	b.app.installSlots(slotInputs{cacheAbsent: rootCacheAbsent(b.cfg, b.opts)})

	return b
}

// InitializeRegistry creates and configures the module registry.
func (b *Builder) InitializeRegistry() *Builder {
	if b.err != nil {
		return b
	}

	if b.app == nil {
		b.err = errors.New("app instance required before initializing registry")
		return b
	}

	registry := NewModuleRegistry(b.bundle.deps)
	// Set post-construction: NewModuleRegistry is shipped API and must keep its
	// signature byte-identical (apidiff gate).
	registry.rootDBAbsent = rootDatabaseAbsent(b.cfg, b.opts)
	b.app.registry = registry
	return b
}

// ConfigureRuntimeHelpers prepares helper components used during runtime.
func (b *Builder) ConfigureRuntimeHelpers() *Builder {
	if b.err != nil {
		return b
	}

	if b.app == nil {
		b.err = errors.New("app instance required before configuring runtime helpers")
		return b
	}

	// ADR-050: with the built-in connector, an untyped connection string can
	// never dispatch (database.NewConnection switches on Type). A custom
	// Options.DatabaseConnector owns DSN parsing and is exempt.
	if b.opts == nil || b.opts.DatabaseConnector == nil {
		if paths := config.UntypedDatabaseSections(b.cfg); len(paths) > 0 {
			b.err = fmt.Errorf(
				"database configuration at %v: connectionstring has no resolved database type; "+
					"set <path>.type to postgresql or oracle, or run config.Validate before configuring runtime helpers",
				paths)
			return b
		}
	}

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
// Every kind is pre-initialized by its own slot, in registration order, under its OWN
// context budget sourced from app.startup.{database,messaging,cache} — the documented
// three-level fallback (component value > app.startup.timeout > built-in default) is
// resolved earlier, in config.applyStartupDefaults. Whether a failure is fatal is the
// slot's own verdict: database and messaging abort startup (a misconfigured backing store
// should fail fast), while the cache stays best-effort, because an unreachable cache is a
// runtime condition — cache *misconfiguration* already aborted earlier, at manager
// construction (CreateCacheManager).
// requireSlots is the Builder-side half of App.requireSlots: it records the failure on
// b.err (the Builder's error-accumulation convention) instead of returning it.
func (b *Builder) requireSlots(step string) bool {
	if err := b.app.requireSlots(step); err != nil {
		b.err = err
		return false
	}
	return true
}

func (b *Builder) performPreInitialization() {
	if b.err != nil {
		return
	}
	if !b.requireSlots("pre-initialization") {
		return
	}
	// The slots read cfg for their configured/budget answers, so an App assembled without a
	// config (WithConfig never ran) has nothing to pre-initialize — skip the walk rather than
	// let a slot dereference nil.
	if b.app.cfg == nil {
		b.logger.Debug().Msg("Skipping pre-initialization: no config")
		return
	}

	// Single parent context for the whole pre-init phase; each slot derives its own budget
	// from it via startupContext so all of them share one cancellation lineage. The context
	// is threaded as a parameter (never stored on the builder), matching the framework's
	// startup-at-Background precedent.
	parent := context.Background()
	b.logger.Debug().Msg("Performing pre-initialization for static single-tenant mode")

	for _, slot := range b.app.slots {
		err := slot.preInit(parent)
		if err == nil {
			continue
		}
		if slot.preInitFatal() {
			b.err = fmt.Errorf("%s connection failed during startup: %w", slot.name(), err)
			return
		}
		b.logger.Warn().Err(err).Msgf("Failed to pre-initialize %s connection (non-fatal)", slot.name())
	}
}

// CreateHealthProbes creates health check probes for all managers. prepareRuntime
// re-collects the set after the start phase (streamsSlot.probe, slot.go), so this
// build-time list is only what an App built but never Run would report.
func (b *Builder) CreateHealthProbes() *Builder {
	if b.err != nil {
		return b
	}

	if b.app == nil {
		b.err = errors.New("app instance required before creating health probes")
		return b
	}

	if !b.requireSlots("creating health probes") {
		return b
	}

	b.warnIfCacheCriticalityOptOut()
	b.warnIfQueryParameterLogging()
	b.app.healthProbes = b.app.collectProbes()

	return b
}

// warnIfCacheCriticalityOptOut WARNs (never fails) when a live cache probe explicitly sets
// cache.critical=false, weakening readiness. No-op under the strict default (absent key)
// and when no probe can ever report an error.
func (b *Builder) warnIfCacheCriticalityOptOut() {
	cfg := b.app.cfg
	if cfg == nil || cfg.Cache.Critical == nil || *cfg.Cache.Critical {
		return
	}
	if b.app.cacheManager == nil {
		return
	}
	// The default Redis connector declines with a nil error when cache.enabled is false, so
	// the flag is inert; a custom Options.CacheConnector never reads that key and is probed
	// regardless.
	if !cfg.Cache.Enabled && (b.opts == nil || b.opts.CacheConnector == nil) {
		return
	}
	b.logger.Warn().
		Str("resource", "cache").
		Msg("cache.critical is explicitly false; readiness will not reflect cache health — " +
			"/ready keeps answering 200 while the cache is down, so a dead cache still reports ready " +
			"(remove cache.critical to restore the strict default)")
}

// warnIfQueryParameterLogging WARNs (never fails) when database.query.log.parameters is
// enabled outside development, for the root database or any named database
// (cfg.Databases — multi-DB single-tenant, see wiki/database.md). Bound parameter
// VALUES — on cardholder tables, the PAN — are then logged verbatim, and they bypass
// SensitiveDataFilter: it masks by field name, but parameters arrive as a positional
// []any under one "args" key, so no field-name needle can match. See config.example.yaml.
func (b *Builder) warnIfQueryParameterLogging() {
	cfg := b.app.cfg
	if cfg == nil || config.IsDevelopment(cfg.App.Env) {
		return
	}

	if cfg.Database.Query.Log.Parameters {
		b.logger.Warn().Msg("database.query.log.parameters is enabled: bound parameter values (possible PII/PAN) will be logged verbatim")
	}

	names := make([]string, 0, len(cfg.Databases))
	for name := range cfg.Databases {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		if cfg.Databases[name].Query.Log.Parameters {
			b.logger.Warn().Msgf("database.query.log.parameters is enabled for named database %q: "+
				"bound parameter values (possible PII/PAN) will be logged verbatim", name)
		}
	}
}

// RegisterClosers registers all components that need cleanup on shutdown.
func (b *Builder) RegisterClosers() *Builder {
	if b.err != nil {
		return b
	}

	if b.app == nil {
		b.err = errors.New("app instance required before registering closers")
		return b
	}

	if !b.requireSlots("registering closers") {
		return b
	}

	// Each slot decides whether it has anything to close; the nil checks that avoided typed
	// nil interfaces now live in the slots' closer methods.
	b.app.registerSlotClosers()
	return b
}

// RegisterReadyHandler registers the health check handler with the server.
func (b *Builder) RegisterReadyHandler() *Builder {
	if b.err != nil {
		return b
	}

	if b.app == nil {
		b.err = errors.New("app instance required before registering ready handler")
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
		b.closeBundleManagers()
		return nil, log, b.err
	}

	if b.app == nil {
		// A hand-driven chain that resolved dependencies but never built the app
		// strands the same managers as an error does.
		b.closeBundleManagers()
		return nil, log, errors.New("app building incomplete")
	}

	return b.app, log, nil
}

// closeBundleManagers stops the background work the bundle's three managers started at
// construction when a step AFTER ResolveDependencies aborts — the ADR-050 untyped-connectionstring
// abort in ConfigureRuntimeHelpers, or a fatal slot pre-initialization. Build returns
// (nil, log, err) there, so no App is ever assembled and the close phase never runs over them:
// a host that retries app.New after a broker outage would otherwise leak one idle-cleanup sweep
// per manager per attempt. A no-op when the failure predates the bundle (b.bundle nil), which
// covers every guard before ResolveDependencies as well as its own failure — that path closes
// its own partial set (closeManagersOnDependencyError).
func (b *Builder) closeBundleManagers() {
	if b.bundle == nil {
		return
	}
	closeManagersOnDependencyError(b.bundle.dbManager, b.bundle.messagingManager)
	// The cache manager is the third: unlike the ADR-067 pair it has self-started its sweep all
	// along, so it strands the same way and dependencies() never has one to close.
	if b.bundle.cacheManager != nil {
		_ = b.bundle.cacheManager.Close()
	}
}

// Error returns any error encountered during the building process.
func (b *Builder) Error() error {
	return b.err
}
