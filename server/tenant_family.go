package server

import (
	"slices"
	"strconv"
	"sync"

	"github.com/labstack/echo/v5"

	"github.com/gaborage/go-bricks/internal/pathutil"
	"github.com/gaborage/go-bricks/logger"
)

// tenantFamily is one tenant-owned route family.
type tenantFamily struct {
	prefix string              // the full template the family's echo group mounts at
	steps  echo.MiddlewareFunc // Authenticate, Tenant, the grammar check and the publish

	mu        sync.Mutex
	catchAlls map[string]struct{} // routeNodeKey(echo.RouteNotFound, full path) of each not-found catch-all
}

// tenantOwnedGroup mounts a tenant-owned route family at r's prefix joined with prefix. The
// family's echo group is built on the application engine at that full prefix, not under r, so
// no parent group's middleware ever runs on its routes. The family's steps (p.run) run outermost
// on every family route and on its catch-alls: an any-method route at the prefix, a not-found
// route at prefix/*, and one at each family template ending in a wildcard. So an unknown
// sub-path or a wrong method answers the framework 404 only after the tenant is published, and
// Deny before it. A Tenant reading a path parameter finds none on the prefix catch-alls, so it
// must fail on the empty value, which Deny answers; returning "" with no error fails the grammar
// backstop, a 500. It panics at the call site when r was not built
// by a Server, r is already inside a family, p.Tenant is nil, the prefix is empty or "/", the
// full prefix holds a path parameter or wildcard, multi-tenant mode is off, the full prefix
// equals or covers the health or ready path, or it equals, covers or sits under another family.
func tenantOwnedGroup(r RouteRegistrar, prefix string, p TenantPolicy) RouteRegistrar {
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
	if probe, covered := rg.admission.coveredProbePath(full); covered {
		panic(tenantFamilyRefusal(full, "it equals or covers the probe path "+strconv.Quote(probe)))
	}
	if other, claimed := rg.admission.claimFamily(full); !claimed {
		panic(tenantFamilyRefusal(full, "it overlaps the tenant-owned family at "+strconv.Quote(other)))
	}
	// Copied before p is captured, so a later edit to the caller's slice cannot reach the chain.
	p.Authenticate = slices.Clone(p.Authenticate)
	fam := &tenantFamily{
		prefix:    full,
		steps:     adaptMiddleware(p.middleware(rg.admission.log), rg.cfg),
		catchAlls: map[string]struct{}{catchAllKey(full + "/*"): {}},
	}
	group := rg.admission.engine.Group(full)
	// Registered on the engine outside admission, which refuses RouteAny and RouteNotFound
	// (ADR-155). The any slot takes every method no family route takes at the bare prefix, so
	// echo never backtracks from it to a route outside the family.
	group.Any("", familyNotFound, fam.steps)
	group.RouteNotFound("/*", familyNotFound, fam.steps)
	return &routeGroup{
		group:     group,
		prefix:    full,
		cfg:       rg.cfg,
		admission: rg.admission,
		family:    fam,
	}
}

// middleware runs p as a flat MiddlewareFunc, logging its refusals to l.
func (p TenantPolicy) middleware(l logger.Logger) MiddlewareFunc {
	return func(c HandlerContext, next func() error) error {
		_, _, err := p.run(c, l, next)
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

// tenantFamilyRefusal renders tenantOwnedGroup's call-site panic for prefix.
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
