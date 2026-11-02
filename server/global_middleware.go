package server

import (
	"errors"
	"fmt"
	"strings"

	"github.com/labstack/echo/v5"
)

// ErrTenantFamilyWithGlobalMiddleware is what Start, and app startup, return for a server that
// installs global middleware beside a tenant-owned route family: global middleware runs before
// a family resolves its tenant, so the two never ship together (ADR-164). Match it with errors.Is.
var ErrTenantFamilyWithGlobalMiddleware = errors.New("tenant-owned route family with global middleware")

const tenantFamilyGlobalMiddlewareHint = "global middleware runs before a family resolves its tenant, so it would see no tenant on the family's routes — " +
	"attach each gate per group instead: in the family's Authenticate (before the tenant) or Use (after it), and on every other group that needs it"

// RegisterGlobalMiddleware appends application middleware to the root engine chain. Each
// runs once per request after every built-in middleware (tenant resolution, rate limiting,
// recovery, ...) and before the route handler, and skips the health/ready probes. It must
// be called during startup, before Start(). Start refuses a server that installs one beside
// a tenant-owned route family (ErrTenantFamilyWithGlobalMiddleware, ADR-164), and after Start
// a call beside a family panics, since nothing checks again.
func (s *Server) RegisterGlobalMiddleware(mw ...MiddlewareFunc) {
	skipper := newProbeSkipper(s.buildFullPath(s.healthRoute), s.buildFullPath(s.readyRoute))
	adapted := make([]echo.MiddlewareFunc, 0, len(mw))
	for _, m := range mw {
		if m == nil {
			continue
		}
		adapted = append(adapted, adaptMiddleware(skipProbes(m, skipper), s.cfg))
	}
	if len(adapted) == 0 {
		return
	}
	if families := s.admission.familyPrefixes(); s.started.Load() && len(families) > 0 {
		panic("server: global middleware registered after Start beside a tenant-owned route family (" +
			strings.Join(families, ", ") + "); " + tenantFamilyGlobalMiddlewareHint)
	}
	s.echo.Use(adapted...)
	s.globalMiddleware += len(adapted)
}

// GlobalMiddlewareCount returns how many global middleware RegisterGlobalMiddleware installed;
// nil entries are never installed, so they never count. Startup-only, like that method.
func (s *Server) GlobalMiddlewareCount() int {
	return s.globalMiddleware
}

// TenantFamilyPrefixes returns each tenant-owned route family's full prefix, in build order,
// or nil when this server has none. The list is append-only: a built family is never removed
// or reordered, so index i names the same family in every later call (app attribution relies
// on it).
func (s *Server) TenantFamilyPrefixes() []string {
	return s.admission.familyPrefixes()
}

// tenantFamilyGlobalMiddlewareError is Start's backstop: nil unless a tenant-owned route family
// and at least one installed global middleware share this server.
func (s *Server) tenantFamilyGlobalMiddlewareError() error {
	families := s.TenantFamilyPrefixes()
	if len(families) == 0 || s.globalMiddleware == 0 {
		return nil
	}
	return fmt.Errorf("%w: %d family(ies) (%s); %d global middleware installed; %s",
		ErrTenantFamilyWithGlobalMiddleware, len(families), strings.Join(families, ", "),
		s.globalMiddleware, tenantFamilyGlobalMiddlewareHint)
}

// skipProbes exempts the probe routes from one global middleware. The decision reads the
// matched route template (isProbeRequest), not the request URL: a global middleware
// registered earlier in the chain can rewrite r.URL.Path, and this seat is exactly where
// consumer code sits.
func skipProbes(mw MiddlewareFunc, skipper probeSkipper) MiddlewareFunc {
	return func(c HandlerContext, next func() error) error {
		if skipper(c.ectx) {
			return next()
		}
		return mw(c, next)
	}
}
