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
// the middleware: the same per-server table, filled the way addEcho fills it.
func sensitiveLookup(route string, names ...string) sensitiveParamLookup {
	tracker := newRouteConflictTracker()
	tracker.recordSensitive(http.MethodGet, route, names)
	return tracker.sensitiveParams
}

// maskThroughRouter routes target through a real Echo router (so c.Path and c.Param are set the
// way the middleware sees them) and returns what maskSensitivePathParams makes of it.
func maskThroughRouter(t *testing.T, route, target string, lookup sensitiveParamLookup) string {
	t.Helper()
	var got string
	e := echo.New()
	e.GET(route, func(c *echo.Context) error {
		got = maskSensitivePathParams(c, routerPath(c.Request()), lookup)
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
			// Wildcard routes fall back to a literal search of the decoded value; an encoded
			// spelling is not found there and nothing breaks.
			name: "wildcard_fallback_masks_a_literal_value", route: "/files/:pan/*", sensitive: []string{"pan"},
			target: "/files/" + sensitiveTestPAN + "/a/b", want: "/files/***/a/b",
		},
		{
			// Echo routes on the raw path and c.Param carries that raw spelling, so the
			// fallback's literal search still finds an encoded value.
			name: "wildcard_fallback_masks_an_encoded_value", route: "/files/:pan/*",
			sensitive: []string{"pan"},
			target:    "/files/41%2F11/a", want: "/files/***/a",
		},
		{
			name: "declared_name_that_is_not_a_route_param", route: "/cards/:pan", sensitive: []string{"cvv"},
			target: "/cards/" + sensitiveTestPAN, want: "/cards/" + sensitiveTestPAN,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, maskThroughRouter(t, tt.route, tt.target, sensitiveLookup(tt.route, tt.sensitive...)))
		})
	}
}

// TestMaskSensitivePathParamsValueNotInPath covers the fallback when the value does not occur
// literally in the path handed in: nothing is replaced and nothing breaks.
func TestMaskSensitivePathParamsValueNotInPath(t *testing.T) {
	lookup := sensitiveLookup("/cards/:pan", "pan")

	var got string
	e := echo.New()
	e.GET("/cards/:pan", func(c *echo.Context) error {
		got = maskSensitivePathParams(c, "/unrelated", lookup)
		return c.NoContent(http.StatusOK)
	})
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/cards/"+sensitiveTestPAN, http.NoBody))
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "/unrelated", got)
}

// TestMaskSensitivePathParamsAllocsStable pins the common case: a route without sensitive params
// returns rawPath itself and allocates nothing (ADR-026 guard, run by make test-alloc).
func TestMaskSensitivePathParamsAllocsStable(t *testing.T) {
	if racedetect.Enabled {
		t.Skip("testing.AllocsPerRun is unreliable under -race; alloc baseline enforced in the non-race matrix")
	}
	empty := newRouteConflictTracker().sensitiveParams // a server with no sensitive route
	other := sensitiveLookup("/cards/:pan", "pan")     // a server whose sensitive route is elsewhere

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
