// Package app provides the core application structure and lifecycle management.
package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"time"

	"github.com/gaborage/go-bricks/cache"
	"github.com/gaborage/go-bricks/config"
	"github.com/gaborage/go-bricks/database"
	"github.com/gaborage/go-bricks/logger"
	"github.com/gaborage/go-bricks/messaging"
	"github.com/gaborage/go-bricks/observability"
	"github.com/gaborage/go-bricks/server"
)

const (
	serverErrorMsg      = "server error: %w"
	disabledStatus      = "disabled"
	healthyStatus       = "healthy"
	unhealthyStatus     = "unhealthy"
	notConfiguredStatus = "not_configured"
)

var ErrNoTenantInContext = errors.New("no tenant in context")

// OSSignalHandler implements SignalHandler using the real OS signal package
type OSSignalHandler struct{}

func (osh *OSSignalHandler) Notify(c chan<- os.Signal, sig ...os.Signal) {
	signal.Notify(c, sig...)
}

func (osh *OSSignalHandler) WaitForSignal(c <-chan os.Signal) {
	<-c
}

// StandardTimeoutProvider implements TimeoutProvider using context.WithTimeout
type StandardTimeoutProvider struct{}

func (stp *StandardTimeoutProvider) WithTimeout(parent context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(parent, timeout)
}

// App represents the main application instance.
// It manages the lifecycle and coordination of all application components.
type App struct {
	cfg             *config.Config
	server          ServerRunner
	logger          logger.Logger
	registry        *ModuleRegistry
	signalHandler   SignalHandler
	timeoutProvider TimeoutProvider

	// Observability
	observability observability.Provider

	// Unified managers
	dbManager        *database.DbManager
	messagingManager *messaging.Manager
	cacheManager     *cache.CacheManager
	resourceProvider ResourceProvider

	// Messaging declarations for manager usage
	messagingDeclarations *messaging.Declarations
	messagingInitializer  *MessagingInitializer
	connectionPreWarmer   *ConnectionPreWarmer

	closers      []namedCloser
	healthProbes []HealthProbe
}

// createHealthProbesForManagers creates health probes for the new managers
func createHealthProbesForManagers(dbManager *database.DbManager, messagingManager *messaging.Manager, cacheManager *cache.CacheManager, log logger.Logger) []HealthProbe {
	var probes []HealthProbe

	if dbManager != nil {
		probes = append(probes, databaseManagerHealthProbe(dbManager, log))
	}

	if messagingManager != nil {
		probes = append(probes, messagingManagerHealthProbe(messagingManager, log))
	}

	if cacheManager != nil {
		probes = append(probes, cacheManagerHealthProbe(cacheManager, log))
	}

	return probes
}

func (a *App) buildMessagingDeclarations() error {
	if a.messagingDeclarations != nil {
		return nil
	}

	if a.registry == nil {
		return fmt.Errorf("module registry not initialized")
	}

	decls := messaging.NewDeclarations()
	if err := a.registry.DeclareMessaging(decls); err != nil {
		return err
	}

	a.messagingDeclarations = decls

	if setter, ok := a.resourceProvider.(declarationSetter); ok && a.resourceProvider != nil {
		setter.SetDeclarations(decls)
	}

	return nil
}

func resolveSignalAndTimeout(opts *Options) (SignalHandler, TimeoutProvider) {
	signalHandler := SignalHandler(&OSSignalHandler{})
	timeoutProvider := TimeoutProvider(&StandardTimeoutProvider{})

	if opts != nil {
		if opts.SignalHandler != nil {
			signalHandler = opts.SignalHandler
		}
		if opts.TimeoutProvider != nil {
			timeoutProvider = opts.TimeoutProvider
		}
	}

	return signalHandler, timeoutProvider
}

func resolveServer(cfg *config.Config, log logger.Logger, opts *Options) ServerRunner {
	if opts != nil && opts.Server != nil {
		log.Debug().Msg("Using provided server instance")
		return opts.Server
	}

	return server.New(cfg, log)
}

// createBootstrapLogger creates a logger with smart defaults for bootstrap/initialization logging.
// This logger is available even when configuration loading fails.
func createBootstrapLogger() logger.Logger {
	// Smart defaults: debug in dev, info in prod
	level := "info"
	pretty := false

	// Check environment for development mode
	env := strings.TrimSpace(os.Getenv("APP_ENV"))
	if env == "" || strings.EqualFold(env, "development") || strings.EqualFold(env, "dev") {
		level = "debug"
		pretty = true
	}

	// Allow override via environment variable
	if envLevel := os.Getenv("LOG_LEVEL"); envLevel != "" {
		level = envLevel
	}

	return logger.New(level, pretty)
}

// New creates a new application instance with dependencies determined by configuration.
// It initializes only the services that are configured, failing fast if configured services cannot connect.
// Returns the app instance, a logger (always available even on failure), and any error.
func New() (*App, logger.Logger, error) {
	return NewWithOptions(nil)
}

// NewWithOptions creates a new application instance allowing overrides for config loading and dependencies.
// Returns the app instance, a logger (always available even on failure), and any error.
func NewWithOptions(opts *Options) (*App, logger.Logger, error) {
	// Create bootstrap logger first - always available
	bootstrapLog := createBootstrapLogger()

	loader := config.Load
	if opts != nil && opts.ConfigLoader != nil {
		loader = opts.ConfigLoader
	}

	cfg, err := loader()
	if err != nil {
		return nil, bootstrapLog, fmt.Errorf("failed to load config: %w", err)
	}

	return NewWithConfig(cfg, opts)
}

// NewWithConfig creates a new application instance with the provided config and optional overrides.
// This factory method allows for dependency injection while maintaining fail-fast behavior.
// Returns the app instance, a logger (always available even on failure), and any error.
func NewWithConfig(cfg *config.Config, opts *Options) (*App, logger.Logger, error) {
	builder := NewAppBuilder()

	app, log, err := builder.
		WithConfig(cfg, opts).
		CreateLogger().
		CreateBootstrap().
		ResolveDependencies().
		CreateApp().
		InitializeRegistry().
		ConfigureRuntimeHelpers().
		CreateHealthProbes().
		RegisterClosers().
		RegisterReadyHandler().
		Build()

	if err != nil {
		// Return the logger from builder if available, otherwise create bootstrap logger
		if log == nil {
			log = createBootstrapLogger()
		}
		return nil, log, fmt.Errorf("failed to create app: %w", err)
	}

	return app, log, nil
}

// RegisterModule registers a new module with the application.
// It adds the module to the registry for initialization and route registration.
func (a *App) RegisterModule(module Module) error {
	return a.registry.Register(module)
}

func (a *App) registerCloser(name string, closer interface{ Close() error }) {
	if closer == nil {
		return
	}

	a.closers = append(a.closers, namedCloser{name: name, closer: closer})
}

// MessagingDeclarations returns the captured messaging declarations.
// This is used by tenant managers to replay infrastructure for each tenant.
func (a *App) MessagingDeclarations() *messaging.Declarations {
	return a.messagingDeclarations
}
