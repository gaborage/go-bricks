package inbox

import (
	"context"
	"time"

	dbtypes "github.com/gaborage/go-bricks/database/types"
	"github.com/gaborage/go-bricks/multitenant"
	"github.com/gaborage/go-bricks/scheduler"
)

// Cleanup is a scheduler.Executor that removes processed-event records older
// than the configured retention period. Runs daily at 04:00 by default.
//
// It resolves the database through the tenant-aware getDB resolver and fans out
// across the configured tenants in (static) multi-tenant mode, because the scheduler
// builds the JobContext from a tenant-less context (shared with the outbox cleanup
// via multitenant.FanOutRetentionCleanup).
type Cleanup struct {
	store           Store
	retentionPeriod time.Duration
	getDB           func(context.Context) (dbtypes.Interface, error)
	tenants         []string
}

func (c *Cleanup) Execute(jobCtx scheduler.JobContext) error {
	return multitenant.FanOutRetentionCleanup(
		jobCtx, jobCtx.Logger(), c.tenants, c.getDB, c.retentionPeriod, "inbox", c.store.DeleteProcessed,
	)
}
