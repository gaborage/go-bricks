package server

import (
	"regexp"
	"strconv"
	"strings"
	"time"

	echootel "github.com/labstack/echo-opentelemetry"
	"github.com/labstack/echo/v5"
	"github.com/labstack/echo/v5/middleware"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"

	"github.com/gaborage/go-bricks/config"
	"github.com/gaborage/go-bricks/logger"
	"github.com/gaborage/go-bricks/multitenant"
)

// SetupMiddlewares configures and registers all HTTP middlewares for the Echo server.
// It sets up CORS, logging, recovery, security headers, rate limiting, and other essential middleware.
// observabilityEnabled gates the OTel HTTP instrumentation middleware (passed explicitly,
// like healthPath/readyPath, so the decision is made by the caller from observability.enabled).
// healthPath and readyPath are used by the tenant middleware skipper to bypass probe endpoints.
func SetupMiddlewares(e *echo.Echo, log logger.Logger, cfg *config.Config, observabilityEnabled bool, healthPath, readyPath string) {
	// Request ID — validates the inbound X-Request-ID and sets the response
	// header to either the validated value or a fresh UUID. Replaces Echo's
	// stock middleware.RequestID() which echoes the inbound header verbatim
	// without validation, exposing the framework to log poisoning and
	// CRLF-injection via attacker-controlled request IDs.
	e.Use(requestIDMiddlewareEcho())

	// OpenTelemetry instrumentation - creates spans AND metrics for HTTP requests
	// Skip health/ready probes to avoid noisy traces/metrics
	// IMPORTANT: We explicitly pass TracerProvider(otel.GetTracerProvider()) to capture
	// the global provider at middleware setup time. The middleware caches the tracer
	// provider when called, NOT at request time.
	//
	// MetricAttributes seeds from the library's default semconv attribute set and
	// then appends custom attributes (proxy-aware url.scheme, error.type). This
	// extends — rather than replaces — the standard HTTP server metric attributes.
	// Honor "zero overhead when disabled": when observability is off the global
	// tracer/meter providers are non-nil no-ops, so registering this middleware
	// would still build and discard span/metric attributes on every request.
	// Gate it on observabilityEnabled (RequestID/RequestEnrich below stay
	// unconditional so W3C trace propagation works regardless).
	if observabilityEnabled {
		probeSkipper := CreateProbeSkipper(healthPath, readyPath)
		e.Use(echootel.NewMiddlewareWithConfig(echootel.Config{
			ServerName:     cfg.App.Name,
			TracerProvider: otel.GetTracerProvider(),
			Skipper: func(c *echo.Context) bool {
				return probeSkipper(c.Request())
			},
			MetricAttributes: func(c *echo.Context, v *echootel.Values) []attribute.KeyValue {
				// echo-opentelemetry treats a non-empty MetricAttributes return as a
				// REPLACEMENT for its default attribute set: Metrics.Record falls back to
				// v.MetricAttributes() only when the returned slice is empty. Seeding from
				// the defaults preserves the standard semconv metric attributes
				// (http.request.method, http.response.status_code, http.route,
				// server.address, server.port, url.scheme, network.protocol.*) that would
				// otherwise be dropped, while still letting us append custom attributes.
				attrs := v.MetricAttributes()

				// Override url.scheme with a proxy-aware value. The library derives
				// url.scheme from r.TLS alone; we additionally honor X-Forwarded-Proto.
				// Appended after the defaults so our value wins attribute.Set's
				// last-value-wins de-duplication (a duplicate url.scheme key is harmless).
				scheme := "http"
				if c.Request().TLS != nil || c.Request().Header.Get("X-Forwarded-Proto") == "https" {
					scheme = "https"
				}
				attrs = append(attrs, attribute.String("url.scheme", scheme))

				// Add error.type for 4xx/5xx responses (status code as string per HTTP semconv).
				if v.HTTPResponseStatusCode >= 400 {
					attrs = append(attrs, attribute.String("error.type", strconv.Itoa(v.HTTPResponseStatusCode)))
				}

				return attrs
			},
		}))
	}

	// Enrich the request context in a single Request.WithContext clone: trace ID +
	// W3C headers for outbound propagation, plus the per-request AMQP/DB operation
	// counters. Combines the standalone TraceContext + PerformanceStats middlewares
	// (which remain exported for callers that register them individually).
	e.Use(requestEnrichEcho())

	// CORS — pass cfg.App.Env so the policy honors the Koanf default of
	// EnvDevelopment (instead of falling back to os.Getenv which is empty
	// when the operator relies on config.yaml / framework defaults).
	e.Use(corsEchoWithLogger(cfg.Server.ResponseTime.Enabled, log, cfg.App.Env))

	// IP pre-guard rate limiting (runs before tenant resolution for attack prevention)
	if cfg.App.Rate.IPPreGuard.Enabled {
		e.Use(ipPreGuardEcho(cfg.App.Rate.IPPreGuard.Threshold))
	}

	// Multi-tenant tenant resolver middleware (if enabled)
	if cfg.Multitenant.Enabled {
		resolver := buildTenantResolver(cfg)
		if resolver != nil {
			// Use skipper-aware middleware to bypass tenant resolution for health probes
			skipper := CreateProbeSkipper(healthPath, readyPath)
			e.Use(tenantMiddlewareEcho(resolver, skipper))
		} else {
			log.Warn().Msg("Tenant resolver could not be constructed; skipping tenant middleware")
		}
	}

	// Logger middleware with zerolog
	e.Use(loggerWithConfigEcho(log, LoggerConfig{
		HealthPath:           healthPath,
		ReadyPath:            readyPath,
		SlowRequestThreshold: 1 * time.Second,
	}))

	// Recovery
	e.Use(middleware.Recover())

	// Security headers
	e.Use(middleware.SecureWithConfig(middleware.SecureConfig{
		XSSProtection:         "1; mode=block",
		ContentTypeNosniff:    "nosniff",
		XFrameOptions:         "SAMEORIGIN",
		HSTSMaxAge:            3600,
		ContentSecurityPolicy: "default-src 'self'",
	}))

	// Timeout - add a request-scoped deadline without swapping the response writer.
	// This prevents goroutine panics when the context is canceled mid-flight.
	e.Use(timeoutEcho(cfg.Server.Timeout.Middleware))

	// Body limit
	e.Use(middleware.BodyLimit(10 * 1024 * 1024)) // 10 MB

	// Gzip — skip compressing tiny responses (the gzip header/overhead can exceed
	// the savings for small JSON); threshold is configurable via server.gzip.minlength.
	e.Use(middleware.GzipWithConfig(middleware.GzipConfig{
		Level:     5,
		MinLength: cfg.Server.Gzip.MinLength,
	}))

	// Rate limit
	e.Use(rateLimitEcho(cfg.App.Rate.Limit))

	// Timing — opt-in. The X-Response-Time header costs a per-response header
	// allocation (net/textproto.MIMEHeader.Set allocates a []string per Set) and
	// OTel provides richer latency telemetry, so it defaults off. Enable via
	// server.responsetime.enabled for local debugging or consumer compatibility.
	if cfg.Server.ResponseTime.Enabled {
		e.Use(timingEcho())
	}
}

var defaultTenantIDRegex = regexp.MustCompile(`^[a-z0-9-]{1,64}$`)

func newHeaderResolver(cfg *config.ResolverConfig) multitenant.TenantResolver {
	name := cfg.Header
	if name == "" {
		name = HeaderXTenantID
	}
	return &multitenant.HeaderResolver{HeaderName: name}
}

func newSubdomainResolver(cfg *config.ResolverConfig) multitenant.TenantResolver {
	// Normalize Domain: strip leading dot to accept both ".example.com" and "example.com"
	rootDomain := strings.TrimPrefix(cfg.Domain, ".")
	if rootDomain == "" {
		return nil
	}
	return &multitenant.SubdomainResolver{RootDomain: rootDomain, TrustProxies: cfg.Proxies}
}

func newPathResolver(cfg *config.ResolverConfig) multitenant.TenantResolver {
	if cfg.Path.Segment <= 0 {
		return nil
	}
	return &multitenant.PathResolver{Segment: cfg.Path.Segment, Prefix: cfg.Path.Prefix}
}

// newSubResolver builds the sub-resolver named by a composite order entry, or
// nil when the name is unknown or that sub-resolver isn't configured.
func newSubResolver(cfg *config.ResolverConfig, name string) multitenant.TenantResolver {
	switch name {
	case config.ResolverTypeSubdomain:
		return newSubdomainResolver(cfg)
	case config.ResolverTypePath:
		return newPathResolver(cfg)
	case config.ResolverTypeHeader:
		return newHeaderResolver(cfg)
	default:
		return nil
	}
}

// compositeSubResolvers builds the sub-resolvers named by cfg.Order, skipping
// names that are unknown or whose sub-resolver isn't configured. config.Validate
// now requires an explicit, non-empty Order for type: composite, so this
// fallback path only matters for a ResolverConfig that bypassed Validate
// entirely (e.g. built directly in tests, or by an embedding app). For such a
// config, an empty/unusable Order falls back to config.DefaultResolverOrder():
// it must not end up with zero sub-resolvers, because buildTenantResolver then
// returns nil and SetupMiddlewares skips tenant resolution entirely.
func compositeSubResolvers(cfg *config.ResolverConfig) []multitenant.TenantResolver {
	build := func(order []string) []multitenant.TenantResolver {
		subs := make([]multitenant.TenantResolver, 0, len(order))
		for _, name := range order {
			if sub := newSubResolver(cfg, name); sub != nil {
				subs = append(subs, sub)
			}
		}
		return subs
	}

	subs := build(cfg.Order)
	if len(subs) == 0 {
		subs = build(config.DefaultResolverOrder())
	}
	return subs
}

func buildTenantResolver(cfg *config.Config) multitenant.TenantResolver {
	resolverCfg := &cfg.Multitenant.Resolver
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

	switch resolverCfg.Type {
	case config.ResolverTypeHeader:
		return wrap(newHeaderResolver(resolverCfg))
	case config.ResolverTypeSubdomain:
		return wrap(newSubdomainResolver(resolverCfg))
	case config.ResolverTypePath:
		return wrap(newPathResolver(resolverCfg))
	case config.ResolverTypeComposite:
		return buildCompositeTenantResolver(tenantRegex, compositeSubResolvers(resolverCfg)...)
	default:
		return nil
	}
}

// buildCompositeTenantResolver collects the non-nil sub-resolvers (header,
// subdomain, path) into a single CompositeResolver wrapped with the tenant-ID
// validation regex. Returns nil when no sub-resolver is configured so the
// caller can fall through to the default no-resolution path.
func buildCompositeTenantResolver(tenantRegex *regexp.Regexp, subs ...multitenant.TenantResolver) multitenant.TenantResolver {
	resolvers := make([]multitenant.TenantResolver, 0, len(subs))
	for _, r := range subs {
		if r != nil {
			resolvers = append(resolvers, r)
		}
	}
	if len(resolvers) == 0 {
		return nil
	}
	return &multitenant.CompositeResolver{Resolvers: resolvers, TenantRegex: tenantRegex}
}
