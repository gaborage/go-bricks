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
	Name      string `koanf:"name"`
	Version   string `koanf:"version"`
	Env       string `koanf:"env"`
	Debug     bool   `koanf:"debug"`
	RateLimit int    `koanf:"rate_limit"`
	Namespace string `koanf:"namespace"`
}

type ServerConfig struct {
	Host              string        `koanf:"host"`
	Port              int           `koanf:"port"`
	ReadTimeout       time.Duration `koanf:"read_timeout"`
	WriteTimeout      time.Duration `koanf:"write_timeout"`
	MiddlewareTimeout time.Duration `koanf:"middleware_timeout"`
	ShutdownTimeout   time.Duration `koanf:"shutdown_timeout"`
	BasePath          string        `koanf:"base_path"`    // Base path for all routes, should start with "/"
	HealthRoute       string        `koanf:"health_route"` // e.g. "/health"
	ReadyRoute        string        `koanf:"ready_route"`  // e.g. "/ready"
}

type DatabaseConfig struct {
	Type            string        `koanf:"type"` // "postgresql" or "oracle"
	Host            string        `koanf:"host"`
	Port            int           `koanf:"port"`
	Database        string        `koanf:"database"`
	Username        string        `koanf:"username"`
	Password        string        `koanf:"password"`
	SSLMode         string        `koanf:"ssl_mode"`
	MaxConns        int32         `koanf:"max_conns"`
	MaxIdleConns    int32         `koanf:"max_idle_conns"`
	ConnMaxLifetime time.Duration `koanf:"conn_max_lifetime"`
	ConnMaxIdleTime time.Duration `koanf:"conn_max_idle_time"`

	// Oracle-specific settings
	ServiceName string `koanf:"service_name"` // Oracle service name
	SID         string `koanf:"sid"`          // Oracle SID

	// Connection string override (if needed)
	ConnectionString string `koanf:"connection_string"`
}

type LogConfig struct {
	Level  string `koanf:"level"`
	Pretty bool   `koanf:"pretty"`
}

type MessagingConfig struct {
	BrokerURL   string            `koanf:"broker_url"`
	Exchange    string            `koanf:"exchange"`
	RoutingKey  string            `koanf:"routing_key"`
	VirtualHost string            `koanf:"virtual_host"`
	Headers     map[string]string `koanf:"headers"`
}
