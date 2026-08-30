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

	"github.com/gaborage/go-bricks/config"
	"github.com/gaborage/go-bricks/messaging"
	"github.com/gaborage/go-bricks/server"
)

// prepareRuntime prepares the application for runtime execution. ctx is the
// startup context: components it starts that outlive startup inherit that
// context's values rather than beginning from a bare context.Background().
// AMQP consumers are the one exception: they outlive prepareRuntime itself
// and are stopped by the messaging slot's stop phase (ADR-029), so they inherit
// only ctx's values, never its cancellation — see messagingSlot.start.
func (a *App) prepareRuntime(ctx context.Context) error {
	if err := a.requireSlots("prepareRuntime"); err != nil {
		return err
	}

	if err := a.buildMessagingDeclarations(); err != nil {
		return err
	}

	if err := a.assertMessagingConfiguredIfDeclared(a.messagingDeclarations); err != nil {
		return err
	}

	if err := a.assertSharedMessagingRuntimeExists(); err != nil {
		return err
	}

	if err := a.startSlots(ctx); err != nil {
		return err
	}

	// Re-collected here because the streams slot only builds its manager in start; see
	// streamsSlot.probe in slot.go.
	a.healthProbes = a.collectProbes()

	// Register debug endpoints if enabled
	if err := a.registerDebugHandlers(); err != nil {
		return err
	}

	// Register scheduled jobs (after all modules initialized, before routes)
	if err := a.registry.RegisterJobs(); err != nil {
		return err
	}

	if err := a.applyGlobalMiddleware(); err != nil {
		return err
	}

	a.registry.RegisterRoutes(a.server.ModuleGroup())
	if err := a.checkRouteConflicts(); err != nil {
		return err
	}

	return nil
}

// assertSharedMessagingRuntimeExists refuses a configuration config.Validate now
// accepts but this binary cannot serve. messaging.tenancy: shared is validated by
// the config layer one slice before the runtime that acts on it exists, so between
// those two merges a deployment could boot with per-tenant replay while its config
// says shared. Fail closed until the classic-lane slice lands, which deletes this.
func (a *App) assertSharedMessagingRuntimeExists() error {
	if a.cfg == nil || !a.cfg.Multitenant.Enabled || a.cfg.Messaging.Tenancy != config.TenancyShared {
		return nil
	}
	return errors.New("messaging.tenancy: shared is not yet served by this build; " +
		"the shared messaging runtime lands with the classic-lane slice — " +
		"set messaging.tenancy: per-tenant until then")
}

// startSlots runs every kind's start phase in registration order. A fatal error aborts
// startup at the kind that reported it, so nothing after it runs, and the kinds already up
// are stopped again before it is returned. Advisory errors — the best-effort single-tenant
// pre-warms — are aggregated into the one WARN prepareRuntime has always emitted, and never
// fail startup. A fatal discards advisories already collected; each kind logged its own WARN.
func (a *App) startSlots(ctx context.Context) error {
	var advisories []error
	started := make([]resourceSlot, 0, len(a.slots))
	for _, slot := range a.slots {
		advisory, fatal := slot.start(ctx)
		if fatal != nil {
			// The kinds already up own live inbound work — the messaging slot's consumers run
			// under context.WithoutCancel, so the aborting startup context never reaches them —
			// and Run returns a prepareRuntime failure without calling Shutdown. Unwinding here
			// is the only thing that stops them.
			stopEach(ctx, started)
			return fatal
		}
		started = append(started, slot)
		if advisory != nil {
			advisories = append(advisories, advisory)
		}
	}

	if len(advisories) > 0 {
		a.logger.Warn().
			Err(fmt.Errorf("pre-warming issues (non-fatal): %w", errors.Join(advisories...))).
			Msg("Pre-warming completed with warnings")
	}
	return nil
}

// stopSlots halts every kind's inbound work in registration order, before modules are torn
// down (ADR-029). Connections stay open — the close phase, after module Shutdown, owns those.
func (a *App) stopSlots(ctx context.Context) {
	stopEach(ctx, a.slots)
}

// stopEach runs the stop phase over slots in the order given. stop never fails, so there is
// nothing to aggregate.
func stopEach(ctx context.Context, slots []resourceSlot) {
	for _, slot := range slots {
		slot.stop(ctx)
	}
}

// checkRouteConflicts fails startup when two registrations claimed the same
// method+path: echo's router silently overwrites (last one wins), so the first
// handler would be dead on arrival. Fail Fast: surface every collision at once.
// Servers that don't expose conflict tracking (test fakes) are skipped.
func (a *App) checkRouteConflicts() error {
	cs, ok := a.server.(interface{ RouteConflicts() []server.RouteConflict })
	if !ok {
		return nil
	}
	conflicts := cs.RouteConflicts()
	if len(conflicts) == 0 {
		return nil
	}
	errs := make([]error, 0, len(conflicts)+1)
	errs = append(errs, fmt.Errorf("duplicate route registration (%d conflict(s))", len(conflicts)))
	for _, c := range conflicts {
		errs = append(errs, fmt.Errorf("%s %s — first: %s (%s), duplicate: %s (%s)",
			c.Method, c.Path,
			c.First.HandlerName, c.First.Package,
			c.Duplicate.HandlerName, c.Duplicate.Package))
	}
	return errors.Join(errs...)
}

// applyGlobalMiddleware registers module-contributed global middleware on the server. It
// fails closed: if any module registered middleware but the server cannot install it, the
// gate (canonically auth) would be silently absent, so startup aborts rather than serving
// unguarded traffic.
func (a *App) applyGlobalMiddleware() error {
	mws := a.registry.CollectGlobalMiddleware()
	if len(mws) == 0 {
		return nil
	}
	reg, ok := a.server.(interface {
		RegisterGlobalMiddleware(mw ...server.MiddlewareFunc)
	})
	if !ok {
		return fmt.Errorf("%d module(s) registered global middleware but the configured server does not support it", len(mws))
	}
	reg.RegisterGlobalMiddleware(mws...)
	a.logger.Info().Int("count", len(mws)).Msg("Registered global middleware")
	return nil
}

// assertMessagingConfiguredIfDeclared fails-fast in single-tenant mode when
// a module has declared messaging infrastructure but no broker URL is set —
// without this check the declarations would be silently dropped (issue #366).
// Multi-tenant mode resolves messaging per-tenant via the resource source, so
// the static check is skipped there.
func (a *App) assertMessagingConfiguredIfDeclared(decls *messaging.Declarations) error {
	if a.cfg.Multitenant.Enabled || decls == nil || decls.IsEmpty() {
		return nil
	}
	if config.IsMessagingConfigured(&a.cfg.Messaging) {
		return nil
	}
	s := decls.Stats()
	return fmt.Errorf("messaging declarations were registered "+
		"(publishers=%d, consumers=%d, exchanges=%d, queues=%d, bindings=%d) "+
		"but messaging is not configured; "+
		"set messaging.broker.url (or env MESSAGING_BROKER_URL)",
		s.Publishers, s.Consumers, s.Exchanges, s.Queues, s.Bindings)
}

// registerDebugHandlers sets up debug endpoints if enabled in configuration. The error is
// fatal: RegisterDebugEndpoints refuses a registration that would expose the group with no
// access control at all (ADR-049).
func (a *App) registerDebugHandlers() error {
	if !a.cfg.Debug.Enabled {
		return nil
	}
	debugHandlers := NewDebugHandlers(a, &a.cfg.Debug, a.logger)
	return debugHandlers.RegisterDebugEndpoints(a.server.RootGroup())
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

// shutdownTimeouts returns the inner timeout (passed to module Shutdown calls)
// and the outer hard-stop deadline (inner + 5s headroom). Falls back to 10s/15s
// when cfg is nil or the configured value is non-positive — matching the
// documented server.timeout.shutdown default. Reading both values from a single
// helper keeps drainServerError and the main shutdown sequence in lockstep.
func (a *App) shutdownTimeouts() (inner, outer time.Duration) {
	inner = 10 * time.Second
	if a.cfg != nil && a.cfg.Server.Timeout.Shutdown > 0 {
		inner = a.cfg.Server.Timeout.Shutdown
	}
	outer = inner + 5*time.Second
	return inner, outer
}

// drainServerError drains any remaining error from the server error channel
func (a *App) drainServerError(ch <-chan error) error {
	if ch == nil {
		return nil
	}

	_, outer := a.shutdownTimeouts()
	timeout := time.After(outer)

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
		return errors.New("server goroutine failed to complete within timeout")
	}
}

// Run starts the application and blocks until a shutdown signal is received.
// It handles graceful shutdown with a timeout.
func (a *App) Run() error {
	// Run is the process entry point, so the startup context is rooted here and
	// threaded down; nothing above it has a context to inherit.
	if err := a.prepareRuntime(context.Background()); err != nil {
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

	inner, outer := a.shutdownTimeouts()
	ctx, cancel := a.timeoutProvider.WithTimeout(context.Background(), inner)

	a.logger.Info().Msg("Shutting down application")

	// Run shutdown in a goroutine to allow for hard timeout
	shutdownComplete := make(chan error, 1)
	go func() {
		shutdownComplete <- a.Shutdown(ctx)
		close(shutdownComplete)
	}()

	// Wait for shutdown with hard timeout (5s headroom over inner via shutdownTimeouts)
	var shutdownErr error
	select {
	case shutdownErr = <-shutdownComplete:
		a.logger.Info().Msg("Graceful shutdown completed")
		cancel()
	case <-time.After(outer):
		a.logger.Error().Msg("Shutdown timed out, forcing exit")
		cancel()
		return errors.New("shutdown timed out")
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

// shutdownConsumers stops AMQP consumers from accepting NEW messages before modules are
// torn down, so the framework stops handing fresh work to modules that are shutting down.
// It cancels each consumer's context (which propagates to in-flight handlers) but does not
// synchronously join them, and does NOT close the underlying connections — the
// messaging-manager closer does that later. No-op when messaging is not configured.
func (a *App) shutdownConsumers() {
	if a.messagingManager == nil {
		return
	}
	a.logger.Info().Msg("Stopping messaging consumers")
	a.messagingManager.StopConsumers()
}

// shutdownObservability flushes and shuts down the observability provider
func (a *App) shutdownObservability(ctx context.Context, errs *[]error) {
	if a.observability == nil {
		return
	}

	obsStart := time.Now()

	a.logger.Info().Msg("Flushing pending observability data")
	if err := a.observability.ForceFlush(ctx); err != nil {
		a.logger.Warn().Err(err).Msg("Failed to flush observability data")
		// Continue with shutdown even if flush fails
	}

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

	// Order matters. Stop inbound work BEFORE tearing down what it depends on, so the
	// framework stops handing new HTTP requests and AMQP messages to modules/resources that
	// are shutting down (the previous order shut modules down first, while the server was
	// still serving and consumers still delivering — so handlers ran against dead modules).

	// 1. Stop accepting new HTTP requests; the server drains its in-flight handlers within ctx.
	if a.server != nil {
		a.shutdownPhase("HTTP server", func() error {
			if err := a.server.Shutdown(ctx); err != nil {
				return fmt.Errorf(serverErrorMsg, err)
			}
			return nil
		}, &errs)
	}

	// 2. Stop each kind's inbound work (connections are closed later, in step 5, via the
	//    slots' closers). Done before module shutdown so the framework stops delivering fresh
	//    messages to modules that are about to be torn down.
	//    Deliberately unguarded, unlike prepareRuntime: teardown is best-effort and must not
	//    fail on a hand-built App that never installed slots.
	a.stopSlots(ctx)

	// 3. Shut down modules — no new HTTP requests or AMQP deliveries are admitted at this
	//    point. AMQP handlers already in flight may still be unwinding after cancellation.
	a.shutdownPhase("modules", func() error {
		return a.registry.Shutdown()
	}, &errs)

	// 4. Flush and shutdown observability (export pending telemetry).
	a.shutdownObservability(ctx, &errs)

	// 5. Close remaining resources (DB pools, messaging connections, etc.). Each manager's
	//    Close stops the idle-cleanup sweep it started (ADR-067), so the loops still stop
	//    last, in the order ADR-029 fixed.
	a.shutdownClosers(&errs)

	a.logger.Info().Dur("total_duration", time.Since(shutdownStart)).Msg("Application shutdown complete")

	// Return aggregated errors if any occurred
	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	return nil
}

// readyCheck handles the readiness endpoint: one probe run, one gate, one body (ADR-066).
// The run stops at the first failing critical kind, so an outage costs the probes ahead of
// it and no more.
func (a *App) readyCheck(c server.HandlerContext) error {
	ctx := c.RequestContext()
	report, blocking, found := runUntilBlocking(ctx, a.healthProbes)

	if found {
		// /ready is unauthenticated and the limiters do not exempt it, but they key probes
		// by client IP (probeSkipper skips tenant resolution, not the limiters), so one
		// source can still abandon many requests in a row. That IP is derived through the
		// trusted-proxy chain (ADR-057), so only a caller already inside a default-trusted
		// range (loopback, link-local, RFC1918, IPv6 ULA) can still choose its own key, and
		// the budget is per-source either way. An abandoned request — the
		// caller's own context canceled, and the probe reports that same context.Canceled —
		// is not a readiness incident, so it logs WARN, not ERROR. The caller's context must
		// actually be done: a probe that reports context.Canceled while the request is still
		// live was canceled from inside, which is a genuine incident and stays ERROR.
		event := a.logger.Error()
		if errors.Is(ctx.Err(), context.Canceled) && errors.Is(blocking.Err, context.Canceled) {
			event = a.logger.Warn()
		}
		event.Err(blocking.Err).Str("component", blocking.Name).Msg("Readiness check failed")
		return c.JSON(http.StatusServiceUnavailable, notReadyBody(&blocking))
	}

	app := &config.AppConfig{}
	if a.cfg != nil {
		app = &a.cfg.App
	}
	return c.JSON(http.StatusOK, report.readyBody(app, time.Now()))
}
