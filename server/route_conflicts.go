package server

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"

	"github.com/labstack/echo/v5"

	"github.com/gaborage/go-bricks/internal/pathutil"
	"github.com/gaborage/go-bricks/logger"
)

// ErrDuplicateRoute is what every *DuplicateRouteError matches, whatever the Reason of its
// conflicts. Match it with errors.Is.
var ErrDuplicateRoute = errors.New("duplicate route registration")

// RouteConflictReason says why a route registration was refused.
type RouteConflictReason string

// Reasons carried by RouteConflict.Reason: ADR-155's two, and a third for tenant-owned route families.
const (
	// RouteConflictReasonDuplicate is a second registration of a route node; the first keeps it.
	RouteConflictReasonDuplicate RouteConflictReason = "duplicate"
	// RouteConflictReasonReservedMethod is a registration with one of echo's sentinel methods,
	// "echo_route_not_found" or "echo_route_any", which only the engine itself may register.
	RouteConflictReasonReservedMethod RouteConflictReason = "reserved engine method"
	// RouteConflictReasonTenantOwned is a route, or a group carrying middleware, at or below a
	// tenant-owned route family's prefix that did not come through the family.
	RouteConflictReasonTenantOwned RouteConflictReason = "inside a tenant-owned route family"
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

// RouteConflict reports one refused registration. What each field means depends on Reason.
type RouteConflict struct {
	// Method is the refused route's HTTP method. Under RouteConflictReasonTenantOwned it is empty
	// when the refused registration is a group carrying middleware.
	Method string
	// Path is the refused registration's literal full path: the duplicate's under
	// RouteConflictReasonDuplicate, the refused route's under RouteConflictReasonReservedMethod, and
	// the route's or the group's full prefix under RouteConflictReasonTenantOwned.
	Path string
	// FirstPath under RouteConflictReasonDuplicate is the first registration's literal path; it
	// differs from Path when the two templates name a path parameter or wildcard differently
	// (/users/:id, /users/:uid), which echo's router treats as one route. Under
	// RouteConflictReasonTenantOwned it is the family's prefix. Empty under
	// RouteConflictReasonReservedMethod.
	FirstPath string
	// First is the registration that keeps the route under RouteConflictReasonDuplicate. Empty under
	// the other reasons.
	First RouteRegistrant
	// Duplicate is the refused registrant under every reason. For a group under
	// RouteConflictReasonTenantOwned only Package is set: the package that called Group or Use.
	Duplicate RouteRegistrant
	// Reason says why the registration was refused.
	Reason RouteConflictReason
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
	if c.Reason == RouteConflictReasonTenantOwned {
		refused := " — " + string(c.Reason) + " at " + c.FirstPath + ", refused: "
		if c.Method == "" {
			return "group " + c.Path + refused + "middleware (" + c.Duplicate.Package + ")"
		}
		return c.Method + " " + c.Path + refused + c.Duplicate.label()
	}
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
// newRouteGroup construction in tests). It also holds the Server's tenant-owned family
// record: the engine families are built on, the logger their refusals go to, the probe paths
// they may not cover, and each family's full prefix (claimFamily).
type routeAdmission struct {
	mu         sync.Mutex
	seen       map[string]seenRoute // key: routeNodeKey(method, fullPath)
	conflicts  []RouteConflict
	engine     *echo.Echo    // the application engine; set by New
	log        logger.Logger // the Server's logger; set by New
	probePaths []string      // full health and ready paths on the application listener
	families   []string      // each tenant-owned family's full prefix, in build order

	// registered is every admitted route (family routes and probes included) and every
	// middleware-bearing group not built through a family, covered or not, in registration
	// order, each as its tenant-owned conflict without FirstPath.
	registered []RouteConflict
}

// seenRoute is the first registration of a route node, with its literal path.
type seenRoute struct {
	reg  RouteRegistrant
	path string
}

// tenantOwnedConflict is the RouteConflictReasonTenantOwned conflict for a route (or, with an
// empty method, a group), its FirstPath left for the family that refuses it.
func tenantOwnedConflict(method, path string, reg RouteRegistrant) RouteConflict {
	return RouteConflict{Method: method, Path: path, Duplicate: reg, Reason: RouteConflictReasonTenantOwned}
}

func newRouteAdmission() *routeAdmission {
	return &routeAdmission{seen: make(map[string]seenRoute)}
}

// admit refuses a reserved engine method, then a route at or below a tenant-owned family's prefix
// that did not come through the family, then a duplicate of a claimed echo route node (the first
// registration keeps it), recording each refusal as a conflict; an admitted route is recorded in
// registration order for a later family's trespass scan, described in DefaultRouteRegistry, then
// added through add.
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
	c := tenantOwnedConflict(r.Method, r.Path, reg)
	if !r.Descriptor.TenantOwned && a.refuseTrespassLocked(&c) {
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
	a.registered = append(a.registered, c)
	return true
}

// admitGroup records a middleware-bearing group not built through a family at its full prefix
// path, whether or not a family covers it, and records a conflict when one already does. pkg
// called Group or Use.
func (a *routeAdmission) admitGroup(path, pkg string) {
	if a == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	c := tenantOwnedConflict("", path, RouteRegistrant{Package: pkg})
	a.registered = append(a.registered, c)
	a.refuseTrespassLocked(&c)
}

// refuseTrespassLocked sets c.FirstPath to the family prefix covering c.Path and records c as a
// conflict, reporting whether a family covers it. a.mu is held.
func (a *routeAdmission) refuseTrespassLocked(c *RouteConflict) bool {
	for _, family := range a.families {
		if underPrefix(c.Path, family) {
			c.FirstPath = family
			a.conflicts = append(a.conflicts, *c)
			return true
		}
	}
	return false
}

// reportTrespassLocked records every registered route and group at or below a newly built
// family's prefix, interleaved in registration order; families never overlap and a family admits
// no route before its scan, so no family's routes are reported. a.mu is held.
func (a *routeAdmission) reportTrespassLocked(family string) {
	for i := range a.registered {
		if underPrefix(a.registered[i].Path, family) {
			c := a.registered[i]
			c.FirstPath = family
			a.conflicts = append(a.conflicts, c)
		}
	}
}

// underPrefix reports whether path is prefix or continues past it at a "/" segment boundary.
func underPrefix(path, prefix string) bool {
	_, ok := pathutil.StripPathPrefix(path, prefix)
	return ok
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

// coveredProbePath returns the first probe path full equals or covers.
func (a *routeAdmission) coveredProbePath(full string) (string, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, p := range a.probePaths {
		if underPrefix(p, full) {
			return p, true
		}
	}
	return "", false
}

// claimFamily records full as a tenant-owned family's prefix, unless it equals, covers or sits
// under an existing family's prefix; then it returns that prefix and false, recording nothing.
func (a *routeAdmission) claimFamily(full string) (string, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, f := range a.families {
		if underPrefix(full, f) {
			return f, false
		}
		if underPrefix(f, full) {
			return f, false
		}
	}
	a.families = append(a.families, full)
	a.reportTrespassLocked(full)
	return "", true
}

func (a *routeAdmission) familyPrefixes() []string {
	if a == nil {
		return nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return slices.Clone(a.families)
}
