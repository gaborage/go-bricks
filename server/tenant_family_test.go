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
				return parent, TenantOwnedGroup(parent, "/fam", fixedTenantPolicy("family-tenant"))
			},
		},
		{
			name: "parent_use_after_family",
			build: func(srv *Server, parentMW MiddlewareFunc) (parent, family RouteRegistrar) {
				parent = srv.ModuleGroup().Group("/p")
				family = TenantOwnedGroup(parent, "/fam", fixedTenantPolicy("family-tenant"))
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
		return TenantOwnedGroup(srv.ModuleGroup(), "/fam", policy).Group("/sub")
	}
	// besideFamily is the module group of a Server already holding the tenant-owned family /partner/v2.
	besideFamily := func(srv *Server) RouteRegistrar {
		TenantOwnedGroup(srv.ModuleGroup(), "/partner/v2", policy)
		return srv.ModuleGroup()
	}
	tests := []struct {
		name      string
		basePath  string
		parent    func(srv *Server) RouteRegistrar // the registrar the refused call builds under
		prefix    string
		nilTenant bool     // the refused call passes TenantPolicy{} instead of policy
		started   bool     // the refused call runs after srv.Start, through a registrar built before it
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
		{name: "family_after_start_panics", parent: besideFamily, prefix: "/fam", started: true, want: []string{`"/fam"`, "before Start"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := newTenantFamilyServer(t, tt.basePath)
			parent := tt.parent(srv)
			if tt.started {
				errCh := startServer(srv)
				t.Cleanup(func() { shutdownAndDrain(t, srv, errCh) })
				waitForServerReady(t, srv)
			}
			p := policy
			if tt.nilTenant {
				p = TenantPolicy{}
			}
			before := srv.admission.familyPrefixes()
			// Each substring re-runs the refused call; the record must stay unchanged throughout.
			for _, want := range tt.want {
				assertRegistrationPanics(t, want, func() { TenantOwnedGroup(parent, tt.prefix, p) })
			}
			assert.Equal(t, before, srv.admission.familyPrefixes(), "a refused call records no family")
		})
	}
}

func TestTenantOwnedGroupRefusesWhenMultitenantDisabled(t *testing.T) {
	srv := newTenantFamilyServerWithConfig(t, newProbeTestConfig("/api"))
	build := func() { TenantOwnedGroup(srv.ModuleGroup(), "/fam", fixedTenantPolicy("family-tenant")) }

	assertRegistrationPanics(t, strconv.Quote("/api/fam"), build)
	assertRegistrationPanics(t, "multitenant.enabled", build)
	assert.Empty(t, srv.admission.familyPrefixes())
}

// TestTenantOwnedGroupRefusesWithoutTenantSeat pins that a family refuses a server whose tenant
// seat New never installed (here an empty resolver type on a config that skipped Validate):
// its leaf guard would refuse every request, so registration fails fast instead.
func TestTenantOwnedGroupRefusesWithoutTenantSeat(t *testing.T) {
	cfg := newTenantFamilyConfig("")
	cfg.Multitenant.Resolver.Type = ""
	srv := newTenantFamilyServerWithConfig(t, cfg)
	build := func() { TenantOwnedGroup(srv.ModuleGroup(), "/fam", fixedTenantPolicy(familyTenant)) }

	assertRegistrationPanics(t, "tenant seat is not installed", build)
	assert.Empty(t, srv.admission.familyPrefixes())
}

func TestTenantOwnedGroupAllowsSegmentSiblingPrefix(t *testing.T) {
	srv := newTenantFamilyServer(t, "")
	prefixes := []string{"/partner/v2", "/partner/v20"}
	tenants := []string{"partner-a", "partner-b"}
	for i, prefix := range prefixes {
		TenantOwnedGroup(srv.ModuleGroup(), prefix, fixedTenantPolicy(tenants[i])).
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
	traceNextNil      = "next_returned_nil"

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

// forbiddingHandler records traceHandler, then returns a 403 with msg, committing nothing.
func forbiddingHandler(tr *familyTrace, msg string) Handler {
	return func(c HandlerContext) error {
		tr.record(traceHandler, c)
		return NewForbiddenError(msg)
	}
}

// recordingStep is a middleware recording stage, then calling next.
func recordingStep(tr *familyTrace, stage string) MiddlewareFunc {
	return func(c HandlerContext, next func() error) error {
		tr.record(stage, c)
		return next()
	}
}

func recordingMiddleware(tr *familyTrace) MiddlewareFunc { return recordingStep(tr, traceMiddleware) }

// passThrough is a middleware calling next.
func passThrough(_ HandlerContext, next func() error) error { return next() }

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
	family := TenantOwnedGroup(srv.ModuleGroup(), "/fam", policy)
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

// newNotFoundFamilies builds the recording family /fam, with recording Use middleware when use
// is set, a nested group and two wildcard routes; /t, whose Tenant reads the org path
// parameter: empty on the family's catch-all; and /bare, whose one route is GET at its bare
// prefix, under a foreign POST, HEAD and OPTIONS /:seg.
func newNotFoundFamilies(t *testing.T, use bool) (*Server, *familyTrace) {
	t.Helper()
	srv, family, tr := newRecordingFamily(t)
	if use {
		family.Use(recordingMiddleware(tr))
	}
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
	orgFamily := TenantOwnedGroup(srv.ModuleGroup(), "/t", orgPolicy)
	orgFamily.Add(http.MethodGet, "/:org/orders", recordingHandler(tr))
	TenantOwnedGroup(srv.ModuleGroup(), "/bare", recordingPolicy(tr)).Add(http.MethodGet, "", recordingHandler(tr))
	foreign := func(c HandlerContext) error {
		tr.record(traceForeign, c)
		return c.String(http.StatusOK, "foreign")
	}
	for _, method := range []string{http.MethodPost, http.MethodHead, http.MethodOptions} {
		srv.ModuleGroup().Add(method, "/:seg", foreign)
	}
	return srv, tr
}

// notFoundCase is one TestTenantOwnedGroupAnswersNotFoundAfterTenant case.
type notFoundCase struct {
	name       string
	method     string
	path       string
	credential bool
	use        bool     // the family's Use middleware precedes /sub/items, which then carries it
	wantDeny   bool     // the family's Deny answers; otherwise the framework 404, after the tenant
	sameAs     string   // a known path whose anonymous answer this one equals byte for byte
	control    string   // a known path the same caller resolves, so Deny is not the only answer
	stepsOn    []string // instead of method: each sent anonymous, then resolved (assertStepsAnswer)
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
			srv, tr := newNotFoundFamilies(t, tt.use)
			if len(tt.stepsOn) > 0 {
				assertStepsAnswer(t, srv, tr, tt.path, tt.stepsOn)
				return
			}
			var edits []func(*http.Request)
			if tt.credential {
				edits = append(edits, withFamilyCredential)
			}

			rec := serveFamily(srv, tt.method, tt.path, edits...)
			tt.assertAnswer(t, srv, tr, rec, edits)
		})
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

// assertMiddlewareRuns sends GET path with the credential and asserts the recording middleware
// ran after the publish, seeing the family's tenant, then the handler answered.
func assertMiddlewareRuns(t *testing.T, srv *Server, tr *familyTrace, path string) {
	t.Helper()
	rec := serveFamily(srv, http.MethodGet, path, withFamilyCredential)
	assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, []string{traceAuthenticate, traceTenant, traceMiddleware, traceHandler}, tr.events)
	assert.Equal(t, familyTenant, tr.tenants[traceMiddleware])
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
				assertMiddlewareRuns(t, srv, tr, "/fam/mw")
			},
		},
		{
			name: "use_middleware_sees_tenant",
			check: func(t *testing.T, srv *Server, family RouteRegistrar, tr *familyTrace) {
				family.Use(recordingMiddleware(tr))
				family.Add(http.MethodGet, "/used", recordingHandler(tr))
				assertMiddlewareRuns(t, srv, tr, "/fam/used")
			},
		},
		{
			name: "nested_middleware_sees_tenant",
			check: func(t *testing.T, srv *Server, family RouteRegistrar, tr *familyTrace) {
				family.Group("/sub", recordingMiddleware(tr)).Add(http.MethodGet, "/items", recordingHandler(tr))
				assertMiddlewareRuns(t, srv, tr, "/fam/sub/items")
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
				family.Add(http.MethodGet, "/forbidden", forbiddingHandler(tr, "not your order"))
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
	TenantOwnedGroup(srv.ModuleGroup(), "/fam", recordingPolicy(tr)).Add(http.MethodGet, "/orders", recordingHandler(tr))

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
	TenantOwnedGroup(srv.ModuleGroup(), "/fam", policy).Add(http.MethodGet, "/orders", recordingHandler(tr))
	policy.Authenticate[0] = recordingMiddleware(tr)

	assertFamilyDenied(t, serveFamily(srv, http.MethodGet, "/fam/orders"), tr)
	assert.Equal(t, []string{traceAuthenticate, traceDeny}, tr.events, "the family runs the Authenticate step it was built with")
}

func TestTenantOwnedGroupRegistersOneCatchAllPair(t *testing.T) {
	srv := newTenantFamilyServer(t, "")
	// The parent's middleware gives it its own catch-all pair at /p and /p/*.
	parent := srv.ModuleGroup().Group("/p", passThrough)
	family := TenantOwnedGroup(parent, "/fam", fixedTenantPolicy(familyTenant))
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
	family := TenantOwnedGroup(srv.ModuleGroup(), "/fam", fixedTenantPolicy(familyTenant))
	require.NotPanics(t, func() {
		family.Add(http.MethodGet, "/files/*", answerWith("get"))
		family.Add(http.MethodPost, "/files/*", answerWith("post"))
		family.Add(http.MethodGet, "/x/:id/*", answerWith("id"))
		// The same echo node as /x/:id/* under another method, so admission takes it too.
		family.Add(http.MethodPost, "/x/:org/*", answerWith("org"))
		family.Group("/sub").Add(http.MethodGet, "/*", answerWith("sub"))
		family.Add(http.MethodGet, "/*", answerWith("root")) // the node of the prefix's own catch-all
		GET(NewHandlerRegistry(srv.cfg), family, "/typed/*", echoFamilyOrder)
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
	TenantOwnedGroup(srv.ModuleGroup(), "/fam", fixedTenantPolicy("Acme")).Add(http.MethodGet, "/orders", answerWith("orders"))

	assertFrameworkError(t, serveFamilyRequest(srv, "/fam/orders"), http.StatusInternalServerError, errCodeInternalError)
	refusals := refusalEntries(testLog, reasonGrammarBackstop)
	require.Len(t, refusals, 1, "the grammar backstop's refusal reaches the Server's logger")
	assert.Equal(t, "error", refusals[0].level)
}

// refusalEntries returns log's family refusal lines carrying reason.
func refusalEntries(log *testLogger, reason string) []testLogEntry {
	var out []testLogEntry
	for _, entry := range log.logEntries() {
		if strings.Contains(entry.msg, "reason="+strconv.Quote(reason)) {
			out = append(out, entry)
		}
	}
	return out
}

func TestTenantOwnedGroupRunsStepsInOrder(t *testing.T) {
	srv, family, tr := newRecordingFamily(t, func(p *TenantPolicy, tr *familyTrace) {
		p.Authenticate = []MiddlewareFunc{recordingStep(tr, "authenticate_1"), recordingStep(tr, "authenticate_2")}
	})
	family.Use(func(c HandlerContext, next func() error) error {
		tr.record("publish", c)
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
	assert.Equal(t, familyTenant, tr.tenants["publish"], "the first stored middleware observes the publish: the family's tenant, not the seat's")
}

// The committing step's answer, which must stand byte for byte.
const (
	stepCommitStatus = http.StatusUnauthorized
	stepCommitBody   = "step-bytes"
)

// assertStepCommitLogged asserts srv logged one step-committed refusal at ERROR for GET
// /fam/orders, carrying no seat-tenant header value.
func assertStepCommitLogged(t *testing.T, srv *Server) {
	t.Helper()
	log, ok := srv.logger.(*testLogger)
	require.True(t, ok, "newTenantFamilyServer logs to a testLogger")
	entries := refusalEntries(log, reasonStepCommitted)
	require.Len(t, entries, 1, "one wiring-fault line: %v", log.logEntries())
	assert.Equal(t, "error", entries[0].level)
	assert.Contains(t, entries[0].msg, `path="/fam/orders"`)
	assert.NotContains(t, entries[0].msg, familySeatTenant, "the log line never carries a header value")
}

// assertCommittedBytesStand asserts rec is the committing step's answer and srv logged the
// wiring fault.
func assertCommittedBytesStand(t *testing.T, srv *Server, rec *httptest.ResponseRecorder) {
	t.Helper()
	assert.Equal(t, stepCommitStatus, rec.Code)
	assert.Equal(t, stepCommitBody, rec.Body.String(), "the committed bytes stand")
	assertStepCommitLogged(t, srv)
}

// noteNextNil calls next and records traceNextNil when it returns nil.
func noteNextNil(tr *familyTrace, c HandlerContext, next func() error) {
	if next() == nil {
		tr.record(traceNextNil, c)
	}
}

// newStepOutcomeFamily builds the recording family with steps as its Authenticate steps, plus
// /fam/refused, whose handler returns a 403 without committing.
func newStepOutcomeFamily(t *testing.T, steps []func(*familyTrace) MiddlewareFunc) (*Server, *familyTrace) {
	t.Helper()
	srv, family, tr := newRecordingFamily(t, func(p *TenantPolicy, tr *familyTrace) {
		p.Authenticate = make([]MiddlewareFunc, len(steps))
		for i, step := range steps {
			p.Authenticate[i] = step(tr)
		}
	})
	family.Add(http.MethodGet, "/refused", forbiddingHandler(tr, "refused"))
	return srv, tr
}

func TestTenantOwnedGroupAuthenticateStepOutcomes(t *testing.T) {
	tests := []struct {
		name       string
		path       string // default /fam/orders
		steps      []func(*familyTrace) MiddlewareFunc
		wantEvents []string
		check      func(t *testing.T, srv *Server, rec *httptest.ResponseRecorder, tr *familyTrace)
	}{
		{
			name: "step_commits_response_is_wiring_fault",
			steps: []func(*familyTrace) MiddlewareFunc{func(tr *familyTrace) MiddlewareFunc {
				return func(c HandlerContext, _ func() error) error {
					tr.record(traceAuthenticate, c)
					return c.String(stepCommitStatus, stepCommitBody)
				}
			}},
			wantEvents: []string{traceAuthenticate},
			check: func(t *testing.T, srv *Server, rec *httptest.ResponseRecorder, _ *familyTrace) {
				assertCommittedBytesStand(t, srv, rec)
			},
		},
		{
			name: "step_commits_then_calls_next_gets_nil",
			steps: []func(*familyTrace) MiddlewareFunc{
				func(tr *familyTrace) MiddlewareFunc {
					return func(c HandlerContext, next func() error) error {
						tr.record(traceAuthenticate, c)
						err := c.String(stepCommitStatus, stepCommitBody)
						noteNextNil(tr, c, next)
						return err
					}
				},
				func(tr *familyTrace) MiddlewareFunc { return recordingStep(tr, "authenticate_2") },
			},
			wantEvents: []string{traceAuthenticate, traceNextNil},
			check: func(t *testing.T, srv *Server, rec *httptest.ResponseRecorder, _ *familyTrace) {
				assertCommittedBytesStand(t, srv, rec)
			},
		},
		{
			name: "step_commits_and_returns_error_skips_deny",
			steps: []func(*familyTrace) MiddlewareFunc{func(tr *familyTrace) MiddlewareFunc {
				return func(c HandlerContext, _ func() error) error {
					tr.record(traceAuthenticate, c)
					return errors.Join(c.String(stepCommitStatus, stepCommitBody),
						fmt.Errorf("committed then refused: %w", multitenant.ErrTenantResolutionFailed))
				}
			}},
			wantEvents: []string{traceAuthenticate},
			check: func(t *testing.T, srv *Server, rec *httptest.ResponseRecorder, _ *familyTrace) {
				assertCommittedBytesStand(t, srv, rec)
			},
		},
		{
			name: "step_calls_next_twice_runs_tenant_once",
			path: "/fam/refused", // its handler commits nothing, so only the re-entry check stops a second pass
			steps: []func(*familyTrace) MiddlewareFunc{func(tr *familyTrace) MiddlewareFunc {
				return func(c HandlerContext, next func() error) error {
					tr.record(traceAuthenticate, c)
					first := next()
					noteNextNil(tr, c, next)
					return first
				}
			}},
			wantEvents: []string{traceAuthenticate, traceTenant, traceHandler, traceNextNil},
			check: func(t *testing.T, _ *Server, rec *httptest.ResponseRecorder, _ *familyTrace) {
				assertFrameworkError(t, rec, http.StatusForbidden, errCodeForbidden)
			},
		},
		{
			name: "step_returns_nil_without_next_gets_500",
			steps: []func(*familyTrace) MiddlewareFunc{func(tr *familyTrace) MiddlewareFunc {
				return func(c HandlerContext, _ func() error) error {
					tr.record(traceAuthenticate, c)
					return nil
				}
			}},
			wantEvents: []string{traceAuthenticate},
			check: func(t *testing.T, _ *Server, rec *httptest.ResponseRecorder, _ *familyTrace) {
				assertFrameworkError(t, rec, http.StatusInternalServerError, errCodeInternalError)
			},
		},
		{
			name:       "step_returns_401_goes_to_deny",
			steps:      []func(*familyTrace) MiddlewareFunc{fakeBearerStep},
			wantEvents: []string{traceAuthenticate, traceDeny},
			check: func(t *testing.T, _ *Server, rec *httptest.ResponseRecorder, tr *familyTrace) {
				assertFamilyDenied(t, rec, tr)
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv, tr := newStepOutcomeFamily(t, tt.steps)

			// Anonymous: only fakeBearerStep reads the credential, and it must deny.
			rec := serveFamily(srv, http.MethodGet, cmp.Or(tt.path, "/fam/orders"))
			tt.check(t, srv, rec, tr)
			assert.Equal(t, tt.wantEvents, tr.events, tt.name)
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
				assertMiddlewareRuns(t, srv, tr, "/fam/later")
			},
		},
		{
			name: "use_reaches_later_route_in_earlier_nested_group",
			check: func(t *testing.T, srv *Server, family RouteRegistrar, tr *familyTrace) {
				sub := family.Group("/sub")
				family.Use(recordingMiddleware(tr))
				sub.Add(http.MethodGet, "/later", recordingHandler(tr))
				assertMiddlewareRuns(t, srv, tr, "/fam/sub/later")
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
				want := []string{echo.RouteAny + " /fam", echo.RouteNotFound + " /fam/*"}
				family.Use(recordingMiddleware(tr))
				assert.ElementsMatch(t, want, catchAllRoutes(srv, "/fam"), "after Use")
				family.Group("/sub", recordingMiddleware(tr)).Add(http.MethodGet, "/items", recordingHandler(tr))
				assert.ElementsMatch(t, want, catchAllRoutes(srv, "/fam"), "after a nested group with middleware")
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
				POST(NewHandlerRegistry(srv.cfg), family.Group("/sub", passThrough), "/typed", echoFamilyOrder)
				return "/fam/sub/typed"
			},
			want: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := newTenantFamilyServer(t, "")
			family := TenantOwnedGroup(srv.ModuleGroup(), "/fam", fixedTenantPolicy(familyTenant))
			path := tt.register(srv, family)

			routes := DefaultRouteRegistry.ByPath(path)
			require.Len(t, routes, 1)
			assert.Equal(t, tt.want, routes[0].TenantOwned)
		})
	}
}

// TestTenantOwnedGuardRequiresSeatSkipEvidence pins, white-box, that a family's leaf guard
// runs the handler only with the seat's skip evidence and the tenant the family's steps
// published; anything else is the framework 500, and the handler never runs.
func TestTenantOwnedGuardRequiresSeatSkipEvidence(t *testing.T) {
	published := multitenant.SetTenant(context.Background(), familyTenant)
	tests := []struct {
		name          string
		evidence      bool
		published     context.Context // familyPublishedKey's value; nil sets none
		requestTenant string
		wantRun       bool
		wantReason    string
	}{
		{name: "missing_evidence", published: published, requestTenant: familyTenant, wantReason: reasonSeatNotSkipped},
		{name: "published_context_missing", evidence: true, requestTenant: familyTenant, wantReason: reasonTenantReplaced},
		{name: "neither_context_holds_tenant", evidence: true, published: context.Background(), wantReason: reasonTenantReplaced},
		{name: "request_holds_another_tenant", evidence: true, published: published, requestTenant: "other-tenant", wantReason: reasonTenantReplaced},
		{name: "evidence_and_published_tenant_run", evidence: true, published: published, requestTenant: familyTenant, wantRun: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			if tt.requestTenant != "" {
				ctx = multitenant.SetTenant(ctx, tt.requestTenant)
			}
			req := httptest.NewRequestWithContext(ctx, http.MethodGet, "/fam/orders", http.NoBody)
			hc := NewHandlerContextForTest(httptest.NewRecorder(), req, newTenantFamilyConfig(""), WithRouteTemplate("/fam/orders"))
			if tt.evidence {
				hc.Set(seatSkippedKey, true)
			}
			if tt.published != nil {
				hc.Set(familyPublishedKey, tt.published)
			}

			ran := false
			fam := &tenantFamily{log: &testLogger{}}
			err := fam.guard(func(*echo.Context) error { ran = true; return nil })(hc.ectx)

			assert.Equal(t, tt.wantRun, ran)
			if tt.wantRun {
				require.NoError(t, err)
				return
			}
			var fault *tenantFaultError
			require.ErrorAs(t, err, &fault)
			assert.Equal(t, http.StatusInternalServerError, fault.HTTPStatus())
			assert.Equal(t, tt.wantReason, fault.reason)
		})
	}
}

// TestTenantOwnedGuardRequiresPublishedTenant pins that on every kind of family route, a
// middleware that replaces the published tenant gets the framework 500 and the handler never
// runs. A middleware that only enriches the context keeps the tenant and runs it.
func TestTenantOwnedGuardRequiresPublishedTenant(t *testing.T) {
	type enrichKey struct{}
	enrich := func(c HandlerContext, next func() error) error {
		c.SetRequestContext(context.WithValue(c.RequestContext(), enrichKey{}, "value"))
		return next()
	}
	tests := []struct {
		name    string
		path    string
		build   func(family RouteRegistrar, hr *HandlerRegistry, h Handler)
		wantRun bool
	}{
		{name: "use_middleware_replaces_tenant", path: "/fam/x", build: func(f RouteRegistrar, _ *HandlerRegistry, h Handler) {
			f.Use(replaceFamilyTenant)
			f.Add(http.MethodGet, "/x", h)
		}},
		{name: "route_middleware_replaces_tenant", path: "/fam/x", build: func(f RouteRegistrar, _ *HandlerRegistry, h Handler) {
			f.Add(http.MethodGet, "/x", h, replaceFamilyTenant)
		}},
		{name: "typed_route", path: "/fam/typed/x", build: func(f RouteRegistrar, hr *HandlerRegistry, h Handler) {
			GET(hr, f.Group("/typed", replaceFamilyTenant), "/x", func(_ EmptyRequest, c HandlerContext) (helloResp, IAPIError) {
				_ = h(c)
				return helloResp{}, nil
			})
		}},
		{name: "raw_add_route", path: "/fam/raw/x", build: func(f RouteRegistrar, _ *HandlerRegistry, h Handler) {
			f.Group("/raw", replaceFamilyTenant).Add(http.MethodGet, "/x", h)
		}},
		{name: "nested_group_route", path: "/fam/a/b/x", build: func(f RouteRegistrar, _ *HandlerRegistry, h Handler) {
			f.Group("/a").Group("/b", replaceFamilyTenant).Add(http.MethodGet, "/x", h)
		}},
		{name: "enriched_context_runs", path: "/fam/x", wantRun: true, build: func(f RouteRegistrar, _ *HandlerRegistry, h Handler) {
			f.Use(enrich)
			f.Add(http.MethodGet, "/x", h)
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := newTenantFamilyServer(t, "")
			family := TenantOwnedGroup(srv.ModuleGroup(), "/fam", fixedTenantPolicy(familyTenant))
			ran := false
			ok := answerWith("ok")
			tt.build(family, NewHandlerRegistry(srv.cfg), func(c HandlerContext) error {
				ran = true
				return ok(c)
			})

			rec := serveFamilyRequest(srv, tt.path)

			assert.Equal(t, tt.wantRun, ran)
			if tt.wantRun {
				assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
				return
			}
			assertTenantReplacedRefusal(t, srv, rec)
		})
	}
}

// replaceFamilyTenant is a middleware that replaces the tenant the family's steps published.
func replaceFamilyTenant(c HandlerContext, next func() error) error {
	c.SetRequestContext(multitenant.SetTenant(c.RequestContext(), "other-tenant"))
	return next()
}

// TestTenantOwnedGuardRunsBeforeBindingAndJOSE pins that the leaf guard sits outside the typed
// leaf: with the published tenant replaced, a body that fails binding and an envelope JOSE
// refuses both get the guard's framework 500, never binding's or JOSE's 400, and the handler
// never runs.
func TestTenantOwnedGuardRunsBeforeBindingAndJOSE(t *testing.T) {
	tests := []struct {
		name  string
		path  string
		body  func(*http.Request)
		build func(t *testing.T, srv *Server, family RouteRegistrar, ran *bool)
	}{
		{
			name: "guard_runs_before_binding",
			path: "/fam/typed",
			body: withMalformedJSON,
			build: func(_ *testing.T, srv *Server, family RouteRegistrar, ran *bool) {
				POST(NewHandlerRegistry(srv.cfg), family, "/typed", func(req familyOrderRequest, _ HandlerContext) (familyOrderResponse, IAPIError) {
					*ran = true
					return familyOrderResponse(req), nil
				})
			},
		},
		{
			name: "guard_runs_before_jose",
			path: "/fam/tokens",
			body: withFamilyBody(jose.ContentType, "not-a-jwe"),
			build: func(t *testing.T, srv *Server, family RouteRegistrar, ran *bool) {
				hr := NewHandlerRegistry(srv.cfg, WithJOSEResolver(newJOSEFixture(t).resolver))
				POST(hr, family, "/tokens", func(_ taggedReq, _ HandlerContext) (taggedResp, IAPIError) {
					*ran = true
					return taggedResp{}, nil
				})
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := newTenantFamilyServer(t, "")
			family := TenantOwnedGroup(srv.ModuleGroup(), "/fam", fixedTenantPolicy(familyTenant))
			family.Use(replaceFamilyTenant)
			ran := false
			tt.build(t, srv, family, &ran)

			rec := serveFamily(srv, http.MethodPost, tt.path, tt.body)

			assert.False(t, ran, "the handler never runs")
			assertTenantReplacedRefusal(t, srv, rec)
		})
	}
}

// TestTenantOwnedRefusalLogsCarryReasonClassOnly pins, end to end through New and across
// every family refusal reason, that a refusal logs one line carrying its reason class and the
// matched template, and never the tenant or a request header's bytes. Other tests pin each
// reason's trigger; this one pins the line's shape for all of them.
func TestTenantOwnedRefusalLogsCarryReasonClassOnly(t *testing.T) {
	const (
		secretTenant = "refusal-tenant-secret"
		secretHeader = "refusal-header-secret"
	)
	clientFault := func(HandlerContext) (string, error) {
		return "", fmt.Errorf("%w: %s", multitenant.ErrTenantResolutionFailed, secretHeader)
	}
	tests := []struct {
		name       string
		policy     TenantPolicy
		use        MiddlewareFunc // the family's own middleware; nil for none
		wantStatus int
		reason     string
	}{
		{
			name:   "guard_missing_evidence",
			policy: fixedTenantPolicy(secretTenant),
			use: func(c HandlerContext, next func() error) error {
				c.Set(seatSkippedKey, false)
				return next()
			},
			wantStatus: http.StatusInternalServerError, reason: reasonSeatNotSkipped,
		},
		{
			name:   "guard_replaced_tenant",
			policy: fixedTenantPolicy(secretTenant),
			use: func(c HandlerContext, next func() error) error {
				c.SetRequestContext(multitenant.SetTenant(c.RequestContext(), secretTenant+"-other"))
				return next()
			},
			wantStatus: http.StatusInternalServerError, reason: reasonTenantReplaced,
		},
		{
			name:       "grammar_backstop",
			policy:     fixedTenantPolicy(secretTenant + " fails the grammar"),
			wantStatus: http.StatusInternalServerError, reason: reasonGrammarBackstop,
		},
		{
			name: "deny_returned_error",
			policy: TenantPolicy{Tenant: clientFault, Deny: func(HandlerContext, error) error {
				return NewForbiddenError(secretHeader)
			}},
			wantStatus: http.StatusForbidden, reason: reasonDenyError,
		},
		{
			name:       "deny_committed_nothing",
			policy:     TenantPolicy{Tenant: clientFault, Deny: func(HandlerContext, error) error { return nil }},
			wantStatus: http.StatusInternalServerError, reason: reasonDenyNothing,
		},
		{
			name: "authenticate_committed_response",
			policy: TenantPolicy{Tenant: clientFault, Authenticate: []MiddlewareFunc{func(c HandlerContext, next func() error) error {
				if err := c.String(http.StatusUnauthorized, ""); err != nil {
					return err
				}
				return next()
			}}},
			wantStatus: http.StatusUnauthorized, reason: reasonStepCommitted,
		},
		{
			name: "authenticate_committed_nothing",
			policy: TenantPolicy{Tenant: clientFault, Authenticate: []MiddlewareFunc{func(HandlerContext, func() error) error {
				return nil
			}}},
			wantStatus: http.StatusInternalServerError, reason: reasonNoTenantPublished,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := newTenantFamilyServer(t, "")
			family := TenantOwnedGroup(srv.ModuleGroup(), "/fam", tt.policy)
			if tt.use != nil {
				family.Use(tt.use)
			}
			family.Add(http.MethodGet, "/orders/:id", func(c HandlerContext) error { return c.String(http.StatusOK, "ok") })

			rec := serveFamily(srv, http.MethodGet, "/fam/orders/42", func(req *http.Request) {
				req.Header.Set(HeaderXTenantID, secretHeader)
				req.Header.Set(echo.HeaderAuthorization, "Bearer "+secretHeader)
			})

			assert.Equal(t, tt.wantStatus, rec.Code, rec.Body.String())
			refusals := familyRefusals(t, srv)
			require.Len(t, refusals, 1, "one refusal line per request")
			assert.Contains(t, refusals[0], "reason="+strconv.Quote(tt.reason))
			assert.Contains(t, refusals[0], `path="/fam/orders/:id"`)
			assert.NotContains(t, refusals[0], secretTenant)
			assert.NotContains(t, refusals[0], secretHeader)
		})
	}
}

// assertTenantReplacedRefusal asserts rec is the framework 500 and srv logged exactly one
// family refusal, for a tenant replaced after the publish.
func assertTenantReplacedRefusal(t *testing.T, srv *Server, rec *httptest.ResponseRecorder) {
	t.Helper()
	assertFrameworkError(t, rec, http.StatusInternalServerError, errCodeInternalError)
	refusals := familyRefusals(t, srv)
	require.Len(t, refusals, 1)
	assert.Contains(t, refusals[0], "reason="+strconv.Quote(reasonTenantReplaced))
}

// familyRefusals returns the family refusal lines srv's logger recorded.
func familyRefusals(t *testing.T, srv *Server) []string {
	t.Helper()
	log, ok := srv.logger.(*testLogger)
	require.True(t, ok)
	var out []string
	for _, e := range log.logEntries() {
		if strings.Contains(e.msg, "[server.tenant] family request refused") {
			out = append(out, e.msg)
		}
	}
	return out
}
