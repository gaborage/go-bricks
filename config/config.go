package config

import (
	"errors"
	"fmt"
	"os"
	"reflect"
	"sort"
	"strings"

	"github.com/go-viper/mapstructure/v2"
	"github.com/knadh/koanf/parsers/yaml"
	"github.com/knadh/koanf/providers/confmap"
	envprovider "github.com/knadh/koanf/providers/env/v2"
	"github.com/knadh/koanf/providers/file"
	"github.com/knadh/koanf/v2"

	"github.com/gaborage/go-bricks/logger"
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

	// Load environment-specific YAML (if exists). The suffix must honor APP_ENV from the
	// environment so a 12-factor deployment (APP_ENV=production + config.production.yaml)
	// selects the right overlay — the env provider is loaded only below (after this
	// selection), so reading koanf alone would always see the default/config.yaml value.
	env := resolveEnvOverlaySuffix(k)
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
	if err := k.UnmarshalWithConf("", &cfg, koanf.UnmarshalConf{
		DecoderConfig: buildDecoderConfig(),
	}); err != nil {
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

// resolveEnvOverlaySuffix determines the config.<env>.yaml overlay suffix. The APP_ENV
// environment variable takes precedence, because the env provider is loaded only after
// overlay selection — so without this a deployment that sets APP_ENV but ships only
// config.<env>.yaml would silently load the default/config.yaml suffix instead. Falls back
// to the koanf value (defaults / config.yaml) when APP_ENV is unset.
//
// The resolved suffix is interpolated into a filename, so it is validated against the same
// envFormat as cfg.App.Env: a malformed value (path separators, etc.) returns "" so no
// config.<garbage>.yaml read is attempted, leaving validateApp to reject it with a clear
// error rather than an opaque YAML-parse failure.
func resolveEnvOverlaySuffix(k *koanf.Koanf) string {
	env := strings.TrimSpace(os.Getenv(keyToEnvVar(fieldAppEnv)))
	if env == "" {
		env = k.String(fieldAppEnv)
	}
	if !envFormat.MatchString(env) {
		return ""
	}
	return env
}

// keyToEnvVar converts a koanf key (lower.dotted) to its environment-variable name
// (UPPER_SNAKE), the inverse of the env provider's TransformFunc in Load.
func keyToEnvVar(key string) string {
	return strings.ToUpper(strings.ReplaceAll(key, ".", "_"))
}

// PerTenantJobKeys returns the tenant keys a per-tenant background job (e.g. the outbox
// relay or the outbox/inbox cleanup jobs) should iterate each cycle:
//
//   - single-tenant mode → a single "" key (one pass with no tenant in context;
//     multitenant.SetTenant with "" is a no-op);
//   - static multi-tenant mode → the configured tenant IDs, sorted for deterministic
//     iteration.
//
// The result is empty ONLY for a degenerate static multi-tenant config with no tenants
// (multitenant.enabled=true but multitenant.tenants omitted) — callers must reject that,
// because a per-tenant job would otherwise silently iterate nothing. Dynamic multi-tenant
// sources are not enumerable here; callers reject them before relying on this.
func (c *Config) PerTenantJobKeys() []string {
	if c == nil || !c.Multitenant.Enabled {
		return []string{""}
	}
	ids := make([]string, 0, len(c.Multitenant.Tenants))
	for id := range c.Multitenant.Tenants {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
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

// buildDecoderConfig replicates koanf's default Unmarshal decoder
// (knadh/koanf/v2 koanf.go:265-272) so we control the DecodeHook chain.
// koanf fills in Result and TagName at unmarshal time.
func buildDecoderConfig() *mapstructure.DecoderConfig {
	return &mapstructure.DecoderConfig{
		DecodeHook: mapstructure.ComposeDecodeHookFunc(
			stringToTrimmedSliceHookFunc(","),
			mapstructure.StringToTimeDurationHookFunc(),
			mapstructure.TextUnmarshallerHookFunc(),
		),
		WeaklyTypedInput: true,
	}
}

// stringToTrimmedSliceHookFunc splits a scalar string into []string on sep,
// trimming each element and dropping empties. Scoped to string -> []string only
// (Type-level), so []byte, other slices, and YAML sequences are untouched. This
// lets a single env var express a multi-element list, e.g.
// SCHEDULER_SECURITY_CIDRALLOWLIST=10.0.0.0/8,192.168.0.0/16.
func stringToTrimmedSliceHookFunc(sep string) mapstructure.DecodeHookFunc {
	return func(f reflect.Type, t reflect.Type, data any) (any, error) {
		if f.Kind() != reflect.String || t != reflect.TypeOf([]string(nil)) {
			return data, nil
		}
		// reflect.Value.String() (not data.(string)) so named string types don't panic.
		return splitAndTrimList(reflect.ValueOf(data).String(), sep), nil
	}
}

func loadDefaults(k *koanf.Koanf) error {
	defaults := map[string]any{
		"app.name":                      "gobricks-service",
		"app.version":                   "v1.0.0",
		fieldAppEnv:                     EnvDevelopment,
		"app.debug":                     false,
		"app.namespace":                 "default",
		fieldAppRateLimit:               100,
		"app.rate.burst":                200,
		"app.rate.ippreguard.enabled":   true,
		"app.rate.ippreguard.threshold": 2000,
		"app.startup.timeout":           "10s",

		"server.host":               "0.0.0.0",
		fieldServerPort:             8080,
		"server.timeout.read":       "15s",
		"server.timeout.write":      "30s",
		"server.timeout.idle":       "60s",
		"server.timeout.middleware": "5s",
		"server.timeout.shutdown":   "10s",
		"server.path.base":          "",
		"server.path.health":        "/health",
		"server.path.ready":         "/ready",
		"server.gzip.minlength":     1024,

		// Database defaults not provided for deterministic behavior
		// Database will only be enabled when explicitly configured

		// Cache defaults
		"cache.enabled":               false,
		"cache.type":                  CacheTypeRedis,
		"cache.redis.host":            defaultHost,
		"cache.redis.port":            6379,
		"cache.redis.password":        "",
		fieldCacheRedisDB:             0,
		fieldCacheRedisPool:           10,
		"cache.redis.dialtimeout":     "5s",
		"cache.redis.readtimeout":     "3s",
		"cache.redis.writetimeout":    "3s",
		"cache.redis.maxretries":      3,
		"cache.redis.minretrybackoff": "8ms",
		"cache.redis.maxretrybackoff": "512ms",

		fieldLogLevel:       logger.LevelInfo,
		"log.pretty":        false,
		"log.output.format": "auto",
		"log.output.file":   "",

		// Debug endpoints defaults (disabled by default for security)
		"debug.enabled":              false,
		"debug.pathprefix":           "/_sys",
		"debug.allowedips":           []string{"127.0.0.1", "::1"},
		"debug.trustedproxies":       []string{},
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

		// KeyStore defaults — symmetric secret floor (32 bytes). Set to 0 to
		// disable the minimum-length check explicitly.
		"keystore.secretminlength": 32,
	}

	return k.Load(confmap.Provider(defaults, "."), nil)
}
