package inbox

import (
	"fmt"
	"time"

	"github.com/gaborage/go-bricks/config"
)

// DefaultTableName is the default inbox ledger table name.
const DefaultTableName = "gobricks_inbox"

// DefaultRetentionPeriod is the default processed-event retention (7 days).
// It must exceed the broker's maximum redelivery window. Written as a duration
// (168h) because Go's time.ParseDuration does not accept a "7d" unit.
const DefaultRetentionPeriod = 7 * 24 * time.Hour

// applyDefaults fills zero-value fields with production-safe defaults.
// AutoCreateTable is intentionally left at its zero value (false, opt-in).
func applyDefaults(c *config.InboxConfig) {
	if c.TableName == "" {
		c.TableName = DefaultTableName
	}
	if c.RetentionPeriod == 0 {
		c.RetentionPeriod = DefaultRetentionPeriod
	}
}

// validateConfig checks that config values are within valid ranges.
// Called after applyDefaults, so zero values have already been replaced.
func validateConfig(c *config.InboxConfig) error {
	if c.RetentionPeriod < 0 {
		return fmt.Errorf("inbox: retentionperiod must not be negative, got %s", c.RetentionPeriod)
	}
	if err := validateTableName(c.TableName); err != nil {
		return err
	}
	return nil
}
