package config

import (
	"fmt"
	"strings"

	"github.com/knadh/koanf/parsers/yaml"
	"github.com/knadh/koanf/providers/confmap"
	envprovider "github.com/knadh/koanf/providers/env"
	"github.com/knadh/koanf/providers/file"
	"github.com/knadh/koanf/v2"
)

// Load loads configuration from multiple sources with priority:
// 1. Environment variables (highest priority)
// 2. YAML configuration files
// 3. Default values (lowest priority)
func Load() (*Config, error) {
	k := koanf.New(".")

	// Load default configuration first
	if err := loadDefaults(k); err != nil {
		return nil, fmt.Errorf("failed to load defaults: %w", err)
	}

	// Load from YAML file (if exists)
	if err := k.Load(file.Provider("config.yaml"), yaml.Parser()); err != nil {
		// YAML file is optional, log but don't fail
		fmt.Printf("Warning: could not load config.yaml: %v\n", err)
	}

	// Load environment-specific YAML (if exists)
	env := k.String("app.env")
	if env != "" {
		envFile := fmt.Sprintf("config.%s.yaml", env)
		if err := k.Load(file.Provider(envFile), yaml.Parser()); err != nil {
			fmt.Printf("Warning: could not load %s: %v\n", envFile, err)
		}
	}

	// Load environment variables (highest priority)
	if err := k.Load(envprovider.Provider("", ".", func(s string) string {
		// Convert UPPER_CASE to lower.case for koanf
		return strings.ReplaceAll(strings.ToLower(s), "_", ".")
	}), nil); err != nil {
		return nil, fmt.Errorf("failed to load environment variables: %w", err)
	}

	// Unmarshal into config struct
	var cfg Config
	if err := k.Unmarshal("", &cfg); err != nil {
		return nil, fmt.Errorf("failed to unmarshal config: %w", err)
	}

	// Validate configuration
	if err := Validate(&cfg); err != nil {
		return nil, fmt.Errorf("invalid configuration: %w", err)
	}

	return &cfg, nil
}

func loadDefaults(k *koanf.Koanf) error {
	defaults := map[string]interface{}{
		"app.name":       "nova-service",
		"app.version":    "v1.0.0",
		"app.env":        EnvDevelopment,
		"app.debug":      false,
		"app.rate_limit": 100,
		"app.namespace":  "default",

		"server.host":               "0.0.0.0",
		"server.port":               8080,
		"server.read_timeout":       "15s",
		"server.write_timeout":      "30s",
		"server.middleware_timeout": "5s",
		"server.shutdown_timeout":   "10s",

		"database.type":               "postgresql",
		"database.host":               "localhost",
		"database.port":               5432,
		"database.ssl_mode":           "disable",
		"database.max_conns":          25,
		"database.max_idle_conns":     5,
		"database.conn_max_lifetime":  "5m",
		"database.conn_max_idle_time": "5m",

		"log.level":  "info",
		"log.pretty": false,
	}

	return k.Load(confmap.Provider(defaults, "."), nil)
}
