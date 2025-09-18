package server

import (
	"reflect"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"

	"github.com/gaborage/go-bricks/config"
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
	registrar := newRouteGroup(e.Group(""), "")

	// Register a route with metadata
	GET(registry, registrar, "/test/:id", testHandler,
		WithModule("test"),
		WithTags("testing", "example"),
		WithSummary("Test endpoint"),
		WithDescription("A test endpoint for validation"))

	// Verify route was registered
	routes := DefaultRouteRegistry.GetRoutes()
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

	registry.AddRoute(&route1)
	registry.AddRoute(&route2)

	routes := registry.GetRoutes()
	assert.Len(t, routes, 2)

	// Test filtering by method
	getRoutes := registry.GetRoutesByMethod("GET")
	assert.Len(t, getRoutes, 1)
	assert.Equal(t, "GET", getRoutes[0].Method)

	// Test filtering by module
	route3 := RouteDescriptor{
		Method:     "GET",
		Path:       "/products",
		ModuleName: "products",
	}
	registry.AddRoute(&route3)

	moduleRoutes := registry.GetRoutesByModule("products")
	assert.Len(t, moduleRoutes, 1)
	assert.Equal(t, "products", moduleRoutes[0].ModuleName)

	// Test clearing
	registry.Clear()
	assert.Len(t, registry.GetRoutes(), 0)
}

func TestBackwardCompatibility(t *testing.T) {
	// Clear the registry before testing
	DefaultRouteRegistry.Clear()

	// Create test setup
	cfg := &config.Config{}
	registry := NewHandlerRegistry(cfg)
	e := echo.New()
	registrar := newRouteGroup(e.Group(""), "")

	// Register route without options (backward compatible)
	GET(registry, registrar, "/legacy/:id", testHandler)

	// Verify route was registered
	routes := DefaultRouteRegistry.GetRoutes()
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

func TestRouteRegistry_GetByPathAndCount(t *testing.T) {
	registry := &RouteRegistry{}

	userGet := RouteDescriptor{Method: "GET", Path: "/users"}
	userPost := RouteDescriptor{Method: "POST", Path: "/users"}
	product := RouteDescriptor{Method: "GET", Path: "/products"}

	registry.Register(&userGet)
	registry.Register(&userPost)
	registry.Register(&product)

	assert.Equal(t, 3, registry.Count())

	users := registry.GetByPath("/users")
	assert.Len(t, users, 2)
	for _, route := range users {
		assert.Equal(t, "/users", route.Path)
	}

	none := registry.GetByPath("/missing")
	assert.Empty(t, none)
}
