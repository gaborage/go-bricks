package server

import (
	"context"
	"net/http"
	"strings"
	"testing"

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
		Method: http.MethodGet, Path: "/users",
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
					Method: http.MethodGet, Path: "/one",
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
