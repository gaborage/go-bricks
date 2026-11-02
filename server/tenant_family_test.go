package server

import (
	"context"
	"encoding/json"
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

// familySeatTenant is the tenant every request through the root chain carries: the root
// tenant seat still resolves family routes.
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
	// bare is a group no Server built: it carries no admission.
	bare := func(*Server) RouteRegistrar {
		return newRouteGroup(echo.New().Group(""), "", newTenantFamilyConfig(""))
	}
	moduleGroup := (*Server).ModuleGroup
	rootGroup := (*Server).RootGroup
	ordersGroup := func(srv *Server) RouteRegistrar { return srv.ModuleGroup().Group("/orders") }
	// inFamily is a plain Group inside the tenant-owned family /fam.
	inFamily := func(srv *Server) RouteRegistrar {
		return tenantOwnedGroup(srv.ModuleGroup(), "/fam", policy).Group("/sub")
	}
	// besideFamily is the module group of a Server already holding the tenant-owned family /partner/v2.
	besideFamily := func(srv *Server) RouteRegistrar {
		tenantOwnedGroup(srv.ModuleGroup(), "/partner/v2", policy)
		return srv.ModuleGroup()
	}
	tests := []struct {
		name      string
		basePath  string
		parent    func(srv *Server) RouteRegistrar // the registrar the refused call builds under
		prefix    string
		nilTenant bool     // the refused call passes TenantPolicy{} instead of policy
		want      []string // substrings of the panic message, the full prefix first
	}{
		{name: "non_framework_registrar", parent: bare, prefix: "/fam", want: []string{`"/fam"`}},
		{name: "nil_tenant", parent: moduleGroup, prefix: "/fam", nilTenant: true, want: []string{`"/fam"`}},
		{name: "nested_in_family", parent: inFamily, prefix: "/inner", want: []string{`"/fam/sub/inner"`, `"/fam"`}},
		{name: "empty_prefix", parent: ordersGroup, prefix: "", want: []string{`"/orders"`}},
		{name: "root_prefix", parent: ordersGroup, prefix: "/", want: []string{`"/orders"`}},
		{name: "param_segment", parent: moduleGroup, prefix: "/orgs/:org", want: []string{`"/orgs/:org"`}},
		{name: "wildcard_segment", parent: moduleGroup, prefix: "/files/*", want: []string{`"/files/*"`}},
		{name: "covers_health_path", basePath: "/api", parent: rootGroup, prefix: "/api", want: []string{`"/api"`, `"/api/health"`}},
		{name: "covers_ready_path", parent: rootGroup, prefix: "/ready", want: []string{`"/ready"`}},
		{name: "equals_other_family", parent: besideFamily, prefix: "/partner/v2", want: []string{`"/partner/v2"`}},
		{name: "covers_other_family", parent: besideFamily, prefix: "/partner", want: []string{`"/partner"`, `"/partner/v2"`}},
		{name: "under_other_family", parent: besideFamily, prefix: "/partner/v2/x", want: []string{`"/partner/v2/x"`, `"/partner/v2"`}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := newTenantFamilyServer(t, tt.basePath)
			parent := tt.parent(srv)
			p := policy
			if tt.nilTenant {
				p = TenantPolicy{}
			}
			before := srv.admission.familyPrefixes()
			// Each substring re-runs the refused call; the record must stay unchanged throughout.
			for _, want := range tt.want {
				assertRegistrationPanics(t, want, func() { tenantOwnedGroup(parent, tt.prefix, p) })
			}
			assert.Equal(t, before, srv.admission.familyPrefixes(), "a refused call records no family")
		})
	}
}

func TestTenantOwnedGroupRefusesWhenMultitenantDisabled(t *testing.T) {
	srv := newTenantFamilyServerWithConfig(t, newProbeTestConfig("/api"))
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
	traceForeign      = "foreign" // a handler outside every family

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

func (tr *familyTrace) reset() {
	tr.events = nil
	clear(tr.tenants)
}

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

// recordingPolicy is the recording family's TenantPolicy: fakeBearerStep, a Tenant resolving
// familyTenant, and a Deny writing familyDenyStatus and familyDenyBody.
func recordingPolicy(tr *familyTrace) TenantPolicy {
	return TenantPolicy{
		Authenticate: []MiddlewareFunc{fakeBearerStep(tr)},
		Tenant:       resolveTo(tr, familyTenant, nil),
		Deny:         recordingDeny(tr, familyDenyStatus, familyDenyBody),
	}
}

// newRecordingFamily builds a Server holding the tenant-owned family /fam under
// recordingPolicy, each edit adjusting that policy, with GET /fam/orders answered by
// recordingHandler.
func newRecordingFamily(t *testing.T, edits ...func(*TenantPolicy, *familyTrace)) (*Server, RouteRegistrar, *familyTrace) {
	t.Helper()
	srv := newTenantFamilyServer(t, "")
	tr := newFamilyTrace()
	policy := recordingPolicy(tr)
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
	var envelope APIResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &envelope), rec.Body.String())
	require.NotNil(t, envelope.Error, rec.Body.String())
	assert.Equal(t, code, envelope.Error.Code)
}

// newNotFoundFamilies builds the recording family /fam with a nested group and two wildcard
// routes; /t, whose Tenant reads the org path parameter: empty on the family's catch-all; and
// /bare, whose one route is GET at its bare prefix, under a foreign POST, HEAD and OPTIONS /:seg.
func newNotFoundFamilies(t *testing.T) (*Server, *familyTrace) {
	t.Helper()
	srv, family, tr := newRecordingFamily(t)
	family.Group("/sub").Add(http.MethodGet, "/items", recordingHandler(tr))
	family.Add(http.MethodGet, "/files/*", recordingHandler(tr))
	family.Add(http.MethodGet, "/x/:id/*", recordingHandler(tr))
	orgPolicy := recordingPolicy(tr)
	orgPolicy.Tenant = func(c HandlerContext) (string, error) {
		tr.record(traceTenant, c)
		if org := c.Param("org"); org != "" {
			return org, nil
		}
		return "", fmt.Errorf("no org path parameter: %w", multitenant.ErrTenantResolutionFailed)
	}
	orgFamily := tenantOwnedGroup(srv.ModuleGroup(), "/t", orgPolicy)
	orgFamily.Add(http.MethodGet, "/:org/orders", recordingHandler(tr))
	tenantOwnedGroup(srv.ModuleGroup(), "/bare", recordingPolicy(tr)).Add(http.MethodGet, "", recordingHandler(tr))
	foreign := func(c HandlerContext) error {
		tr.record(traceForeign, c)
		return c.String(http.StatusOK, "foreign")
	}
	for _, method := range []string{http.MethodPost, http.MethodHead, http.MethodOptions} {
		srv.ModuleGroup().Add(method, "/:seg", foreign)
	}
	return srv, tr
}

// notFoundCase is one TestTenantOwnedGroupAnswersNotFoundAfterTenant request.
type notFoundCase struct {
	name       string
	method     string
	path       string
	credential bool
	wantDeny   bool     // the family's Deny answers; otherwise the framework 404, after the tenant
	sameAs     string   // a known path whose anonymous answer this one equals byte for byte
	control    string   // a known path the same caller resolves, so Deny is not the only answer
	stepsOn    []string // instead of method: each sent anonymous, then resolved (assertStepsAnswer)
}

func TestTenantOwnedGroupAnswersNotFoundAfterTenant(t *testing.T) {
	// The two Use cases arrive with per-route Use in a later link.
	tests := []notFoundCase{
		{name: "known_path_anonymous_gets_deny", method: http.MethodGet, path: "/fam/orders", wantDeny: true},
		{name: "unknown_sub_path_anonymous_gets_same_deny", method: http.MethodGet, path: "/fam/nope", wantDeny: true, sameAs: "/fam/orders"},
		{name: "bare_prefix_without_route_runs_family", method: http.MethodGet, path: "/fam", wantDeny: true},
		{name: "unknown_sub_path_resolved_gets_404", method: http.MethodGet, path: "/fam/nope", credential: true},
		{name: "wrong_method_resolved_gets_404", method: http.MethodDelete, path: "/fam/orders", credential: true},
		{name: "nested_group_unknown_path_gets_404", method: http.MethodGet, path: "/fam/sub/nope", credential: true},
		{name: "nested_group_wrong_method_gets_404", method: http.MethodDelete, path: "/fam/sub/items", credential: true},
		{name: "path_param_family_denies_on_unknown_sub_path", method: http.MethodGet, path: "/t/acme/nope", credential: true, wantDeny: true, control: "/t/acme/orders"},
		{name: "wildcard_wrong_method_runs_steps", path: "/fam/files/a", stepsOn: []string{http.MethodPost}},
		{name: "wildcard_head_runs_steps", path: "/fam/files/a", stepsOn: []string{http.MethodHead}},
		{name: "wildcard_options_runs_steps", path: "/fam/files/a", stepsOn: []string{http.MethodOptions}},
		{name: "param_wildcard_wrong_method_runs_steps", path: "/fam/x/1/a", stepsOn: []string{http.MethodPost}},
		{name: "wildcard_bare_prefix_wrong_method_runs_steps", path: "/fam/files", stepsOn: []string{http.MethodPost}},
		{name: "bare_prefix_wrong_method_runs_steps", path: "/bare", stepsOn: []string{http.MethodPost}},
		{name: "bare_prefix_head_and_options_run_steps", path: "/bare", stepsOn: []string{http.MethodHead, http.MethodOptions}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv, tr := newNotFoundFamilies(t)
			if len(tt.stepsOn) > 0 {
				assertStepsAnswer(t, srv, tr, tt.path, tt.stepsOn)
				return
			}
			assertNotFoundCase(t, srv, tr, &tt)
		})
	}
}

// assertNotFoundCase sends tt's one request and asserts its answer.
func assertNotFoundCase(t *testing.T, srv *Server, tr *familyTrace, tt *notFoundCase) {
	t.Helper()
	var edits []func(*http.Request)
	if tt.credential {
		edits = append(edits, withFamilyCredential)
	}

	rec := serveFamily(srv, tt.method, tt.path, edits...)
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
}

// assertStepsAnswer sends each method to path anonymous, then resolved: the family's steps answer
// both, Deny the first and the framework 404 the second, no route's handler runs and neither
// carries an Allow header.
func assertStepsAnswer(t *testing.T, srv *Server, tr *familyTrace, path string, methods []string) {
	t.Helper()
	for _, method := range methods {
		tr.reset()
		anonymous := serveFamily(srv, method, path)
		assert.Equal(t, familyDenyStatus, anonymous.Code, method)
		assert.Equal(t, familyDenyBody, anonymous.Body.String(), method)
		assert.Equal(t, []string{traceAuthenticate, traceDeny}, tr.events, method)
		assert.Empty(t, anonymous.Header().Get(echo.HeaderAllow), method)

		tr.reset()
		resolved := serveFamily(srv, method, path, withFamilyCredential)
		assertFrameworkError(t, resolved, http.StatusNotFound, errCodeNotFound)
		assert.Equal(t, []string{traceAuthenticate, traceTenant}, tr.events, method)
		assert.Empty(t, resolved.Header().Get(echo.HeaderAllow), method)
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
			name: "handler_sees_tenant",
			check: func(t *testing.T, srv *Server, _ RouteRegistrar, tr *familyTrace) {
				rec := serveFamily(srv, http.MethodGet, "/fam/orders", withFamilyCredential)
				assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
				assert.Equal(t, familyTenant, tr.tenants[traceHandler])
				assert.Equal(t, familySeatTenant, tr.tenants[traceAuthenticate], "control: the root seat's tenant until the family publishes")
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
	// use_middleware_returns_403 arrives with per-route Use in a later link.
	tests := []struct {
		name       string
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
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv, family, tr := newRecordingFamily(t)
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
	tenantOwnedGroup(srv.ModuleGroup(), "/fam", recordingPolicy(tr)).Add(http.MethodGet, "/orders", recordingHandler(tr))

	for _, path := range []string{"/fam/orders", "/fam/nope"} {
		rec := serveFamily(srv, http.MethodGet, path, withFamilyCredential)
		assertFrameworkError(t, rec, http.StatusUnauthorized, errCodeUnauthorized)
	}
	assert.Empty(t, tr.events, "the root's require answers before any family step")
}

func TestTenantOwnedGroupCopiesAuthenticateSteps(t *testing.T) {
	srv := newTenantFamilyServer(t, "")
	tr := newFamilyTrace()
	policy := recordingPolicy(tr)
	tenantOwnedGroup(srv.ModuleGroup(), "/fam", policy).Add(http.MethodGet, "/orders", recordingHandler(tr))
	policy.Authenticate[0] = recordingMiddleware(tr)

	assertFamilyDenied(t, serveFamily(srv, http.MethodGet, "/fam/orders"), tr)
	assert.Equal(t, []string{traceAuthenticate, traceDeny}, tr.events, "the family runs the Authenticate step it was built with")
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

	assert.ElementsMatch(t, []string{echo.RouteAny + " /p/fam", echo.RouteNotFound + " /p/fam/*"}, catchAllRoutes(srv, "/p/fam"))
	assert.Contains(t, catchAllRoutes(srv, "/p"), echo.RouteNotFound+" /p/*", "control: the parent's own pair stays outside the family")
}

// catchAllRoutes lists srv's RouteAny and RouteNotFound routes at or under prefix, each as
// "method path".
func catchAllRoutes(srv *Server, prefix string) []string {
	var out []string
	for _, r := range srv.echo.Router().Routes() {
		if r.Method != echo.RouteAny && r.Method != echo.RouteNotFound {
			continue
		}
		if _, under := pathutil.StripPathPrefix(r.Path, prefix); under {
			out = append(out, r.Method+" "+r.Path)
		}
	}
	return out
}

func TestTenantOwnedGroupRegistersOneCatchAllPerWildcardNode(t *testing.T) {
	srv := newTenantFamilyServer(t, "")
	family := tenantOwnedGroup(srv.ModuleGroup(), "/fam", fixedTenantPolicy(familyTenant))
	require.NotPanics(t, func() {
		family.Add(http.MethodGet, "/files/*", answerWith("get"))
		family.Add(http.MethodPost, "/files/*", answerWith("post"))
		family.Add(http.MethodGet, "/x/:id/*", answerWith("id"))
		// The same echo node as /x/:id/* under another method, so admission takes it too.
		family.Add(http.MethodPost, "/x/:org/*", answerWith("org"))
		family.Group("/sub").Add(http.MethodGet, "/*", answerWith("sub"))
		family.Add(http.MethodGet, "/*", answerWith("root")) // the node of the prefix's own catch-all
		GET(NewHandlerRegistry(srv.cfg), family, "/typed/*", func(req familyOrderRequest, _ HandlerContext) (familyOrderResponse, IAPIError) {
			return familyOrderResponse(req), nil
		})
	})

	assert.ElementsMatch(t, []string{
		echo.RouteAny + " /fam",
		echo.RouteNotFound + " /fam/*",
		echo.RouteNotFound + " /fam/files/*",
		echo.RouteNotFound + " /fam/x/:id/*",
		echo.RouteNotFound + " /fam/sub/*",
		echo.RouteNotFound + " /fam/typed/*",
	}, catchAllRoutes(srv, "/fam"))
	assert.Empty(t, srv.admission.snapshot(), "admission refuses none of them")
}

func TestTenantOwnedGroupLogsRefusalsToServerLogger(t *testing.T) {
	DefaultRouteRegistry.Clear()
	t.Cleanup(DefaultRouteRegistry.Clear)
	testLog := &testLogger{}
	srv := New(newTenantFamilyConfig(""), testLog)
	tenantOwnedGroup(srv.ModuleGroup(), "/fam", fixedTenantPolicy("Acme")).Add(http.MethodGet, "/orders", answerWith("orders"))

	assertFrameworkError(t, serveFamilyRequest(srv, "/fam/orders"), http.StatusInternalServerError, errCodeInternalError)
	var refusals []testLogEntry
	for _, entry := range testLog.logEntries() {
		if strings.Contains(entry.msg, "family request refused") {
			refusals = append(refusals, entry)
		}
	}
	require.Len(t, refusals, 1, "the grammar backstop's refusal reaches the Server's logger")
	assert.Equal(t, "error", refusals[0].level)
	assert.Contains(t, refusals[0].msg, "reason="+strconv.Quote(reasonGrammarBackstop))
}
