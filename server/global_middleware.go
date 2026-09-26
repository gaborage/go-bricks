package server

import "github.com/labstack/echo/v5"

// RegisterGlobalMiddleware appends application middleware to the root engine chain. Each
// runs once per request after every built-in middleware (tenant resolution, rate limiting,
// recovery, ...) and before the route handler, and skips the health/ready probes. It must
// be called during startup, before Start().
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
	s.echo.Use(adapted...)
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
