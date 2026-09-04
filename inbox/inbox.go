// Package inbox provides durable consumer-side idempotency: a ledger that records
// processed event ids so redeliveries are skipped. It is the consumer-side
// complement to the transactional outbox.
//
// Consumers take the event id from the delivery (messaging.Metadata.DedupKey,
// which validates the x-outbox-event-id header against the ledger grammar) and
// wrap their handler in deps.Inbox.ProcessOnce, which re-checks the grammar,
// records the id and runs the handler atomically, exactly once per id.
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

// ProcessOnce records eventID in the ledger and runs fn exactly once per id,
// atomically within a single transaction. A redelivery of an already-processed
// id short-circuits (fn is not run), counts one dedup hit and returns nil. The
// tenant is resolved from ctx; in single-tenant mode the tenant id is empty.
//
// eventID must match ^[A-Za-z0-9_-]{1,128}$ (messaging.ValidateEventID); any
// other id is refused BEFORE the ledger with an error wrapping
// messaging.ErrInvalidEventID and no row is written. The check is here, at the
// ledger door, rather than only where a header is read, so it holds however the
// consumer obtained the id — and so a header-sourced id can never spell a sealed
// dedup key, whose `:` is outside the grammar.
func (i *Inbox) ProcessOnce(ctx context.Context, eventID string, fn func(ctx context.Context, tx dbtypes.Tx) error) error {
	if err := messaging.ValidateEventID(eventID); err != nil {
		return fmt.Errorf("inbox: %w", err)
	}
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
			i.module.recordDedupHit(ctx, tenantID, eventID)
			return nil // already processed: skip fn, commit the no-op
		}
		return fn(ctx, tx)
	})
}
