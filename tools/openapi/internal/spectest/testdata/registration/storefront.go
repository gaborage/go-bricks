// Package storefront registers routes via groups, helper methods, control-flow
// blocks, and concatenated paths — exercising the analyzer's registration walk.
package storefront

import (
	"github.com/gaborage/go-bricks/app"
	"github.com/gaborage/go-bricks/server"
)

const apiBase = "/api"

// Module is the storefront module.
type Module struct{}

func (m *Module) Name() string                    { return "storefront" }
func (m *Module) Init(d *app.ModuleDeps) error    { return nil }
func (m *Module) Shutdown() error                 { return nil }

// Item is the storefront item resource.
type Item struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

// RegisterRoutes registers routes through several non-trivial patterns.
func (m *Module) RegisterRoutes(hr *server.HandlerRegistry, r server.RouteRegistrar) {
	// Grouped routes: /v1/items
	v1 := r.Group("/v1")
	server.GET(hr, v1, "/items", m.listItems, server.WithTags("items"))

	// Nested group: /v1/admin/stats
	admin := v1.Group("/admin")
	server.GET(hr, admin, "/stats", m.stats, server.WithTags("admin"))

	// Route registered inside a conditional block.
	if true {
		server.GET(hr, r, "/health", m.health, server.WithTags("ops"))
	}

	// Concatenated path from a constant: /api/version
	server.GET(hr, r, apiBase+"/version", m.version, server.WithTags("ops"))

	// Routes registered by a helper method.
	m.registerItemWrites(hr, r)
}

// registerItemWrites is a helper the walker must recurse into.
func (m *Module) registerItemWrites(hr *server.HandlerRegistry, r server.RouteRegistrar) {
	server.POST(hr, r, "/items", m.createItem, server.WithTags("items"))
}

func (m *Module) listItems(ctx server.HandlerContext) (server.Result[Item], server.IAPIError) { return server.OK(Item{}), nil }
func (m *Module) stats(ctx server.HandlerContext) (server.Result[Item], server.IAPIError)     { return server.OK(Item{}), nil }
func (m *Module) health(ctx server.HandlerContext) (server.Result[Item], server.IAPIError)    { return server.OK(Item{}), nil }
func (m *Module) version(ctx server.HandlerContext) (server.Result[Item], server.IAPIError)   { return server.OK(Item{}), nil }
func (m *Module) createItem(req Item, ctx server.HandlerContext) (server.Result[Item], server.IAPIError) {
	return server.Created(Item{}), nil
}
