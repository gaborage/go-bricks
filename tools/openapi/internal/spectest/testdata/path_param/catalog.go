// Package catalog serves product lookups.
package catalog

import (
	"net/http"

	"github.com/gaborage/go-bricks/app"
	"github.com/gaborage/go-bricks/server"
)

// Module is the catalog module.
type Module struct{}

func (m *Module) Name() string                    { return "catalog" }
func (m *Module) Init(deps *app.ModuleDeps) error { return nil }
func (m *Module) Shutdown() error                 { return nil }

// GetProductReq identifies a product plus optional pagination. Its fields are
// path/query parameters, exercising Echo ":id" -> OpenAPI "{id}" templating.
type GetProductReq struct {
	ID   int64 `param:"id" validate:"required"`
	Page int   `query:"page"`
}

// Product is a catalog product.
type Product struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

// RegisterRoutes registers the module's HTTP routes.
func (m *Module) RegisterRoutes(hr *server.HandlerRegistry, r server.RouteRegistrar) {
	server.GET(hr, r, "/products/:id", m.getProduct, server.WithTags("catalog"))
}

func (m *Module) getProduct(req GetProductReq, ctx server.HandlerContext) (server.Result[Product], server.IAPIError) {
	return server.NewResult(http.StatusOK, Product{}), nil
}
