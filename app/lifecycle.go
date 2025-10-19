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
)

// startMaintenanceLoops starts background cleanup processes for managers
func (a *App) startMaintenanceLoops() {
	// Start cleanup for unified managers
	if a.dbManager != nil {
		a.logger.Info().Msg("Starting database manager cleanup loop")
		a.dbManager.StartCleanup(5 * time.Minute) // Database cleanup every 5 minutes
	}
	if a.messagingManager != nil {
		a.logger.Info().Msg("Starting messaging manager cleanup loop")
		a.messagingManager.StartCleanup(2 * time.Minute) // Messaging cleanup every 2 minutes
	}
}

// prepareRuntime prepares the application for runtime execution
func (a *App) prepareRuntime() error {
	if err := a.buildMessagingDeclarations(); err != nil {
		return err
	}

	decls := a.messagingDeclarations

	if a.messagingInitializer != nil && a.messagingInitializer.IsAvailable() && decls != nil {
		a.messagingInitializer.LogDeploymentMode()

		if a.resourceProvider != nil {
			if err := a.messagingInitializer.SetupLazyConsumerInit(a.resourceProvider, decls); err != nil {
				return err
			}
		}

		if err := a.messagingInitializer.PrepareRuntimeConsumers(context.Background(), decls); err != nil {
			return err
		}
	}

	if !a.cfg.Multitenant.Enabled && a.connectionPreWarmer != nil && a.connectionPreWarmer.IsAvailable() {
		a.connectionPreWarmer.LogAvailability()
		if err := a.connectionPreWarmer.PreWarmSingleTenant(context.Background(), decls); err != nil {
			a.logger.Warn().Err(err).Msg("Pre-warming completed with warnings")
		}
	}

	// Register debug endpoints if enabled
	a.registerDebugHandlers()

	// Register scheduled jobs (after all modules initialized, before routes)
	if err := a.registry.RegisterJobs(); err != nil {
		return err
	}

	a.registry.RegisterRoutes(a.server.ModuleGroup())
	a.startMaintenanceLoops()

	return nil
}

// registerDebugHandlers sets up debug endpoints if enabled in configuration
func (a *App) registerDebugHandlers() {
	if a.cfg.Debug.Enabled {
		debugHandlers := NewDebugHandlers(a, &a.cfg.Debug, a.logger)
		debugHandlers.RegisterDebugEndpoints(a.server.Echo())
	}
}

// serve starts the HTTP server in a goroutine and returns an error channel
func (a *App) serve() <-chan error {
	errCh := make(chan error, 1)

	go func() {
		a.logger.Info().Msg("Server goroutine starting")
		err := a.server.Start()
		a.logger.Info().Err(err).Msg("Server goroutine terminating")

		// Send the error (could be nil if graceful shutdown, or actual error)
		select {
		case errCh <- err:
		default:
			// Channel might be closed already during shutdown
		}
		close(errCh)
	}()

	return errCh
}

// waitForShutdownOrServerError waits for either a shutdown signal or server error
func (a *App) waitForShutdownOrServerError(serverErrCh <-chan error) (bool, error) {
	quit := make(chan os.Signal, 1)
	a.signalHandler.Notify(quit, os.Interrupt, syscall.SIGTERM)
	a.logger.Info().Msg("Signal handler registered, waiting for shutdown signal or server error")

	// Ensure we clean up signal registration regardless of how we exit
	defer func() {
		a.logger.Info().Msg("Cleaning up signal handler")
		signal.Stop(quit)
		a.logger.Info().Msg("Signal handler cleanup complete")
	}()

	// Wait directly on the signal channel instead of spawning another goroutine
	select {
	case <-quit:
		a.logger.Info().Msg("Shutdown requested via signal")
		return true, nil
	case err, ok := <-serverErrCh:
		a.logger.Info().Err(err).Msgf("Server error channel event (channel_open=%t)", ok)
		if !ok {
			return false, nil
		}
		return false, err
	}
}

// drainServerError drains any remaining error from the server error channel
func (a *App) drainServerError(ch <-chan error) error {
	if ch == nil {
		return nil
	}

	// Derive from configured shutdown timeout with headroom
	timeoutDur := 15 * time.Second // default timeout
	if a.cfg != nil {
		timeoutDur = a.cfg.Server.Timeout.Shutdown
		if timeoutDur <= 0 {
			timeoutDur = 15 * time.Second
		} else {
			timeoutDur += 5 * time.Second
		}
	}
	timeout := time.After(timeoutDur)

	if a.logger != nil {
		a.logger.Debug().Msg("Draining server error channel")
	}

	select {
	case err, ok := <-ch:
		if !ok {
			if a.logger != nil {
				a.logger.Debug().Msg("Server error channel closed normally")
			}
			return nil
		}
		if a.logger != nil {
			a.logger.Debug().Err(err).Msg("Server error channel returned error")
		}
		return err
	case <-timeout:
		if a.logger != nil {
			a.logger.Warn().Msg("Timeout waiting for server goroutine to complete - this may indicate a shutdown issue")
		}
		return fmt.Errorf("server goroutine failed to complete within timeout")
	}
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

	a.logger.Info().Msg("Shutting down application")

	// Run shutdown in a goroutine to allow for hard timeout
	shutdownComplete := make(chan error, 1)
	go func() {
		shutdownComplete <- a.Shutdown(ctx)
		close(shutdownComplete)
	}()

	// Wait for shutdown with hard timeout
	var shutdownErr error
	select {
	case shutdownErr = <-shutdownComplete:
		a.logger.Info().Msg("Graceful shutdown completed")
		cancel()
	case <-time.After(15 * time.Second): // 5 seconds longer than shutdown context
		a.logger.Error().Msg("Shutdown timed out, forcing exit")
		cancel()
		return fmt.Errorf("shutdown timed out")
	}

	var errs []error

	if shutdownRequested {
		a.logger.Info().Msg("Waiting for server goroutine to complete")
		if err := a.drainServerError(serverErrCh); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errs = append(errs, fmt.Errorf(serverErrorMsg, err))
		} else {
			a.logger.Info().Msg("Server goroutine completed successfully")
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

	name := strings.TrimSpace(closer.name)
	if name == "" {
		a.logger.Info().Msg("Resource closed successfully")
		return
	}

	// Capitalize the first letter of the name for logging
	r := []rune(name)
	capitalizedName := strings.ToUpper(string(r[0])) + string(r[1:])
	a.logger.Info().Msgf("%s closed successfully", capitalizedName)
}

// shutdownPhase executes a shutdown phase with timing, logging, and error handling
func (a *App) shutdownPhase(phaseName string, shutdownFn func() error, errs *[]error) {
	if shutdownFn == nil {
		return
	}

	phaseStart := time.Now()
	a.logger.Info().Msgf("Shutting down %s", phaseName)

	if err := shutdownFn(); err != nil {
		*errs = append(*errs, err)
		a.logger.Error().Err(err).Msgf("Failed to shutdown %s", phaseName)
		return
	}

	a.logger.Info().Dur("duration", time.Since(phaseStart)).Msgf("%s shutdown completed", capitalizeFirst(phaseName))
}

// capitalizeFirst capitalizes the first letter of a string
func capitalizeFirst(s string) string {
	if s == "" {
		return s
	}
	r := []rune(s)
	return strings.ToUpper(string(r[0])) + string(r[1:])
}

// shutdownManagers stops cleanup loops for database and messaging managers
func (a *App) shutdownManagers() {
	managerStart := time.Now()
	if a.dbManager != nil {
		a.logger.Info().Msg("Stopping database manager cleanup loop")
		a.dbManager.StopCleanup()
	}
	if a.messagingManager != nil {
		a.logger.Info().Msg("Stopping messaging manager cleanup loop")
		a.messagingManager.StopCleanup()
	}
	a.logger.Info().Dur("duration", time.Since(managerStart)).Msg("Manager cleanup loops stopped")
}

// shutdownObservability flushes and shuts down the observability provider
func (a *App) shutdownObservability(ctx context.Context, errs *[]error) {
	if a.observability == nil {
		return
	}

	obsStart := time.Now()

	// Force flush pending spans/metrics before shutdown
	a.logger.Info().Msg("Flushing pending observability data")
	if err := a.observability.ForceFlush(ctx); err != nil {
		a.logger.Warn().Err(err).Msg("Failed to flush observability data")
		// Continue with shutdown even if flush fails
	}

	// Now shut down observability provider
	a.logger.Info().Msg("Shutting down observability provider")
	if err := a.observability.Shutdown(ctx); err != nil {
		*errs = append(*errs, fmt.Errorf("observability: %w", err))
		a.logger.Error().Err(err).Msg("Failed to shutdown observability")
	} else {
		a.logger.Info().Dur("duration", time.Since(obsStart)).Msg("Observability shutdown completed")
	}
}

// shutdownClosers closes all remaining resources registered with the app
func (a *App) shutdownClosers(errs *[]error) {
	if len(a.closers) == 0 {
		return
	}

	closerStart := time.Now()
	a.logger.Info().Msgf("Closing %d remaining resources", len(a.closers))
	for _, closer := range a.closers {
		a.shutdownResource(closer, errs)
	}
	a.logger.Info().Dur("duration", time.Since(closerStart)).Msg("Resource closing completed")
}

// Shutdown gracefully shuts down the application with the given context.
// It closes database connections, messaging client, observability, and stops the HTTP server.
// Returns an aggregated error if any components fail to shut down.
func (a *App) Shutdown(ctx context.Context) error {
	var errs []error
	shutdownStart := time.Now()

	// Shutdown modules first
	a.shutdownPhase("modules", func() error {
		return a.registry.Shutdown()
	}, &errs)

	// Shutdown server
	if a.server != nil {
		a.shutdownPhase("HTTP server", func() error {
			if err := a.server.Shutdown(ctx); err != nil {
				return fmt.Errorf(serverErrorMsg, err)
			}
			return nil
		}, &errs)
	}

	// Flush and shutdown observability (export pending telemetry)
	a.shutdownObservability(ctx, &errs)

	// Stop cleanup loops for managers
	a.shutdownManagers()

	// Close remaining resources
	a.shutdownClosers(&errs)

	a.logger.Info().Dur("total_duration", time.Since(shutdownStart)).Msg("Application shutdown complete")

	// Return aggregated errors if any occurred
	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	return nil
}

// readyCheck handles the health check endpoint
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
