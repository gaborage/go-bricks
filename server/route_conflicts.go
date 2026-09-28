package server

import (
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/gaborage/go-bricks/internal/pathutil"
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

// RouteConflict reports two registrations of the same method + route node. Path is the
// duplicate's literal path and FirstPath the first registration's; they differ when the two
// templates name a path parameter or wildcard differently (/users/:id, /users/:uid), which
// echo's router treats as one route.
type RouteConflict struct {
	Method    string
	Path      string
	FirstPath string
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
// registrants; the first registration's path is shown only where it differs.
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
	s := fmt.Sprintf("%s %s — first: %s (%s)", c.Method, c.Path, c.First.HandlerName, c.First.Package)
	if c.FirstPath != "" && c.FirstPath != c.Path {
		s += " at " + c.FirstPath
	}
	return s + fmt.Sprintf(", duplicate: %s (%s)", c.Duplicate.HandlerName, c.Duplicate.Package)
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
	seen      map[string]seenRoute // key: routeNodeKey(method, fullPath)
	conflicts []RouteConflict
}

// seenRoute is the first registration of a route node, with its literal path.
type seenRoute struct {
	reg  RouteRegistrant
	path string
}

func newRouteConflictTracker() *routeConflictTracker {
	return &routeConflictTracker{seen: make(map[string]seenRoute)}
}

// record reports whether method+fullPath names a new route node. A duplicate is recorded as a conflict
// and reported false, so the caller skips the engine Add and the first handler keeps the
// route. A nil tracker reports true: untracked groups register everything.
func (t *routeConflictTracker) record(method, fullPath string, reg RouteRegistrant) bool {
	if t == nil {
		return true
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	key := routeNodeKey(method, fullPath)
	if first, dup := t.seen[key]; dup {
		t.conflicts = append(t.conflicts, RouteConflict{
			Method: method, Path: fullPath, FirstPath: first.path, First: first.reg, Duplicate: reg,
		})
		return false
	}
	t.seen[key] = seenRoute{reg: reg, path: fullPath}
	return true
}

// routeNodeKey identifies the echo router node method+path lands on, mirroring
// DefaultRouter.Add (echo v5): a missing leading slash is added; an unescaped ':' starts a
// parameter anywhere in the path and its name runs to the next '/', so the name is dropped;
// an escaped "\:" is a literal colon and is kept verbatim; the first '*' outside a parameter
// name is the wildcard and ends the path, so anything after it is dropped. Two templates
// with the same key are one route to echo, which would overwrite the first handler.
func routeNodeKey(method, path string) string {
	path = pathutil.EnsureLeadingSlash(path)
	var b strings.Builder
	b.Grow(len(method) + 1 + len(path))
	b.WriteString(method)
	b.WriteByte(' ')
	for i := 0; i < len(path); i++ {
		switch c := path[i]; {
		case c == ':' && path[i-1] != '\\': // EnsureLeadingSlash made path[0] '/', so i >= 1 here
			b.WriteByte(':')
			for i+1 < len(path) && path[i+1] != '/' {
				i++
			}
		case c == '*':
			b.WriteByte('*')
			return b.String()
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
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
