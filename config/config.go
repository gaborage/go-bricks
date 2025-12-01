package config

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/knadh/koanf/parsers/yaml"
	"github.com/knadh/koanf/providers/confmap"
	envprovider "github.com/knadh/koanf/providers/env/v2"
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

	// Load from YAML file (if exists) - try both .yaml and .yml extensions
	if err := tryLoadYAMLFile(k, "config"); err != nil {
		return nil, err
	}

	// Load environment-specific YAML (if exists)
	env := k.String("app.env")
	if env != "" {
		envFile := fmt.Sprintf("config.%s", env)
		if err := tryLoadYAMLFile(k, envFile); err != nil {
			return nil, err
		}
	}

	// Load environment variables (highest priority)
	if err := k.Load(envprovider.Provider(".", envprovider.Opt{
		TransformFunc: func(k, v string) (string, any) {
			// Convert UPPER_CASE to lower.case for koanf
			k = strings.ReplaceAll(strings.ToLower(k), "_", ".")
			return k, v
		},
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

// tryLoadYAMLFile attempts to load a YAML configuration file with both .yaml and .yml extensions.
// It tries .yaml first, then falls back to .yml if .yaml is not found.
// Both extensions are optional - no error is returned if neither file exists.
// However, syntax errors, permission errors, and other I/O errors are propagated.
func tryLoadYAMLFile(k *koanf.Koanf, baseName string) error {
	// Try .yaml extension first
	yamlFile := baseName + ".yaml"
	if err := k.Load(file.Provider(yamlFile), yaml.Parser()); err != nil {
		// If file doesn't exist, try .yml fallback
		if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("failed to load %s: %w", yamlFile, err)
		}

		// Try .yml extension as fallback
		ymlFile := baseName + ".yml"
		if err := k.Load(file.Provider(ymlFile), yaml.Parser()); err != nil {
			// File not found is OK - config files are optional
			if !errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("failed to load %s: %w", ymlFile, err)
			}
		}
	}
	return nil
}

func loadDefaults(k *koanf.Koanf) error {
	defaults := map[string]any{
		"app.name":                      "gobricks-service",
		"app.version":                   "v1.0.0",
		"app.env":                       EnvDevelopment,
		"app.debug":                     false,
		"app.namespace":                 "default",
		"app.rate.limit":                100,
		"app.rate.burst":                200,
		"app.rate.ippreguard.enabled":   true,
		"app.rate.ippreguard.threshold": 2000,
		"app.startup.timeout":           "10s",

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

		// Cache defaults
		"cache.enabled":               false,
		"cache.type":                  "redis",
		"cache.redis.host":            "localhost",
		"cache.redis.port":            6379,
		"cache.redis.password":        "",
		"cache.redis.database":        0,
		"cache.redis.poolsize":        10,
		"cache.redis.dialtimeout":     "5s",
		"cache.redis.readtimeout":     "3s",
		"cache.redis.writetimeout":    "3s",
		"cache.redis.maxretries":      3,
		"cache.redis.minretrybackoff": "8ms",
		"cache.redis.maxretrybackoff": "512ms",

		"log.level":         "info",
		"log.pretty":        false,
		"log.output.format": "json",
		"log.output.file":   "",

		// Debug endpoints defaults (disabled by default for security)
		"debug.enabled":              false,
		"debug.pathprefix":           "/_sys",
		"debug.allowedips":           []string{"127.0.0.1", "::1"},
		"debug.bearertoken":          "",
		"debug.endpoints.goroutines": true,
		"debug.endpoints.gc":         true,
		"debug.endpoints.health":     true,
		"debug.endpoints.info":       true,

		// Source configuration defaults
		"source.type": SourceTypeStatic,

		// Scheduler defaults
		"scheduler.timeout.shutdown":        "30s",
		"scheduler.timeout.slowjob":         "25s",
		"scheduler.security.cidrallowlist":  []string{},
		"scheduler.security.trustedproxies": []string{},
	}

	return k.Load(confmap.Provider(defaults, "."), nil)
}
