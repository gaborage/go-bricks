package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v5"
	"github.com/labstack/echo/v5/middleware"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/gaborage/go-bricks/config"
	"github.com/gaborage/go-bricks/logger"
	"github.com/gaborage/go-bricks/multitenant"
)

const (
	preSetupMarker  = "pre-setup"
	postSetupMarker = "post-setup"

	// Test probe endpoint paths
	testHealthPath = "/health"
	testReadyPath  = "/ready"

	defaultTenantHeader = "X-Tenant-ID"
	testDomain          = "example.com"
)

func TestSetupMiddlewares(t *testing.T) {
	tests := []struct {
		name   string
		config *config.Config
	}{
		{
			name: "standard_middleware_setup",
			config: &config.Config{
				App: config.AppConfig{
					Rate: config.RateConfig{
						Limit: 100,
						IPPreGuard: config.IPPreGuardConfig{
							Enabled:   true,
							Threshold: 1000,
						},
					},
				},
				Server: config.ServerConfig{
					Timeout: config.TimeoutConfig{
						Middleware: 30 * time.Second,
					},
				},
			},
		},
		{
			name: "zero_rate_limit_disabled",
			config: &config.Config{
				App: config.AppConfig{
					Rate: config.RateConfig{
						Limit: 0,
						IPPreGuard: config.IPPreGuardConfig{
							Enabled:   false,
							Threshold: 0,
						},
					},
				},
				Server: config.ServerConfig{
					Timeout: config.TimeoutConfig{
						Middleware: 30 * time.Second,
					},
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := echo.New()
			log := logger.New("disabled", false)

			// Setup middlewares
			SetupMiddlewares(e, log, tt.config, true, testHealthPath, testReadyPath)

			// Create test handler
			e.GET("/test", func(c *echo.Context) error {
				return c.JSON(http.StatusOK, map[string]string{"status": "ok"})
			})

			// Test request
			req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/test", http.NoBody)
			rec := httptest.NewRecorder()

			e.ServeHTTP(rec, req)

			// Verify security headers are set
			assert.Equal(t, "1; mode=block", rec.Header().Get(HeaderXXSSProtection))
			assert.Equal(t, "nosniff", rec.Header().Get(HeaderXContentTypeOptions))
			assert.Equal(t, "SAMEORIGIN", rec.Header().Get(HeaderXFrameOptions))
			assert.Equal(t, "default-src 'self'", rec.Header().Get("Content-Security-Policy"))

			// HSTS header might not be set for HTTP in test environment
			hstsHeader := rec.Header().Get("Strict-Transport-Security")
			if hstsHeader != "" {
				assert.Contains(t, hstsHeader, "max-age=3600")
			}

			// Verify request ID is set
			assert.NotEmpty(t, rec.Header().Get(echo.HeaderXRequestID))

			// Timing header is opt-in (server.responsetime.enabled); the test
			// configs leave it at the zero-value default (disabled), so the
			// X-Response-Time header must be absent.
			assert.Empty(t, rec.Header().Get(HeaderXResponseTime))

			// Response should be successful
			assert.Equal(t, http.StatusOK, rec.Code)
		})
	}
}

// TestSetupMiddlewaresResponseTimeHeader verifies the X-Response-Time header is
// opt-in: absent under the default (disabled) config and present once
// server.responsetime.enabled is true.
func TestSetupMiddlewaresResponseTimeHeader(t *testing.T) {
	newCfg := func(enabled bool) *config.Config {
		return &config.Config{
			App: config.AppConfig{Rate: config.RateConfig{Limit: 100}},
			Server: config.ServerConfig{
				Timeout:      config.TimeoutConfig{Middleware: 30 * time.Second},
				ResponseTime: config.ResponseTimeConfig{Enabled: enabled},
			},
		}
	}

	t.Run("disabled_by_default_omits_header", func(t *testing.T) {
		e := echo.New()
		log := logger.New("disabled", false)
		SetupMiddlewares(e, log, newCfg(false), true, testHealthPath, testReadyPath)
		e.GET("/test", func(c *echo.Context) error {
			return c.JSON(http.StatusOK, map[string]string{"status": "ok"})
		})

		req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/test", http.NoBody)
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusOK, rec.Code)
		assert.Empty(t, rec.Header().Get(HeaderXResponseTime),
			"X-Response-Time must be absent when server.responsetime.enabled is false")
	})

	t.Run("enabled_sets_header", func(t *testing.T) {
		e := echo.New()
		log := logger.New("disabled", false)
		SetupMiddlewares(e, log, newCfg(true), true, testHealthPath, testReadyPath)
		e.GET("/test", func(c *echo.Context) error {
			return c.JSON(http.StatusOK, map[string]string{"status": "ok"})
		})

		req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/test", http.NoBody)
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusOK, rec.Code)
		assert.NotEmpty(t, rec.Header().Get(HeaderXResponseTime),
			"X-Response-Time must be present when server.responsetime.enabled is true")
	})
}

func TestBuildTenantResolver(t *testing.T) {
	t.Run("header resolver wraps validation", func(t *testing.T) {
		cfg := &config.Config{Multitenant: config.MultitenantConfig{Resolver: config.ResolverConfig{Type: "header"}}}

		resolver := buildTenantResolver(cfg)
		require.IsType(t, &multitenant.ValidatingResolver{}, resolver)
		vr := resolver.(*multitenant.ValidatingResolver)
		assert.Equal(t, multitenant.DefaultTenantIDPattern(), vr.TenantRegex)
		require.IsType(t, &multitenant.HeaderResolver{}, vr.Resolver)
		hr := vr.Resolver.(*multitenant.HeaderResolver)
		assert.Equal(t, defaultTenantHeader, hr.HeaderName)
	})

	t.Run("subdomain resolver wraps validation", func(t *testing.T) {
		cfg := &config.Config{Multitenant: config.MultitenantConfig{Resolver: config.ResolverConfig{Type: "subdomain", Domain: testDomain}}}

		resolver := buildTenantResolver(cfg)
		require.IsType(t, &multitenant.ValidatingResolver{}, resolver)
		vr := resolver.(*multitenant.ValidatingResolver)
		assert.Equal(t, multitenant.DefaultTenantIDPattern(), vr.TenantRegex)
		require.IsType(t, &multitenant.SubdomainResolver{}, vr.Resolver)
		sr := vr.Resolver.(*multitenant.SubdomainResolver)
		assert.Equal(t, testDomain, sr.RootDomain)
	})

	t.Run("composite resolver builds header and subdomain", func(t *testing.T) {
		cfg := &config.Config{Multitenant: config.MultitenantConfig{Resolver: config.ResolverConfig{Type: "composite", Header: defaultTenantHeader, Domain: testDomain}}}

		resolver := buildTenantResolver(cfg)
		require.IsType(t, &multitenant.CompositeResolver{}, resolver)
		cr := resolver.(*multitenant.CompositeResolver)
		assert.Equal(t, multitenant.DefaultTenantIDPattern(), cr.TenantRegex)
		assert.Len(t, cr.Resolvers, 2)

		hasHeader := false
		hasSubdomain := false
		for _, r := range cr.Resolvers {
			switch typed := r.(type) {
			case *multitenant.HeaderResolver:
				hasHeader = true
				assert.Equal(t, defaultTenantHeader, typed.HeaderName)
			case *multitenant.SubdomainResolver:
				hasSubdomain = true
				assert.Equal(t, testDomain, typed.RootDomain)
			}
		}
		assert.True(t, hasHeader)
		assert.True(t, hasSubdomain)
	})

	t.Run("invalid resolver type returns nil", func(t *testing.T) {
		cfg := &config.Config{Multitenant: config.MultitenantConfig{Resolver: config.ResolverConfig{Type: "invalid"}}}
		assert.Nil(t, buildTenantResolver(cfg))
	})
}

// TestBuildCompositeResolverOrder pins the security fix: on the default order a
// spoofable X-Tenant-ID header can no longer override a network-bound subdomain
// match, and an operator can still opt back into header-first explicitly.
func TestBuildCompositeResolverOrder(t *testing.T) {
	tests := []struct {
		name           string
		order          []string
		domain         string
		pathSegment    int
		expectedTypes  []any
		expectedTenant string
	}{
		{
			name:           "default_order_resolves_subdomain_over_spoofed_header",
			order:          nil,
			domain:         testDomain,
			expectedTypes:  []any{&multitenant.SubdomainResolver{}, &multitenant.HeaderResolver{}},
			expectedTenant: "a",
		},
		{
			// No domain configured, so the default order's subdomain entry drops
			// out and path is the network-bound source that must beat the header.
			name:           "default_order_resolves_path_over_spoofed_header",
			order:          nil,
			pathSegment:    1,
			expectedTypes:  []any{&multitenant.PathResolver{}, &multitenant.HeaderResolver{}},
			expectedTenant: "a",
		},
		{
			name:           "configured_header_first_order_is_honored",
			order:          []string{config.ResolverTypeHeader, config.ResolverTypeSubdomain},
			domain:         testDomain,
			expectedTypes:  []any{&multitenant.HeaderResolver{}, &multitenant.SubdomainResolver{}},
			expectedTenant: "b",
		},
		{
			name:           "configured_header_before_path_is_honored",
			order:          []string{config.ResolverTypeHeader, config.ResolverTypePath},
			pathSegment:    1,
			expectedTypes:  []any{&multitenant.HeaderResolver{}, &multitenant.PathResolver{}},
			expectedTenant: "b",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &config.Config{Multitenant: config.MultitenantConfig{Resolver: config.ResolverConfig{
				Type:   config.ResolverTypeComposite,
				Header: defaultTenantHeader,
				Domain: tt.domain,
				Path:   config.PathResolverConfig{Segment: tt.pathSegment},
				Order:  tt.order,
			}}}

			resolver := buildTenantResolver(cfg)
			require.IsType(t, &multitenant.CompositeResolver{}, resolver)
			cr := resolver.(*multitenant.CompositeResolver)
			require.Len(t, cr.Resolvers, len(tt.expectedTypes))
			for i, want := range tt.expectedTypes {
				assert.IsType(t, want, cr.Resolvers[i])
			}

			// One request carrying tenant "a" on every network-bound source and a
			// conflicting tenant "b" on the spoofable header.
			ctx := context.Background()
			req := httptest.NewRequestWithContext(ctx, http.MethodGet, "/a/orders", http.NoBody)
			req.Host = "a." + testDomain
			req.Header.Set(defaultTenantHeader, "b")

			tenantID, err := cr.ResolveTenant(ctx, req)
			require.NoError(t, err)
			assert.Equal(t, tt.expectedTenant, tenantID)
		})
	}
}

// TestBuildCompositeResolverUnavailableOrderFallsBack covers the fail-open a
// nil sub-resolver would otherwise open: an unvalidated config naming only an
// unconfigured resolver (subdomain without a domain) must still fall back to
// the default order rather than yield a nil resolver — SetupMiddlewares skips
// the tenant middleware entirely when buildTenantResolver returns nil.
func TestBuildCompositeResolverUnavailableOrderFallsBack(t *testing.T) {
	cfg := &config.Config{Multitenant: config.MultitenantConfig{Resolver: config.ResolverConfig{
		Type:   config.ResolverTypeComposite,
		Header: defaultTenantHeader,
		Order:  []string{config.ResolverTypeSubdomain}, // no Domain set — resolver is nil
	}}}

	resolver := buildTenantResolver(cfg)
	require.NotNil(t, resolver, "an unavailable order must not silently disable tenant resolution")
	cr := resolver.(*multitenant.CompositeResolver)
	require.Len(t, cr.Resolvers, 1)
	assert.IsType(t, &multitenant.HeaderResolver{}, cr.Resolvers[0])
}

// TestBuildCompositeResolverWiresEveryOrderEntry guards the silent-skip class:
// every name config accepts in resolver.order must actually build a sub-resolver
// here. A new entry added to config without a case below would otherwise pass
// validation and then never resolve a tenant.
func TestBuildCompositeResolverWiresEveryOrderEntry(t *testing.T) {
	order := config.DefaultResolverOrder()
	cfg := &config.Config{Multitenant: config.MultitenantConfig{Resolver: config.ResolverConfig{
		Type:   config.ResolverTypeComposite,
		Header: defaultTenantHeader,
		Domain: testDomain,
		Path:   config.PathResolverConfig{Segment: 1},
		Order:  order,
	}}}

	resolver := buildTenantResolver(cfg)
	require.IsType(t, &multitenant.CompositeResolver{}, resolver)
	cr := resolver.(*multitenant.CompositeResolver)
	assert.Len(t, cr.Resolvers, len(order),
		"every entry config.DefaultResolverOrder() accepts must build a sub-resolver")
}

func TestMiddlewareOrder(t *testing.T) {
	e := echo.New()
	log := logger.New("disabled", false)
	cfg := &config.Config{
		App: config.AppConfig{Rate: config.RateConfig{Limit: 100}},
		Server: config.ServerConfig{
			Timeout: config.TimeoutConfig{
				Middleware: 30 * time.Second,
			},
		},
	}

	var middlewareOrder []string

	// Add tracking middleware to verify order
	e.Use(func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) error {
			middlewareOrder = append(middlewareOrder, preSetupMarker)
			return next(c)
		}
	})

	SetupMiddlewares(e, log, cfg, true, testHealthPath, testReadyPath)

	e.Use(func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) error {
			middlewareOrder = append(middlewareOrder, postSetupMarker)
			return next(c)
		}
	})

	e.GET("/test", func(c *echo.Context) error {
		middlewareOrder = append(middlewareOrder, "handler")
		return c.JSON(http.StatusOK, map[string]string{"status": "ok"})
	})

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/test", http.NoBody)
	rec := httptest.NewRecorder()

	e.ServeHTTP(rec, req)

	assert.NotEmpty(t, rec.Header().Get(echo.HeaderXRequestID))

	// Verify middleware executed in correct order
	assert.Contains(t, middlewareOrder, preSetupMarker)
	assert.Contains(t, middlewareOrder, postSetupMarker)
	assert.Contains(t, middlewareOrder, "handler")

	// Verify pre-setup comes before post-setup
	preIndex := -1
	postIndex := -1
	handlerIndex := -1

	for i, mw := range middlewareOrder {
		switch mw {
		case preSetupMarker:
			preIndex = i
		case postSetupMarker:
			postIndex = i
		case "handler":
			handlerIndex = i
		}
	}

	assert.True(t, preIndex < postIndex, "pre-setup should come before post-setup")
	assert.True(t, postIndex < handlerIndex, "post-setup should come before handler")
}

func TestMiddlewareBodyLimit(t *testing.T) {
	e := echo.New()
	log := logger.New("disabled", false)
	cfg := &config.Config{
		App: config.AppConfig{Rate: config.RateConfig{Limit: 100}},
		Server: config.ServerConfig{
			Timeout: config.TimeoutConfig{
				Middleware: 30 * time.Second,
			},
		},
	}

	SetupMiddlewares(e, log, cfg, true, testHealthPath, testReadyPath)

	e.POST("/test", func(c *echo.Context) error {
		return c.JSON(http.StatusOK, map[string]string{"status": "ok"})
	})

	t.Run("body_within_limit", func(t *testing.T) {
		body := strings.NewReader(`{"data": "small payload"}`)
		req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/test", body)
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()

		e.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusOK, rec.Code)
	})

	t.Run("body_exceeds_limit", func(t *testing.T) {
		// Create a payload larger than 10MB
		largePayload := strings.Repeat("x", 11*1024*1024) // 11MB
		body := strings.NewReader(largePayload)
		req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/test", body)
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()

		e.ServeHTTP(rec, req)

		// Should be rejected due to body size limit
		assert.Equal(t, http.StatusRequestEntityTooLarge, rec.Code)
	})
}

func TestMiddlewareBodyLimitFromConfig(t *testing.T) {
	log := logger.New("disabled", false)

	newEngine := func(limit int64) *echo.Echo {
		e := echo.New()
		cfg := &config.Config{
			App: config.AppConfig{Rate: config.RateConfig{Limit: 100}},
			Server: config.ServerConfig{
				Timeout:   config.TimeoutConfig{Middleware: 30 * time.Second},
				BodyLimit: limit,
			},
		}
		SetupMiddlewares(e, log, cfg, true, testHealthPath, testReadyPath)
		e.POST("/test", func(c *echo.Context) error {
			return c.JSON(http.StatusOK, map[string]string{"status": "ok"})
		})
		return e
	}

	post := func(e *echo.Echo, size int) int {
		body := strings.NewReader(strings.Repeat("x", size))
		req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/test", body)
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)
		return rec.Code
	}

	t.Run("configured_limit_is_enforced", func(t *testing.T) {
		e := newEngine(1024) // 1 KB
		assert.Equal(t, http.StatusOK, post(e, 512))
		assert.Equal(t, http.StatusRequestEntityTooLarge, post(e, 2048))
	})

	t.Run("non_positive_limit_falls_back_to_default", func(t *testing.T) {
		// This pins the BACKSTOP, not the default's owner: config.normalizeServer fills a
		// zero and refuses a negative, so neither value reaches here on a validated path.
		// What is tested is the construction paths trustedProxyOptions names, which never
		// run Validate — for those, a 0 or -1 must still resolve to the shared 10 MB cap.
		// Handing echo BodyLimit(0) would not uncap the server, it would 413 every request
		// with a body (echo compares ContentLength > LimitBytes), so the guard is what
		// keeps an unvalidated config serving at all. Pin the boundary tightly against
		// DefaultBodyLimitBytes: a body just under it passes, one just over it is rejected.
		underDefault := int(config.DefaultBodyLimitBytes) - 1024
		overDefault := int(config.DefaultBodyLimitBytes) + 1024
		for _, limit := range []int64{0, -1} {
			e := newEngine(limit)
			assert.Equal(t, http.StatusOK, post(e, underDefault),
				"limit %d must fall back to the 10 MB default (accepts an under-default body)", limit)
			assert.Equal(t, http.StatusRequestEntityTooLarge, post(e, overDefault),
				"limit %d must fall back to the 10 MB default (rejects an over-default body)", limit)
		}
	})
}

func TestGzipMiddleware(t *testing.T) {
	e := echo.New()
	log := logger.New("disabled", false)
	cfg := &config.Config{
		App: config.AppConfig{Rate: config.RateConfig{Limit: 100}},
		Server: config.ServerConfig{
			Timeout: config.TimeoutConfig{
				Middleware: 30 * time.Second,
			},
		},
	}

	SetupMiddlewares(e, log, cfg, true, testHealthPath, testReadyPath)

	// Create handler that returns large response
	largeResponse := strings.Repeat("This is a test response that should be compressed. ", 100)
	e.GET("/test", func(c *echo.Context) error {
		return c.String(http.StatusOK, largeResponse)
	})

	t.Run("gzip_compression_enabled", func(t *testing.T) {
		req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/test", http.NoBody)
		req.Header.Set("Accept-Encoding", "gzip")
		rec := httptest.NewRecorder()

		e.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusOK, rec.Code)
		assert.Equal(t, "gzip", rec.Header().Get("Content-Encoding"))
		assert.Contains(t, rec.Header().Values(echo.HeaderVary), "Accept-Encoding")

		// Compressed response should be smaller than original
		assert.Less(t, len(rec.Body.Bytes()), len(largeResponse))
	})

	t.Run("no_compression_when_not_requested", func(t *testing.T) {
		req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/test", http.NoBody)
		// No Accept-Encoding header
		rec := httptest.NewRecorder()

		e.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusOK, rec.Code)
		assert.Empty(t, rec.Header().Get("Content-Encoding"))

		// Uncompressed response should match original length
		assert.Equal(t, len(largeResponse), len(rec.Body.String()))
	})
}

// TestGzipMiddlewareMinLength verifies the server.gzip.minlength wiring: responses
// smaller than the configured threshold are sent uncompressed, larger ones gzipped.
func TestGzipMiddlewareMinLength(t *testing.T) {
	e := echo.New()
	log := logger.New("disabled", false)
	cfg := &config.Config{
		App: config.AppConfig{Rate: config.RateConfig{Limit: 100}},
		Server: config.ServerConfig{
			Timeout: config.TimeoutConfig{Middleware: 30 * time.Second},
			Gzip:    config.GzipConfig{MinLength: 2048},
		},
	}
	SetupMiddlewares(e, log, cfg, true, testHealthPath, testReadyPath)

	e.GET("/small", func(c *echo.Context) error { return c.String(http.StatusOK, "tiny") })
	e.GET("/large", func(c *echo.Context) error {
		return c.String(http.StatusOK, strings.Repeat("compress me please. ", 200)) // ~4000 bytes
	})

	t.Run("below_minlength_not_compressed", func(t *testing.T) {
		req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/small", http.NoBody)
		req.Header.Set("Accept-Encoding", "gzip")
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)
		assert.Empty(t, rec.Header().Get("Content-Encoding"),
			"responses below server.gzip.minlength must not be compressed")
	})

	t.Run("above_minlength_compressed", func(t *testing.T) {
		req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/large", http.NoBody)
		req.Header.Set("Accept-Encoding", "gzip")
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)
		assert.Equal(t, "gzip", rec.Header().Get("Content-Encoding"),
			"responses above server.gzip.minlength must be compressed")
	})
}

func TestRecoveryMiddleware(t *testing.T) {
	e := echo.New()
	log := logger.New("debug", false) // Enable logging to capture panic logs
	cfg := &config.Config{
		App: config.AppConfig{Rate: config.RateConfig{Limit: 100}},
		Server: config.ServerConfig{
			Timeout: config.TimeoutConfig{
				Middleware: 30 * time.Second,
			},
		},
	}

	SetupMiddlewares(e, log, cfg, true, testHealthPath, testReadyPath)

	// Handler that panics
	e.GET("/panic", func(_ *echo.Context) error {
		panic("test panic")
	})

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/panic", http.NoBody)
	rec := httptest.NewRecorder()

	// This should not crash the server
	e.ServeHTTP(rec, req)

	// Should return 500 Internal Server Error
	assert.Equal(t, http.StatusInternalServerError, rec.Code)

	// Should have request ID in response
	assert.NotEmpty(t, rec.Header().Get(echo.HeaderXRequestID))
}

func TestSecurityHeaders(t *testing.T) {
	e := echo.New()
	log := logger.New("disabled", false)
	cfg := &config.Config{
		App: config.AppConfig{Rate: config.RateConfig{Limit: 100}},
		Server: config.ServerConfig{
			Timeout: config.TimeoutConfig{
				Middleware: 30 * time.Second,
			},
		},
	}

	SetupMiddlewares(e, log, cfg, true, testHealthPath, testReadyPath)

	e.GET("/test", func(c *echo.Context) error {
		return c.JSON(http.StatusOK, map[string]string{"status": "ok"})
	})

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/test", http.NoBody)
	rec := httptest.NewRecorder()

	e.ServeHTTP(rec, req)

	// Test all security headers
	securityHeaders := map[string]string{
		"X-XSS-Protection":        "1; mode=block",
		"X-Content-Type-Options":  "nosniff",
		"X-Frame-Options":         "SAMEORIGIN",
		"Content-Security-Policy": "default-src 'self'",
	}

	for header, expectedValue := range securityHeaders {
		assert.Equal(t, expectedValue, rec.Header().Get(header),
			"Security header %s should be set correctly", header)
	}

	// HSTS header should contain max-age (note: HSTS may not be set for HTTP in test)
	hstsHeader := rec.Header().Get("Strict-Transport-Security")
	if hstsHeader != "" {
		assert.Contains(t, hstsHeader, "max-age=3600")
	}
}

// forwardedCertRequireWarnNeedle is the distinguishing fragment of the startup
// WARN that setupIdentityMiddlewares emits when require implies registration.
const forwardedCertRequireWarnNeedle = "forwardedclientcert.require=true with enabled=false"

// newForwardedCertCfg builds a minimal SetupMiddlewares config carrying the
// given forwarded-client-cert posture.
func newForwardedCertCfg(enabled, requireIdentity bool) *config.Config {
	return &config.Config{
		App: config.AppConfig{Rate: config.RateConfig{Limit: 100}},
		Server: config.ServerConfig{
			Timeout:             config.TimeoutConfig{Middleware: 30 * time.Second},
			ForwardedClientCert: config.ForwardedClientCertConfig{Enabled: enabled, Require: requireIdentity},
		},
	}
}

// forwardedCertProbe drives one request through e and reports the status code
// plus whether the middleware exposed an identity on the request context.
func forwardedCertProbe(e *echo.Echo, path string, withCertHeaders bool) (code int, sawIdentity bool) {
	e.GET(path, func(c *echo.Context) error {
		_, sawIdentity = ForwardedClientCertFromContext(c.Request().Context())
		return c.String(http.StatusOK, "ok")
	})

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, path, http.NoBody)
	if withCertHeaders {
		req.Header.Set(headerClientCertSubject, testForwardedCertSubject)
		req.Header.Set(headerClientCertSerialNumber, testForwardedCertSerial)
	}
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	return rec.Code, sawIdentity
}

// TestForwardedClientCertRequireWithoutEnabledFailsClosed is the regression pin
// for the app.NewWithConfig bypass: that path never calls config.Validate, so a
// programmatic config with Require set and Enabled left false used to register
// no middleware at all and serve every request unauthenticated. Require now
// implies registration, so the request is rejected.
func TestForwardedClientCertRequireWithoutEnabledFailsClosed(t *testing.T) {
	t.Run("unauthenticated_request_is_rejected", func(t *testing.T) {
		e := newTenantTestEcho()
		SetupMiddlewares(e, &capturingLogger{}, newForwardedCertCfg(false, true), true, testHealthPath, testReadyPath)

		code, sawIdentity := forwardedCertProbe(e, "/check", false)

		assert.Equal(t, http.StatusUnauthorized, code,
			"require=true must fail closed even though enabled was left false")
		assert.False(t, sawIdentity, "the handler must never run for a rejected request")
	})

	// The probe skipper is passed through unchanged in the require-implied case:
	// ALB health checks present no client certificate, so requiring identity must
	// never take the target group down.
	t.Run("health_probe_still_bypasses", func(t *testing.T) {
		e := newTenantTestEcho()
		SetupMiddlewares(e, &capturingLogger{}, newForwardedCertCfg(false, true), true, testHealthPath, testReadyPath)

		code, _ := forwardedCertProbe(e, testHealthPath, false)

		assert.Equal(t, http.StatusOK, code, "health probes must stay exempt when require implies registration")
	})

	t.Run("ready_probe_still_bypasses", func(t *testing.T) {
		e := newTenantTestEcho()
		SetupMiddlewares(e, &capturingLogger{}, newForwardedCertCfg(false, true), true, testHealthPath, testReadyPath)

		code, _ := forwardedCertProbe(e, testReadyPath, false)

		assert.Equal(t, http.StatusOK, code, "ready probes must stay exempt when require implies registration")
	})
}

// TestForwardedClientCertDisabledStaysDisabled pins the all-zero posture: the
// middleware must remain unwired, so the accessor reports absent even when a
// caller sends the headers.
func TestForwardedClientCertDisabledStaysDisabled(t *testing.T) {
	capturer := &capturingLogger{}
	e := newTenantTestEcho()
	SetupMiddlewares(e, capturer, newForwardedCertCfg(false, false), true, testHealthPath, testReadyPath)
	assert.NotContains(t, strings.Join(capturer.warns, "\n"), forwardedCertRequireWarnNeedle,
		"the all-zero posture must not warn")

	code, sawIdentity := forwardedCertProbe(e, "/check", true)

	assert.Equal(t, http.StatusOK, code)
	assert.False(t, sawIdentity, "neither flag set — the middleware must never be registered")
}

// TestForwardedClientCertEnabledWithoutRequireStillRegisters guards the widened
// condition against collapsing to require-only: enabled alone must keep wiring
// the parse-and-expose middleware.
func TestForwardedClientCertEnabledWithoutRequireStillRegisters(t *testing.T) {
	e := newTenantTestEcho()
	SetupMiddlewares(e, &capturingLogger{}, newForwardedCertCfg(true, false), true, testHealthPath, testReadyPath)

	code, sawIdentity := forwardedCertProbe(e, "/check", true)

	assert.Equal(t, http.StatusOK, code, "require=false never rejects")
	assert.True(t, sawIdentity, "enabled=true alone must still register the middleware")
}

// TestForwardedClientCertRequireImpliedWarn pins the startup WARN to exactly the
// incoherent combination — a coherent require+enabled config must stay silent.
func TestForwardedClientCertRequireImpliedWarn(t *testing.T) {
	tests := []struct {
		name     string
		enabled  bool
		wantWarn bool
	}{
		{name: "require_without_enabled_warns", enabled: false, wantWarn: true},
		{name: "require_with_enabled_is_silent", enabled: true, wantWarn: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			capturer := &capturingLogger{}
			SetupMiddlewares(newTenantTestEcho(), capturer, newForwardedCertCfg(tc.enabled, true),
				true, testHealthPath, testReadyPath)

			joined := strings.Join(capturer.warns, "\n")
			if tc.wantWarn {
				assert.Contains(t, joined, forwardedCertRequireWarnNeedle)
				assert.Contains(t, joined, "Set enabled=true explicitly.",
					"the WARN must name the remedy, not just the symptom")
			} else {
				assert.NotContains(t, joined, forwardedCertRequireWarnNeedle,
					"a coherent require+enabled config must not warn")
			}
		})
	}
}

// testHeaderResolver validates header resolver properties

// TestRecoveredPanicNeverReachesTheSpan pins ADR-081 on the HTTP lane. The OTel
// middleware is OUTER to Recover, so it reads the error Recover produces and puts
// it in the span status description — off-platform, on every panicking request,
// in production posture. Echo builds that error with its own `fmt.Errorf("%v", r)`,
// or uses the panic value verbatim when it is already an error, so both shapes
// must be covered: testing only panic("string") would pass while missing the
// panic(fmt.Errorf(...)) case entirely.
func TestRecoveredPanicNeverReachesTheSpan(t *testing.T) {
	tests := []struct {
		name     string
		panicVal any
		wantType string
	}{
		{name: "bare_string", panicVal: recoverProbeSecret, wantType: "string"},
		// fmt.Errorf WITHOUT %w yields *errors.errorString; the point of the case is
		// that Echo adopts an error panic verbatim and renders nothing, so the value
		// would walk through untouched without a first-party %T.
		{name: "plain_error", panicVal: fmt.Errorf("boom: %s", recoverProbeSecret), wantType: "*errors.errorString"},
		{name: "wrapped_error", panicVal: fmt.Errorf("boom: %w", errors.New(recoverProbeSecret)), wantType: "*fmt.wrapError"},
		// The decisive case: a consumer's own error type. Anything that reads the
		// type off Echo's WRAPPED error reports *errors.errorString here and loses
		// the only diagnostic ADR-081 trades the value for.
		{name: "domain_error", panicVal: &probeDomainError{}, wantType: "*server.probeDomainError"},
		{name: "struct_value", panicVal: struct{ Token string }{recoverProbeSecret}, wantType: "struct { Token string }"},
		{name: "map_value", panicVal: map[string]string{"token": recoverProbeSecret}, wantType: "map[string]string"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			exporter := tracetest.NewInMemoryExporter()
			tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))

			// Restore the process-wide provider: the OTel middleware caches the
			// provider at SETUP time, so a provider left installed outlives this
			// test and silently changes what every later test in the binary records.
			prevTP := otel.GetTracerProvider()
			// no Shutdown: the first-installed provider is otel's permanent delegate (internal/global/state.go sync.Once, #1093)
			t.Cleanup(func() { otel.SetTracerProvider(prevTP) })
			otel.SetTracerProvider(tp)

			cfg := &config.Config{}
			cfg.App.Name = "panic-span"
			cfg.App.Env = "production"

			e := echo.New()
			SetupMiddlewares(e, logger.New("disabled", true), cfg, true, "/health", "/ready")
			e.GET("/boom", func(*echo.Context) error { panic(tt.panicVal) })

			rec := httptest.NewRecorder()
			e.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/boom", http.NoBody))
			require.NoError(t, tp.ForceFlush(t.Context()))

			spans := exporter.GetSpans()
			require.NotEmpty(t, spans, "the OTel middleware must have produced a span")
			for _, s := range spans {
				// SECURITY: panic value - synthetic constant, and rendering it IS the
				// assertion that proves absence. assert.False so a FAILURE does not
				// print the container and disclose the payload it is asserting is gone.
				// This is a test asserting the rule, not a site subject to it.
				assert.False(t, strings.Contains(s.Status.Description, recoverProbeSecret),
					"span status description discloses the panic value")
				assert.Contains(t, s.Status.Description, tt.wantType,
					"span status description should name the panic value's TYPE")
				for _, ev := range s.Events {
					for _, attr := range ev.Attributes {
						// SECURITY: panic value - see above; assert.False keeps the
						// payload out of failure output.
						assert.False(t, strings.Contains(attr.Value.String(), recoverProbeSecret),
							fmt.Sprintf("span event attribute %q discloses the panic value", attr.Key))
					}
				}
			}
			assert.Equal(t, http.StatusInternalServerError, rec.Code)
		})
	}
}

// recoverProbeSecret stands in for anything sensitive a handler might panic with.
const recoverProbeSecret = "not-a-real-secret-5512"

// probeDomainError stands in for a consumer's own error type.
type probeDomainError struct{}

func (*probeDomainError) Error() string { return "domain failure: " + recoverProbeSecret }

// TestSanitizePanicValuePreservesTheAbortContract pins net/http's sentinel: it
// must reach the server untouched, exactly as Echo's own Recover re-panics it.
// Sanitizing it would swallow a connection abort into a 500.
func TestSanitizePanicValuePreservesTheAbortContract(t *testing.T) {
	mw := sanitizePanicValue()
	h := mw(func(*echo.Context) error { panic(http.ErrAbortHandler) })

	var recovered any
	func() {
		defer func() { recovered = recover() }()
		_ = h(nil)
	}()

	assert.Equal(t, http.ErrAbortHandler, recovered,
		"the abort sentinel must propagate unchanged, not become a panicTypeError")
}

// TestSanitizePanicValueStillYieldsAPanicStackError pins the dependency the HTTP
// error handler has on Echo's shape: server.New does errors.As(&*PanicStackError)
// to emit its structured "Panic recovered" line with the stack. Sanitizing must
// not break that, or the panic log silently stops.
func TestSanitizePanicValueStillYieldsAPanicStackError(t *testing.T) {
	e := echo.New()
	cfg := &config.Config{}
	cfg.App.Name = "panic-shape"
	SetupMiddlewares(e, logger.New("disabled", true), cfg, false, "/health", "/ready")

	var seen error
	e.HTTPErrorHandler = func(_ *echo.Context, err error) { seen = err }
	e.GET("/boom", func(*echo.Context) error { panic(recoverProbeSecret) })
	e.ServeHTTP(httptest.NewRecorder(), httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/boom", http.NoBody))

	var pse *middleware.PanicStackError
	require.True(t, errors.As(seen, &pse), "server.New's errors.As branch must still match")
	assert.NotEmpty(t, pse.Stack, "the stack must survive — it identifies the panic site")
	// SECURITY: panic value - see the span assertions above; assert.False keeps the
	// payload out of failure output.
	assert.False(t, strings.Contains(seen.Error(), recoverProbeSecret),
		"the rendered error discloses the panic value")
	assert.Contains(t, seen.Error(), "panic (type: string)")

	// Pins the ONE assumption this design makes about Echo: that Recover adopts an
	// error panic verbatim (`tmpErr, ok := r.(error)`) instead of rendering it. If a
	// future Echo renders instead, Unwrap stops being our type and every panic
	// collapses to *errors.errorString — the exact degradation this design exists to
	// avoid — while the message text above still reads correctly. Assert the TYPE, or
	// the regression is silent.
	var typed *panicTypeError
	assert.True(t, errors.As(pse.Unwrap(), &typed),
		"Echo must adopt the sanitized error verbatim; if it renders it instead, the true panic type is lost")
}

// TestSanitizePanicValueKeepsTheOriginalFrameInTheStack pins diagnostics, not
// secrecy. Re-panicking means Echo's runtime.Stack runs at ITS recover point,
// after unwinding began — if the original frames were lost the stack would name
// only the sanitizer, and a test that merely checks the secret is absent would
// pass while the panic site had become unfindable.
func TestSanitizePanicValueKeepsTheOriginalFrameInTheStack(t *testing.T) {
	e := echo.New()
	cfg := &config.Config{}
	cfg.App.Name = "panic-stack"
	SetupMiddlewares(e, logger.New("disabled", true), cfg, false, "/health", "/ready")

	var seen error
	e.HTTPErrorHandler = func(_ *echo.Context, err error) { seen = err }
	e.GET("/boom", theOriginalPanickingHandler)
	e.ServeHTTP(httptest.NewRecorder(), httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/boom", http.NoBody))

	var pse *middleware.PanicStackError
	require.True(t, errors.As(seen, &pse))
	assert.Contains(t, string(pse.Stack), "theOriginalPanickingHandler",
		"the stack must still name the function that panicked, not just the sanitizer")
	// SECURITY: panic value - see the span assertions above; assert.False keeps the
	// payload out of failure output.
	assert.False(t, strings.Contains(string(pse.Stack), recoverProbeSecret),
		"the stack discloses the panic value")
}

// theOriginalPanickingHandler is named distinctly so its frame is unambiguous.
func theOriginalPanickingHandler(*echo.Context) error { panic(recoverProbeSecret) }

// TestSanitizePanicValueRefusesAWrappedAbortSentinel pins the bypass gate's
// WIDTH, not its correctness. The abort branch re-panics the value unsanitized,
// so anything that satisfies it escapes the type-only rule entirely. errors.Is
// matches a WRAPPED sentinel, which lets `panic(fmt.Errorf("%s: %w", secret,
// http.ErrAbortHandler))` carry a payload straight through to Echo — which
// adopts an error verbatim — and on to the span status and the action log.
// net/http only honors the exact sentinel, so identity is both the safe and the
// faithful comparison here: breadth in a bypass gate is a defect, not leniency.
func TestSanitizePanicValueRefusesAWrappedAbortSentinel(t *testing.T) {
	wrapped := fmt.Errorf("carrier %s: %w", recoverProbeSecret, http.ErrAbortHandler)

	mw := sanitizePanicValue()
	h := mw(func(*echo.Context) error { panic(wrapped) })

	var recovered any
	func() {
		defer func() { recovered = recover() }()
		_ = h(nil)
	}()

	require.NotNil(t, recovered)
	// SECURITY: panic value - synthetic constant, and rendering it IS the assertion
	// that proves absence: a type check alone would pass for a *panicTypeError whose
	// own field carried the payload, so this half is load-bearing and stays.
	// assert.False so a FAILURE does not print the rendering and disclose it.
	// This is a test asserting the rule, not a site subject to it.
	rendered := fmt.Sprintf("%v", recovered)
	assert.False(t, strings.Contains(rendered, recoverProbeSecret),
		"a wrapped abort sentinel must NOT take the bypass — it carries a payload")
	var typed *panicTypeError
	require.True(t, errors.As(recovered.(error), &typed),
		"only the exact sentinel bypasses sanitizing")
}
