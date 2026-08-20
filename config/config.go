package config

import (
	"errors"
	"fmt"
	stdmaps "maps"
	"os"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/go-viper/mapstructure/v2"
	"github.com/knadh/koanf/maps"
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
	fieldDatabases:  true,
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

	var cfg Config
	if err := k.UnmarshalWithConf("", &cfg, koanf.UnmarshalConf{
		DecoderConfig: buildDecoderConfig(),
	}); err != nil {
		return nil, fmt.Errorf("failed to unmarshal config: %w", err)
	}

	// Store the Koanf instance for flexible access
	cfg.k = k

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
// config.<garbage>.yaml read is attempted, leaving checkApp to reject it with a clear
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
// correct whether or not Validate has run (a Builder assembled without WithConfig,
// or any Config built by hand outside app.NewWithConfig, still reaches this unvalidated).
func (c *Config) ShouldLogRoutes() bool {
	if c == nil {
		return false
	}
	if c.Server.LogRoutes != nil {
		return *c.Server.LogRoutes
	}
	return c.App.IsDevelopment()
}

// SecretFloor is the effective keystore.secretminlength: the documented default
// when the tri-state pointer is nil, otherwise the configured value (0 = off,
// deprecated). Validate's normalize fills the pointer with this same answer, so
// after Validate the two never disagree; the accessor exists for the reader,
// as IsCacheCritical does for cache.critical.
func (c *KeyStoreConfig) SecretFloor() int {
	if c == nil || c.SecretMinLength == nil {
		return DefaultKeyStoreSecretMinLength
	}
	return *c.SecretMinLength
}

// IsCacheCritical reports whether a failing cache probe should fail /ready with 503.
// Strict by default: only an explicit cache.critical=false opts out, so an absent key —
// and a nil receiver, the most absent config there is — stays critical.
func (c *Config) IsCacheCritical() bool {
	if c == nil || c.Cache.Critical == nil {
		return true
	}
	return *c.Cache.Critical
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

// derivedDefaultKeys is the allowlist of koanf keys whose default VALUE is rendered by the
// normalize phase instead of being written again below. One mechanism per key, deliberately
// not one mechanism for all of them (design doc decision 17): keys that must FAIL on zero —
// app.name, server.port, server.timeout.*, log.level — stay hand-written in koanfOnlyDefaults,
// because deriving them would silently turn a required key into a defaulted one.
//
// Full derivation is barred for two reasons beyond that. It would hand debug.allowedips to
// whatever normalize fills, deciding the fail-closed posture ADR-049 depends on somewhere
// other than the explicit map that states it today. And it would preload the database
// identity keys, disarming the koanf-presence check ADR-051's delivered-empty rule reads.
//
// A key joins this list only when normalize owns its fill AND its value is mode-invariant —
// a default that differs between single- and multi-tenant (the manager pool sizes, where zero
// means unlimited) cannot be preloaded without overriding that meaning. Both properties are
// enforced by test, not by review.
var derivedDefaultKeys = []string{
	"app.startup.timeout",
	"cache.redis.port",
	"cache.redis.poolsize",
	"cache.redis.dialtimeout",
	"cache.redis.readtimeout",
	"cache.redis.writetimeout",
	"cache.redis.maxretries",
	"cache.redis.minretrybackoff",
	"cache.redis.maxretrybackoff",
	"keystore.secretminlength",
}

// derivedDefaults renders the allowlisted keys by normalizing a zero Config and picking them
// out of the result, so the Go-side constants normalize applies are the single truth for
// those keys (ADR-064: normalize has to handle a hand-built config anyway).
//
// Durations are rendered as strings so a koanf getter sees the same type it saw when these
// values were literals, and a *int is dereferenced for the same reason.
func derivedDefaults() (map[string]any, error) {
	var zero Config
	if err := normalize(&zero); err != nil {
		return nil, fmt.Errorf("deriving koanf defaults: %w", err)
	}

	flat, err := flattenConfig(&zero)
	if err != nil {
		return nil, err
	}

	derived := make(map[string]any, len(derivedDefaultKeys))
	for _, key := range derivedDefaultKeys {
		value, ok := flat[key]
		if !ok {
			return nil, fmt.Errorf("deriving koanf defaults: normalize does not fill %q", key)
		}
		derived[key] = renderDefault(value)
	}
	return derived, nil
}

// flattenConfig turns a Config into the dotted key space koanf stores, using the same koanf
// tags the loader unmarshals through.
func flattenConfig(cfg *Config) (map[string]any, error) {
	var nested map[string]any
	dec, err := mapstructure.NewDecoder(&mapstructure.DecoderConfig{Result: &nested, TagName: "koanf"})
	if err != nil {
		return nil, fmt.Errorf("deriving koanf defaults: %w", err)
	}
	if err := dec.Decode(cfg); err != nil {
		return nil, fmt.Errorf("deriving koanf defaults: %w", err)
	}

	flat, _ := maps.Flatten(nested, nil, ".") // second return is the key-path index, unused here
	return flat, nil
}

// renderDefault matches the type a hand-written literal would have carried.
func renderDefault(value any) any {
	switch v := value.(type) {
	case time.Duration:
		return v.String()
	case *int:
		if v == nil {
			return nil
		}
		return *v
	default:
		return value
	}
}

func loadDefaults(k *koanf.Koanf) error {
	defaults := koanfOnlyDefaults()

	derived, err := derivedDefaults()
	if err != nil {
		return err
	}
	// Disjoint by construction and pinned by test, so neither side can quietly win.
	stdmaps.Copy(defaults, derived)

	return k.Load(confmap.Provider(defaults, "."), nil)
}

// koanfOnlyDefaults are the keys koanf alone declares: the ones that must fail validation
// when unset, and the ones nothing reads through a normalize-filled struct field.
func koanfOnlyDefaults() map[string]any {
	return map[string]any{
		"app.name":                      "gobricks-service",
		"app.version":                   "v1.0.0",
		fieldAppEnv:                     EnvDevelopment,
		"app.debug":                     false,
		"app.namespace":                 "default",
		fieldAppRateLimit:               100,
		"app.rate.burst":                200,
		"app.rate.ippreguard.enabled":   true,
		"app.rate.ippreguard.threshold": 2000,

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
		fieldServerTrustedProxies:   []string{},

		// Database defaults not provided for deterministic behavior
		// Database will only be enabled when explicitly configured

		// Cache defaults. The redis port/pool/timeout/retry keys are DERIVED — see
		// derivedDefaultKeys — so only the keys normalize does not own are written here.
		"cache.enabled":        false,
		"cache.type":           CacheTypeRedis,
		"cache.redis.host":     defaultHost,
		"cache.redis.password": "",
		fieldCacheRedisDB:      0,

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
		// disable the minimum-length check explicitly (deprecated, WARNs — #1036).
	}
}
