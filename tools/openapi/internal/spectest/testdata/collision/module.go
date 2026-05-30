// Package api demonstrates component-name collision qualification: two distinct
// in-module packages each define a Request type.
package api

import (
	"github.com/gaborage/go-bricks/app"
	"github.com/gaborage/go-bricks/server"
)

// Module wires the routes to a Handler.
type Module struct {
	h *Handler
}

func (m *Module) Name() string                    { return "api" }
func (m *Module) Init(deps *app.ModuleDeps) error { m.h = &Handler{}; return nil }
func (m *Module) Shutdown() error                 { return nil }

func (m *Module) RegisterRoutes(hr *server.HandlerRegistry, r server.RouteRegistrar) {
	server.POST(hr, r, "/users", m.h.createUser, server.WithTags("users"))
	server.POST(hr, r, "/orders", m.h.createOrder, server.WithTags("orders"))
}
