package app

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/gaborage/go-bricks/config"
	"github.com/gaborage/go-bricks/logger"
	"github.com/gaborage/go-bricks/server"
)

const (
	debugPath     = "/_debug"
	localhostIPV4 = "127.0.0.1"
	debugInfoPath = debugPath + "/info"
	testIPAddress = "127.0.0.1:12345"
)

// recordedRoute is a single route registration captured by recordingRegistrar.
type recordedRoute struct {
	method     string
	path       string
	handler    server.Handler
	middleware []server.MiddlewareFunc
}

// recordingRegistrar is an echo-free server.RouteRegistrar that records the groups, routes,
// and middleware registered against it. It lets the debug-endpoint wiring be exercised
// end-to-end — registration + IP/auth gating + handler responses — without standing up a
// real HTTP listener, matching the flat-middleware test style used across the echo-free
// packages (e.g. scheduler's invokeCIDRMiddleware).
type recordingRegistrar struct {
	prefix     string
	parent     *recordingRegistrar
	middleware []server.MiddlewareFunc
	routes     []recordedRoute
	children   []*recordingRegistrar
}

func newRecordingRegistrar() *recordingRegistrar { return &recordingRegistrar{} }

func (r *recordingRegistrar) Add(method, path string, handler server.Handler, mw ...server.MiddlewareFunc) {
	r.routes = append(r.routes, recordedRoute{
		method:     method,
		path:       path,
		handler:    handler,
		middleware: append([]server.MiddlewareFunc{}, mw...),
	})
}

func (r *recordingRegistrar) Group(prefix string, mw ...server.MiddlewareFunc) server.RouteRegistrar {
	child := &recordingRegistrar{
		prefix:     prefix,
		parent:     r,
		middleware: append([]server.MiddlewareFunc{}, mw...),
	}
	r.children = append(r.children, child)
	return child
}

func (r *recordingRegistrar) Use(mw ...server.MiddlewareFunc) {
	r.middleware = append(r.middleware, mw...)
}

func (r *recordingRegistrar) FullPath(path string) string {
	return r.prefix + path
}

// chain returns the group middleware from the root down to this group (outermost first).
func (r *recordingRegistrar) chain() []server.MiddlewareFunc {
	if r.parent == nil {
		return append([]server.MiddlewareFunc{}, r.middleware...)
	}
	return append(r.parent.chain(), r.middleware...)
}

// match resolves method+path (query stripped) to its group and route, matching each route
// as group.prefix + route.path.
func (r *recordingRegistrar) match(method, path string) (group *recordingRegistrar, route recordedRoute, ok bool) {
	for _, rt := range r.routes {
		if rt.method == method && r.prefix+rt.path == path {
			return r, rt, true
		}
	}
	for _, child := range r.children {
		if g, rt, found := child.match(method, path); found {
			return g, rt, true
		}
	}
	return nil, recordedRoute{}, false
}

// serve runs method+target through the recorded middleware chain (group middleware
// outermost, then route middleware) and handler, returning the resulting HTTP status and
// the response recorder. The client peer is always the loopback testIPAddress; IP/auth
// variation is covered by the unit tests, while these integration tests vary the allowlist
// and endpoint config instead. Unmatched routes resolve to 404 (mirroring the real router);
// a middleware/handler error carrying an IAPIError resolves to that error's HTTP status.
func (r *recordingRegistrar) serve(method, target string) (status int, rec *httptest.ResponseRecorder) {
	rec = httptest.NewRecorder()

	path := target
	if i := strings.IndexByte(path, '?'); i >= 0 {
		path = path[:i]
	}

	group, route, ok := r.match(method, path)
	if !ok {
		return http.StatusNotFound, rec
	}

	req := httptest.NewRequestWithContext(context.Background(), method, target, http.NoBody)
	req.RemoteAddr = testIPAddress
	ctx := server.NewHandlerContextForTest(rec, req, nil)

	mws := append(group.chain(), route.middleware...)
	next := func() error { return route.handler(ctx) }
	for i := len(mws) - 1; i >= 0; i-- {
		mw := mws[i]
		downstream := next
		next = func() error { return mw(ctx, downstream) }
	}

	if err := next(); err != nil {
		var apiErr server.IAPIError
		if errors.As(err, &apiErr) {
			return apiErr.HTTPStatus(), rec
		}
		return http.StatusInternalServerError, rec
	}
	return rec.Code, rec
}

func TestDebugEndpointsIntegration(t *testing.T) {
	// Create test configuration with debug enabled
	debugConfig := &config.DebugConfig{
		Enabled:     true,
		PathPrefix:  debugPath,
		AllowedIPs:  []string{localhostIPV4},
		BearerToken: "",
		Endpoints: config.DebugEndpointsConfig{
			Info:       true,
			Goroutines: true,
			GC:         true,
			Health:     true,
		},
	}

	testLogger := logger.New("info", false)
	app := &App{logger: testLogger}
	debugHandlers := NewDebugHandlers(app, debugConfig, testLogger)

	// Register debug endpoints through the echo-free RouteRegistrar.
	root := newRecordingRegistrar()
	debugHandlers.RegisterDebugEndpoints(root)

	// The info endpoint is accessible from an allowlisted client IP.
	status, rec := root.serve(http.MethodGet, debugInfoPath)

	assert.Equal(t, http.StatusOK, status)
	assert.Contains(t, rec.Body.String(), "goroutines")
	assert.Contains(t, rec.Body.String(), "go_version")

	// The force-GC endpoint is registered under POST and reachable end-to-end.
	status, rec = root.serve(http.MethodPost, debugPath+"/gc")

	assert.Equal(t, http.StatusOK, status)
	assert.Contains(t, rec.Body.String(), `"forced":true`)
}

func TestDebugEndpointsIPRestriction(t *testing.T) {
	// Create test configuration with debug enabled but restricted IPs
	debugConfig := &config.DebugConfig{
		Enabled:    true,
		PathPrefix: debugPath,
		AllowedIPs: []string{"192.168.1.1"}, // Different IP
		Endpoints:  config.DebugEndpointsConfig{Info: true},
	}

	testLogger := logger.New("info", false)
	app := &App{logger: testLogger}
	debugHandlers := NewDebugHandlers(app, debugConfig, testLogger)

	root := newRecordingRegistrar()
	debugHandlers.RegisterDebugEndpoints(root)

	// A request from a non-allowed IP is rejected.
	status, _ := root.serve(http.MethodGet, debugInfoPath)

	assert.Equal(t, http.StatusForbidden, status)
}

func TestDebugEndpointsDisabled(t *testing.T) {
	// Create test configuration with debug disabled
	debugConfig := &config.DebugConfig{
		Enabled:     false,
		PathPrefix:  debugPath,
		AllowedIPs:  []string{localhostIPV4},
		BearerToken: "",
	}

	testLogger := logger.New("info", false)
	app := &App{logger: testLogger}
	debugHandlers := NewDebugHandlers(app, debugConfig, testLogger)

	root := newRecordingRegistrar()
	debugHandlers.RegisterDebugEndpoints(root)

	// Nothing is registered when disabled, so the route resolves to 404.
	assert.Empty(t, root.children)
	status, _ := root.serve(http.MethodGet, debugInfoPath)

	assert.Equal(t, http.StatusNotFound, status)
}

func TestGoroutineEndpoint(t *testing.T) {
	debugConfig := &config.DebugConfig{
		Enabled:     true,
		PathPrefix:  debugPath,
		AllowedIPs:  []string{localhostIPV4},
		BearerToken: "",
		Endpoints: config.DebugEndpointsConfig{
			Goroutines: true,
			Info:       true,
			GC:         true,
			Health:     true,
		},
	}

	testLogger := logger.New("info", false)
	app := &App{logger: testLogger}
	debugHandlers := NewDebugHandlers(app, debugConfig, testLogger)

	root := newRecordingRegistrar()
	debugHandlers.RegisterDebugEndpoints(root)

	// Test goroutines endpoint with JSON format
	status, rec := root.serve(http.MethodGet, debugPath+"/goroutines")

	assert.Equal(t, http.StatusOK, status)
	assert.Contains(t, rec.Body.String(), "count")
	assert.Contains(t, rec.Body.String(), "timestamp")

	// Test goroutines endpoint with text format
	status, rec = root.serve(http.MethodGet, debugPath+"/goroutines?format=text")

	assert.Equal(t, http.StatusOK, status)
	assert.Contains(t, rec.Header().Get("Content-Type"), "text/plain")
	assert.Contains(t, rec.Body.String(), "goroutine")
}
