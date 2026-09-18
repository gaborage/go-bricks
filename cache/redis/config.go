package redis

import (
	"fmt"
	"strings"
	"time"

	"github.com/gaborage/go-bricks/cache"
	"github.com/gaborage/go-bricks/internal/clienttls"
)

// tlsFieldPrefix namespaces a clienttls.Violation's relative key (e.g.
// "cafile") into this package's config-error field.
const tlsFieldPrefix = "redis.tls."

// Config holds Redis-specific configuration options.
type Config struct {
	// Host is the Redis server hostname or IP address.
	Host string `config:"host" required:"true"`

	// Port is the Redis server port (default: 6379).
	Port int `config:"port" default:"6379"`

	// Username is the Redis ACL user to authenticate as, sent as
	// AUTH <username> <password>. Empty authenticates as the implicit "default"
	// user. Requires Password — Validate refuses a name with an empty password,
	// because the driver then sends no AUTH and the dial would run as the default
	// user. Required by deployments that gate access with ACLs, such as Amazon
	// ElastiCache RBAC. Filled from config.RedisConfig, which owns the
	// cache.redis.username key (env CACHE_REDIS_USERNAME); deliberately carries
	// no config: tag, because nothing injects this struct and the tags on the
	// fields around it are dead (#1729).
	Username string

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
	_, err := c.validate()
	return err
}

// validate is Validate plus the TLS material projection it built on the way,
// so a caller that needs both — NewClient — pays for the projection once.
func (c *Config) validate() (clienttls.Material, error) {
	if c.Host == "" {
		return clienttls.Material{}, cache.NewConfigError("redis.host", "host is required", nil)
	}

	if c.Port <= 0 || c.Port > 65535 {
		return clienttls.Material{}, cache.NewConfigError("redis.port", fmt.Sprintf("invalid port: %d", c.Port), nil)
	}

	// Empty is the default user; whitespace-only is a typo that would travel as an
	// AUTH argument no ACL rule can match. Checked before the coupling rule below so
	// a name that is both blank and unaccompanied is reported as the typo it is.
	if c.Username != "" && strings.TrimSpace(c.Username) == "" {
		return clienttls.Material{}, cache.NewConfigError("redis.username", "username cannot be whitespace-only", nil)
	}

	// A name with no password never authenticates: go-redis builds the HELLO
	// handshake's AUTH clause inside `if password != ""`, and gates the legacy AUTH
	// fallback the same way, so nothing is sent and the dial runs as whatever identity
	// the server hands an unauthenticated client. Refused here rather than dialed,
	// mirroring config.validateRedisCache, because a hand-built Config reaches this
	// door without passing through the config layer. A password alone is the legacy
	// form that selects the implicit "default" user and stands.
	if c.Username != "" && c.Password == "" {
		return clienttls.Material{}, cache.NewConfigError("redis.username",
			"username requires redis.password: the client sends no AUTH without one, "+
				"so the connection would silently run as the default user", nil)
	}

	if c.Database < 0 || c.Database > 15 {
		return clienttls.Material{}, cache.NewConfigError("redis.database", fmt.Sprintf("invalid database number: %d (must be 0-15)", c.Database), nil)
	}

	if c.PoolSize <= 0 {
		return clienttls.Material{}, cache.NewConfigError("redis.pool_size", fmt.Sprintf("invalid pool size: %d (must be > 0)", c.PoolSize), nil)
	}

	if c.DialTimeout < 0 {
		return clienttls.Material{}, cache.NewConfigError("redis.dial_timeout", "dial timeout cannot be negative", nil)
	}

	if c.ReadTimeout < -1 {
		return clienttls.Material{}, cache.NewConfigError("redis.read_timeout", "read timeout cannot be less than -1", nil)
	}

	if c.WriteTimeout < -1 {
		return clienttls.Material{}, cache.NewConfigError("redis.write_timeout", "write timeout cannot be less than -1", nil)
	}

	// Zero stays valid: it means "unset", and LoadThrough then uses its own fallback. A
	// negative is rejected here because a hand-built Config never passes through the config
	// layer's cache.loadtimeout normalization, and LoadThrough treats a non-positive value
	// as "not configured" — so without this the operator's value would be silently ignored
	// rather than corrected or refused.
	if c.LoadTimeout < 0 {
		return clienttls.Material{}, cache.NewConfigError("redis.load_timeout", "load timeout cannot be negative", nil)
	}

	return c.TLS.validate()
}

// validate checks the structural TLS rules without touching the filesystem:
// material staged under a disabled block, a piece configured from two sources,
// a half client-certificate pair, and the min-version enum. The rules
// themselves live in clienttls, beside the loader that consumes the same
// material; reading and parsing the PEM happens when the client dials.
//
// It returns the projection it validated so the dial path can reuse it instead
// of building a second, identical one.
func (t *TLSConfig) validate() (clienttls.Material, error) {
	m := t.material()
	if v := clienttls.ValidateMaterial(&m, t.Enabled); v != nil {
		return clienttls.Material{}, cache.NewConfigError(tlsFieldPrefix+v.Field, v.Message, nil)
	}
	return m, nil
}

// material projects the block onto the shared clienttls.Material. ServerName is
// carried exactly as configured: falling back to the Redis host is the dial's
// business, not the shape check's — substituting it here would make an empty
// disabled block look like staged material.
func (t *TLSConfig) material() clienttls.Material {
	return clienttls.Material{
		CertFile:   t.CertFile,
		CertValue:  t.CertValue,
		KeyFile:    t.KeyFile,
		KeyValue:   t.KeyValue,
		CAFile:     t.CAFile,
		CAValue:    t.CAValue,
		ServerName: t.ServerName,
		MinVersion: t.MinVersion,
	}
}

// Address returns the Redis server address in "host:port" format.
func (c *Config) Address() string {
	return fmt.Sprintf("%s:%d", c.Host, c.Port)
}
