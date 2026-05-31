// Package legacy demonstrates raw-response mode (Strangler-Fig migration) and the
// WithHandlerName operationId override. getLegacyUser bypasses the data/meta
// envelope; enqueueJob returns 202 Accepted with an overridden operationId.
package legacy

import (
	"github.com/gaborage/go-bricks/app"
	"github.com/gaborage/go-bricks/server"
)

// Module wires the legacy routes to a Handler.
type Module struct {
	h *Handler
}

func (m *Module) Name() string                    { return "legacy" }
func (m *Module) Init(deps *app.ModuleDeps) error { m.h = &Handler{}; return nil }
func (m *Module) Shutdown() error                 { return nil }

// RegisterRoutes registers a raw-response GET and an async POST.
func (m *Module) RegisterRoutes(hr *server.HandlerRegistry, r server.RouteRegistrar) {
	server.GET(hr, r, "/legacy/users/:id", m.h.getLegacyUser,
		server.WithTags("legacy"), server.WithRawResponse())
	server.POST(hr, r, "/jobs", m.h.enqueueJob,
		server.WithTags("jobs"), server.WithHandlerName("enqueueLegacyJob"))
}
