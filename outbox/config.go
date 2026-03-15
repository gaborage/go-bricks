package outbox

import (
	"time"

	"github.com/gaborage/go-bricks/config"
)

// DefaultTableName is the default outbox table name.
const DefaultTableName = "gobricks_outbox"

// applyDefaults fills in zero-value fields with production-safe defaults.
func applyDefaults(c *config.OutboxConfig) {
	if c.TableName == "" {
		c.TableName = DefaultTableName
	}
	if c.PollInterval == 0 {
		c.PollInterval = 5 * time.Second
	}
	if c.BatchSize == 0 {
		c.BatchSize = 100
	}
	if c.MaxRetries == 0 {
		c.MaxRetries = 5
	}
	if c.RetentionPeriod == 0 {
		c.RetentionPeriod = 72 * time.Hour
	}
}
