package server

import "github.com/labstack/echo/v5"

// RegisterGlobalMiddleware appends application middleware to the root engine chain. Each
// runs once per request after every built-in middleware (tenant resolution, rate limiting,
// recovery, ...) and before the route handler, and skips the health/ready probes. It must
// be called during startup, before Start().
func (s *Server) RegisterGlobalMiddleware(mw ...MiddlewareFunc) {
	healthPath := s.buildFullPath(s.healthRoute)
	readyPath := s.buildFullPath(s.readyRoute)
	adapted := make([]echo.MiddlewareFunc, 0, len(mw))
	for _, m := range mw {
		if m == nil {
			continue
		}
		adapted = append(adapted, adaptMiddleware(skipProbes(m, healthPath, readyPath), s.cfg))
	}
	if len(adapted) == 0 {
		return
	}
	s.echo.Use(adapted...)
}

func skipProbes(mw MiddlewareFunc, healthPath, readyPath string) MiddlewareFunc {
	skipper := CreateProbeSkipper(healthPath, readyPath)
	return func(c HandlerContext, next func() error) error {
		if skipper(c.Request()) {
			return next()
		}
		return mw(c, next)
	}
}
