package outbox

import (
	"context"
	"errors"
	"fmt"
	"time"

	dbtypes "github.com/gaborage/go-bricks/database/types"
	"github.com/gaborage/go-bricks/multitenant"
	"github.com/gaborage/go-bricks/scheduler"
)

// Cleanup is a scheduler.Executor that removes published events
// older than the configured retention period.
//
// Runs daily at 04:00 by default (registered via scheduler.DailyAt).
// Like the relay, it resolves the database through the tenant-aware getDB
// resolver and fans out across the configured tenants in multi-tenant mode.
type Cleanup struct {
	store           Store
	retentionPeriod time.Duration
	getDB           func(context.Context) (dbtypes.Interface, error)
	tenants         []string
}

func (c *Cleanup) Execute(jobCtx scheduler.JobContext) error {
	log := jobCtx.Logger()
	cutoff := time.Now().Add(-c.retentionPeriod)
	var errs []error
	for _, tenantID := range c.tenants {
		ctx := multitenant.SetTenant(jobCtx, tenantID)
		db, err := c.getDB(ctx)
		if err != nil {
			errs = append(errs, fmt.Errorf("outbox cleanup: tenant %q: database not available: %w", tenantID, err))
			continue
		}
		if db == nil {
			errs = append(errs, fmt.Errorf("outbox cleanup: tenant %q: database not available", tenantID))
			continue
		}

		deleted, err := c.store.DeletePublished(ctx, db, cutoff)
		if err != nil {
			errs = append(errs, fmt.Errorf("outbox cleanup: tenant %q: delete failed: %w", tenantID, err))
			continue
		}

		if deleted > 0 {
			event := log.Info().
				Int64("deleted", deleted).
				Str("cutoff", cutoff.Format(time.RFC3339))
			if tenantID != "" {
				event = event.Str("tenant", tenantID)
			}
			event.Msg("Outbox cleanup completed")
		}
	}
	return errors.Join(errs...)
}
