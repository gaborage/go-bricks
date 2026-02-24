package redis

import (
	"fmt"
	"time"

	"github.com/gaborage/go-bricks/cache"
)

// Config holds Redis-specific configuration options.
type Config struct {
	// Host is the Redis server hostname or IP address.
	Host string `config:"host" required:"true"`

	// Port is the Redis server port (default: 6379).
	Port int `config:"port" default:"6379"`

	// Password for Redis authentication (optional).
	// Should be provided via environment variable: CACHE_REDIS_PASSWORD
	Password string `config:"password"` //nolint:gosec // G117 - config field, loaded from env/vault

	// Database number to use (default: 0).
	// Redis supports databases 0-15 by default.
	Database int `config:"database" default:"0"`

	// PoolSize is the maximum number of socket connections (default: 10).
	// Higher values allow more concurrent operations but consume more resources.
	PoolSize int `config:"pool_size" default:"10"`

	// DialTimeout is the timeout for establishing new connections (default: 5s).
	DialTimeout time.Duration `config:"dial_timeout" default:"5s"`

	// ReadTimeout is the timeout for socket reads (default: 3s).
	// -1 disables timeout.
	ReadTimeout time.Duration `config:"read_timeout" default:"3s"`

	// WriteTimeout is the timeout for socket writes (default: 3s).
	// -1 disables timeout.
	WriteTimeout time.Duration `config:"write_timeout" default:"3s"`

	// MaxRetries is the maximum number of retries before giving up (default: 3).
	// -1 disables retries.
	MaxRetries int `config:"max_retries" default:"3"`

	// MinRetryBackoff is the minimum backoff between retries (default: 8ms).
	MinRetryBackoff time.Duration `config:"min_retry_backoff" default:"8ms"`

	// MaxRetryBackoff is the maximum backoff between retries (default: 512ms).
	MaxRetryBackoff time.Duration `config:"max_retry_backoff" default:"512ms"`
}

// Validate performs fail-fast validation of Redis configuration.
// Returns error if configuration is invalid.
func (c *Config) Validate() error {
	if c.Host == "" {
		return cache.NewConfigError("redis.host", "host is required", nil)
	}

	if c.Port <= 0 || c.Port > 65535 {
		return cache.NewConfigError("redis.port", fmt.Sprintf("invalid port: %d", c.Port), nil)
	}

	if c.Database < 0 || c.Database > 15 {
		return cache.NewConfigError("redis.database", fmt.Sprintf("invalid database number: %d (must be 0-15)", c.Database), nil)
	}

	if c.PoolSize <= 0 {
		return cache.NewConfigError("redis.pool_size", fmt.Sprintf("invalid pool size: %d (must be > 0)", c.PoolSize), nil)
	}

	if c.DialTimeout < 0 {
		return cache.NewConfigError("redis.dial_timeout", "dial timeout cannot be negative", nil)
	}

	if c.ReadTimeout < -1 {
		return cache.NewConfigError("redis.read_timeout", "read timeout cannot be less than -1", nil)
	}

	if c.WriteTimeout < -1 {
		return cache.NewConfigError("redis.write_timeout", "write timeout cannot be less than -1", nil)
	}

	return nil
}

// Address returns the Redis server address in "host:port" format.
func (c *Config) Address() string {
	return fmt.Sprintf("%s:%d", c.Host, c.Port)
}
