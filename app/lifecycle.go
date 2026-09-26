package app

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"os"
	"os/signal"
	"runtime/debug"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sync/singleflight"

	"github.com/gaborage/go-bricks/config"
	"github.com/gaborage/go-bricks/logger"
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

	if err := a.requireJudge(); err != nil {
		return err
	}

	if err := a.requireProbeSeam(); err != nil {
		return err
	}

	if err := a.buildMessagingDeclarations(); err != nil {
		return err
	}

	if err := a.assertMessagingConfiguredIfDeclared(a.messagingDeclarations); err != nil {
		return err
	}

	if err := a.startSlots(ctx); err != nil {
		return err
	}

	// Every route registered from here on belongs to this App.
	routesStart := server.DefaultRouteRegistry.Count()

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

	// Every startup resolution has run (sealing at declaration time above, HTTP jose
	// at route registration), so the keystore's role log is complete.
	warnDualRoleKeys(a.logger, a.registry.deps.KeyStore)

	return a.runPostRegisterRoutes(routesStart)
}

// runPostRegisterRoutes hands the consumer's hook this App's route table once every
// framework check has passed, so the consumer veto runs last.
func (a *App) runPostRegisterRoutes(routesStart int) error {
	if a.postRegisterRoutes == nil {
		return nil
	}
	if err := a.postRegisterRoutes(slices.Concat(a.probeRoutes, a.registry.routesSince(routesStart))); err != nil {
		return fmt.Errorf("app.Options.PostRegisterRoutes rejected the route table: %w", err)
	}
	return nil
}

// warnDualRoleKeys logs one WARN per keystore entry resolved by both HTTP jose and
// sealing (#1306, ADR-097 namespace hygiene). Warn only: the two protocols must not
// share a kid, but an enforced partition would break shipped HTTP surface. The line
// names the entry and its role tags, never key material. A store without a role log
// (no keystore module, a consumer-supplied store) reports nothing.
func warnDualRoleKeys(log logger.Logger, ks KeyStore) {
	reporter, ok := ks.(dualRoleReporter)
	if !ok {
		return
	}
	dual := reporter.DualRoleEntries()
	for _, entry := range slices.Sorted(maps.Keys(dual)) {
		log.Warn().
			Str("entry", entry).
			Str("roles", strings.Join(dual[entry], ",")).
			Msg("keystore entry resolved by both HTTP jose and sealing; never reuse a kid across them")
	}
}

// dualRoleReporter is keystore.DualRoleReporter spelled locally: app cannot import
// keystore (keystore imports app), so the door is a plain-typed method.
type dualRoleReporter interface {
	DualRoleEntries() map[string][]string
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
		// Seal the kind's description here, and only here: after its start returned without a
		// fatal error, before serve(), and never again — including during shutdown, so a
		// /ready overlapping stopSlots reads a stable value (ADR-066 as amended).
		slot.seal(slot.describe())
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
	// Every kind has sealed; the judge may now answer from the report rather than failing
	// closed. This is the only write, and it happens before serve() starts the listener
	// goroutine — that start orders it before every request's read.
	a.judge.started = true
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

// requireProbeSeam fails closed when server.probes.port is set but the configured server
// cannot report the probe listener's serve error: the key would otherwise be silently
// ignored (ADR-120). *server.Server always implements the seam; an injected
// Options.Server may not.
func (a *App) requireProbeSeam() error {
	if a.cfg == nil || a.cfg.Server.Probes.Port <= 0 {
		return nil
	}
	if _, ok := a.server.(probeRunner); ok {
		return nil
	}
	return fmt.Errorf("server.probes.port is set (%d) but the configured server does not support the probe listener",
		a.cfg.Server.Probes.Port)
}

var _ probeRunner = (*server.Server)(nil)

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
		"(exchanges=%d, queues=%d, bindings=%d, publishers=%d, consumers=%d) "+
		"but messaging is not configured; "+
		"set messaging.broker.url (or env MESSAGING_BROKER_URL)",
		s.Exchanges, s.Queues, s.Bindings, s.Publishers, s.Consumers)
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

// serve starts the HTTP server in a goroutine and returns the channel both listeners report
// on (ADR-120): Start's result, and the probe listener's serve error when the server has
// one. Each of the two senders sends at most once, so the two-slot buffer never blocks a
// send, and the channel closes only after both have finished.
func (a *App) serve() <-chan error {
	errCh := make(chan error, 2)
	var senders sync.WaitGroup

	senders.Go(func() {
		a.logger.Info().Msg("Server goroutine starting")
		err := a.server.Start()
		a.logger.Info().Err(err).Msg("Server goroutine terminating")

		// nil after a graceful shutdown, which Start may report only once its drain ends.
		errCh <- err
	})
	// A nil ProbeErrors breaks the seam's contract; ranging over it would park the forwarder,
	// and with it the close, for good.
	if probes, ok := a.server.(probeRunner); ok && probes.ProbeErrors() != nil {
		probeErrs := probes.ProbeErrors()
		senders.Go(func() { forwardProbeError(probeErrs, errCh) })
	}
	go func() {
		senders.Wait()
		close(errCh)
	}()

	return errCh
}

// forwardProbeError relays the probe listener's first serve failure onto errCh and returns
// once ProbeErrors closes. A clean stop sends nothing, so the probe listener stopping never
// ends Run; sending at most once keeps serve's buffer from blocking on a runner that breaks
// the ProbeErrors contract.
func forwardProbeError(probeErrs <-chan error, errCh chan<- error) {
	sent := false
	for err := range probeErrs {
		if isServeFailure(err) && !sent {
			errCh <- err
			sent = true
		}
	}
}

// isServeFailure reports whether a listener's result is a failure rather than a clean
// stop: nil follows a graceful Shutdown, and http.ErrServerClosed a Shutdown that vetoed
// Start.
func isServeFailure(err error) bool {
	return err != nil && !errors.Is(err, http.ErrServerClosed)
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

// drainServerError reads the server error channel until it closes, bounded by the outer
// shutdown timeout, and returns every serve failure either listener reported, joined
// (ADR-120). Clean stops (nil, http.ErrServerClosed) are dropped value by value, so a
// joined result never hides a failure behind the sentinel.
func (a *App) drainServerError(ch <-chan error) error {
	if ch == nil {
		return nil
	}

	_, outer := a.shutdownTimeouts()

	if a.logger != nil {
		a.logger.Debug().Msg("Draining server error channel")
	}

	err := errors.Join(a.receiveServeFailures(ch, time.After(outer))...)
	if a.logger != nil && err != nil {
		a.logger.Debug().Err(err).Msg("Server error channel returned error")
	}
	return err
}

// receiveServeFailures collects the serve failures ch carries until it closes. If timeout
// fires first, a sender never finished, which is itself a failure, reported alongside
// those already collected.
func (a *App) receiveServeFailures(ch <-chan error, timeout <-chan time.Time) []error {
	var failures []error
	for {
		select {
		case err, ok := <-ch:
			if !ok {
				return failures
			}
			if isServeFailure(err) {
				failures = append(failures, err)
			}
		case <-timeout:
			if a.logger != nil {
				a.logger.Warn().Msg("Timeout waiting for server goroutine to complete - this may indicate a shutdown issue")
			}
			return append(failures, errors.New("server goroutine failed to complete within timeout"))
		}
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

	if isServeFailure(serverErr) {
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

	return a.runResult(serverErr, serverErrCh, shutdownErr)
}

// runResult joins what Run reports once shutdown has completed: the serve error that woke
// it, whatever either listener still reports while the channel drains, and the shutdown
// error. The drain runs on the server-error path too, so a probe listener failure that
// follows an application one is joined rather than lost (ADR-120).
func (a *App) runResult(serverErr error, serverErrCh <-chan error, shutdownErr error) error {
	var errs []error
	if isServeFailure(serverErr) {
		errs = append(errs, fmt.Errorf(serverErrorMsg, serverErr))
	}

	a.logger.Info().Msg("Waiting for server goroutine to complete")
	if err := a.drainServerError(serverErrCh); err != nil {
		errs = append(errs, fmt.Errorf(serverErrorMsg, err))
	} else if len(errs) == 0 {
		a.logger.Info().Msg("Server goroutine completed successfully")
	}

	return errors.Join(append(errs, shutdownErr)...)
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

// shutdownObservability flushes and shuts down the observability provider. The phase is
// best-effort: a telemetry sink that is unreachable is not an application failure, so its
// error is warned once and never folded into the shutdown error (ADR-029 amendment).
func (a *App) shutdownObservability(ctx context.Context) {
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
		a.logger.Warn().Err(err).Dur("duration", time.Since(obsStart)).Msg(observabilityShutdownWarnMsg)
		return
	}

	a.logger.Info().Dur("duration", time.Since(obsStart)).Msg("Observability shutdown completed")
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
	a.shutdownObservability(ctx)

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
// it and no more. Concurrent requests share one judgment (ADR-120), but each logs and
// renders its own answer from the verdict, so the failure log fires once per request, as
// it did before the judgment was shared.
func (a *App) readyCheck(c server.HandlerContext) error {
	ctx := c.RequestContext()
	verdict, err := a.judgeReadiness(ctx)
	if err != nil {
		return err
	}
	if verdict.found {
		// /ready is unauthenticated. On the application listener the limiters apply to it
		// and key probes by client IP (probeSkipper skips tenant resolution, not the
		// limiters); with server.probes.port set, /ready is on the probe listener, which has
		// no limiter (ADR-120). Either way one source can still abandon many requests in a
		// row. Where the limiters apply, the IP is derived through the trusted-proxy chain
		// (ADR-057), so only a caller already inside a default-trusted range (loopback,
		// link-local, RFC1918, IPv6 ULA) can still choose its own key, and the budget is
		// per-source. An abandoned request — the caller's own context canceled, and the
		// probe reports that same context.Canceled, or the request stopped waiting on the
		// shared judgment when it was canceled — is not a readiness incident, so it logs
		// WARN, not ERROR. The caller's context must actually be done: a probe that
		// reports context.Canceled while the request is still live was canceled from inside,
		// which is a genuine incident and stays ERROR.
		blocking := &verdict.blocking
		event := a.logger.Error()
		if errors.Is(ctx.Err(), context.Canceled) && errors.Is(blocking.Err, context.Canceled) {
			event = a.logger.Warn()
		}
		event.Err(blocking.Err).Str("component", blocking.Name).Msg("Readiness check failed")
		return c.JSON(http.StatusServiceUnavailable, notReadyBody(blocking))
	}

	app := &config.AppConfig{}
	if a.cfg != nil {
		app = &a.cfg.App
	}
	return c.JSON(http.StatusOK, verdict.report.readyBody(app, time.Now()))
}

// readinessVerdict is one framework judgment: what a readiness flight shares with every
// /ready request waiting on it.
type readinessVerdict struct {
	report   readinessReport
	blocking HealthStatus
	found    bool
}

// readinessFlightKey keys the one framework-judgment flight per App.
const readinessFlightKey = "readiness-judgment"

// judgeReadiness runs the framework judgment at most once across concurrent /ready requests
// (ADR-120). Only an in-flight verdict is shared: the next request after a judgment finishes
// starts a new one. A caller whose request is canceled stops waiting at once with a verdict
// naming readiness itself and carrying its ctx error, since the blocking kind is not yet
// known, while the judgment runs on for the rest. A caller whose own deadline expires waits
// for the verdict instead, so it still names the kind that blocked: the flight ends by the
// leader's deadline, so the leader waits only the probes' return latency past its own. A
// probe that ignores its context holds the caller, as it held the request that judged on its
// own. The only error is a flight that panicked, which the caller
// returns to the engine's error handler.
func (a *App) judgeReadiness(ctx context.Context) (readinessVerdict, error) {
	results := a.readyFlight.DoChan(readinessFlightKey, func() (any, error) {
		return a.readinessFlight(ctx)
	})
	var result singleflight.Result
	select {
	case result = <-results:
	case <-ctx.Done():
		if errors.Is(ctx.Err(), context.Canceled) {
			return readinessVerdict{blocking: readinessFailure(ctx.Err()), found: true}, nil
		}
		result = <-results
	}
	if result.Err != nil {
		return readinessVerdict{}, result.Err
	}
	verdict, _ := result.Val.(readinessVerdict) // a flight that returns no error returns a verdict
	return verdict, nil
}

// readinessFlight is one framework judgment on readinessFlightContext's context. It recovers
// its own panic, names it by type only (ADR-081) and logs it with its stack, which carries no
// panic value: DoChan re-panics a flight's panic on a new goroutine that no recover reaches,
// which would end the process, and the waiters return the error to an error handler that
// logs no stack for it. completed, not the recovered value, separates a normal return from a
// panic: under GODEBUG=panicnil=1 a panic(nil) recovers as nil.
func (a *App) readinessFlight(leaderCtx context.Context) (verdict readinessVerdict, err error) {
	completed := false
	defer func() {
		if completed {
			return
		}
		r := recover()
		err = fmt.Errorf("readiness judgment panicked (type: %T)", r)
		a.logger.Error().Err(err).Bytes("stack", debug.Stack()).Msg("Readiness judgment panicked")
	}()
	ctx, cancel := a.readinessFlightContext(leaderCtx)
	defer cancel()
	report, blocking, found := a.judge.gate(ctx)
	completed = true
	return readinessVerdict{report: report, blocking: blocking, found: found}, nil
}

// readinessFlightContext is a judgment's context: the leader's, detached from its
// cancellation so the leader walking away fails no follower, ending at the earlier of the
// leader's own deadline and server.timeout.middleware from now. The engine sets that deadline
// to the same timeout from the start of the leader's request, so the flight ends with the
// leader's budget, which on the probe listener covers the application-listener check too;
// with neither, no deadline is added, as none bounded that request. Shutdown waits for no
// flight: one that outlives stopSlots reads a stopping slot and reports unhealthy, while
// /ready already answers 503 from the server's stopping latch.
func (a *App) readinessFlightContext(leaderCtx context.Context) (context.Context, context.CancelFunc) {
	ctx := context.WithoutCancel(leaderCtx)
	deadline, bounded := leaderCtx.Deadline()
	if a.cfg != nil && a.cfg.Server.Timeout.Middleware > 0 {
		if budget := time.Now().Add(a.cfg.Server.Timeout.Middleware); !bounded || budget.Before(deadline) {
			deadline, bounded = budget, true
		}
	}
	if !bounded {
		return context.WithCancel(ctx)
	}
	return context.WithDeadline(ctx, deadline)
}
