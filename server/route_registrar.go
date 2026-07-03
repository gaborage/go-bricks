package server

import (
	"github.com/labstack/echo/v5"

	"github.com/gaborage/go-bricks/config"
	"github.com/gaborage/go-bricks/internal/pathutil"
)

type routeGroup struct {
	group  *echo.Group
	prefix string
	cfg    *config.Config // populates HandlerContext.Config in the go-bricks↔echo adapters
}

func newRouteGroup(group *echo.Group, prefix string, cfg *config.Config) RouteRegistrar {
	return &routeGroup{
		group:  group,
		prefix: pathutil.NormalizePrefix(prefix),
		cfg:    cfg,
	}
}

// addEcho implements the unexported echoAdder seam: it registers a pre-built
// echo.HandlerFunc directly, so the framework's typed-handler hot path pays no
// per-request adapter cost (ADR-026).
func (rg *routeGroup) addEcho(method, path string, h echo.HandlerFunc) {
	rg.group.Add(method, rg.relativePath(path), h)
}

// Add registers an echo-free Handler with optional flat middleware. The go-bricks→echo
// adapters are built once here at registration time; the route handle (echo.RouteInfo)
// is intentionally discarded.
//
// It also records a RouteDescriptor in DefaultRouteRegistry so raw routes are discoverable
// alongside typed ones (issue #634). The framework's routeGroup implements the addEcho seam, so
// typed handlers register through addEcho (which emits its own descriptor and never traverses
// Add) — framework-registered routes are never double-counted. Only the fields derivable at this
// seam are populated (method, full path, handler ID/name, caller package); type- and JOSE-related
// fields stay zero-valued because raw handlers carry no request/response models.
func (rg *routeGroup) Add(method, path string, handler Handler, middleware ...MiddlewareFunc) {
	relative := rg.relativePath(path)
	rg.group.Add(method, relative, adaptHandler(handler, rg.cfg), rg.adaptAll(middleware)...)

	fullPath := rg.fullPathFromRelative(relative)
	DefaultRouteRegistry.Register(&RouteDescriptor{
		Method:      method,
		Path:        fullPath,
		HandlerID:   formatHandlerID(method, fullPath),
		HandlerName: extractHandlerName(handler),
		Package:     getCallerPackage(2), // getCallerPackage → Add → module (best-effort, as typed routes)
	})
}

func (rg *routeGroup) Group(prefix string, middleware ...MiddlewareFunc) RouteRegistrar {
	normalized := pathutil.NormalizePrefix(prefix)
	newGroup := rg.group.Group(normalized, rg.adaptAll(middleware)...)

	return &routeGroup{
		group:  newGroup,
		prefix: rg.combinePrefix(normalized),
		cfg:    rg.cfg,
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
