package config

import (
	"time"

	"github.com/knadh/koanf/v2"
)

type Config struct {
	App       AppConfig       `koanf:"app"`
	Server    ServerConfig    `koanf:"server"`
	Database  DatabaseConfig  `koanf:"database"`
	Log       LogConfig       `koanf:"log"`
	Messaging MessagingConfig `koanf:"messaging"`

	// k holds the underlying Koanf instance for flexible access to custom configurations
	k *koanf.Koanf `json:"-" yaml:"-" toml:"-" mapstructure:"-"`
}

type AppConfig struct {
	Name      string     `koanf:"name"`
	Version   string     `koanf:"version"`
	Env       string     `koanf:"env"`
	Debug     bool       `koanf:"debug"`
	Namespace string     `koanf:"namespace"`
	Rate      RateConfig `koanf:"rate"`
}

type RateConfig struct {
	Limit int `koanf:"limit"`
	Burst int `koanf:"burst"`
}

type ServerConfig struct {
	Host    string        `koanf:"host"`
	Port    int           `koanf:"port"`
	Timeout TimeoutConfig `koanf:"timeout"`
	Path    PathConfig    `koanf:"path"`
}

type TimeoutConfig struct {
	Read       time.Duration `koanf:"read"`
	Write      time.Duration `koanf:"write"`
	Idle       time.Duration `koanf:"idle"`
	Middleware time.Duration `koanf:"middleware"`
	Shutdown   time.Duration `koanf:"shutdown"`
}

type PathConfig struct {
	Base   string `koanf:"base"`
	Health string `koanf:"health"`
	Ready  string `koanf:"ready"`
}

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

type PoolConfig struct {
	Max      PoolMaxConfig  `koanf:"max"`
	Idle     PoolIdleConfig `koanf:"idle"`
	Lifetime LifetimeConfig `koanf:"lifetime"`
}

type PoolMaxConfig struct {
	Connections int32 `koanf:"connections"`
}

type PoolIdleConfig struct {
	Connections int32         `koanf:"connections"`
	Time        time.Duration `koanf:"time"`
}

type LifetimeConfig struct {
	Max time.Duration `koanf:"max"`
}

type QueryConfig struct {
	Slow SlowQueryConfig `koanf:"slow"`
	Log  QueryLogConfig  `koanf:"log"`
}

type SlowQueryConfig struct {
	Threshold time.Duration `koanf:"threshold"`
	Enabled   bool          `koanf:"enabled"`
}

type QueryLogConfig struct {
	Parameters bool `koanf:"parameters"`
	MaxLength  int  `koanf:"maxlength"`
}

type TLSConfig struct {
	Mode     string `koanf:"mode"`
	CertFile string `koanf:"cert"`
	KeyFile  string `koanf:"key"`
	CAFile   string `koanf:"ca"`
}

type OracleConfig struct {
	Service ServiceConfig `koanf:"service"`
}

type ServiceConfig struct {
	Name string `koanf:"name"`
	SID  string `koanf:"sid"`
}

type MongoConfig struct {
	Replica ReplicaConfig `koanf:"replica"`
	Auth    AuthConfig    `koanf:"auth"`
	Concern ConcernConfig `koanf:"concern"`
}

type ReplicaConfig struct {
	Set            string `koanf:"set"`
	ReadPreference string `koanf:"readpreference"`
}

type AuthConfig struct {
	Source string `koanf:"source"`
}

type ConcernConfig struct {
	Write string `koanf:"write"`
}

type LogConfig struct {
	Level  string       `koanf:"level"`
	Pretty bool         `koanf:"pretty"`
	Output OutputConfig `koanf:"output"`
}

type OutputConfig struct {
	Format string `koanf:"format"`
	File   string `koanf:"file"`
}

type MessagingConfig struct {
	Broker  BrokerConfig      `koanf:"broker"`
	Routing RoutingConfig     `koanf:"routing"`
	Headers map[string]string `koanf:"headers"`
}

type BrokerConfig struct {
	URL         string `koanf:"url"`
	VirtualHost string `koanf:"virtualhost"`
}

type RoutingConfig struct {
	Exchange string `koanf:"exchange"`
	Key      string `koanf:"key"`
}
