package app

import (
	"context"
	"fmt"
	"strconv"

	"github.com/gaborage/go-bricks/config"
	"github.com/gaborage/go-bricks/database"
	"github.com/gaborage/go-bricks/logger"
	"github.com/gaborage/go-bricks/messaging"
	"github.com/gaborage/go-bricks/observability"
)

// appBootstrap handles the initialization sequence for creating an App instance.
// It encapsulates the step-by-step process of setting up all dependencies.
type appBootstrap struct {
	cfg  *config.Config
	log  logger.Logger
	opts *Options

	// newProvider constructs the observability provider under the supplied
	// context. Defaults to observability.NewProviderWithContext; overridable
	// in tests to drive the startup-budget timeout path deterministically.
	newProvider func(context.Context, *observability.Config) (observability.Provider, error)

	// closeManagers stops dbManager/messagingManager's idle-cleanup sweeps when a later
	// manager fails to build (see closeManagersOnDependencyError). Defaults to that
	// function; overridable in tests to observe the exact instances dependencies()
	// constructed before it discards them on the fail-closed cache path.
	closeManagers func(*database.DbManager, *messaging.Manager)
}

// newAppBootstrap creates a new bootstrap helper with the provided configuration.
func newAppBootstrap(cfg *config.Config, log logger.Logger, opts *Options) *appBootstrap {
	return &appBootstrap{cfg: cfg, log: log, opts: opts}
}

// coreComponents resolves and creates the core application components.
// Returns the signal handler and server runner instances.
func (b *appBootstrap) coreComponents() (SignalHandler, ServerRunner) {
	return resolveSignalHandler(b.opts), resolveServer(b.cfg, b.log, b.opts)
}

// newManagerConfigBuilderFromConfig copies every operator-tunable manager setting
// from validated config into the builder — the seam where a key silently reverts
// to validated-but-ignored if an assignment is dropped or cross-wired (#662).
func newManagerConfigBuilderFromConfig(cfg *config.Config, plan resourcePlan) *ManagerConfigBuilder {
	configBuilder := NewManagerConfigBuilder(cfg.Multitenant.Enabled, cfg.Multitenant.Limits.Tenants)
	configBuilder.connectionTimeout = cfg.Messaging.Reconnect.ConnectionTimeout
	configBuilder.maxPublishAttempts = cfg.Messaging.Reconnect.MaxPublishAttempts
	configBuilder.readyTimeout = cfg.Messaging.Reconnect.ReadyTimeout
	configBuilder.publishTimeout = cfg.Messaging.PublishTimeout
	configBuilder.reconnectDelay = cfg.Messaging.Reconnect.Delay
	configBuilder.reconnectMaxDelay = cfg.Messaging.Reconnect.MaxDelay
	configBuilder.reInitDelay = cfg.Messaging.Reconnect.ReinitDelay
	configBuilder.resendDelay = cfg.Messaging.Reconnect.ResendDelay
	configBuilder.appName = cfg.App.Name
	configBuilder.tenantStamps = plan.tenantStamps()
	configBuilder.publisherConfig = cfg.Messaging.Publisher
	configBuilder.cacheConfig = cfg.Cache.Manager
	configBuilder.dbConfig = cfg.Database.Manager
	// Only count statically-configured tenants when multitenancy is enabled. Koanf
	// populates Multitenant.Tenants from YAML regardless of the enabled flag, but
	// those entries are meaningless in single-tenant mode (mirrors the guard in
	// config/tenant_store.go). Without this gate, leftover/shared tenants entries
	// would trip a spurious pool-below-tenant-count WARN even though single-tenant
	// pools are never per-tenant keyed — and would contradict StaticTenantCount's
	// documented "0 for single-tenant" contract.
	if cfg.Multitenant.Enabled {
		configBuilder.staticTenantCount = len(cfg.Multitenant.Tenants)
	}
	return configBuilder
}

// closeManagersOnDependencyError stops the idle-cleanup sweep each manager started at
// construction (ADR-067) when a later manager in the same dependencies() call fails to
// build. dependencies() returns (nil, err) on that path and the builder never assembles
// an App, so nothing downstream ever gets a handle to these two — this call is all that can
// stop their goroutines (Builder.closeBundleManagers is the sibling for an abort after the
// bundle exists, and reuses this). Nil-guarded: the factory never returns a nil
// *database.DbManager or *messaging.Manager today, but Close on a nil pointer would
// panic, and this stays correct if that ever changes.
func closeManagersOnDependencyError(dbManager *database.DbManager, messagingManager *messaging.Manager) {
	if dbManager != nil {
		_ = dbManager.Close()
	}
	if messagingManager != nil {
		_ = messagingManager.Close()
	}
}

// dependencies creates and configures all resource managers and dependencies.
// Returns a bundle containing the database manager, messaging manager, cache manager, resource provider, and observability.
// dbManager and messagingManager each hold a cleanup goroutine from construction (ADR-067),
// so a manager that cannot be constructed from the supplied configuration must close
// whichever of the two already exist before aborting startup — no bundle exists yet, so
// Builder.closeBundleManagers cannot reach them (see closeManagersOnDependencyError).
func (b *appBootstrap) dependencies(startupCtx context.Context) (*dependencyBundle, error) {
	resolver := newFactoryResolverForConfig(b.opts, b.cfg)
	resourceSource := resolver.ResourceSource(b.cfg)
	builtInStore := b.opts == nil || b.opts.ResourceSource == nil
	plan, err := planResources(startupCtx, b.cfg, b.opts, resourceSource, builtInStore)
	if err != nil {
		return nil, err
	}
	configBuilder := newManagerConfigBuilderFromConfig(b.cfg, plan)
	factory := NewResourceManagerFactory(resolver, configBuilder, b.log)

	factory.LogFactoryInfo()

	// Gate DB-operation OpenTelemetry spans/metrics on observability.enabled before
	// any connection is created. Honors the no-op provider's zero-overhead contract:
	// with observability off the tracking layer builds no span/metric attributes.
	database.SetObservabilityEnabled(b.cfg.Bool("observability.enabled", false))

	// Create managers using the factory
	dbManager := factory.CreateDatabaseManager(resourceSource)
	b.warnIfDatabaseAbsent(plan.database)
	messagingManager := factory.CreateMessagingManager(resourceSource)
	cacheManager, err := factory.CreateCacheManager(resourceSource)
	if err != nil {
		closeManagers := b.closeManagers
		if closeManagers == nil {
			closeManagers = closeManagersOnDependencyError
		}
		closeManagers(dbManager, messagingManager)
		return nil, err
	}

	// Create appropriate resource provider based on mode
	var provider ResourceProvider
	if plan.multitenant() {
		mtProvider := NewMultiTenantResourceProvider(dbManager, messagingManager, cacheManager, nil)
		mtProvider.SetMessagingTenancy(plan.messaging.tenancy.String())
		provider = mtProvider
	} else {
		provider = NewSingleTenantResourceProvider(dbManager, messagingManager, cacheManager, nil)
	}

	// Initialize observability provider (no-op if disabled)
	obsProvider, err := b.initializeObservability(startupCtx)
	if err != nil {
		if cacheManager != nil {
			_ = cacheManager.Close()
		}
		closeManagers := b.closeManagers
		if closeManagers == nil {
			closeManagers = closeManagersOnDependencyError
		}
		closeManagers(dbManager, messagingManager)
		return nil, err
	}

	// Enhance logger with OTLP export if enabled
	// This upgrades the bootstrap logger so all subsequent components share a single
	// stdout + OTLP (or OTLP-only) instance.
	enhancedLogger := b.enhanceLoggerWithOTel(obsProvider)

	// Create ModuleDeps using the enhanced logger and observability
	deps := &ModuleDeps{
		Logger:        enhancedLogger, // Use enhanced logger with OTLP export
		Config:        b.cfg,
		Tracer:        obsProvider.TracerProvider().Tracer(b.cfg.App.Name),
		MeterProvider: obsProvider.MeterProvider(),
		DB:            provider.DB,
		DBByName:      provider.DBByName,
		Messaging:     provider.Messaging,
		Cache:         provider.Cache,
	}
	markConfigured(deps, plan)

	return &dependencyBundle{
		plan:             plan,
		deps:             deps,
		dbManager:        dbManager,
		messagingManager: messagingManager,
		cacheManager:     cacheManager,
		provider:         provider,
		observability:    obsProvider,
	}, nil
}

// markConfigured sets the three flags from the Resource plan: a kind is configured unless it
// is unavailable (ADR-127, ADR-128). See ModuleDeps.DBConfigured for the contract. It also sets
// ControlPlaneMessagingAbsent from the messaging row's "" fact alone, never from Tenancy (ADR-128).
func markConfigured(deps *ModuleDeps, plan resourcePlan) {
	deps.DBConfigured = !plan.database.unavailable()
	deps.MessagingConfigured = !plan.messaging.unavailable()
	deps.CacheConfigured = !plan.cache.unavailable()
	deps.ControlPlaneMessagingAbsent = plan.messaging.controlPlaneAbsent()
}

// warnIfDatabaseAbsent emits one advisory startup WARN for a database-free service.
// It is the backstop for modules that declare no DatabaseRequirer: without a
// declaration this line is the only production-visible signal that distinguishes a
// deliberately database-free service from one whose config never arrived.
func (b *appBootstrap) warnIfDatabaseAbsent(databasePlan kindPlan) {
	if !databasePlan.unavailable() {
		return
	}

	b.log.Warn().Msg("No database configured - database-backed features are unavailable; " +
		"if this service expects a database, its configuration did not reach the process")
}

// observabilityConfigKey is the koanf section initializeObservability decodes; its presence
// separates "no observability configured" from "configured, but undecodable".
const observabilityConfigKey = "observability"

// initializeObservability creates and configures the observability provider.
// Returns a no-op provider when the observability section is absent.
//
// A section that IS present but cannot be decoded aborts startup instead: degrading to
// the no-op provider there turns one bad key into total telemetry loss — no traces, no
// metrics, no OTLP logs, no migration audit events — announced by a single WARN on the
// way past. That is exactly the shape a delivered-empty numeric now produces (ADR-074),
// so the distinction has to be drawn rather than assumed.
func (b *appBootstrap) initializeObservability(startupCtx context.Context) (observability.Provider, error) {
	b.log.Debug().Msg("Starting observability initialization")

	// Create observability config
	var obsCfg observability.Config

	// Absence is decided BEFORE decoding, not inferred from a decode error: koanf returns no
	// error for a missing key, so an absent section would otherwise decode to a zero Config
	// and fall through to construction, leaving the documented no-op branch unreachable.
	if !b.cfg.Exists(observabilityConfigKey) {
		b.log.Debug().Msg("No observability configuration, using no-op provider")
		return observability.MustNewProvider(&observability.Config{Enabled: false}), nil
	}

	if err := b.cfg.Unmarshal(observabilityConfigKey, &obsCfg); err != nil {
		return nil, fmt.Errorf("observability configuration is present but invalid: %w", err)
	}

	b.log.Debug().
		Str("enabled", strconv.FormatBool(obsCfg.Enabled)).
		Str("service_name", obsCfg.Service.Name).
		Str("service_version", obsCfg.Service.Version).
		Str("environment", obsCfg.Environment).
		Msg("Raw observability config loaded from YAML")

	// Apply default values for fields not specified in config
	obsCfg.ApplyDefaults()

	traceEnabled := obsCfg.Trace.Enabled != nil && *obsCfg.Trace.Enabled
	metricsEnabled := obsCfg.Metrics.Enabled != nil && *obsCfg.Metrics.Enabled
	logsEnabled := obsCfg.Logs.Enabled != nil && *obsCfg.Logs.Enabled

	// Format sample rate for logging (handle nil pointer)
	traceSampleRateStr := "nil"
	if obsCfg.Trace.Sample.Rate != nil {
		traceSampleRateStr = strconv.FormatFloat(*obsCfg.Trace.Sample.Rate, 'f', 2, 64)
	}

	b.log.Debug().
		Str("trace_enabled", strconv.FormatBool(traceEnabled)).
		Str("trace_endpoint", obsCfg.Trace.Endpoint).
		Str("trace_protocol", obsCfg.Trace.Protocol).
		Str("trace_insecure", strconv.FormatBool(obsCfg.Trace.Insecure)).
		Str("trace_sample_rate", traceSampleRateStr).
		Str("metrics_enabled", strconv.FormatBool(metricsEnabled)).
		Str("metrics_endpoint", obsCfg.Metrics.Endpoint).
		Str("metrics_protocol", obsCfg.Metrics.Protocol).
		Str("logs_enabled", strconv.FormatBool(logsEnabled)).
		Str("logs_endpoint", obsCfg.Logs.Endpoint).
		Str("logs_protocol", obsCfg.Logs.Protocol).
		Str("logs_disable_stdout", strconv.FormatBool(obsCfg.Logs.DisableStdout)).
		Msg("Observability config after applying defaults")

	// Create provider (will be no-op if Enabled is false) under the
	// app.startup.observability budget, derived from the shared startup context
	// so it stays on the same cancellation/trace lineage as the other pre-init
	// budgets. The budget is enforced by threading a deadline-bound context
	// through provider construction (resource detection and OTLP exporter setup),
	// so a slow resource probe or exporter setup fails fast instead of blocking
	// the whole startup on the shared global timeout.
	budget := b.cfg.App.Startup.Observability
	construct := b.newProvider
	if construct == nil {
		construct = observability.NewProviderWithContext
	}
	ctx := startupCtx
	if ctx == nil {
		ctx = context.Background()
	}
	if budget > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, budget)
		defer cancel()
	}

	provider, err := construct(ctx, &obsCfg)
	if err != nil {
		// Construction failure is an environment problem (unreachable collector, bad
		// resource probe), not a malformed config: it stays non-fatal, as before.
		b.log.Warn().Err(err).Msg("Failed to initialize observability, using no-op provider")
		return observability.MustNewProvider(&observability.Config{Enabled: false}), nil
	}

	if obsCfg.Enabled {
		b.log.Info().
			Str("service", obsCfg.Service.Name).
			Str("environment", obsCfg.Environment).
			Str("trace_endpoint", obsCfg.Trace.Endpoint).
			Str("metrics_endpoint", obsCfg.Metrics.Endpoint).
			Str("logs_endpoint", obsCfg.Logs.Endpoint).
			Msg("Observability initialized successfully")
	} else {
		b.log.Debug().Msg("Observability disabled by configuration")
	}

	return provider, nil
}

// enhanceLoggerWithOTel attaches OTLP log export to the logger if observability is enabled.
// Returns the original logger if OTLP logging is disabled or if the logger type doesn't support it.
//
// This method implements the integration point between the logger and observability packages,
// enabling automatic export of structured logs to OTLP collectors for centralized logging.
func (b *appBootstrap) enhanceLoggerWithOTel(provider observability.Provider) logger.Logger {
	// Check if the provider has a logger provider (i.e., OTLP log export is enabled)
	if provider == nil || provider.LoggerProvider() == nil {
		b.log.Debug().Msg("OTLP log export disabled, using standard logger")
		return b.log
	}

	// Type assertion to access the WithOTelProvider method
	// The logger.Logger interface doesn't expose this method to avoid coupling,
	// but ZeroLogger implements it for observability integration.
	zerologger, ok := b.log.(*logger.ZeroLogger)
	if !ok {
		b.log.Warn().Msg("Logger does not support OTLP export (not a ZeroLogger instance)")
		return b.log
	}

	b.log.Debug().
		Str("disable_stdout", strconv.FormatBool(provider.ShouldDisableStdout())).
		Msg("Enhancing logger with OTLP export")

	// This will panic if the logger is in pretty mode (fail-fast configuration validation)
	enhancedLogger := zerologger.WithOTelProvider(provider)

	// Enhance the logger with OTLP export
	b.log.Info().
		Str("mode", getLogOutputMode(provider.ShouldDisableStdout())).
		Msg("OTLP log export enabled")

	// Replace bootstrap logger so subsequent components reuse the enhanced instance
	b.log = enhancedLogger

	return enhancedLogger
}

// getLogOutputMode returns a human-readable description of the log output mode.
func getLogOutputMode(disableStdout bool) string {
	if disableStdout {
		return "OTLP-only"
	}
	return "stdout+OTLP"
}
