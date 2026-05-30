// Package health exposes a liveness endpoint via a package-level handler func.
package health

import (
	"net/http"

	"github.com/gaborage/go-bricks/app"
	"github.com/gaborage/go-bricks/server"
)

// Module is the health module.
type Module struct{}

func (m *Module) Name() string                    { return "health" }
func (m *Module) Init(deps *app.ModuleDeps) error { return nil }
func (m *Module) Shutdown() error                 { return nil }

// PingResponse is the ping payload.
type PingResponse struct {
	Status string `json:"status"`
}

// RegisterRoutes registers a route whose handler is a bare package-level func.
func (m *Module) RegisterRoutes(hr *server.HandlerRegistry, r server.RouteRegistrar) {
	server.GET(hr, r, "/ping", ping, server.WithTags("health"))
}

func ping(ctx server.HandlerContext) (server.Result[PingResponse], server.IAPIError) {
	return server.NewResult(http.StatusOK, PingResponse{}), nil
}
