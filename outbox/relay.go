package outbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/gaborage/go-bricks/config"
	dbtypes "github.com/gaborage/go-bricks/database/types"
	"github.com/gaborage/go-bricks/internal/leasescope"
	"github.com/gaborage/go-bricks/logger"
	"github.com/gaborage/go-bricks/messaging"
	"github.com/gaborage/go-bricks/multitenant"
	"github.com/gaborage/go-bricks/scheduler"
	gobrickstrace "github.com/gaborage/go-bricks/trace"
)

// Relay is a scheduler.Executor that polls for pending outbox events
// and publishes them to the message broker via existing AMQP infrastructure.
//
// The relay runs as a scheduled job (registered via scheduler.FixedRate),
// getting overlapping prevention, panic recovery, and OTel metrics for free.
//
// Resources are resolved through the tenant-aware getDB/getMessaging resolvers
// (the module's deps.DB/deps.Messaging) rather than the scheduler JobContext,
// because the scheduler builds the JobContext from a tenant-less context — so
// in multi-tenant mode the relay must inject each tenant into the context
// itself before resolving that tenant's database and broker.
type Relay struct {
	store        Store
	config       config.OutboxConfig
	getDB        func(context.Context) (dbtypes.Interface, error)
	getMessaging func(context.Context) (messaging.AMQPClient, error)
	// tenants lists the tenant keys to relay each cycle. Always non-empty: a single
	// "" entry for single-tenant mode (multitenant.SetTenant with "" is a no-op), or
	// the configured static multitenant tenant IDs. Dynamic multi-tenant sources are
	// rejected at module Init (their tenant set is not enumerable at registration time).
	tenants []string
}

// Execute runs one relay cycle per configured tenant. In single-tenant mode this is a
// single pass with no tenant in context; in static multi-tenant mode it fans out across
// the configured tenants, resolving each tenant's database and broker independently.
// Per-tenant failures are collected so one unhealthy tenant does not block the others.
func (r *Relay) Execute(jobCtx scheduler.JobContext) error {
	log := jobCtx.Logger()
	var errs []error
	for _, tenantID := range r.tenants {
		ctx := multitenant.SetTenant(jobCtx, tenantID)
		if err := r.relayTenant(ctx, log, tenantID); err != nil {
			errs = append(errs, fmt.Errorf("outbox relay: tenant %q: %w", tenantID, err))
		}
	}
	return errors.Join(errs...)
}

// relayTenant runs a single relay cycle for the given (tenant-scoped) context.
func (r *Relay) relayTenant(ctx context.Context, log logger.Logger, tenantID string) error {
	// Install a per-tenant lease scope so this tenant's DB and messaging handles are released
	// at the end of its relay cycle rather than pinned until the whole fan-out job ends
	// (ADR-032). This bounds the working set to ~one tenant, so a large multi-tenant relay
	// cannot hold every tenant's connection open at once and exceed the pool's MaxSize. The
	// per-tenant scope shadows the job-level scope installed by the scheduler.
	ctx, scope := leasescope.Install(ctx)
	defer scope.ReleaseAll()

	db, err := r.getDB(ctx)
	if err != nil {
		return fmt.Errorf("database not available: %w", err)
	}
	if db == nil {
		return fmt.Errorf("database not available")
	}

	msgClient, err := r.getMessaging(ctx)
	if err != nil {
		return fmt.Errorf("messaging not available: %w", err)
	}
	if msgClient == nil {
		return fmt.Errorf("messaging not available")
	}

	// Check broker readiness before fetching records.
	// Avoids pulling a full batch only to have every publish fail with errNotConnected
	// on the first not-ready cycle, which wastes a DB round-trip and retry increments.
	if !msgClient.IsReady() {
		return fmt.Errorf("messaging client not ready")
	}

	records, err := r.store.FetchPending(ctx, db, r.config.BatchSize, r.config.MaxRetries)
	if err != nil {
		return fmt.Errorf("fetch failed: %w", err)
	}

	if len(records) == 0 {
		return nil
	}

	var published, failed int
	for i := range records {
		if r.publishRecord(ctx, log, db, msgClient, &records[i]) {
			published++
		} else {
			failed++
		}
	}

	event := log.Info().
		Int("published", published).
		Int("failed", failed).
		Int("total", len(records))
	if tenantID != "" {
		event = event.Str("tenant", tenantID)
	}
	event.Msg("Outbox relay cycle completed")

	return nil
}

// publishRecord attempts to publish a single outbox record to the message broker.
// Returns true on success, false on failure (record is marked as failed).
func (r *Relay) publishRecord(ctx context.Context, log logger.Logger, db dbtypes.Interface, msgClient messaging.AMQPClient, record *Record) bool {
	// Decode headers — treat corrupted headers as a publish failure
	headers, decodeErr := decodeHeaders(record.Headers)
	if decodeErr != nil {
		r.markRecordFailed(ctx, log, db, record.ID, decodeErr.Error())
		return false
	}

	// Inject outbox metadata headers for consumer idempotency
	if headers == nil {
		headers = make(map[string]any)
	}
	headers[HeaderEventID] = record.ID
	headers[HeaderEventType] = record.EventType

	// Rehydrate the originating trace context (persisted by Publish) into the
	// publish context. The relay job runs detached with no ambient trace, so
	// without this the downstream preparePublishing would stamp a freshly
	// generated CorrelationId — which the consumer's failure-path logger and
	// consume span surface — breaking trace continuity on the error path.
	pubCtx := gobrickstrace.ExtractFromHeaders(ctx, &mapHeaderAccessor{headers: headers})

	opts := messaging.PublishOptions{
		Exchange:   record.Exchange,
		RoutingKey: record.RoutingKey,
		Headers:    headers,
	}

	if err := msgClient.PublishToExchange(pubCtx, opts, record.Payload); err != nil {
		r.markRecordFailed(ctx, log, db, record.ID, err.Error())
		return false
	}

	if err := r.store.MarkPublished(ctx, db, record.ID); err != nil {
		log.Error().
			Err(err).
			Str("eventID", record.ID).
			Msg("Failed to mark outbox event as published")
		return false
	}

	return true
}

// markRecordFailed marks an outbox record as failed, logging any secondary errors.
func (r *Relay) markRecordFailed(ctx context.Context, log logger.Logger, db dbtypes.Interface, eventID, errMsg string) {
	if markErr := r.store.MarkFailed(ctx, db, eventID, errMsg); markErr != nil {
		log.Error().
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
