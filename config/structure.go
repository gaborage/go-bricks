package config

import (
	"regexp"
	"time"

	"github.com/knadh/koanf/v2"
)

// Config represents the overall application configuration structure.
// It includes sections for application settings, server parameters,
// database connection details, logging preferences, and messaging options.
// The embedded koanf.Koanf instance allows for flexible access to
// additional custom configurations not explicitly defined in the struct.
type Config struct {
	App         AppConfig         `koanf:"app"`
	Server      ServerConfig      `koanf:"server"`
	Database    DatabaseConfig    `koanf:"database"`
	Log         LogConfig         `koanf:"log"`
	Messaging   MessagingConfig   `koanf:"messaging"`
	Multitenant MultitenantConfig `koanf:"multitenant"`

	// k holds the underlying Koanf instance for flexible access to custom configurations
	k *koanf.Koanf `json:"-" yaml:"-" toml:"-" mapstructure:"-"`
}

// AppConfig holds general application settings.
type AppConfig struct {
	Name      string     `koanf:"name"`
	Version   string     `koanf:"version"`
	Env       string     `koanf:"env"`
	Debug     bool       `koanf:"debug"`
	Namespace string     `koanf:"namespace"`
	Rate      RateConfig `koanf:"rate"`
}

// RateConfig holds rate limiting settings.
type RateConfig struct {
	Limit int `koanf:"limit"`
	Burst int `koanf:"burst"`
}

// ServerConfig holds HTTP server settings.
type ServerConfig struct {
	Host    string        `koanf:"host"`
	Port    int           `koanf:"port"`
	Timeout TimeoutConfig `koanf:"timeout"`
	Path    PathConfig    `koanf:"path"`
}

// TimeoutConfig holds various timeout durations for the server.
type TimeoutConfig struct {
	Read       time.Duration `koanf:"read"`
	Write      time.Duration `koanf:"write"`
	Idle       time.Duration `koanf:"idle"`
	Middleware time.Duration `koanf:"middleware"`
	Shutdown   time.Duration `koanf:"shutdown"`
}

// PathConfig holds URL path settings for the server.
type PathConfig struct {
	Base   string `koanf:"base"`
	Health string `koanf:"health"`
	Ready  string `koanf:"ready"`
}

// DatabaseConfig holds database connection settings.
type DatabaseConfig struct {
	Type     string `koanf:"type"`
	Host     string `koanf:"host"`
	Port     int    `koanf:"port"`
	Database string `koanf:"database"`
	Username string `koanf:"username"`
	Password string `koanf:"password"`

	ConnectionString string `koanf:"connectionstring"`

	Pool  PoolConfig  `koanf:"pool"`
	Query QueryConfig `koanf:"query"`
	TLS   TLSConfig   `koanf:"tls"`

	Oracle OracleConfig `koanf:"oracle"`
	Mongo  MongoConfig  `koanf:"mongo"`
}

// PoolConfig holds connection pool settings.
type PoolConfig struct {
	Max      PoolMaxConfig  `koanf:"max"`
	Idle     PoolIdleConfig `koanf:"idle"`
	Lifetime LifetimeConfig `koanf:"lifetime"`
}

// PoolMaxConfig holds maximum connections settings.
type PoolMaxConfig struct {
	Connections int32 `koanf:"connections"`
}

// PoolIdleConfig holds idle connections settings.
type PoolIdleConfig struct {
	Connections int32         `koanf:"connections"`
	Time        time.Duration `koanf:"time"`
}

// LifetimeConfig holds maximum lifetime settings for connections.
type LifetimeConfig struct {
	Max time.Duration `koanf:"max"`
}

// QueryConfig holds settings related to query logging and slow query detection.
type QueryConfig struct {
	Slow SlowQueryConfig `koanf:"slow"`
	Log  QueryLogConfig  `koanf:"log"`
}

// SlowQueryConfig holds settings for slow query detection.
type SlowQueryConfig struct {
	Threshold time.Duration `koanf:"threshold"`
	Enabled   bool          `koanf:"enabled"`
}

// QueryLogConfig holds settings for query logging.
type QueryLogConfig struct {
	Parameters bool `koanf:"parameters"`
	MaxLength  int  `koanf:"maxlength"`
}

// TLSConfig holds TLS/SSL settings for database connections.
type TLSConfig struct {
	Mode     string `koanf:"mode"`
	CertFile string `koanf:"cert"`
	KeyFile  string `koanf:"key"`
	CAFile   string `koanf:"ca"`
}

// OracleConfig holds Oracle-specific database settings.
type OracleConfig struct {
	Service ServiceConfig `koanf:"service"`
}

// ServiceConfig holds Oracle service connection settings.
type ServiceConfig struct {
	Name string `koanf:"name"`
	SID  string `koanf:"sid"`
}

// MongoConfig holds MongoDB-specific database settings.
type MongoConfig struct {
	Replica ReplicaConfig `koanf:"replica"`
	Auth    AuthConfig    `koanf:"auth"`
	Concern ConcernConfig `koanf:"concern"`
}

// ReplicaConfig holds MongoDB replica set and read preference settings.
type ReplicaConfig struct {
	Set            string `koanf:"set"`
	ReadPreference string `koanf:"readpreference"`
}

// AuthConfig holds MongoDB authentication source settings.
type AuthConfig struct {
	Source string `koanf:"source"`
}

// ConcernConfig holds MongoDB write concern settings.
type ConcernConfig struct {
	Write string `koanf:"write"`
}

// LogConfig holds logging settings.
type LogConfig struct {
	Level  string       `koanf:"level"`
	Pretty bool         `koanf:"pretty"`
	Output OutputConfig `koanf:"output"`
}

// OutputConfig holds log output settings.
type OutputConfig struct {
	Format string `koanf:"format"`
	File   string `koanf:"file"`
}

// MessagingConfig holds messaging/broker settings.
type MessagingConfig struct {
	Broker  BrokerConfig      `koanf:"broker"`
	Routing RoutingConfig     `koanf:"routing"`
	Headers map[string]string `koanf:"headers"`
}

// BrokerConfig holds message broker connection settings.
type BrokerConfig struct {
	URL         string `koanf:"url"`
	VirtualHost string `koanf:"virtualhost"`
}

// RoutingConfig holds message routing settings.
type RoutingConfig struct {
	Exchange string `koanf:"exchange"`
	Key      string `koanf:"key"`
}

// MultitenantConfig holds multi-tenant specific settings.
type MultitenantConfig struct {
	Enabled  bool                      `koanf:"enabled"`
	Resolver MultitenantResolverConfig `koanf:"resolver"`
	Cache    MultitenantCacheConfig    `koanf:"cache"`
	Limits   MultitenantLimitsConfig   `koanf:"limits"`
	TenantID MultitenantTenantIDConfig `koanf:"tenantid"`
}

// MultitenantResolverConfig holds tenant resolution strategy settings.
type MultitenantResolverConfig struct {
	Type         string `koanf:"type"`         // header, subdomain, composite
	HeaderName   string `koanf:"headername"`   // default: X-Tenant-ID
	RootDomain   string `koanf:"rootdomain"`   // e.g., .api.example.com
	TrustProxies bool   `koanf:"trustproxies"` // trust X-Forwarded-Host
}

// MultitenantCacheConfig holds caching settings for tenant configurations.
type MultitenantCacheConfig struct {
	TTL time.Duration `koanf:"ttl"` // default: 5m
}

// MultitenantLimitsConfig holds resource limits for multi-tenant operation.
type MultitenantLimitsConfig struct {
	MaxActiveTenants int `koanf:"maxactivetenants"` // default: 100
}

// MultitenantTenantIDConfig holds tenant ID validation settings.
type MultitenantTenantIDConfig struct {
	Pattern string `koanf:"pattern"` // default: ^[a-z0-9-]{1,64}$
	regex   *regexp.Regexp
}

// GetRegex returns the compiled regex for tenant ID validation.
func (c *MultitenantTenantIDConfig) GetRegex() *regexp.Regexp {
	return c.regex
}

// SetRegex compiles and sets the regex pattern.
func (c *MultitenantTenantIDConfig) SetRegex(pattern string) error {
	if pattern == "" {
		pattern = `^[a-z0-9-]{1,64}$`
	}
	regex, err := regexp.Compile(pattern)
	if err != nil {
		return err
	}
	c.regex = regex
	c.Pattern = pattern
	return nil
}
