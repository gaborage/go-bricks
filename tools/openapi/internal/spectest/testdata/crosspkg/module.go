// Package shop demonstrates cross-package (in-module sibling) type resolution.
package shop

import (
	"github.com/gaborage/go-bricks/app"
	"github.com/gaborage/go-bricks/server"
)

// Module wires the routes to a Handler.
type Module struct {
	h *Handler
}

func (m *Module) Name() string                    { return "shop" }
func (m *Module) Init(deps *app.ModuleDeps) error { m.h = &Handler{}; return nil }
func (m *Module) Shutdown() error                 { return nil }

func (m *Module) RegisterRoutes(hr *server.HandlerRegistry, r server.RouteRegistrar) {
	server.GET(hr, r, "/orders/:id", m.h.getOrder, server.WithTags("shop"))
	server.GET(hr, r, "/prices/:id", m.h.getPrice, server.WithTags("shop"))
	server.GET(hr, r, "/receipts/:id", m.h.getReceipt, server.WithTags("shop"))
}
