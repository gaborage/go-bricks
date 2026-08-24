// Package server provides HTTP server functionality using Echo framework.
// It includes middleware setup, routing, and request handling.
package server

import (
	"context"
	"crypto/tls"
	goerrors "errors"
	"fmt"
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
func New(cfg *config.Config, log logger.Logger) *Server {
	SetCaptureStackTraces(cfg.App.IsDevelopment())

	e := echo.New()
	// Use an error handler that emits standardized APIResponse envelopes.
	// Echo v5's Recover middleware wraps panics in middleware.PanicStackError;
	// we log them here with structured zerolog fields before normal error handling.
	e.HTTPErrorHandler = func(c *echo.Context, err error) {
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
		echo:         e,
		cfg:          cfg,
		logger:       log,
		basePath:     basePath,
		healthRoute:  healthRoute,
		readyRoute:   readyRoute,
		readyHandler: nil,
		conflicts:    newRouteConflictTracker(),
	}

	// Compute full paths for probe endpoints before middleware setup
	healthPath := s.buildFullPath(healthRoute)
	readyPath := s.buildFullPath(readyRoute)

	// Setup middlewares with probe endpoint paths for tenant skipper. The OTel HTTP
	// middleware is registered only when observability is enabled (zero overhead when off).
	SetupMiddlewares(e, log, cfg, cfg.Bool("observability.enabled", false), healthPath, readyPath)

	s.RegisterReadyHandler(nil)

	e.GET(healthPath, s.healthCheck)
	e.HEAD(healthPath, s.healthCheck)
	e.GET(readyPath, s.dispatchReady)
	e.HEAD(readyPath, s.dispatchReady)

	// The probes register directly on the engine (not through a routeGroup), so record
	// them explicitly: a module claiming the health/ready path must fail startup like
	// any other collision.
	probe := RouteRegistrant{HandlerName: "healthCheck", Package: serverPackagePath}
	s.conflicts.record(http.MethodGet, healthPath, probe)
	s.conflicts.record(http.MethodHead, healthPath, probe)
	probe.HandlerName = "dispatchReady"
	s.conflicts.record(http.MethodGet, readyPath, probe)
	s.conflicts.record(http.MethodHead, readyPath, probe)

	log.Debug().
		Str("base_path", basePath).
		Str("health_path", healthPath).
		Str("ready_path", readyPath).
		Msg("Server paths configured")

	return s
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

// dispatchReady executes the currently registered ready handler.
func (s *Server) dispatchReady(c *echo.Context) error {
	s.readyMu.RLock()
	handler := s.readyHandler
	s.readyMu.RUnlock()
	return handler(c)
}

// Start starts the HTTP server and begins accepting requests.
// It blocks until the server is shut down or encounters an error.
func (s *Server) Start() error {
	addr := fmt.Sprintf("%s:%d", s.cfg.Server.Host, s.cfg.Server.Port)

	var tlsCfg *tls.Config
	if s.cfg.Server.TLS.Enabled {
		var err error
		tlsCfg, err = buildServerTLSConfig(&s.cfg.Server.TLS)
		if err != nil {
			return err
		}
	} else if hasStagedServerTLSMaterial(&s.cfg.Server.TLS) {
		// Fail-open is deliberate — staging material ahead of a flip is a
		// legitimate rollout step — but a mistyped SERVER_TLS_ENABLED that
		// leaves full material configured and serves plaintext must never be
		// silent.
		s.logger.Warn().
			Str("field", "server.tls.enabled").
			Msg("server.tls material is configured but server.tls.enabled is false; serving plaintext")
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
		Address:    addr,
		HideBanner: true,
		HidePort:   true,
		TLSConfig:  tlsCfg,
		ListenerAddrFunc: func(addr net.Addr) {
			a := addr
			s.boundAddr.Store(&a)
		},
		BeforeServeFunc: func(srv *http.Server) error {
			// Configure timeouts on the http.Server (StartConfig doesn't expose these)
			srv.ReadTimeout = s.cfg.Server.Timeout.Read
			srv.WriteTimeout = s.cfg.Server.Timeout.Write
			srv.IdleTimeout = s.cfg.Server.Timeout.Idle
			srv.ReadHeaderTimeout = s.cfg.Server.Timeout.Read
			// Capture the server instance for proper shutdown
			s.httpServer.Store(srv)
			return nil
		},
	}

	return sc.Start(context.Background(), s.echo)
}

// Shutdown gracefully shuts down the HTTP server with the given context.
// It waits for existing connections to finish within the context timeout.
func (s *Server) Shutdown(ctx context.Context) error {
	// In v5, Echo no longer has a Shutdown method. Shut down the http.Server directly.
	if srv := s.httpServer.Load(); srv != nil {
		if err := srv.Shutdown(ctx); err != nil && !goerrors.Is(err, http.ErrServerClosed) {
			return err
		}
	}
	return nil
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
	msg := "Internal server error"
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
		msg = "An error occurred while processing your request"
	}

	if status >= http.StatusInternalServerError {
		// SECURITY: use the injected framework logger (not Echo's stock logger) so log
		// output is subject to the same lifecycle as the rest of the app. The
		// SensitiveDataFilter only masks by field name, not by message content, so it
		// cannot be trusted to scrub PII/PCI a driver error may embed (e.g. a
		// unique-constraint value). Mirror the response-body redaction above instead:
		// non-debug builds log the error type only, never the raw message; debug builds
		// keep full detail for troubleshooting. Str() still routes through the injected
		// filter as defense-in-depth for any field an operator has marked sensitive.
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
func appendErrorDetail(event logger.LogEvent, err error, debug bool) logger.LogEvent {
	if err == nil {
		return event
	}
	if debug {
		return event.Str("error", err.Error())
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
