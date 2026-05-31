// Package shop manages the storefront. Handlers live on a separate *Handler
// struct (the documented Enhanced Handler Pattern) registered as m.h.method.
package shop

import (
	"github.com/gaborage/go-bricks/app"
	"github.com/gaborage/go-bricks/server"
)

// Module wires the shop's routes to a Handler.
type Module struct {
	h *Handler
}

func (m *Module) Name() string                    { return "shop" }
func (m *Module) Init(deps *app.ModuleDeps) error { m.h = &Handler{}; return nil }
func (m *Module) Shutdown() error                 { return nil }

// RegisterRoutes registers routes whose handlers are methods on m.h (*Handler).
func (m *Module) RegisterRoutes(hr *server.HandlerRegistry, r server.RouteRegistrar) {
	server.POST(hr, r, "/users", m.h.createUser, server.WithTags("users"), server.WithSummary("Create a user"))
	server.GET(hr, r, "/users/:id", m.h.getUser, server.WithTags("users"))
	server.DELETE(hr, r, "/users/:id", m.h.deleteUser, server.WithTags("users"))
}
