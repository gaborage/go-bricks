package outbox

import (
	"fmt"
	"time"

	"github.com/gaborage/go-bricks/config"
)

// DefaultTableName is the default outbox table name.
const DefaultTableName = "gobricks_outbox"

// validateConfig checks that config values are within valid ranges.
// Called after applyDefaults, so zero values have already been replaced.
func validateConfig(c *config.OutboxConfig) error {
	if c.PollInterval <= 0 {
		return fmt.Errorf("outbox: poll_interval must be positive, got %s", c.PollInterval)
	}
	if c.BatchSize <= 0 {
		return fmt.Errorf("outbox: batch_size must be positive, got %d", c.BatchSize)
	}
	if c.MaxRetries < 0 {
		return fmt.Errorf("outbox: max_retries must not be negative, got %d", c.MaxRetries)
	}
	if c.RetentionPeriod < 0 {
		return fmt.Errorf("outbox: retention_period must not be negative, got %s", c.RetentionPeriod)
	}
	return nil
}

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
