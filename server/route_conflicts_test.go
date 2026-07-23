package server

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRouteConflictTrackerRecordsDuplicate(t *testing.T) {
	tr := newRouteConflictTracker()

	first := RouteRegistrant{HandlerName: "handlerA", Package: "pkg/a"}
	dup := RouteRegistrant{HandlerName: "handlerB", Package: "pkg/b"}

	tr.record(http.MethodGet, "/users", first)
	tr.record(http.MethodGet, "/users", dup)

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
		nilTracker.record(http.MethodGet, "/x", first)
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
