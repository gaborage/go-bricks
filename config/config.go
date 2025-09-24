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

	// Store the Koanf instance for flexible access
	cfg.k = k

	// Validate configuration
	if err := Validate(&cfg); err != nil {
		return nil, fmt.Errorf("invalid configuration: %w", err)
	}

	return &cfg, nil
}

func loadDefaults(k *koanf.Koanf) error {
	defaults := map[string]any{
		"app.name":                              "gobricks-service",
		"app.version":                           "v1.0.0",
		"app.env":                               EnvDevelopment,
		"app.debug":                             false,
		"app.namespace":                         "default",
		"app.rate.limit":                        100,
		"app.rate.burst":                        200,
		"app.rate.ippreguard.enabled":           true,
		"app.rate.ippreguard.requestspersecond": 2000,

		"server.host":               "0.0.0.0",
		"server.port":               8080,
		"server.timeout.read":       "15s",
		"server.timeout.write":      "30s",
		"server.timeout.idle":       "60s",
		"server.timeout.middleware": "5s",
		"server.timeout.shutdown":   "10s",
		"server.path.base":          "",
		"server.path.health":        "/health",
		"server.path.ready":         "/ready",

		// Database defaults not provided for deterministic behavior
		// Database will only be enabled when explicitly configured

		"log.level":         "info",
		"log.pretty":        false,
		"log.output.format": "json",
		"log.output.file":   "",
	}

	return k.Load(confmap.Provider(defaults, "."), nil)
}
