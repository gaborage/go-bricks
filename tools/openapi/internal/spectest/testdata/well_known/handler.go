package events

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/gaborage/go-bricks/server"
	"github.com/google/uuid"
)

// Handler holds the events HTTP handlers.
type Handler struct{}

// Address is a nested struct used as a map value (map[string]Address).
type Address struct {
	Street string `json:"street"`
	City   string `json:"city"`
}

// Event exercises the well-known type formats, maps, and uint64 fidelity.
type Event struct {
	ID        uuid.UUID            `json:"id"`        // -> {string, uuid}
	CreatedAt time.Time            `json:"createdAt"` // -> {string, date-time}
	TTL       time.Duration        `json:"ttl"`       // -> {integer, int64} (encoding/json: ns count)
	Payload   []byte               `json:"payload"`   // -> {string, binary}
	Count     uint64               `json:"count"`     // -> {integer, int64, minimum 0}
	Labels    map[string]string    `json:"labels"`    // -> object, additionalProperties {string}
	Addrs     map[string]Address   `json:"addrs"`     // -> object, additionalProperties $ref Address
	History   map[string][]Address `json:"history"`   // -> object, additionalProperties {array, items $ref}
	Raw       json.RawMessage      `json:"raw"`       // -> {object}
}

// GetEventReq identifies an event by path parameter.
type GetEventReq struct {
	ID string `param:"id" validate:"required"`
}

func (h *Handler) getEvent(req GetEventReq, ctx server.HandlerContext) (server.Result[Event], server.IAPIError) {
	return server.NewResult(http.StatusOK, Event{}), nil
}
