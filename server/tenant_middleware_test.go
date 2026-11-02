package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/labstack/echo/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/config"
	"github.com/gaborage/go-bricks/logger"
	"github.com/gaborage/go-bricks/multitenant"
)

// newTenantTestEcho builds an *echo.Echo wired with customErrorHandler so an
// IAPIError returned by tenantMiddlewareEcho (e.g. *BadRequestError) renders
// its real HTTP status instead of Echo's default 500 for non-*echo.HTTPError
// errors. Mirrors the pattern in errors_test.go.
func newTenantTestEcho() *echo.Echo {
	e := echo.New()
	cfg := &config.Config{App: config.AppConfig{Env: config.EnvDevelopment}}
	e.HTTPErrorHandler = func(c *echo.Context, err error) {
		customErrorHandler(c, err, cfg, &testLogger{})
	}
	return e
}

// fixedTenantResolver is a minimal multitenant.TenantResolver test double that
// always returns the configured (tenantID, err) pair, regardless of the request.
type fixedTenantResolver struct {
	tenantID string
	err      error
}

func (r *fixedTenantResolver) ResolveTenant(_ context.Context, _ *http.Request) (string, error) {
	return r.tenantID, r.err
}

// TestTenantMiddlewareLogsRejection verifies a 400 tenant-resolution rejection
// emits one WARN through the provided framework logger, carrying the path and
// status. This is the audit trail for the observability-off blind spot:
// tenantMiddlewareEcho is registered outer to the access logger
// (server/middleware.go) and never calls next() on reject, so without this
// WARN the request leaves zero server-side trail. capturingLogger is defined
// in cors_test.go.
func TestTenantMiddlewareLogsRejection(t *testing.T) {
	tests := []struct {
		name     string
		resolver multitenant.TenantResolver
		reason   string
	}{
		{name: "empty_tenant", resolver: &fixedTenantResolver{tenantID: "", err: nil}, reason: `reason="empty tenant"`},
		{name: "resolver_error", resolver: &fixedTenantResolver{tenantID: "tenant-canary", err: errors.New("boom")}, reason: `reason="resolver error"`},
		{
			name:     "untrusted_forwarded_host",
			resolver: &fixedTenantResolver{err: fmt.Errorf("wrapped: %w", multitenant.ErrUntrustedForwardedHost)},
			reason:   `reason="X-Forwarded-Host from a peer outside server.trustedproxies"`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			capturer := &capturingLogger{}
			e := newTenantTestEcho()
			e.Use(tenantMiddlewareEcho(tc.resolver, nil, capturer))
			e.GET("/tenant-check", func(c *echo.Context) error {
				return c.String(http.StatusOK, "ok")
			})

			req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/tenant-check", http.NoBody)
			rec := httptest.NewRecorder()
			e.ServeHTTP(rec, req)

			require.Equal(t, http.StatusBadRequest, rec.Code)

			require.Len(t, capturer.warns, 1, "exactly one WARN per rejected request")
			captured := strings.Join(capturer.warns, "\n")
			assert.Contains(t, captured, `method="GET"`)
			assert.Contains(t, captured, `path="/tenant-check"`)
			assert.Contains(t, captured, `client="192.0.2.1"`)
			assert.Contains(t, captured, "status=400")
			assert.Contains(t, captured, tc.reason, "each resolver outcome logs its fixed reason")
			assert.NotContains(t, captured, "tenant=", "no resolved tenant field on the reject path")
			assert.NotContains(t, captured, "tenant-canary", "a resolver-provided tenant must never be logged on reject")
		})
	}
}

// TestTenantMiddlewareNilLoggerDoesNotPanic verifies the nil-logger path
// (public TenantMiddleware construction, which threads nil through to
// tenantMiddlewareEcho) still rejects with 400 and does not panic — guards
// against a future refactor reintroducing an unconditional l.Warn() call.
func TestTenantMiddlewareNilLoggerDoesNotPanic(t *testing.T) {
	resolver := &fixedTenantResolver{tenantID: "", err: nil}
	e := newTenantTestEcho()
	e.Use(tenantMiddlewareEcho(resolver, nil, nil))
	e.GET("/tenant-check", func(c *echo.Context) error {
		return c.String(http.StatusOK, "ok")
	})

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/tenant-check", http.NoBody)
	rec := httptest.NewRecorder()

	assert.NotPanics(t, func() {
		e.ServeHTTP(rec, req)
	})
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

// TestTenantRejectionLogEscapesRequestValues varies the attacker-written
// dimension of logTenantRejection: method, path and client IP must reach the
// log escaped on both logger paths.
func TestTenantRejectionLogEscapesRequestValues(t *testing.T) {
	assertRejectionLogEscapesRequestValues(t, func(l logger.Logger, c *echo.Context) {
		logTenantRejection(l, c, tenantRejectionReason(nil), c.Request().URL.Path)
	})
}

// TestTenantMiddlewareResolvesPercentEncodedProbePath verifies tenant resolution runs for a
// percent-encoded probe spelling (rejected as an invalid tenant without the header) while the
// genuine probe still bypasses it.
func TestTenantMiddlewareResolvesPercentEncodedProbePath(t *testing.T) {
	cfg := newTestConfig(probeSkipBase, "", "")
	cfg.Multitenant.Enabled = true
	cfg.Multitenant.Resolver.Type = config.ResolverTypeHeader
	srv := New(cfg, &testLogger{})
	srv.echo.GET(probeSkipModuleRoute, okEchoHandler)

	assertServeCode(t, srv.echo, http.MethodGet, probeSkipEncodedRdy, http.StatusBadRequest)
	assertServeCode(t, srv.echo, http.MethodGet, probeSkipReady, http.StatusOK)
}

// TestTenantMiddlewareHonorsConsumerSkipper pins that a SkipperFunc handed to the public
// constructor actually reaches the decision (skipperFromRequest): the probe route is exempt
// without a tenant header, while a module route reached through an encoded spelling is not.
func TestTenantMiddlewareHonorsConsumerSkipper(t *testing.T) {
	cfg := &config.Config{App: config.AppConfig{Env: "development"}}
	e := newTenantTestEcho()
	resolver := &multitenant.HeaderResolver{HeaderName: HeaderXTenantID}
	e.Use(adaptMiddleware(TenantMiddleware(resolver, CreateProbeSkipper("/health", "/ready")), cfg))
	e.GET("/health", func(c *echo.Context) error { return c.String(http.StatusOK, "ok") })
	e.GET("/:id", func(c *echo.Context) error { return c.String(http.StatusOK, "module") })

	assert.Equal(t, http.StatusOK, serveEngine(e, http.MethodGet, "/health").Code,
		"the consumer skipper must exempt the probe path")
	assert.Equal(t, http.StatusBadRequest, serveEngine(e, http.MethodGet, "/%68ealth").Code,
		"an encoded spelling routes to a module route, so tenant resolution still applies")
}

// TestTenantMiddlewareNilSkipperResolvesEveryRoute pins the nil-skipper contract of the public
// constructor: without a skipper nothing is exempt, so even a probe path resolves a tenant.
func TestTenantMiddlewareNilSkipperResolvesEveryRoute(t *testing.T) {
	e := newTenantTestEcho()
	e.Use(adaptMiddleware(TenantMiddleware(&multitenant.HeaderResolver{HeaderName: HeaderXTenantID}, nil), &config.Config{App: config.AppConfig{Env: "development"}}))
	e.GET("/health", func(c *echo.Context) error { return c.String(http.StatusOK, "ok") })

	assert.Equal(t, http.StatusBadRequest, serveEngine(e, http.MethodGet, "/health").Code,
		"a nil skipper exempts nothing, so the probe path still needs a tenant")

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/health", http.NoBody)
	req.Header.Set(HeaderXTenantID, "acme")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code, "with a tenant header the same request resolves")
}

// newTenantServer is a Server resolving the tenant from X-Tenant-ID, or from path segment 1.
func newTenantServer(resolverType string, testLog *testLogger) *Server {
	cfg := newTestConfig("", "", "")
	cfg.Multitenant.Enabled = true
	cfg.Multitenant.Resolver.Type = resolverType
	cfg.Multitenant.Resolver.Path.Segment = 1
	return New(cfg, testLog)
}

// serveTenant sends GET path for tenant and decodes the envelope.
func serveTenant(t *testing.T, srv *Server, path, tenant string) (int, APIResponse) {
	t.Helper()
	rec := httptest.NewRecorder()
	srv.echo.ServeHTTP(rec, observedRequest(http.MethodGet, path, tenant, nil))
	var resp APIResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp), rec.Body.String())
	return rec.Code, resp
}

type unknownTenantReq struct{}

func TestUnknownTenantRefusalLogsReasonClassOnly(t *testing.T) {
	storeErr := unknownTenantStoreErr(t)
	tests := []struct {
		name     string
		resolver string
		typed    bool
		cause    error
	}{
		{name: "raw_header_resolver", resolver: config.ResolverTypeHeader, cause: storeErr},
		{name: "raw_path_resolver", resolver: config.ResolverTypePath, cause: storeErr},
		{name: "typed_header_resolver", resolver: config.ResolverTypeHeader, typed: true, cause: storeErr},
		{name: "typed_path_resolver", resolver: config.ResolverTypePath, typed: true, cause: storeErr},
		{name: "typed_misused_door_logs_error", resolver: config.ResolverTypeHeader, typed: true, cause: errors.New("secrets manager: request timed out")},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			testLog := &testLogger{}
			srv := newTenantServer(tc.resolver, testLog)
			route, path := "/orders", "/orders"
			if tc.resolver == config.ResolverTypePath {
				route, path = "/:tenant/orders", "/"+unknownTenantID+"/orders"
			}
			if tc.typed {
				hr := NewHandlerRegistry(srv.cfg, WithLogger(testLog))
				GET(hr, srv.ModuleGroup(), route, func(unknownTenantReq, HandlerContext) (Result[string], IAPIError) {
					return Result[string]{}, NewUnknownTenantError(tc.cause)
				})
			} else {
				srv.ModuleGroup().Add(http.MethodGet, route, func(HandlerContext) error { return tc.cause })
			}

			status, _ := serveTenant(t, srv, path, unknownTenantID)

			warns, unhandled := splitTenantRefusalLogs(testLog.logEntries())
			if !errors.Is(tc.cause, multitenant.ErrUnknownTenant) {
				assert.Equal(t, http.StatusInternalServerError, status)
				assert.Empty(t, warns)
				assert.Len(t, unhandled, 1, "a misused door is a server fault, logged once")
				return
			}
			assert.Equal(t, http.StatusBadRequest, status)
			assert.Empty(t, unhandled, "an unknown tenant is not an unhandled error")
			require.Len(t, warns, 1, "exactly one WARN per refusal")
			assert.Contains(t, warns[0].msg, `reason="unknown tenant"`)
			assert.Contains(t, warns[0].msg, "status=400")
			assert.Contains(t, warns[0].msg, fmt.Sprintf("path=%q", route), "the route template, never the URL")
			for _, field := range append([]string{warns[0].msg}, slices.Collect(maps.Values(warns[0].values))...) {
				assert.NotContains(t, field, unknownTenantID, "the tenant ID never reaches the line")
				assert.NotContains(t, field, "configuration not found", "the error text never reaches the line")
			}
		})
	}
}

// splitTenantRefusalLogs splits entries into tenant-refusal WARNs and unhandled-error lines.
func splitTenantRefusalLogs(entries []testLogEntry) (warns, unhandled []testLogEntry) {
	for _, e := range entries {
		if e.level == "warn" && strings.HasPrefix(e.msg, "[server.tenant]") {
			warns = append(warns, e)
		}
		if e.msg == "unhandled error" {
			unhandled = append(unhandled, e)
		}
	}
	return warns, unhandled
}

// TestUnknownTenantRefusalNilLoggerFallsBackToStdlib covers public WrapHandler, which threads
// a nil logger: a misused door still leaves its unhandled-error line, type only.
func TestUnknownTenantRefusalNilLoggerFallsBackToStdlib(t *testing.T) {
	var buf bytes.Buffer
	prev := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(prev) })
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/orders", http.NoBody)
	c := echo.New().NewContext(req, httptest.NewRecorder())

	logUnknownTenantRefusal(NewUnknownTenantError(errors.New("dial tcp 10.0.0.7: refused")), c, nil, false)

	assert.Contains(t, buf.String(), "ERROR unhandled error")
	assert.Contains(t, buf.String(), "error_type=*errors.errorString")
	assert.NotContains(t, buf.String(), "10.0.0.7", "outside debug the error text stays out")
}

// TestTenantSeatRefusesOffGrammarID pins the seat built by New: a header-resolved ID
// outside the tenant-ID grammar is refused before any module route runs.
func TestTenantSeatRefusesOffGrammarID(t *testing.T) {
	cfg := newTestConfig("", "", "")
	cfg.Multitenant.Enabled = true
	cfg.Multitenant.Resolver.Type = config.ResolverTypeHeader
	srv := New(cfg, &testLogger{})
	srv.echo.GET("/api/orders", okEchoHandler)

	tests := []struct {
		name       string
		tenant     string
		wantStatus int
	}{
		{name: "uppercase", tenant: "Acme", wantStatus: http.StatusBadRequest},
		{name: "underscore", tenant: "acme_1", wantStatus: http.StatusBadRequest},
		{name: "dot", tenant: "acme.prod", wantStatus: http.StatusBadRequest},
		{name: "colon", tenant: "named:primary", wantStatus: http.StatusBadRequest},
		{name: "slash", tenant: "acme/prod", wantStatus: http.StatusBadRequest},
		{name: "over_64_bytes", tenant: strings.Repeat("a", 65), wantStatus: http.StatusBadRequest},
		{name: "max_64_bytes", tenant: strings.Repeat("a", 64), wantStatus: http.StatusOK},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()

			srv.echo.ServeHTTP(rec, observedRequest(http.MethodGet, "/api/orders", tt.tenant, nil))

			require.Equal(t, tt.wantStatus, rec.Code)
			if tt.wantStatus == http.StatusOK {
				return
			}
			var resp APIResponse
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
			require.NotNil(t, resp.Error)
			assert.Equal(t, "BAD_REQUEST", resp.Error.Code)
			assert.Equal(t, "Invalid tenant", resp.Error.Message)
		})
	}
}
