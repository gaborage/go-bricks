package server

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/labstack/echo/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/config"
)

func TestRouteAdmissionRecordsDuplicate(t *testing.T) {
	DefaultRouteRegistry.Clear()
	t.Cleanup(DefaultRouteRegistry.Clear)
	first := RouteRegistrant{HandlerName: "handlerA", Package: "pkg/a"}
	dup := RouteRegistrant{HandlerName: "handlerB", Package: "pkg/b"}
	route := func(method string, reg RouteRegistrant) admittedRoute {
		return admittedRoute{Method: method, Path: "/users", Descriptor: modellessDescriptor(method, "/users", reg)}
	}
	added := 0
	add := func() { added++ }

	a := newRouteAdmission()
	a.admit(route(http.MethodGet, first), add)
	a.admit(route(http.MethodGet, dup), add)
	assert.Equal(t, 1, added, "only the admitted route is added")

	conflicts := a.snapshot()
	require.Len(t, conflicts, 1)
	assert.Equal(t, RouteConflict{
		Method: http.MethodGet, Path: "/users", FirstPath: "/users",
		First: first, Duplicate: dup, Reason: RouteConflictReasonDuplicate,
	}, conflicts[0])

	// Different methods, same path — no conflict.
	aMethods := newRouteAdmission()
	aMethods.admit(route(http.MethodGet, first), add)
	aMethods.admit(route(http.MethodPost, dup), add)
	assert.Empty(t, aMethods.snapshot())

	// A nil admission must not panic, admits everything and reports no conflicts.
	var nilAdmission *routeAdmission
	added = 0
	assert.NotPanics(t, func() {
		nilAdmission.admit(route(http.MethodGet, first), add)
		nilAdmission.admit(route(http.MethodGet, dup), add)
	})
	assert.Equal(t, 2, added, "an untracked group registers everything")
	assert.Nil(t, nilAdmission.snapshot())
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
	// full path proves admission propagation through Group().
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
	getOneDuplicate := RouteConflict{
		Method: http.MethodGet, Path: "/one", FirstPath: "/one",
		First:     RouteRegistrant{HandlerName: "a", Package: "pkg/a"},
		Duplicate: RouteRegistrant{HandlerName: "b", Package: "pkg/b"},
		Reason:    RouteConflictReasonDuplicate,
	}
	reservedConflict := RouteConflict{
		Method: echo.RouteNotFound, Path: "/partner/v2/*",
		Duplicate: RouteRegistrant{HandlerName: "b", Package: "pkg/b"},
		Reason:    RouteConflictReasonReservedMethod,
	}
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
				getOneDuplicate,
				{
					Method: http.MethodPost, Path: "/two",
					First:     RouteRegistrant{HandlerName: "c", Package: "pkg/c"},
					Duplicate: RouteRegistrant{HandlerName: "d", Package: "pkg/d"},
					Reason:    RouteConflictReasonDuplicate,
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
				Reason:    RouteConflictReasonDuplicate,
			}},
			wantMsg: "duplicate route registration (1 conflict(s))\n" +
				"GET /users/:uid — first: a (pkg/a) at /users/:id, duplicate: b (pkg/b)",
		},
		{
			name:      "reserved_method_only",
			conflicts: []RouteConflict{reservedConflict},
			wantMsg: "route registration refused (1 conflict(s))\n" +
				"echo_route_not_found /partner/v2/* — reserved engine method, refused: b (pkg/b)",
		},
		{
			name:      "duplicate_and_reserved_method",
			conflicts: []RouteConflict{getOneDuplicate, reservedConflict},
			wantMsg: "route registration refused (2 conflict(s))\n" +
				"GET /one — first: a (pkg/a), duplicate: b (pkg/b)\n" +
				"echo_route_not_found /partner/v2/* — reserved engine method, refused: b (pkg/b)",
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

// TestRouteNodeKey pins the admission key to echo v5's node identity (DefaultRouter.Add).
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
		{name: "inline_verb_vs_param", a: `/x/:id\:cancel`, b: "/x/:id"},
		{name: "inline_verbs_differ", a: `/x/:id\:cancel`, b: `/x/:id\:approve`},
		{name: "inline_verb_param_name_differs", a: `/x/:id\:cancel`, b: `/x/:uid\:cancel`, sameNode: true},
		{name: "inline_verb_empty_param_name", a: `/x/:\:c`, b: `/x/:id\:c`, sameNode: true},
		{name: "inline_verb_one_char_param_name", a: `/x/:a\:cancel`, b: "/x/:a"},
		{name: "escaped_colon_before_param_colon", a: `/x/:a\:b:c`, b: "/x/:z", sameNode: true},
		{name: "escaped_colon_then_param_colon", a: `/x/:a\::c`, b: "/x/:z", sameNode: true},
		{name: "escaped_colon_before_star", a: `/x/:a\:c*`, b: "/x/:z", sameNode: true},
		{name: "colon_in_param_name_before_escaped_colon", a: `/x/:a:b\:c`, b: "/x/:z", sameNode: true},
		{name: "star_in_param_name_before_escaped_colon", a: `/x/:a*\:c`, b: "/x/:z", sameNode: true},
		{name: "backslash_in_param_name", a: `/x/:a\bc`, b: "/x/:z", sameNode: true},
		{name: "trailing_backslash_in_param_name", a: `/x/:a\`, b: "/x/:z", sameNode: true},
		{name: "inline_verb_holding_escaped_colon", a: `/x/:id\:a\:b`, b: "/x/:id"},
		{name: "inline_verb_before_next_segment", a: `/x/:id\:c/:y`, b: "/x/:z/:y"},
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
// echo bump that changes node identity fails here: a request for the first template reaching
// the second handler means echo kept one node.
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
		{name: "param_vs_escaped_colon", first: "/a:c", second: `/a\:b`, request: "/aq"},
		{name: "escaped_colon_vs_param", first: `/a\:b`, second: "/a:c", request: "/a:b"},
		{name: "param_vs_inline_verb", first: "/x/:id", second: `/x/:id\:cancel`, request: "/x/42"},
		{name: "inline_verb_vs_param", first: `/x/:id\:cancel`, second: "/x/:id", request: "/x/42:cancel"},
		{name: "inline_verbs_differ", first: `/x/:id\:cancel`, second: `/x/:id\:approve`, request: "/x/42:cancel"},
		{name: "inline_verb_param_name_differs", first: `/x/:id\:cancel`, second: `/x/:uid\:cancel`, request: "/x/42:cancel"},
		{name: "inline_verb_one_char_param_name", first: "/x/:a", second: `/x/:a\:cancel`, request: "/x/q"},
		{name: "escaped_colon_then_param_colon", first: "/x/:z", second: `/x/:a\::c`, request: "/x/q"},
		{name: "escaped_colon_before_star", first: "/x/:z", second: `/x/:a\:c*`, request: "/x/q"},
		{name: "colon_in_param_name_before_escaped_colon", first: "/x/:z", second: `/x/:a:b\:c`, request: "/x/q"},
		{name: "star_in_param_name_before_escaped_colon", first: "/x/:z", second: `/x/:a*\:c`, request: "/x/q"},
		{name: "backslash_in_param_name", first: "/x/:z", second: `/x/:a\bc`, request: "/x/q"},
		{name: "trailing_backslash_in_param_name", first: "/x/:z", second: `/x/:a\`, request: "/x/q"},
		{name: "inline_verb_holding_escaped_colon", first: `/x/:id\:a\:b`, second: "/x/:id", request: "/x/42:a:b"},
		{name: "inline_verb_before_next_segment", first: `/x/:id\:c/:y`, second: "/x/:z/:y", request: "/x/42:c/7"},
		{name: "param_vs_static", first: "/users/:id", second: "/users/me", request: "/users/42"},
		{name: "param_vs_deeper_path", first: "/users/:id", second: "/users/:id/x", request: "/users/42"},
		{name: "wildcard_vs_param", first: "/f/*", second: "/f/:id", request: "/f/"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			echoSameNode := echoReachesLast(t, tt.first, tt.second, tt.request)
			keySameNode := routeNodeKey(http.MethodGet, tt.first) == routeNodeKey(http.MethodGet, tt.second)
			assert.Equal(t, echoSameNode, keySameNode, "routeNodeKey disagrees with echo's router")
		})
	}
}

// TestRouteNodeKeyAgreesWithEchoSweep compares routeNodeKey with echo's router over every pair of
// one- and two-token templates. Echo shares a node when, in both registration orders, a request
// built from the template registered first reaches the handler registered last.
func TestRouteNodeKeyAgreesWithEchoSweep(t *testing.T) {
	tokens := []string{":id", ":", `\:`, `\:cancel`, "a", "/", "*"}
	templates := make([]string, 0, len(tokens)*(len(tokens)+1))
	for _, a := range tokens {
		templates = append(templates, "/"+a)
		for _, b := range tokens {
			templates = append(templates, "/"+a+b)
		}
	}
	reverse := func(p string) string { return echo.RouteInfo{Path: p}.Reverse("v1", "v2") }
	for i, a := range templates {
		for _, b := range templates[i+1:] {
			echoSameNode := echoReachesLast(t, a, b, reverse(a)) && echoReachesLast(t, b, a, reverse(b))
			keySameNode := routeNodeKey(http.MethodGet, a) == routeNodeKey(http.MethodGet, b)
			assert.Equal(t, echoSameNode, keySameNode, "routeNodeKey disagrees with echo's router on %q and %q", a, b)
		}
	}
}

// echoReachesLast registers first then second on a fresh engine (overwrite on, as echo.New sets
// it) and reports whether request reaches second's handler.
func echoReachesLast(t *testing.T, first, second, request string) bool {
	t.Helper()
	e := echo.New()
	e.GET(first, func(c *echo.Context) error { return c.String(http.StatusOK, "first") })
	e.GET(second, func(c *echo.Context) error { return c.String(http.StatusOK, "second") })
	rec := serveEngine(e, http.MethodGet, request)
	require.Equal(t, http.StatusOK, rec.Code, "%s must reach a handler with %q then %q registered", request, first, second)
	return rec.Body.String() == "second"
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
	// The escaped template registers before the param one on purpose.
	for _, p := range []string{"/users/:id", "/users/:id/x", "/users/me", `/a\:b`, "/a:c"} {
		body := p
		mg.Add(http.MethodGet, p, func(c HandlerContext) error { return c.String(http.StatusOK, body) })
	}
	assert.Empty(t, srv.RouteConflicts())
	for req, want := range map[string]string{
		"/users/42": "/users/:id", "/users/42/x": "/users/:id/x", "/users/me": "/users/me", "/aq": "/a:c", "/a:b": `/a\:b`,
	} {
		assert.Equal(t, want, serveEngine(srv.echo, http.MethodGet, req).Body.String(), req)
	}
}

// TestRouteConflictInlineVerbBesideParam pins echo v5.4.1's inline verb: /:id\:cancel is its own
// node beside /:id, so the pair boots and a request ending in :cancel reaches the verb handler.
func TestRouteConflictInlineVerbBesideParam(t *testing.T) {
	srv := newTestServer("", "", "")
	mg := srv.ModuleGroup()
	mg.Add(http.MethodGet, "/v1/orders/:id", func(c HandlerContext) error {
		return c.String(http.StatusOK, "get:"+c.Param("id"))
	})
	mg.Add(http.MethodGet, `/v1/orders/:id\:cancel`, func(c HandlerContext) error {
		return c.String(http.StatusOK, "cancel:"+c.Param("id"))
	})
	require.Empty(t, srv.RouteConflicts())

	errCh := startServer(srv)
	t.Cleanup(func() { shutdownAndDrain(t, srv, errCh) })
	waitForServerReady(t, srv)
	tests := []struct {
		name string
		path string
		want string
	}{
		{name: "inline_verb", path: "/v1/orders/42:cancel", want: "cancel:42"},
		{name: "param", path: "/v1/orders/42", want: "get:42"},
		{name: "percent_encoded_colon_is_param_value", path: "/v1/orders/42%3Acancel", want: "get:42%3Acancel"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp, err := doRequest(t.Context(), noKeepAliveClient(), http.MethodGet, "http://"+srv.BoundAddr().String()+tt.path)
			require.NoError(t, err)
			assert.Equal(t, http.StatusOK, resp.code)
			assert.Equal(t, tt.want, resp.body)
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
	assert.Equal(t, RouteConflictReasonDuplicate, dre.Conflicts[0].Reason)
	name := dre.Conflicts[0].First.HandlerName
	assert.Equal(t, "duplicate route registration (1 conflict(s))\nGET /dup — first: "+name+" ("+serverPackagePath+"), duplicate: "+name+" ("+serverPackagePath+")", err.Error())
}

const reservedTemplate = "/partner/v2/*"

// reservedDoors registers method at reservedTemplate through each module route door; group is
// a registrar at /partner/v2.
var reservedDoors = []struct {
	name     string
	register func(srv *Server, group RouteRegistrar, method string)
}{
	{name: "module_group_add", register: func(srv *Server, _ RouteRegistrar, method string) {
		srv.ModuleGroup().Add(method, reservedTemplate, func(c HandlerContext) error { return c.String(http.StatusTeapot, "") })
	}},
	{name: "register_handler", register: func(srv *Server, _ RouteRegistrar, method string) {
		RegisterHandler(NewHandlerRegistry(srv.cfg), srv.ModuleGroup(), method, reservedTemplate,
			func(_ EmptyRequest, _ HandlerContext) (helloResp, IAPIError) { return helloResp{}, nil })
	}},
	{name: "group_registrar_add", register: func(_ *Server, group RouteRegistrar, method string) {
		group.Add(method, "/*", func(c HandlerContext) error { return c.String(http.StatusTeapot, "") })
	}},
}

// TestServerStartRefusesReservedEngineMethod pins that echo's sentinel methods are refused at every
// module door, on either side of the group catch-alls echo registers when /partner/v2 gets middleware.
func TestServerStartRefusesReservedEngineMethod(t *testing.T) {
	orders := []struct {
		name            string
		middlewareFirst bool
	}{{name: "after_group", middlewareFirst: true}, {name: "before_group"}}
	for _, method := range []string{echo.RouteNotFound, echo.RouteAny} {
		for _, door := range reservedDoors {
			for _, order := range orders {
				t.Run(method+"_"+door.name+"_"+order.name, func(t *testing.T) { requireReservedRefusal(t, method, door.register, order.middlewareFirst) })
			}
		}
	}
}

func requireReservedRefusal(t *testing.T, method string, register func(*Server, RouteRegistrar, string), middlewareFirst bool) {
	t.Helper()
	srv := newTestServer("", "", "")
	t.Cleanup(func() { _ = srv.Shutdown(context.Background()) })
	group := srv.ModuleGroup().Group("/partner/v2")
	addGroupMiddleware := func() { group.Use(func(_ HandlerContext, next func() error) error { return next() }) }
	if middlewareFirst {
		addGroupMiddleware()
	}
	mark := DefaultRouteRegistry.Count()
	register(srv, group, method)
	if !middlewareFirst {
		addGroupMiddleware()
	}

	assert.Equal(t, mark, DefaultRouteRegistry.Count(), "a refused registration writes no descriptor")
	conflicts := srv.RouteConflicts()
	require.Len(t, conflicts, 1)
	assert.Equal(t, RouteConflict{
		Method: method, Path: reservedTemplate, Duplicate: conflicts[0].Duplicate, Reason: RouteConflictReasonReservedMethod,
	}, conflicts[0], "a reserved refusal names no first registrant")
	err := requireStartRefusedBeforeBind(t, srv, "Start bound and served despite a reserved engine method")
	require.ErrorIs(t, err, ErrDuplicateRoute)
	for _, want := range []string{method + " " + reservedTemplate, "reserved engine method"} {
		assert.Contains(t, err.Error(), want)
	}
	assert.NotContains(t, err.Error(), "duplicate")
}

// TestGroupCatchAllsUnaffectedByAdmission guards echo's own group catch-alls, which never pass
// through admission: unknown sub-paths under each middleware-bearing group still run its middleware.
func TestGroupCatchAllsUnaffectedByAdmission(t *testing.T) {
	srv := newTestServer("", "", "")
	prefixes := []string{"/partner/v2", "/billing"}
	for _, prefix := range prefixes {
		marker := func(c HandlerContext, next func() error) error {
			c.ResponseWriter().Header().Set("X-Group", prefix)
			return next()
		}
		srv.ModuleGroup().Group(prefix, marker).Add(http.MethodGet, "/known", func(c HandlerContext) error {
			return c.String(http.StatusOK, "")
		})
	}
	require.Empty(t, srv.RouteConflicts(), "no recorded conflict, so Start would not refuse the route table")

	for _, prefix := range prefixes {
		rec := serveEngine(srv.echo, http.MethodGet, prefix+"/unknown")
		assert.Equal(t, http.StatusNotFound, rec.Code, prefix)
		assert.Equal(t, prefix, rec.Header().Get("X-Group"), prefix)
	}
}

// TestServerProbeListenerHealthEqualsReady pins first-wins on both engines when the probe
// listener is enabled and server.path.health equals server.path.ready: health registers first,
// so it keeps the path on the probe engine, and Start refuses on the recorded conflict.
func TestServerProbeListenerHealthEqualsReady(t *testing.T) {
	srv := newProbeTestServer(sharedProbePath(newProbeTestConfig("")), &testLogger{})
	t.Cleanup(func() { _ = srv.Shutdown(context.Background()) })

	rec := serveEngine(srv.probeEcho, http.MethodGet, "/probe")
	assert.Equal(t, http.StatusOK, rec.Code, "readiness (503 before Start) must not replace health")
	assert.Contains(t, rec.Body.String(), `"status":"ok"`)
	require.Len(t, srv.RouteConflicts(), len(probeMethods))
	assert.Equal(t, "dispatchReady", srv.RouteConflicts()[0].Duplicate.HandlerName)

	err := requireStartRefusedBeforeBind(t, srv, "Start bound and served despite health == ready")
	require.ErrorIs(t, err, ErrDuplicateRoute)
}

// sharedProbePath points health and ready at one path, so readiness is the refused duplicate.
func sharedProbePath(cfg *config.Config) *config.Config {
	cfg.Server.Path.Health = "/probe"
	cfg.Server.Path.Ready = "/probe"
	return cfg
}

// TestRouteAdmissionRefusedDuplicateIsNotDescribed pins that every route door describes only the
// route admission keeps: a refused duplicate writes no route-registry descriptor.
func TestRouteAdmissionRefusedDuplicateIsNotDescribed(t *testing.T) {
	noop := func(c HandlerContext) error { return c.String(http.StatusOK, "") }
	typed := func(_ EmptyRequest, _ HandlerContext) (helloResp, IAPIError) { return helloResp{}, nil }
	tests := []struct {
		name      string
		newServer func() *Server // nil builds the default test server
		register  func(srv *Server)
		path      string
		methods   []string
	}{
		{
			name: "typed_get_twice",
			register: func(srv *Server) {
				hr := NewHandlerRegistry(srv.cfg)
				GET(hr, srv.ModuleGroup(), "/admitted", typed)
				GET(hr, srv.ModuleGroup(), "/admitted", typed)
			},
			path:    "/admitted",
			methods: []string{http.MethodGet},
		},
		{
			name: "raw_add_twice",
			register: func(srv *Server) {
				srv.ModuleGroup().Add(http.MethodGet, "/admitted", noop)
				srv.ModuleGroup().Add(http.MethodGet, "/admitted", noop)
			},
			path:    "/admitted",
			methods: []string{http.MethodGet},
		},
		{
			name:     "module_route_on_probe_path",
			register: func(srv *Server) { srv.ModuleGroup().Add(http.MethodGet, healthRoute, noop) },
			path:     healthRoute,
			methods:  []string{http.MethodGet},
		},
		{
			name:      "shared_health_ready_path_port0",
			newServer: func() *Server { return New(sharedProbePath(newTestConfig("", "", "")), &testLogger{}) },
			path:      "/probe",
			methods:   probeMethods,
		},
		{
			name:      "probe_listener_duplicate",
			newServer: func() *Server { return newProbeTestServer(sharedProbePath(newProbeTestConfig("")), &testLogger{}) },
			path:      "/probe",
			methods:   probeMethods,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			DefaultRouteRegistry.Clear()
			t.Cleanup(DefaultRouteRegistry.Clear)
			newServer := tt.newServer
			if newServer == nil {
				newServer = func() *Server { return newTestServer("", "", "") }
			}
			srv := newServer()
			t.Cleanup(func() { _ = srv.Shutdown(context.Background()) })
			if tt.register != nil {
				tt.register(srv)
			}

			conflicts := srv.RouteConflicts()
			require.Len(t, conflicts, len(tt.methods), "one refused route per method")
			routes := DefaultRouteRegistry.ByPath(tt.path)
			described := map[string][]string{}
			for i := range routes {
				described[routes[i].Method] = append(described[routes[i].Method], routes[i].HandlerName)
			}
			for i, method := range tt.methods {
				require.Equal(t, method, conflicts[i].Method)
				assert.Equal(t, []string{conflicts[i].First.HandlerName}, described[method],
					"%s %s: only the admitted route is described", method, tt.path)
			}
		})
	}
}
