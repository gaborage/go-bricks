package server

import (
	"strconv"

	"github.com/gaborage/go-bricks/internal/pathutil"
)

// tenantFamily is one tenant-owned route family.
type tenantFamily struct {
	prefix string // the full template the family's echo group mounts at
}

// tenantOwnedGroup mounts a tenant-owned route family at r's prefix joined with prefix. The
// family's echo group is built on the application engine at that full prefix, not under r, so
// no parent group's middleware ever runs on it. It panics at the call site when r was not built
// by a Server, r is already inside a family, p.Tenant is nil, the prefix is empty or "/", the
// full prefix holds a path parameter or wildcard, multi-tenant mode is off, the full prefix
// equals or covers the health or ready path, or it equals, covers or sits under another family.
func tenantOwnedGroup(r RouteRegistrar, prefix string, p TenantPolicy) RouteRegistrar {
	rg, ok := r.(*routeGroup)
	if !ok || rg.admission == nil || rg.admission.engine == nil {
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
	if rg.cfg == nil || !rg.cfg.Multitenant.Enabled {
		panic(tenantFamilyRefusal(full, "multitenant.enabled is false; a module shared with single-tenant binaries must branch on config"))
	}
	if probe, covered := rg.admission.coveredProbePath(full); covered {
		panic(tenantFamilyRefusal(full, "it equals or covers the probe path "+strconv.Quote(probe)))
	}
	if other, claimed := rg.admission.claimFamily(full); !claimed {
		panic(tenantFamilyRefusal(full, "it overlaps the tenant-owned family at "+strconv.Quote(other)))
	}
	return &routeGroup{
		group:     rg.admission.engine.Group(full),
		prefix:    full,
		cfg:       rg.cfg,
		admission: rg.admission,
		family:    &tenantFamily{prefix: full},
	}
}

// tenantFamilyRefusal renders tenantOwnedGroup's call-site panic for prefix.
func tenantFamilyRefusal(prefix, reason string) string {
	return "server: tenant-owned group registration failed for " + strconv.Quote(prefix) + ": " + reason
}

// isStaticTemplate reports whether path holds no path parameter or wildcard in echo's grammar.
func isStaticTemplate(path string) bool {
	static := true
	walkRouteTemplate(newRouteTemplate(path), func(kind routePartKind, _ string) {
		if kind != routePartStatic {
			static = false
		}
	})
	return static
}
