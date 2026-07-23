package server

import "sync"

// serverPackagePath attributes framework-registered routes (health/ready probes)
// in conflict reports.
const serverPackagePath = "github.com/gaborage/go-bricks/server"

// RouteRegistrant identifies who registered a route, for conflict reporting.
// ModuleName is intentionally absent: no registration path populates it
// (attribution in route logging uses registration-order spans instead).
type RouteRegistrant struct {
	HandlerName string
	Package     string
}

// RouteConflict reports two registrations of the same method + full path.
type RouteConflict struct {
	Method    string
	Path      string
	First     RouteRegistrant
	Duplicate RouteRegistrant
}

// routeConflictTracker records every route added through a Server's routeGroups
// and accumulates conflicts. One instance per Server; a nil tracker disables
// recording (bare newRouteGroup construction in tests).
type routeConflictTracker struct {
	mu        sync.Mutex
	seen      map[string]RouteRegistrant // key: formatHandlerID(method, fullPath)
	conflicts []RouteConflict
}

func newRouteConflictTracker() *routeConflictTracker {
	return &routeConflictTracker{seen: make(map[string]RouteRegistrant)}
}

func (t *routeConflictTracker) record(method, fullPath string, reg RouteRegistrant) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	key := formatHandlerID(method, fullPath)
	if first, dup := t.seen[key]; dup {
		t.conflicts = append(t.conflicts, RouteConflict{
			Method: method, Path: fullPath, First: first, Duplicate: reg,
		})
		return
	}
	t.seen[key] = reg
}

func (t *routeConflictTracker) snapshot() []RouteConflict {
	if t == nil {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]RouteConflict, len(t.conflicts))
	copy(out, t.conflicts)
	return out
}
