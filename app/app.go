// Package app provides the core application structure and lifecycle management.
package app

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/labstack/echo/v4"

	"github.com/gaborage/go-bricks/config"
	"github.com/gaborage/go-bricks/database"
	"github.com/gaborage/go-bricks/logger"
	"github.com/gaborage/go-bricks/messaging"
	"github.com/gaborage/go-bricks/server"
)

// SignalHandler interface allows for injectable signal handling for testing
type SignalHandler interface {
	Notify(c chan<- os.Signal, sig ...os.Signal)
	WaitForSignal(c <-chan os.Signal)
}

// TimeoutProvider interface allows for injectable timeout creation for testing
type TimeoutProvider interface {
	WithTimeout(parent context.Context, timeout time.Duration) (context.Context, context.CancelFunc)
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
	server          *server.Server
	db              database.Interface
	logger          logger.Logger
	messaging       messaging.Client
	registry        *ModuleRegistry
	signalHandler   SignalHandler
	timeoutProvider TimeoutProvider
}

// isDatabaseEnabled determines if database should be initialized based on config
func isDatabaseEnabled(cfg *config.Config) bool {
	return cfg.Database.Host != "" || cfg.Database.Type != ""
}

// isMessagingEnabled determines if messaging should be initialized based on config
func isMessagingEnabled(cfg *config.Config) bool {
	return cfg.Messaging.BrokerURL != ""
}

// Options contains optional dependencies for creating an App instance
type Options struct {
	Database        database.Interface
	MessagingClient messaging.Client
	SignalHandler   SignalHandler
	TimeoutProvider TimeoutProvider
}

// New creates a new application instance with dependencies determined by configuration.
// It initializes only the services that are configured, failing fast if configured services cannot connect.
func New() (*App, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, fmt.Errorf("failed to load config: %w", err)
	}

	return NewWithConfig(cfg, nil)
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

	// Initialize database if configured or provided
	var db database.Interface
	if opts != nil && opts.Database != nil {
		db = opts.Database
		log.Debug().Msg("Using provided database instance")
	} else if isDatabaseEnabled(cfg) {
		var err error
		db, err = database.NewConnection(&cfg.Database, log)
		if err != nil {
			return nil, fmt.Errorf("failed to connect to database: %w", err)
		}
		log.Info().
			Str("type", cfg.Database.Type).
			Str("host", cfg.Database.Host).
			Msg("Database connection established")
	} else {
		log.Info().Msg("No database configured, running without database")
	}

	// Initialize messaging client if configured or provided
	var msgClient messaging.Client
	if opts != nil && opts.MessagingClient != nil {
		msgClient = opts.MessagingClient
		log.Debug().Msg("Using provided messaging client")
	} else if isMessagingEnabled(cfg) {
		msgClient = messaging.NewAMQPClient(cfg.Messaging.BrokerURL, log)
		log.Info().
			Str("broker_url", cfg.Messaging.BrokerURL).
			Msg("Initializing AMQP messaging client")
	} else {
		log.Info().Msg("No messaging broker URL configured, messaging disabled")
	}

	// Use provided signal handler and timeout provider or defaults
	var signalHandler SignalHandler = &OSSignalHandler{}
	var timeoutProvider TimeoutProvider = &StandardTimeoutProvider{}
	if opts != nil {
		if opts.SignalHandler != nil {
			signalHandler = opts.SignalHandler
		}
		if opts.TimeoutProvider != nil {
			timeoutProvider = opts.TimeoutProvider
		}
	}

	srv := server.New(cfg, log)

	deps := &ModuleDeps{
		DB:        db,
		Logger:    log,
		Messaging: msgClient,
		Config:    cfg,
	}
	registry := NewModuleRegistry(deps)

	app := &App{
		cfg:             cfg,
		server:          srv,
		db:              db,
		logger:          log,
		messaging:       msgClient,
		registry:        registry,
		signalHandler:   signalHandler,
		timeoutProvider: timeoutProvider,
	}

	srv.Echo().GET("/ready", app.readyCheck)

	return app, nil
}

// RegisterModule registers a new module with the application.
// It adds the module to the registry for initialization and route registration.
func (a *App) RegisterModule(module Module) error {
	return a.registry.Register(module)
}

// Run starts the application and blocks until a shutdown signal is received.
// It handles graceful shutdown with a timeout.
func (a *App) Run() error {
	// Register messaging infrastructure before starting the server
	if err := a.registry.RegisterMessaging(); err != nil {
		return fmt.Errorf("failed to register messaging infrastructure: %w", err)
	}

	a.registry.RegisterRoutes(a.server.Echo())

	go func() {
		if err := a.server.Start(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			a.logger.Fatal().Err(err).Msg("Failed to start server")
		}
	}()

	quit := make(chan os.Signal, 1)
	a.signalHandler.Notify(quit, os.Interrupt, syscall.SIGTERM)
	a.signalHandler.WaitForSignal(quit)

	a.logger.Info().Msg("Shutting down application")

	ctx, cancel := a.timeoutProvider.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	return a.Shutdown(ctx)
}

// Shutdown gracefully shuts down the application with the given context.
// It closes database connections, messaging client, and stops the HTTP server.
func (a *App) Shutdown(ctx context.Context) error {
	if err := a.registry.Shutdown(); err != nil {
		a.logger.Error().Err(err).Msg("Failed to shutdown modules")
	}

	if err := a.server.Shutdown(ctx); err != nil {
		a.logger.Error().Err(err).Msg("Failed to shutdown server")
	}

	// Close messaging client if enabled
	if a.messaging != nil {
		if err := a.messaging.Close(); err != nil {
			a.logger.Error().Err(err).Msg("Failed to close messaging client")
		} else {
			a.logger.Info().Msg("Messaging client closed successfully")
		}
	}

	// Close database connection if enabled
	if a.db != nil {
		if err := a.db.Close(); err != nil {
			a.logger.Error().Err(err).Msg("Failed to close database connection")
		} else {
			a.logger.Info().Msg("Database connection closed successfully")
		}
	}

	a.logger.Info().Msg("Application shutdown complete")
	return nil
}

func (a *App) readyCheck(c echo.Context) error {
	ctx := c.Request().Context()

	// Check database health if enabled
	dbHealth := "disabled"
	var stats map[string]any
	if a.db != nil {
		if err := a.db.Health(ctx); err != nil {
			dbHealth = "unhealthy"
			return c.JSON(http.StatusServiceUnavailable, map[string]any{
				"status":   "not ready",
				"database": dbHealth,
				"error":    err.Error(),
			})
		}
		dbHealth = "healthy"

		var err error
		stats, err = a.db.Stats()
		if err != nil {
			a.logger.Error().Err(err).Msg("Failed to get database stats")
			stats = map[string]any{"error": err.Error()}
		}
	} else {
		stats = map[string]any{"status": "disabled"}
	}

	// Check messaging health if enabled
	messagingHealth := "disabled"
	if a.messaging != nil {
		if a.messaging.IsReady() {
			messagingHealth = "healthy"
		} else {
			messagingHealth = "unhealthy"
		}
	}

	return c.JSON(http.StatusOK, map[string]any{
		"status":    "ready",
		"time":      time.Now().Unix(),
		"database":  dbHealth,
		"db_stats":  stats,
		"messaging": messagingHealth,
		"app": map[string]any{
			"name":        a.cfg.App.Name,
			"environment": a.cfg.App.Env,
			"version":     a.cfg.App.Version,
		},
	})
}
