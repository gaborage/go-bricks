package inbox

import (
	"fmt"
	"time"

	"github.com/gaborage/go-bricks/scheduler"
)

// Cleanup is a scheduler.Executor that removes processed-event records older
// than the configured retention period. Runs daily at 04:00 by default.
type Cleanup struct {
	store           Store
	retentionPeriod time.Duration
}

func (c *Cleanup) Execute(ctx scheduler.JobContext) error {
	db := ctx.DB()
	if db == nil {
		return fmt.Errorf("inbox cleanup: database not available")
	}

	cutoff := time.Now().Add(-c.retentionPeriod)

	deleted, err := c.store.DeleteProcessed(ctx, db, cutoff)
	if err != nil {
		return fmt.Errorf("inbox cleanup: delete failed: %w", err)
	}

	if deleted > 0 {
		ctx.Logger().Info().
			Int64("deleted", deleted).
			Str("cutoff", cutoff.Format(time.RFC3339)).
			Msg("Inbox cleanup completed")
	}

	return nil
}
