package server

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/labstack/echo/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRouteConflictTrackerRecordsDuplicate(t *testing.T) {
	tr := newRouteConflictTracker()

	first := RouteRegistrant{HandlerName: "handlerA", Package: "pkg/a"}
	dup := RouteRegistrant{HandlerName: "handlerB", Package: "pkg/b"}

	assert.True(t, tr.record(http.MethodGet, "/users", first), "a new route is reported new")
	assert.False(t, tr.record(http.MethodGet, "/users", dup), "a duplicate is reported so the caller skips it")

	conflicts := tr.snapshot()
	require.Len(t, conflicts, 1)
	assert.Equal(t, RouteConflict{
		Method: http.MethodGet, Path: "/users", FirstPath: "/users",
		First: first, Duplicate: dup,
	}, conflicts[0])

	// Different methods, same path — no conflict.
	trMethods := newRouteConflictTracker()
	trMethods.record(http.MethodGet, "/users", first)
	trMethods.record(http.MethodPost, "/users", dup)
	assert.Empty(t, trMethods.snapshot())

	// A nil tracker must not panic and must report no conflicts.
	var nilTracker *routeConflictTracker
	assert.NotPanics(t, func() {
		assert.True(t, nilTracker.record(http.MethodGet, "/x", first), "an untracked group registers everything")
	})
	assert.Nil(t, nilTracker.snapshot())
}

func TestServerRouteConflictsAcrossGroups(t *testing.T) {
	srv := newTestServer("", "", "")

	moduleGroup := srv.ModuleGroup()
	rootGroup := srv.RootGroup()

	noop := func(c HandlerContext) error { return c.String(http.StatusOK, "") }

	moduleGroup.Add(http.MethodGet, "/shared", noop)
	rootGroup.Add(http.MethodGet, "/shared", noop)

	conflicts := srv.RouteConflicts()
	require.Len(t, conflicts, 1, "same method+path registered via ModuleGroup and RootGroup should collide")
	assert.Equal(t, http.MethodGet, conflicts[0].Method)
	assert.Equal(t, "/shared", conflicts[0].Path)

	// Nested Group() registration colliding with a flat registration of the identical
	// full path proves tracker propagation through Group().
	sub := moduleGroup.Group("/sub")
	sub.Add(http.MethodGet, "/leaf", noop)
	moduleGroup.Add(http.MethodGet, "/sub/leaf", noop)

	conflicts = srv.RouteConflicts()
	require.Len(t, conflicts, 2, "nested-group registration colliding with an equivalent flat path should be detected")
}

func TestRouteConflictDetectsProbeCollision(t *testing.T) {
	srv := newTestServer("", "", "") // health defaults to /health, ready to /ready

	noop := func(c HandlerContext) error { return c.String(http.StatusOK, "") }
	srv.ModuleGroup().Add(http.MethodGet, "/health", noop)

	conflicts := srv.RouteConflicts()
	require.Len(t, conflicts, 1, "module route shadowing the health probe must be detected")
	assert.Equal(t, http.MethodGet, conflicts[0].Method)
	assert.Equal(t, "/health", conflicts[0].Path)
	assert.Equal(t, "healthCheck", conflicts[0].First.HandlerName)
	assert.Equal(t, serverPackagePath, conflicts[0].First.Package)
}

func TestRouteConflictTypedAndRawBothTracked(t *testing.T) {
	srv := newTestServer("", "", "")
	hr := NewHandlerRegistry(srv.cfg)
	moduleGroup := srv.ModuleGroup()

	GET(hr, moduleGroup, "/dup", func(_ EmptyRequest, _ HandlerContext) (helloResp, IAPIError) {
		return helloResp{Message: "typed"}, nil
	})

	moduleGroup.Add(http.MethodGet, "/dup", func(c HandlerContext) error {
		return c.String(http.StatusOK, "raw")
	})

	conflicts := srv.RouteConflicts()
	require.Len(t, conflicts, 1)
	c := conflicts[0]
	assert.Equal(t, http.MethodGet, c.Method)
	assert.Equal(t, "/dup", c.Path)
	assert.NotEmpty(t, c.First.HandlerName, "typed registration's provenance must thread through the addEcho seam")
	assert.NotEmpty(t, c.First.Package)
}

func TestDuplicateRouteError(t *testing.T) {
	tests := []struct {
		name      string
		conflicts []RouteConflict
		wantMsg   string
	}{
		{name: "nil_conflicts"},
		{name: "empty_conflicts", conflicts: []RouteConflict{}},
		{
			name: "two_conflicts",
			conflicts: []RouteConflict{
				{
					Method: http.MethodGet, Path: "/one", FirstPath: "/one",
					First:     RouteRegistrant{HandlerName: "a", Package: "pkg/a"},
					Duplicate: RouteRegistrant{HandlerName: "b", Package: "pkg/b"},
				},
				{
					Method: http.MethodPost, Path: "/two",
					First:     RouteRegistrant{HandlerName: "c", Package: "pkg/c"},
					Duplicate: RouteRegistrant{HandlerName: "d", Package: "pkg/d"},
				},
			},
			wantMsg: "duplicate route registration (2 conflict(s))\n" +
				"GET /one — first: a (pkg/a), duplicate: b (pkg/b)\n" +
				"POST /two — first: c (pkg/c), duplicate: d (pkg/d)",
		},
		{
			name: "first_path_differs",
			conflicts: []RouteConflict{{
				Method: http.MethodGet, Path: "/users/:uid", FirstPath: "/users/:id",
				First:     RouteRegistrant{HandlerName: "a", Package: "pkg/a"},
				Duplicate: RouteRegistrant{HandlerName: "b", Package: "pkg/b"},
			}},
			wantMsg: "duplicate route registration (1 conflict(s))\n" +
				"GET /users/:uid — first: a (pkg/a) at /users/:id, duplicate: b (pkg/b)",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := duplicateRouteError(tt.conflicts)
			if tt.wantMsg == "" {
				assert.NoError(t, err)
				return
			}
			require.ErrorIs(t, err, ErrDuplicateRoute)
			var dre *DuplicateRouteError
			require.ErrorAs(t, err, &dre)
			assert.Equal(t, tt.conflicts, dre.Conflicts)
			assert.Equal(t, tt.wantMsg, err.Error())

			children := dre.Unwrap()
			require.Len(t, children, len(tt.conflicts)+1, "head plus one child per conflict")
			require.ErrorIs(t, children[0], ErrDuplicateRoute, "the head carries the sentinel")
			lines := make([]string, 0, len(children))
			for _, c := range children {
				lines = append(lines, c.Error())
			}
			assert.Equal(t, tt.wantMsg, strings.Join(lines, "\n"), "the children spell the error text")
		})
	}
}

// TestRouteNodeKey pins the tracker key to echo v5's node identity (DefaultRouter.Add).
func TestRouteNodeKey(t *testing.T) {
	tests := []struct {
		name     string
		a, b     string
		sameNode bool
	}{
		{name: "param_name_differs", a: "/users/:id", b: "/users/:uid", sameNode: true},
		{name: "mid_segment_param_name_differs", a: "/files/f:name/raw", b: "/files/f:n/raw", sameNode: true},
		{name: "wildcard_name_differs", a: "/f/*", b: "/f/*x", sameNode: true},
		{name: "wildcard_drops_suffix", a: "/f/*", b: "/f/*/ignored", sameNode: true},
		{name: "star_inside_param_name", a: "/x/:a*/y", b: "/x/:b/y", sameNode: true},
		{name: "missing_leading_slash", a: "users", b: "/users", sameNode: true},
		{name: "empty_is_root", a: "", b: "/", sameNode: true},
		{name: "param_vs_deeper_path", a: "/users/:id", b: "/users/:id/x"},
		{name: "param_vs_static", a: "/users/:id", b: "/users/me"},
		{name: "escaped_colon_vs_param", a: `/a\:b`, b: "/a:c"},
		{name: "wildcard_vs_param", a: "/f/*", b: "/f/:id"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ka, kb := routeNodeKey(http.MethodGet, tt.a), routeNodeKey(http.MethodGet, tt.b)
			if tt.sameNode {
				assert.Equal(t, ka, kb)
			} else {
				assert.NotEqual(t, ka, kb)
			}
		})
	}
}

// TestRouteNodeKeyAgreesWithEchoRouter checks routeNodeKey against the echo router itself, so an
// echo bump that changes node identity fails here: both templates go on a fresh engine
// (overwrite on, as echo.New sets it), and a request for the first reaching the second handler
// means echo kept one node.
func TestRouteNodeKeyAgreesWithEchoRouter(t *testing.T) {
	tests := []struct {
		name          string
		first, second string
		request       string
	}{
		{name: "identical", first: "/users", second: "/users", request: "/users"},
		{name: "param_name_differs", first: "/users/:id", second: "/users/:uid", request: "/users/42"},
		{name: "mid_segment_param_name_differs", first: "/a/x:id", second: "/a/x:uid", request: "/a/x42"},
		{name: "wildcard_name_differs", first: "/f/*", second: "/f/*x", request: "/f/a/b"},
		{name: "wildcard_drops_suffix", first: "/f/*", second: "/f/*/y", request: "/f/a"},
		{name: "star_inside_param_name", first: "/x/:a*/y", second: "/x/:b/y", request: "/x/q/y"},
		{name: "missing_leading_slash", first: "users", second: "/users", request: "/users"},
		{name: "empty_is_root", first: "", second: "/", request: "/"},
		// Only this order: echo v5.3.1 panics routing /a:b when the escaped template registers first.
		{name: "param_vs_escaped_colon", first: "/a:c", second: `/a\:b`, request: "/aq"},
		{name: "param_vs_static", first: "/users/:id", second: "/users/me", request: "/users/42"},
		{name: "param_vs_deeper_path", first: "/users/:id", second: "/users/:id/x", request: "/users/42"},
		{name: "wildcard_vs_param", first: "/f/*", second: "/f/:id", request: "/f/"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := echo.New()
			e.GET(tt.first, func(c *echo.Context) error { return c.String(http.StatusOK, "first") })
			e.GET(tt.second, func(c *echo.Context) error { return c.String(http.StatusOK, "second") })

			rec := serveEngine(e, http.MethodGet, tt.request)
			require.Equal(t, http.StatusOK, rec.Code, "the request must reach one of the two handlers")
			echoSameNode := rec.Body.String() == "second"
			keySameNode := routeNodeKey(http.MethodGet, tt.first) == routeNodeKey(http.MethodGet, tt.second)
			assert.Equal(t, echoSameNode, keySameNode, "routeNodeKey disagrees with echo's router")
		})
	}
}

// TestRouteConflictParamNameDiffers pins the case echo's router treats as one route while the
// template strings differ: the second registration is a conflict and the first keeps serving.
func TestRouteConflictParamNameDiffers(t *testing.T) {
	tests := []struct {
		name          string
		first, second string
		request       string
	}{
		{name: "param", first: "/users/:id", second: "/users/:uid", request: "/users/42"},
		{name: "wildcard", first: "/f/*", second: "/f/*x", request: "/f/a/b"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := newTestServer("", "", "")
			mg := srv.ModuleGroup()
			mg.Add(http.MethodGet, tt.first, func(c HandlerContext) error { return c.String(http.StatusOK, "first") })
			mg.Add(http.MethodGet, tt.second, func(c HandlerContext) error { return c.String(http.StatusOK, "second") })

			conflicts := srv.RouteConflicts()
			require.Len(t, conflicts, 1)
			assert.Equal(t, tt.second, conflicts[0].Path)
			assert.Equal(t, tt.first, conflicts[0].FirstPath)
			rec := serveEngine(srv.echo, http.MethodGet, tt.request)
			assert.Equal(t, http.StatusOK, rec.Code)
			assert.Equal(t, "first", rec.Body.String())
		})
	}
}

// TestRouteConflictDistinctNodesCoexist pins that templates echo keeps apart are not conflicts.
func TestRouteConflictDistinctNodesCoexist(t *testing.T) {
	srv := newTestServer("", "", "")
	mg := srv.ModuleGroup()
	for _, p := range []string{"/users/:id", "/users/:id/x", "/users/me"} {
		body := p
		mg.Add(http.MethodGet, p, func(c HandlerContext) error { return c.String(http.StatusOK, body) })
	}
	assert.Empty(t, srv.RouteConflicts())
	for req, want := range map[string]string{"/users/42": "/users/:id", "/users/42/x": "/users/:id/x", "/users/me": "/users/me"} {
		assert.Equal(t, want, serveEngine(srv.echo, http.MethodGet, req).Body.String(), req)
	}
}

// TestRouteConflictKeepsFirstHandler pins that a duplicate never replaces the handler:
// echo's Add would overwrite it, so the duplicate is recorded and not added.
func TestRouteConflictKeepsFirstHandler(t *testing.T) {
	tests := []struct {
		name     string
		register func(hr *HandlerRegistry, mg RouteRegistrar, body string)
	}{
		{
			name: "raw_handler",
			register: func(_ *HandlerRegistry, mg RouteRegistrar, body string) {
				mg.Add(http.MethodGet, "/dup", func(c HandlerContext) error { return c.String(http.StatusOK, body) })
			},
		},
		{
			name: "typed_handler",
			register: func(hr *HandlerRegistry, mg RouteRegistrar, body string) {
				GET(hr, mg, "/dup", func(_ EmptyRequest, _ HandlerContext) (helloResp, IAPIError) {
					return helloResp{Message: body}, nil
				})
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := newTestServer("", "", "")
			hr := NewHandlerRegistry(srv.cfg)
			mg := srv.ModuleGroup()
			tt.register(hr, mg, "first")
			tt.register(hr, mg, "second")

			rec := serveEngine(srv.echo, http.MethodGet, "/dup")
			assert.Equal(t, http.StatusOK, rec.Code)
			assert.Contains(t, rec.Body.String(), "first")
			assert.NotContains(t, rec.Body.String(), "second")
			assert.Len(t, srv.RouteConflicts(), 1)
		})
	}
}

// TestRouteConflictModuleCannotClaimProbePath pins the invariant the probe exemption rests on
// (probe_skip.go): the probe handler keeps its template, so a module claiming it never runs.
func TestRouteConflictModuleCannotClaimProbePath(t *testing.T) {
	tests := []struct {
		name       string
		newServer  func() *Server
		wantStatus int
	}{
		{
			name:       "probes_on_application_listener",
			newServer:  func() *Server { return newTestServer(testAPIV1Path, "", "") },
			wantStatus: http.StatusOK,
		},
		{
			name: "probe_listener_reservation",
			newServer: func() *Server {
				return newProbeTestServer(newProbeTestConfig(testAPIV1Path), &testLogger{})
			},
			wantStatus: http.StatusNotFound,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := tt.newServer()
			for _, route := range []string{healthRoute, testReadyRoute} {
				srv.ModuleGroup().Add(http.MethodGet, route, func(c HandlerContext) error {
					return c.String(http.StatusOK, "module")
				})
			}

			for _, route := range []string{healthRoute, testReadyRoute} {
				rec := serveEngine(srv.echo, http.MethodGet, testAPIV1Path+route)
				assert.Equal(t, tt.wantStatus, rec.Code, route)
				assert.NotEqual(t, "module", rec.Body.String(), route)
			}
			assert.Len(t, srv.RouteConflicts(), 2)
		})
	}
}

// TestServerStartRefusesRouteConflict pins the server-level refusal: without app, a
// recorded conflict fails Start before either listener binds.
func TestServerStartRefusesRouteConflict(t *testing.T) {
	srv := newProbeTestServer(newProbeTestConfig(""), &testLogger{})
	t.Cleanup(func() { _ = srv.Shutdown(context.Background()) })
	noop := func(c HandlerContext) error { return c.String(http.StatusOK, "") }
	srv.ModuleGroup().Add(http.MethodGet, "/dup", noop)
	srv.ModuleGroup().Add(http.MethodGet, "/dup", noop)

	err := requireStartRefusedBeforeBind(t, srv, "Start bound and served despite a recorded route conflict")
	require.ErrorIs(t, err, ErrDuplicateRoute)
	var dre *DuplicateRouteError
	require.ErrorAs(t, err, &dre)
	require.Len(t, dre.Conflicts, 1)
	assert.Equal(t, "/dup", dre.Conflicts[0].Path)
}

// TestServerProbeListenerHealthEqualsReady pins first-wins on both engines when the probe
// listener is enabled and server.path.health equals server.path.ready: health registers first,
// so it keeps the path on the probe engine, and Start refuses on the recorded conflict.
func TestServerProbeListenerHealthEqualsReady(t *testing.T) {
	cfg := newProbeTestConfig("")
	cfg.Server.Path.Health = "/probe"
	cfg.Server.Path.Ready = "/probe"
	srv := newProbeTestServer(cfg, &testLogger{})
	t.Cleanup(func() { _ = srv.Shutdown(context.Background()) })

	rec := serveEngine(srv.probeEcho, http.MethodGet, "/probe")
	assert.Equal(t, http.StatusOK, rec.Code, "readiness (503 before Start) must not replace health")
	assert.Contains(t, rec.Body.String(), `"status":"ok"`)
	require.Len(t, srv.RouteConflicts(), len(probeMethods))
	assert.Equal(t, "dispatchReady", srv.RouteConflicts()[0].Duplicate.HandlerName)

	err := requireStartRefusedBeforeBind(t, srv, "Start bound and served despite health == ready")
	require.ErrorIs(t, err, ErrDuplicateRoute)
}
