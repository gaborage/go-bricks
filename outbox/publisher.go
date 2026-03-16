package outbox

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/gaborage/go-bricks/app"
	dbtypes "github.com/gaborage/go-bricks/database/types"
	"github.com/google/uuid"
)

// outboxPublisher implements app.OutboxPublisher by writing events to the outbox table
// within the caller's database transaction.
type outboxPublisher struct {
	store           Store
	defaultExchange string
}

// newPublisher creates a new outbox publisher.
func newPublisher(store Store, defaultExchange string) app.OutboxPublisher {
	return &outboxPublisher{
		store:           store,
		defaultExchange: defaultExchange,
	}
}

func (p *outboxPublisher) Publish(ctx context.Context, tx dbtypes.Tx, event *app.OutboxEvent) (string, error) {
	if tx == nil {
		return "", fmt.Errorf("outbox: transaction must not be nil")
	}

	if event == nil {
		return "", fmt.Errorf("outbox: event must not be nil")
	}

	if event.EventType == "" {
		return "", fmt.Errorf("outbox: event type must not be empty")
	}

	if event.AggregateID == "" {
		return "", fmt.Errorf("outbox: aggregate ID must not be empty")
	}

	// Marshal payload
	payload, err := marshalPayload(event.Payload)
	if err != nil {
		return "", fmt.Errorf("outbox: failed to marshal payload: %w", err)
	}

	// Marshal headers (if any)
	var headers []byte
	if len(event.Headers) > 0 {
		headers, err = json.Marshal(event.Headers)
		if err != nil {
			return "", fmt.Errorf("outbox: failed to marshal headers: %w", err)
		}
	}

	// Resolve exchange
	exchange := event.Exchange
	if exchange == "" {
		exchange = p.defaultExchange
	}

	// Resolve routing key
	routingKey := event.RoutingKey
	if routingKey == "" {
		routingKey = event.EventType
	}

	// Generate event ID
	eventID := uuid.New().String()

	record := &Record{
		ID:          eventID,
		EventType:   event.EventType,
		AggregateID: event.AggregateID,
		Payload:     payload,
		Headers:     headers,
		Exchange:    exchange,
		RoutingKey:  routingKey,
		Status:      StatusPending,
		CreatedAt:   time.Now(),
	}

	if err := p.store.Insert(ctx, tx, record); err != nil {
		return "", err
	}

	return eventID, nil
}

// marshalPayload converts the payload to []byte.
// If the payload is already []byte, it is returned as-is.
// Otherwise, it is JSON-marshaled.
func marshalPayload(payload any) ([]byte, error) {
	if payload == nil {
		return []byte("null"), nil
	}

	if b, ok := payload.([]byte); ok {
		return b, nil
	}

	return json.Marshal(payload)
}
