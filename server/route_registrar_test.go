package server

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRouteGroupAddNormalizesPaths(t *testing.T) {
	e := echo.New()
	base := newRouteGroup(e.Group("/api"), "/api")
	rg, ok := base.(*routeGroup)
	require.True(t, ok)

	var hits int
	rg.Add(http.MethodGet, "/users", func(c echo.Context) error {
		hits++
		return c.NoContent(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/api/users", http.NoBody)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, 1, hits)

	rg.Add(http.MethodGet, "/api/orders", func(c echo.Context) error {
		hits++
		return c.NoContent(http.StatusOK)
	})

	req = httptest.NewRequest(http.MethodGet, "/api/orders", http.NoBody)
	rec = httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, 2, hits)

	rg.Add(http.MethodGet, "/", func(c echo.Context) error {
		hits++
		return c.NoContent(http.StatusOK)
	})

	req = httptest.NewRequest(http.MethodGet, "/api", http.NoBody)
	rec = httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, 3, hits)
}

func TestRouteGroupGroupCreatesNestedRegistrar(t *testing.T) {
	e := echo.New()
	base := newRouteGroup(e.Group("/api"), "/api")
	parent := base.(*routeGroup)

	child := parent.Group("/v1")
	childGroup, ok := child.(*routeGroup)
	require.True(t, ok)
	assert.Equal(t, "/api/v1", childGroup.prefix)

	var hits int
	childGroup.Add(http.MethodGet, "/widgets", func(c echo.Context) error {
		hits++
		return c.String(http.StatusOK, "widgets")
	})

	childGroup.Add(http.MethodGet, "/api/v1/analytics", func(c echo.Context) error {
		hits++
		return c.String(http.StatusOK, "analytics")
	})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/widgets", http.NoBody)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "widgets", rec.Body.String())

	req = httptest.NewRequest(http.MethodGet, "/api/v1/analytics", http.NoBody)
	rec = httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "analytics", rec.Body.String())
	assert.Equal(t, 2, hits)
}

func TestRouteGroupUseAppliesMiddleware(t *testing.T) {
	e := echo.New()
	base := newRouteGroup(e.Group(""), "")
	rg := base.(*routeGroup)

	var called bool
	rg.Use(func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			called = true
			return next(c)
		}
	})

	rg.Add(http.MethodGet, "/ping", func(c echo.Context) error {
		return c.String(http.StatusOK, "pong")
	})

	req := httptest.NewRequest(http.MethodGet, "/ping", http.NoBody)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.True(t, called)
}

func TestRouteGroupFullPathAndRelative(t *testing.T) {
	rg := &routeGroup{prefix: "/api"}

	assert.Equal(t, "/api/child", rg.FullPath("/child"))
	assert.Equal(t, "/api", rg.FullPath("/"))
	assert.Equal(t, "/api", rg.FullPath(""))
	assert.Equal(t, "/child", (&routeGroup{}).FullPath("/child"))

	assert.Equal(t, "", rg.relativePath("/"))
	assert.Equal(t, "", rg.relativePath("/api"))
	assert.Equal(t, "/child", rg.relativePath("/api/child"))
	assert.Equal(t, "/child", rg.relativePath("child"))
	assert.Equal(t, "/apples", rg.relativePath("/apples"))

	assert.Equal(t, "/api", rg.combinePrefix(""))
	assert.Equal(t, "/api/v1", rg.combinePrefix("/v1"))
	assert.Equal(t, "/v1", (&routeGroup{}).combinePrefix("/v1"))
}

func TestEnsureLeadingSlash(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"", "/"},
		{"test", "/test"},
		{"/already", "/already"},
	}

	for _, tt := range tests {
		assert.Equal(t, tt.expected, ensureLeadingSlash(tt.input))
	}
}

func TestNormalizeGroupBase(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"", ""},
		{"/", ""},
		{"api", "/api"},
		{"/api/", "/api"},
		{"/api///", "/api"},
	}

	for _, tt := range tests {
		assert.Equal(t, tt.expected, normalizeGroupBase(tt.input))
	}
}

func TestNormalizeGroupPrefix(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"", ""},
		{"/", ""},
		{"v1", "/v1"},
		{"/v1/", "/v1"},
		{"/v1///", "/v1"},
	}

	for _, tt := range tests {
		assert.Equal(t, tt.expected, normalizeGroupPrefix(tt.input))
	}
}

func TestStripPathPrefix(t *testing.T) {
	tests := []struct {
		path            string
		prefix          string
		expectedTail    string
		expectedMatched bool
	}{
		{"/users", "", "/users", false},
		{"/users", "/api", "/users", false},
		{"/api", "/api", "", true},
		{"/api/users", "/api", "/users", true},
		{"/apiusers", "/api", "/apiusers", false},
	}

	for _, tt := range tests {
		tail, matched := stripPathPrefix(tt.path, tt.prefix)
		assert.Equal(t, tt.expectedTail, tail)
		assert.Equal(t, tt.expectedMatched, matched)
	}
}
