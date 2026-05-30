package embed

import (
	"net/http"
	"time"

	"github.com/gaborage/go-bricks/server"
)

// Handler holds the HTTP handlers.
type Handler struct{}

// Base is an embedded struct whose exported fields are promoted into the parent.
type Base struct {
	ID        int64     `json:"id"`
	CreatedAt time.Time `json:"createdAt"`
}

// User embeds Base by value (no json tag) -> id and createdAt are promoted
// alongside name.
type User struct {
	Base
	Name string `json:"name"`
}

// Account embeds *Base (pointer) -> promotes the same fields as value embedding.
type Account struct {
	*Base
	Balance int64 `json:"balance"`
}

// Wrapper embeds Base WITH a json tag -> nests as a sub-object ("base") instead
// of promoting (rendered as a $ref to Base).
type Wrapper struct {
	Base  `json:"base"`
	Label string `json:"label"`
}

// IDReq identifies a resource by path parameter.
type IDReq struct {
	ID int64 `param:"id" validate:"required"`
}

func (h *Handler) getUser(req IDReq, ctx server.HandlerContext) (server.Result[User], server.IAPIError) {
	return server.NewResult(http.StatusOK, User{}), nil
}
func (h *Handler) getAccount(req IDReq, ctx server.HandlerContext) (server.Result[Account], server.IAPIError) {
	return server.NewResult(http.StatusOK, Account{}), nil
}
func (h *Handler) getWrapper(req IDReq, ctx server.HandlerContext) (server.Result[Wrapper], server.IAPIError) {
	return server.NewResult(http.StatusOK, Wrapper{}), nil
}
