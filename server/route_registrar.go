package server

import (
	"github.com/labstack/echo/v5"

	"github.com/gaborage/go-bricks/config"
	"github.com/gaborage/go-bricks/internal/pathutil"
)

type routeGroup struct {
	group     *echo.Group
	prefix    string
	cfg       *config.Config // populates HandlerContext.Config in the go-bricks↔echo adapters
	admission *routeAdmission
}

func newRouteGroup(group *echo.Group, prefix string, cfg *config.Config) *routeGroup {
	return &routeGroup{
		group:  group,
		prefix: pathutil.NormalizePrefix(prefix),
		cfg:    cfg,
	}
}

// newAdmittingRouteGroup is newRouteGroup plus the Server's route admission: Server's group
// factories use it so every routeGroup they hand out (including nested Group() children)
// admits through the same per-Server routeAdmission.
func newAdmittingRouteGroup(group *echo.Group, prefix string, cfg *config.Config, admission *routeAdmission) RouteRegistrar {
	rg := newRouteGroup(group, prefix, cfg)
	rg.admission = admission
	return rg
}

// addEcho registers a pre-built echo.HandlerFunc directly, so the framework's
// typed-handler hot path pays no per-request adapter cost (ADR-026). The descriptor is
// written only if the route is admitted; see routeAdmission.admit.
func (rg *routeGroup) addEcho(method, path string, h echo.HandlerFunc, descriptor *RouteDescriptor) {
	relative := rg.relativePath(path)
	rg.admission.admit(admittedRoute{
		Method:     method,
		Path:       rg.fullPathFromRelative(relative),
		Registrant: RouteRegistrant{HandlerName: descriptor.HandlerName, Package: descriptor.Package},
		Descriptor: descriptor,
	}, func() { rg.group.Add(method, relative, h) })
}

// Add registers an echo-free Handler with optional flat middleware. The go-bricks→echo
// adapters are built once here at registration time; the route handle (echo.RouteInfo)
// is intentionally discarded.
//
// It also records a RouteDescriptor in DefaultRouteRegistry so raw routes are discoverable
// alongside typed ones (issue #634). Typed handlers on the framework's routeGroup register
// through routeGroup.addEcho, which never traverses Add, so no route is described twice.
// Only the fields derivable at this seam are populated (method, full path, handler ID/name,
// caller package); type- and JOSE-related fields stay zero-valued because raw handlers carry
// no request/response models. A duplicate is neither described nor added; see
// routeAdmission.admit.
func (rg *routeGroup) Add(method, path string, handler Handler, middleware ...MiddlewareFunc) {
	relative := rg.relativePath(path)
	fullPath := rg.fullPathFromRelative(relative)
	pkg := getCallerPackage(2) // getCallerPackage → Add → module (best-effort, as typed routes)
	reg := RouteRegistrant{HandlerName: extractHandlerName(handler), Package: pkg}
	rg.admission.admit(admittedRoute{
		Method:     method,
		Path:       fullPath,
		Registrant: reg,
		Descriptor: modellessDescriptor(method, fullPath, reg),
	}, func() { rg.group.Add(method, relative, adaptHandler(handler, rg.cfg), rg.adaptAll(middleware)...) })
}

// probeListenerDescriptor describes a probe served on the probe listener, at its unprefixed
// path. The application listener's reservation of <base><path> is the admission key and
// carries no descriptor. The HandlerID carries the "probes:" prefix (probes:GET:/ready), so
// it stays unique across listeners and never equals an admission key.
func probeListenerDescriptor(method, path string, reg RouteRegistrant) *RouteDescriptor {
	d := modellessDescriptor(method, path, reg)
	d.Listener = ListenerProbes
	d.HandlerID = formatListenerHandlerID(ListenerProbes, method, path)
	return d
}

func modellessDescriptor(method, path string, reg RouteRegistrant) *RouteDescriptor {
	return &RouteDescriptor{
		Method:      method,
		Path:        path,
		HandlerID:   formatHandlerID(method, path),
		HandlerName: reg.HandlerName,
		Package:     reg.Package,
	}
}

func (rg *routeGroup) Group(prefix string, middleware ...MiddlewareFunc) RouteRegistrar {
	normalized := pathutil.NormalizePrefix(prefix)
	newGroup := rg.group.Group(normalized, rg.adaptAll(middleware)...)

	return &routeGroup{
		group:     newGroup,
		prefix:    rg.combinePrefix(normalized),
		cfg:       rg.cfg,
		admission: rg.admission,
	}
}

func (rg *routeGroup) Use(middleware ...MiddlewareFunc) {
	rg.group.Use(rg.adaptAll(middleware)...)
}

// adaptAll converts a slice of flat MiddlewareFunc to echo middleware using the group's config.
func (rg *routeGroup) adaptAll(mw []MiddlewareFunc) []echo.MiddlewareFunc {
	if len(mw) == 0 {
		return nil
	}
	out := make([]echo.MiddlewareFunc, len(mw))
	for i, m := range mw {
		out[i] = adaptMiddleware(m, rg.cfg)
	}
	return out
}

func (rg *routeGroup) FullPath(path string) string {
	return rg.fullPathFromRelative(rg.relativePath(path))
}

// fullPathFromRelative joins the group prefix with an already-resolved relative path. Add uses it
// to avoid recomputing relativePath (which FullPath would otherwise do a second time per route).
func (rg *routeGroup) fullPathFromRelative(relative string) string {
	if relative == "" {
		if rg.prefix == "" {
			return "/"
		}
		return rg.prefix
	}

	if rg.prefix == "" {
		return relative
	}

	return rg.prefix + relative
}

func (rg *routeGroup) relativePath(path string) string {
	normalized := pathutil.EnsureLeadingSlash(path)

	if normalized == "/" {
		return ""
	}

	if trimmed, ok := pathutil.StripPathPrefix(normalized, rg.prefix); ok {
		if trimmed == "" {
			return ""
		}
		return trimmed
	}

	return normalized
}

func (rg *routeGroup) combinePrefix(suffix string) string {
	if suffix == "" {
		return rg.prefix
	}

	if rg.prefix == "" {
		return suffix
	}

	return rg.prefix + suffix
}
