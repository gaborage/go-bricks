package server

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/labstack/echo/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/config"
	"github.com/gaborage/go-bricks/internal/pathutil"
	"github.com/gaborage/go-bricks/jose"
	jositest "github.com/gaborage/go-bricks/jose/testing"
	"github.com/gaborage/go-bricks/multitenant"
	gobrickstrace "github.com/gaborage/go-bricks/trace"
)

// familySeatTenant is the X-Tenant-ID every serveFamily request carries; the seat skips a
// family's routes, so it resolves it only off them.
const familySeatTenant = "seat-tenant"

// newTenantFamilyConfig is newProbeTestConfig with multi-tenant mode on, resolving the tenant
// from X-Tenant-ID.
func newTenantFamilyConfig(basePath string) *config.Config {
	cfg := newProbeTestConfig(basePath)
	cfg.Multitenant.Enabled = true
	cfg.Multitenant.Resolver.Type = config.ResolverTypeHeader
	cfg.Multitenant.Resolver.Header = HeaderXTenantID
	return cfg
}

// fixedTenantPolicy is a TenantPolicy whose Tenant always resolves id.
func fixedTenantPolicy(id string) TenantPolicy {
	return TenantPolicy{Tenant: func(HandlerContext) (string, error) { return id, nil }}
}

// newTenantFamilyServer builds a Server from newTenantFamilyConfig, clearing
// DefaultRouteRegistry before and after the test.
func newTenantFamilyServer(t *testing.T, basePath string) *Server {
	t.Helper()
	return newTenantFamilyServerWithConfig(t, newTenantFamilyConfig(basePath))
}

// newTenantFamilyServerWithConfig is newTenantFamilyServer with the config supplied by the caller.
func newTenantFamilyServerWithConfig(t *testing.T, cfg *config.Config) *Server {
	t.Helper()
	DefaultRouteRegistry.Clear()
	t.Cleanup(DefaultRouteRegistry.Clear)
	return New(cfg, &testLogger{})
}

// serveFamilyRequest sends GET path through srv's engine carrying the seat tenant.
func serveFamilyRequest(srv *Server, path string) *httptest.ResponseRecorder {
	return serveFamily(srv, http.MethodGet, path)
}

// serveFamily sends method path through srv's engine carrying the seat tenant; each edit
// adjusts the request first.
func serveFamily(srv *Server, method, path string, edits ...func(*http.Request)) *httptest.ResponseRecorder {
	req := httptest.NewRequestWithContext(context.Background(), method, path, http.NoBody)
	req.Header.Set(HeaderXTenantID, familySeatTenant)
	for _, edit := range edits {
		edit(req)
	}
	rec := httptest.NewRecorder()
	srv.echo.ServeHTTP(rec, req)
	return rec
}

// withFamilyCredential sends the credential fakeBearerStep accepts; a request without it is
// anonymous.
func withFamilyCredential(req *http.Request) {
	req.Header.Set(echo.HeaderAuthorization, "Bearer family-test")
}

// withFamilyBody sends body with contentType.
func withFamilyBody(contentType, body string) func(*http.Request) {
	return func(req *http.Request) {
		req.Body = io.NopCloser(strings.NewReader(body))
		req.ContentLength = int64(len(body))
		req.Header.Set(echo.HeaderContentType, contentType)
	}
}

// answerWith is a Handler answering 200 with body.
func answerWith(body string) Handler {
	return func(c HandlerContext) error { return c.String(http.StatusOK, body) }
}

func TestTenantOwnedGroupIgnoresParentMiddleware(t *testing.T) {
	tests := []struct {
		name string
		// build returns the parent group and the family built under it.
		build func(srv *Server, parentMW MiddlewareFunc) (parent, family RouteRegistrar)
	}{
		{
			name: "parent_middleware_before_family",
			build: func(srv *Server, parentMW MiddlewareFunc) (parent, family RouteRegistrar) {
				parent = srv.ModuleGroup().Group("/p", parentMW)
				return parent, tenantOwnedGroup(parent, "/fam", fixedTenantPolicy("family-tenant"))
			},
		},
		{
			name: "parent_use_after_family",
			build: func(srv *Server, parentMW MiddlewareFunc) (parent, family RouteRegistrar) {
				parent = srv.ModuleGroup().Group("/p")
				family = tenantOwnedGroup(parent, "/fam", fixedTenantPolicy("family-tenant"))
				parent.Use(parentMW)
				return parent, family
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := newTenantFamilyServer(t, "")
			parentRan := false
			parentMW := func(_ HandlerContext, next func() error) error {
				parentRan = true
				return next()
			}
			parent, family := tt.build(srv, parentMW)
			family.Add(http.MethodGet, "/orders", answerWith("family"))
			parent.Add(http.MethodGet, "/own", answerWith("parent"))

			rec := serveFamilyRequest(srv, "/p/fam/orders")
			assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			assert.Equal(t, "family", rec.Body.String())
			assert.False(t, parentRan, "a parent group's middleware must never run on a tenant-owned family route")

			rec = serveFamilyRequest(srv, "/p/own")
			assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			assert.True(t, parentRan, "control: the parent middleware runs on the parent's own route")
		})
	}
}

func TestTenantOwnedGroupPanicsAtCallSite(t *testing.T) {
	policy := fixedTenantPolicy("family-tenant")
	q := strconv.Quote
	// existingFamily is a server already holding the tenant-owned family /partner/v2.
	existingFamily := func(t *testing.T) *Server {
		srv := newTenantFamilyServer(t, "")
		tenantOwnedGroup(srv.ModuleGroup(), "/partner/v2", policy)
		return srv
	}
	tests := []struct {
		name string
		// fixture returns the refused call and the Server backing it (nil when none does).
		fixture func(t *testing.T) (call func(), srv *Server)
		want    []string // substrings of the panic message, the full prefix first
	}{
		{
			name: "non_framework_registrar",
			fixture: func(*testing.T) (func(), *Server) {
				bare := newRouteGroup(echo.New().Group(""), "", newTenantFamilyConfig(""))
				return func() { tenantOwnedGroup(bare, "/fam", policy) }, nil
			},
			want: []string{q("/fam")},
		},
		{
			name: "nil_tenant",
			fixture: func(t *testing.T) (func(), *Server) {
				srv := newTenantFamilyServer(t, "")
				return func() { tenantOwnedGroup(srv.ModuleGroup(), "/fam", TenantPolicy{}) }, srv
			},
			want: []string{q("/fam")},
		},
		{
			name: "nested_in_family",
			fixture: func(t *testing.T) (func(), *Server) {
				srv := newTenantFamilyServer(t, "")
				sub := tenantOwnedGroup(srv.ModuleGroup(), "/fam", policy).Group("/sub")
				return func() { tenantOwnedGroup(sub, "/inner", policy) }, srv
			},
			want: []string{q("/fam/sub/inner"), q("/fam")},
		},
		{
			name: "empty_prefix",
			fixture: func(t *testing.T) (func(), *Server) {
				srv := newTenantFamilyServer(t, "")
				parent := srv.ModuleGroup().Group("/orders")
				return func() { tenantOwnedGroup(parent, "", policy) }, srv
			},
			want: []string{q("/orders")},
		},
		{
			name: "root_prefix",
			fixture: func(t *testing.T) (func(), *Server) {
				srv := newTenantFamilyServer(t, "")
				parent := srv.ModuleGroup().Group("/orders")
				return func() { tenantOwnedGroup(parent, "/", policy) }, srv
			},
			want: []string{q("/orders")},
		},
		{
			name: "param_segment",
			fixture: func(t *testing.T) (func(), *Server) {
				srv := newTenantFamilyServer(t, "")
				return func() { tenantOwnedGroup(srv.ModuleGroup(), "/orgs/:org", policy) }, srv
			},
			want: []string{q("/orgs/:org")},
		},
		{
			name: "wildcard_segment",
			fixture: func(t *testing.T) (func(), *Server) {
				srv := newTenantFamilyServer(t, "")
				return func() { tenantOwnedGroup(srv.ModuleGroup(), "/files/*", policy) }, srv
			},
			want: []string{q("/files/*")},
		},
		{
			name: "covers_health_path",
			fixture: func(t *testing.T) (func(), *Server) {
				srv := newTenantFamilyServer(t, "/api")
				return func() { tenantOwnedGroup(srv.RootGroup(), "/api", policy) }, srv
			},
			want: []string{q("/api"), q("/api/health")},
		},
		{
			name: "covers_ready_path",
			fixture: func(t *testing.T) (func(), *Server) {
				srv := newTenantFamilyServer(t, "")
				return func() { tenantOwnedGroup(srv.RootGroup(), "/ready", policy) }, srv
			},
			want: []string{q("/ready")},
		},
		{
			name: "equals_other_family",
			fixture: func(t *testing.T) (func(), *Server) {
				srv := existingFamily(t)
				return func() { tenantOwnedGroup(srv.ModuleGroup(), "/partner/v2", policy) }, srv
			},
			want: []string{q("/partner/v2")},
		},
		{
			name: "covers_other_family",
			fixture: func(t *testing.T) (func(), *Server) {
				srv := existingFamily(t)
				return func() { tenantOwnedGroup(srv.ModuleGroup(), "/partner", policy) }, srv
			},
			want: []string{q("/partner"), q("/partner/v2")},
		},
		{
			name: "under_other_family",
			fixture: func(t *testing.T) (func(), *Server) {
				srv := existingFamily(t)
				return func() { tenantOwnedGroup(srv.ModuleGroup(), "/partner/v2/x", policy) }, srv
			},
			want: []string{q("/partner/v2/x"), q("/partner/v2")},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			call, srv := tt.fixture(t)
			var admission *routeAdmission
			if srv != nil {
				admission = srv.admission
			}
			before := admission.familyPrefixes()
			// Each substring re-runs the refused call; the record must stay unchanged throughout.
			for _, want := range tt.want {
				assertRegistrationPanics(t, want, call)
			}
			assert.Equal(t, before, admission.familyPrefixes(), "a refused call records no family")
		})
	}
}

func TestTenantOwnedGroupRefusesWhenMultitenantDisabled(t *testing.T) {
	DefaultRouteRegistry.Clear()
	t.Cleanup(DefaultRouteRegistry.Clear)
	srv := New(newProbeTestConfig("/api"), &testLogger{})
	build := func() { tenantOwnedGroup(srv.ModuleGroup(), "/fam", fixedTenantPolicy("family-tenant")) }

	assertRegistrationPanics(t, strconv.Quote("/api/fam"), build)
	assertRegistrationPanics(t, "multitenant.enabled", build)
	assert.Empty(t, srv.admission.familyPrefixes())
}

func TestTenantOwnedGroupAllowsSegmentSiblingPrefix(t *testing.T) {
	srv := newTenantFamilyServer(t, "")
	prefixes := []string{"/partner/v2", "/partner/v20"}
	tenants := []string{"partner-a", "partner-b"}
	for i, prefix := range prefixes {
		tenantOwnedGroup(srv.ModuleGroup(), prefix, fixedTenantPolicy(tenants[i])).
			Add(http.MethodGet, "/orders", answerWith(prefix))
	}

	for _, prefix := range prefixes {
		rec := serveFamilyRequest(srv, prefix+"/orders")
		assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		assert.Equal(t, prefix, rec.Body.String())
	}
	assert.Equal(t, prefixes, srv.admission.familyPrefixes())
}

// The request-path pins below share one recording family; the stages it records.
const (
	familyTenant = "fam-tenant" // the family's own tenant, distinct from familySeatTenant

	traceAuthenticate = "authenticate"
	traceTenant       = "tenant"
	traceDeny         = "deny"
	traceMiddleware   = "middleware"
	traceHandler      = "handler"

	familyDenyStatus = http.StatusForbidden
	familyDenyBody   = "family-deny"
)

// familyTrace records, in order, which stages ran on a tenant-owned family's requests, and
// the tenant each stage last saw on the request context ("" for none).
type familyTrace struct {
	events  []string
	tenants map[string]string
}

func newFamilyTrace() *familyTrace { return &familyTrace{tenants: map[string]string{}} }

func (tr *familyTrace) record(stage string, c HandlerContext) {
	tr.events = append(tr.events, stage)
	tr.tenants[stage], _ = multitenant.GetTenant(c.RequestContext())
}

func (tr *familyTrace) ran(stage string) bool { return slices.Contains(tr.events, stage) }

func (tr *familyTrace) reset() { tr.events = nil }

// fakeBearerStep stands in for auth.Middleware: 401 without Authorization, next otherwise,
// returning next's error unchanged.
func fakeBearerStep(tr *familyTrace) MiddlewareFunc {
	return func(c HandlerContext, next func() error) error {
		tr.record(traceAuthenticate, c)
		if c.RequestHeader(echo.HeaderAuthorization) == "" {
			return NewUnauthorizedError("missing bearer credential")
		}
		return next()
	}
}

// resolveTo is a Tenant step answering id and err.
func resolveTo(tr *familyTrace, id string, err error) TenantFunc {
	return func(c HandlerContext) (string, error) {
		tr.record(traceTenant, c)
		return id, err
	}
}

// recordingDeny is a Deny writing status and body.
func recordingDeny(tr *familyTrace, status int, body string) DenyFunc {
	return func(c HandlerContext, _ error) error {
		tr.record(traceDeny, c)
		return c.String(status, body)
	}
}

func recordingHandler(tr *familyTrace) Handler {
	return func(c HandlerContext) error {
		tr.record(traceHandler, c)
		return c.String(http.StatusOK, "handled")
	}
}

func recordingMiddleware(tr *familyTrace) MiddlewareFunc {
	return func(c HandlerContext, next func() error) error {
		tr.record(traceMiddleware, c)
		return next()
	}
}

// newRecordingFamily builds a Server holding the tenant-owned family /fam (fakeBearerStep, a
// Tenant resolving familyTenant, and a Deny writing familyDenyStatus and familyDenyBody, each
// edit adjusting that policy) with GET /fam/orders answered by recordingHandler.
func newRecordingFamily(t *testing.T, edits ...func(*TenantPolicy, *familyTrace)) (*Server, RouteRegistrar, *familyTrace) {
	t.Helper()
	srv := newTenantFamilyServer(t, "")
	tr := newFamilyTrace()
	policy := TenantPolicy{
		Authenticate: []MiddlewareFunc{fakeBearerStep(tr)},
		Tenant:       resolveTo(tr, familyTenant, nil),
		Deny:         recordingDeny(tr, familyDenyStatus, familyDenyBody),
	}
	for _, edit := range edits {
		edit(&policy, tr)
	}
	family := tenantOwnedGroup(srv.ModuleGroup(), "/fam", policy)
	family.Add(http.MethodGet, "/orders", recordingHandler(tr))
	return srv, family, tr
}

// familyOrderRequest is a typed family route's request; a malformed body fails its binding.
type familyOrderRequest struct {
	Item string `json:"item"`
}

type familyOrderResponse struct {
	Item string `json:"item"`
}

// addTypedFamilyRoute registers POST /typed on family as a typed route recording its handler.
func addTypedFamilyRoute(srv *Server, family RouteRegistrar, tr *familyTrace) {
	POST(NewHandlerRegistry(srv.cfg), family, "/typed", func(req familyOrderRequest, c HandlerContext) (familyOrderResponse, IAPIError) {
		tr.record(traceHandler, c)
		return familyOrderResponse(req), nil
	})
}

// withMalformedJSON sends a JSON body that fails binding.
var withMalformedJSON = withFamilyBody(echo.MIMEApplicationJSON, `{"item":`)

// assertFamilyDenied asserts rec is exactly the recording Deny's answer and the handler never ran.
func assertFamilyDenied(t *testing.T, rec *httptest.ResponseRecorder, tr *familyTrace) {
	t.Helper()
	assert.Equal(t, familyDenyStatus, rec.Code, rec.Body.String())
	assert.Equal(t, familyDenyBody, rec.Body.String())
	assert.True(t, tr.ran(traceDeny), "Deny must answer: %v", tr.events)
	assert.False(t, tr.ran(traceHandler), "the handler must not run on a denied request")
}

// assertFrameworkError asserts rec is the framework envelope for status with code.
func assertFrameworkError(t *testing.T, rec *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	assert.Equal(t, status, rec.Code, rec.Body.String())
	var envelope struct {
		Error *APIErrorResponse `json:"error"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &envelope), rec.Body.String())
	require.NotNil(t, envelope.Error, rec.Body.String())
	assert.Equal(t, code, envelope.Error.Code)
}

// newNotFoundFamilies builds the recording family /fam, with recording Use middleware when use
// is set and a nested group, and /t, whose Tenant reads the org path parameter: empty on the
// family's catch-all.
func newNotFoundFamilies(t *testing.T, use bool) (*Server, *familyTrace) {
	t.Helper()
	srv, family, tr := newRecordingFamily(t)
	if use {
		family.Use(recordingMiddleware(tr))
	}
	family.Group("/sub").Add(http.MethodGet, "/items", recordingHandler(tr))
	orgFamily := tenantOwnedGroup(srv.ModuleGroup(), "/t", TenantPolicy{
		Authenticate: []MiddlewareFunc{fakeBearerStep(tr)},
		Tenant: func(c HandlerContext) (string, error) {
			tr.record(traceTenant, c)
			if org := c.Param("org"); org != "" {
				return org, nil
			}
			return "", fmt.Errorf("no org path parameter: %w", multitenant.ErrTenantResolutionFailed)
		},
		Deny: recordingDeny(tr, familyDenyStatus, familyDenyBody),
	})
	orgFamily.Add(http.MethodGet, "/:org/orders", recordingHandler(tr))
	return srv, tr
}

// notFoundCase is one TestTenantOwnedGroupAnswersNotFoundAfterTenant case.
type notFoundCase struct {
	name       string
	method     string
	path       string
	credential bool
	use        bool   // the family's Use middleware precedes /sub/items, which then carries it
	wantDeny   bool   // the family's Deny answers; otherwise the framework 404, after the tenant
	sameAs     string // a known path whose anonymous answer this one equals byte for byte
	control    string // a known path the same caller resolves, so Deny is not the only answer
}

// assertAnswer checks rec, then the known paths the case names as controls.
func (tt *notFoundCase) assertAnswer(t *testing.T, srv *Server, tr *familyTrace, rec *httptest.ResponseRecorder, edits []func(*http.Request)) {
	t.Helper()
	if tt.wantDeny {
		assertFamilyDenied(t, rec, tr)
	} else {
		assertFrameworkError(t, rec, http.StatusNotFound, errCodeNotFound)
		assert.Equal(t, []string{traceAuthenticate, traceTenant}, tr.events, "the 404 answers after Authenticate and Tenant")
	}
	if tt.sameAs != "" {
		known := serveFamily(srv, http.MethodGet, tt.sameAs)
		assert.Equal(t, known.Code, rec.Code)
		assert.Equal(t, known.Body.Bytes(), rec.Body.Bytes(), "an unknown sub-path's anonymous answer equals a known path's")
	}
	if tt.control != "" {
		known := serveFamily(srv, http.MethodGet, tt.control, edits...)
		assert.Equal(t, http.StatusOK, known.Code, known.Body.String())
		assert.Equal(t, "acme", tr.tenants[traceHandler])
	}
	if tt.use {
		tr.reset()
		known := serveFamily(srv, http.MethodGet, "/fam/sub/items", edits...)
		assert.Equal(t, http.StatusOK, known.Code, known.Body.String())
		assert.True(t, tr.ran(traceMiddleware), "control: the known route runs the Use middleware")
	}
}

func TestTenantOwnedGroupAnswersNotFoundAfterTenant(t *testing.T) {
	tests := []notFoundCase{
		{name: "known_path_anonymous_gets_deny", method: http.MethodGet, path: "/fam/orders", wantDeny: true},
		{name: "unknown_sub_path_anonymous_gets_same_deny", method: http.MethodGet, path: "/fam/nope", wantDeny: true, sameAs: "/fam/orders"},
		{name: "bare_prefix_without_route_runs_family", method: http.MethodGet, path: "/fam", wantDeny: true},
		{name: "unknown_sub_path_resolved_gets_404", method: http.MethodGet, path: "/fam/nope", credential: true},
		{name: "wrong_method_resolved_gets_404", method: http.MethodDelete, path: "/fam/orders", credential: true},
		{name: "nested_group_unknown_path_gets_404", method: http.MethodGet, path: "/fam/sub/nope", credential: true},
		{name: "nested_group_wrong_method_gets_404", method: http.MethodDelete, path: "/fam/sub/items", credential: true},
		{name: "path_param_family_denies_on_unknown_sub_path", method: http.MethodGet, path: "/t/acme/nope", credential: true, wantDeny: true, control: "/t/acme/orders"},
		{name: "use_middleware_skips_unknown_sub_path", method: http.MethodGet, path: "/fam/sub/nope", credential: true, use: true},
		{name: "use_middleware_skips_wrong_method", method: http.MethodDelete, path: "/fam/sub/items", credential: true, use: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv, tr := newNotFoundFamilies(t, tt.use)
			var edits []func(*http.Request)
			if tt.credential {
				edits = append(edits, withFamilyCredential)
			}

			rec := serveFamily(srv, tt.method, tt.path, edits...)
			tt.assertAnswer(t, srv, tr, rec, edits)
		})
	}
}

func TestTenantOwnedGroupPublishesTenant(t *testing.T) {
	tests := []struct {
		name  string
		check func(t *testing.T, srv *Server, family RouteRegistrar, tr *familyTrace)
	}{
		{
			name: "route_middleware_sees_tenant",
			check: func(t *testing.T, srv *Server, family RouteRegistrar, tr *familyTrace) {
				family.Add(http.MethodGet, "/mw", recordingHandler(tr), recordingMiddleware(tr))

				rec := serveFamily(srv, http.MethodGet, "/fam/mw", withFamilyCredential)
				assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
				assert.Equal(t, []string{traceAuthenticate, traceTenant, traceMiddleware, traceHandler}, tr.events)
				assert.Equal(t, familyTenant, tr.tenants[traceMiddleware])
			},
		},
		{
			name: "use_middleware_sees_tenant",
			check: func(t *testing.T, srv *Server, family RouteRegistrar, tr *familyTrace) {
				family.Use(recordingMiddleware(tr))
				family.Add(http.MethodGet, "/used", recordingHandler(tr))

				rec := serveFamily(srv, http.MethodGet, "/fam/used", withFamilyCredential)
				assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
				assert.Equal(t, []string{traceAuthenticate, traceTenant, traceMiddleware, traceHandler}, tr.events)
				assert.Equal(t, familyTenant, tr.tenants[traceMiddleware])
			},
		},
		{
			name: "nested_middleware_sees_tenant",
			check: func(t *testing.T, srv *Server, family RouteRegistrar, tr *familyTrace) {
				family.Group("/sub", recordingMiddleware(tr)).Add(http.MethodGet, "/items", recordingHandler(tr))

				rec := serveFamily(srv, http.MethodGet, "/fam/sub/items", withFamilyCredential)
				assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
				assert.Equal(t, []string{traceAuthenticate, traceTenant, traceMiddleware, traceHandler}, tr.events)
				assert.Equal(t, familyTenant, tr.tenants[traceMiddleware])
			},
		},
		{
			name: "handler_sees_tenant",
			check: func(t *testing.T, srv *Server, _ RouteRegistrar, tr *familyTrace) {
				rec := serveFamily(srv, http.MethodGet, "/fam/orders", withFamilyCredential)
				assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
				assert.Equal(t, familyTenant, tr.tenants[traceHandler])
				assert.Empty(t, tr.tenants[traceAuthenticate], "the seat skips a family route: no tenant exists until the family publishes")
			},
		},
		{
			name: "binding_runs_after_publish",
			check: func(t *testing.T, srv *Server, family RouteRegistrar, tr *familyTrace) {
				addTypedFamilyRoute(srv, family, tr)

				assertFamilyDenied(t, serveFamily(srv, http.MethodPost, "/fam/typed", withMalformedJSON), tr)
				tr.reset()
				rec := serveFamily(srv, http.MethodPost, "/fam/typed", withMalformedJSON, withFamilyCredential)
				assertFrameworkError(t, rec, http.StatusBadRequest, errCodeBadRequest)
				assert.Equal(t, []string{traceAuthenticate, traceTenant}, tr.events, "binding fails after the publish, before the handler")
			},
		},
		{
			name: "jose_runs_after_publish",
			check: func(t *testing.T, srv *Server, family RouteRegistrar, tr *familyTrace) {
				f := newJOSEFixture(t)
				hr := NewHandlerRegistry(srv.cfg, WithJOSEResolver(f.resolver))
				POST(hr, family, "/tokens", func(_ taggedReq, c HandlerContext) (taggedResp, IAPIError) {
					tr.record(traceHandler, c)
					return taggedResp{}, nil
				})
				withBadEnvelope := withFamilyBody(jose.ContentType, "not-a-jwe")

				assertFamilyDenied(t, serveFamily(srv, http.MethodPost, "/fam/tokens", withBadEnvelope), tr)
				tr.reset()
				rec := serveFamily(srv, http.MethodPost, "/fam/tokens", withBadEnvelope, withFamilyCredential)
				assert.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
				var envelope map[string]any
				require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &envelope), rec.Body.String())
				assert.Equal(t, "JOSE_MALFORMED", envelope["code"])
				assert.Equal(t, []string{traceAuthenticate, traceTenant}, tr.events, "JOSE fails after the publish, before the handler")

				// Control: a sealed request reaches the handler with the family's tenant.
				tr.reset()
				sealed := jositest.SealForTest(t, []byte(`{"pan":"test-pan-001"}`), f.peerOutbound(), f.resolver)
				rec = serveFamily(srv, http.MethodPost, "/fam/tokens", withFamilyBody(jose.ContentType, sealed), withFamilyCredential)
				assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
				assert.Equal(t, familyTenant, tr.tenants[traceHandler])
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv, family, tr := newRecordingFamily(t)
			tt.check(t, srv, family, tr)
		})
	}
}

func TestTenantOwnedGroupGrammarBackstop(t *testing.T) {
	tests := []struct {
		name string
		id   string
	}{
		{name: "empty_id", id: ""},
		{name: "uppercase_id", id: "Acme"},
		{name: "underscore_id", id: "acme_co"},
		{name: "over_64_bytes", id: strings.Repeat("a", 65)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv, _, tr := newRecordingFamily(t, func(p *TenantPolicy, tr *familyTrace) {
				p.Tenant = resolveTo(tr, tt.id, nil)
			})

			rec := serveFamily(srv, http.MethodGet, "/fam/orders", withFamilyCredential)
			assertFrameworkError(t, rec, http.StatusInternalServerError, errCodeInternalError)
			assert.Equal(t, []string{traceAuthenticate, traceTenant}, tr.events, "Deny never runs and the handler never runs")
		})
	}
}

func TestTenantOwnedGroupDenyWritesFamilyBytes(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
	}{
		{name: "status_200_with_body", status: http.StatusOK, body: `{"result":"refused"}`},
		{name: "status_401_with_body", status: http.StatusUnauthorized, body: `{"error":"invalid_token"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv, _, tr := newRecordingFamily(t, func(p *TenantPolicy, tr *familyTrace) {
				p.Tenant = resolveTo(tr, "", fmt.Errorf("caller names no tenant: %w", multitenant.ErrTenantResolutionFailed))
				p.Deny = recordingDeny(tr, tt.status, tt.body)
			})

			rec := serveFamily(srv, http.MethodGet, "/fam/orders", withFamilyCredential)
			assert.Equal(t, tt.status, rec.Code)
			assert.Equal(t, tt.body, rec.Body.String())
			assert.Equal(t, []string{traceAuthenticate, traceTenant, traceDeny}, tr.events)
		})
	}
}

func TestTenantOwnedGroupStampsTraceparentBeforeDeny(t *testing.T) {
	var inDeny string
	srv, _, tr := newRecordingFamily(t, func(p *TenantPolicy, tr *familyTrace) {
		p.Deny = func(c HandlerContext, _ error) error {
			tr.record(traceDeny, c)
			inDeny = c.ResponseWriter().Header().Get(gobrickstrace.HeaderTraceParent)
			return c.String(familyDenyStatus, familyDenyBody)
		}
	})

	rec := serveFamily(srv, http.MethodGet, "/fam/orders")
	assertFamilyDenied(t, rec, tr)
	assert.NotEmpty(t, inDeny, "the response carries traceparent before Deny runs")
	assert.Equal(t, inDeny, rec.Header().Get(gobrickstrace.HeaderTraceParent))
}

func TestTenantOwnedGroupDenyMisbehavior(t *testing.T) {
	tests := []struct {
		name       string
		deny       func(c HandlerContext) error
		wantStatus int
		wantCode   string
	}{
		{
			name:       "deny_error_rendered_by_framework",
			deny:       func(HandlerContext) error { return NewBaseAPIError("DENY_FAILED", "deny failed", http.StatusConflict) },
			wantStatus: http.StatusConflict,
			wantCode:   "DENY_FAILED",
		},
		{
			name:       "deny_commits_nothing_gets_500",
			deny:       func(HandlerContext) error { return nil },
			wantStatus: http.StatusInternalServerError,
			wantCode:   errCodeInternalError,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv, _, tr := newRecordingFamily(t, func(p *TenantPolicy, tr *familyTrace) {
				p.Deny = func(c HandlerContext, _ error) error {
					tr.record(traceDeny, c)
					return tt.deny(c)
				}
			})

			rec := serveFamily(srv, http.MethodGet, "/fam/orders")
			assertFrameworkError(t, rec, tt.wantStatus, tt.wantCode)
			assert.Equal(t, []string{traceAuthenticate, traceDeny}, tr.events)
		})
	}
}

func TestTenantOwnedGroupErrorsAfterPublishBypassDeny(t *testing.T) {
	tests := []struct {
		name       string
		policy     func(*TenantPolicy, *familyTrace)
		route      func(srv *Server, family RouteRegistrar, tr *familyTrace)
		method     string
		path       string
		edits      []func(*http.Request)
		wantStatus int
		wantCode   string
		wantEvents []string
	}{
		{
			name: "handler_returns_400",
			route: func(_ *Server, family RouteRegistrar, tr *familyTrace) {
				family.Add(http.MethodGet, "/bad", func(c HandlerContext) error {
					tr.record(traceHandler, c)
					return NewBadRequestError("bad order")
				})
			},
			method:     http.MethodGet,
			path:       "/fam/bad",
			wantStatus: http.StatusBadRequest,
			wantCode:   errCodeBadRequest,
			wantEvents: []string{traceAuthenticate, traceTenant, traceHandler},
		},
		{
			name:       "binding_fails",
			route:      addTypedFamilyRoute,
			method:     http.MethodPost,
			path:       "/fam/typed",
			edits:      []func(*http.Request){withMalformedJSON},
			wantStatus: http.StatusBadRequest,
			wantCode:   errCodeBadRequest,
			wantEvents: []string{traceAuthenticate, traceTenant},
		},
		{
			name: "error_returned_through_authenticate_step",
			policy: func(p *TenantPolicy, tr *familyTrace) {
				p.Authenticate = []MiddlewareFunc{func(c HandlerContext, next func() error) error {
					tr.record(traceAuthenticate, c)
					return next()
				}}
			},
			route: func(_ *Server, family RouteRegistrar, tr *familyTrace) {
				family.Add(http.MethodGet, "/forbidden", func(c HandlerContext) error {
					tr.record(traceHandler, c)
					return NewForbiddenError("not your order")
				})
			},
			method:     http.MethodGet,
			path:       "/fam/forbidden",
			wantStatus: http.StatusForbidden,
			wantCode:   errCodeForbidden,
			wantEvents: []string{traceAuthenticate, traceTenant, traceHandler},
		},
		{
			name: "use_middleware_returns_403",
			route: func(_ *Server, family RouteRegistrar, tr *familyTrace) {
				family.Use(func(c HandlerContext, _ func() error) error {
					tr.record(traceMiddleware, c)
					return NewForbiddenError("not your order")
				})
				family.Add(http.MethodGet, "/guarded", recordingHandler(tr))
			},
			method:     http.MethodGet,
			path:       "/fam/guarded",
			wantStatus: http.StatusForbidden,
			wantCode:   errCodeForbidden,
			wantEvents: []string{traceAuthenticate, traceTenant, traceMiddleware},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var edits []func(*TenantPolicy, *familyTrace)
			if tt.policy != nil {
				edits = append(edits, tt.policy)
			}
			srv, family, tr := newRecordingFamily(t, edits...)
			tt.route(srv, family, tr)

			rec := serveFamily(srv, tt.method, tt.path, append(tt.edits, withFamilyCredential)...)
			assertFrameworkError(t, rec, tt.wantStatus, tt.wantCode)
			assert.Equal(t, tt.wantEvents, tr.events, "Deny never runs on an error raised after the publish")
		})
	}
}

func TestTenantOwnedGroupForwardedClientCertRequireAnswersFirst(t *testing.T) {
	cfg := newTenantFamilyConfig("")
	cfg.Server.ForwardedClientCert = config.ForwardedClientCertConfig{Enabled: true, Require: true}
	srv := newTenantFamilyServerWithConfig(t, cfg)
	tr := newFamilyTrace()
	tenantOwnedGroup(srv.ModuleGroup(), "/fam", TenantPolicy{
		Authenticate: []MiddlewareFunc{fakeBearerStep(tr)},
		Tenant:       resolveTo(tr, familyTenant, nil),
		Deny:         recordingDeny(tr, familyDenyStatus, familyDenyBody),
	}).Add(http.MethodGet, "/orders", recordingHandler(tr))

	for _, path := range []string{"/fam/orders", "/fam/nope"} {
		rec := serveFamily(srv, http.MethodGet, path, withFamilyCredential)
		assertFrameworkError(t, rec, http.StatusUnauthorized, errCodeUnauthorized)
	}
	assert.Empty(t, tr.events, "the root's require answers before any family step")
}

func TestTenantOwnedGroupRegistersOneCatchAllPair(t *testing.T) {
	srv := newTenantFamilyServer(t, "")
	passThrough := func(_ HandlerContext, next func() error) error { return next() }
	// The parent's middleware gives it its own catch-all pair at /p and /p/*.
	parent := srv.ModuleGroup().Group("/p", passThrough)
	family := tenantOwnedGroup(parent, "/fam", fixedTenantPolicy(familyTenant))
	family.Add(http.MethodGet, "/orders", answerWith("orders"))
	family.Add(http.MethodPost, "/orders", answerWith("created"))
	family.Group("/sub").Add(http.MethodGet, "/items", answerWith("items"))

	var family404s, all404s []string
	for _, r := range srv.echo.Router().Routes() {
		if r.Method != echo.RouteNotFound {
			continue
		}
		all404s = append(all404s, r.Path)
		if _, under := pathutil.StripPathPrefix(r.Path, "/p/fam"); under {
			family404s = append(family404s, r.Path)
		}
	}
	assert.ElementsMatch(t, []string{"/p/fam", "/p/fam/*"}, family404s)
	assert.Contains(t, all404s, "/p/*", "control: the parent's own pair stays outside the family")
}

// recordingStep is a middleware recording stage, then calling next.
func recordingStep(tr *familyTrace, stage string) MiddlewareFunc {
	return func(c HandlerContext, next func() error) error {
		tr.record(stage, c)
		return next()
	}
}

func TestTenantOwnedGroupRunsStepsInOrder(t *testing.T) {
	srv, family, tr := newRecordingFamily(t, func(p *TenantPolicy, tr *familyTrace) {
		p.Authenticate = []MiddlewareFunc{recordingStep(tr, "authenticate_1"), recordingStep(tr, "authenticate_2")}
	})
	family.Use(func(c HandlerContext, next func() error) error {
		// The first stored middleware observes the publish: the family's tenant, not the seat's.
		if id, _ := multitenant.GetTenant(c.RequestContext()); id == familyTenant {
			tr.events = append(tr.events, "publish")
		}
		tr.record("use_middleware", c)
		return next()
	})
	family.Group("/sub", recordingStep(tr, "nested_middleware")).
		Add(http.MethodGet, "/items", recordingHandler(tr), recordingStep(tr, "route_middleware"))

	rec := serveFamily(srv, http.MethodGet, "/fam/sub/items")
	assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, []string{
		"authenticate_1", "authenticate_2", traceTenant, "publish",
		"use_middleware", "nested_middleware", "route_middleware", traceHandler,
	}, tr.events)
}

// The committing step's answer, which must stand byte for byte.
const (
	stepCommitStatus = http.StatusUnauthorized
	stepCommitBody   = "step-bytes"
	traceNextNil     = "next_returned_nil"
)

// assertStepCommitLogged asserts srv logged the step-committed wiring fault for GET
// /fam/orders, carrying no tenant or header value.
func assertStepCommitLogged(t *testing.T, srv *Server) {
	t.Helper()
	log, ok := srv.logger.(*testLogger)
	require.True(t, ok, "newTenantFamilyServer logs to a testLogger")
	var lines []string
	for _, entry := range log.logEntries() {
		if strings.Contains(entry.msg, strconv.Quote(reasonStepCommitted)) {
			lines = append(lines, entry.level+" "+entry.msg)
		}
	}
	require.Len(t, lines, 1, "one wiring-fault line: %v", log.logEntries())
	assert.Contains(t, lines[0], "error ")
	assert.Contains(t, lines[0], `path="/fam/orders"`)
	for _, value := range []string{familyTenant, familySeatTenant, "family-test"} {
		assert.NotContains(t, lines[0], value, "the log line never carries a tenant or header value")
	}
}

// noteNextNil calls next and records traceNextNil when it returns nil.
func noteNextNil(tr *familyTrace, next func() error) {
	if next() == nil {
		tr.events = append(tr.events, traceNextNil)
	}
}

// newStepOutcomeFamily builds the recording family with steps (nil entries skipped) as its
// Authenticate steps, plus /fam/refused, whose handler returns a 403 without committing.
func newStepOutcomeFamily(t *testing.T, steps ...func(*familyTrace) MiddlewareFunc) (*Server, *familyTrace) {
	t.Helper()
	srv, family, tr := newRecordingFamily(t, func(p *TenantPolicy, tr *familyTrace) {
		p.Authenticate = nil
		for _, step := range steps {
			if step != nil {
				p.Authenticate = append(p.Authenticate, step(tr))
			}
		}
	})
	family.Add(http.MethodGet, "/refused", func(c HandlerContext) error {
		tr.record(traceHandler, c)
		return NewForbiddenError("refused")
	})
	return srv, tr
}

func TestTenantOwnedGroupAuthenticateStepOutcomes(t *testing.T) {
	tests := []struct {
		name   string
		path   string // default /fam/orders
		step   func(tr *familyTrace) MiddlewareFunc
		second func(tr *familyTrace) MiddlewareFunc // optional; the next Authenticate step
		check  func(t *testing.T, srv *Server, rec *httptest.ResponseRecorder, tr *familyTrace)
	}{
		{
			name: "step_commits_response_is_wiring_fault",
			step: func(tr *familyTrace) MiddlewareFunc {
				return func(c HandlerContext, _ func() error) error {
					tr.record(traceAuthenticate, c)
					return c.String(stepCommitStatus, stepCommitBody)
				}
			},
			check: func(t *testing.T, srv *Server, rec *httptest.ResponseRecorder, tr *familyTrace) {
				assert.Equal(t, stepCommitStatus, rec.Code)
				assert.Equal(t, stepCommitBody, rec.Body.String(), "the committed bytes stand")
				assert.Equal(t, []string{traceAuthenticate}, tr.events, "neither Tenant, Deny nor the handler runs")
				assertStepCommitLogged(t, srv)
			},
		},
		{
			name: "step_commits_then_calls_next_gets_nil",
			step: func(tr *familyTrace) MiddlewareFunc {
				return func(c HandlerContext, next func() error) error {
					tr.record(traceAuthenticate, c)
					err := c.String(stepCommitStatus, stepCommitBody)
					noteNextNil(tr, next)
					return err
				}
			},
			second: func(tr *familyTrace) MiddlewareFunc { return recordingStep(tr, "authenticate_2") },
			check: func(t *testing.T, srv *Server, rec *httptest.ResponseRecorder, tr *familyTrace) {
				assert.Equal(t, stepCommitStatus, rec.Code)
				assert.Equal(t, stepCommitBody, rec.Body.String(), "the committed bytes stand")
				assert.Equal(t, []string{traceAuthenticate, traceNextNil}, tr.events,
					"next returns nil: neither the second step, Tenant nor the handler runs")
				assertStepCommitLogged(t, srv)
			},
		},
		{
			name: "step_commits_and_returns_error_skips_deny",
			step: func(tr *familyTrace) MiddlewareFunc {
				return func(c HandlerContext, _ func() error) error {
					tr.record(traceAuthenticate, c)
					return errors.Join(c.String(stepCommitStatus, stepCommitBody),
						fmt.Errorf("committed then refused: %w", multitenant.ErrTenantResolutionFailed))
				}
			},
			check: func(t *testing.T, srv *Server, rec *httptest.ResponseRecorder, tr *familyTrace) {
				assert.Equal(t, stepCommitStatus, rec.Code)
				assert.Equal(t, stepCommitBody, rec.Body.String(), "the committed bytes stand")
				assert.Equal(t, []string{traceAuthenticate}, tr.events, "Deny never writes over committed bytes")
				assertStepCommitLogged(t, srv)
			},
		},
		{
			name: "step_calls_next_twice_runs_tenant_once",
			path: "/fam/refused", // its handler commits nothing, so only the re-entry check stops a second pass
			step: func(tr *familyTrace) MiddlewareFunc {
				return func(c HandlerContext, next func() error) error {
					tr.record(traceAuthenticate, c)
					first := next()
					noteNextNil(tr, next)
					return first
				}
			},
			check: func(t *testing.T, _ *Server, rec *httptest.ResponseRecorder, tr *familyTrace) {
				assertFrameworkError(t, rec, http.StatusForbidden, errCodeForbidden)
				assert.Equal(t, []string{traceAuthenticate, traceTenant, traceHandler, traceNextNil}, tr.events,
					"the second next returns nil: Tenant, the publish and the handler run once")
			},
		},
		{
			name: "step_returns_nil_without_next_gets_500",
			step: func(tr *familyTrace) MiddlewareFunc {
				return func(c HandlerContext, _ func() error) error {
					tr.record(traceAuthenticate, c)
					return nil
				}
			},
			check: func(t *testing.T, _ *Server, rec *httptest.ResponseRecorder, tr *familyTrace) {
				assertFrameworkError(t, rec, http.StatusInternalServerError, errCodeInternalError)
				assert.Equal(t, []string{traceAuthenticate}, tr.events, "neither Tenant, Deny nor the handler runs")
			},
		},
		{
			name: "step_returns_401_goes_to_deny",
			step: fakeBearerStep,
			check: func(t *testing.T, _ *Server, rec *httptest.ResponseRecorder, tr *familyTrace) {
				assertFamilyDenied(t, rec, tr)
				assert.Equal(t, []string{traceAuthenticate, traceDeny}, tr.events)
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv, tr := newStepOutcomeFamily(t, tt.step, tt.second)

			// Anonymous: only fakeBearerStep reads the credential, and it must deny.
			rec := serveFamily(srv, http.MethodGet, cmp.Or(tt.path, "/fam/orders"))
			tt.check(t, srv, rec, tr)
		})
	}
}

func TestTenantOwnedGroupUseAttachesPerRoute(t *testing.T) {
	tests := []struct {
		name  string
		check func(t *testing.T, srv *Server, family RouteRegistrar, tr *familyTrace)
	}{
		{
			name: "use_reaches_later_route",
			check: func(t *testing.T, srv *Server, family RouteRegistrar, tr *familyTrace) {
				family.Use(recordingMiddleware(tr))
				family.Add(http.MethodGet, "/later", recordingHandler(tr))

				rec := serveFamily(srv, http.MethodGet, "/fam/later", withFamilyCredential)
				assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
				assert.Equal(t, []string{traceAuthenticate, traceTenant, traceMiddleware, traceHandler}, tr.events)
			},
		},
		{
			name: "use_reaches_later_route_in_earlier_nested_group",
			check: func(t *testing.T, srv *Server, family RouteRegistrar, tr *familyTrace) {
				sub := family.Group("/sub")
				family.Use(recordingMiddleware(tr))
				sub.Add(http.MethodGet, "/later", recordingHandler(tr))

				rec := serveFamily(srv, http.MethodGet, "/fam/sub/later", withFamilyCredential)
				assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
				assert.Equal(t, []string{traceAuthenticate, traceTenant, traceMiddleware, traceHandler}, tr.events)
			},
		},
		{
			name: "use_skips_earlier_route",
			check: func(t *testing.T, srv *Server, family RouteRegistrar, tr *familyTrace) {
				family.Add(http.MethodGet, "/earlier", recordingHandler(tr))
				family.Use(recordingMiddleware(tr))
				family.Add(http.MethodGet, "/later", recordingHandler(tr))

				rec := serveFamily(srv, http.MethodGet, "/fam/earlier", withFamilyCredential)
				assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
				assert.Equal(t, []string{traceAuthenticate, traceTenant, traceHandler}, tr.events)

				tr.reset()
				rec = serveFamily(srv, http.MethodGet, "/fam/later", withFamilyCredential)
				assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
				assert.True(t, tr.ran(traceMiddleware), "control: a route registered after Use runs it")
			},
		},
		{
			name: "use_registers_no_catch_all",
			check: func(t *testing.T, srv *Server, family RouteRegistrar, tr *familyTrace) {
				familyNotFoundPaths := func() []string {
					var paths []string
					for _, r := range srv.echo.Router().Routes() {
						if _, under := pathutil.StripPathPrefix(r.Path, "/fam"); under && r.Method == echo.RouteNotFound {
							paths = append(paths, r.Path)
						}
					}
					return paths
				}
				family.Use(recordingMiddleware(tr))
				assert.ElementsMatch(t, []string{"/fam", "/fam/*"}, familyNotFoundPaths(), "after Use")
				family.Group("/sub", recordingMiddleware(tr)).Add(http.MethodGet, "/items", recordingHandler(tr))
				assert.ElementsMatch(t, []string{"/fam", "/fam/*"}, familyNotFoundPaths(), "after a nested group with middleware")
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv, family, tr := newRecordingFamily(t)
			tt.check(t, srv, family, tr)
		})
	}
}

// echoFamilyOrder is a typed handler answering its request.
func echoFamilyOrder(req familyOrderRequest, _ HandlerContext) (familyOrderResponse, IAPIError) {
	return familyOrderResponse(req), nil
}

func TestTenantOwnedDescriptorCannotBeForgedOrCleared(t *testing.T) {
	setOwned := func(owned bool) RouteOption {
		return func(d *RouteDescriptor) { d.TenantOwned = owned }
	}
	tests := []struct {
		name string
		// register registers one route and returns its full path.
		register func(srv *Server, family RouteRegistrar) string
		want     bool
	}{
		{
			name: "typed_route_option_clears_flag_in_family",
			register: func(srv *Server, family RouteRegistrar) string {
				POST(NewHandlerRegistry(srv.cfg), family, "/typed", echoFamilyOrder, setOwned(false))
				return "/fam/typed"
			},
			want: true,
		},
		{
			name: "typed_route_option_sets_flag_outside_family",
			register: func(srv *Server, _ RouteRegistrar) string {
				POST(NewHandlerRegistry(srv.cfg), srv.ModuleGroup(), "/plain", echoFamilyOrder, setOwned(true))
				return "/plain"
			},
			want: false,
		},
		{
			name: "raw_add_route",
			register: func(_ *Server, family RouteRegistrar) string {
				family.Add(http.MethodPost, "/raw", answerWith("raw"))
				return "/fam/raw"
			},
			want: true,
		},
		{
			name: "nested_group_route",
			register: func(srv *Server, family RouteRegistrar) string {
				passThrough := func(_ HandlerContext, next func() error) error { return next() }
				POST(NewHandlerRegistry(srv.cfg), family.Group("/sub", passThrough), "/typed", echoFamilyOrder)
				return "/fam/sub/typed"
			},
			want: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := newTenantFamilyServer(t, "")
			family := tenantOwnedGroup(srv.ModuleGroup(), "/fam", fixedTenantPolicy(familyTenant))
			path := tt.register(srv, family)

			routes := DefaultRouteRegistry.ByPath(path)
			require.Len(t, routes, 1)
			assert.Equal(t, tt.want, routes[0].TenantOwned)
		})
	}
}
