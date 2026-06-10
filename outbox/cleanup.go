package outbox

import (
	"context"
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
// resolver and fans out across the configured tenants in multi-tenant mode
// (shared with the inbox cleanup via multitenant.FanOutRetentionCleanup).
type Cleanup struct {
	store           Store
	retentionPeriod time.Duration
	getDB           func(context.Context) (dbtypes.Interface, error)
	tenants         []string
}

func (c *Cleanup) Execute(jobCtx scheduler.JobContext) error {
	return multitenant.FanOutRetentionCleanup(
		jobCtx, jobCtx.Logger(), c.tenants, c.getDB, c.retentionPeriod, "outbox", c.store.DeletePublished,
	)
}
