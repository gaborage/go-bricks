package app

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"syscall"
	"time"

	"github.com/labstack/echo/v4"
)

// startMaintenanceLoops starts background cleanup processes for managers
func (a *App) startMaintenanceLoops() {
	// Start cleanup for unified managers
	if a.dbManager != nil {
		a.dbManager.StartCleanup(5 * time.Minute) // Database cleanup every 5 minutes
	}
	if a.messagingManager != nil {
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

	a.registry.RegisterRoutes(a.server.ModuleGroup())
	a.startMaintenanceLoops()

	return nil
}

// serve starts the HTTP server in a goroutine and returns an error channel
func (a *App) serve() <-chan error {
	errCh := make(chan error, 1)

	go func() {
		err := a.server.Start()
		errCh <- err
		close(errCh)
	}()

	return errCh
}

// waitForShutdownOrServerError waits for either a shutdown signal or server error
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

// drainServerError drains any remaining error from the server error channel
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

	name := strings.TrimSpace(closer.name)
	if name == "" {
		a.logger.Info().Msg("Resource closed successfully")
		return
	}

	// Capitalize the first letter of the name for logging
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
