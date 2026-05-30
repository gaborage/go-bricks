// Package vts integrates with Visa Token Services using JOSE-protected payloads.
package vts

import (
	"github.com/gaborage/go-bricks/app"
	"github.com/gaborage/go-bricks/server"
)

// Module is the VTS module.
type Module struct{}

func (m *Module) Name() string                    { return "vts" }
func (m *Module) Init(deps *app.ModuleDeps) error { return nil }
func (m *Module) Shutdown() error                 { return nil }

// CreateTokenRequest is decrypted-then-verified on inbound. The sentinel field
// carries the jose tag that marks the route as JOSE-protected; the analyzer
// detects it and the generator emits Content-Type application/jose for the body.
type CreateTokenRequest struct {
	_   struct{} `jose:"decrypt=our-signing,verify=visa-vts-verify"`
	PAN string   `json:"pan" validate:"required"`
}

// CreateTokenResponse is signed-then-encrypted on outbound.
type CreateTokenResponse struct {
	_     struct{} `jose:"sign=our-signing,encrypt=visa-vts-encrypt"`
	Token string   `json:"token"`
}

// RegisterRoutes registers the module's HTTP routes.
func (m *Module) RegisterRoutes(hr *server.HandlerRegistry, r server.RouteRegistrar) {
	server.POST(hr, r, "/v1/tokens", m.createToken, server.WithTags("vts"))
}

// createToken's response is wrapped in server.Result[T]. The analyzer does not
// yet unwrap that generic (it parses as an *ast.IndexExpr — handled in a later
// PR), so route.Response is nil today and the generated 200 shows the standard
// application/json SuccessResponse envelope rather than application/jose. The
// request side IS detected, so the requestBody is application/jose and the 4xx
// references JOSEErrorEnvelope. When Result[T] unwrapping lands, the golden's 200
// will flip to application/jose and this fixture will exercise the full
// JOSE-response round-trip end-to-end.
func (m *Module) createToken(req CreateTokenRequest, ctx server.HandlerContext) (server.Result[CreateTokenResponse], server.IAPIError) {
	return server.OK(CreateTokenResponse{}), nil
}
