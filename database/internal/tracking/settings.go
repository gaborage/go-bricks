// Package tracking provides performance tracking for database operations.
// This package implements query tracking, slow query detection, and structured logging
// for database operations across all supported database backends.
package tracking

import (
	"time"

	"github.com/gaborage/go-bricks/config"
	"github.com/gaborage/go-bricks/logger"
)

const (
	// DefaultSlowQueryThreshold defines the default threshold for slow query detection
	DefaultSlowQueryThreshold = 200 * time.Millisecond
	// DefaultMaxQueryLength defines the default maximum query length for logging
	DefaultMaxQueryLength = 1000
)

// Settings holds configuration for database query tracking and logging.
// These settings control how database operations are monitored and logged.
type Settings struct {
	slowQueryThreshold time.Duration
	maxQueryLength     int
	logQueryParameters bool
}

// Context groups tracking-related parameters to reduce function parameter count.
// This context is passed to tracking functions to provide consistent access to
// logger, database vendor information, tracking settings, and server connection metadata
// for OpenTelemetry semantic convention attributes.
type Context struct {
	Logger   logger.Logger
	Vendor   string
	Settings Settings

	// Server connection metadata for OTel attributes
	ServerAddress string // server.address attribute (database host)
	ServerPort    int    // server.port attribute (database port)
	Namespace     string // db.namespace attribute (vendor-specific format)
}

// NewSettings creates Settings populated from the provided database configuration.
// If cfg is nil or a numeric field is non-positive, sensible defaults are used:
// DefaultSlowQueryThreshold for slowQueryThreshold and DefaultMaxQueryLength for maxQueryLength.
// NewSettings creates a Settings configured from the provided DatabaseConfig.
//
// It initializes defaults from DefaultSlowQueryThreshold, DefaultMaxQueryLength,
// and a default of false for logging query parameters. If cfg is nil the defaults
// are returned. When cfg is provided, cfg.Query.Slow.Threshold > 0 overrides
// the slow query threshold, cfg.Query.Log.MaxLength > 0 overrides the max
// query length, and cfg.Query.Log.Parameters is copied into the logQueryParameters flag.
func NewSettings(cfg *config.DatabaseConfig) Settings {
	settings := Settings{
		slowQueryThreshold: DefaultSlowQueryThreshold,
		maxQueryLength:     DefaultMaxQueryLength,
		logQueryParameters: false,
	}

	if cfg == nil {
		return settings
	}

	if cfg.Query.Slow.Threshold > 0 {
		settings.slowQueryThreshold = cfg.Query.Slow.Threshold
	}
	if cfg.Query.Log.MaxLength > 0 {
		settings.maxQueryLength = cfg.Query.Log.MaxLength
	}
	settings.logQueryParameters = cfg.Query.Log.Parameters

	return settings
}

// SlowQueryThreshold returns the threshold for slow query detection
func (s Settings) SlowQueryThreshold() time.Duration {
	return s.slowQueryThreshold
}

// MaxQueryLength returns the maximum query length for logging
func (s Settings) MaxQueryLength() int {
	return s.maxQueryLength
}

// LogQueryParameters returns whether query parameters should be logged
func (s Settings) LogQueryParameters() bool {
	return s.logQueryParameters
}
