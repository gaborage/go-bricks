package api

import (
	"github.com/example/coll/orders"
	"github.com/example/coll/users"
	"github.com/gaborage/go-bricks/server"
)

// Handler holds the API handlers.
type Handler struct{}

func (h *Handler) createUser(req users.Request, ctx server.HandlerContext) (server.NoContentResult, server.IAPIError) {
	return server.NoContent(), nil
}
func (h *Handler) createOrder(req orders.Request, ctx server.HandlerContext) (server.NoContentResult, server.IAPIError) {
	return server.NoContent(), nil
}
