package outbox

import (
	"encoding/json"
	"fmt"

	"github.com/gaborage/go-bricks/config"
	dbtypes "github.com/gaborage/go-bricks/database/types"
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

	// Check broker readiness before fetching records.
	// PublishToExchange returns nil (not error) when the client is not ready,
	// which would cause events to be incorrectly marked as published.
	if !msgClient.IsReady() {
		return fmt.Errorf("outbox relay: messaging client not ready")
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
		if r.publishRecord(ctx, db, msgClient, &records[i]) {
			published++
		} else {
			failed++
		}
	}

	ctx.Logger().Info().
		Int("published", published).
		Int("failed", failed).
		Int("total", len(records)).
		Msg("Outbox relay cycle completed")

	return nil
}

// publishRecord attempts to publish a single outbox record to the message broker.
// Returns true on success, false on failure (record is marked as failed).
func (r *Relay) publishRecord(ctx scheduler.JobContext, db dbtypes.Interface, msgClient messaging.AMQPClient, record *Record) bool {
	// Decode headers — treat corrupted headers as a publish failure
	headers, decodeErr := decodeHeaders(record.Headers)
	if decodeErr != nil {
		r.markRecordFailed(ctx, db, record.ID, decodeErr.Error())
		return false
	}

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
		r.markRecordFailed(ctx, db, record.ID, err.Error())
		return false
	}

	if err := r.store.MarkPublished(ctx, db, record.ID); err != nil {
		ctx.Logger().Error().
			Err(err).
			Str("eventID", record.ID).
			Msg("Failed to mark outbox event as published")
		return false
	}

	return true
}

// markRecordFailed marks an outbox record as failed, logging any secondary errors.
func (r *Relay) markRecordFailed(ctx scheduler.JobContext, db dbtypes.Interface, eventID, errMsg string) {
	if markErr := r.store.MarkFailed(ctx, db, eventID, errMsg); markErr != nil {
		ctx.Logger().Error().
			Err(markErr).
			Str("eventID", eventID).
			Msg("Failed to mark outbox event as failed")
	}
}

// decodeHeaders unmarshals JSON-encoded headers.
// Returns (nil, nil) on empty input, or an error on invalid JSON.
func decodeHeaders(data []byte) (map[string]any, error) {
	if len(data) == 0 {
		return nil, nil
	}

	var headers map[string]any
	if err := json.Unmarshal(data, &headers); err != nil {
		return nil, fmt.Errorf("outbox relay: invalid headers JSON: %w", err)
	}

	return headers, nil
}
