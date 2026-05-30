// Package orders defines a Request type that collides by name with users.Request.
package orders

// Request is the order-creation request body (different shape, same name).
type Request struct {
	SKU      string `json:"sku" validate:"required"`
	Quantity int    `json:"quantity"`
}
