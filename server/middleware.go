package server

import (
	"regexp"
	"strings"

	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
	"go.opentelemetry.io/contrib/instrumentation/github.com/labstack/echo/otelecho"
	"go.opentelemetry.io/otel"

	"github.com/gaborage/go-bricks/config"
	"github.com/gaborage/go-bricks/logger"
	"github.com/gaborage/go-bricks/multitenant"
)

// SetupMiddlewares configures and registers all HTTP middlewares for the Echo server.
// It sets up CORS, logging, recovery, security headers, rate limiting, and other essential middleware.
// healthPath and readyPath are used by the tenant middleware skipper to bypass probe endpoints.
func SetupMiddlewares(e *echo.Echo, log logger.Logger, cfg *config.Config, healthPath, readyPath string) {
	// Request ID
	e.Use(middleware.RequestID())

	// OpenTelemetry instrumentation - creates spans for HTTP requests
	// Skip health/ready probes to avoid noisy traces
	// IMPORTANT: We explicitly pass WithTracerProvider(otel.GetTracerProvider()) to capture
	// the global provider at middleware setup time. The otelecho.Middleware() function caches
	// the tracer provider when called, NOT at request time.
	//
	// In the standard bootstrap flow (app.NewWithConfig), observability is initialized BEFORE
	// the server is created, so the real provider is captured here. However, explicit wiring
	// makes this dependency clear and ensures correct behavior if SetupMiddlewares is called
	// directly (e.g., in tests or custom server initialization scenarios).
	probeSkipper := CreateProbeSkipper(healthPath, readyPath)
	e.Use(otelecho.Middleware(
		cfg.App.Name,
		otelecho.WithTracerProvider(otel.GetTracerProvider()),
		otelecho.WithSkipper(func(c echo.Context) bool {
			return probeSkipper(c)
		}),
	))

	// Inject trace context into request context for outbound propagation
	e.Use(TraceContext())

	// Operation Tracker - Initialize AMQP and DB operation tracking for each request
	e.Use(PerformanceStats())

	// CORS
	e.Use(CORS())

	// IP pre-guard rate limiting (runs before tenant resolution for attack prevention)
	if cfg.App.Rate.IPPreGuard.Enabled {
		e.Use(IPPreGuard(cfg.App.Rate.IPPreGuard.Threshold))
	}

	// Multi-tenant tenant resolver middleware (if enabled)
	if cfg.Multitenant.Enabled {
		resolver := buildTenantResolver(cfg)
		if resolver != nil {
			// Use skipper-aware middleware to bypass tenant resolution for health probes
			skipper := CreateProbeSkipper(healthPath, readyPath)
			e.Use(TenantMiddleware(resolver, skipper))
		} else {
			log.Warn().Msg("Tenant resolver could not be constructed; skipping tenant middleware")
		}
	}

	// Logger middleware with zerolog
	e.Use(Logger(log, healthPath, readyPath))

	// Recovery
	e.Use(middleware.RecoverWithConfig(middleware.RecoverConfig{
		LogErrorFunc: func(c echo.Context, err error, stack []byte) error {
			log.Error().
				Err(err).
				Str("request_id", c.Response().Header().Get(echo.HeaderXRequestID)).
				Bytes("stack", stack).
				Msg("Panic recovered")
			return err
		},
	}))

	// Security headers
	e.Use(middleware.SecureWithConfig(middleware.SecureConfig{
		XSSProtection:         "1; mode=block",
		ContentTypeNosniff:    "nosniff",
		XFrameOptions:         "SAMEORIGIN",
		HSTSMaxAge:            3600,
		ContentSecurityPolicy: "default-src 'self'",
	}))

	// Timeout - moved before BodyLimit to catch timeouts early in the chain
	// This prevents timeouts from racing with request body parsing
	e.Use(middleware.TimeoutWithConfig(middleware.TimeoutConfig{
		Timeout: cfg.Server.Timeout.Middleware,
	}))

	// Body limit
	e.Use(middleware.BodyLimit("10M"))

	// Gzip
	e.Use(middleware.GzipWithConfig(middleware.GzipConfig{
		Level: 5,
	}))

	// Rate limit
	e.Use(RateLimit(cfg.App.Rate.Limit))

	// Timing
	e.Use(Timing())
}

var defaultTenantIDRegex = regexp.MustCompile(`^[a-z0-9-]{1,64}$`)

func buildTenantResolver(cfg *config.Config) multitenant.TenantResolver {
	mtCfg := &cfg.Multitenant
	resolverCfg := mtCfg.Resolver
	tenantRegex := defaultTenantIDRegex

	wrap := func(res multitenant.TenantResolver) multitenant.TenantResolver {
		if res == nil {
			return nil
		}
		if tenantRegex == nil {
			return res
		}
		return &multitenant.ValidatingResolver{Resolver: res, TenantRegex: tenantRegex}
	}

	newHeaderResolver := func() multitenant.TenantResolver {
		name := resolverCfg.Header
		if name == "" {
			name = "X-Tenant-ID"
		}
		return &multitenant.HeaderResolver{HeaderName: name}
	}

	newSubdomainResolver := func() multitenant.TenantResolver {
		// Normalize Domain: strip leading dot to accept both ".example.com" and "example.com"
		rootDomain := strings.TrimPrefix(resolverCfg.Domain, ".")
		if rootDomain == "" {
			return nil
		}
		return &multitenant.SubdomainResolver{RootDomain: rootDomain, TrustProxies: resolverCfg.Proxies}
	}

	switch resolverCfg.Type {
	case "header":
		return wrap(newHeaderResolver())
	case "subdomain":
		return wrap(newSubdomainResolver())
	case "composite":
		resolvers := []multitenant.TenantResolver{}
		if header := newHeaderResolver(); header != nil {
			resolvers = append(resolvers, header)
		}
		if subdomain := newSubdomainResolver(); subdomain != nil {
			resolvers = append(resolvers, subdomain)
		}
		if len(resolvers) == 0 {
			return nil
		}
		return &multitenant.CompositeResolver{Resolvers: resolvers, TenantRegex: tenantRegex}
	default:
		return nil
	}
}
