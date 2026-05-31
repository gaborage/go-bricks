// Package events demonstrates well-known type formats, map additionalProperties,
// and uint64 minimum fidelity in the generated schema.
package events

import (
	"github.com/gaborage/go-bricks/app"
	"github.com/gaborage/go-bricks/server"
)

// Module wires the events route to a Handler.
type Module struct {
	h *Handler
}

func (m *Module) Name() string                    { return "events" }
func (m *Module) Init(deps *app.ModuleDeps) error { m.h = &Handler{}; return nil }
func (m *Module) Shutdown() error                 { return nil }

func (m *Module) RegisterRoutes(hr *server.HandlerRegistry, r server.RouteRegistrar) {
	server.GET(hr, r, "/events/:id", m.h.getEvent, server.WithTags("events"))
}
