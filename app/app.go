// Package app provides the core application framework for the MDW API Platform.
// It handles application lifecycle, module registration, and service orchestration.
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

// App represents the main application instance.
// It manages the lifecycle and coordination of all application components.
type App struct {
	cfg       *config.Config
	server    *server.Server
	db        database.Interface
	logger    logger.Logger
	messaging messaging.Client
	registry  *ModuleRegistry
}

// New creates a new application instance with all necessary dependencies.
// It initializes configuration, logging, database, and HTTP server components.
func New() (*App, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, fmt.Errorf("failed to load config: %w", err)
	}

	log := logger.New(cfg.Log.Level, cfg.Log.Pretty)

	log.Info().
		Str("app", cfg.App.Name).
		Str("env", cfg.App.Env).
		Str("version", cfg.App.Version).
		Msg("Starting application")

	db, err := database.NewConnection(&cfg.Database, log)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to database: %w", err)
	}

	srv := server.New(cfg, log)

	// Initialize messaging client if broker URL is provided
	var msgClient messaging.Client
	if cfg.Messaging.BrokerURL != "" {
		msgClient = messaging.NewAMQPClient(cfg.Messaging.BrokerURL, log)
		log.Info().
			Str("broker_url", cfg.Messaging.BrokerURL).
			Msg("Initializing AMQP messaging client")
	} else {
		log.Debug().Msg("No messaging broker URL configured, messaging disabled")
	}

	deps := &ModuleDeps{
		DB:        db,
		Logger:    log,
		Messaging: msgClient,
	}
	registry := NewModuleRegistry(deps)

	app := &App{
		cfg:       cfg,
		server:    srv,
		db:        db,
		logger:    log,
		messaging: msgClient,
		registry:  registry,
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
	signal.Notify(quit, os.Interrupt, syscall.SIGTERM)
	<-quit

	a.logger.Info().Msg("Shutting down application")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
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

	if err := a.db.Close(); err != nil {
		a.logger.Error().Err(err).Msg("Failed to close database connection")
	}

	a.logger.Info().Msg("Application shutdown complete")
	return nil
}

func (a *App) readyCheck(c echo.Context) error {
	ctx := c.Request().Context()

	dbHealth := "healthy"
	if err := a.db.Health(ctx); err != nil {
		dbHealth = "unhealthy"
		return c.JSON(http.StatusServiceUnavailable, map[string]interface{}{
			"status":   "not ready",
			"database": dbHealth,
			"error":    err.Error(),
		})
	}

	stats, err := a.db.Stats()
	if err != nil {
		a.logger.Error().Err(err).Msg("Failed to get database stats")
		stats = map[string]interface{}{"error": err.Error()}
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

	return c.JSON(http.StatusOK, map[string]interface{}{
		"status":    "ready",
		"time":      time.Now().Unix(),
		"database":  dbHealth,
		"db_stats":  stats,
		"messaging": messagingHealth,
		"app": map[string]interface{}{
			"name":        a.cfg.App.Name,
			"environment": a.cfg.App.Env,
			"version":     a.cfg.App.Version,
		},
	})
}
