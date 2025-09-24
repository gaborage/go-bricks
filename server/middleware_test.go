package server

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"

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

	// Test constants for buildTenantResolver tests
	headerResolverType     = "*multitenant.HeaderResolver"
	subdomainResolverType  = "*multitenant.SubdomainResolver"
	validatingResolverType = "*multitenant.ValidatingResolver"
	compositeResolverType  = "*multitenant.CompositeResolver"
	defaultTenantHeader    = "X-Tenant-ID"
	testDomain             = "example.com"
)

// testData represents the test case data for buildTenantResolver tests
type testData struct {
	name            string
	config          *config.Config
	expectNil       bool
	expectType      string
	expectValidated bool
	headerName      string
	rootDomain      string
	trustProxies    bool
}

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
			SetupMiddlewares(e, log, tt.config, testHealthPath, testReadyPath)

			// Create test handler
			e.GET("/test", func(c echo.Context) error {
				return c.JSON(http.StatusOK, map[string]string{"status": "ok"})
			})

			// Test request
			req := httptest.NewRequest(http.MethodGet, "/test", http.NoBody)
			rec := httptest.NewRecorder()

			e.ServeHTTP(rec, req)

			// Verify security headers are set
			assert.Equal(t, "1; mode=block", rec.Header().Get("X-XSS-Protection"))
			assert.Equal(t, "nosniff", rec.Header().Get("X-Content-Type-Options"))
			assert.Equal(t, "SAMEORIGIN", rec.Header().Get("X-Frame-Options"))
			assert.Equal(t, "default-src 'self'", rec.Header().Get("Content-Security-Policy"))

			// HSTS header might not be set for HTTP in test environment
			hstsHeader := rec.Header().Get("Strict-Transport-Security")
			if hstsHeader != "" {
				assert.Contains(t, hstsHeader, "max-age=3600")
			}

			// Verify request ID is set
			assert.NotEmpty(t, rec.Header().Get(echo.HeaderXRequestID))

			// Verify timing header is set
			assert.NotEmpty(t, rec.Header().Get("X-Response-Time"))

			// Response should be successful
			assert.Equal(t, http.StatusOK, rec.Code)
		})
	}
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
		return func(c echo.Context) error {
			middlewareOrder = append(middlewareOrder, preSetupMarker)
			return next(c)
		}
	})

	SetupMiddlewares(e, log, cfg, testHealthPath, testReadyPath)

	e.Use(func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			middlewareOrder = append(middlewareOrder, postSetupMarker)
			return next(c)
		}
	})

	e.GET("/test", func(c echo.Context) error {
		middlewareOrder = append(middlewareOrder, "handler")
		return c.JSON(http.StatusOK, map[string]string{"status": "ok"})
	})

	req := httptest.NewRequest(http.MethodGet, "/test", http.NoBody)
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

	SetupMiddlewares(e, log, cfg, testHealthPath, testReadyPath)

	e.POST("/test", func(c echo.Context) error {
		return c.JSON(http.StatusOK, map[string]string{"status": "ok"})
	})

	t.Run("body_within_limit", func(t *testing.T) {
		body := strings.NewReader(`{"data": "small payload"}`)
		req := httptest.NewRequest(http.MethodPost, "/test", body)
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()

		e.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusOK, rec.Code)
	})

	t.Run("body_exceeds_limit", func(t *testing.T) {
		// Create a payload larger than 10MB
		largePayload := strings.Repeat("x", 11*1024*1024) // 11MB
		body := strings.NewReader(largePayload)
		req := httptest.NewRequest(http.MethodPost, "/test", body)
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()

		e.ServeHTTP(rec, req)

		// Should be rejected due to body size limit
		assert.Equal(t, http.StatusRequestEntityTooLarge, rec.Code)
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

	SetupMiddlewares(e, log, cfg, testHealthPath, testReadyPath)

	// Create handler that returns large response
	largeResponse := strings.Repeat("This is a test response that should be compressed. ", 100)
	e.GET("/test", func(c echo.Context) error {
		return c.String(http.StatusOK, largeResponse)
	})

	t.Run("gzip_compression_enabled", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/test", http.NoBody)
		req.Header.Set("Accept-Encoding", "gzip")
		rec := httptest.NewRecorder()

		e.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusOK, rec.Code)
		assert.Equal(t, "gzip", rec.Header().Get("Content-Encoding"))
		assert.Contains(t, rec.Header().Get("Vary"), "Accept-Encoding")

		// Compressed response should be smaller than original
		assert.Less(t, len(rec.Body.Bytes()), len(largeResponse))
	})

	t.Run("no_compression_when_not_requested", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/test", http.NoBody)
		// No Accept-Encoding header
		rec := httptest.NewRecorder()

		e.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusOK, rec.Code)
		assert.Empty(t, rec.Header().Get("Content-Encoding"))

		// Uncompressed response should match original length
		assert.Equal(t, len(largeResponse), len(rec.Body.String()))
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

	SetupMiddlewares(e, log, cfg, testHealthPath, testReadyPath)

	// Handler that panics
	e.GET("/panic", func(_ echo.Context) error {
		panic("test panic")
	})

	req := httptest.NewRequest(http.MethodGet, "/panic", http.NoBody)
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

	SetupMiddlewares(e, log, cfg, testHealthPath, testReadyPath)

	e.GET("/test", func(c echo.Context) error {
		return c.JSON(http.StatusOK, map[string]string{"status": "ok"})
	})

	req := httptest.NewRequest(http.MethodGet, "/test", http.NoBody)
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

func TestBuildTenantResolver(t *testing.T) {
	tests := []testData{
		{
			name: "header_resolver_default_header",
			config: &config.Config{
				Multitenant: config.MultitenantConfig{
					Resolver: config.ResolverConfig{
						Type: "header",
					},
					Validation: config.IDValidationConfig{},
				},
			},
			expectType: headerResolverType,
			headerName: defaultTenantHeader,
		},
		{
			name: "header_resolver_custom_header",
			config: &config.Config{
				Multitenant: config.MultitenantConfig{
					Resolver: config.ResolverConfig{
						Type:   "header",
						Header: "X-Custom-Tenant",
					},
					Validation: config.IDValidationConfig{},
				},
			},
			expectType: headerResolverType,
			headerName: "X-Custom-Tenant",
		},
		{
			name: "header_resolver_with_validation",
			config: func() *config.Config {
				cfg := &config.Config{
					Multitenant: config.MultitenantConfig{
						Resolver: config.ResolverConfig{
							Type:   "header",
							Header: defaultTenantHeader,
						},
						Validation: config.IDValidationConfig{},
					},
				}
				// Set regex pattern to enable validation
				cfg.Multitenant.Validation.SetRegex(`^[a-z0-9-]{1,64}$`)
				return cfg
			}(),
			expectType:      validatingResolverType,
			expectValidated: true,
		},
		{
			name: "subdomain_resolver_basic",
			config: &config.Config{
				Multitenant: config.MultitenantConfig{
					Resolver: config.ResolverConfig{
						Type:   "subdomain",
						Domain: testDomain,
					},
					Validation: config.IDValidationConfig{},
				},
			},
			expectType: subdomainResolverType,
			rootDomain: testDomain,
		},
		{
			name: "subdomain_resolver_with_leading_dot",
			config: &config.Config{
				Multitenant: config.MultitenantConfig{
					Resolver: config.ResolverConfig{
						Type:   "subdomain",
						Domain: "." + testDomain,
					},
					Validation: config.IDValidationConfig{},
				},
			},
			expectType: subdomainResolverType,
			rootDomain: testDomain,
		},
		{
			name: "subdomain_resolver_with_proxies",
			config: &config.Config{
				Multitenant: config.MultitenantConfig{
					Resolver: config.ResolverConfig{
						Type:    "subdomain",
						Domain:  testDomain,
						Proxies: true,
					},
					Validation: config.IDValidationConfig{},
				},
			},
			expectType:   subdomainResolverType,
			rootDomain:   testDomain,
			trustProxies: true,
		},
		{
			name: "subdomain_resolver_empty_domain",
			config: &config.Config{
				Multitenant: config.MultitenantConfig{
					Resolver: config.ResolverConfig{
						Type:   "subdomain",
						Domain: "",
					},
					Validation: config.IDValidationConfig{},
				},
			},
			expectNil: true,
		},
		{
			name: "subdomain_resolver_with_validation",
			config: func() *config.Config {
				cfg := &config.Config{
					Multitenant: config.MultitenantConfig{
						Resolver: config.ResolverConfig{
							Type:   "subdomain",
							Domain: testDomain,
						},
						Validation: config.IDValidationConfig{},
					},
				}
				cfg.Multitenant.Validation.SetRegex(`^[a-z0-9-]{1,64}$`)
				return cfg
			}(),
			expectType:      validatingResolverType,
			expectValidated: true,
		},
		{
			name: "composite_resolver_both_configured",
			config: &config.Config{
				Multitenant: config.MultitenantConfig{
					Resolver: config.ResolverConfig{
						Type:   "composite",
						Header: defaultTenantHeader,
						Domain: testDomain,
					},
					Validation: config.IDValidationConfig{},
				},
			},
			expectType: compositeResolverType,
		},
		{
			name: "composite_resolver_header_only",
			config: &config.Config{
				Multitenant: config.MultitenantConfig{
					Resolver: config.ResolverConfig{
						Type:   "composite",
						Header: defaultTenantHeader,
						Domain: "", // Empty domain should not create subdomain resolver
					},
					Validation: config.IDValidationConfig{},
				},
			},
			expectType: compositeResolverType,
		},
		{
			name: "composite_resolver_subdomain_only",
			config: &config.Config{
				Multitenant: config.MultitenantConfig{
					Resolver: config.ResolverConfig{
						Type:   "composite",
						Header: "", // Empty header should still create default header resolver
						Domain: testDomain,
					},
					Validation: config.IDValidationConfig{},
				},
			},
			expectType: compositeResolverType,
		},
		{
			name: "composite_resolver_no_valid_resolvers",
			config: &config.Config{
				Multitenant: config.MultitenantConfig{
					Resolver: config.ResolverConfig{
						Type:   "composite",
						Header: "", // Will create default header resolver
						Domain: "", // Won't create subdomain resolver
					},
					Validation: config.IDValidationConfig{},
				},
			},
			expectType: compositeResolverType, // Should still create composite with header resolver
		},
		{
			name: "invalid_resolver_type",
			config: &config.Config{
				Multitenant: config.MultitenantConfig{
					Resolver: config.ResolverConfig{
						Type: "invalid",
					},
					Validation: config.IDValidationConfig{},
				},
			},
			expectNil: true,
		},
		{
			name: "empty_resolver_type",
			config: &config.Config{
				Multitenant: config.MultitenantConfig{
					Resolver: config.ResolverConfig{
						Type: "",
					},
					Validation: config.IDValidationConfig{},
				},
			},
			expectNil: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resolver := buildTenantResolver(tt.config)

			if tt.expectNil {
				assert.Nil(t, resolver, "Expected resolver to be nil")
				return
			}

			assert.NotNil(t, resolver, "Expected resolver to be created")

			// Check resolver type
			resolverType := strings.ReplaceAll(reflect.TypeOf(resolver).String(), "server.", "")
			assert.Equal(t, tt.expectType, resolverType, "Unexpected resolver type")

			// Test specific resolver properties using helper functions
			testResolverProperties(t, resolver, &tt)
		})
	}
}

// testResolverProperties tests the properties of different resolver types
func testResolverProperties(t *testing.T, resolver multitenant.TenantResolver, td *testData) {
	switch td.expectType {
	case headerResolverType:
		testHeaderResolver(t, resolver, td)
	case subdomainResolverType:
		testSubdomainResolver(t, resolver, td)
	case validatingResolverType:
		testValidatingResolver(t, resolver)
	case compositeResolverType:
		testCompositeResolver(t, resolver, td)
	}
}

// testHeaderResolver validates header resolver properties
func testHeaderResolver(t *testing.T, resolver multitenant.TenantResolver, td *testData) {
	headerResolver := resolver.(*multitenant.HeaderResolver)
	assert.Equal(t, td.headerName, headerResolver.HeaderName)
}

// testSubdomainResolver validates subdomain resolver properties
func testSubdomainResolver(t *testing.T, resolver multitenant.TenantResolver, td *testData) {
	subdomainResolver := resolver.(*multitenant.SubdomainResolver)
	assert.Equal(t, td.rootDomain, subdomainResolver.RootDomain)
	assert.Equal(t, td.trustProxies, subdomainResolver.TrustProxies)
}

// testValidatingResolver validates validating resolver properties
func testValidatingResolver(t *testing.T, resolver multitenant.TenantResolver) {
	validatingResolver := resolver.(*multitenant.ValidatingResolver)
	assert.NotNil(t, validatingResolver.Resolver, "ValidatingResolver should wrap another resolver")
	assert.NotNil(t, validatingResolver.TenantRegex, "ValidatingResolver should have regex")
}

// testCompositeResolver validates composite resolver properties
func testCompositeResolver(t *testing.T, resolver multitenant.TenantResolver, td *testData) {
	compositeResolver := resolver.(*multitenant.CompositeResolver)
	assert.NotEmpty(t, compositeResolver.Resolvers, "CompositeResolver should have at least one resolver")

	// Check that resolvers were created correctly
	hasHeader := false
	hasSubdomain := false
	for _, r := range compositeResolver.Resolvers {
		switch r.(type) {
		case *multitenant.HeaderResolver:
			hasHeader = true
		case *multitenant.SubdomainResolver:
			hasSubdomain = true
		}
	}

	// Should always have header resolver (uses default if empty)
	assert.True(t, hasHeader, "CompositeResolver should contain header resolver")

	// Should have subdomain resolver only if domain is specified
	if td.config.Multitenant.Resolver.Domain != "" {
		assert.True(t, hasSubdomain, "CompositeResolver should contain subdomain resolver when domain is specified")
	}
}
