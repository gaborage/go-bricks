package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"

	"github.com/gaborage/go-bricks/config"
	"github.com/gaborage/go-bricks/internal/racedetect"
	"github.com/gaborage/go-bricks/logger"
)

// sensitiveTestPAN is the well-known synthetic Visa test PAN; it is not cardholder data.
const (
	sensitiveTestPAN   = "4111111111111111"
	sensitiveCardRoute = "/cards/v1/user/cardInformation/:pan"
	sensitiveCardPath  = "/cards/v1/user/cardInformation/" + sensitiveTestPAN
	maskedCardPath     = "/cards/v1/user/cardInformation/***"
)

// sensitiveLookup is the lookup a Server whose only sensitive route is GET route would hand
// the middleware: the same per-server table, compiled and filled the way RegisterHandler fills it.
func sensitiveLookup(t *testing.T, route string, names ...string) sensitiveParamLookup {
	t.Helper()
	plan, err := compileSensitivePlan(route, names)
	require.NoError(t, err)
	tracker := newRouteConflictTracker()
	tracker.recordSensitive(http.MethodGet, route, plan)
	return tracker.lookupSensitivePlan
}

// maskThroughRouter routes target through a real Echo router (so c.Path is set the way the
// middleware sees it) and returns what maskSensitivePathParams makes of the request's path.
func maskThroughRouter(t *testing.T, route, target string, lookup sensitiveParamLookup) string {
	t.Helper()
	return maskThroughRouterRaw(t, route, target, "", lookup)
}

// maskThroughRouterRaw is maskThroughRouter masking rawPath instead of the request's own path
// when rawPath is non-empty, so a path that does not fit the matched route can be handed in.
func maskThroughRouterRaw(t *testing.T, route, target, rawPath string, lookup sensitiveParamLookup) string {
	t.Helper()
	var got string
	e := echo.New()
	e.GET(route, func(c *echo.Context) error {
		raw := rawPath
		if raw == "" {
			raw = routerPath(c.Request())
		}
		got = maskSensitivePathParams(c, raw, lookup)
		return c.NoContent(http.StatusOK)
	})
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequestWithContext(context.Background(), http.MethodGet, target, http.NoBody))
	require.Equal(t, http.StatusOK, rec.Code, "the request must reach the route")
	return got
}

type sensitiveCardRequest struct {
	PAN string `param:"pan"`
}

func sensitiveCardHandler(sensitiveCardRequest, HandlerContext) (struct{}, IAPIError) {
	return struct{}{}, nil
}

// newSensitiveCardServer builds a real Server and registers sensitiveCardRoute as a typed
// handler declaring ":pan" sensitive — the path a module's routes take.
func newSensitiveCardServer(t *testing.T, log logger.Logger, observability bool) *Server {
	t.Helper()
	cfg, err := config.LoadFromMap(map[string]any{"observability.enabled": observability})
	require.NoError(t, err)
	base := newTestConfig("", "", "")
	cfg.App = base.App
	cfg.Server = base.Server
	srv := New(cfg, log)
	require.NoError(t, srv.setupErr)
	GET(NewHandlerRegistry(cfg), srv.ModuleGroup(), sensitiveCardRoute, sensitiveCardHandler, WithSensitiveParams("pan"))
	return srv
}

func serveSensitiveCard(t *testing.T, srv *Server) {
	t.Helper()
	rec := httptest.NewRecorder()
	srv.echo.ServeHTTP(rec, httptest.NewRequestWithContext(context.Background(), http.MethodGet, sensitiveCardPath, http.NoBody))
	require.Equal(t, http.StatusOK, rec.Code)
}

func TestMaskSensitivePathParams(t *testing.T) {
	tests := []struct {
		name      string
		route     string
		sensitive []string
		target    string
		want      string
	}{
		{
			name: "sensitive_param_masked", route: sensitiveCardRoute, sensitive: []string{"pan"},
			target: sensitiveCardPath, want: maskedCardPath,
		},
		{
			name: "route_without_sensitive_params_unchanged", route: sensitiveCardRoute,
			target: sensitiveCardPath, want: sensitiveCardPath,
		},
		{
			name: "two_sensitive_params_both_masked", route: "/accounts/:account/cards/:pan",
			sensitive: []string{"account", "pan"},
			target:    "/accounts/00123456789012/cards/" + sensitiveTestPAN, want: "/accounts/***/cards/***",
		},
		{
			// echo accepts a repeated name; every segment carrying it is masked.
			name: "repeated_param_masks_every_segment", route: "/cards/:id/transfers/:id",
			sensitive: []string{"id"},
			target:    "/cards/" + sensitiveTestPAN + "/transfers/" + sensitiveTestPAN, want: "/cards/***/transfers/***",
		},
		{
			name: "only_the_declared_param_is_masked", route: "/users/:id/cards/:pan",
			sensitive: []string{"pan"},
			target:    "/users/42/cards/" + sensitiveTestPAN, want: "/users/42/cards/***",
		},
		{
			// A value that also occurs in a literal segment must not mask the literal instead.
			name: "value_repeated_in_a_literal_segment", route: "/v1/cards/:pan", sensitive: []string{"pan"},
			target: "/v1/cards/1", want: "/v1/cards/***",
		},
		{
			name: "percent_encoded_value_masked_whole", route: "/cards/:pan", sensitive: []string{"pan"},
			target: "/cards/41%2F11", want: "/cards/***",
		},
		{
			// The value "1" also prefixes the literal "v1": masking is by position, never by search.
			name: "wildcard_route_masks_param_by_position", route: "/v1/:pin/*", sensitive: []string{"pin"},
			target: "/v1/1/receipt", want: "/v1/***/receipt",
		},
		{
			name: "wildcard_route_masks_an_encoded_value_whole", route: "/files/:pan/*",
			sensitive: []string{"pan"},
			target:    "/files/41%2F11/a", want: "/files/***/a",
		},
		{
			name: "sensitive_wildcard_masks_the_whole_tail", route: "/files/*", sensitive: []string{"*"},
			target: "/files/" + sensitiveTestPAN + "/a/b", want: "/files/***",
		},
		{
			name: "sensitive_param_and_wildcard_both_masked", route: "/v1/:pin/*", sensitive: []string{"pin", "*"},
			target: "/v1/1/receipt/42", want: "/v1/***/***",
		},
		{
			name: "sensitive_wildcard_with_empty_tail", route: "/files/*", sensitive: []string{"*"},
			target: "/files/", want: "/files/***",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, maskThroughRouter(t, tt.route, tt.target, sensitiveLookup(t, tt.route, tt.sensitive...)))
		})
	}
}

// TestMaskSensitivePathParamsShapeMismatchFailsClosed pins that a rawPath which does not fit the
// matched route's plan yields the route template, never the raw path.
func TestMaskSensitivePathParamsShapeMismatchFailsClosed(t *testing.T) {
	got := maskThroughRouterRaw(t, "/cards/:pan", "/cards/1", "/cards/"+sensitiveTestPAN+"/x",
		sensitiveLookup(t, "/cards/:pan", "pan"))
	assert.Equal(t, "/cards/:pan", got)
}

func TestMaskBySegment(t *testing.T) {
	tests := []struct {
		name   string
		route  string
		names  []string
		path   string
		want   string
		wantOK bool
	}{
		{name: "fixed_route_match", route: "/a/:x", names: []string{"x"}, path: "/a/1", want: "/a/***", wantOK: true},
		{name: "fixed_route_shorter_path", route: "/a/:x", names: []string{"x"}, path: "/a"},
		{name: "fixed_route_longer_path", route: "/a/:x", names: []string{"x"}, path: "/a/1/2"},
		{name: "wildcard_path_at_wildcard_index", route: "/a/:x/*", names: []string{"x", "*"}, path: "/a/1", want: "/a/***", wantOK: true},
		{name: "wildcard_path_below_wildcard_index", route: "/a/:x/*", names: []string{"x"}, path: "/a"},
		{name: "wildcard_not_sensitive_keeps_tail", route: "/a/:x/*", names: []string{"x"}, path: "/a/1/b/c", want: "/a/***/b/c", wantOK: true},
		{name: "wildcard_tail_collapses", route: "/a/*", names: []string{"*"}, path: "/a/b/c", want: "/a/***", wantOK: true},
		{name: "wildcard_inside_segment", route: "/img*", names: []string{"*"}, path: "/img/a", want: "/***", wantOK: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			plan, err := compileSensitivePlan(tt.route, tt.names)
			require.NoError(t, err)
			got, ok := maskBySegment(plan, tt.path)
			assert.Equal(t, tt.wantOK, ok)
			assert.Equal(t, tt.want, got)
		})
	}
}

// TestMaskSensitivePathParamsAllocsStable pins the common case: a route without sensitive params
// returns rawPath itself and allocates nothing (ADR-026 guard, run by make test-alloc).
func TestMaskSensitivePathParamsAllocsStable(t *testing.T) {
	if racedetect.Enabled {
		t.Skip("testing.AllocsPerRun is unreliable under -race; alloc baseline enforced in the non-race matrix")
	}
	empty := newRouteConflictTracker().lookupSensitivePlan // a server with no sensitive route
	other := sensitiveLookup(t, "/cards/:pan", "pan")      // a server whose sensitive route is elsewhere

	e := echo.New()
	e.GET("/users/:id", func(c *echo.Context) error {
		raw := routerPath(c.Request())
		for _, lookup := range []sensitiveParamLookup{nil, empty, other} {
			allocs := testing.AllocsPerRun(100, func() { _ = maskSensitivePathParams(c, raw, lookup) })
			assert.Zero(t, allocs, "a route without sensitive params must not allocate")
		}
		return c.NoContent(http.StatusOK)
	})
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/users/42", http.NoBody))
	require.Equal(t, http.StatusOK, rec.Code)
}

// TestOTelMiddlewareSpanMasksSensitivePathParam drives a request through a real Server: the
// span's url.path carries the mask, http.route keeps the template.
func TestOTelMiddlewareSpanMasksSensitivePathParam(t *testing.T) {
	exporter := installTestTracerProvider(t)
	DefaultRouteRegistry.Clear()
	t.Cleanup(DefaultRouteRegistry.Clear)
	srv := newSensitiveCardServer(t, &testLogger{}, true)
	srv.echo.GET("/users/:id", okEchoHandler) // registered outside the server's table

	serveSensitiveCard(t, srv)
	rec := httptest.NewRecorder()
	srv.echo.ServeHTTP(rec, httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/users/42", http.NoBody))
	require.Equal(t, http.StatusOK, rec.Code)

	spans := exporter.GetSpans()
	require.Len(t, spans, 2)
	urlPath, route := spanPathAttrs(&spans[0])
	assert.Equal(t, maskedCardPath, urlPath, "url.path must carry the mask, never the value")
	assert.Equal(t, sensitiveCardRoute, route, "http.route must keep the route template")
	for _, attr := range spans[0].Attributes {
		assert.NotContainsf(t, attr.Value.String(), sensitiveTestPAN, "span attribute %q leaks the value", attr.Key)
	}
	urlPath, _ = spanPathAttrs(&spans[1])
	assert.Equal(t, "/users/42", urlPath, "a route without sensitive params is untouched")
}

func spanPathAttrs(span *tracetest.SpanStub) (urlPath, route string) {
	for _, attr := range span.Attributes {
		switch attr.Key {
		case semconv.URLPathKey:
			urlPath = attr.Value.AsString()
		case semconv.HTTPRouteKey:
			route = attr.Value.AsString()
		}
	}
	return urlPath, route
}

// TestAccessLogMasksSensitivePathParam pins both the structured url.path field and the human
// message of the access log.
func TestAccessLogMasksSensitivePathParam(t *testing.T) {
	DefaultRouteRegistry.Clear()
	t.Cleanup(DefaultRouteRegistry.Clear)
	recLog := &recLogger{}
	srv := newSensitiveCardServer(t, recLog, false)

	serveSensitiveCard(t, srv)
	assertAccessLogMasked(t, recLog)
}

// TestDefaultRouteRegistryClearKeepsMasking pins that the table is per-server state: clearing
// the process-global introspection registry on a live server leaves masking on.
func TestDefaultRouteRegistryClearKeepsMasking(t *testing.T) {
	DefaultRouteRegistry.Clear()
	t.Cleanup(DefaultRouteRegistry.Clear)
	recLog := &recLogger{}
	srv := newSensitiveCardServer(t, recLog, false)

	DefaultRouteRegistry.Clear()
	serveSensitiveCard(t, srv)
	assertAccessLogMasked(t, recLog)
}

func assertAccessLogMasked(t *testing.T, recLog *recLogger) {
	t.Helper()
	require.NotNil(t, recLog.last, "the route must emit an action log")
	assert.Equal(t, maskedCardPath, recLog.last.fields["url.path"])
	assert.Equal(t, sensitiveCardRoute, recLog.last.fields["http.route"])
	assert.Contains(t, recLog.last.message, maskedCardPath)
	assert.NotContains(t, recLog.last.message, sensitiveTestPAN)
}
