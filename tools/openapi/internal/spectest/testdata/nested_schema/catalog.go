// Package catalog exercises nested, sliced, and recursive schema types so the
// generator emits $ref / items.$ref and a component per reachable struct.
package catalog

import (
	"github.com/gaborage/go-bricks/app"
	"github.com/gaborage/go-bricks/server"
)

// Module is the catalog module.
type Module struct{}

func (m *Module) Name() string                 { return "catalog" }
func (m *Module) Init(d *app.ModuleDeps) error { return nil }
func (m *Module) Shutdown() error              { return nil }

// Address is a nested value struct.
type Address struct {
	Street string `json:"street" validate:"required"`
	City   string `json:"city"`
}

// CreateUserReq nests a struct and a slice of structs.
type CreateUserReq struct {
	Name      string    `json:"name" validate:"required,min=2"`
	Address   Address   `json:"address"`   // nested struct -> $ref
	Addresses []Address `json:"addresses"` // slice of struct -> items.$ref
}

// User self-references via a pointer and a slice of pointers.
type User struct {
	ID      int64   `json:"id"`
	Profile Address `json:"profile"` // nested struct -> $ref
	Manager *User   `json:"manager"` // pointer self-ref -> $ref
	Reports []*User `json:"reports"` // slice of pointers -> items.$ref
}

// Category is recursive through both a pointer and a slice.
type Category struct {
	Name     string     `json:"name"`
	Parent   *Category  `json:"parent"`   // self-ref pointer -> $ref
	Children []Category `json:"children"` // self-ref slice -> items.$ref
}

// RegisterRoutes registers the module's HTTP routes.
func (m *Module) RegisterRoutes(hr *server.HandlerRegistry, r server.RouteRegistrar) {
	server.POST(hr, r, "/users", m.createUser, server.WithTags("users"))
	server.GET(hr, r, "/categories", m.listCategories, server.WithTags("categories"))
}

func (m *Module) createUser(req CreateUserReq, ctx server.HandlerContext) (server.Result[User], server.IAPIError) {
	return server.Created(User{}), nil
}

func (m *Module) listCategories(ctx server.HandlerContext) (server.Result[Category], server.IAPIError) {
	return server.OK(Category{}), nil
}
