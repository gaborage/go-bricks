package outbox

import (
	"encoding/json"
	"fmt"

	"github.com/gaborage/go-bricks/config"
	"github.com/gaborage/go-bricks/messaging"
	"github.com/gaborage/go-bricks/scheduler"
)

// Relay is a scheduler.Executor that polls for pending outbox events
// and publishes them to the message broker via existing AMQP infrastructure.
//
// The relay runs as a scheduled job (registered via scheduler.FixedRate),
// getting overlapping prevention, panic recovery, and OTel metrics for free.
type Relay struct {
	store        Store
	config       config.OutboxConfig
	getMessaging func(scheduler.JobContext) messaging.AMQPClient
}

func (r *Relay) Execute(ctx scheduler.JobContext) error {
	db := ctx.DB()
	if db == nil {
		return fmt.Errorf("outbox relay: database not available")
	}

	msgClient := r.getMessaging(ctx)
	if msgClient == nil {
		return fmt.Errorf("outbox relay: messaging not available")
	}

	records, err := r.store.FetchPending(ctx, db, r.config.BatchSize, r.config.MaxRetries)
	if err != nil {
		return fmt.Errorf("outbox relay: fetch failed: %w", err)
	}

	if len(records) == 0 {
		return nil
	}

	var published, failed int
	for i := range records {
		record := &records[i]
		// Decode headers
		headers := decodeHeaders(record.Headers)

		// Inject outbox metadata headers for consumer idempotency
		if headers == nil {
			headers = make(map[string]any)
		}
		headers["x-outbox-event-id"] = record.ID
		headers["x-outbox-event-type"] = record.EventType

		opts := messaging.PublishOptions{
			Exchange:   record.Exchange,
			RoutingKey: record.RoutingKey,
			Headers:    headers,
		}

		if err := msgClient.PublishToExchange(ctx, opts, record.Payload); err != nil {
			if markErr := r.store.MarkFailed(ctx, db, record.ID, err.Error()); markErr != nil {
				ctx.Logger().Error().
					Err(markErr).
					Str("eventID", record.ID).
					Msg("Failed to mark outbox event as failed")
			}
			failed++
			continue
		}

		if err := r.store.MarkPublished(ctx, db, record.ID); err != nil {
			ctx.Logger().Error().
				Err(err).
				Str("eventID", record.ID).
				Msg("Failed to mark outbox event as published")
			failed++
			continue
		}

		published++
	}

	ctx.Logger().Info().
		Int("published", published).
		Int("failed", failed).
		Int("total", len(records)).
		Msg("Outbox relay cycle completed")

	return nil
}

// decodeHeaders unmarshals JSON-encoded headers. Returns nil on empty/invalid input.
func decodeHeaders(data []byte) map[string]any {
	if len(data) == 0 {
		return nil
	}

	var headers map[string]any
	if err := json.Unmarshal(data, &headers); err != nil {
		return nil
	}

	return headers
}
