// Package api exposes user and health endpoints.
package api

import (
	"github.com/gaborage/go-bricks/app"
	"github.com/gaborage/go-bricks/server"
)

// Module is the API module.
type Module struct{}

func (m *Module) Name() string                    { return "api" }
func (m *Module) Init(deps *app.ModuleDeps) error { return nil }
func (m *Module) Shutdown() error                 { return nil }

// CreateUserReq is the request body for creating a user.
type CreateUserReq struct {
	Name  string `json:"name" validate:"required,min=2,max=50"`
	Email string `json:"email" validate:"required,email"`
	Role  string `json:"role" validate:"oneof=admin member guest"`
}

// User is the user resource.
type User struct {
	ID    int64  `json:"id"`
	Name  string `json:"name"`
	Email string `json:"email"`
}

// RegisterRoutes registers the module's HTTP routes.
func (m *Module) RegisterRoutes(hr *server.HandlerRegistry, r server.RouteRegistrar) {
	server.POST(hr, r, "/users", m.createUser, server.WithTags("users"), server.WithSummary("Create a user"))
	server.GET(hr, r, "/ping", m.ping, server.WithTags("health"))
}

func (m *Module) createUser(req CreateUserReq, ctx server.HandlerContext) (server.Result[User], server.IAPIError) {
	return server.Created(User{}), nil
}

func (m *Module) ping(ctx server.HandlerContext) (server.Result[User], server.IAPIError) {
	return server.OK(User{}), nil
}
