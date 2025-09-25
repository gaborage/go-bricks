// Package main demonstrates the enhanced go-bricks framework with OpenAPI metadata generation.
// This example shows both the original API (for backward compatibility) and the new
// enhanced API with route options for documentation metadata.
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/gaborage/go-bricks/app"
	"github.com/gaborage/go-bricks/config"
	"github.com/gaborage/go-bricks/database"
	"github.com/gaborage/go-bricks/examples/openapi/user"
	"github.com/gaborage/go-bricks/logger"
	"github.com/gaborage/go-bricks/messaging"
	"github.com/gaborage/go-bricks/server"
)

func main() {
	// Load configuration
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}

	// Initialize logger
	logLevel := "info"
	if cfg.Log.Level != "" {
		logLevel = cfg.Log.Level
	}
	appLogger := logger.New(logLevel, true)

	// Create mock database (for this example)
	var db database.Interface

	// Create module dependencies
	deps := &app.ModuleDeps{
		Logger: appLogger,
		Config: cfg,
		GetDB: func(_ context.Context) (database.Interface, error) {
			if db == nil {
				return nil, fmt.Errorf("database not configured")
			}
			return db, nil
		},
		GetMessaging: func(_ context.Context) (messaging.AMQPClient, error) {
			return nil, fmt.Errorf("messaging not configured") // No messaging in this example
		},
	}

	// Create module registry
	registry := app.NewModuleRegistry(deps)

	// Register modules
	userModule := &user.Module{}
	if err := registry.Register(userModule); err != nil {
		appLogger.Fatal().Err(err).Msg("Failed to register user module")
	}

	// Create HTTP server
	httpServer := server.New(cfg, appLogger)

	// Register routes using base path aware registrar
	registry.RegisterRoutes(httpServer.ModuleGroup())

	// Print discovered routes (for demonstration)
	printDiscoveredRoutes(appLogger)

	// Start server
	go func() {
		appLogger.Info().Msg("Starting HTTP server")
		if err := httpServer.Start(); err != nil {
			appLogger.Fatal().Err(err).Msg("Failed to start HTTP server")
		}
	}()

	// Wait for shutdown signal
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	appLogger.Info().Msg("Shutting down...")

	// Graceful shutdown
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := httpServer.Shutdown(ctx); err != nil {
		appLogger.Error().Err(err).Msg("Server shutdown error")
	}

	if err := registry.Shutdown(); err != nil {
		appLogger.Error().Err(err).Msg("Module shutdown error")
	}

	appLogger.Info().Msg("Server stopped")
}

// printDiscoveredRoutes demonstrates how to access the route registry
func printDiscoveredRoutes(appLogger logger.Logger) {
	routes := server.DefaultRouteRegistry.GetRoutes()

	appLogger.Info().
		Int("count", len(routes)).
		Msg("Discovered routes")

	for i := range routes {
		route := &routes[i]
		appLogger.Info().
			Str("method", route.Method).
			Str("path", route.Path).
			Str("handler", route.HandlerName).
			Str("module", route.ModuleName).
			Str("package", route.Package).
			Str("tags", strings.Join(route.Tags, ",")).
			Str("summary", route.Summary).
			Msg("Route registered")
	}

	// Also show module metadata
	modules := app.DefaultModuleRegistry.GetModules()
	for name, info := range modules {
		appLogger.Info().
			Str("module", name).
			Str("package", info.Package).
			Str("description", info.Descriptor.Description).
			Str("tags", strings.Join(info.Descriptor.Tags, ",")).
			Msg("Module registered")
	}
}
