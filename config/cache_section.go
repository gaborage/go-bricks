package config

import (
	"fmt"
	"slices"

	"github.com/gaborage/go-bricks/internal/cachekey"
	"github.com/gaborage/go-bricks/internal/clienttls"
	"github.com/gaborage/go-bricks/internal/redisrules"
)

// normalizeCache fills Redis defaults unconditionally, even when the cache is
// disabled — koanf already fills cache.redis.* in that state, and an enabled
// hand-built cache with a zero port/poolsize must not fail where koanf gives
// 6379/10. Manager defaults (mode-dependent MaxSize, see
// applyCacheManagerDefaults) fill only when enabled: koanf carries no
// cache.manager.* defaults, and a disabled cache's negative manager value is
// CreateCacheManager's to reject (ADR-054), not Validate's.
func normalizeCache(cfg *CacheConfig, multitenant bool) error {
	if err := normalizeCacheSection(cfg); err != nil {
		return err
	}

	if cfg.Enabled {
		return applyCacheManagerDefaults(cfg, multitenant)
	}
	return nil
}

// normalizeCacheSection is the Normalization step both cache doors share: Validate's
// normalizeCache and the connect door, ResolveCacheSectionForKey. It fills the section's own
// fields only, so re-running it on a normalized section changes nothing. The type is filled
// only for an enabled section, the gating the tenant default always had.
func normalizeCacheSection(cfg *CacheConfig) error {
	if cfg.Enabled && cfg.Type == "" {
		cfg.Type = CacheTypeRedis
	}
	applyRedisDefaults(&cfg.Redis)

	// Unconditional, like the Redis defaults above: the load-through bound belongs to
	// the resolved cache instance, and a hand-built enabled cache must not inherit a
	// zero here. A negative value is rejected rather than normalized.
	return applyNonNegativeDefault(&cfg.LoadTimeout, defaultCacheLoadTimeout, "cache.loadtimeout")
}

// checkCache rejects an enabled cache's type and Redis fields; a disabled
// cache is not checked.
func checkCache(cfg *CacheConfig) error {
	if !cfg.Enabled {
		return nil
	}

	if err := checkCacheType(cfg); err != nil {
		return err
	}
	return validateRedisCache(&cfg.Redis)
}

func checkCacheType(cfg *CacheConfig) error {
	validTypes := []string{CacheTypeRedis}
	if !slices.Contains(validTypes, cfg.Type) {
		return NewInvalidFieldError("cache.type", fmt.Sprintf(errNotSupportedFmt, cfg.Type), validTypes)
	}
	return nil
}

// applyRedisDefaults fills in production-safe Redis defaults for any unset
// fields. The top-level cache.* config receives these via koanf, but per-tenant
// cache config (multitenant.tenants.<id>.cache.*) has no koanf defaults, so this
// is the only place those values are populated for tenant caches. Host is left
// untouched: a missing host is a real misconfiguration that must fail fast.
func applyRedisDefaults(cfg *RedisConfig) {
	if cfg.Mode == "" {
		cfg.Mode = redisrules.ModeStandalone
	}
	if cfg.Port == 0 {
		cfg.Port = defaultRedisPort
	}
	if cfg.PoolSize == 0 {
		cfg.PoolSize = defaultRedisPoolSize
	}
	if cfg.DialTimeout == 0 {
		cfg.DialTimeout = defaultRedisDialTimeout
	}
	if cfg.ReadTimeout == 0 {
		cfg.ReadTimeout = defaultRedisReadTimeout
	}
	if cfg.WriteTimeout == 0 {
		cfg.WriteTimeout = defaultRedisWriteTimeout
	}
	if cfg.MaxRetries == 0 {
		cfg.MaxRetries = defaultRedisMaxRetries
	}
	if cfg.MinRetryBackoff == 0 {
		cfg.MinRetryBackoff = defaultRedisMinRetryBackoff
	}
	if cfg.MaxRetryBackoff == 0 {
		cfg.MaxRetryBackoff = defaultRedisMaxRetryBackoff
	}
}

// validateRedisCache is the startup door's Redis check: checkCacheSection's Redis rules, then
// the keyprefix grammar and the TLS material load.
func validateRedisCache(cfg *RedisConfig) error {
	if err := checkRedis(cfg); err != nil {
		return err
	}

	if err := validateRedisKeyPrefix(cfg); err != nil {
		return err
	}

	return loadRedisTLS(&cfg.TLS)
}

// checkCacheSection is checkCache's connect-door twin for an enabled, normalized section: the
// type, then every Redis rule validateRedisCache runs except the two that stay at startup, the
// keyprefix grammar (namespacing owns it on the connector path) and the TLS file read (the
// dial loads the material itself).
func checkCacheSection(cfg *CacheConfig) error {
	if err := checkCacheType(cfg); err != nil {
		return err
	}
	return checkRedis(&cfg.Redis)
}

// checkRedis runs the shared Redis endpoint rules (internal/redisrules) and addresses the
// first violation to its cache.redis.* key: a missing value names its env var and YAML path,
// a closed set becomes the Action.
func checkRedis(cfg *RedisConfig) error {
	v := redisrules.Check(&redisrules.Endpoint{
		Host:         cfg.Host,
		Mode:         cfg.Mode,
		Username:     cfg.Username,
		Password:     cfg.Password,
		Port:         cfg.Port,
		Database:     cfg.Database,
		PoolSize:     cfg.PoolSize,
		DialTimeout:  cfg.DialTimeout,
		ReadTimeout:  cfg.ReadTimeout,
		WriteTimeout: cfg.WriteTimeout,
		TLSEnabled:   cfg.TLS.Enabled,
		TLS:          redisTLSMaterial(&cfg.TLS),
	})
	if v == nil {
		return nil
	}

	field := "cache.redis." + v.Field
	switch {
	case v.Missing:
		return NewMissingFieldError(field, keyToEnvVar(field), field)
	case len(v.Allowed) > 0:
		return NewInvalidFieldError(field, v.Message, v.Allowed)
	default:
		return NewValidationError(field, v.Message)
	}
}

// validateRedisKeyPrefix checks an explicitly delivered key namespace against the
// shared grammar. An absent key (nil) carries no value to check: it takes app.name,
// whose own fitness is checkCacheKeyNamespace's rule. An explicit empty string is the
// opt-out and passes the grammar unchanged.
func validateRedisKeyPrefix(cfg *RedisConfig) error {
	if cfg.KeyPrefix == nil {
		return nil
	}
	if err := cachekey.Validate(*cfg.KeyPrefix); err != nil {
		return NewValidationError(fieldCacheRedisKeyPrefix, err.Error())
	}
	return nil
}

// checkCacheKeyNamespace enforces the one rule the app.name default creates: where no
// cache.redis.keyprefix was delivered, app.name IS the cache key namespace, so it must
// be usable as one. Without this check a name like "my service" would boot green and
// fail at the first cache access instead.
//
// The rule binds wherever the default applies, which is not only the root section: a
// multi-tenant deployment may enable caches solely under multitenant.tenants.<id>.cache,
// and those prefixes fold the same app.name in. Every failing section produces the same
// error — the fault is the name — so the tenant that tripped it is not named.
func checkCacheKeyNamespace(cfg *Config) error {
	if !cacheKeyPrefixDefaultApplies(cfg) {
		return nil
	}
	if err := cachekey.Validate(cfg.App.Name); err != nil {
		return &ConfigError{
			Category: errCategoryInvalid,
			Field:    fieldAppName,
			Message:  "is the default cache key namespace and " + err.Error(),
			Action: "rename the application, or set a usable keyprefix on each enabled cache " +
				"section (cache.redis.keyprefix, multitenant.tenants.<id>.cache.redis.keyprefix); " +
				"a tenant section does not inherit the root one",
		}
	}
	return nil
}

// cacheKeyPrefixDefaultApplies reports whether any enabled cache section would take its
// namespace from app.name.
func cacheKeyPrefixDefaultApplies(cfg *Config) bool {
	if cfg.Cache.Enabled && cfg.Cache.Redis.KeyPrefix == nil {
		return true
	}
	if !cfg.Multitenant.Enabled {
		return false
	}
	for tenantID := range cfg.Multitenant.Tenants {
		tenant := cfg.Multitenant.Tenants[tenantID]
		if tenant.Cache.Enabled && tenant.Cache.Redis.KeyPrefix == nil {
			return true
		}
	}
	return false
}

// fieldCacheRedisTLS is the key a TLS material load failure is addressed to. The tenant
// spelling (multitenant.tenants.<id>.cache.redis.tls) is derived from it downstream, so the
// "cache." head must stay.
const fieldCacheRedisTLS = "cache.redis.tls"

// cacheRedisTLSErrPrefix names the cache's error namespace for the shared TLS
// loader, matching the spelling cache/redis uses for the same material.
const cacheRedisTLSErrPrefix = "cache: redis: tls:"

// loadRedisTLS actually loads the material, which means this validation reads files —
// unusual here, and deliberate. Unlike server.tls, whose material is read at Start() one hop
// later, a Redis client is created lazily per tenant on first use, so config validation is
// the only door at which a missing or corrupt bundle can fail the boot rather than a request
// hours later. The dial loads it again; the two loads are a startup gate and a use-time one,
// not a cache. It assumes checkRedis passed.
func loadRedisTLS(cfg *RedisTLSConfig) error {
	if !cfg.Enabled {
		return nil
	}
	m := redisTLSMaterial(cfg)
	if _, err := clienttls.Build(cacheRedisTLSErrPrefix, &m); err != nil {
		return NewValidationError(fieldCacheRedisTLS, err.Error())
	}
	return nil
}

func redisTLSMaterial(cfg *RedisTLSConfig) clienttls.Material {
	return clienttls.Material{
		CertFile:   cfg.CertFile,
		CertValue:  cfg.CertValue,
		KeyFile:    cfg.KeyFile,
		KeyValue:   cfg.KeyValue,
		CAFile:     cfg.CAFile,
		CAValue:    cfg.CAValue,
		ServerName: cfg.ServerName,
		MinVersion: cfg.MinVersion,
	}
}

// ResolveCacheSectionForKey returns a normalized, checked, owned clone of
// section, with errors addressed to resourceKey. It never mutates section.
// It is the cache connect door, for sections Validate may never have seen. It leaves the key
// namespace to the connector that applies it.
func ResolveCacheSectionForKey(section *CacheConfig, resourceKey string) (*CacheConfig, error) {
	sec := kindCache(resourceKey)
	if section == nil {
		return nil, sec.qualify(NewValidationError(fieldCache,
			fmt.Sprintf("configuration is nil for key '%s'", resourceKey)))
	}
	if !section.Enabled {
		return nil, sec.qualify(NewNotConfiguredError(fieldCache, "CACHE_ENABLED", "cache.enabled"))
	}

	resolved := cloneCacheSection(section)
	if err := normalizeCacheSection(resolved); err != nil {
		return nil, sec.qualify(err)
	}
	if err := checkCacheSection(resolved); err != nil {
		return nil, sec.qualify(err)
	}
	return resolved, nil
}

// cloneCacheSection copies section so the copy shares no memory with it; KeyPrefix is the
// one reference field.
func cloneCacheSection(section *CacheConfig) *CacheConfig {
	clone := *section
	if section.Redis.KeyPrefix != nil {
		prefix := *section.Redis.KeyPrefix
		clone.Redis.KeyPrefix = &prefix
	}
	return &clone
}
