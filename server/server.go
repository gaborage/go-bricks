// Package server provides HTTP server functionality using Echo framework.
// It includes middleware setup, routing, and request handling.
package server

import (
	"context"
	"crypto/tls"
	goerrors "errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/labstack/echo/v5"
	"github.com/labstack/echo/v5/middleware"

	"github.com/gaborage/go-bricks/config"
	"github.com/gaborage/go-bricks/internal/pathutil"
	"github.com/gaborage/go-bricks/logger"
)

// Server represents an HTTP server instance with Echo framework.
// It manages server lifecycle, configuration, and request handling.
type Server struct {
	echo         *echo.Echo
	httpServer   atomic.Pointer[http.Server] // Store the actual http.Server instance for proper shutdown with race-free access
	cfg          *config.Config
	logger       logger.Logger
	basePath     string
	healthRoute  string
	readyRoute   string
	readyMu      sync.RWMutex
	readyHandler echo.HandlerFunc
	conflicts    *routeConflictTracker
	boundAddr    atomic.Pointer[net.Addr] // set via ListenerAddrFunc once Start's listener is bound; nil until then
	ready        chan struct{}
	started      atomic.Bool
	// lifecycleMu serializes the readiness commit in onBeforeServe with Shutdown.
	// stopping and httpServer stay atomic because Start and the lifecycle tests read
	// them outside this lock.
	lifecycleMu sync.Mutex
	stopping    atomic.Bool
	// testHookReadyCommit runs inside the readiness commit while lifecycleMu is held.
	// Only the lifecycle test sets it, to park a commit and prove Shutdown cannot
	// return while one is in flight.
	testHookReadyCommit func()

	// probeEcho serves /health and /ready on the probe listener (ADR-120); nil when the
	// listener is disabled and the application engine serves the probes itself.
	probeEcho      *echo.Echo
	probeAddr      string
	probe          atomic.Pointer[probeListener]
	probeBoundAddr atomic.Pointer[net.Addr]
	probeErrs      chan error
	closeProbeErrs func()
	// appCheck is the probe listener's application-listener check, built in Start before
	// either bind; nil when the probe listener is disabled.
	appCheck *appListenerCheck
	// probeStopBudget bounds the probe listener's graceful stop; only a test shortens it.
	probeStopBudget time.Duration
}

// probeListener is the probe listener's server and the socket it serves, stored together
// so a stop releases the socket even before Serve has tracked it.
type probeListener struct {
	srv *http.Server
	ln  net.Listener
}

// close stops the probe listener at once. Closing ln as well releases the port even when
// Serve has not yet tracked the listener, which srv.Close alone would miss.
func (p *probeListener) close() {
	_ = p.srv.Close()
	_ = p.ln.Close()
}

// probeStopTimeout is the probe listener's graceful-stop budget, detached from the
// caller's Shutdown context so a probe in flight at that deadline cannot fail a clean exit.
const probeStopTimeout = time.Second

// probeMethods are the methods each probe answers.
var probeMethods = []string{http.MethodGet, http.MethodHead}

// serverOption adjusts a Server in newServer before its engines and routes are wired.
type serverOption func(*Server)

// withEphemeralProbeListener enables the probe listener on 127.0.0.1:0 while
// server.probes.port stays 0, so the Start-time collision rule does not judge it. Tests
// only: a consumer test that wants a probe listener binds a real free port.
func withEphemeralProbeListener() serverOption {
	return func(s *Server) { s.probeAddr = "127.0.0.1:0" }
}

// hostPort joins a listener's bind address. It trims brackets first, so "::" and "[::]",
// both spellings the probe collision rule accepts, bind the same address.
func hostPort(host string, port int) string {
	return net.JoinHostPort(strings.Trim(host, "[]"), strconv.Itoa(port))
}

// normalizeBasePath cannot use pathutil.NormalizePrefix because that helper
// collapses "/" to "" while buildFullPath treats "/" as a meaningful state
// distinct from the empty no-prefix case. Diverging on purpose.
func normalizeBasePath(basePath string) string {
	if basePath == "" {
		return ""
	}
	if !strings.HasPrefix(basePath, "/") {
		basePath = "/" + basePath
	}
	if len(basePath) > 1 {
		basePath = strings.TrimRight(basePath, "/")
	}
	return basePath
}

func normalizeRoutePath(route, defaultRoute string) string {
	if route == "" {
		route = defaultRoute
	}
	return pathutil.EnsureLeadingSlash(route)
}

// buildFullPath combines base path with route path
func (s *Server) buildFullPath(route string) string {
	if s.basePath == "" || s.basePath == "/" {
		return route
	}

	// If route is just "/", don't append it to avoid double slashes
	if route == "/" {
		return s.basePath
	}

	return s.basePath + route
}

// trustedProxyOptions turns the configured server.trustedproxies CIDR ranges
// into echo TrustOptions, preserving echo's loopback/link-local/private
// defaults (dropping those would break every in-VPC deployment by keying
// every request on the load balancer's own address).
//
// Every entry is re-vetted through config.ParseTrustedProxyCIDR — the same rule set
// startup validation applies — rather than parsed here, because the re-vet stays
// load-bearing for callers outside the app construction path — server.New used
// directly, or a Builder assembled without WithConfig — which never pass
// config.Validate (ADR-064 closed the NewWithConfig bypass). Without the re-vet, one
// `0.0.0.0/0` or host-bits entry would trust every hop and hand the extractor back
// the caller-authored left-most X-Forwarded-For value — the exact spoofing ADR-057
// closes. Skipping is the safe response because echo's TrustOptions are purely
// additive, so dropping one can only narrow trust, and the ERROR log makes it visible.
func trustedProxyOptions(trustedProxies []string, log logger.Logger) []echo.TrustOption {
	opts := make([]echo.TrustOption, 0, len(trustedProxies))
	nets := make([]*net.IPNet, 0, len(trustedProxies))
	for _, entry := range trustedProxies {
		ipNet, err := config.ParseTrustedProxyCIDR(entry)
		if err != nil {
			log.Error().Err(err).Str("cidr", entry).
				Msg("Ignoring invalid server.trustedproxies entry; its proxy will be treated as an untrusted client")
			continue
		}
		nets = append(nets, ipNet)
		opts = append(opts, echo.TrustIPRange(ipNet))
	}

	// Per-entry vetting cannot see that a SET trusts everyone: ["0.0.0.0/1","128.0.0.0/1"]
	// is two properly-masked entries covering all of IPv4 between them. Trusting every
	// address hands the extractor back the caller-authored left-most X-Forwarded-For value,
	// which is the spoofing this re-vet exists to prevent, so the whole list is dropped
	// (ADR-080).
	for _, bits := range []int{net.IPv4len * 8, net.IPv6len * 8} {
		if config.CoversAddressFamily(nets, bits) {
			log.Error().Str("cidrs", strings.Join(trustedProxies, ",")).
				Msg("Ignoring server.trustedproxies entirely: the entries together trust every address, which would restore X-Forwarded-For spoofing")
			return nil
		}
	}
	return opts
}

// New creates a new HTTP server instance with the given configuration and logger.
// It initializes Echo with middlewares, error handling, and health check endpoints.
// With server.probes.port set it also builds the probe listener's engine (ADR-120).
func New(cfg *config.Config, log logger.Logger) *Server {
	return newServer(cfg, log)
}

func newServer(cfg *config.Config, log logger.Logger, opts ...serverOption) *Server {
	SetCaptureStackTraces(cfg.App.IsDevelopment())

	e := echo.New()
	e.HTTPErrorHandler = httpErrorHandler(cfg, log)

	// Derive RealIP() by walking X-Forwarded-For right-to-left and returning the
	// first untrusted hop, so the address that keys rate limits and appears in
	// access logs is not one the caller writes. Echo trusts loopback, link-local
	// and RFC1918 ranges by default, so a service behind an in-VPC load balancer
	// needs no configuration; server.trustedproxies adds ranges for a proxy that
	// sits on a public address. X-Real-IP is deliberately not honored — it is
	// caller-authored whenever the proxy does not overwrite it, and honoring it
	// would reopen the hole for deployments whose proxy strips XFF.
	// This discharges the trusted-proxy follow-up recorded in ADR-015 (see ADR-057).
	e.IPExtractor = echo.ExtractIPFromXFFHeader(trustedProxyOptions(cfg.Server.TrustedProxies, log)...)
	e.Validator = NewValidator()

	// Initialize server with path configuration
	basePath := normalizeBasePath(cfg.Server.Path.Base)
	healthRoute := normalizeRoutePath(cfg.Server.Path.Health, "/health")
	readyRoute := normalizeRoutePath(cfg.Server.Path.Ready, "/ready")

	s := &Server{
		echo:            e,
		cfg:             cfg,
		logger:          log,
		basePath:        basePath,
		healthRoute:     healthRoute,
		readyRoute:      readyRoute,
		readyHandler:    nil,
		conflicts:       newRouteConflictTracker(),
		ready:           make(chan struct{}),
		probeErrs:       make(chan error, 1),
		probeStopBudget: probeStopTimeout,
	}
	s.closeProbeErrs = sync.OnceFunc(func() { close(s.probeErrs) })
	if cfg.Server.Probes.Port > 0 {
		s.probeAddr = hostPort(cfg.Server.EffectiveProbeHost(), cfg.Server.Probes.Port)
	}
	for _, opt := range opts {
		opt(s)
	}

	// Compute full paths for probe endpoints before middleware setup
	healthPath := s.buildFullPath(healthRoute)
	readyPath := s.buildFullPath(readyRoute)

	// Setup middlewares with probe endpoint paths for tenant skipper. The OTel HTTP
	// middleware is registered only when observability is enabled (zero overhead when off).
	SetupMiddlewares(e, log, cfg, cfg.Bool("observability.enabled", false), healthPath, readyPath)

	if s.probeAddr != "" {
		s.probeEcho = newProbeEngine(e, cfg, log, healthRoute, readyRoute)
	} else {
		s.closeProbeErrs()
	}

	s.RegisterReadyHandler(nil)
	s.registerProbeRoutes(healthPath, readyPath)

	log.Debug().
		Str("base_path", basePath).
		Str("health_path", healthPath).
		Str("ready_path", readyPath).
		Msg("Server paths configured")

	return s
}

// httpErrorHandler emits standardized APIResponse envelopes; both engines use it.
// Echo v5's Recover middleware wraps panics in middleware.PanicStackError; it logs
// them with structured zerolog fields before normal error handling.
func httpErrorHandler(cfg *config.Config, log logger.Logger) echo.HTTPErrorHandler {
	return func(c *echo.Context, err error) {
		var panicErr *middleware.PanicStackError
		if goerrors.As(err, &panicErr) {
			// SECURITY: debug-gate the panic cause the same way as the unhandled-5xx
			// path — a panicking driver/downstream error can embed PII/PCI, and the
			// SensitiveDataFilter masks by field name, not message content.
			appendErrorDetail(
				log.Error().Bytes("stack", panicErr.Stack).Str("request_id", safeGetRequestID(c)),
				panicErr.Unwrap(), cfg.App.Debug,
			).Msg("Panic recovered")
		}
		customErrorHandler(c, err, cfg, log)
	}
}

// newProbeEngine builds the probe listener's engine (ADR-120). It shares the application
// engine's error handler, and its client-IP extractor so the probe access log resolves
// client.address by the same trusted-proxy walk; it gets its own minimal chain.
func newProbeEngine(app *echo.Echo, cfg *config.Config, log logger.Logger, healthRoute, readyRoute string) *echo.Echo {
	pe := echo.New()
	pe.HTTPErrorHandler = app.HTTPErrorHandler
	pe.IPExtractor = app.IPExtractor
	setupProbeMiddlewares(pe, log, cfg, healthRoute, readyRoute)
	return pe
}

// registerProbeRoutes wires /health and /ready. They register directly on the engine
// (not through a routeGroup), so they are recorded explicitly: a module claiming a probe
// path must fail startup like any other collision, and the route table must list them
// like any other route. The reservation at <base><path> holds whatever
// server.probes.port says, so flipping it never changes which module routes are legal;
// with the probe listener enabled the application engine answers 404 there, and the
// probe engine serves the probes at their unprefixed paths. Then the route table lists
// the probe listener's routes, not the reservation.
func (s *Server) registerProbeRoutes(healthPath, readyPath string) {
	probes := []struct {
		path, route, name     string
		handler, probeHandler echo.HandlerFunc
	}{
		{healthPath, s.healthRoute, "healthCheck", s.healthCheck, s.healthCheck},
		{readyPath, s.readyRoute, "dispatchReady", s.dispatchReady, s.dispatchProbeReady},
	}
	for _, p := range probes {
		reg := RouteRegistrant{HandlerName: p.name, Package: serverPackagePath}
		for _, method := range probeMethods {
			if s.probeEcho == nil {
				s.echo.Add(method, p.path, p.handler)
				registerRoute(s.conflicts, method, p.path, reg)
				continue
			}
			s.echo.Add(method, p.path, reservedProbeRoute)
			s.conflicts.record(method, p.path, reg)
			s.probeEcho.Add(method, p.route, p.probeHandler)
			registerProbeListenerRoute(method, p.route, reg)
		}
	}
}

// reservedProbeRoute holds a probe's <base><path> on the application engine while the
// probe listener serves it. Echo matches a static route ahead of a param or wildcard
// route, so a module's /:id or /* under the base never serves the reserved path.
func reservedProbeRoute(*echo.Context) error {
	return echo.ErrNotFound
}

// ModuleGroup returns a route registrar with the base path applied for module route
// registration. If no base path is configured, it returns a registrar with empty prefix.
func (s *Server) ModuleGroup() RouteRegistrar {
	if s.basePath == "" || s.basePath == "/" {
		return newTrackedRouteGroup(s.echo.Group(""), "", s.cfg, s.conflicts)
	}
	return newTrackedRouteGroup(s.echo.Group(s.basePath), s.basePath, s.cfg, s.conflicts)
}

// RootGroup returns a route registrar rooted at the engine with NO base path applied. It
// is the registration surface for framework-internal endpoints that must sit at the URL
// root regardless of server.path.base — e.g. the debug/system endpoints. It replaces the
// former Echo() accessor for that internal need without exposing the engine.
func (s *Server) RootGroup() RouteRegistrar {
	return newTrackedRouteGroup(s.echo.Group(""), "", s.cfg, s.conflicts)
}

// RouteConflicts returns every duplicate method+path registration observed on
// this server's registrars, in registration order. Empty when there are none.
func (s *Server) RouteConflicts() []RouteConflict {
	return s.conflicts.snapshot()
}

// RegisterReadyHandler overrides the readiness endpoint handler with a go-bricks Handler.
// Passing nil restores the default handler. The handler is adapted to the engine once here.
func (s *Server) RegisterReadyHandler(handler Handler) {
	s.readyMu.Lock()
	defer s.readyMu.Unlock()
	if handler == nil {
		s.readyHandler = s.readyCheck
	} else {
		s.readyHandler = adaptHandler(handler, s.cfg)
	}
}

// dispatchReady executes the currently registered ready handler. Once Shutdown sets the
// stopping latch it answers 503 instead and never calls the handler.
func (s *Server) dispatchReady(c *echo.Context) error {
	if s.stopping.Load() {
		return notReady(c)
	}
	s.readyMu.RLock()
	handler := s.readyHandler
	s.readyMu.RUnlock()
	return handler(c)
}

// dispatchProbeReady serves /ready on the probe listener, gating in order: the stopping
// latch; ReadyCh, since that listener binds before the application listener serves; then
// the application-listener check. Only then does it defer to dispatchReady. The latch
// comes first so a probe during the drain answers 503 without judging a listener that is
// closing on purpose, and a failed check re-reads it: a Shutdown that latched after the
// first read closed that listener on purpose, so no WARN. Nor does a failed check whose
// probe request's own context is done, as when the prober's timeout expires mid-check:
// that failure judges the abandoned probe, not the listener.
func (s *Server) dispatchProbeReady(c *echo.Context) error {
	if s.stopping.Load() {
		return notReady(c)
	}
	select {
	case <-s.ready:
	default:
		return notReady(c)
	}
	ctx := c.Request().Context()
	if err := s.checkApplicationListener(ctx); err != nil {
		if !s.stopping.Load() && !goerrors.Is(ctx.Err(), context.Canceled) {
			s.logger.Warn().Err(err).Msg("Application listener unresponsive")
		}
		return notReady(c)
	}
	return s.dispatchReady(c)
}

// notReady writes the /ready gates' 503 verdict.
func notReady(c *echo.Context) error {
	return c.JSON(http.StatusServiceUnavailable, map[string]string{
		fieldStatus: statusNotReady,
	})
}

// ErrServerAlreadyStarted is returned by every Start on a Server after the first,
// including after Shutdown.
var ErrServerAlreadyStarted = goerrors.New("server: Start called more than once")

// Start starts the HTTP server and begins accepting requests.
// It blocks until the server is shut down or encounters an error.
// A Server is single-use: any later Start, including after Shutdown, returns
// ErrServerAlreadyStarted without binding, and Shutdown resets neither
// BoundAddr nor ReadyCh. Shutdown called before the first Start makes that
// Start return http.ErrServerClosed without binding or serving.
//
// With the probe listener enabled it binds and serves first; a probe bind failure
// returns before the application listener binds, and any later failure closes the
// probe listener before Start returns. A TLS leaf the probe listener's
// application-listener check cannot pin (no SAN) or verify against that pin (no serverAuth
// use, outside its validity period) refuses Start before either bind.
func (s *Server) Start() error {
	if !s.started.CompareAndSwap(false, true) {
		return ErrServerAlreadyStarted
	}
	if s.stopping.Load() {
		s.closeProbeErrs()
		return http.ErrServerClosed
	}
	addr := hostPort(s.cfg.Server.Host, s.cfg.Server.Port)

	tlsCfg, err := s.serverTLSConfig()
	if err != nil {
		s.closeProbeErrs()
		return err
	}

	closeProbes, err := s.startProbeListener(tlsCfg)
	if err != nil {
		return err
	}

	s.logger.Info().
		Str("service", s.cfg.App.Name).
		Str("version", s.cfg.App.Version).
		Str("env", s.cfg.App.Env).
		Str("port", strconv.Itoa(s.cfg.Server.Port)).
		Str("address", addr).
		Bool("tls", s.cfg.Server.TLS.Enabled).
		Msg("Starting server...")

	sc := echo.StartConfig{
		Address:          addr,
		HideBanner:       true,
		HidePort:         true,
		TLSConfig:        tlsCfg,
		ListenerAddrFunc: s.onListenerBound,
		BeforeServeFunc:  s.onBeforeServe,
	}

	// Echo returns nil only after a graceful Shutdown, which owns the probe listener's
	// stop from then on: it runs last, after the application drain.
	if err := sc.Start(context.Background(), s.echo); err != nil {
		closeProbes()
		return err
	}
	return nil
}

// serverTLSConfig builds the application listener's TLS config, or nil when TLS is off.
func (s *Server) serverTLSConfig() (*tls.Config, error) {
	if s.cfg.Server.TLS.Enabled {
		return buildServerTLSConfig(&s.cfg.Server.TLS)
	}
	if hasStagedServerTLSMaterial(&s.cfg.Server.TLS) {
		// Fail-open is deliberate — staging material ahead of a flip is a
		// legitimate rollout step — but a mistyped SERVER_TLS_ENABLED that
		// leaves full material configured and serves plaintext must never be
		// silent.
		s.logger.Warn().
			Str("field", "server.tls.enabled").
			Msg("server.tls material is configured but server.tls.enabled is false; serving plaintext")
	}
	return nil, nil
}

// buildAppListenerCheck builds the probe listener's application-listener check from the
// application listener's TLS config. A TLS leaf it cannot pin refuses Start before either
// bind.
func (s *Server) buildAppListenerCheck(tlsCfg *tls.Config) error {
	check, err := newAppListenerCheck(s.cfg.Server.Host, s.buildFullPath(s.readyRoute), tlsCfg)
	if err != nil {
		return err
	}
	s.appCheck = check
	return nil
}

// startProbeListener builds the application-listener check from tlsCfg, the application
// listener's TLS config, then binds and serves the probe listener ahead of the application
// bind (ADR-120) and returns what closes it at once; a no-op when the listener is
// disabled. Every refusal closes ProbeErrors, since no Serve goroutine will. The check is
// stored before the Serve goroutine starts, which publishes it to probe handlers. The
// store shares a lifecycleMu critical section with the stopping read, as onBeforeServe's
// commit does, so a Shutdown that already latched vetoes it and one that latches later
// finds the listener to stop.
func (s *Server) startProbeListener(tlsCfg *tls.Config) (closeProbes func(), err error) {
	if s.probeEcho == nil {
		return func() {
			// No probe listener was bound, so there is nothing to close.
		}, nil
	}
	defer func() {
		if err != nil {
			s.closeProbeErrs()
		}
	}()
	if collision := s.cfg.Server.CheckProbeCollision(); collision != nil {
		return nil, collision
	}
	if checkErr := s.buildAppListenerCheck(tlsCfg); checkErr != nil {
		return nil, checkErr
	}
	lc := net.ListenConfig{}
	ln, err := lc.Listen(context.Background(), "tcp", s.probeAddr)
	if err != nil {
		return nil, err
	}
	p := &probeListener{
		srv: &http.Server{
			Handler:           s.probeEcho,
			ErrorLog:          slog.NewLogLogger(s.probeEcho.Logger.Handler(), slog.LevelError),
			ReadHeaderTimeout: s.cfg.Server.Timeout.Read, // in the literal for gosec G112
		},
		ln: ln,
	}
	applyServerTimeouts(p.srv, s.cfg.Server.Timeout)

	s.lifecycleMu.Lock()
	vetoed := s.stopping.Load()
	if !vetoed {
		s.probe.Store(p)
	}
	s.lifecycleMu.Unlock()
	if vetoed {
		_ = ln.Close()
		return nil, http.ErrServerClosed
	}

	boundAddr := ln.Addr()
	s.probeBoundAddr.Store(&boundAddr)
	s.logger.Info().
		Str("address", boundAddr.String()).
		Msg("Starting probe listener...")
	go s.serveProbes(p.srv, ln)
	return p.close, nil
}

// serveProbes runs the probe listener until it stops, sends any serve error other than
// http.ErrServerClosed on ProbeErrors, and then closes it.
func (s *Server) serveProbes(srv *http.Server, ln net.Listener) {
	defer s.closeProbeErrs()
	if err := srv.Serve(ln); err != nil && !goerrors.Is(err, http.ErrServerClosed) {
		s.probeErrs <- err
	}
}

func (s *Server) onListenerBound(addr net.Addr) {
	s.boundAddr.Store(&addr)
}

// onBeforeServe applies the timeouts StartConfig lacks and stores srv before
// closing ready. If Shutdown already set the stopping latch, it returns
// http.ErrServerClosed without closing ready so echo closes the listener and
// Start returns that sentinel. Shutdown never clears httpServer, so ready
// closes once, and only when the server is actually serving.
//
// Storing srv, reading the latch and closing ready are one critical section
// under lifecycleMu, so a Shutdown cannot land between the latch read and the
// close and shut down a server this callback is about to report ready. The
// ordering the lock still allows — Shutdown running after the commit but before
// echo calls Serve — is indistinguishable from a Shutdown issued the instant
// after readiness, and is an ordinary graceful stop rather than a false
// readiness.
func (s *Server) onBeforeServe(srv *http.Server) error {
	applyServerTimeouts(srv, s.cfg.Server.Timeout)

	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	first := s.httpServer.Swap(srv) == nil
	if s.stopping.Load() {
		return http.ErrServerClosed
	}
	if s.testHookReadyCommit != nil {
		s.testHookReadyCommit()
	}
	if first {
		close(s.ready)
	}
	return nil
}

// applyServerTimeouts applies server.timeout.* to either listener's *http.Server.
// ReadHeaderTimeout follows Read.
func applyServerTimeouts(srv *http.Server, t config.TimeoutConfig) {
	srv.ReadTimeout = t.Read
	srv.WriteTimeout = t.Write
	srv.IdleTimeout = t.Idle
	srv.ReadHeaderTimeout = t.Read
}

// Shutdown gracefully shuts down the HTTP server with the given context.
// It waits for existing connections to finish within the context timeout.
// A Shutdown issued before Start makes the later Start return
// http.ErrServerClosed without binding. A Shutdown issued after the listener
// binds but before the serve callback stores the *http.Server is observed by
// that callback, which then refuses to serve. Setting the latch and reading the
// stored server share onBeforeServe's critical section, so Shutdown never
// returns while a readiness commit is in flight and ReadyCh never closes after
// Shutdown returned.
//
// The probe listener stops last, after the application drain, so /ready answers 503
// rather than refusing connections throughout it (ADR-120). It gets its own budget,
// detached from ctx; a probe listener that overruns it is closed with a WARN, and that
// alone is not an error.
func (s *Server) Shutdown(ctx context.Context) error {
	s.lifecycleMu.Lock()
	s.stopping.Store(true)
	srv := s.httpServer.Load()
	probe := s.probe.Load()
	s.lifecycleMu.Unlock()

	// In v5, Echo no longer has a Shutdown method. Shut down the http.Server directly.
	// The drain runs outside lifecycleMu: it blocks until connections finish, and
	// holding the lock there would stall a concurrent serve callback for its duration.
	var err error
	if srv != nil {
		if drainErr := srv.Shutdown(ctx); drainErr != nil && !goerrors.Is(drainErr, http.ErrServerClosed) {
			err = drainErr
		}
	}
	if probeErr := s.stopProbeListener(ctx, probe); probeErr != nil {
		return goerrors.Join(err, probeErr)
	}
	return err
}

// stopProbeListener stops the probe listener within probeStopBudget, detached from the
// caller's ctx, and closes it on expiry. The deadline is logged, not returned.
func (s *Server) stopProbeListener(ctx context.Context, p *probeListener) error {
	if p == nil {
		return nil
	}
	stopCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), s.probeStopBudget)
	defer cancel()
	err := p.srv.Shutdown(stopCtx)
	// Shutdown closes only a listener Serve has tracked; one stored before then is still bound.
	_ = p.ln.Close()
	if !goerrors.Is(err, context.DeadlineExceeded) {
		return err
	}
	s.logger.Warn().
		Dur("budget", s.probeStopBudget).
		Msg("Probe listener did not drain within its stop budget; closing it")
	_ = p.srv.Close()
	return nil
}

// BoundAddr returns the address Start's listener bound (the port the OS picked
// when Server.Port is 0, TLS included), or nil before it first binds. It never
// blocks.
func (s *Server) BoundAddr() net.Addr {
	if addr := s.boundAddr.Load(); addr != nil {
		return *addr
	}
	return nil
}

// ProbeBoundAddr returns the address the probe listener bound, or nil before it binds
// or when it is disabled. It never blocks.
func (s *Server) ProbeBoundAddr() net.Addr {
	if addr := s.probeBoundAddr.Load(); addr != nil {
		return *addr
	}
	return nil
}

// ProbeErrors reports the probe listener's serve error: at most one, never
// http.ErrServerClosed. It is non-nil from New and closed exactly once — at New when the
// listener is disabled, when Start returns without binding it, or when its Serve goroutine
// exits.
func (s *Server) ProbeErrors() <-chan error {
	return s.probeErrs
}

// ReadyCh returns a channel closed once Start first serves. It closes after the
// *http.Server is stored and the stopping latch is clear, never when the
// listener binds and never when Shutdown vetoes the start. If Start fails or is
// vetoed before serving it never closes, so select on Start's error as well.
func (s *Server) ReadyCh() <-chan struct{} {
	return s.ready
}

// healthCheck is the default health probe handler.
func (s *Server) healthCheck(c *echo.Context) error {
	return c.JSON(http.StatusOK, map[string]string{
		fieldStatus: statusOK,
	})
}

// readyCheck is the default readiness probe handler.
func (s *Server) readyCheck(c *echo.Context) error {
	// App overrides this via RegisterReadyHandler with a probe-driven readiness check
	// (DB, messaging, etc.); see app/lifecycle.go's App.readyCheck. This handler remains
	// the fallback when no override is registered.
	return c.JSON(http.StatusOK, map[string]any{
		fieldStatus: statusReady,
		"time":      time.Now().Unix(),
	})
}

// customErrorHandler is a centralized error handler that formats errors
// into standardized APIResponse envelopes based on error type and server configuration.
// When the request context carries the raw response flag (set by handlerWrapper.wrap),
// it uses formatRawErrorResponse which writes minimal JSON without the envelope.
func customErrorHandler(c *echo.Context, err error, cfg *config.Config, log logger.Logger) {
	// SAFETY: Prevent double-writes if error handler is invoked multiple times.
	// This can happen with certain middleware combinations (e.g., otelecho).
	// Matches Echo's default error handler behavior.
	if isResponseCommitted(c) {
		return
	}

	// Select formatter based on raw response mode (set early in handlerWrapper.wrap)
	formatter := formatErrorResponse
	if raw, ok := c.Get(rawResponseContextKey).(bool); ok && raw {
		formatter = formatRawErrorResponse
	}

	apiErr := classifyError(err, c, cfg, log)
	_ = formatter(c, apiErr, cfg)
}

// classifyError converts an arbitrary error into a structured IAPIError.
// It handles context.DeadlineExceeded, IAPIError, echo.HTTPError, and
// untyped errors, applying production sanitization and server-error logging.
// The 500 body's message text. Production hides the internal wording; debug
// keeps it. They are constants, and internalErrorMessage is the single reader,
// because the panic guard outside Echo's Recover renders its own 500 without
// passing through classifyError — and a caller must not be able to tell which
// recovery layer caught the panic from the message it gets back.
const (
	msgInternalErrorDebug = "Internal server error"
	msgInternalErrorProd  = "An error occurred while processing your request"
)

// internalErrorMessage returns the 500 message this deployment's posture allows.
func internalErrorMessage(cfg *config.Config) string {
	if cfg.App.Debug {
		return msgInternalErrorDebug
	}
	return msgInternalErrorProd
}

func classifyError(err error, c *echo.Context, cfg *config.Config, log logger.Logger) IAPIError {
	// Context deadline exceeded (timeout errors)
	if goerrors.Is(err, context.DeadlineExceeded) {
		return NewServiceUnavailableError("Request processing timed out")
	}

	// Already a structured API error — use as-is
	var apiErr IAPIError
	if goerrors.As(err, &apiErr) {
		return apiErr
	}

	// Map echo.HTTPError, echo.HTTPStatusCoder, and untyped errors.
	// In v5, sentinel errors like ErrNotFound are httpError (lowercase) which
	// implements HTTPStatusCoder but NOT *HTTPError, so we check both interfaces.
	status := http.StatusInternalServerError
	msg := msgInternalErrorDebug
	var he *echo.HTTPError
	if goerrors.As(err, &he) {
		status = he.Code
		// In v5, HTTPError.Message is always a string
		if he.Message != "" {
			msg = he.Message
		}
	} else if sc := echo.StatusCode(err); sc != 0 {
		// Handles httpError sentinels (ErrNotFound, ErrMethodNotAllowed, etc.)
		status = sc
		msg = http.StatusText(sc)
	}

	// In non-debug (production) hide internal details for 500s
	if !cfg.App.Debug && status == http.StatusInternalServerError {
		msg = msgInternalErrorProd
	}

	if status >= http.StatusInternalServerError {
		// SECURITY: use the injected framework logger (not Echo's stock logger) so log
		// output is subject to the same lifecycle as the rest of the app. The
		// SensitiveDataFilter only masks by field name, not by message content, so it
		// cannot be trusted to scrub PII/PCI a driver error may embed (e.g. a
		// unique-constraint value). Mirror the response-body redaction above instead:
		// non-debug builds log the error type only, never the raw message; debug builds
		// keep full detail for troubleshooting. The debug branch writes through Err, so
		// FilterConfig.ErrorRedactor — the seam that CAN see message content — reaches
		// it (#1182), and Err applies the field-name filter as well, so marking `error`
		// sensitive still masks this line. The non-debug error_type goes through Str.
		appendErrorDetail(log.Error().Str("request_id", safeGetRequestID(c)), err, cfg.App.Debug).
			Msg("unhandled error")
	}

	code := statusToErrorCode(status)
	base := NewBaseAPIError(code, msg, status)
	// SECURITY: the response body is the LESS trusted of the two error sinks, so it
	// shares the log sinks' app.debug gate and adds the development requirement on top
	// — the stricter of the two keys wins (#1140). Gating on the environment alone let
	// an operator who turned app.debug off in a dev environment silence the log while
	// the body kept shipping raw error detail to the caller. The IsDevelopment half
	// restates what devDetails' own render gate already requires, deliberately: this
	// site must be safe on its own, so neither gate is the sole one.
	if cfg.App.Debug && cfg.App.IsDevelopment() {
		_ = base.WithDetails("error", err.Error())
	}

	return base
}

// appendErrorDetail adds debug-gated error detail to a log event: the raw error
// message only in Debug builds, the error type otherwise. SECURITY: the
// SensitiveDataFilter masks by field name, not message content, so a raw error
// string (which can embed driver-supplied PII/PCI) must never be logged in
// production. Shared by the panic-recovery and unhandled-5xx log paths.
//
// The debug branch goes through Err rather than Str because FilterConfig.ErrorRedactor
// — the one seam that sees error CONTENT — runs at Err and nowhere else (#1168). Writing
// the message with Str put these two sites outside the reach of a redactor an operator
// wired up to scrub driver-supplied PII, which is the one thing field-name masking
// cannot do (#1182). The field name is unchanged: zerolog's Err writes under
// zerolog.ErrorFieldName, which is "error", and so does the redacted path.
//
// Field-name masking is not lost in the move: Err applies the `error` needle the
// same way Str did, so an operator running log.sensitivefields: [error] keeps that
// masking here — and gains it at every other Err site, which never had it. The
// review of this change is what surfaced that gap; ErrorRedactor and the needle are
// now both live at this door, the mask winning when both are configured.
func appendErrorDetail(event logger.LogEvent, err error, debug bool) logger.LogEvent {
	if err == nil {
		return event
	}
	if debug {
		return event.Err(err)
	}
	return event.Str("error_type", fmt.Sprintf("%T", err))
}

// statusToErrorCode maps HTTP status codes to standardized error codes.
func statusToErrorCode(status int) string {
	switch status {
	case http.StatusBadRequest:
		return errCodeBadRequest
	case http.StatusUnauthorized:
		return errCodeUnauthorized
	case http.StatusForbidden:
		return errCodeForbidden
	case http.StatusNotFound:
		return errCodeNotFound
	case http.StatusMethodNotAllowed:
		return errCodeMethodNotAllowed
	case http.StatusConflict:
		return errCodeConflict
	case http.StatusTooManyRequests:
		return errCodeTooManyRequests
	case http.StatusServiceUnavailable:
		return errCodeServiceUnavailable
	default:
		return errCodeInternalError
	}
}
