package server

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"testing/synctest"

	"github.com/labstack/echo/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/config"
	"github.com/gaborage/go-bricks/internal/testutil"
	"github.com/gaborage/go-bricks/jose"
	jositest "github.com/gaborage/go-bricks/jose/testing"
	"github.com/gaborage/go-bricks/multitenant"
)

const (
	// admissionKnownTenant is the one tenant admissionChecker holds.
	admissionKnownTenant = "acme"
	// admissionUnknownTenant is a well-formed id the checker does not hold.
	admissionUnknownTenant = "unlisted-tenant"
	// admissionHeaderOnly is a header tenant the checker does not hold.
	admissionHeaderOnly = "header-only"
)

// admissionChecker holds admissionKnownTenant alone.
func admissionChecker() testutil.TenantSet { return testutil.TenantSet{admissionKnownTenant: {}} }

// newAdmissionServer is a Server reading the tenant from X-Tenant-ID first, then path segment 2
// (/tenants/<id>/…), with checker vouching for path-read ids. tune adjusts the config first.
func newAdmissionServer(t *testing.T, log *testLogger, checker multitenant.TenantChecker, tune func(*config.Config)) *Server {
	t.Helper()
	cfg := newTestConfig("", "", "")
	cfg.Multitenant.Enabled = true
	cfg.Multitenant.Resolver.Type = config.ResolverTypeComposite
	cfg.Multitenant.Resolver.Order = []string{config.ResolverTypeHeader, config.ResolverTypePath}
	cfg.Multitenant.Resolver.Path.Segment = 2
	if tune != nil {
		tune(cfg)
	}
	return NewWithOptions(cfg, log, Options{TenantChecker: checker})
}

// admissionProbe records what the handler and the global middleware saw.
type admissionProbe struct {
	handlerRuns  int
	handlerID    string
	globalTenant []string // the tenant each request carried in global middleware ("" for none)
}

// admissionBody binds a JSON body with one required field.
type admissionBody struct {
	Name string `json:"name" validate:"required"`
}

// registerAdmissionRoutes adds a typed and a raw route under /tenants/:tenant, a typed POST
// that binds a body, and a global middleware recording the request's tenant.
func registerAdmissionRoutes(srv *Server, log *testLogger, probe *admissionProbe) {
	srv.RegisterGlobalMiddleware(func(c HandlerContext, next func() error) error {
		id, _ := multitenant.GetTenant(c.RequestContext())
		probe.globalTenant = append(probe.globalTenant, id)
		return next()
	})
	hr := NewHandlerRegistry(srv.cfg, WithLogger(log))
	record := func(c HandlerContext) {
		probe.handlerRuns++
		probe.handlerID, _ = multitenant.GetTenant(c.RequestContext())
	}
	GET(hr, srv.ModuleGroup(), "/tenants/:tenant/orders/x", func(_ EmptyRequest, c HandlerContext) (helloResp, IAPIError) {
		record(c)
		return helloResp{Message: "ok"}, nil
	})
	POST(hr, srv.ModuleGroup(), "/tenants/:tenant/orders", func(_ admissionBody, c HandlerContext) (helloResp, IAPIError) {
		record(c)
		return helloResp{Message: "ok"}, nil
	})
	srv.ModuleGroup().Add(http.MethodGet, "/tenants/:tenant/raw", func(c HandlerContext) error {
		record(c)
		return c.String(http.StatusOK, "ok")
	})
}

// serveAdmission sends one request, with body under contentType when contentType is set.
func serveAdmission(srv *Server, method, target, headerTenant, contentType, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequestWithContext(context.Background(), method, target, strings.NewReader(body))
	if contentType != "" {
		req.Header.Set(echo.HeaderContentType, contentType)
	}
	if headerTenant != "" {
		req.Header.Set(HeaderXTenantID, headerTenant)
	}
	rec := httptest.NewRecorder()
	srv.echo.ServeHTTP(rec, req)
	return rec
}

// assertInvalidTenantEnvelope checks the 400 BAD_REQUEST "Invalid tenant" envelope.
func assertInvalidTenantEnvelope(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	assertFrameworkError(t, rec, http.StatusBadRequest, errCodeBadRequest)
	var resp APIResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp), rec.Body.String())
	assert.Equal(t, msgInvalidTenant, resp.Error.Message)
}

// allLogText flattens every recorded line, message and field values, for a needle scan.
func allLogText(log *testLogger) string {
	var b strings.Builder
	for _, e := range log.logEntries() {
		b.WriteString(e.msg)
		for _, v := range slices.Sorted(maps.Keys(e.values)) {
			b.WriteString(" " + v + "=" + e.values[v])
		}
		b.WriteString("\n")
	}
	return b.String()
}

// TestPathTenantAdmissionWritesOnlyHeldPathTenants pins who gets a tenant: a path tenant the
// checker holds is written as on main; a header tenant is never asked; with no checker no path
// tenant is held (fail closed); and a request no route matched keeps its 404, since only a
// handler adapter refuses.
func TestPathTenantAdmissionWritesOnlyHeldPathTenants(t *testing.T) {
	tests := []struct {
		name       string
		target     string
		header     string
		checker    multitenant.TenantChecker
		wantStatus int
		wantID     string // the tenant the handler saw; "" when it did not run
	}{
		{name: "known_path_tenant", target: "/tenants/" + admissionKnownTenant + "/orders/x", checker: admissionChecker(), wantStatus: http.StatusOK, wantID: admissionKnownTenant},
		{name: "header_tenant_never_asked", target: "/tenants/" + admissionUnknownTenant + "/orders/x", header: admissionHeaderOnly, checker: admissionChecker(), wantStatus: http.StatusOK, wantID: admissionHeaderOnly},
		{name: "no_checker_holds_none", target: "/tenants/" + admissionKnownTenant + "/orders/x", wantStatus: http.StatusBadRequest},
		{name: "unmatched_keeps_404", target: "/tenants/" + admissionUnknownTenant + "/nowhere", checker: admissionChecker(), wantStatus: http.StatusNotFound},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			log := &testLogger{}
			probe := &admissionProbe{}
			srv := newAdmissionServer(t, log, tc.checker, nil)
			registerAdmissionRoutes(srv, log, probe)

			rec := serveAdmission(srv, http.MethodGet, tc.target, tc.header, "", "")

			require.Equal(t, tc.wantStatus, rec.Code, rec.Body.String())
			assert.Equal(t, tc.wantID, probe.handlerID)
			assert.Equal(t, tc.wantID != "", probe.handlerRuns == 1, "the handler runs only for a written tenant")
			warns, _ := splitTenantRefusalLogs(log.logEntries())
			assert.Equal(t, tc.wantStatus == http.StatusBadRequest, len(warns) == 1, "only the adapter's refusal WARNs")
		})
	}
}

// TestPathTenantAdmissionUnknownTenantAnswersFromAdapter pins the refusal: a path tenant the
// checker does not hold is never written. Global middleware runs
// with no tenant, the handler never runs, the typed and raw adapters answer the 400 "Invalid
// tenant" envelope with one masked WARN, the access line records the 400 with no tenant field,
// and no line carries the id.
func TestPathTenantAdmissionUnknownTenantAnswersFromAdapter(t *testing.T) {
	tests := []struct {
		name   string
		method string
		route  string // the target after /tenants/<id>
		body   string
	}{
		{name: "typed_route", method: http.MethodGet, route: "orders/x"},
		{name: "raw_route", method: http.MethodGet, route: "raw"},
		// The refusal comes before binding: a body that fails to bind still gets the tenant 400.
		{name: "typed_route_before_binding", method: http.MethodPost, route: "orders", body: `{"name":`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			log := &testLogger{}
			probe := &admissionProbe{}
			srv := newAdmissionServer(t, log, admissionChecker(), nil)
			registerAdmissionRoutes(srv, log, probe)
			contentType := ""
			if tc.body != "" {
				contentType = echo.MIMEApplicationJSON
			}

			rec := serveAdmission(srv, tc.method, "/tenants/"+admissionUnknownTenant+"/"+tc.route, "", contentType, tc.body)

			assertInvalidTenantEnvelope(t, rec)
			assert.Zero(t, probe.handlerRuns, "the handler never runs for an unknown path tenant")
			assert.Equal(t, []string{""}, probe.globalTenant, "global middleware runs with no tenant (ADR-170 reverses ADR-036 here)")
			warns, unhandled := splitTenantRefusalLogs(log.logEntries())
			assert.Empty(t, unhandled)
			require.Len(t, warns, 1, "one tenant WARN per refusal")
			assert.Contains(t, warns[0].msg, `reason="unknown tenant"`)
			assert.Contains(t, warns[0].msg, fmt.Sprintf("path=%q", "/tenants/"+maskToken+"/"+tc.route))
			var access []testLogEntry
			for _, e := range log.logEntries() {
				if strings.Contains(e.msg, warnAccessLog4xx) {
					access = append(access, e)
				}
			}
			require.Len(t, access, 1, "the refused request keeps its 4xx access line")
			assert.NotContains(t, access[0].values, "tenant", "no tenant was written, so none is logged")
			assert.NotContains(t, allLogText(log), admissionUnknownTenant, "no framework line carries the unknown id")
			assert.NotContains(t, rec.Body.String(), admissionUnknownTenant)
		})
	}
}

// TestPathTenantAdmissionAuthGroupAnswers401ForEveryID pins the ordering: route-group
// middleware (auth) runs before the adapter refuses, so an unauthenticated caller gets the same
// 401 for a known and an unknown id.
func TestPathTenantAdmissionAuthGroupAnswers401ForEveryID(t *testing.T) {
	log := &testLogger{}
	srv := newAdmissionServer(t, log, admissionChecker(), nil)
	requireAuth := func(c HandlerContext, next func() error) error {
		if c.RequestHeader(echo.HeaderAuthorization) == "" {
			return c.JSON(http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		}
		return next()
	}
	hr := NewHandlerRegistry(srv.cfg, WithLogger(log))
	secured := srv.ModuleGroup().Group("/tenants/:tenant/secure", requireAuth)
	GET(hr, secured, "/x", func(_ EmptyRequest, _ HandlerContext) (helloResp, IAPIError) {
		return helloResp{Message: "ok"}, nil
	})

	known := serveAdmission(srv, http.MethodGet, "/tenants/"+admissionKnownTenant+"/secure/x", "", "", "")
	unknown := serveAdmission(srv, http.MethodGet, "/tenants/"+admissionUnknownTenant+"/secure/x", "", "", "")

	assert.Equal(t, http.StatusUnauthorized, known.Code)
	assert.Equal(t, http.StatusUnauthorized, unknown.Code)
	assert.Equal(t, known.Body.String(), unknown.Body.String())
	warns, _ := splitTenantRefusalLogs(log.logEntries())
	assert.Empty(t, warns, "the auth group answered before the adapter")
}

// TestPathTenantAdmissionDrainedClientGetsSame429 pins the limiter: under app.rate.limit 1 (burst
// 2), a client that drained its bucket gets the same 429 for a known and an unknown path tenant,
// since the limiter keys on the client address alone (ADR-160) and runs before the adapter.
func TestPathTenantAdmissionDrainedClientGetsSame429(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		log := &testLogger{}
		srv := newAdmissionServer(t, log, admissionChecker(), func(cfg *config.Config) { cfg.App.Rate.Limit = 1 })
		registerAdmissionRoutes(srv, log, &admissionProbe{})
		for range 2 {
			serveAdmission(srv, http.MethodGet, "/tenants/"+admissionKnownTenant+"/orders/x", "", "", "")
		}

		known := serveAdmission(srv, http.MethodGet, "/tenants/"+admissionKnownTenant+"/orders/x", "", "", "")
		unknown := serveAdmission(srv, http.MethodGet, "/tenants/"+admissionUnknownTenant+"/orders/x", "", "", "")

		require.Equal(t, http.StatusTooManyRequests, known.Code)
		require.Equal(t, http.StatusTooManyRequests, unknown.Code)
		assert.Equal(t, withoutRequestID(known.Header()), withoutRequestID(unknown.Header()))
		assert.Equal(t, withoutRequestIDBody(t, known), withoutRequestIDBody(t, unknown))
	})
}

// withoutRequestID copies h without the per-request X-Request-Id.
func withoutRequestID(h http.Header) http.Header {
	out := h.Clone()
	out.Del(echo.HeaderXRequestID)
	return out
}

// withoutRequestIDBody decodes a 429 body and drops its request id.
func withoutRequestIDBody(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body), rec.Body.String())
	if errObj, ok := body[fieldError].(map[string]any); ok {
		delete(errObj, fieldRequestID)
	}
	return body
}

// TestPathTenantAdmissionRunsAfterJOSEInbound pins the ordering on a JOSE route: a body that
// fails inbound verification gets the JOSE pre-trust error, not the tenant 400; a verified one
// gets the tenant 400 sealed, and the handler never runs.
func TestPathTenantAdmissionRunsAfterJOSEInbound(t *testing.T) {
	f := newJOSEFixture(t)
	log := &testLogger{}
	srv := newAdmissionServer(t, log, admissionChecker(), nil)
	hr := NewHandlerRegistry(srv.cfg, WithLogger(log), WithJOSEResolver(f.resolver))
	runs := 0
	POST(hr, srv.ModuleGroup(), "/tenants/:tenant/tokens", func(taggedReq, HandlerContext) (taggedResp, IAPIError) {
		runs++
		return taggedResp{}, nil
	})
	sealed := jositest.SealForTest(t, []byte(`{"pan":"test-pan-001"}`), f.peerOutbound(), f.resolver)
	tampered := []byte(sealed)
	tampered[len(tampered)/2] ^= 0x01
	target := "/tenants/" + admissionUnknownTenant + "/tokens"

	preTrust := serveAdmission(srv, http.MethodPost, target, "", jose.ContentType, string(tampered))
	assert.Contains(t, []int{http.StatusBadRequest, http.StatusUnauthorized}, preTrust.Code)
	assert.NotContains(t, preTrust.Body.String(), msgInvalidTenant, "JOSE inbound answers first")

	postTrust := serveAdmission(srv, http.MethodPost, target, "", jose.ContentType, sealed)
	require.Equal(t, http.StatusBadRequest, postTrust.Code)
	require.Equal(t, jose.ContentType, postTrust.Header().Get(echo.HeaderContentType), "the post-trust refusal is sealed")
	plain, _ := jositest.OpenForTest(t, postTrust.Body.String(), f.peerInbound(), f.resolver)
	var envelope map[string]any
	require.NoError(t, json.Unmarshal(plain, &envelope))
	assert.Equal(t, msgInvalidTenant, joseErrorObject(t, envelope)["message"])
	assert.Zero(t, runs)
}

// TestTenantMiddlewareWritesResolvedID pins that TenantMiddleware writes the id its resolver
// returns.
func TestTenantMiddlewareWritesResolvedID(t *testing.T) {
	e := newTenantTestEcho()
	var got string
	e.GET("/tenants/:tenant/orders/x", func(c *echo.Context) error {
		got, _ = multitenant.GetTenant(c.Request().Context())
		return c.String(http.StatusOK, "ok")
	}, adaptMiddleware(TenantMiddleware(&multitenant.PathResolver{Segment: 2}, nil), nil))

	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/tenants/"+admissionUnknownTenant+"/orders/x", http.NoBody))

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, admissionUnknownTenant, got)
}
