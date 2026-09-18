package app

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/gaborage/go-bricks/cache"
	"github.com/gaborage/go-bricks/cache/redis"
	"github.com/gaborage/go-bricks/config"
	"github.com/gaborage/go-bricks/database"
	"github.com/gaborage/go-bricks/internal/cachekey"
	"github.com/gaborage/go-bricks/logger"
	"github.com/gaborage/go-bricks/messaging"
)

// FactoryResolver encapsulates the logic for resolving factory functions
// from Options, providing default implementations when not specified.
type FactoryResolver struct {
	opts *Options
	// appName is the app.name config value clients built by the default messaging
	// factory stamp as the AMQP app_id property on every publish (ADR-105). It rides
	// the resolver rather than MessagingClientFactoryOptions so that struct stays
	// small enough to pass by value.
	appName string
}

// NewFactoryResolver creates a new factory resolver with the given options.
func NewFactoryResolver(opts *Options) *FactoryResolver {
	return &FactoryResolver{
		opts: opts,
	}
}

// newFactoryResolverForConfig builds the resolver bootstrap uses: the exported
// constructor plus the config-sourced fields that never belonged on Options.
func newFactoryResolverForConfig(opts *Options, cfg *config.Config) *FactoryResolver {
	r := NewFactoryResolver(opts)
	r.appName = cfg.App.Name
	return r
}

// DatabaseConnector returns the appropriate database connector function.
// If no custom connector is provided in options, returns the default implementation.
func (f *FactoryResolver) DatabaseConnector() database.Connector {
	if f.opts != nil && f.opts.DatabaseConnector != nil {
		return f.opts.DatabaseConnector
	}
	return database.NewConnection
}

// MessagingClientFactoryOptions bundles the per-publish tuning knobs threaded
// into the default messaging client factory. Introduced alongside the
// existing MessagingClientFactory (kept byte-identical for apidiff
// compatibility) so ReadyTimeout could be added without breaking that
// method's exported signature.
type MessagingClientFactoryOptions struct {
	ConnectionTimeout  time.Duration
	MaxPublishAttempts int
	ReadyTimeout       time.Duration
	// PublishTimeout is the aggregate per-publish bound (messaging.publishtimeout);
	// zero leaves the publish unbounded.
	PublishTimeout    time.Duration
	ReconnectDelay    time.Duration
	ReconnectMaxDelay time.Duration
	ReinitDelay       time.Duration
	ResendDelay       time.Duration
}

// MessagingClientFactory returns the appropriate messaging client factory function.
// The default factory creates AMQPClient instances configured with the supplied per-publish
// connection timeout and bounded publish-retry attempts. If a custom
// Options.MessagingClientFactory is set it owns construction and receives only (url, log) —
// neither connectionTimeout nor maxPublishAttempts applies to it.
//
// Deprecated: kept for backward compatibility (its signature cannot change without
// breaking apidiff). Use MessagingClientFactoryWithOptions, which also carries
// ReadyTimeout and the four reconnect delays (messaging.reconnect.*) — clients
// built through this method keep the hardcoded client defaults for those.
func (f *FactoryResolver) MessagingClientFactory(connectionTimeout time.Duration, maxPublishAttempts int) messaging.ClientFactory {
	return f.MessagingClientFactoryWithOptions(MessagingClientFactoryOptions{
		ConnectionTimeout:  connectionTimeout,
		MaxPublishAttempts: maxPublishAttempts,
	})
}

// MessagingClientFactoryWithOptions is the options-struct successor to
// MessagingClientFactory. Internal bootstrap wiring (CreateMessagingManager)
// uses this method so every messaging.reconnect.* client knob reaches the client.
//
// Same custom-factory precedence as MessagingClientFactory: if
// Options.MessagingClientFactory is set it owns construction and receives only
// (url, log) — NO field of opts applies to it, so none of the messaging.reconnect.*
// config (timeouts, attempts, and the four reconnect delays) reaches it. Such a
// factory owns construction outright: whatever timeouts, retry bound, reconnect
// delays and app id its client ends up with are the factory's own, not the
// framework's. In particular it never reaches WithAppName, so its clients publish
// no app_id unless the factory sets one itself.
func (f *FactoryResolver) MessagingClientFactoryWithOptions(opts MessagingClientFactoryOptions) messaging.ClientFactory {
	if f.opts != nil && f.opts.MessagingClientFactory != nil {
		return func(url string, log logger.Logger) messaging.AMQPClient {
			return f.opts.MessagingClientFactory(url, log)
		}
	}

	return func(url string, log logger.Logger) messaging.AMQPClient {
		return messaging.NewAMQPClient(url, log,
			messaging.WithConnectionTimeout(opts.ConnectionTimeout),
			messaging.WithMaxPublishAttempts(opts.MaxPublishAttempts),
			messaging.WithReadyTimeout(opts.ReadyTimeout),
			messaging.WithPublishTimeout(opts.PublishTimeout),
			messaging.WithReconnectDelay(opts.ReconnectDelay),
			messaging.WithReconnectMaxDelay(opts.ReconnectMaxDelay),
			messaging.WithReinitDelay(opts.ReinitDelay),
			messaging.WithResendDelay(opts.ResendDelay),
			messaging.WithAppName(f.appName),
		)
	}
}

// CacheConnector returns the appropriate cache connector function.
// If no custom connector is provided in options, returns a Redis connector that
// reads configuration from the resourceSource for the given tenant/key.
//
// Whichever connector is in play, the instance it produces comes back behind the
// key-namespace decorator (ADR-117): the cache a custom Options.CacheConnector dials
// is namespaced exactly like the framework's own. That is the one thing such a
// connector does inherit — cache.redis.username and cache.redis.mode never reach it,
// because it owns its own dial, and no field of the resolved cache config applies to
// it. This method is CreateCacheManager's only caller, so the decorator is installed
// exactly once per pooled instance.
func (f *FactoryResolver) CacheConnector(resourceSource TenantStore, log logger.Logger) cache.Connector {
	return f.namespacedCacheConnector(f.innerCacheConnector(resourceSource, log), resourceSource, log)
}

// innerCacheConnector picks the connector that actually dials: the custom one from
// Options, else the default Redis connector.
func (f *FactoryResolver) innerCacheConnector(resourceSource TenantStore, log logger.Logger) cache.Connector {
	if f.opts != nil && f.opts.CacheConnector != nil {
		return f.opts.CacheConnector
	}
	return newRedisConnector(resourceSource, log)
}

// errUnresolvedCacheNamespace reports a cache whose namespace resolved to nothing
// without anyone asking it to. It is not the documented opt-out: that one is an
// explicit cache.redis.keyprefix: "", and it is honored.
var errUnresolvedCacheNamespace = errors.New(
	"app: cache key namespace resolved to nothing; set app.name, or cache.redis.keyprefix to opt out explicitly")

// namespacedCacheConnector returns inner's instances behind the key-prefix decorator.
//
// It fails closed three times over, each one closing the instance inner just dialed
// rather than pooling a cache under a namespace nobody chose. A prefix the grammar
// refuses — reachable when a dynamic tenant source delivers a section config.Validate
// never saw — would otherwise be an unnamespaced or ambiguous cache, which on a shared
// endpoint is exactly the collision the prefix exists to prevent. A section the store
// could not READ would otherwise resolve to app.name while the section it hid may carry
// a prefix of its own. And a namespace that resolved to nothing at all is refused too:
// the exported NewFactoryResolver carries no app name, so a root key with no section
// prefix would otherwise join to "" and hand back the instance unwrapped, silently
// dropping the namespace for every consumer that builds a resolver itself. An explicit
// "" is a different event and still opts out.
func (f *FactoryResolver) namespacedCacheConnector(inner cache.Connector, resourceSource TenantStore, log logger.Logger) cache.Connector {
	return func(ctx context.Context, key string) (cache.Cache, error) {
		instance, err := inner(ctx, key)
		if err != nil {
			return nil, err
		}

		base, explicit, err := f.cacheKeyPrefixBase(ctx, resourceSource, key)
		if err != nil {
			closeRejectedCacheInstance(instance, key, log)
			log.Error().Err(err).Str("key", key).
				Msg("Cache section could not be read; the key namespace cannot be resolved")
			return nil, err
		}

		// The base passes the ONE-SEGMENT grammar before the tenant fold appends to it.
		// Joined first, a base the grammar refuses can read as a legal namespace:
		// "orders:v2" folds to "orders:v2:acme", three segments each legal on its own,
		// and the ambiguity the one-segment rule removed is back — a service prefixed
		// "orders" writing "v2:acme:…" lands on the same keys. A dynamic tenant source
		// is not obliged to have run config.Validate, so this is reachable.
		if err = cachekey.Validate(base); err != nil {
			closeRejectedCacheInstance(instance, key, log)
			return nil, reportUnusableCachePrefix(
				fmt.Errorf("%w %q: %w", cache.ErrInvalidKeyPrefix, base, err), key, log)
		}

		prefix := cachekey.Join(base, key)

		// WithKeyPrefix runs first even for the empty prefix, so the nil-cache contract
		// break is caught here rather than by a Close below on the nil it returned.
		namespaced, err := cache.WithKeyPrefix(instance, prefix)
		if err != nil {
			// A connector that broke its contract and returned a nil cache with no error
			// has nothing to close, and closing it would panic where the framework must
			// only report. Anything else is a live instance that must not leak.
			if !errors.Is(err, cache.ErrNilCache) {
				closeRejectedCacheInstance(instance, key, log)
			}
			return nil, reportUnusableCachePrefix(err, key, log)
		}

		if prefix == "" && !explicit {
			// namespaced is instance itself — WithKeyPrefix installs no wrapper for an
			// empty prefix — and nothing asked for an unnamespaced cache.
			closeRejectedCacheInstance(namespaced, key, log)
			log.Error().Str("key", key).Msg("Cache key namespace is unresolved")
			return nil, errUnresolvedCacheNamespace
		}
		return namespaced, nil
	}
}

// reportUnusableCachePrefix logs err against the resource key and returns it unchanged,
// so both namespace checks — the base before the fold, the assembled prefix after it —
// report a refusal the same way.
func reportUnusableCachePrefix(err error, key string, log logger.Logger) error {
	log.Error().Err(err).Str("key", key).Msg("Cache key prefix is not a usable namespace")
	return err
}

// closeRejectedCacheInstance releases an instance the namespace check refused, so a
// dialed connection does not leak behind an error the caller never sees a cache for.
func closeRejectedCacheInstance(instance cache.Cache, key string, log logger.Logger) {
	if closeErr := instance.Close(); closeErr != nil {
		log.Warn().Err(closeErr).Str("key", key).
			Msg("Failed to close the cache instance rejected by the key-prefix check")
	}
}

// cacheKeyPrefixBase resolves the namespace the resource key hangs off: the section's
// own cache.redis.keyprefix when one was delivered, else app.name.
//
// The two ways to have no section are NOT the same event, and they no longer share an
// answer. A *config.ConfigError is the store reporting that none is declared here — the
// single-tenant not-configured case, a tenant that declares no cache, and the custom
// Options.CacheConnector deployment with no cache.* block at all. There is no override
// to honor, the app.name default still namespaces, and it is silent: failing here would
// fail every such deployment on every pooled instance.
//
// Anything else is a source that FAILED, and its answer is the error. The section it
// could not deliver may carry an explicit keyprefix, so defaulting to app.name would put
// this key's entries in a different keyspace from the ones already written under that
// prefix — and the instance is POOLED, so the wrong namespace outlives the outage that
// produced it. The default Redis connector has already read the same section by the time
// this runs, which is why a custom connector is the case that reaches here with a live
// instance to close; the read is per pooled instance, not per request.
//
// The bool reports whether the section DELIVERED the value, which is what separates
// the documented opt-out from an app.name that was never set: only an explicit prefix
// may resolve a root instance to no namespace at all.
func (f *FactoryResolver) cacheKeyPrefixBase(ctx context.Context, resourceSource TenantStore, key string) (base string, explicit bool, err error) {
	if resourceSource == nil {
		return f.appName, false, nil
	}
	cacheCfg, err := resourceSource.CacheConfig(ctx, key)
	if err != nil {
		var cfgErr *config.ConfigError
		if !errors.As(err, &cfgErr) {
			return "", false, fmt.Errorf("app: cache key namespace for key %q could not be resolved: %w", key, err)
		}
		return f.appName, false, nil
	}
	if cacheCfg == nil || cacheCfg.Redis.KeyPrefix == nil {
		return f.appName, false, nil
	}
	return *cacheCfg.Redis.KeyPrefix, true, nil
}

// ResourceSource returns the appropriate tenant resource source.
// If no custom resource source is provided in options, creates one from config.
func (f *FactoryResolver) ResourceSource(cfg *config.Config) TenantStore {
	if f.opts != nil && f.opts.ResourceSource != nil {
		return f.opts.ResourceSource
	}
	return config.NewTenantStore(cfg)
}

// HasCustomFactories returns true if any custom factories are provided in options.
// This can be useful for logging or debugging purposes.
func (f *FactoryResolver) HasCustomFactories() bool {
	if f.opts == nil {
		return false
	}

	return f.opts.DatabaseConnector != nil ||
		f.opts.MessagingClientFactory != nil ||
		f.opts.CacheConnector != nil ||
		f.opts.ResourceSource != nil
}

// newRedisConnector creates a cache connector that reads Redis configuration
// from the resourceSource for each tenant/key and creates Redis cache instances.
func newRedisConnector(resourceSource TenantStore, log logger.Logger) cache.Connector {
	return func(ctx context.Context, key string) (cache.Cache, error) {
		if resourceSource == nil {
			err := fmt.Errorf("tenant resource source is nil for key '%s'", key)
			log.Error().
				Str("key", key).
				Msg("Cannot resolve cache configuration: nil resource source")
			return nil, err
		}

		cacheCfg, err := resourceSource.CacheConfig(ctx, key)
		if err != nil {
			log.Debug().
				Err(err).
				Str("key", key).
				Msg("Cache config not available")
			return nil, err
		}

		if err := validateRedisCacheConfig(cacheCfg, key, log); err != nil {
			// Wrapped once, at this one call site, for every check validateRedisCacheConfig
			// raises — the door's own errors cannot forget the wrap the way #1248 did, because
			// there is only one place left to call it from.
			return nil, config.QualifyCacheConfigErrorForKey(err, key)
		}

		return connectRedisCache(cacheCfg, key, log)
	}
}

// validateRedisCacheConfig rejects a cache config this connector cannot build a Redis client
// from: unexpectedly nil, disabled, an unsupported type, or a missing host. Every error it
// raises is root-spelled — addressing it to key is the caller's single responsibility, not
// this function's, which is what makes the wrap impossible to forget for a check added here
// later.
func validateRedisCacheConfig(cacheCfg *config.CacheConfig, key string, log logger.Logger) error {
	if cacheCfg == nil {
		log.Error().
			Str("key", key).
			Msg("Cache configuration unexpectedly nil")
		return config.NewValidationError("cache", fmt.Sprintf("configuration is nil for key '%s'", key))
	}

	if !cacheCfg.Enabled {
		log.Error().
			Str("key", key).
			Msg("Cache configuration has Enabled=false")
		return config.NewNotConfiguredError("cache", "CACHE_ENABLED", "cache.enabled")
	}

	// Validate cache type is "redis" (or empty for backward compatibility)
	if cacheCfg.Type != "" && cacheCfg.Type != config.CacheTypeRedis {
		log.Error().
			Str("key", key).
			Str("type", cacheCfg.Type).
			Msg("Invalid cache type - only 'redis' is supported")
		return config.NewInvalidFieldError("cache.type",
			fmt.Sprintf("unsupported type '%s'", cacheCfg.Type),
			[]string{config.CacheTypeRedis})
	}

	if cacheCfg.Redis.Host == "" {
		log.Error().
			Str("key", key).
			Msg("Redis host is empty - cannot create cache instance")
		return config.NewMissingFieldError("cache.redis.host", "CACHE_REDIS_HOST", "cache.redis.host")
	}

	return nil
}

// redisClientConfig maps the resolved cache config onto the Redis client's own config.
func redisClientConfig(cacheCfg *config.CacheConfig) *redis.Config {
	return &redis.Config{
		Host:            cacheCfg.Redis.Host,
		Port:            cacheCfg.Redis.Port,
		Mode:            cacheCfg.Redis.Mode,
		Username:        cacheCfg.Redis.Username,
		Password:        cacheCfg.Redis.Password,
		Database:        cacheCfg.Redis.Database,
		PoolSize:        cacheCfg.Redis.PoolSize,
		DialTimeout:     cacheCfg.Redis.DialTimeout,
		ReadTimeout:     cacheCfg.Redis.ReadTimeout,
		WriteTimeout:    cacheCfg.Redis.WriteTimeout,
		MaxRetries:      cacheCfg.Redis.MaxRetries,
		MinRetryBackoff: cacheCfg.Redis.MinRetryBackoff,
		MaxRetryBackoff: cacheCfg.Redis.MaxRetryBackoff,
		LoadTimeout:     cacheCfg.LoadTimeout,
		// A struct conversion, not a field-by-field copy: the two blocks carry the
		// same fields in the same order, so adding one to either side without the
		// other is a compile error rather than a silently dropped setting.
		TLS: redis.TLSConfig(cacheCfg.Redis.TLS),
	}
}

// connectRedisCache builds and dials the Redis client for an already-validated cache config.
// redis.NewClient returns two error classes through one return, and they are spelled
// differently on the way out: a dial failure is not a config-shape error, so it is returned
// exactly as the cache package raised it, while a config-class error — cache.ConfigError,
// raised by the client's own shape check and by the TLS material load — is addressed to key,
// the same as validateRedisCacheConfig's whole return is above.
//
// Do not add a config-validation check here: one belongs in validateRedisCacheConfig, whose
// whole return the door qualifies. What this function qualifies is the config-class error the
// cache package raises from inside NewClient, which no check of this door's can pre-empt.
func connectRedisCache(cacheCfg *config.CacheConfig, key string, log logger.Logger) (cache.Cache, error) {
	redisCfg := redisClientConfig(cacheCfg)

	log.Info().
		Str("key", key).
		Str("host", cacheCfg.Redis.Host).
		Int("port", cacheCfg.Redis.Port).
		Int("database", cacheCfg.Redis.Database).
		Int("pool_size", cacheCfg.Redis.PoolSize).
		Bool("tls", cacheCfg.Redis.TLS.Enabled).
		Msg("Creating Redis cache instance")

	// Note: redis.NewClient() does not accept context parameter. It creates its own
	// 5-second timeout context for the initial PING validation during connection.
	client, err := redis.NewClient(redisCfg)
	if err != nil {
		log.Error().
			Err(err).
			Str("key", key).
			Str("host", cacheCfg.Redis.Host).
			Int("port", cacheCfg.Redis.Port).
			Int("database", cacheCfg.Redis.Database).
			Msg("Failed to create Redis cache client")
		return nil, qualifyRedisClientError(err, key)
	}

	log.Debug().
		Str("key", key).
		Str("host", cacheCfg.Redis.Host).
		Int("database", cacheCfg.Redis.Database).
		Msg("Redis cache client created successfully")

	return client, nil
}

// qualifyRedisClientError addresses a config-class error from redis.NewClient to the resource
// key that produced it, and leaves every other class — a dial failure above all — untouched.
//
// The cache package spells its config errors in its own root namespace ("redis.tls",
// "redis.port"), so a tenant's error named no tenant and not even the "cache." head this
// layer's keys carry. The error is therefore restated as this layer's ConfigError at the root
// spelling and handed to the same addressing engine validateRedisCacheConfig's return goes
// through, which rewrites the root head to the tenant's cache subtree. The cache package's own
// message, loader prefix and all, is carried across verbatim so nothing is lost in the move.
func qualifyRedisClientError(err error, key string) error {
	var cfgErr *cache.ConfigError
	if !errors.As(err, &cfgErr) {
		return err
	}
	message := cfgErr.Message
	if cfgErr.Err != nil {
		message += ": " + cfgErr.Err.Error()
	}
	rootSpelled := config.NewValidationError("cache."+cfgErr.Field, message)
	return config.QualifyCacheConfigErrorForKey(rootSpelled, key)
}
