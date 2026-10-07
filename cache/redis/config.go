package redis

import (
	"cmp"
	"fmt"
	"strings"
	"time"

	"github.com/gaborage/go-bricks/cache"
	"github.com/gaborage/go-bricks/internal/clienttls"
	"github.com/gaborage/go-bricks/internal/redisrules"
)

// Connection modes selecting which protocol the client speaks.
const (
	// ModeStandalone dials one server and speaks the single-node protocol. It is
	// the default, and the empty Mode means exactly this.
	ModeStandalone = redisrules.ModeStandalone

	// ModeCluster speaks the cluster protocol against the single configured
	// address, which the client treats as a seed and follows the slot map from.
	// Required by endpoints that answer MOVED to a single-node client, such as
	// Amazon ElastiCache Serverless.
	ModeCluster = redisrules.ModeCluster
)

// Config holds Redis-specific configuration options. Nothing decodes it: the app
// layer fills it by hand from the resolved config.CacheConfig, so it carries no
// injection tags at all and the operator-facing keys live on config.RedisConfig,
// whose koanf tags are the spelling every error below is addressed in (#1729).
type Config struct {
	// Host is the Redis server hostname or IP address. Required.
	Host string

	// Port is the Redis server port (default: 6379).
	Port int

	// Mode selects the protocol the client speaks: ModeStandalone (the default,
	// and what the empty string means) or ModeCluster. Cluster is required for a
	// cluster-protocol endpoint such as Amazon ElastiCache Serverless, which
	// answers MOVED to a single-node client. Under cluster, Database must be 0 —
	// the cluster client has no database selection. Filled from
	// config.RedisConfig, which owns the cache.redis.mode key (env
	// CACHE_REDIS_MODE).
	Mode string

	// Username is the Redis ACL user to authenticate as, sent as
	// AUTH <username> <password>. Empty authenticates as the implicit "default"
	// user. Requires Password — Validate refuses a name with an empty password,
	// because the driver then sends no AUTH and the dial would run as the default
	// user. Required by deployments that gate access with ACLs, such as Amazon
	// ElastiCache RBAC. Filled from config.RedisConfig, which owns the
	// cache.redis.username key (env CACHE_REDIS_USERNAME).
	Username string

	// Password for Redis authentication (optional).
	// Should be provided via environment variable: CACHE_REDIS_PASSWORD
	Password string

	// Database number to use (default: 0).
	// Redis supports databases 0-15 by default.
	Database int

	// PoolSize is the maximum number of socket connections (default: 10).
	// Higher values allow more concurrent operations but consume more resources.
	PoolSize int

	// DialTimeout is the timeout for establishing new connections (default: 5s).
	DialTimeout time.Duration

	// LoadTimeout bounds each cache leg of cache.LoadThrough (cache.loadtimeout,
	// default: 500ms). Zero leaves the helper on its own fallback; a deployment-resolved
	// config always carries a positive value.
	LoadTimeout time.Duration

	// ReadTimeout is the timeout for socket reads (default: 3s).
	// -1 disables timeout.
	ReadTimeout time.Duration

	// WriteTimeout is the timeout for socket writes (default: 3s).
	// -1 disables timeout.
	WriteTimeout time.Duration

	// MaxRetries is the maximum number of retries before giving up (default: 3).
	// -1 disables retries.
	MaxRetries int

	// MinRetryBackoff is the minimum backoff between retries (default: 8ms).
	MinRetryBackoff time.Duration

	// MaxRetryBackoff is the maximum backoff between retries (default: 512ms).
	MaxRetryBackoff time.Duration

	// TLS configures the client-side TLS of the connection. Zero value =
	// plaintext.
	TLS TLSConfig
}

// TLSConfig enables TLS on the Redis connection. Each PEM piece comes from a
// file path (*File) or a base64-encoded PEM string (*Value) — at most one
// source per piece. An enabled block with no material at all verifies against
// the system roots; staged material under a disabled block is an error, not a
// warning, because a silently plaintext cache connection is the failure mode
// this config exists to prevent.
type TLSConfig struct {
	// Enabled turns TLS on. False with any other field set is refused.
	Enabled bool

	// CAFile and CAValue name the root bundle that verifies the server.
	CAFile  string
	CAValue string

	// CertFile and CertValue name the client certificate; a cert requires a key.
	CertFile  string
	CertValue string

	// KeyFile and KeyValue name the client key; a key requires a cert.
	KeyFile  string
	KeyValue string

	// ServerName overrides the SNI/verification hostname; empty defaults to Host.
	ServerName string

	// MinVersion: "" or "1.2" (default floor) | "1.3".
	MinVersion string
}

// Validate performs fail-fast validation of Redis configuration.
// Returns error if configuration is invalid.
//
// One rule is a coupling rather than a range check: under ModeCluster a
// non-zero Database is refused, because go-redis drops UniversalOptions.DB when
// it builds the cluster client (UniversalOptions.Cluster copies no DB, and
// ClusterOptions has no such field). Accepting it would move a deployment's
// whole keyspace to database 0 on the mode flip alone, with nothing said.
func (c *Config) Validate() error {
	_, err := c.validate()
	return err
}

// validate is Validate plus the TLS material projection it built on the way,
// so a caller that needs both — NewClient — pays for the projection once. The
// endpoint rules come from redisrules, because a hand-built Config reaches this
// door without passing through config.
func (c *Config) validate() (clienttls.Material, error) {
	m := c.TLS.material()
	if v := redisrules.Check(&redisrules.Endpoint{
		Host:         c.Host,
		Mode:         c.Mode,
		Username:     c.Username,
		Password:     c.Password,
		Port:         c.Port,
		Database:     c.Database,
		PoolSize:     c.PoolSize,
		DialTimeout:  c.DialTimeout,
		ReadTimeout:  c.ReadTimeout,
		WriteTimeout: c.WriteTimeout,
		TLSEnabled:   c.TLS.Enabled,
		TLS:          m,
	}); v != nil {
		msg := v.Message
		if len(v.Allowed) != 0 {
			msg += " (must be one of: " + strings.Join(v.Allowed, ", ") + ")"
		}
		return clienttls.Material{}, cache.NewConfigError("redis."+v.Field, msg, nil)
	}

	// Zero stays valid: it means "unset", and LoadThrough then uses its own fallback. A
	// negative is rejected here because a hand-built Config never passes through the config
	// layer's cache.loadtimeout normalization, and LoadThrough treats a non-positive value
	// as "not configured" — so without this the operator's value would be silently ignored
	// rather than corrected or refused. The field carries no "redis." head on purpose: the
	// key is cache.loadtimeout, one level above the Redis sub-block.
	if c.LoadTimeout < 0 {
		return clienttls.Material{}, cache.NewConfigError("loadtimeout", "load timeout cannot be negative", nil)
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

// effectiveMode reports the mode the client dials with, resolving the empty
// Mode to ModeStandalone so a reader never has to decide what "" means.
func (c *Config) effectiveMode() string {
	return cmp.Or(c.Mode, ModeStandalone)
}

// Address returns the Redis server address in "host:port" format.
func (c *Config) Address() string {
	return fmt.Sprintf("%s:%d", c.Host, c.Port)
}
