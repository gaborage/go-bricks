package shop

import (
	"net/http"

	"github.com/example/cross/types"
	"github.com/gaborage/go-bricks/server"
)

// Handler holds the shop's HTTP handlers.
type Handler struct{}

// Cents is a local named scalar; its underlying kind (integer) is emitted instead
// of the object fallback.
type Cents int64

// Order has a field of an in-module sibling-package type (types.Money) -> $ref,
// plus a named-scalar field (Discount) emitted as its underlying integer kind.
type Order struct {
	ID       int64       `json:"id"`
	Total    types.Money `json:"total"`
	Discount Cents       `json:"discount"`
}

// Receipt embeds a cross-package (in-module) struct -> its fields (amount,
// currency) are promoted alongside note.
type Receipt struct {
	types.Money
	Note string `json:"note"`
}

// IDReq identifies a resource by path parameter.
type IDReq struct {
	ID int64 `param:"id" validate:"required"`
}

func (h *Handler) getOrder(req IDReq, ctx server.HandlerContext) (server.Result[Order], server.IAPIError) {
	return server.NewResult(http.StatusOK, Order{}), nil
}

// getPrice returns a sibling-package type directly as the top-level response.
func (h *Handler) getPrice(req IDReq, ctx server.HandlerContext) (server.Result[types.Money], server.IAPIError) {
	return server.NewResult(http.StatusOK, types.Money{}), nil
}

// getReceipt returns a struct that embeds a cross-package type (promotion).
func (h *Handler) getReceipt(req IDReq, ctx server.HandlerContext) (server.Result[Receipt], server.IAPIError) {
	return server.NewResult(http.StatusOK, Receipt{}), nil
}
