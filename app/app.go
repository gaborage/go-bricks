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
	serverErrorMsg = "server error: %w"
	disabledStatus = "disabled"
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
	db              database.Interface
	logger          logger.Logger
	messaging       messaging.Client
	registry        *ModuleRegistry
	signalHandler   SignalHandler
	timeoutProvider TimeoutProvider

	// Multi-tenant support
	tenantConnManager *multitenant.TenantConnectionManager
	messagingManager  *multitenant.TenantMessagingManager
	tenantMessaging   TenantMessagingOrchestrator

	// Messaging declarations captured during startup for per-tenant replay
	messagingDeclarations *multitenant.MessagingDeclarations

	closers      []namedCloser
	healthProbes []HealthProbe
}

type namedCloser struct {
	name   string
	closer interface{ Close() error }
}

type dependencyBundle struct {
	deps                   *ModuleDeps
	db                     database.Interface
	messaging              messaging.Client
	tenantConnManager      *multitenant.TenantConnectionManager
	tenantMessagingManager *multitenant.TenantMessagingManager
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

func (b *appBootstrap) dependencies() (*dependencyBundle, error) {
	if b.cfg.Multitenant.Enabled {
		return b.multitenantDependencies()
	}
	return b.singleTenantDependencies()
}

func (b *appBootstrap) singleTenantDependencies() (*dependencyBundle, error) {
	db, err := resolveDatabase(b.cfg, b.log, b.opts)
	if err != nil {
		return nil, err
	}

	msgClient := resolveMessaging(b.cfg, b.log, b.opts)

	deps := &ModuleDeps{
		Logger: b.log,
		Config: b.cfg,
		GetDB: func(_ context.Context) (database.Interface, error) {
			if db == nil {
				return nil, fmt.Errorf("database not configured")
			}
			return db, nil
		},
		GetMessaging: func(_ context.Context) (messaging.AMQPClient, error) {
			if msgClient == nil {
				return nil, fmt.Errorf("messaging not configured")
			}
			if amqpClient, ok := msgClient.(messaging.AMQPClient); ok {
				return amqpClient, nil
			}
			return nil, fmt.Errorf("messaging client is not an AMQPClient")
		},
	}

	return &dependencyBundle{
		deps:      deps,
		db:        db,
		messaging: msgClient,
	}, nil
}

func (b *appBootstrap) multitenantDependencies() (*dependencyBundle, error) {
	b.log.Info().Msg("Multi-tenant mode enabled")

	tenantConnManager, err := resolveTenantConnectionManager(b.cfg, b.log, b.opts)
	if err != nil {
		return nil, err
	}

	messagingManager := resolveTenantMessagingManager(b.cfg, b.log, b.opts)

	deps := &ModuleDeps{
		Logger: b.log,
		Config: b.cfg,
		GetDB: func(ctx context.Context) (database.Interface, error) {
			tenantID, ok := multitenant.GetTenant(ctx)
			if !ok {
				return nil, ErrNoTenantInContext
			}
			return tenantConnManager.GetDatabase(ctx, tenantID)
		},
		GetMessaging: func(ctx context.Context) (messaging.AMQPClient, error) {
			if messagingManager == nil {
				return nil, fmt.Errorf("messaging not configured in multi-tenant mode")
			}

			tenantID, ok := multitenant.GetTenant(ctx)
			if !ok {
				return nil, ErrNoTenantInContext
			}

			return nil, fmt.Errorf("messaging declarations not yet captured for tenant %s - call during app startup", tenantID)
		},
	}

	return &dependencyBundle{
		deps:                   deps,
		tenantConnManager:      tenantConnManager,
		tenantMessagingManager: messagingManager,
	}, nil
}

// isDatabaseEnabled determines if database should be initialized based on config
// Uses the shared logic from config.IsDatabaseConfigured for consistency
func isDatabaseEnabled(cfg *config.Config) bool {
	return config.IsDatabaseConfigured(&cfg.Database)
}

// isMessagingEnabled determines if messaging should be initialized based on config
func isMessagingEnabled(cfg *config.Config) bool {
	return cfg.Messaging.Broker.URL != ""
}

func resolveDatabase(cfg *config.Config, log logger.Logger, opts *Options) (database.Interface, error) {
	if opts != nil && opts.Database != nil {
		log.Debug().Msg("Using provided database instance")
		return opts.Database, nil
	}

	if !isDatabaseEnabled(cfg) {
		log.Info().Msg("No database configured, running without database")
		return nil, nil
	}

	connector := database.NewConnection
	if opts != nil && opts.DatabaseConnector != nil {
		connector = opts.DatabaseConnector
	}

	db, err := connector(&cfg.Database, log)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to database: %w", err)
	}

	log.Info().
		Str("type", cfg.Database.Type).
		Str("host", cfg.Database.Host).
		Msg("Database connection established")

	return db, nil
}

func resolveMessaging(cfg *config.Config, log logger.Logger, opts *Options) messaging.Client {
	factory := func(brokerURL string, log logger.Logger) messaging.Client {
		return messaging.NewAMQPClient(brokerURL, log)
	}
	if opts != nil && opts.MessagingClientFactory != nil {
		factory = opts.MessagingClientFactory
	}

	if opts != nil && opts.MessagingClient != nil {
		log.Debug().Msg("Using provided messaging client")
		return opts.MessagingClient
	}

	if !isMessagingEnabled(cfg) {
		log.Info().Msg("No messaging broker URL configured, messaging disabled")
		return nil
	}

	log.Info().
		Str("broker_url", cfg.Messaging.Broker.URL).
		Msg("Initializing AMQP messaging client")

	return factory(cfg.Messaging.Broker.URL, log)
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

// resolveTenantMessagingManager creates the tenant messaging manager for multi-tenant messaging
func resolveTenantMessagingManager(cfg *config.Config, log logger.Logger, opts *Options) *multitenant.TenantMessagingManager {
	if opts != nil && opts.TenantMessagingManager != nil {
		return opts.TenantMessagingManager
	}

	if opts == nil || opts.TenantConfigProvider == nil {
		return nil
	}

	cache := resolveTenantCache(cfg, opts)

	cfg.Multitenant.Messaging.Normalize()
	idleTTL := cfg.Multitenant.Messaging.PublisherTTL
	maxActive := cfg.Multitenant.Messaging.MaxPublishers

	return multitenant.NewTenantMessagingManager(opts.TenantConfigProvider, cache, log, idleTTL, maxActive)
}

// resolveTenantConnectionManager creates the tenant connection manager
func resolveTenantConnectionManager(cfg *config.Config, log logger.Logger, opts *Options) (*multitenant.TenantConnectionManager, error) {
	if opts != nil && opts.TenantConnectionManager != nil {
		return opts.TenantConnectionManager, nil
	}

	if opts == nil || opts.TenantConfigProvider == nil {
		return nil, fmt.Errorf("multitenant enabled but no tenant config provider was supplied")
	}

	cache := resolveTenantCache(cfg, opts)
	connOpts := resolveTenantConnectionOptions(cfg, opts)

	tenantConnManager := multitenant.NewTenantConnectionManager(opts.TenantConfigProvider, cache, log, connOpts...)
	if tenantConnManager == nil {
		return nil, fmt.Errorf("failed to initialize tenant connection manager")
	}

	return tenantConnManager, nil
}

// resolveTenantCache creates or uses provided tenant config cache
func resolveTenantCache(cfg *config.Config, opts *Options) *multitenant.TenantConfigCache {
	if opts.TenantConfigCache != nil {
		return opts.TenantConfigCache
	}

	cacheOpts := []multitenant.CacheOption{
		multitenant.WithTTL(cfg.Multitenant.Cache.TTL),
		multitenant.WithMaxSize(cfg.Multitenant.Limits.Tenants),
	}
	cacheOpts = append(cacheOpts, opts.TenantCacheOptions...)

	return multitenant.NewTenantConfigCache(opts.TenantConfigProvider, cacheOpts...)
}

// resolveTenantConnectionOptions creates connection options from config and overrides
func resolveTenantConnectionOptions(cfg *config.Config, opts *Options) []multitenant.ConnectionOption {
	connOpts := []multitenant.ConnectionOption{
		multitenant.WithMaxTenants(cfg.Multitenant.Limits.Tenants),
	}

	if opts != nil {
		connOpts = append(connOpts, opts.TenantConnectionOptions...)
	}

	return connOpts
}

// Options contains optional dependencies for creating an App instance
type Options struct {
	Database                database.Interface
	MessagingClient         messaging.Client
	SignalHandler           SignalHandler
	TimeoutProvider         TimeoutProvider
	Server                  ServerRunner
	ConfigLoader            func() (*config.Config, error)
	DatabaseConnector       func(*config.DatabaseConfig, logger.Logger) (database.Interface, error)
	MessagingClientFactory  func(string, logger.Logger) messaging.Client
	TenantConfigProvider    multitenant.TenantConfigProvider
	TenantConfigCache       *multitenant.TenantConfigCache
	TenantCacheOptions      []multitenant.CacheOption
	TenantConnectionOptions []multitenant.ConnectionOption
	TenantConnectionManager *multitenant.TenantConnectionManager
	TenantMessagingManager  *multitenant.TenantMessagingManager
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

	bundle, err := bootstrap.dependencies()
	if err != nil {
		return nil, err
	}

	app := &App{
		cfg:               cfg,
		server:            srv,
		db:                bundle.db,
		logger:            log,
		messaging:         bundle.messaging,
		registry:          nil, // Will be set after app creation
		signalHandler:     signalHandler,
		timeoutProvider:   timeoutProvider,
		tenantConnManager: bundle.tenantConnManager,
		messagingManager:  bundle.tenantMessagingManager,
	}

	registry := NewModuleRegistry(bundle.deps)
	app.registry = registry

	if bundle.tenantMessagingManager != nil {
		app.tenantMessaging = newTenantMessagingOrchestrator(registry, bundle.tenantMessagingManager, log)
	}

	if cfg.Multitenant.Enabled {
		app.setupMultitenantGetMessaging(bundle.deps)
	}

	app.healthProbes = createHealthProbes(app.db, app.messaging, log)

	app.registerCloser("messaging client", app.messaging)
	app.registerCloser("database connection", app.db)
	app.registerCloser("tenant connection manager", app.tenantConnManager)
	app.registerCloser("messaging manager", app.messagingManager)

	srv.RegisterReadyHandler(app.readyCheck)

	return app, nil
}

// setupMultitenantGetMessaging configures the GetMessaging function for multi-tenant mode
func (a *App) setupMultitenantGetMessaging(deps *ModuleDeps) {
	deps.GetMessaging = func(ctx context.Context) (messaging.AMQPClient, error) {
		if a.tenantMessaging == nil {
			return nil, fmt.Errorf("messaging not configured in multi-tenant mode")
		}

		tenantID, ok := multitenant.GetTenant(ctx)
		if !ok {
			return nil, ErrNoTenantInContext
		}

		if a.messagingDeclarations == nil {
			return nil, fmt.Errorf("messaging declarations not yet captured for tenant %s - call during app startup", tenantID)
		}

		return a.tenantMessaging.PublisherForTenant(ctx, tenantID, a.messagingDeclarations)
	}
}

// RegisterModule registers a new module with the application.
// It adds the module to the registry for initialization and route registration.
func (a *App) RegisterModule(module Module) error {
	return a.registry.Register(module)
}

// captureMessagingDeclarations captures messaging infrastructure declarations
// from all registered modules in multi-tenant mode. This creates a recording
// registry with a mock AMQP client to capture declarations without broker connection.
//
// The captured declarations are stored for later per-tenant replay.
func (a *App) captureMessagingDeclarations() error {
	// Only capture in multi-tenant mode
	if !a.cfg.Multitenant.Enabled || a.tenantMessaging == nil {
		return nil
	}

	declarations, err := a.tenantMessaging.CaptureDeclarations()
	if err != nil {
		return err
	}

	a.messagingDeclarations = declarations
	return nil
}

func (a *App) registerCloser(name string, closer interface{ Close() error }) {
	if closer == nil {
		return
	}

	a.closers = append(a.closers, namedCloser{name: name, closer: closer})
}

func (a *App) startMaintenanceLoops() {
	if a.tenantConnManager != nil {
		cleanupInterval := a.cfg.Multitenant.Limits.Cleanup.Interval
		if cleanupInterval == 0 {
			cleanupInterval = 5 * time.Minute
		}
		a.tenantConnManager.StartCleanup(cleanupInterval)
	}

	if a.messagingManager != nil {
		cleanupInterval := a.cfg.Multitenant.Messaging.CleanupInterval
		if cleanupInterval == 0 {
			cleanupInterval = time.Minute
		}
		a.messagingManager.StartCleanup(cleanupInterval)
	}
}

func (a *App) prepareRuntime() error {
	if err := a.captureMessagingDeclarations(); err != nil {
		return fmt.Errorf("failed to capture messaging declarations: %w", err)
	}

	if err := a.registry.RegisterMessaging(); err != nil {
		return fmt.Errorf("failed to register messaging infrastructure: %w", err)
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
func (a *App) GetMessagingDeclarations() *multitenant.MessagingDeclarations {
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

	return c.JSON(http.StatusOK, map[string]any{
		"status":    "ready",
		"time":      time.Now().Unix(),
		"database":  dbStatus.Status,
		"db_stats":  dbStats,
		"messaging": messagingStatus.Status,
		"app": map[string]any{
			"name":        a.cfg.App.Name,
			"environment": a.cfg.App.Env,
			"version":     a.cfg.App.Version,
		},
	})
}
