package server

import (
	"errors"
	"fmt"
	"strings"
	"sync"
)

// ErrDuplicateRoute is what every *DuplicateRouteError matches: two registrations claimed
// the same method + full path. Match it with errors.Is.
var ErrDuplicateRoute = errors.New("duplicate route registration")

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

// DuplicateRouteError reports every duplicate method+path registration at once.
// Server.Start and app startup both fail with it; errors.As recovers the conflicts,
// errors.Is(err, ErrDuplicateRoute) holds, and Unwrap returns the head (wrapping
// ErrDuplicateRoute) followed by one child per conflict.
type DuplicateRouteError struct {
	Conflicts []RouteConflict
}

// Error renders a head with the conflict count, then one line per conflict naming both
// registrants.
func (e *DuplicateRouteError) Error() string {
	var b strings.Builder
	b.WriteString(e.head().Error())
	for i := range e.Conflicts {
		b.WriteByte('\n')
		b.WriteString(e.Conflicts[i].line())
	}
	return b.String()
}

// Unwrap returns the head, which wraps ErrDuplicateRoute, then one error per conflict line.
func (e *DuplicateRouteError) Unwrap() []error {
	errs := make([]error, 0, 1+len(e.Conflicts))
	errs = append(errs, e.head())
	for i := range e.Conflicts {
		errs = append(errs, errors.New(e.Conflicts[i].line()))
	}
	return errs
}

func (e *DuplicateRouteError) head() error {
	return fmt.Errorf("%w (%d conflict(s))", ErrDuplicateRoute, len(e.Conflicts))
}

// line renders one conflict as it appears in DuplicateRouteError's text.
func (c *RouteConflict) line() string {
	return fmt.Sprintf("%s %s — first: %s (%s), duplicate: %s (%s)",
		c.Method, c.Path,
		c.First.HandlerName, c.First.Package,
		c.Duplicate.HandlerName, c.Duplicate.Package)
}

// duplicateRouteError returns nil for no conflicts, otherwise a *DuplicateRouteError.
func duplicateRouteError(conflicts []RouteConflict) error {
	if len(conflicts) == 0 {
		return nil
	}
	return &DuplicateRouteError{Conflicts: conflicts}
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

// record reports whether method+fullPath is a new route. A duplicate is recorded as a conflict
// and reported false, so the caller skips the engine Add and the first handler keeps the
// route. A nil tracker reports true: untracked groups register everything.
func (t *routeConflictTracker) record(method, fullPath string, reg RouteRegistrant) bool {
	if t == nil {
		return true
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	key := formatHandlerID(method, fullPath)
	if first, dup := t.seen[key]; dup {
		t.conflicts = append(t.conflicts, RouteConflict{
			Method: method, Path: fullPath, First: first, Duplicate: reg,
		})
		return false
	}
	t.seen[key] = reg
	return true
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
