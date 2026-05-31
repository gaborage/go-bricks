// Package users defines a Request type that collides by name with orders.Request.
package users

// Request is the user-creation request body.
type Request struct {
	Email string `json:"email" validate:"required,email"`
}
