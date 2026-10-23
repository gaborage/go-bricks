package server

import (
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/labstack/echo/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/config"
	"github.com/gaborage/go-bricks/jose"
)

type TestRequest struct {
	ID   int    `param:"id" validate:"required,min=1"`
	Name string `json:"name" validate:"required,min=2"`
}

type TestResponse struct {
	ID      int    `json:"id"`
	Name    string `json:"name"`
	Success bool   `json:"success"`
}

func testHandler(req TestRequest, _ HandlerContext) (TestResponse, IAPIError) {
	return TestResponse{
		ID:      req.ID,
		Name:    req.Name,
		Success: true,
	}, nil
}

func TestRouteMetadataCapture(t *testing.T) {
	// Clear the registry before testing
	DefaultRouteRegistry.Clear()

	// Create test setup
	cfg := &config.Config{}
	registry := NewHandlerRegistry(cfg)
	e := echo.New()
	registrar := newRouteGroup(e.Group(""), "", nil)

	// Register a route with metadata
	GET(registry, registrar, "/test/:id", testHandler,
		WithModule("test"),
		WithTags("testing", "example"),
		WithSummary("Test endpoint"),
		WithDescription("A test endpoint for validation"))

	// Verify route was registered
	routes := DefaultRouteRegistry.Routes()
	assert.Len(t, routes, 1, "Should have registered one route")

	route := routes[0]
	assert.Equal(t, "GET", route.Method)
	assert.Equal(t, "/test/:id", route.Path)
	assert.Equal(t, "test", route.ModuleName)
	assert.Equal(t, []string{"testing", "example"}, route.Tags)
	assert.Equal(t, "Test endpoint", route.Summary)
	assert.Equal(t, "A test endpoint for validation", route.Description)
	assert.NotEmpty(t, route.Package)
	assert.True(t, strings.HasSuffix(route.Package, "/server"))

	// Verify request and response types are captured
	assert.Equal(t, reflect.TypeOf(TestRequest{}), route.RequestType)
	assert.Equal(t, reflect.TypeOf(TestResponse{}), route.ResponseType)
}

func TestRouteRegistryOperations(t *testing.T) {
	registry := &RouteRegistry{}

	// Test adding routes
	route1 := RouteDescriptor{
		Method: "GET",
		Path:   "/users",
	}
	route2 := RouteDescriptor{
		Method: "POST",
		Path:   "/users",
	}

	registry.Register(&route1)
	registry.Register(&route2)

	routes := registry.Routes()
	assert.Len(t, routes, 2)

	// Test filtering by method
	getRoutes := registry.RoutesByMethod("GET")
	assert.Len(t, getRoutes, 1)
	assert.Equal(t, "GET", getRoutes[0].Method)

	// Test filtering by module
	route3 := RouteDescriptor{
		Method:     "GET",
		Path:       "/products",
		ModuleName: "products",
	}
	registry.Register(&route3)

	moduleRoutes := registry.ByModule("products")
	assert.Len(t, moduleRoutes, 1)
	assert.Equal(t, "products", moduleRoutes[0].ModuleName)

	// Test clearing
	registry.Clear()
	assert.Empty(t, registry.Routes())
}

func TestBackwardCompatibility(t *testing.T) {
	// Clear the registry before testing
	DefaultRouteRegistry.Clear()

	// Create test setup
	cfg := &config.Config{}
	registry := NewHandlerRegistry(cfg)
	e := echo.New()
	registrar := newRouteGroup(e.Group(""), "", nil)

	// Register route without options (backward compatible)
	GET(registry, registrar, "/legacy/:id", testHandler)

	// Verify route was registered
	routes := DefaultRouteRegistry.Routes()
	assert.Len(t, routes, 1, "Should have registered one route")

	route := routes[0]
	assert.Equal(t, "GET", route.Method)
	assert.Equal(t, "/legacy/:id", route.Path)
	assert.Empty(t, route.ModuleName)
	assert.Empty(t, route.Tags)
	assert.Empty(t, route.Summary)
	assert.Empty(t, route.Description)

	// But type information should still be captured
	assert.Equal(t, reflect.TypeOf(TestRequest{}), route.RequestType)
	assert.Equal(t, reflect.TypeOf(TestResponse{}), route.ResponseType)
}

func TestRouteOptions(t *testing.T) {
	// Test individual route options
	opts := []RouteOption{
		WithModule("test-module"),
		WithTags("tag1", "tag2"),
		WithSummary("Test summary"),
		WithDescription("Test description"),
		WithMiddleware("auth", "logging"),
		WithHandlerName("customHandler"),
	}

	var descriptor RouteDescriptor
	for _, opt := range opts {
		opt(&descriptor)
	}

	assert.Equal(t, "test-module", descriptor.ModuleName)
	assert.Equal(t, []string{"tag1", "tag2"}, descriptor.Tags)
	assert.Equal(t, "Test summary", descriptor.Summary)
	assert.Equal(t, "Test description", descriptor.Description)
	assert.Equal(t, []string{"auth", "logging"}, descriptor.Middleware)
	assert.Equal(t, "customHandler", descriptor.HandlerName)
}

func TestWithRawResponse(t *testing.T) {
	var descriptor RouteDescriptor
	assert.False(t, descriptor.RawResponse, "default should be false")

	WithRawResponse()(&descriptor)
	assert.True(t, descriptor.RawResponse, "should be true after applying option")
}

func TestRouteRegistryGetByPathAndCount(t *testing.T) {
	registry := &RouteRegistry{}

	userGet := RouteDescriptor{Method: "GET", Path: "/users"}
	userPost := RouteDescriptor{Method: "POST", Path: "/users"}
	product := RouteDescriptor{Method: "GET", Path: "/products"}

	registry.Register(&userGet)
	registry.Register(&userPost)
	registry.Register(&product)

	assert.Equal(t, 3, registry.Count())

	users := registry.ByPath("/users")
	assert.Len(t, users, 2)
	for _, route := range users {
		assert.Equal(t, "/users", route.Path)
	}

	none := registry.ByPath("/missing")
	assert.Empty(t, none)
}

func TestCloneDescriptorCopiesJOSEPolicies(t *testing.T) {
	src := RouteDescriptor{
		Method: "POST",
		Path:   "/tokens",
		InboundJOSE: &jose.Policy{
			DecryptKid: "our-key",
			VerifyKid:  "peer-key",
			Mode:       jose.SealModeJWEofJWS,
			ProtectedHeaders: map[string]any{
				"iat": int64(1),
			},
		},
		OutboundJOSE: &jose.Policy{
			SignKid:    "our-key",
			EncryptKid: "peer-key",
			ProtectedHeaders: map[string]any{
				"cty": "application/json",
			},
		},
	}

	got := cloneDescriptor(&src)
	assert.Equal(t, src.InboundJOSE.DecryptKid, got.InboundJOSE.DecryptKid)
	assert.Equal(t, src.OutboundJOSE.SignKid, got.OutboundJOSE.SignKid)
	assert.NotSame(t, src.InboundJOSE, got.InboundJOSE)
	assert.NotSame(t, src.OutboundJOSE, got.OutboundJOSE)

	got.InboundJOSE.ProtectedHeaders["iat"] = int64(99)
	assert.Equal(t, int64(1), src.InboundJOSE.ProtectedHeaders["iat"])
	got.OutboundJOSE.ProtectedHeaders["cty"] = "text/plain"
	assert.Equal(t, "application/json", src.OutboundJOSE.ProtectedHeaders["cty"])
}

func TestCloneDescriptorNilPoliciesStayNil(t *testing.T) {
	got := cloneDescriptor(&RouteDescriptor{Method: "GET", Path: "/health"})
	assert.Nil(t, got.InboundJOSE)
	assert.Nil(t, got.OutboundJOSE)
}

func TestWithSensitiveParams(t *testing.T) {
	var descriptor RouteDescriptor
	WithSensitiveParams("pan")(&descriptor)
	WithSensitiveParams("cvv", "account")(&descriptor)
	assert.Equal(t, []string{"pan", "cvv", "account"}, descriptor.SensitiveParams)
}

func TestCloneDescriptorDetachesSensitiveParams(t *testing.T) {
	src := RouteDescriptor{SensitiveParams: []string{"pan"}}
	clone := cloneDescriptor(&src)
	require.Equal(t, src.SensitiveParams, clone.SensitiveParams)

	clone.SensitiveParams[0] = "mutated"
	assert.Equal(t, "pan", src.SensitiveParams[0], "the clone must not share the backing array")
	assert.Nil(t, cloneDescriptor(&RouteDescriptor{}).SensitiveParams, "nil stays nil")
}

func TestRouteConflictTrackerSensitivePlan(t *testing.T) {
	tracker := newRouteConflictTracker()
	assert.Nil(t, tracker.sensitive.Load(), "no sensitive route: the lookup has no table to read")
	_, ok := tracker.lookupSensitivePlan(http.MethodGet, "/cards/:pan")
	assert.False(t, ok)

	cards := sensitivePlan{segCount: 3, wildcardIdx: -1, maskIdx: []int{2}}
	accounts := sensitivePlan{segCount: 3, wildcardIdx: -1, maskIdx: []int{2}}
	files := sensitivePlan{segCount: 3, wildcardIdx: 2, maskTail: true}
	tracker.recordSensitive(http.MethodGet, "/cards/:pan", cards)
	tracker.recordSensitive(http.MethodGet, "/users/:id", sensitivePlan{})
	tracker.recordSensitive(http.MethodPost, "/accounts/:account", accounts)
	tracker.recordSensitive(http.MethodGet, "/files/*", files)

	for _, tt := range []struct {
		method, path string
		want         sensitivePlan
		wantOK       bool
	}{
		{method: http.MethodGet, path: "/cards/:pan", want: cards, wantOK: true},
		{method: http.MethodPost, path: "/accounts/:account", want: accounts, wantOK: true},
		{method: http.MethodGet, path: "/files/*", want: files, wantOK: true},
		{method: http.MethodDelete, path: "/cards/:pan"}, // same path, other method must not inherit
		{method: http.MethodGet, path: "/users/:id"},     // a plan that masks nothing is not recorded
		{method: http.MethodGet, path: "/unknown"},
	} {
		got, ok := tracker.lookupSensitivePlan(tt.method, tt.path)
		assert.Equal(t, tt.wantOK, ok, "%s %s", tt.method, tt.path)
		assert.Equal(t, tt.want, got, "%s %s", tt.method, tt.path)
	}

	var nilTracker *routeConflictTracker
	assert.NotPanics(t, func() { nilTracker.recordSensitive(http.MethodGet, "/x/:y", cards) })
}

func TestCompileSensitivePlan(t *testing.T) {
	tests := []struct {
		name    string
		path    string
		params  []string
		want    sensitivePlan
		wantErr string // the offending param, quoted in the error
	}{
		{name: "no_names", path: "/cards/:pan", want: sensitivePlan{}},
		{
			name: "whole_segment_name", path: "/api/cards/:pan", params: []string{"pan"},
			want: sensitivePlan{segCount: 4, wildcardIdx: -1, maskIdx: []int{3}},
		},
		{
			name: "several_names_sorted_and_deduped", path: "/accounts/:account/cards/:pan", params: []string{"pan", "account", "pan"},
			want: sensitivePlan{segCount: 5, wildcardIdx: -1, maskIdx: []int{2, 4}},
		},
		{
			name: "repeated_name_masks_every_segment", path: "/cards/:id/transfers/:id", params: []string{"id"},
			want: sensitivePlan{segCount: 5, wildcardIdx: -1, maskIdx: []int{2, 4}},
		},
		{
			name: "repeated_name_declared_twice_is_deduped", path: "/cards/:id/transfers/:id", params: []string{"id", "id"},
			want: sensitivePlan{segCount: 5, wildcardIdx: -1, maskIdx: []int{2, 4}},
		},
		{
			name: "repeated_name_after_wildcard_is_not_counted", path: "/cards/:id/*", params: []string{"id"},
			want: sensitivePlan{segCount: 4, wildcardIdx: 3, maskIdx: []int{2}},
		},
		{
			name: "wildcard_only", path: "/files/*", params: []string{"*"},
			want: sensitivePlan{segCount: 3, wildcardIdx: 2, maskTail: true},
		},
		{
			name: "param_before_wildcard", path: "/v1/:pin/*", params: []string{"pin"},
			want: sensitivePlan{segCount: 4, wildcardIdx: 3, maskIdx: []int{2}},
		},
		{
			name: "param_and_wildcard", path: "/v1/:pin/*", params: []string{"pin", "*"},
			want: sensitivePlan{segCount: 4, wildcardIdx: 3, maskIdx: []int{2}, maskTail: true},
		},
		{
			name: "wildcard_inside_segment", path: "/img*", params: []string{"*"},
			want: sensitivePlan{segCount: 2, wildcardIdx: 1, maskTail: true},
		},
		{name: "param_after_wildcard", path: "/files/*/:pan", params: []string{"pan"}, wantErr: `"pan"`},
		{name: "embedded_after_literal", path: "/cards/pan-:pan", params: []string{"pan"}, wantErr: `"pan"`},
		{name: "prefix_of_longer_segment", path: "/cards/:pan-x", params: []string{"pan"}, wantErr: `"pan"`},
		{name: "escaped_colon_is_literal", path: `/cards/\:pan`, params: []string{"pan"}, wantErr: `"pan"`},
		{name: "literal_segment_named_like_param", path: "/cards/pan", params: []string{"pan"}, wantErr: `"pan"`},
		{name: "unknown_name", path: "/cards/:pan", params: []string{"pan", "cvv"}, wantErr: `"cvv"`},
		{name: "empty_name", path: "/cards/:pan", params: []string{""}, wantErr: `""`},
		{name: "wildcard_name_without_wildcard", path: "/cards/:pan", params: []string{"*"}, wantErr: `"*"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := compileSensitivePlan(tt.path, tt.params)
			if tt.wantErr == "" {
				require.NoError(t, err)
				assert.Equal(t, tt.want, got)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr, "the error names the param")
			assert.Contains(t, err.Error(), tt.path, "the error names the route")
			assert.Equal(t, sensitivePlan{}, got)
		})
	}
}

// newSensitiveTestRegistrar returns a typed-handler registry and a tracked "/api" registrar
// recording into tracker, with DefaultRouteRegistry cleared around the test.
func newSensitiveTestRegistrar(t *testing.T) (*HandlerRegistry, RouteRegistrar, *routeConflictTracker) {
	t.Helper()
	DefaultRouteRegistry.Clear()
	t.Cleanup(DefaultRouteRegistry.Clear)
	tracker := newRouteConflictTracker()
	return NewHandlerRegistry(&config.Config{}), newTrackedRouteGroup(echo.New().Group("/api"), "/api", nil, tracker), tracker
}

func TestRegisterHandlerSensitiveParams(t *testing.T) {
	t.Run("valid_routes_are_recorded", func(t *testing.T) {
		registry, registrar, tracker := newSensitiveTestRegistrar(t)
		GET(registry, registrar, "/cards/:id", testHandler, WithSensitiveParams("id"))
		GET(registry, registrar, "/v1/:pin/*", testHandler, WithSensitiveParams("pin", "*"))
		// A refused duplicate never reaches the engine, so its declaration is not recorded.
		POST(registry, registrar, "/cards/:id", testHandler)
		POST(registry, registrar, "/cards/:id", testHandler, WithSensitiveParams("id"))

		got, ok := tracker.lookupSensitivePlan(http.MethodGet, "/api/cards/:id")
		require.True(t, ok)
		assert.Equal(t, sensitivePlan{segCount: 4, wildcardIdx: -1, maskIdx: []int{3}}, got)
		got, ok = tracker.lookupSensitivePlan(http.MethodGet, "/api/v1/:pin/*")
		require.True(t, ok)
		assert.Equal(t, sensitivePlan{segCount: 5, wildcardIdx: 4, maskIdx: []int{3}, maskTail: true}, got)
		_, ok = tracker.lookupSensitivePlan(http.MethodPost, "/api/cards/:id")
		assert.False(t, ok)

		routes := DefaultRouteRegistry.Routes()
		require.NotEmpty(t, routes)
		assert.Equal(t, []string{"id"}, routes[0].SensitiveParams, "the descriptor still carries it for discovery")
	})

	for _, tt := range []struct{ name, path, param string }{
		{name: "unknown_name", path: "/cards/:id", param: "pan"},
		{name: "param_after_wildcard", path: "/files/*/:id", param: "id"},
		{name: "wildcard_name_without_wildcard", path: "/cards/:id", param: "*"},
	} {
		t.Run("rejects_"+tt.name, func(t *testing.T) {
			registry, registrar, tracker := newSensitiveTestRegistrar(t)
			fullPath := "/api" + tt.path
			_, err := compileSensitivePlan(fullPath, []string{tt.param})
			require.Error(t, err)

			assert.PanicsWithValue(t, "server: handler registration failed for GET "+fullPath+": "+err.Error(),
				func() { GET(registry, registrar, tt.path, testHandler, WithSensitiveParams(tt.param)) })

			assert.Nil(t, tracker.sensitive.Load(), "a rejected route records nothing")
			assert.Empty(t, DefaultRouteRegistry.Routes(), "a rejected route is never published")
			assert.True(t, tracker.record(http.MethodGet, fullPath, RouteRegistrant{}),
				"a rejected route never reserved its path")
		})
	}
}

// wrappingRegistrar delegates to the framework's route group without being one, the shape of a
// consumer decorator around RouteRegistrar: RegisterHandler can only reach it through Add.
type wrappingRegistrar struct{ inner RouteRegistrar }

func (w wrappingRegistrar) Add(method, path string, handler Handler, middleware ...MiddlewareFunc) {
	w.inner.Add(method, path, handler, middleware...)
}

func (w wrappingRegistrar) Group(prefix string, middleware ...MiddlewareFunc) RouteRegistrar {
	return wrappingRegistrar{inner: w.inner.Group(prefix, middleware...)}
}
func (w wrappingRegistrar) Use(middleware ...MiddlewareFunc) { w.inner.Use(middleware...) }
func (w wrappingRegistrar) FullPath(p string) string         { return w.inner.FullPath(p) }

func TestRegisterHandlerSensitiveParamsWrappedRegistrar(t *testing.T) {
	t.Run("sensitive_declaration_panics_before_publishing", func(t *testing.T) {
		registry, registrar, tracker := newSensitiveTestRegistrar(t)
		wrapped := wrappingRegistrar{inner: registrar}

		assert.PanicsWithValue(t,
			"server: handler registration failed for GET /api/cards/:id: WithSensitiveParams requires the framework's route group registrar",
			func() { GET(registry, wrapped, "/cards/:id", testHandler, WithSensitiveParams("id")) })

		assert.Nil(t, tracker.sensitive.Load(), "a refused route records nothing")
		assert.Empty(t, DefaultRouteRegistry.Routes(), "a refused route is never published")
		assert.True(t, tracker.record(http.MethodGet, "/api/cards/:id", RouteRegistrant{}),
			"a refused route never reserved its path")
	})

	t.Run("non_sensitive_route_uses_the_fallback", func(t *testing.T) {
		registry, registrar, tracker := newSensitiveTestRegistrar(t)
		wrapped := wrappingRegistrar{inner: registrar}

		assert.NotPanics(t, func() { GET(registry, wrapped, "/cards/:id", testHandler) })

		assert.Nil(t, tracker.sensitive.Load(), "nothing sensitive was declared")
		assert.NotEmpty(t, DefaultRouteRegistry.ByPath("/api/cards/:id"), "the route is published")
		assert.False(t, tracker.record(http.MethodGet, "/api/cards/:id", RouteRegistrant{}),
			"the wrapped Add reached the route group and reserved the path")
	})
}
