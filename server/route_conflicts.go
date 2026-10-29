package server

import (
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/labstack/echo/v5"
)

// ErrDuplicateRoute is what every *DuplicateRouteError matches, whatever the Reason of its
// conflicts. Match it with errors.Is.
var ErrDuplicateRoute = errors.New("duplicate route registration")

// RouteConflictReason says why a route registration was refused.
type RouteConflictReason string

// Reasons carried by RouteConflict.Reason (ADR-155).
const (
	// RouteConflictReasonDuplicate is a second registration of a route node; the first keeps it.
	RouteConflictReasonDuplicate RouteConflictReason = "duplicate"
	// RouteConflictReasonReservedMethod is a registration with one of echo's sentinel methods,
	// "echo_route_not_found" or "echo_route_any", which only the engine itself may register.
	RouteConflictReasonReservedMethod RouteConflictReason = "reserved engine method"
)

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

// RouteConflict reports one refused registration. For RouteConflictReasonDuplicate, two
// registrations of the same method + route node: Path is the duplicate's literal path and
// FirstPath the first registration's; they differ when the two templates name a path parameter
// or wildcard differently (/users/:id, /users/:uid), which echo's router treats as one route.
// For RouteConflictReasonReservedMethod, Duplicate is the refused registrant and First and
// FirstPath are empty.
type RouteConflict struct {
	Method    string
	Path      string
	FirstPath string
	First     RouteRegistrant
	Duplicate RouteRegistrant
	Reason    RouteConflictReason
}

// DuplicateRouteError reports every refused route registration at once.
// Server.Start and app startup both fail with it; errors.As recovers the conflicts,
// errors.Is(err, ErrDuplicateRoute) holds, and Unwrap returns the head (matching
// ErrDuplicateRoute) followed by one child per conflict.
type DuplicateRouteError struct {
	Conflicts []RouteConflict
}

// Error renders a head with the conflict count, then one line per conflict.
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
	label := ErrDuplicateRoute.Error()
	for i := range e.Conflicts {
		if e.Conflicts[i].Reason != RouteConflictReasonDuplicate {
			label = "route registration refused"
			break
		}
	}
	return conflictsHeadError{label: label, n: len(e.Conflicts)}
}

// conflictsHeadError heads a DuplicateRouteError; it matches ErrDuplicateRoute whatever its label.
type conflictsHeadError struct {
	label string
	n     int
}

func (h conflictsHeadError) Error() string {
	return fmt.Sprintf("%s (%d conflict(s))", h.label, h.n)
}

func (conflictsHeadError) Unwrap() error { return ErrDuplicateRoute }

func (r RouteRegistrant) label() string {
	return r.HandlerName + " (" + r.Package + ")"
}

// line renders one conflict as it appears in DuplicateRouteError's text.
func (c *RouteConflict) line() string {
	prefix := c.Method + " " + c.Path + " — "
	if c.Reason != RouteConflictReasonDuplicate {
		return prefix + string(c.Reason) + ", refused: " + c.Duplicate.label()
	}
	s := prefix + "first: " + c.First.label()
	if c.FirstPath != "" && c.FirstPath != c.Path {
		s += " at " + c.FirstPath
	}
	return s + ", duplicate: " + c.Duplicate.label()
}

// duplicateRouteError returns nil for no conflicts, otherwise a *DuplicateRouteError.
func duplicateRouteError(conflicts []RouteConflict) error {
	if len(conflicts) == 0 {
		return nil
	}
	return &DuplicateRouteError{Conflicts: conflicts}
}

// admittedRoute is one registration offered to routeAdmission. Path is the full template on the
// application listener, the spelling the conflict key is built from; it can differ from
// Descriptor.Path (a probe-listener route).
type admittedRoute struct {
	Method     string
	Path       string
	Descriptor *RouteDescriptor
}

// routeAdmission is the one admission step every go-bricks route door on a Server goes
// through; see admit. One instance per Server; a nil admission admits everything (bare
// newRouteGroup construction in tests).
type routeAdmission struct {
	mu        sync.Mutex
	seen      map[string]seenRoute // key: routeNodeKey(method, fullPath)
	conflicts []RouteConflict
}

// seenRoute is the first registration of a route node, with its literal path.
type seenRoute struct {
	reg  RouteRegistrant
	path string
}

func newRouteAdmission() *routeAdmission {
	return &routeAdmission{seen: make(map[string]seenRoute)}
}

// admit refuses a reserved engine method, then a duplicate of a claimed echo route node (the first
// registration keeps it), recording each refusal as a conflict; an admitted route is described in
// DefaultRouteRegistry, then added through add.
func (a *routeAdmission) admit(r admittedRoute, add func()) {
	if !a.claim(r) {
		return
	}
	DefaultRouteRegistry.Register(r.Descriptor)
	add()
}

// claim reports whether r is admitted, recording a conflict when it is not.
func (a *routeAdmission) claim(r admittedRoute) bool {
	if a == nil {
		return true
	}
	reg := RouteRegistrant{HandlerName: r.Descriptor.HandlerName, Package: r.Descriptor.Package}
	a.mu.Lock()
	defer a.mu.Unlock()
	if r.Method == echo.RouteNotFound || r.Method == echo.RouteAny {
		a.conflicts = append(a.conflicts, RouteConflict{
			Method: r.Method, Path: r.Path, Duplicate: reg, Reason: RouteConflictReasonReservedMethod,
		})
		return false
	}
	key := routeNodeKey(r.Method, r.Path)
	if first, dup := a.seen[key]; dup {
		a.conflicts = append(a.conflicts, RouteConflict{
			Method: r.Method, Path: r.Path, FirstPath: first.path, First: first.reg, Duplicate: reg,
			Reason: RouteConflictReasonDuplicate,
		})
		return false
	}
	a.seen[key] = seenRoute{reg: reg, path: r.Path}
	return true
}

// routeNodeKey identifies the echo router node method+path lands on: parameter names are
// dropped and nothing after the wildcard counts (walkRouteTemplate), so two templates with the
// same key are one route to echo, which would overwrite the first handler.
func routeNodeKey(method, path string) string {
	var b strings.Builder
	b.WriteString(method)
	b.WriteByte(' ')
	walkRouteTemplate(newRouteTemplate(path), func(kind routePartKind, text string) {
		if kind == routePartParam {
			b.WriteByte(':')
		} else {
			b.WriteString(text)
		}
	})
	return b.String()
}

func (a *routeAdmission) snapshot() []RouteConflict {
	if a == nil {
		return nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]RouteConflict, len(a.conflicts))
	copy(out, a.conflicts)
	return out
}
