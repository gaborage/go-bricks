package config

import (
	"time"

	"github.com/knadh/koanf/v2"
)

// Config represents the overall application configuration structure.
// It includes sections for application settings, server parameters,
// database connection details, logging preferences, and messaging options.
// The embedded koanf.Koanf instance allows for flexible access to
// additional custom configurations not explicitly defined in the struct.
type Config struct {
	App         AppConfig         `koanf:"app" json:"app" yaml:"app" toml:"app" mapstructure:"app"`
	Server      ServerConfig      `koanf:"server" json:"server" yaml:"server" toml:"server" mapstructure:"server"`
	Database    DatabaseConfig    `koanf:"database" json:"database" yaml:"database" toml:"database" mapstructure:"database"`
	Log         LogConfig         `koanf:"log" json:"log" yaml:"log" toml:"log" mapstructure:"log"`
	Messaging   MessagingConfig   `koanf:"messaging" json:"messaging" yaml:"messaging" toml:"messaging" mapstructure:"messaging"`
	Multitenant MultitenantConfig `koanf:"multitenant" json:"multitenant" yaml:"multitenant" toml:"multitenant" mapstructure:"multitenant"`
	Debug       DebugConfig       `koanf:"debug" json:"debug" yaml:"debug" toml:"debug" mapstructure:"debug"`
	Source      SourceConfig      `koanf:"source" json:"source" yaml:"source" toml:"source" mapstructure:"source"`
	Scheduler   SchedulerConfig   `koanf:"scheduler" json:"scheduler" yaml:"scheduler" toml:"scheduler" mapstructure:"scheduler"`

	// k holds the underlying Koanf instance for flexible access to custom configurations
	k *koanf.Koanf `json:"-" yaml:"-" toml:"-" mapstructure:"-"`
}

// AppConfig holds general application settings.
type AppConfig struct {
	Name      string        `koanf:"name" json:"name" yaml:"name" toml:"name" mapstructure:"name"`
	Version   string        `koanf:"version" json:"version" yaml:"version" toml:"version" mapstructure:"version"`
	Env       string        `koanf:"env" json:"env" yaml:"env" toml:"env" mapstructure:"env"`
	Debug     bool          `koanf:"debug" json:"debug" yaml:"debug" toml:"debug" mapstructure:"debug"`
	Namespace string        `koanf:"namespace" json:"namespace" yaml:"namespace" toml:"namespace" mapstructure:"namespace"`
	Rate      RateConfig    `koanf:"rate" json:"rate" yaml:"rate" toml:"rate" mapstructure:"rate"`
	Startup   StartupConfig `koanf:"startup" json:"startup" yaml:"startup" toml:"startup" mapstructure:"startup"`
}

// StartupConfig holds application startup settings.
type StartupConfig struct {
	Timeout time.Duration `koanf:"timeout" json:"timeout" yaml:"timeout" toml:"timeout" mapstructure:"timeout"`
}

// RateConfig holds rate limiting settings.
type RateConfig struct {
	Limit      int              `koanf:"limit" json:"limit" yaml:"limit" toml:"limit" mapstructure:"limit"`
	Burst      int              `koanf:"burst" json:"burst" yaml:"burst" toml:"burst" mapstructure:"burst"`
	IPPreGuard IPPreGuardConfig `koanf:"ippreguard" json:"ippreguard" yaml:"ippreguard" toml:"ippreguard" mapstructure:"ippreguard"`
}

// IPPreGuardConfig holds IP pre-guard rate limiting settings.
type IPPreGuardConfig struct {
	Enabled   bool `koanf:"enabled" json:"enabled" yaml:"enabled" toml:"enabled" mapstructure:"enabled"`           // enable IP pre-guard rate limiting
	Threshold int  `koanf:"threshold" json:"threshold" yaml:"threshold" toml:"threshold" mapstructure:"threshold"` // requests per second limit per IP
}

// ServerConfig holds HTTP server settings.
type ServerConfig struct {
	Host    string        `koanf:"host" json:"host" yaml:"host" toml:"host" mapstructure:"host"`
	Port    int           `koanf:"port" json:"port" yaml:"port" toml:"port" mapstructure:"port"`
	Timeout TimeoutConfig `koanf:"timeout" json:"timeout" yaml:"timeout" toml:"timeout" mapstructure:"timeout"`
	Path    PathConfig    `koanf:"path" json:"path" yaml:"path" toml:"path" mapstructure:"path"`
}

// TimeoutConfig holds various timeout durations for the server.
type TimeoutConfig struct {
	Read       time.Duration `koanf:"read" json:"read" yaml:"read" toml:"read" mapstructure:"read"`
	Write      time.Duration `koanf:"write" json:"write" yaml:"write" toml:"write" mapstructure:"write"`
	Idle       time.Duration `koanf:"idle" json:"idle" yaml:"idle" toml:"idle" mapstructure:"idle"`
	Middleware time.Duration `koanf:"middleware" json:"middleware" yaml:"middleware" toml:"middleware" mapstructure:"middleware"`
	Shutdown   time.Duration `koanf:"shutdown" json:"shutdown" yaml:"shutdown" toml:"shutdown" mapstructure:"shutdown"`
}

// PathConfig holds URL path settings for the server.
type PathConfig struct {
	Base   string `koanf:"base" json:"base" yaml:"base" toml:"base" mapstructure:"base"`
	Health string `koanf:"health" json:"health" yaml:"health" toml:"health" mapstructure:"health"`
	Ready  string `koanf:"ready" json:"ready" yaml:"ready" toml:"ready" mapstructure:"ready"`
}

// DatabaseConfig holds database connection settings.
type DatabaseConfig struct {
	Type     string `koanf:"type" json:"type" yaml:"type" toml:"type" mapstructure:"type"`
	Host     string `koanf:"host" json:"host" yaml:"host" toml:"host" mapstructure:"host"`
	Port     int    `koanf:"port" json:"port" yaml:"port" toml:"port" mapstructure:"port"`
	Database string `koanf:"database" json:"database" yaml:"database" toml:"database" mapstructure:"database"`
	Username string `koanf:"username" json:"username" yaml:"username" toml:"username" mapstructure:"username"`
	Password string `koanf:"password" json:"password" yaml:"password" toml:"password" mapstructure:"password"`

	ConnectionString string `koanf:"connectionstring" json:"connectionstring" yaml:"connectionstring" toml:"connectionstring" mapstructure:"connectionstring"`

	Pool  PoolConfig  `koanf:"pool" json:"pool" yaml:"pool" toml:"pool" mapstructure:"pool"`
	Query QueryConfig `koanf:"query" json:"query" yaml:"query" toml:"query" mapstructure:"query"`
	TLS   TLSConfig   `koanf:"tls" json:"tls" yaml:"tls" toml:"tls" mapstructure:"tls"`

	PostgreSQL PostgreSQLConfig `koanf:"postgresql" json:"postgresql" yaml:"postgresql" toml:"postgresql" mapstructure:"postgresql"`
	Oracle     OracleConfig     `koanf:"oracle" json:"oracle" yaml:"oracle" toml:"oracle" mapstructure:"oracle"`
	Mongo      MongoConfig      `koanf:"mongo" json:"mongo" yaml:"mongo" toml:"mongo" mapstructure:"mongo"`
}

// PoolConfig holds connection pool settings.
type PoolConfig struct {
	Max      PoolMaxConfig  `koanf:"max" json:"max" yaml:"max" toml:"max" mapstructure:"max"`
	Idle     PoolIdleConfig `koanf:"idle" json:"idle" yaml:"idle" toml:"idle" mapstructure:"idle"`
	Lifetime LifetimeConfig `koanf:"lifetime" json:"lifetime" yaml:"lifetime" toml:"lifetime" mapstructure:"lifetime"`
}

// PoolMaxConfig holds maximum connections settings.
type PoolMaxConfig struct {
	Connections int32 `koanf:"connections" json:"connections" yaml:"connections" toml:"connections" mapstructure:"connections"`
}

// PoolIdleConfig holds idle connections settings.
type PoolIdleConfig struct {
	Connections int32         `koanf:"connections" json:"connections" yaml:"connections" toml:"connections" mapstructure:"connections"`
	Time        time.Duration `koanf:"time" json:"time" yaml:"time" toml:"time" mapstructure:"time"`
}

// LifetimeConfig holds maximum lifetime settings for connections.
type LifetimeConfig struct {
	Max time.Duration `koanf:"max" json:"max" yaml:"max" toml:"max" mapstructure:"max"`
}

// QueryConfig holds settings related to query logging and slow query detection.
type QueryConfig struct {
	Slow SlowQueryConfig `koanf:"slow" json:"slow" yaml:"slow" toml:"slow" mapstructure:"slow"`
	Log  QueryLogConfig  `koanf:"log" json:"log" yaml:"log" toml:"log" mapstructure:"log"`
}

// SlowQueryConfig holds settings for slow query detection.
type SlowQueryConfig struct {
	Threshold time.Duration `koanf:"threshold" json:"threshold" yaml:"threshold" toml:"threshold" mapstructure:"threshold"`
	Enabled   bool          `koanf:"enabled" json:"enabled" yaml:"enabled" toml:"enabled" mapstructure:"enabled"`
}

// QueryLogConfig holds settings for query logging.
type QueryLogConfig struct {
	Parameters bool `koanf:"parameters" json:"parameters" yaml:"parameters" toml:"parameters" mapstructure:"parameters"`
	MaxLength  int  `koanf:"max" json:"max" yaml:"max" toml:"max" mapstructure:"max"`
}

// TLSConfig holds TLS/SSL settings for database connections.
type TLSConfig struct {
	Mode     string `koanf:"mode" json:"mode" yaml:"mode" toml:"mode" mapstructure:"mode"`
	CertFile string `koanf:"cert" json:"cert" yaml:"cert" toml:"cert" mapstructure:"cert"`
	KeyFile  string `koanf:"key" json:"key" yaml:"key" toml:"key" mapstructure:"key"`
	CAFile   string `koanf:"ca" json:"ca" yaml:"ca" toml:"ca" mapstructure:"ca"`
}

// PostgreSQLConfig holds PostgreSQL-specific database settings.
type PostgreSQLConfig struct {
	Schema string `koanf:"schema" json:"schema" yaml:"schema" toml:"schema" mapstructure:"schema"`
}

// OracleConfig holds Oracle-specific database settings.
type OracleConfig struct {
	Service ServiceConfig `koanf:"service" json:"service" yaml:"service" toml:"service" mapstructure:"service"`
}

// ServiceConfig holds Oracle service connection settings.
type ServiceConfig struct {
	Name string `koanf:"name" json:"name" yaml:"name" toml:"name" mapstructure:"name"`
	SID  string `koanf:"sid" json:"sid" yaml:"sid" toml:"sid" mapstructure:"sid"`
}

// MongoConfig holds MongoDB-specific database settings.
type MongoConfig struct {
	Replica ReplicaConfig `koanf:"replica" json:"replica" yaml:"replica" toml:"replica" mapstructure:"replica"`
	Auth    AuthConfig    `koanf:"auth" json:"auth" yaml:"auth" toml:"auth" mapstructure:"auth"`
	Concern ConcernConfig `koanf:"concern" json:"concern" yaml:"concern" toml:"concern" mapstructure:"concern"`
}

// ReplicaConfig holds MongoDB replica set and read preference settings.
type ReplicaConfig struct {
	Set        string `koanf:"set" json:"set" yaml:"set" toml:"set" mapstructure:"set"`
	Preference string `koanf:"preference" json:"preference" yaml:"preference" toml:"preference" mapstructure:"preference"`
}

// AuthConfig holds MongoDB authentication source settings.
type AuthConfig struct {
	Source string `koanf:"source" json:"source" yaml:"source" toml:"source" mapstructure:"source"`
}

// ConcernConfig holds MongoDB write concern settings.
type ConcernConfig struct {
	Write string `koanf:"write" json:"write" yaml:"write" toml:"write" mapstructure:"write"`
}

// LogConfig holds logging settings.
type LogConfig struct {
	Level  string       `koanf:"level" json:"level" yaml:"level" toml:"level" mapstructure:"level"`
	Pretty bool         `koanf:"pretty" json:"pretty" yaml:"pretty" toml:"pretty" mapstructure:"pretty"`
	Output OutputConfig `koanf:"output" json:"output" yaml:"output" toml:"output" mapstructure:"output"`
}

// OutputConfig holds log output settings.
type OutputConfig struct {
	Format string `koanf:"format" json:"format" yaml:"format" toml:"format" mapstructure:"format"`
	File   string `koanf:"file" json:"file" yaml:"file" toml:"file" mapstructure:"file"`
}

// MessagingConfig holds messaging/broker settings.
type MessagingConfig struct {
	Broker  BrokerConfig      `koanf:"broker" json:"broker" yaml:"broker" toml:"broker" mapstructure:"broker"`
	Routing RoutingConfig     `koanf:"routing" json:"routing" yaml:"routing" toml:"routing" mapstructure:"routing"`
	Headers map[string]string `koanf:"headers" json:"headers" yaml:"headers" toml:"headers" mapstructure:"headers"`
}

// BrokerConfig holds message broker connection settings.
type BrokerConfig struct {
	URL         string `koanf:"url" json:"url" yaml:"url" toml:"url" mapstructure:"url"`
	VirtualHost string `koanf:"virtualhost" json:"virtualhost" yaml:"virtualhost" toml:"virtualhost" mapstructure:"virtualhost"`
}

// RoutingConfig holds message routing settings.
type RoutingConfig struct {
	Exchange string `koanf:"exchange" json:"exchange" yaml:"exchange" toml:"exchange" mapstructure:"exchange"`
	Key      string `koanf:"key" json:"key" yaml:"key" toml:"key" mapstructure:"key"`
}

// MultitenantConfig holds multi-tenant specific settings.
type MultitenantConfig struct {
	Enabled  bool                   `koanf:"enabled" json:"enabled" yaml:"enabled" toml:"enabled" mapstructure:"enabled"`
	Resolver ResolverConfig         `koanf:"resolver" json:"resolver" yaml:"resolver" toml:"resolver" mapstructure:"resolver"`
	Limits   LimitsConfig           `koanf:"limits" json:"limits" yaml:"limits" toml:"limits" mapstructure:"limits"`
	Tenants  map[string]TenantEntry `koanf:"tenants" json:"tenants" yaml:"tenants" toml:"tenants" mapstructure:"tenants"`
}

// TenantEntry represents a single tenant's resource configuration
type TenantEntry struct {
	Database  DatabaseConfig        `koanf:"database" json:"database" yaml:"database" toml:"database" mapstructure:"database"`
	Messaging TenantMessagingConfig `koanf:"messaging" json:"messaging" yaml:"messaging" toml:"messaging" mapstructure:"messaging"`
}

// TenantMessagingConfig holds messaging configuration for a tenant
type TenantMessagingConfig struct {
	URL string `koanf:"url" json:"url" yaml:"url" toml:"url" mapstructure:"url"`
}

// ResolverConfig holds tenant resolution strategy settings.
type ResolverConfig struct {
	Type    string `koanf:"type" json:"type" yaml:"type" toml:"type" mapstructure:"type"`                // header, subdomain, composite
	Header  string `koanf:"header" json:"header" yaml:"header" toml:"header" mapstructure:"header"`      // default: X-Tenant-ID
	Domain  string `koanf:"domain" json:"domain" yaml:"domain" toml:"domain" mapstructure:"domain"`      // e.g., api.example.com or .api.example.com (leading dot optional)
	Proxies bool   `koanf:"proxies" json:"proxies" yaml:"proxies" toml:"proxies" mapstructure:"proxies"` // trust X-Forwarded-Host
}

// LimitsConfig holds resource limits for multi-tenant operation.
type LimitsConfig struct {
	Tenants int `koanf:"tenants" json:"tenants" yaml:"tenants" toml:"tenants" mapstructure:"tenants"`
}

// DebugConfig holds debug endpoint settings.
type DebugConfig struct {
	Enabled     bool                 `koanf:"enabled" json:"enabled" yaml:"enabled" toml:"enabled" mapstructure:"enabled"`                     // Enable debug endpoints
	PathPrefix  string               `koanf:"pathprefix" json:"pathprefix" yaml:"pathprefix" toml:"pathprefix" mapstructure:"pathprefix"`      // URL path prefix for debug endpoints
	AllowedIPs  []string             `koanf:"allowedips" json:"allowedips" yaml:"allowedips" toml:"allowedips" mapstructure:"allowedips"`      // List of allowed IP addresses/CIDRs
	BearerToken string               `koanf:"bearertoken" json:"bearertoken" yaml:"bearertoken" toml:"bearertoken" mapstructure:"bearertoken"` // Optional bearer token for authentication
	Endpoints   DebugEndpointsConfig `koanf:"endpoints" json:"endpoints" yaml:"endpoints" toml:"endpoints" mapstructure:"endpoints"`           // Individual endpoint settings
}

// DebugEndpointsConfig holds settings for individual debug endpoints.
type DebugEndpointsConfig struct {
	Goroutines bool `koanf:"goroutines" json:"goroutines" yaml:"goroutines" toml:"goroutines" mapstructure:"goroutines"` // Enable goroutine analysis endpoint
	GC         bool `koanf:"gc" json:"gc" yaml:"gc" toml:"gc" mapstructure:"gc"`                                         // Enable garbage collection endpoints
	Health     bool `koanf:"health" json:"health" yaml:"health" toml:"health" mapstructure:"health"`                     // Enable enhanced health endpoint
	Info       bool `koanf:"info" json:"info" yaml:"info" toml:"info" mapstructure:"info"`                               // Enable system info endpoint
}

// Source type constants
const (
	SourceTypeStatic  = "static"
	SourceTypeDynamic = "dynamic"
)

// SourceConfig controls how tenant configuration is loaded.
type SourceConfig struct {
	Type string `koanf:"type" json:"type" yaml:"type" toml:"type" mapstructure:"type"` // SourceTypeStatic for YAML config, SourceTypeDynamic for external stores
}

// SchedulerConfig holds job scheduler settings.
type SchedulerConfig struct {
	Security SchedulerSecurityConfig `koanf:"security" json:"security" yaml:"security" toml:"security" mapstructure:"security"`
	Timeout  SchedulerTimeoutConfig  `koanf:"timeout" json:"timeout" yaml:"timeout" toml:"timeout" mapstructure:"timeout"`
}

// SchedulerSecurityConfig holds security settings for scheduler system APIs.
type SchedulerSecurityConfig struct {
	// CIDRAllowlist holds CIDR ranges allowed to access /_sys/job* endpoints.
	// Empty list = localhost-only access (127.0.0.1, ::1).
	// Non-empty list = restrict to matching IP ranges only.
	CIDRAllowlist []string `koanf:"cidrallowlist" json:"cidrallowlist" yaml:"cidrallowlist" toml:"cidrallowlist" mapstructure:"cidrallowlist"`

	// TrustedProxies holds CIDR ranges of trusted reverse proxies.
	// X-Forwarded-For and X-Real-IP headers are ONLY honored if the immediate peer
	// matches one of these CIDR ranges. Empty list = do not trust any proxy headers.
	TrustedProxies []string `koanf:"trustedproxies" json:"trustedproxies" yaml:"trustedproxies" toml:"trustedproxies" mapstructure:"trustedproxies"`
}

// SchedulerTimeoutConfig holds timeout and threshold settings for scheduler operations.
type SchedulerTimeoutConfig struct {
	// Shutdown is the graceful shutdown timeout for in-flight jobs.
	// Default: 30s.
	Shutdown time.Duration `koanf:"shutdown" json:"shutdown" yaml:"shutdown" toml:"shutdown" mapstructure:"shutdown"`

	// SlowJob is the execution duration threshold for marking jobs as slow.
	// Jobs exceeding this duration are logged with result_code="WARN" even if successful.
	// Zero or negative = disabled. Default: 30s.
	SlowJob time.Duration `koanf:"slowjob" json:"slowjob" yaml:"slowjob" toml:"slowjob" mapstructure:"slowjob"`
}
