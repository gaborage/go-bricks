package redis

import (
	"fmt"
	"time"

	"github.com/gaborage/go-bricks/cache"
)

// Config error fields for the TLS block, matching the config keys.
const (
	fieldTLSEnabled    = "redis.tls.enabled"
	fieldTLSCAFile     = "redis.tls.cafile"
	fieldTLSCertFile   = "redis.tls.certfile"
	fieldTLSKeyFile    = "redis.tls.keyfile"
	fieldTLSMinVersion = "redis.tls.minversion"
)

// Config holds Redis-specific configuration options.
type Config struct {
	// Host is the Redis server hostname or IP address.
	Host string `config:"host" required:"true"`

	// Port is the Redis server port (default: 6379).
	Port int `config:"port" default:"6379"`

	// Password for Redis authentication (optional).
	// Should be provided via environment variable: CACHE_REDIS_PASSWORD
	Password string `config:"password"`

	// Database number to use (default: 0).
	// Redis supports databases 0-15 by default.
	Database int `config:"database" default:"0"`

	// PoolSize is the maximum number of socket connections (default: 10).
	// Higher values allow more concurrent operations but consume more resources.
	PoolSize int `config:"pool_size" default:"10"`

	// DialTimeout is the timeout for establishing new connections (default: 5s).
	DialTimeout time.Duration `config:"dial_timeout" default:"5s"`

	// LoadTimeout bounds each cache leg of cache.LoadThrough (cache.loadtimeout).
	// Zero leaves the helper on its own fallback; a deployment-resolved config always
	// carries a positive value.
	LoadTimeout time.Duration `config:"load_timeout" default:"500ms"`

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

	// TLS configures the client-side TLS of the connection. Zero value =
	// plaintext.
	TLS TLSConfig `config:"tls"`
}

// TLSConfig enables TLS on the Redis connection. Each PEM piece comes from a
// file path (*File) or a base64-encoded PEM string (*Value) — at most one
// source per piece. An enabled block with no material at all verifies against
// the system roots; staged material under a disabled block is an error, not a
// warning, because a silently plaintext cache connection is the failure mode
// this config exists to prevent.
type TLSConfig struct {
	// Enabled turns TLS on. False with any other field set is refused.
	Enabled bool `config:"enabled"`

	// CAFile and CAValue name the root bundle that verifies the server.
	CAFile  string `config:"cafile"`
	CAValue string `config:"cavalue"`

	// CertFile and CertValue name the client certificate; a cert requires a key.
	CertFile  string `config:"certfile"`
	CertValue string `config:"certvalue"`

	// KeyFile and KeyValue name the client key; a key requires a cert.
	KeyFile  string `config:"keyfile"`
	KeyValue string `config:"keyvalue"`

	// ServerName overrides the SNI/verification hostname; empty defaults to Host.
	ServerName string `config:"servername"`

	// MinVersion: "" or "1.2" (default floor) | "1.3".
	MinVersion string `config:"minversion"`
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

	// Zero stays valid: it means "unset", and LoadThrough then uses its own fallback. A
	// negative is rejected here because a hand-built Config never passes through the config
	// layer's cache.loadtimeout normalization, and LoadThrough treats a non-positive value
	// as "not configured" — so without this the operator's value would be silently ignored
	// rather than corrected or refused.
	if c.LoadTimeout < 0 {
		return cache.NewConfigError("redis.load_timeout", "load timeout cannot be negative", nil)
	}

	return c.TLS.validate()
}

// validate checks the structural TLS rules without touching the filesystem:
// material staged under a disabled block, a piece configured from two sources,
// a half client-certificate pair, and the min-version enum. Reading and parsing
// the PEM happens when the client dials.
func (t *TLSConfig) validate() error {
	if !t.Enabled {
		if t.hasMaterial() {
			return cache.NewConfigError(fieldTLSEnabled,
				"must be true when any redis.tls.* material is configured", nil)
		}
		return nil
	}

	sources := []struct{ fileField, valueField, file, value string }{
		{fieldTLSCAFile, "redis.tls.cavalue", t.CAFile, t.CAValue},
		{fieldTLSCertFile, "redis.tls.certvalue", t.CertFile, t.CertValue},
		{fieldTLSKeyFile, "redis.tls.keyvalue", t.KeyFile, t.KeyValue},
	}
	for _, s := range sources {
		if s.file != "" && s.value != "" {
			return cache.NewConfigError(s.fileField,
				s.fileField+" and "+s.valueField+" are mutually exclusive (exactly one)", nil)
		}
	}

	hasCert := t.CertFile != "" || t.CertValue != ""
	hasKey := t.KeyFile != "" || t.KeyValue != ""
	switch {
	case hasCert && !hasKey:
		return cache.NewConfigError(fieldTLSKeyFile,
			"a client certificate requires "+fieldTLSKeyFile+" or redis.tls.keyvalue", nil)
	case hasKey && !hasCert:
		return cache.NewConfigError(fieldTLSCertFile,
			"a client key requires "+fieldTLSCertFile+" or redis.tls.certvalue", nil)
	}

	switch t.MinVersion {
	case "", "1.2", "1.3":
		return nil
	default:
		return cache.NewConfigError(fieldTLSMinVersion,
			fmt.Sprintf("invalid value: %q (accepted values are \"1.2\" and \"1.3\")", t.MinVersion), nil)
	}
}

// hasMaterial reports whether any field other than Enabled is set.
func (t *TLSConfig) hasMaterial() bool {
	return t.CAFile != "" || t.CAValue != "" ||
		t.CertFile != "" || t.CertValue != "" ||
		t.KeyFile != "" || t.KeyValue != "" ||
		t.ServerName != "" || t.MinVersion != ""
}

// Address returns the Redis server address in "host:port" format.
func (c *Config) Address() string {
	return fmt.Sprintf("%s:%d", c.Host, c.Port)
}
