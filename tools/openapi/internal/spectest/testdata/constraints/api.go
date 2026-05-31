// Package shop exercises PR11 validator-constraint coverage end-to-end.
package shop

import (
	"github.com/gaborage/go-bricks/app"
	"github.com/gaborage/go-bricks/server"
)

// Cents is a named integer scalar; its underlying kind (integer) drives numeric
// constraints (min/max) instead of the object fallback.
type Cents int64

// CreateReq carries one field per PR11 constraint family.
type CreateReq struct {
	Amount Cents    `json:"amount" validate:"min=1,max=10000"`         // named numeric -> minimum/maximum
	Name   string   `json:"name" validate:"gt=2,lt=50"`                  // string gt/lt -> minLength/maxLength
	Tags   []string `json:"tags" validate:"min=1,max=5,dive,email"`      // slice cardinality + dive element format
	Server string   `json:"server" validate:"ipv4"`                      // content format
	Slug   string   `json:"slug" validate:"startswith=svc-"`             // value pattern
	Region string   `json:"region" validate:"oneof='North East' 'South West'"` // quoted enum
}

// Ack is the create acknowledgement.
type Ack struct {
	ID int64 `json:"id"`
}

// Module wires the route.
type Module struct{}

func (m *Module) Name() string                    { return "shop" }
func (m *Module) Init(deps *app.ModuleDeps) error { return nil }
func (m *Module) Shutdown() error                 { return nil }

func (m *Module) create(req CreateReq, ctx server.HandlerContext) (server.Result[Ack], server.IAPIError) {
	return server.Created(Ack{}), nil
}

func (m *Module) RegisterRoutes(hr *server.HandlerRegistry, r server.RouteRegistrar) {
	server.POST(hr, r, "/items", m.create, server.WithTags("items"))
}
