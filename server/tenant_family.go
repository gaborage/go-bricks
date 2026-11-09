package server

import (
	"context"
	"slices"
	"strconv"
	"sync"

	"github.com/labstack/echo/v5"

	"github.com/gaborage/go-bricks/internal/pathutil"
	"github.com/gaborage/go-bricks/logger"
	"github.com/gaborage/go-bricks/multitenant"
)

// tenantFamily is one tenant-owned route family.
type tenantFamily struct {
	prefix string              // the full template the family's echo group mounts at
	steps  echo.MiddlewareFunc // Authenticate, Tenant, the grammar check and the publish
	log    logger.Logger       // the Server's logger, for the leaf guard's refusals

	mu        sync.Mutex
	catchAlls map[string]struct{} // routeNodeKey(echo.RouteNotFound, full path) of each not-found catch-all
}

// familyPublishedKey holds the request context a family's steps published the tenant on.
const familyPublishedKey = "_tenant_family_published"

// guard is a family route's innermost middleware (routeMiddleware): it wraps the leaf handler
// h, around the typed leaf, so binding and JOSE run after it. h runs only on a request the
// tenant seat skipped (seatSkippedKey) whose tenant is still exactly the one the family's
// steps published. Anything else is a wiring fault: the framework 500, logged once, and h
// never runs. It guards wiring, not a security boundary: HandlerContext.Set can write the
// evidence it reads.
func (f *tenantFamily) guard(h echo.HandlerFunc) echo.HandlerFunc {
	return func(c *echo.Context) error {
		if skipped, _ := c.Get(seatSkippedKey).(bool); !skipped {
			return newTenantFault(newHandlerContext(c, nil), f.log, reasonSeatNotSkipped, nil)
		}
		if !holdsPublishedTenant(c) {
			return newTenantFault(newHandlerContext(c, nil), f.log, reasonTenantReplaced, nil)
		}
		return h(c)
	}
}

// holdsPublishedTenant reports whether c's request carries exactly the tenant the family's
// steps published. GetTenant answers false for a missing (nil) published context.
func holdsPublishedTenant(c *echo.Context) bool {
	published, _ := c.Get(familyPublishedKey).(context.Context)
	want, ok := multitenant.GetTenant(published)
	got, _ := multitenant.GetTenant(c.Request().Context())
	return ok && got == want
}

// familyNode is one family registrar's own middleware: the family root's (nil parent) or a
// Group's below it. Use appends to it; a route reads its ancestors' at registration.
type familyNode struct {
	parent *familyNode
	mw     []echo.MiddlewareFunc
}

// appendChain appends the middleware of n's ancestors, the family root first, then n's own.
func (n *familyNode) appendChain(chain []echo.MiddlewareFunc) []echo.MiddlewareFunc {
	if n == nil {
		return chain
	}
	return append(n.parent.appendChain(chain), n.mw...)
}

// TenantOwnedGroup mounts a route family that resolves its own tenant, after its own
// authentication when it has one, instead of passing the root tenant seat (ADR-164).
// Call it once per family in Module.RegisterRoutes, after every module Init, so p may
// close over what Init built. prefix is relative to r. The family is built on the
// application engine at the full prefix, so no parent group's middleware runs on it.
//
// On a family route the root chain runs first, unchanged, and its tenant seat skips
// the route. Then:
//  1. p.Authenticate, each step in order. No tenant exists yet.
//  2. p.Tenant, on every request under the prefix.
//  3. The framework checks the ID with multitenant.ValidateTenantID and publishes it on
//     the request context.
//  4. If no family route matches (an unknown sub-path, or a wrong method under the
//     prefix), the request gets the framework 404 here and step 5 never runs.
//  5. Middleware added with Use, nested-group middleware, route middleware, binding,
//     JOSE and the handler, all after the publish.
//
// A 4xx from steps 1-2, or a status-less error wrapping multitenant.ErrTenantResolutionFailed
// or ErrInvalidTenantID, goes to p.Deny, except a 413 and an error wrapping a deadline, a
// cancel, or multitenant.ErrNoTenant. A step that commits its own response keeps it.
// Everything else, including a 5xx, is framework-rendered.
// Steps 1-3 also run before step 4's 404. There a Tenant that reads a path parameter finds
// none on the prefix catch-alls, and the template's own on a wildcard route's not-found
// catch-all. Either way it must fail on an empty value with an error Deny answers; returning
// "" with a nil error fails the grammar backstop, a logged framework 500.
// Use and group middleware on a family, and on its nested groups, attach per route at
// registration, so they reach only routes registered after the call.
//
// It panics at the call site when:
//   - r is not a registrar the framework built, or r is already inside a family;
//   - p.Tenant is nil;
//   - multitenant is disabled, or the Server installed no tenant seat because its
//     tenant resolver could not be built;
//   - the Server has no tenant existence check (Options.TenantChecker), or its check cannot
//     answer (a nil pointer, or HasTenant("") panics or answers true; ADR-172);
//   - prefix is "" or "/" (the panic names the full prefix);
//   - the full prefix has a :param or * segment, covers the health or ready path, or
//     equals, covers or sits under another family's prefix on a segment boundary;
//   - it is called after Start.
//
// A route, or a group carrying middleware, at or below the prefix that was not registered
// through the family is trespass and fails startup as a RouteConflictReasonTenantOwned
// conflict; after Start it panics at registration instead, with an error matching
// ErrDuplicateRoute. Under a tenant resolver that may read the path, a prefix at or below a
// framework route root (ADR-168) fails startup as a RouteConflictReasonFrameworkRoot conflict
// (it panics after Start), and the family's steps are not mounted. Startup also refuses any installed global middleware beside a family
// (ErrTenantFamilyWithGlobalMiddleware).
func TenantOwnedGroup(r RouteRegistrar, prefix string, p TenantPolicy) RouteRegistrar {
	rg, ok := r.(*routeGroup)
	if !ok || rg.admission == nil {
		panic(tenantFamilyRefusal(prefix, "the registrar was not built by a Server; use ModuleGroup, RootGroup or a Group under them"))
	}
	relative := pathutil.NormalizePrefix(prefix)
	full := rg.combinePrefix(relative)
	if rg.family != nil {
		panic(tenantFamilyRefusal(full, "already inside a tenant-owned family at "+strconv.Quote(rg.family.prefix)))
	}
	if p.Tenant == nil {
		panic(tenantFamilyRefusal(full, "TenantPolicy.Tenant is nil"))
	}
	if relative == "" {
		panic(tenantFamilyRefusal(full, "the family prefix is empty or \"/\""))
	}
	if !isStaticTemplate(full) {
		panic(tenantFamilyRefusal(full, "a family prefix cannot hold a path parameter or wildcard"))
	}
	if !rg.cfg.Multitenant.Enabled {
		panic(tenantFamilyRefusal(full, "multitenant.enabled is false; a module shared with single-tenant binaries must branch on config"))
	}
	if !rg.admission.seated {
		panic(tenantFamilyRefusal(full, "the tenant seat is not installed: the configured tenant resolver could not be built"))
	}
	if probe, covered := rg.admission.coveredProbePath(full); covered {
		panic(tenantFamilyRefusal(full, "it equals or covers the probe path "+strconv.Quote(probe)))
	}
	if rg.admission.refuseFamilyUnderFrameworkRoot(full, getCallerPackage(2)) {
		// Start fails with the recorded conflict. The prefix gets a plain group, so the family's
		// steps are never mounted under the root, and each route registered through it is judged
		// like any other route there.
		return &routeGroup{group: rg.admission.engine.Group(full), prefix: full, cfg: rg.cfg, admission: rg.admission}
	}
	if rg.admission.checker == nil {
		panic(tenantFamilyRefusal(full, "the Server has no tenant existence check to verify the family's tenant with: "+
			"build it through the App, whose tenant resource source implements multitenant.TenantChecker, or pass Options.TenantChecker (ADR-172)"))
	}
	if err := vetExistence(rg.admission.checker); err != nil {
		panic(tenantFamilyRefusal(full, err.Error()))
	}
	if reason, claimed := rg.admission.claimFamily(full); !claimed {
		panic(tenantFamilyRefusal(full, reason))
	}
	// Copied before p is captured, so a later edit to the caller's slice cannot reach the chain.
	p.Authenticate = slices.Clone(p.Authenticate)
	fam := &tenantFamily{
		prefix:    full,
		log:       rg.admission.log,
		steps:     adaptMiddleware(p.middleware(rg.admission.log, rg.admission.checker), rg.cfg),
		catchAlls: map[string]struct{}{catchAllKey(full + "/*"): {}},
	}
	group := rg.admission.engine.Group(full)
	// Registered on the engine outside admission, which refuses RouteAny and RouteNotFound
	// (ADR-155). The any slot takes every method no family route takes at the bare prefix, so
	// echo never backtracks from it to a route outside the family.
	group.Any("", familyNotFound, fam.steps)
	group.RouteNotFound("/*", familyNotFound, fam.steps)
	return &routeGroup{
		group:      group,
		prefix:     full,
		cfg:        rg.cfg,
		admission:  rg.admission,
		family:     fam,
		familyNode: &familyNode{},
	}
}

// middleware runs p as a flat MiddlewareFunc, logging its refusals to l and verifying the
// published tenant with checker.
func (p TenantPolicy) middleware(l logger.Logger, checker multitenant.TenantChecker) MiddlewareFunc {
	return func(c HandlerContext, next func() error) error {
		_, _, err := p.run(c, l, checker, next)
		return err
	}
}

// familyNotFound answers a family request no route matched, once the steps let it through.
func familyNotFound(*echo.Context) error { return echo.ErrNotFound }

// addWildcardCatchAll registers the family's catch-all on g at relative, the spelling on g of
// full, an admitted family route's template, when full holds a wildcard and its echo route node
// holds no family catch-all yet: echo never backtracks from a wildcard node to prefix/*, so
// without it a wrong method gets echo's 405 and no step runs.
func (f *tenantFamily) addWildcardCatchAll(g *echo.Group, relative, full string) {
	if !holdsWildcard(full) {
		return
	}
	key := catchAllKey(full)
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, added := f.catchAlls[key]; added {
		return
	}
	// Steps only, outside admission, which refuses RouteNotFound (ADR-155).
	g.RouteNotFound(relative, familyNotFound, f.steps)
	f.catchAlls[key] = struct{}{}
}

// catchAllKey identifies the echo route node a family catch-all at full sits on.
func catchAllKey(full string) string { return routeNodeKey(echo.RouteNotFound, full) }

// tenantFamilyRefusal renders TenantOwnedGroup's call-site panic for prefix.
func tenantFamilyRefusal(prefix, reason string) string {
	return "server: tenant-owned group registration failed for " + strconv.Quote(prefix) + ": " + reason
}

// isStaticTemplate reports whether path holds no path parameter or wildcard in echo's grammar.
func isStaticTemplate(path string) bool {
	return !templateHolds(path, func(kind routePartKind) bool { return kind != routePartStatic })
}

// holdsWildcard reports whether path holds echo's wildcard; nothing after it counts, so the
// template ends there.
func holdsWildcard(path string) bool {
	return templateHolds(path, func(kind routePartKind) bool { return kind == routePartWildcard })
}

// templateHolds reports whether any part of path, in echo's grammar, is of a kind match accepts.
func templateHolds(path string, match func(routePartKind) bool) bool {
	held := false
	walkRouteTemplate(newRouteTemplate(path), func(kind routePartKind, _ string) {
		if match(kind) {
			held = true
		}
	})
	return held
}
