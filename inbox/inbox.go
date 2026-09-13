// Package inbox provides durable consumer-side idempotency: a ledger that records
// processed event ids so redeliveries are skipped. It is the consumer-side
// complement to the transactional outbox.
//
// Consumers take the ledger key from the delivery (messaging.Metadata.DedupKey,
// which validates the x-outbox-event-id header — or, when the delivery carries
// no such header, the AMQP message_id property — against the ledger grammar)
// and wrap their handler in deps.Inbox.ProcessOnce, which checks the key's
// provenance, records it and runs the handler atomically, exactly once per key.
package inbox

import (
	"context"
	"fmt"
	"time"

	"github.com/gaborage/go-bricks/database"
	dbtypes "github.com/gaborage/go-bricks/database/types"
	"github.com/gaborage/go-bricks/messaging"
	"github.com/gaborage/go-bricks/multitenant"
)

// Inbox implements app.InboxProcessor, backed by the module's lazily-initialized
// vendor store.
type Inbox struct {
	module *Module
}

// ProcessOnce records key in the ledger and runs fn exactly once per key,
// atomically within a single transaction. A redelivery of an already-processed
// key short-circuits (fn is not run), counts one dedup hit and returns nil. The
// tenant is resolved from ctx; in single-tenant mode the tenant id is empty.
//
// key comes from messaging.Metadata.DedupKey or messaging.WireDedupKey; the
// ledger row carries key.String(). messaging.ValidateDedupKey runs BEFORE the
// ledger: the zero DedupKey, and a Sealed key under a context the sealed typed
// door did not mark (messaging.IsSealedDelivery), are refused with an error
// wrapping messaging.ErrInvalidEventID and no row is written. Only the sealed
// door mints a Sealed key, so no string a publisher or consumer writes can
// occupy a sealed message's ledger row.
func (i *Inbox) ProcessOnce(ctx context.Context, key messaging.DedupKey, fn func(ctx context.Context, tx dbtypes.Tx) error) error {
	if err := messaging.ValidateDedupKey(ctx, key); err != nil {
		return fmt.Errorf("inbox: %w", err)
	}
	eventID := key.String()
	store, err := i.module.ensureStoreInitialized(ctx)
	if err != nil {
		return err
	}
	db, err := i.module.getDB(ctx)
	if err != nil {
		return err
	}

	tenantID, _ := multitenant.GetTenant(ctx)
	rec := Record{TenantID: tenantID, EventID: eventID, ProcessedAt: time.Now()}

	return database.WithTx(ctx, db, func(ctx context.Context, tx dbtypes.Tx) error {
		inserted, err := store.MarkProcessed(ctx, tx, rec)
		if err != nil {
			return err
		}
		if !inserted {
			i.module.recordDedupHit(ctx, tenantID, eventID, key.Sealed())
			return nil // already processed: skip fn, commit the no-op
		}
		return fn(ctx, tx)
	})
}
