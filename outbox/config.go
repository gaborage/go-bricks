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
		return fmt.Errorf("outbox: pollinterval must be positive, got %s", c.PollInterval)
	}
	if c.BatchSize <= 0 {
		return fmt.Errorf("outbox: batchsize must be positive, got %d", c.BatchSize)
	}
	if c.MaxRetries < 0 {
		return fmt.Errorf("outbox: maxretries must not be negative, got %d", c.MaxRetries)
	}
	if c.RetentionPeriod < 0 {
		return fmt.Errorf("outbox: retentionperiod must not be negative, got %s", c.RetentionPeriod)
	}
	if c.PublishTimeout < 0 {
		return fmt.Errorf("outbox: publishtimeout must not be negative, got %s", c.PublishTimeout)
	}
	if c.Tenancy != config.TenancyPerTenant && c.Tenancy != config.TenancyShared {
		return fmt.Errorf("outbox: tenancy must be %q or %q, got %q",
			config.TenancyPerTenant, config.TenancyShared, c.Tenancy)
	}
	return nil
}

// applyDefaults fills in zero-value fields with production-safe defaults.
// AutoCreateTable is intentionally not set here: its default (false, opt-in) is
// the zero value, so auto-creation must be explicitly enabled.
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
	if c.PublishTimeout == 0 {
		c.PublishTimeout = 60 * time.Second
	}
	if c.Tenancy == "" {
		c.Tenancy = config.TenancyPerTenant
	}
}
