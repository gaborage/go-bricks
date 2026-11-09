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
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/labstack/echo/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/gaborage/go-bricks/config"
	"github.com/gaborage/go-bricks/internal/testutil"
	"github.com/gaborage/go-bricks/logger"
	"github.com/gaborage/go-bricks/multitenant"
)

// newTenantTestEcho builds an *echo.Echo wired with customErrorHandler so an
// IAPIError returned by tenantMiddlewareEcho (e.g. *BadRequestError) renders
// its real HTTP status instead of Echo's default 500 for non-*echo.HTTPError
// errors. Mirrors the pattern in errors_test.go.
func newTenantTestEcho() *echo.Echo {
	e := newEngine()
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
			e.Use(tenantMiddlewareEcho(tc.resolver, nil, seatInputs{}, capturer))
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
	e.Use(tenantMiddlewareEcho(resolver, nil, seatInputs{}, nil))
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
// dimension of logTenantRejection: the client IP must reach the log escaped on
// both logger paths, and the request path not at all.
func TestTenantRejectionLogEscapesRequestValues(t *testing.T) {
	assertRejectionLogEscapesRequestValues(t, func(l logger.Logger, c *echo.Context) {
		logTenantRejection(l, c, tenantRejectionReason(nil))
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
	e.Use(adaptMiddleware(TenantMiddleware(resolver, CreateProbeSkipper("/health", "/ready"), nil), cfg))
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
	e.Use(adaptMiddleware(TenantMiddleware(&multitenant.HeaderResolver{HeaderName: HeaderXTenantID}, nil, nil), &config.Config{App: config.AppConfig{Env: "development"}}))
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
			route, path, masked := "/orders", "/orders", "/orders"
			if tc.resolver == config.ResolverTypePath {
				route, path, masked = "/:tenant/orders", "/"+unknownTenantID+"/orders", "/"+maskToken+"/orders"
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
			assert.Contains(t, warns[0].msg, fmt.Sprintf("path=%q", masked), "the masked route template, never the URL")
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
	c := newEngine().NewContext(req, httptest.NewRecorder())

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

// TestTenantSeatSkipsOwnedPrefix pins, through New with the header resolver, that the tenant
// seat skips exactly the requests whose matched route template a tenant-owned family owns,
// leaving seatSkippedKey for the family to find, and resolves every other route as before.
func TestTenantSeatSkipsOwnedPrefix(t *testing.T) {
	const (
		familyID = "partner-tenant"
		seatID   = "acme"
	)
	tests := []struct {
		name       string
		path       string
		seatHeader string
		wantStatus int
		wantFamily bool   // the family's Tenant step ran and found the seat's evidence
		wantBody   string // the tenant the handler saw, when one answered
	}{
		{name: "static_route", path: "/partner/v2/orders", wantStatus: http.StatusOK, wantFamily: true, wantBody: familyID},
		{name: "param_route", path: "/partner/v2/orders/42", wantStatus: http.StatusOK, wantFamily: true, wantBody: familyID},
		{name: "wildcard_route", path: "/partner/v2/files/a/b.txt", wantStatus: http.StatusOK, wantFamily: true, wantBody: familyID},
		// The seat keys on the matched template: a spelling no route registers lands in the
		// family's catch-all, and the family, not the seat, answers 404.
		{name: "percent_encoded_spelling", path: "/partner/v2/%6Frders", wantStatus: http.StatusNotFound, wantFamily: true},
		{name: "bare_prefix_route", path: "/partner/v2", wantStatus: http.StatusOK, wantFamily: true, wantBody: familyID},
		{name: "unknown_sub_path", path: "/partner/v2/nowhere", wantStatus: http.StatusNotFound, wantFamily: true},
		{name: "segment_sibling_not_skipped", path: "/partner/v20", wantStatus: http.StatusBadRequest},
		{name: "non_family_route_resolves", path: "/orders", seatHeader: seatID, wantStatus: http.StatusOK, wantBody: seatID},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := newTenantFamilyServer(t, "")
			testLog, ok := srv.logger.(*testLogger)
			require.True(t, ok)

			var familyRan, evidence bool
			policy := TenantPolicy{Tenant: func(c HandlerContext) (string, error) {
				familyRan = true
				evidence, _ = c.Get(seatSkippedKey).(bool)
				return familyID, nil
			}}
			family := TenantOwnedGroup(srv.ModuleGroup(), "/partner/v2", policy)
			for _, path := range []string{"", "/orders", "/orders/:id", "/files/*"} {
				family.Add(http.MethodGet, path, echoTenant)
			}
			srv.ModuleGroup().Add(http.MethodGet, "/orders", echoTenant)
			srv.ModuleGroup().Add(http.MethodGet, "/partner/v20", echoTenant)

			rec := requireObservedStatus(t, srv, observedRequest(http.MethodGet, tc.path, tc.seatHeader, nil), tc.wantStatus)
			assert.Equal(t, tc.wantFamily, familyRan, "the family's own steps run exactly on its routes")
			assert.Equal(t, tc.wantFamily, evidence, "the seat leaves its evidence exactly where it skipped")
			if tc.wantBody != "" {
				assert.Equal(t, tc.wantBody, rec.Body.String())
			}
			wantWarns := 0
			if tc.wantStatus == http.StatusBadRequest {
				wantWarns = 1
			}
			warns, _ := splitTenantRefusalLogs(testLog.logEntries())
			assert.Len(t, warns, wantWarns, "the seat answers, and logs, only a route no family owns")
		})
	}
}

// TestTenantRejectionCorrelatesItsLine pins the seat's rejection WARN to the request on both
// logger paths.
func TestTenantRejectionCorrelatesItsLine(t *testing.T) {
	seat := func(l logger.Logger) echo.MiddlewareFunc {
		return tenantMiddlewareEcho(&fixedTenantResolver{}, nil, seatInputs{}, l)
	}
	t.Run("framework_logger", func(t *testing.T) {
		l := &testLogger{}
		serveCorrelated(t, seat(l), correlatedRequest(t, true))
		entries := l.logEntries()
		require.Len(t, entries, 1)
		assert.Contains(t, entries[0].msg, "[server.tenant] request rejected")
		assertLineCorrelated(t, entries[0])
	})
	t.Run("nil_logger_stdlib_fallback", func(t *testing.T) {
		buf := captureStdlibLog(t)
		serveCorrelated(t, seat(nil), correlatedRequest(t, true))
		line := strings.TrimSuffix(buf.String(), "\n")
		assert.True(t, strings.HasSuffix(line, `reason="empty tenant"`+correlationSuffix), "got %q", line)
	})
	t.Run("disabled_warn_level_formats_nothing", func(t *testing.T) {
		dl := &disabledLogger{ev: &disabledEvent{}}
		reads := serveCorrelated(t, seat(dl), correlatedRequest(t, true))
		assert.Zero(t, reads, "a disabled WARN level must not read the client address")
		assert.False(t, dl.ev.msgCalled)
		assert.Equal(t, int32(1), serveCorrelated(t, seat(&testLogger{}), correlatedRequest(t, true)),
			"an enabled WARN reads the client address once")
	})
}

// echoTenant answers 200 with the tenant on the request context, "" for none.
func echoTenant(c HandlerContext) error {
	tenantID, _ := multitenant.GetTenant(c.RequestContext())
	return c.String(http.StatusOK, tenantID)
}

// embeddedHeaderResolver is a consumer resolver type multitenant does not define, so the
// door cannot tell what it reads.
type embeddedHeaderResolver struct {
	*multitenant.HeaderResolver
}

// TestTenantMiddlewareRequiresCheckerForPathReadingResolver pins that the public door panics at
// construction without a tenant existence check whenever its resolver may read the request
// path, and builds without one for the framework's header and subdomain resolvers.
func TestTenantMiddlewareRequiresCheckerForPathReadingResolver(t *testing.T) {
	path := &multitenant.PathResolver{Segment: 2}
	header := &multitenant.HeaderResolver{HeaderName: HeaderXTenantID}
	subdomain := &multitenant.SubdomainResolver{RootDomain: "example.com"}
	tests := []struct {
		name      string
		resolver  multitenant.TenantResolver
		wantPanic bool
	}{
		{name: "path_resolver", resolver: path, wantPanic: true},
		{name: "validating_path", resolver: &multitenant.ValidatingResolver{Resolver: path}, wantPanic: true},
		{name: "composite_with_path", resolver: &multitenant.CompositeResolver{Resolvers: []multitenant.TenantResolver{header, path}}, wantPanic: true},
		{name: "consumer_type_embedding_header", resolver: embeddedHeaderResolver{header}, wantPanic: true},
		{name: "header_resolver", resolver: header},
		{name: "subdomain_resolver", resolver: subdomain},
		{name: "composite_header_subdomain", resolver: &multitenant.CompositeResolver{Resolvers: []multitenant.TenantResolver{header, subdomain}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			build := func() { TenantMiddleware(tt.resolver, nil, nil) }
			if !tt.wantPanic {
				assert.NotPanics(t, build)
				return
			}
			assert.PanicsWithValue(t, "server: TenantMiddleware: the resolver may read the request path, so a tenant existence check is required (ADR-172): "+
				"pass the multitenant.TenantChecker that serves your tenants' resources", build)
		})
	}
}

// countingChecker answers true for every non-empty id and counts the calls for one. The
// construction-time probe asks about "" and is not counted.
type countingChecker struct{ calls int }

func (c *countingChecker) HasTenant(tenantID string) bool {
	if tenantID == "" {
		return false
	}
	c.calls++
	return true
}

// newPublicDoorGroup builds an engine with one route group running the before middlewares,
// then the public TenantMiddleware with resolver and exists.
func newPublicDoorGroup(resolver multitenant.TenantResolver, exists multitenant.TenantChecker, before ...MiddlewareFunc) (*echo.Echo, RouteRegistrar) {
	e := newTenantTestEcho()
	rg := newRouteGroup(e.Group(""), "", &config.Config{App: config.AppConfig{Env: config.EnvDevelopment}})
	for _, mw := range before {
		rg.Use(mw)
	}
	rg.Use(TenantMiddleware(resolver, nil, exists))
	return e, rg
}

// servePublicDoor registers GET /tenants/:tenant/x under newPublicDoorGroup and serves req
// through it. It reports the status, the tenant the handler saw ("" when it did not run or saw
// none) and whether the handler ran.
func servePublicDoor(resolver multitenant.TenantResolver, exists multitenant.TenantChecker, req *http.Request, before ...MiddlewareFunc) (status int, seen string, ran bool) {
	e, rg := newPublicDoorGroup(resolver, exists, before...)
	rg.Add(http.MethodGet, "/tenants/:tenant/x", func(c HandlerContext) error {
		ran = true
		seen, _ = multitenant.GetTenant(c.RequestContext())
		return c.String(http.StatusOK, "ok")
	})
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	return rec.Code, seen, ran
}

// doorRequest is a GET of target.
func doorRequest(target string) *http.Request {
	return httptest.NewRequestWithContext(context.Background(), http.MethodGet, target, http.NoBody)
}

// TestTenantMiddlewareChecksPathReadTenant pins that the public door writes a path-read tenant
// only when its check holds it: a held id reaches the handler, and any other id answers 400
// from the handler adapter with no tenant written. An empty store is accepted at construction,
// since a consumer may fill it after wiring, and then holds no tenant. A consumer resolver type
// keeps its own refusal (400 at the door), and an id it allows is still asked of the check.
func TestTenantMiddlewareChecksPathReadTenant(t *testing.T) {
	path := &multitenant.PathResolver{Segment: 2}
	claimT1 := claimBoundResolver{PathResolver: path, claim: "t1"}
	tests := []struct {
		name       string
		resolver   multitenant.TenantResolver
		exists     multitenant.TenantChecker
		target     string
		wantStatus int
		wantSeen   string
	}{
		{name: "held_tenant", resolver: path, exists: testutil.TenantSet{"t1": {}}, target: "/tenants/t1/x", wantStatus: http.StatusOK, wantSeen: "t1"},
		{name: "unheld_tenant", resolver: path, exists: testutil.TenantSet{"t1": {}}, target: "/tenants/t2/x", wantStatus: http.StatusBadRequest},
		{name: "empty_tenant_store", resolver: path, exists: config.NewTenantStore(&config.Config{}), target: "/tenants/t1/x", wantStatus: http.StatusBadRequest},
		{name: "outside_claim", resolver: claimT1, exists: testutil.TenantSet{"t1": {}, "t2": {}}, target: "/tenants/t2/x", wantStatus: http.StatusBadRequest},
		{name: "claimed_and_held", resolver: claimT1, exists: testutil.TenantSet{"t1": {}}, target: "/tenants/t1/x", wantStatus: http.StatusOK, wantSeen: "t1"},
		{name: "claimed_not_held", resolver: claimT1, exists: testutil.TenantSet{"t2": {}}, target: "/tenants/t1/x", wantStatus: http.StatusBadRequest},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			status, seen, _ := servePublicDoor(tt.resolver, tt.exists, doorRequest(tt.target))
			assert.Equal(t, tt.wantStatus, status)
			assert.Equal(t, tt.wantSeen, seen)
		})
	}
}

// TestTenantMiddlewareSeatsOnlyPlainTenantKeys pins that a path segment carrying the named
// database prefix is never treated as a tenant: it answers 400 even under a check that would
// answer true, and the check is not consulted.
func TestTenantMiddlewareSeatsOnlyPlainTenantKeys(t *testing.T) {
	check := &countingChecker{}
	status, seen, _ := servePublicDoor(&multitenant.PathResolver{Segment: 2}, check, doorRequest("/tenants/"+config.NamedDatabasePrefix+"reports/x"))
	assert.Equal(t, http.StatusBadRequest, status)
	assert.Empty(t, seen)
	assert.Zero(t, check.calls, "the check is not asked about a prefixed key")
}

// TestTenantMiddlewareRefusesUnknownPathTenantBeforeHandler pins, through server.New with
// observability on, that an id the public door's check does not hold is never written: the
// module middleware after the door runs with no tenant, the handler never runs, the client
// gets 400, and the segment appears on no log line, in no response body and in no span, in its
// sent or its decoded spelling. Escaped and padded segments are refused the same way.
func TestTenantMiddlewareRefusesUnknownPathTenantBeforeHandler(t *testing.T) {
	const decoded = "zeta-tenant" // what the escaped and padded segments spell once decoded
	tests := []struct {
		name    string
		segment string   // the segment as sent in the URL
		needles []string // spellings that must reach no sink
	}{
		{name: "unlisted_segment_refused", segment: "unlisted-path-tenant", needles: []string{"unlisted-path-tenant"}},
		{name: "escaped_segment_refused", segment: "%7Aeta-tenant", needles: []string{"%7Aeta-tenant", decoded}},
		{name: "padded_segment_refused", segment: "%20" + decoded, needles: []string{"%20" + decoded, decoded}},
		{name: "newline_padded_segment_refused", segment: "%0A" + decoded, needles: []string{"%0A" + decoded, decoded}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			door := TenantMiddleware(&multitenant.PathResolver{Segment: 2}, nil, testutil.TenantSet{"t1": {}})
			got := serveModuleDoor(t, door, doorRequest("/tenants/"+tt.segment+"/x"))
			got.assertRefused(t, tt.needles...)
		})
	}
}

// TestTenantMiddlewareRefusesInvalidHeaderTenant pins, through server.New with observability on,
// that every door validates the tenant-ID grammar and refuses a reserved-prefix id, whatever the
// resolver: a bare header resolver's value that fails the grammar or carries the reserved prefix
// answers 400, module middleware and the handler see no tenant, and the value reaches no sink.
// A resolver that is already a ValidatingResolver keeps its own grammar, and a reserved-prefix
// id is refused even when that grammar admits it.
func TestTenantMiddlewareRefusesInvalidHeaderTenant(t *testing.T) {
	const headerName = "X-Door-Tenant"
	reserved := config.NamedDatabasePrefix + "reports"
	header := &multitenant.HeaderResolver{HeaderName: headerName}
	permissive := &multitenant.ValidatingResolver{Resolver: header, TenantRegex: regexp.MustCompile(`^[A-Za-z0-9_:-]+$`)}
	tests := []struct {
		name     string
		resolver multitenant.TenantResolver
		value    string
		wantSeen string // "" when the request must be refused
	}{
		{name: "plain_header_tenant_seated", resolver: header, value: "t1", wantSeen: "t1"},
		{name: "grammar_failing_header_refused", resolver: header, value: "Bad_Tenant"},
		{name: "reserved_prefix_header_refused", resolver: header, value: reserved},
		{name: "own_grammar_kept", resolver: permissive, value: "Bad_Tenant", wantSeen: "Bad_Tenant"},
		{name: "reserved_prefix_under_own_grammar_refused", resolver: permissive, value: reserved},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := doorRequest("/tenants/x")
			req.Header.Set(headerName, tt.value)
			got := serveModuleDoor(t, TenantMiddleware(tt.resolver, nil, nil), req)
			if tt.wantSeen != "" {
				assert.Equal(t, http.StatusOK, got.rec.Code)
				assert.Equal(t, []string{tt.wantSeen}, got.middlewareSaw)
				return
			}
			got.assertRefused(t, tt.value)
		})
	}
}

// moduleDoorResult is what serveModuleDoor observed for one request.
type moduleDoorResult struct {
	rec           *httptest.ResponseRecorder
	testLog       *testLogger
	spans         tracetest.SpanStubs
	middlewareSaw []string
	handlerRan    bool
}

// serveModuleDoor serves req through server.New with observability on, under a module group
// /tenants running door and then a module middleware that records the tenant it sees.
func serveModuleDoor(t *testing.T, door MiddlewareFunc, req *http.Request) moduleDoorResult {
	t.Helper()
	exporter := installTestTracerProvider(t)
	got := moduleDoorResult{testLog: &testLogger{}}
	srv := newAccessLogServer(t, got.testLog, true)

	moduleMiddleware := func(c HandlerContext, next func() error) error {
		if id, ok := multitenant.GetTenant(c.RequestContext()); ok {
			got.middlewareSaw = append(got.middlewareSaw, id)
		}
		return next()
	}
	g := srv.ModuleGroup().Group("/tenants", door, moduleMiddleware)
	handler := func(c HandlerContext) error {
		got.handlerRan = true
		return c.String(http.StatusOK, "ok")
	}
	g.Add(http.MethodGet, "/x", handler)
	g.Add(http.MethodGet, "/:tenant/x", handler)

	got.rec = httptest.NewRecorder()
	srv.echo.ServeHTTP(got.rec, req)
	got.spans = exporter.GetSpans()
	return got
}

// assertRefused fails unless the request answered 400 before the handler, module middleware saw
// no tenant, and no needle reached any sink.
func (got moduleDoorResult) assertRefused(t *testing.T, needles ...string) {
	t.Helper()
	assert.Equal(t, http.StatusBadRequest, got.rec.Code)
	assert.False(t, got.handlerRan, "the handler never runs")
	assert.Empty(t, got.middlewareSaw, "module middleware runs with no tenant written")
	require.NotEmpty(t, got.spans)
	for _, needle := range needles {
		assertNoSinkCarries(t, needle, got.rec, got.testLog, got.spans)
	}
}

// assertNoSinkCarries fails when needle appears in the response body, any log line or field,
// or any span status, attribute or event attribute.
func assertNoSinkCarries(t *testing.T, needle string, rec *httptest.ResponseRecorder, testLog *testLogger, spans tracetest.SpanStubs) {
	t.Helper()
	assert.NotContains(t, rec.Body.String(), needle)
	for _, entry := range testLog.logEntries() {
		assert.NotContains(t, entry.msg, needle)
		for k, v := range entry.values {
			assert.NotContains(t, v, needle, "log field %s", k)
		}
	}
	for i := range spans {
		span := &spans[i]
		assert.NotContains(t, span.Status.Description, needle)
		for _, attr := range span.Attributes {
			assert.NotContains(t, attr.Value.String(), needle, "span attribute %s", attr.Key)
		}
		for _, ev := range span.Events {
			for _, attr := range ev.Attributes {
				assert.NotContains(t, attr.Value.String(), needle, "span event attribute %s", attr.Key)
			}
		}
	}
}

// claimBoundResolver is a consumer resolver that reads the path through an embedded
// *multitenant.PathResolver and refuses any id other than the caller's claim.
type claimBoundResolver struct {
	*multitenant.PathResolver
	claim string
}

func (r claimBoundResolver) ResolveTenant(ctx context.Context, req *http.Request) (string, error) {
	id, err := r.PathResolver.ResolveTenant(ctx, req)
	if err != nil {
		return "", err
	}
	if id != r.claim {
		return "", errors.New("tenant outside the caller's claim")
	}
	return id, nil
}

// TestTenantMiddlewareRateLimitKeysOnClient pins server.RateLimit behind the public door: one
// client's budget is shared by every path tenant it names, held or not, so a drained client
// gets the same 429 for a held and an unheld tenant.
func TestTenantMiddlewareRateLimitKeysOnClient(t *testing.T) {
	e, rg := newPublicDoorGroup(&multitenant.PathResolver{Segment: 2}, testutil.TenantSet{"t1": {}})
	rg.Use(RateLimit(1))
	rg.Add(http.MethodGet, "/tenants/:tenant/x", func(c HandlerContext) error { return c.String(http.StatusOK, "ok") })

	serve := func(tenant string) *httptest.ResponseRecorder {
		req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/tenants/"+tenant+"/x", http.NoBody)
		req.RemoteAddr = "203.0.113.7:4000"
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)
		return rec
	}
	require.Equal(t, http.StatusOK, serve("t1").Code)
	require.Equal(t, http.StatusBadRequest, serve("t2").Code, "the unheld tenant spends the same client budget")

	held, unheld := serve("t1"), serve("t2")
	assert.Equal(t, http.StatusTooManyRequests, held.Code)
	assert.Equal(t, http.StatusTooManyRequests, unheld.Code)
	for _, h := range []string{"X-Ratelimit-Limit", "X-Ratelimit-Remaining", "Retry-After"} {
		assert.Equal(t, held.Header().Values(h), unheld.Header().Values(h), h)
	}
}

// TestTenantMiddlewareRefusesConflictingSeatedTenant pins the public door behind a tenant that is
// already in context: an id that agrees passes, and one the check refuses or that names another
// tenant answers 400 from the handler adapter, with the seated tenant neither kept for the
// handler nor overwritten.
func TestTenantMiddlewareRefusesConflictingSeatedTenant(t *testing.T) {
	held := testutil.TenantSet{"acme": {}, "t1": {}}
	seatAcme := func(c HandlerContext, next func() error) error {
		c.SetRequestContext(multitenant.SetTenant(c.RequestContext(), "acme"))
		return next()
	}
	tests := []struct {
		name       string
		resolver   multitenant.TenantResolver
		target     string
		header     string
		wantStatus int
		wantSeen   string
	}{
		{name: "same_path_tenant", resolver: &multitenant.PathResolver{Segment: 2}, target: "/tenants/acme/x", wantStatus: http.StatusOK, wantSeen: "acme"},
		{name: "unheld_path_tenant", resolver: &multitenant.PathResolver{Segment: 2}, target: "/tenants/t9/x", wantStatus: http.StatusBadRequest},
		{name: "other_held_path_tenant", resolver: &multitenant.PathResolver{Segment: 2}, target: "/tenants/t1/x", wantStatus: http.StatusBadRequest},
		{name: "other_header_tenant", resolver: &multitenant.HeaderResolver{HeaderName: "X-Door-Tenant"}, target: "/tenants/acme/x", header: "t1", wantStatus: http.StatusBadRequest},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := doorRequest(tt.target)
			if tt.header != "" {
				req.Header.Set("X-Door-Tenant", tt.header)
			}
			status, seen, ran := servePublicDoor(tt.resolver, held, req, seatAcme)
			assert.Equal(t, tt.wantStatus, status)
			assert.Equal(t, tt.wantSeen, seen)
			assert.Equal(t, tt.wantStatus == http.StatusOK, ran, "the handler runs only when the tenants agree")
		})
	}
}

// TestTenantMiddlewareRefusesCheckerThatCannotAnswer pins that the public door probes a non-nil
// check at construction and panics with the probe's text when the check is a nil pointer, panics,
// or answers true for the empty id; an empty store passes the probe.
func TestTenantMiddlewareRefusesCheckerThatCannotAnswer(t *testing.T) {
	path := &multitenant.PathResolver{Segment: 2}
	header := &multitenant.HeaderResolver{HeaderName: HeaderXTenantID}
	tests := []struct {
		name      string
		resolver  multitenant.TenantResolver
		exists    multitenant.TenantChecker
		wantPanic string
	}{
		{name: "nil_tenant_store", resolver: path, exists: (*config.TenantStore)(nil), wantPanic: vetNilStoreText},
		{name: "true_for_empty", resolver: path, exists: everyTenantChecker{}, wantPanic: vetTrueText},
		{name: "panicking_check", resolver: header, exists: funcTenantChecker(nil), wantPanic: vetPanicText},
		{name: "empty_tenant_store", resolver: path, exists: config.NewTenantStore(&config.Config{})},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			build := func() { TenantMiddleware(tt.resolver, nil, tt.exists) }
			if tt.wantPanic == "" {
				assert.NotPanics(t, build)
				return
			}
			assert.PanicsWithValue(t, "server: TenantMiddleware: "+tt.wantPanic, build)
		})
	}
}

// TestTenantRejectionRendersUnknownMethodAsOther pins the semconv known-method rule on the
// tenant-rejection WARN: a method outside the known set prints `_OTHER`.
func TestTenantRejectionRendersUnknownMethodAsOther(t *testing.T) {
	assertLineRendersMethodOther(t, func(l logger.Logger, c *echo.Context) { logTenantRejection(l, c, "empty tenant") })
}
