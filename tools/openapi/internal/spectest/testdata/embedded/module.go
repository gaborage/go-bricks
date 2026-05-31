// Package embed demonstrates embedded/anonymous struct field promotion and
// json-tagged named embedding (nesting).
package embed

import (
	"github.com/gaborage/go-bricks/app"
	"github.com/gaborage/go-bricks/server"
)

// Module wires the routes to a Handler.
type Module struct {
	h *Handler
}

func (m *Module) Name() string                    { return "embed" }
func (m *Module) Init(deps *app.ModuleDeps) error { m.h = &Handler{}; return nil }
func (m *Module) Shutdown() error                 { return nil }

func (m *Module) RegisterRoutes(hr *server.HandlerRegistry, r server.RouteRegistrar) {
	server.GET(hr, r, "/users/:id", m.h.getUser, server.WithTags("embed"))
	server.GET(hr, r, "/accounts/:id", m.h.getAccount, server.WithTags("embed"))
	server.GET(hr, r, "/wrappers/:id", m.h.getWrapper, server.WithTags("embed"))
}
