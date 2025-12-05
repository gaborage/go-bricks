package app

import (
	"strconv"

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
	cacheManager := factory.CreateCacheManager(resourceSource)

	// Create appropriate resource provider based on mode
	var provider ResourceProvider
	if b.cfg.Multitenant.Enabled {
		provider = NewMultiTenantResourceProvider(dbManager, messagingManager, cacheManager, nil)
	} else {
		provider = NewSingleTenantResourceProvider(dbManager, messagingManager, cacheManager, nil)
	}

	// Initialize observability provider (no-op if disabled)
	obsProvider := b.initializeObservability()

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
		Messaging:     provider.Messaging,
		Cache:         provider.Cache,
	}

	return &dependencyBundle{
		deps:             deps,
		dbManager:        dbManager,
		messagingManager: messagingManager,
		cacheManager:     cacheManager,
		provider:         provider,
		observability:    obsProvider,
	}
}

// initializeObservability creates and configures the observability provider.
// Returns a no-op provider if observability is disabled or configuration is missing.
func (b *appBootstrap) initializeObservability() observability.Provider {
	b.log.Debug().Msg("Starting observability initialization")

	// Create observability config
	var obsCfg observability.Config

	// Try to unmarshal configuration from the "observability" key
	if err := b.cfg.Unmarshal("observability", &obsCfg); err != nil {
		// Configuration missing or invalid - use defaults (observability disabled)
		b.log.Warn().Err(err).Msg("Observability configuration not found or invalid, using no-op provider")
		return observability.MustNewProvider(&observability.Config{Enabled: false})
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

	// Create provider (will be no-op if Enabled is false)
	provider, err := observability.NewProvider(&obsCfg)
	if err != nil {
		b.log.Warn().Err(err).Msg("Failed to initialize observability, using no-op provider")
		return observability.MustNewProvider(&observability.Config{Enabled: false})
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

	return provider
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
