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

	"github.com/gaborage/go-bricks/internal/configdecode"
	"github.com/gaborage/go-bricks/logger"
)

// configSections is the set of top-level keys that hold a config sub-tree (a map), not a
// scalar. It mirrors the koanf-tagged fields of the Config struct, plus "observability"
// (unmarshaled separately by the observability package from the same koanf instance). A bare
// environment variable named after one of these (DEBUG, CACHE, SERVER, …) — common in
// container runtimes that inject Docker-link vars like SERVER_PORT=tcp://IP:PORT — is never a
// valid config value and is dropped in the env TransformFunc BEFORE koanf unflattens, so it
// cannot collide with a legitimate sub-key (e.g. DEBUG vs DEBUG_ENABLED) during unflatten. The
// set deliberately omits "custom" and any service-specific prefix: those reach config only via
// sub-keys (custom.* / the InjectInto escape hatch) and a bare CUSTOM env var is meaningless.
var configSections = map[string]bool{
	"app":           true,
	"server":        true,
	fieldDatabase:   true,
	"databases":     true,
	fieldCache:      true,
	"log":           true,
	fieldMessaging:  true,
	"multitenant":   true,
	fieldDebug:      true,
	"source":        true,
	"scheduler":     true,
	"outbox":        true,
	"inbox":         true,
	"keystore":      true,
	"observability": true,
}

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

	// Load environment variables (highest priority).
	//
	// SECURITY (M4): the env provider has no Prefix filter, so it ingests EVERY process
	// environment variable. A bare env var whose name maps onto an existing config section
	// (CACHE, DEBUG, DATABASE, …) unflattens to a scalar at that key; the default koanf merge
	// would then replace the nested config map (from defaults/YAML) with the scalar, and
	// UnmarshalWithConf would abort with "expected a map or struct, got string". This is readily
	// triggered in container runtimes (e.g. Kubernetes auto-injects Docker-link vars like
	// SERVER_PORT=tcp://IP:PORT). Two complementary guards neutralize this:
	//
	//  1. TransformFunc drops a bare section-name var (k == a configSections entry, no sub-key)
	//     BEFORE koanf unflattens, so it can never win the intra-source unflatten race against a
	//     legitimate sub-key — i.e. a stray DEBUG=1 cannot silently discard a real DEBUG_ENABLED.
	//  2. skipScalarOverMapMerge guards the merge so any remaining incoming scalar can never
	//     overwrite an existing map node, dropping unrelated collisions instead of corrupting
	//     the tree.
	//
	// Both preserve the InjectInto escape hatch: service-specific config:"..." keys arrive with
	// a sub-path at fresh leaves, so they bypass guard 1 and merge normally under guard 2.
	if err := k.Load(envprovider.Provider(".", envprovider.Opt{
		TransformFunc: func(k, v string) (string, any) {
			// Convert UPPER_CASE to lower.case for koanf
			k = strings.ReplaceAll(strings.ToLower(k), "_", ".")
			// Drop a bare top-level section name (no sub-key); see SECURITY (M4) above.
			if configSections[k] {
				return "", nil
			}
			return k, v
		},
	}), nil, koanf.WithMergeFunc(skipScalarOverMapMerge)); err != nil {
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

// skipScalarOverMapMerge is a koanf.WithMergeFunc used for the environment-variable load.
// It merges src into dest with the same left-to-right semantics as the default koanf merge
// (github.com/knadh/koanf/maps.Merge), with one guard: an incoming scalar from src never
// overwrites an existing map node in dest. See the SECURITY (M4) note in Load — a stray
// process env var whose name collides with a config section (CACHE, DEBUG, a Kubernetes
// Docker-link var, …) would otherwise replace the section's nested map with a string and
// abort UnmarshalWithConf. Such collisions are dropped (the structured config wins); every
// other key — including service-specific InjectInto keys at fresh leaves — merges normally.
func skipScalarOverMapMerge(src, dest map[string]any) error {
	mergeSkippingScalarOverMap(src, dest)
	return nil
}

// mergeSkippingScalarOverMap recursively merges src into dest, mutating dest. It mirrors
// koanf's default maps.Merge except that a scalar in src is dropped (not written) when dest
// already holds a map at the same key, so an env scalar can never clobber a config subtree.
func mergeSkippingScalarOverMap(src, dest map[string]any) {
	for key, srcVal := range src {
		// A non-nil map[string]any here means dest already holds a map node at key;
		// a missing key or scalar leaf gives the nil/false zero values.
		destMap, destIsMap := dest[key].(map[string]any)

		srcMap, srcIsMap := srcVal.(map[string]any)
		if !srcIsMap {
			// Incoming scalar: take it unless it would clobber an existing map node.
			if destIsMap {
				continue // keep the structured config; drop the colliding scalar.
			}
			dest[key] = srcVal
			continue
		}

		// Incoming map: recurse into an existing map, else set it wholesale.
		if destIsMap {
			mergeSkippingScalarOverMap(srcMap, destMap)
			continue
		}
		dest[key] = srcVal
	}
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
// sources are not enumerable here; callers reject them (or run in shared tenancy, which
// does not fan out) before relying on this.
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

// ShouldLogRoutes reports whether the per-route "Route registered" startup lines
// should be emitted. An explicit server.logroutes value always wins; an absent
// key (nil) defaults to development mode (see AppConfig.IsDevelopment) so routes
// are visible at first `go run` while production stays silent. Nil-safe so it is
// correct whether or not Validate has run (the NewWithConfig path bypasses it).
func (c *Config) ShouldLogRoutes() bool {
	if c == nil {
		return false
	}
	if c.Server.LogRoutes != nil {
		return *c.Server.LogRoutes
	}
	return c.App.IsDevelopment()
}

// tryLoadYAMLFile attempts to load a YAML configuration file with both .yaml and .yml extensions.
// It tries .yaml first, then falls back to .yml if .yaml is not found.
// Both extensions are optional - no error is returned if neither file exists.
// However, syntax errors, permission errors, and other I/O errors are propagated.
func tryLoadYAMLFile(k *koanf.Koanf, baseName string) error {
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

// buildDecoderConfig is the decoder for Load: it replicates koanf's default Unmarshal
// decoder (knadh/koanf/v2 koanf.go:265-272) plus the numeric-duration guard and the
// comma-split slice hook (so a single env var can express a []string). koanf fills in
// Result and TagName at unmarshal time.
func buildDecoderConfig() *mapstructure.DecoderConfig {
	return &mapstructure.DecoderConfig{
		DecodeHook: mapstructure.ComposeDecodeHookFunc(
			configdecode.NumericToDurationGuardHookFunc(),
			stringToTrimmedSliceHookFunc(","),
			mapstructure.StringToTimeDurationHookFunc(),
			mapstructure.TextUnmarshallerHookFunc(),
		),
		WeaklyTypedInput: true,
	}
}

// unmarshalDecoderConfig is the decoder for the public Config.Unmarshal. It mirrors koanf's
// default Unmarshal decoder (StringToTimeDurationHookFunc + text-unmarshaler + WeaklyTypedInput)
// plus only the numeric-duration guard — deliberately WITHOUT the comma-split slice hook, so
// string -> []string keeps koanf's default single-element wrap on this public seam instead of
// silently comma-splitting. (koanf's default uses its own unexported textUnmarshalerHookFunc;
// the exported mapstructure.TextUnmarshallerHookFunc is the closest public equivalent and
// differs only for custom string types no framework config field uses.)
func unmarshalDecoderConfig() *mapstructure.DecoderConfig {
	return &mapstructure.DecoderConfig{
		DecodeHook: mapstructure.ComposeDecodeHookFunc(
			configdecode.NumericToDurationGuardHookFunc(),
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
		"server.bodylimit":          DefaultBodyLimitBytes,

		// Database defaults not provided for deterministic behavior
		// Database will only be enabled when explicitly configured

		// Cache defaults. These mirror the defaultRedis* constants in validation.go
		// (the single source of truth, also applied to per-tenant caches via
		// applyRedisDefaults) — keep the two in sync. koanf duration defaults are
		// strings, so the time.Duration constants are rendered via .String().
		"cache.enabled":               false,
		"cache.type":                  CacheTypeRedis,
		"cache.redis.host":            defaultHost,
		"cache.redis.port":            defaultRedisPort,
		"cache.redis.password":        "",
		fieldCacheRedisDB:             0,
		fieldCacheRedisPool:           defaultRedisPoolSize,
		"cache.redis.dialtimeout":     defaultRedisDialTimeout.String(),
		"cache.redis.readtimeout":     defaultRedisReadTimeout.String(),
		"cache.redis.writetimeout":    defaultRedisWriteTimeout.String(),
		"cache.redis.maxretries":      defaultRedisMaxRetries,
		"cache.redis.minretrybackoff": defaultRedisMinRetryBackoff.String(),
		"cache.redis.maxretrybackoff": defaultRedisMaxRetryBackoff.String(),

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
