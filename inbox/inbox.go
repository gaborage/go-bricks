// Package inbox provides durable consumer-side idempotency: a ledger that records
// processed event ids so redeliveries are skipped. It is the consumer-side
// complement to the transactional outbox.
//
// Consumers extract the event id from the delivery (e.g. via
// outbox.EventIDFromHeaders) and wrap their handler in deps.Inbox.ProcessOnce,
// which records the id and runs the handler atomically, exactly once per id.
package inbox

import (
	"context"
	"time"

	"github.com/gaborage/go-bricks/database"
	dbtypes "github.com/gaborage/go-bricks/database/types"
	"github.com/gaborage/go-bricks/multitenant"
)

// Inbox implements app.InboxProcessor, backed by the module's lazily-initialized
// vendor store.
type Inbox struct {
	module *Module
}

// ProcessOnce records eventID in the ledger and runs fn exactly once per id,
// atomically within a single transaction. A redelivery of an already-processed
// id short-circuits (fn is not run) and returns nil. The tenant is resolved from
// ctx; in single-tenant mode the tenant id is empty.
func (i *Inbox) ProcessOnce(ctx context.Context, eventID string, fn func(ctx context.Context, tx dbtypes.Tx) error) error {
	if err := i.module.ensureStoreInitialized(ctx); err != nil {
		return err
	}
	db, err := i.module.getDB(ctx)
	if err != nil {
		return err
	}

	tenantID, _ := multitenant.GetTenant(ctx)
	rec := Record{TenantID: tenantID, EventID: eventID, ProcessedAt: time.Now()}

	return database.WithTx(ctx, db, func(ctx context.Context, tx dbtypes.Tx) error {
		inserted, err := i.module.store.MarkProcessed(ctx, tx, rec)
		if err != nil {
			return err
		}
		if !inserted {
			return nil // already processed: skip fn, commit the no-op
		}
		return fn(ctx, tx)
	})
}
