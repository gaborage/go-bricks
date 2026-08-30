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

// The hold's defaults. They are applied only when the hold is enabled, so a
// deployment reading inbox.hold.* back does not find settings for a hold it
// never asked for.
const (
	DefaultHoldTableName     = "gobricks_inbox_hold"
	DefaultHoldDrainInterval = 5 * time.Second
	DefaultHoldMaxBackoff    = 5 * time.Minute
	DefaultHoldMaxAge        = time.Hour
	DefaultHoldLeaseDuration = 60 * time.Second
)

// applyDefaults fills zero-value fields with production-safe defaults.
// AutoCreateTable is intentionally left at its zero value (false, opt-in).
func applyDefaults(c *config.InboxConfig) {
	if c.TableName == "" {
		c.TableName = DefaultTableName
	}
	if c.RetentionPeriod == 0 {
		c.RetentionPeriod = DefaultRetentionPeriod
	}
	if c.Tenancy == "" {
		c.Tenancy = config.TenancyPerTenant
	}
	applyHoldDefaults(&c.Hold)
}

// applyHoldDefaults fills the hold's zero values, and only for an enabled hold.
func applyHoldDefaults(h *config.InboxHoldConfig) {
	if !h.Enabled {
		return
	}
	if h.TableName == "" {
		h.TableName = DefaultHoldTableName
	}
	if h.DrainInterval == 0 {
		h.DrainInterval = DefaultHoldDrainInterval
	}
	if h.MaxBackoff == 0 {
		h.MaxBackoff = DefaultHoldMaxBackoff
	}
	if h.MaxAge == 0 {
		h.MaxAge = DefaultHoldMaxAge
	}
	if h.LeaseDuration == 0 {
		h.LeaseDuration = DefaultHoldLeaseDuration
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
	if c.Tenancy != config.TenancyPerTenant && c.Tenancy != config.TenancyShared {
		return fmt.Errorf("inbox: tenancy must be %q or %q, got %q",
			config.TenancyPerTenant, config.TenancyShared, c.Tenancy)
	}
	return validateHoldConfig(&c.Hold)
}

// validateHoldConfig checks the hold's own values. Called after the defaults, so
// a zero duration here is one the operator wrote.
func validateHoldConfig(h *config.InboxHoldConfig) error {
	if !h.Enabled {
		return nil
	}

	for _, d := range []struct {
		key   string
		value time.Duration
	}{
		{"draininterval", h.DrainInterval},
		{"maxbackoff", h.MaxBackoff},
		{"maxage", h.MaxAge},
		{"leaseduration", h.LeaseDuration},
	} {
		if d.value <= 0 {
			return fmt.Errorf("inbox: hold.%s must be positive, got %s", d.key, d.value)
		}
	}

	if err := validateTableName(h.TableName); err != nil {
		return fmt.Errorf("inbox: hold.tablename: %w", err)
	}
	return nil
}
