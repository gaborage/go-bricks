package server

import (
	"fmt"

	"github.com/labstack/echo/v5"

	"github.com/gaborage/go-bricks/config"
	"github.com/gaborage/go-bricks/internal/pathutil"
	"github.com/gaborage/go-bricks/multitenant"
)

// frameworkRootPackages are the packages whose routes may sit under a framework route root:
// the scheduler's system API and the App's debug group. A route's package is its handler
// function's (handlerPackage), stamped after every RouteOption, so no option moves a route into
// or out of this set, and no door moves a consumer route into it. The server's own package is absent
// on purpose: the typed-handler fallback for a registrar that is not the framework's adapts the
// handler in a server closure, so admitting server would admit that route. The probes, which
// the server registers itself, are exempt by path instead (refuseFrameworkRootLocked).
var frameworkRootPackages = []string{
	"github.com/gaborage/go-bricks/scheduler",
	"github.com/gaborage/go-bricks/app",
}

// frameworkRoots returns the full route prefixes admission reserves for framework routes under
// a resolver that may read the request path (multitenant.MayReadPath): the scheduler root
// <base>/_sys and, with debug.enabled, debug.pathprefix at the engine root (ADR-168). It is nil
// when multitenant is off or the resolver reads no path. A debug prefix at the engine root
// ("" or "/") or at or above server.path.base would cover every module route, so it is an
// error naming both keys, and the roots then hold the scheduler's alone.
func frameworkRoots(cfg *config.Config, resolver multitenant.TenantResolver) ([]string, error) {
	if !cfg.Multitenant.Enabled || !multitenant.MayReadPath(resolver) {
		return nil, nil
	}
	base := pathutil.NormalizePrefix(cfg.Server.Path.Base) // "" for the engine root
	roots := make([]string, 0, 2)                          // the scheduler root, and the debug prefix when enabled
	roots = append(roots, joinBasePath(base, pathutil.SystemPrefix))
	if !cfg.Debug.Enabled {
		return roots, nil
	}
	debug := pathutil.NormalizePrefix(cfg.Debug.PathPrefix)
	if debug == "" || (base != "" && underPrefix(base, debug)) {
		return roots, fmt.Errorf("server: debug.pathprefix %q is at or above server.path.base %q: under a tenant "+
			"resolver that reads the path the debug prefix is a framework route root, so it would reserve every "+
			"module route; set debug.pathprefix to a non-root path below the engine root that neither equals nor covers "+
			"a non-empty server.path.base (ADR-168)", cfg.Debug.PathPrefix, cfg.Server.Path.Base)
	}
	return append(roots, debug), nil
}

// skipFrameworkRoots extends the tenant seat's probe skip to every matched template under
// roots. Admission refuses any other package's route or middleware-bearing group under a
// root, so a template there names a framework handler, or a not-found catch-all that serves
// none. An empty template (nothing matched) is under no root. With no roots it returns skip
// unchanged; the per-request check allocates nothing.
func skipFrameworkRoots(skip probeSkipper, roots []string) probeSkipper {
	if len(roots) == 0 {
		return skip
	}
	return func(c *echo.Context) bool {
		if skip(c) {
			return true
		}
		_, covered := coveringPrefix(c.Path(), roots)
		return covered
	}
}
