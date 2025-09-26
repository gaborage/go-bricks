// Package app provides the core application structure and lifecycle management.
package app

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/labstack/echo/v4"

	"github.com/gaborage/go-bricks/config"
	"github.com/gaborage/go-bricks/database"
	"github.com/gaborage/go-bricks/logger"
	"github.com/gaborage/go-bricks/messaging"
	"github.com/gaborage/go-bricks/multitenant"
	"github.com/gaborage/go-bricks/server"
)

const (
	serverErrorMsg  = "server error: %w"
	disabledStatus  = "disabled"
	healthyStatus   = "healthy"
	unhealthyStatus = "unhealthy"
)

var ErrNoTenantInContext = errors.New("no tenant in context")

// SignalHandler interface allows for injectable signal handling for testing
type SignalHandler interface {
	Notify(c chan<- os.Signal, sig ...os.Signal)
	WaitForSignal(c <-chan os.Signal)
}

// TimeoutProvider interface allows for injectable timeout creation for testing
type TimeoutProvider interface {
	WithTimeout(parent context.Context, timeout time.Duration) (context.Context, context.CancelFunc)
}

// ServerRunner abstracts the HTTP server to allow injecting test-friendly implementations
type ServerRunner interface {
	Start() error
	Shutdown(ctx context.Context) error
	Echo() *echo.Echo
	ModuleGroup() server.RouteRegistrar
	RegisterReadyHandler(handler echo.HandlerFunc)
}

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

	// Unified managers
	dbManager        *database.DbManager
	messagingManager *messaging.Manager

	// Messaging declarations for manager usage
	messagingDeclarations *messaging.Declarations

	closers      []namedCloser
	healthProbes []HealthProbe
}

type namedCloser struct {
	name   string
	closer interface{ Close() error }
}

type dependencyBundle struct {
	deps             *ModuleDeps
	dbManager        *database.DbManager
	messagingManager *messaging.Manager
}

// TenantResourceSource combines the interfaces required by the database and messaging managers.
type TenantResourceSource interface {
	database.TenantResourceSource
	messaging.TenantMessagingResourceSource
}

type appBootstrap struct {
	cfg  *config.Config
	log  logger.Logger
	opts *Options
}

func newAppBootstrap(cfg *config.Config, log logger.Logger, opts *Options) *appBootstrap {
	return &appBootstrap{cfg: cfg, log: log, opts: opts}
}

func (b *appBootstrap) coreComponents() (SignalHandler, TimeoutProvider, ServerRunner) {
	signalHandler, timeoutProvider := resolveSignalAndTimeout(b.opts)
	return signalHandler, timeoutProvider, resolveServer(b.cfg, b.log, b.opts)
}

func (b *appBootstrap) dependencies() *dependencyBundle {
	// Create resource source for configuration
	var resourceSource TenantResourceSource = config.NewTenantResourceSource(b.cfg)
	if b.opts != nil && b.opts.ResourceSource != nil {
		resourceSource = b.opts.ResourceSource
	}

	// Resolve factories from options
	dbConnector := database.NewConnection
	if b.opts != nil && b.opts.DatabaseConnector != nil {
		dbConnector = b.opts.DatabaseConnector
	}

	clientFactory := func(url string, log logger.Logger) messaging.AMQPClient {
		return messaging.NewAMQPClient(url, log)
	}
	if b.opts != nil && b.opts.MessagingClientFactory != nil {
		clientFactory = func(url string, log logger.Logger) messaging.AMQPClient {
			return b.opts.MessagingClientFactory(url, log)
		}
	}

	// Create managers with proper sizing based on mode
	var dbOpts database.DbManagerOptions
	var msgOpts messaging.ManagerOptions

	if b.cfg.Multitenant.Enabled {
		b.log.Info().Msg("Multi-tenant mode enabled")
		dbOpts = database.DbManagerOptions{
			MaxSize: b.cfg.Multitenant.Limits.Tenants, // Use configured tenant limit
			IdleTTL: 30 * time.Minute,                 // Shorter for multi-tenant
		}
		msgOpts = messaging.ManagerOptions{
			MaxPublishers: b.cfg.Multitenant.Limits.Tenants,
			IdleTTL:       5 * time.Minute,
		}
	} else {
		dbOpts = database.DbManagerOptions{
			MaxSize: 10,            // Small for single-tenant
			IdleTTL: 1 * time.Hour, // Longer for single-tenant
		}
		msgOpts = messaging.ManagerOptions{
			MaxPublishers: 10, // Small for single-tenant
			IdleTTL:       30 * time.Minute,
		}
	}

	// Create managers with injected factories
	dbManager := database.NewDbManager(resourceSource, b.log, dbOpts, dbConnector)
	messagingManager := messaging.NewMessagingManager(resourceSource, b.log, msgOpts, clientFactory)

	// Create ModuleDeps with unified key resolution
	deps := &ModuleDeps{
		Logger: b.log,
		Config: b.cfg,
		GetDB: func(ctx context.Context) (database.Interface, error) {
			key := "" // Single-tenant key
			if b.cfg.Multitenant.Enabled {
				tenantID, ok := multitenant.GetTenant(ctx)
				if !ok {
					return nil, ErrNoTenantInContext
				}
				key = tenantID
			}
			return dbManager.Get(ctx, key)
		},
		GetMessaging: func(ctx context.Context) (messaging.AMQPClient, error) {
			key := "" // Single-tenant key
			if b.cfg.Multitenant.Enabled {
				tenantID, ok := multitenant.GetTenant(ctx)
				if !ok {
					return nil, ErrNoTenantInContext
				}
				key = tenantID
			}
			return messagingManager.GetPublisher(ctx, key)
		},
	}

	return &dependencyBundle{
		deps:             deps,
		dbManager:        dbManager,
		messagingManager: messagingManager,
	}
}

// createHealthProbesForManagers creates health probes for the new managers
func createHealthProbesForManagers(dbManager *database.DbManager, messagingManager *messaging.Manager, log logger.Logger) []HealthProbe {
	var probes []HealthProbe

	if dbManager != nil {
		probes = append(probes, databaseManagerHealthProbe(dbManager, log))
	}

	if messagingManager != nil {
		probes = append(probes, messagingManagerHealthProbe(messagingManager, log))
	}

	return probes
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

// Options contains optional dependencies for creating an App instance
type Options struct {
	Database               database.Interface
	MessagingClient        messaging.Client
	SignalHandler          SignalHandler
	TimeoutProvider        TimeoutProvider
	Server                 ServerRunner
	ConfigLoader           func() (*config.Config, error)
	DatabaseConnector      func(*config.DatabaseConfig, logger.Logger) (database.Interface, error)
	MessagingClientFactory func(string, logger.Logger) messaging.AMQPClient
	ResourceSource         TenantResourceSource
}

// New creates a new application instance with dependencies determined by configuration.
// It initializes only the services that are configured, failing fast if configured services cannot connect.

func New() (*App, error) {
	return NewWithOptions(nil)
}

// NewWithOptions creates a new application instance allowing overrides for config loading and dependencies.
func NewWithOptions(opts *Options) (*App, error) {
	loader := config.Load
	if opts != nil && opts.ConfigLoader != nil {
		loader = opts.ConfigLoader
	}

	cfg, err := loader()
	if err != nil {
		return nil, fmt.Errorf("failed to load config: %w", err)
	}

	return NewWithConfig(cfg, opts)
}

// NewWithConfig creates a new application instance with the provided config and optional overrides.
// This factory method allows for dependency injection while maintaining fail-fast behavior.
func NewWithConfig(cfg *config.Config, opts *Options) (*App, error) {
	log := logger.New(cfg.Log.Level, cfg.Log.Pretty)

	log.Info().
		Str("app", cfg.App.Name).
		Str("env", cfg.App.Env).
		Str("version", cfg.App.Version).
		Msg("Starting application")

	bootstrap := newAppBootstrap(cfg, log, opts)

	signalHandler, timeoutProvider, srv := bootstrap.coreComponents()

	bundle := bootstrap.dependencies()

	app := &App{
		cfg:              cfg,
		server:           srv,
		logger:           log,
		registry:         nil, // Will be set after app creation
		signalHandler:    signalHandler,
		timeoutProvider:  timeoutProvider,
		dbManager:        bundle.dbManager,
		messagingManager: bundle.messagingManager,
	}

	registry := NewModuleRegistry(bundle.deps)
	app.registry = registry

	// Collect messaging declarations from all modules
	messagingDeclarations := messaging.NewDeclarations()
	if err := registry.DeclareMessaging(messagingDeclarations); err != nil {
		return nil, fmt.Errorf("failed to collect messaging declarations: %w", err)
	}

	// Store declarations for use by messaging manager
	app.messagingDeclarations = messagingDeclarations

	// Reassign GetMessaging closure to handle lazy consumer initialization and missing configuration checks
	bundle.deps.GetMessaging = func(ctx context.Context) (messaging.AMQPClient, error) {
		if app.messagingManager == nil {
			return nil, fmt.Errorf("messaging not configured")
		}

		key := "" // Single-tenant key
		if app.cfg.Multitenant.Enabled {
			tenantID, ok := multitenant.GetTenant(ctx)
			if !ok {
				return nil, ErrNoTenantInContext
			}
			key = tenantID
			if app.messagingDeclarations != nil {
				if err := app.messagingManager.EnsureConsumers(ctx, key, app.messagingDeclarations); err != nil {
					return nil, fmt.Errorf("failed to ensure consumers for tenant %s: %w", key, err)
				}
			}
		} else if app.messagingDeclarations != nil {
			if err := app.messagingManager.EnsureConsumers(ctx, key, app.messagingDeclarations); err != nil {
				return nil, fmt.Errorf("failed to ensure consumers: %w", err)
			}
		}

		return app.messagingManager.GetPublisher(ctx, key)
	}

	// Pre-warm single-tenant connections for backward compatibility
	if !cfg.Multitenant.Enabled {
		ctx := context.Background()

		// Pre-warm database connection
		if app.dbManager != nil {
			if _, err := app.dbManager.Get(ctx, ""); err != nil {
				log.Warn().Err(err).Msg("Failed to pre-warm single-tenant database connection")
			} else {
				log.Info().Msg("Pre-warmed single-tenant database connection")
			}
		}

		// Ensure consumers are set up for single-tenant
		if app.messagingManager != nil {
			if err := app.messagingManager.EnsureConsumers(ctx, "", app.messagingDeclarations); err != nil {
				log.Warn().Err(err).Msg("Failed to ensure single-tenant messaging consumers")
			} else {
				log.Info().Msg("Ensured single-tenant messaging consumers")
			}

			// Pre-warm messaging publisher
			if _, err := app.messagingManager.GetPublisher(ctx, ""); err != nil {
				log.Warn().Err(err).Msg("Failed to pre-warm single-tenant messaging publisher")
			} else {
				log.Info().Msg("Pre-warmed single-tenant messaging publisher")
			}
		}
	}

	// Create health probes for the new managers
	app.healthProbes = createHealthProbesForManagers(app.dbManager, app.messagingManager, log)

	// Register unified managers
	app.registerCloser("database manager", app.dbManager)
	app.registerCloser("messaging manager", app.messagingManager)

	srv.RegisterReadyHandler(app.readyCheck)

	return app, nil
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

func (a *App) startMaintenanceLoops() {
	// Start cleanup for unified managers
	if a.dbManager != nil {
		a.dbManager.StartCleanup(5 * time.Minute) // Database cleanup every 5 minutes
	}
	if a.messagingManager != nil {
		a.messagingManager.StartCleanup(2 * time.Minute) // Messaging cleanup every 2 minutes
	}
}

func (a *App) prepareRuntime() error {
	// Use the previously collected messaging declarations
	decls := a.messagingDeclarations
	if decls == nil {
		return fmt.Errorf("messaging declarations not initialized")
	}

	// Initialize consumers based on deployment mode
	if a.cfg.Multitenant.Enabled {
		// Multi-tenant: consumers will be started on-demand per tenant
		a.logger.Info().Msg("Multi-tenant mode: consumers will be started per tenant on demand")
	} else {
		// Single-tenant: pre-warm connections and start consumers
		if a.messagingManager != nil {
			ctx := context.Background()
			if err := a.messagingManager.EnsureConsumers(ctx, "", decls); err != nil {
				a.logger.Warn().Err(err).Msg("Failed to start single-tenant consumers")
				// Don't fail the app startup for messaging issues
			} else {
				a.logger.Info().Msg("Single-tenant consumers started successfully")
			}
		}

		// Pre-warm database connection for single-tenant
		if a.dbManager != nil {
			ctx := context.Background()
			if _, err := a.dbManager.Get(ctx, ""); err != nil {
				a.logger.Warn().Err(err).Msg("Failed to pre-warm single-tenant database connection")
				// Don't fail the app startup for database pre-warming
			} else {
				a.logger.Info().Msg("Single-tenant database connection pre-warmed")
			}
		}
	}

	a.registry.RegisterRoutes(a.server.ModuleGroup())
	a.startMaintenanceLoops()

	return nil
}

func (a *App) serve() <-chan error {
	errCh := make(chan error, 1)

	go func() {
		err := a.server.Start()
		errCh <- err
		close(errCh)
	}()

	return errCh
}

func (a *App) waitForShutdownOrServerError(serverErrCh <-chan error) (bool, error) {
	quit := make(chan os.Signal, 1)
	a.signalHandler.Notify(quit, os.Interrupt, syscall.SIGTERM)

	signalReceived := make(chan struct{}, 1)
	go func() {
		a.signalHandler.WaitForSignal(quit)
		signalReceived <- struct{}{}
	}()

	select {
	case <-signalReceived:
		return true, nil
	case err, ok := <-serverErrCh:
		if !ok {
			return false, nil
		}
		return false, err
	}
}

func (a *App) drainServerError(ch <-chan error) error {
	if ch == nil {
		return nil
	}

	err, ok := <-ch
	if !ok {
		return nil
	}

	return err
}

// GetMessagingDeclarations returns the captured messaging declarations.
// This is used by tenant managers to replay infrastructure for each tenant.
func (a *App) GetMessagingDeclarations() *messaging.Declarations {
	return a.messagingDeclarations
}

// Run starts the application and blocks until a shutdown signal is received.
// It handles graceful shutdown with a timeout.
func (a *App) Run() error {
	if err := a.prepareRuntime(); err != nil {
		return err
	}

	serverErrCh := a.serve()

	shutdownRequested, serverErr := a.waitForShutdownOrServerError(serverErrCh)

	if shutdownRequested {
		a.logger.Info().Msg("Shutdown signal received")
	}

	if serverErr != nil && !errors.Is(serverErr, http.ErrServerClosed) {
		a.logger.Error().Err(serverErr).Msg("Server stopped unexpectedly")
	}

	ctx, cancel := a.timeoutProvider.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	a.logger.Info().Msg("Shutting down application")

	shutdownErr := a.Shutdown(ctx)

	var errs []error

	if shutdownRequested {
		if err := a.drainServerError(serverErrCh); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errs = append(errs, fmt.Errorf(serverErrorMsg, err))
		}
	} else if serverErr != nil && !errors.Is(serverErr, http.ErrServerClosed) {
		errs = append(errs, fmt.Errorf(serverErrorMsg, serverErr))
	}

	if shutdownErr != nil {
		errs = append(errs, shutdownErr)
	}

	if len(errs) > 0 {
		return errors.Join(errs...)
	}

	return nil
}

// shutdownResource safely shuts down a resource and handles error logging
func (a *App) shutdownResource(closer namedCloser, errs *[]error) {
	if err := closer.closer.Close(); err != nil {
		*errs = append(*errs, fmt.Errorf("%s: %w", closer.name, err))
		a.logger.Error().Err(err).Msgf("Failed to close %s", closer.name)
		return
	}

	capitalizedName := strings.ToUpper(closer.name[:1]) + closer.name[1:]
	a.logger.Info().Msgf("%s closed successfully", capitalizedName)
}

// Shutdown gracefully shuts down the application with the given context.
// It closes database connections, messaging client, and stops the HTTP server.
// Returns an aggregated error if any components fail to shut down.
func (a *App) Shutdown(ctx context.Context) error {
	var errs []error

	// Shutdown modules first
	if err := a.registry.Shutdown(); err != nil {
		errs = append(errs, fmt.Errorf("modules: %w", err))
		a.logger.Error().Err(err).Msg("Failed to shutdown modules")
	}

	// Shutdown server
	if err := a.server.Shutdown(ctx); err != nil {
		errs = append(errs, fmt.Errorf(serverErrorMsg, err))
		a.logger.Error().Err(err).Msg("Failed to shutdown server")
	}

	for _, closer := range a.closers {
		a.shutdownResource(closer, &errs)
	}

	a.logger.Info().Msg("Application shutdown complete")

	// Return aggregated errors if any occurred
	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	return nil
}

func (a *App) readyCheck(c echo.Context) error {
	ctx := c.Request().Context()

	componentStatus := make(map[string]HealthStatus, len(a.healthProbes))
	for _, probe := range a.healthProbes {
		result := probe.Run(ctx)
		componentStatus[result.Name] = result
		if result.Err != nil && result.Critical {
			return c.JSON(http.StatusServiceUnavailable, map[string]any{
				"status":    "not ready",
				result.Name: result.Status,
				"error":     result.Err.Error(),
			})
		}
	}

	dbStatus := componentStatus["database"]
	if dbStatus.Status == "" {
		dbStatus.Status = disabledStatus
		dbStatus.Details = map[string]any{"status": disabledStatus}
	}
	dbStats := dbStatus.Details
	if dbStats == nil {
		dbStats = map[string]any{}
	}

	messagingStatus := componentStatus["messaging"]
	if messagingStatus.Status == "" {
		messagingStatus.Status = disabledStatus
	}
	messagingStats := messagingStatus.Details
	if messagingStats == nil {
		messagingStats = map[string]any{}
	}

	return c.JSON(http.StatusOK, map[string]any{
		"status":          "ready",
		"time":            time.Now().Unix(),
		"database":        dbStatus.Status,
		"db_stats":        dbStats,
		"messaging":       messagingStatus.Status,
		"messaging_stats": messagingStats,
		"app": map[string]any{
			"name":        a.cfg.App.Name,
			"environment": a.cfg.App.Env,
			"version":     a.cfg.App.Version,
		},
	})
}
