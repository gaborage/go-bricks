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

func TestRouteConflictTrackerSensitiveParams(t *testing.T) {
	tracker := newRouteConflictTracker()
	assert.Nil(t, tracker.sensitive.Load(), "no sensitive route: the lookup has no table to read")
	assert.Nil(t, tracker.sensitiveParams(http.MethodGet, "/cards/:pan"))

	names := []string{"pan"}
	tracker.recordSensitive(http.MethodGet, "/cards/:pan", names)
	tracker.recordSensitive(http.MethodGet, "/users/:id", nil)
	tracker.recordSensitive(http.MethodPost, "/accounts/:account", []string{"account"})

	assert.Equal(t, []string{"pan"}, tracker.sensitiveParams(http.MethodGet, "/cards/:pan"))
	assert.Equal(t, []string{"account"}, tracker.sensitiveParams(http.MethodPost, "/accounts/:account"))
	assert.Nil(t, tracker.sensitiveParams(http.MethodDelete, "/cards/:pan"), "same path, other method must not inherit")
	assert.Nil(t, tracker.sensitiveParams(http.MethodGet, "/users/:id"))
	assert.Nil(t, tracker.sensitiveParams(http.MethodGet, "/unknown"))

	names[0] = "mutated"
	assert.Equal(t, []string{"pan"}, tracker.sensitiveParams(http.MethodGet, "/cards/:pan"),
		"the table must not share the caller's backing array")

	var nilTracker *routeConflictTracker
	assert.NotPanics(t, func() { nilTracker.recordSensitive(http.MethodGet, "/x/:y", []string{"y"}) })
}

func TestRegisterHandlerRecordsSensitiveParams(t *testing.T) {
	DefaultRouteRegistry.Clear()
	t.Cleanup(DefaultRouteRegistry.Clear)

	registry := NewHandlerRegistry(&config.Config{})
	tracker := newRouteConflictTracker()
	registrar := newTrackedRouteGroup(echo.New().Group("/api"), "/api", nil, tracker)
	GET(registry, registrar, "/cards/:id", testHandler, WithSensitiveParams("id"))
	// A refused duplicate never reaches the engine, so its declaration is not recorded.
	POST(registry, registrar, "/cards/:id", testHandler)
	POST(registry, registrar, "/cards/:id", testHandler, WithSensitiveParams("id"))

	assert.Equal(t, []string{"id"}, tracker.sensitiveParams(http.MethodGet, "/api/cards/:id"))
	assert.Nil(t, tracker.sensitiveParams(http.MethodPost, "/api/cards/:id"))
	routes := DefaultRouteRegistry.Routes()
	require.NotEmpty(t, routes)
	assert.Equal(t, []string{"id"}, routes[0].SensitiveParams, "the descriptor still carries it for discovery")
}
