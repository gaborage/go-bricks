package shop

import (
	"net/http"

	"github.com/gaborage/go-bricks/server"
)

// Handler holds the shop's HTTP handlers (separate from the Module struct).
type Handler struct{}

// CreateUserReq is the create-user request body.
type CreateUserReq struct {
	Name  string `json:"name" validate:"required,min=2,max=50"`
	Email string `json:"email" validate:"required,email"`
}

// User is the user resource (the response type, wrapped in server.Result).
type User struct {
	ID    int64  `json:"id"`
	Name  string `json:"name"`
	Email string `json:"email"`
}

// GetUserReq identifies a user by path parameter.
type GetUserReq struct {
	ID int64 `param:"id" validate:"required"`
}

func (h *Handler) createUser(req CreateUserReq, ctx server.HandlerContext) (server.Result[User], server.IAPIError) {
	return server.Created(User{}), nil
}

func (h *Handler) getUser(req GetUserReq, ctx server.HandlerContext) (server.Result[User], server.IAPIError) {
	return server.NewResult(http.StatusOK, User{}), nil
}

// deleteUser returns NoContentResult — a bodyless 204 response. The analyzer
// marks the response TypeInfo with NoContent=true (no component is generated)
// and the generator renders it as a 204 with no response body.
func (h *Handler) deleteUser(req GetUserReq, ctx server.HandlerContext) (server.NoContentResult, server.IAPIError) {
	return server.NoContent(), nil
}
