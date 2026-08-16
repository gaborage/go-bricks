package config

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// singleTenantFixture is a full literal config with a named database, so the
// map write-back path is under every pin that uses it; staticMultitenantFixture
// is its static-tenant counterpart.
func singleTenantFixture() *Config {
	cfg := createValidFullConfig()
	cfg.Databases = map[string]DatabaseConfig{"reporting": createValidDatabaseConfig()}
	return cfg
}

func TestValidateRejectsNil(t *testing.T) {
	err := Validate(nil)
	require.ErrorIs(t, err, errNilConfig)
}

// staticMultitenantFixture is a full literal config with static
// multitenancy: two tenant databases (so the tenant map write-back path is
// under the pin) and a named database. The root database is left absent — a
// root database and static tenants both configured is a conflict
// (validateNoSingleTenantConflict).
func staticMultitenantFixture() *Config {
	cfg := createValidFullConfig()
	cfg.Database = DatabaseConfig{}
	cfg.Source = SourceConfig{Type: SourceTypeStatic}
	cfg.Multitenant = MultitenantConfig{
		Enabled:  true,
		Resolver: ResolverConfig{Type: ResolverTypeHeader},
		Tenants: map[string]TenantEntry{
			"acme":   {Database: createValidDatabaseConfig()},
			"globex": {Database: createValidDatabaseConfig()},
		},
	}
	cfg.Databases = map[string]DatabaseConfig{"reporting": createValidDatabaseConfig()}
	return cfg
}

// TestValidateIsIdempotent pins decision 5 of the normalize/check split design:
// every construction path calls Validate, some more than once, so a second
// pass must be a no-op. Two independently built configs are compared rather
// than a *cfg snapshot, because a shallow snapshot would share any map field
// with the value it is compared against and could not catch a mutation.
func TestValidateIsIdempotent(t *testing.T) {
	t.Run("literal_door", func(t *testing.T) {
		a := singleTenantFixture()
		require.NoError(t, Validate(a))

		b := singleTenantFixture()
		require.NoError(t, Validate(b))
		require.NoError(t, Validate(b))

		require.Equal(t, a, b)
	})

	t.Run("literal_door_multitenant", func(t *testing.T) {
		a := staticMultitenantFixture()
		require.NoError(t, Validate(a))

		b := staticMultitenantFixture()
		require.NoError(t, Validate(b))
		require.NoError(t, Validate(b))

		require.Equal(t, a, b)
	})

	t.Run("koanf_door", func(t *testing.T) {
		clearEnvironmentVariables()
		defer clearEnvironmentVariables()
		t.Chdir(t.TempDir())

		// a is validated once, by Load itself.
		a, err := Load()
		require.NoError(t, err)

		// b is validated twice: once by Load, once more explicitly; the second
		// pass must keep the koanf handle it was given.
		b, err := Load()
		require.NoError(t, err)
		k := b.k
		require.NoError(t, Validate(b))
		require.Same(t, k, b.k)

		// Two Load calls hold two distinct *koanf.Koanf, so compare copies with
		// the handle blanked rather than mutating the values under test.
		ax, bx := *a, *b
		ax.k, bx.k = nil, nil
		require.Equal(t, ax, bx)
	})
}

// TestCheckDoesNotMutate pins decision 3 of the normalize/check split design:
// check rejects a normalized config without changing it. Both cases normalize
// two independently built, identical configs, then call check on only one of
// them — if check mutated its argument, the pair would no longer be equal.
func TestCheckDoesNotMutate(t *testing.T) {
	t.Run("valid", func(t *testing.T) {
		a := createValidFullConfig()
		b := createValidFullConfig()
		require.NoError(t, normalize(a))
		require.NoError(t, normalize(b))

		require.NoError(t, check(a))
		require.Equal(t, a, b)
	})

	// The rejection comes from the last check step, so every earlier step runs
	// on the failing path before the pin is taken.
	t.Run("invalid", func(t *testing.T) {
		a := createValidFullConfig()
		a.Debug.TrustedProxies = []string{"not-a-cidr"}
		b := createValidFullConfig()
		b.Debug.TrustedProxies = []string{"not-a-cidr"}
		require.NoError(t, normalize(a))
		require.NoError(t, normalize(b))

		require.Error(t, check(a))
		require.Equal(t, a, b)
	})
}

// TestNormalizeDeliveredEmptyWinsOverIncomplete pins the presence step's
// position at the head of normalize (ADR-051): a root database delivered
// through koanf with an empty identity key must fail with the delivered-empty
// error, never the generic incomplete one a later shaping step would raise for
// the same key. An app-side normalize rejection is planted so the pin fails if
// the presence step slips below normalizeApp; the sibling proves the plant is
// live.
func TestNormalizeDeliveredEmptyWinsOverIncomplete(t *testing.T) {
	t.Run("presence_wins", func(t *testing.T) {
		cfg, err := loadDeliveredEmptyFixture(t, "", map[string]string{"DATABASE_HOST": "", "APP_STARTUP_TIMEOUT": "-1s"})
		require.Error(t, err)
		assert.Nil(t, cfg)
		assertDeliveredEmptyError(t, err.Error(), []string{"delivered empty", "database.host"}, []string{"incomplete", appStartupTimeoutField}, nil)
	})

	t.Run("plant_is_live", func(t *testing.T) {
		cfg, err := loadDeliveredEmptyFixture(t, "", map[string]string{"APP_STARTUP_TIMEOUT": "-1s"})
		require.Error(t, err)
		assert.Nil(t, cfg)
		assert.Contains(t, err.Error(), appStartupTimeoutField)
	})
}

// TestNormalizeLiteralDoorIncompleteSurfaces pins the complementary door: a
// hand-built Config literal carries no koanf handle, so
// validateNoDeliveredEmptyDatabase is inert for it (see
// TestValidateNoDeliveredEmptyDatabaseInertForLiteral) and normalizeDatabaseSection's
// own rejection must surface instead.
func TestNormalizeLiteralDoorIncompleteSurfaces(t *testing.T) {
	cfg := createValidFullConfig()
	cfg.Database = DatabaseConfig{Host: "db.internal"}

	err := Validate(cfg)
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "delivered empty")

	var cfgErr *ConfigError
	require.ErrorAs(t, err, &cfgErr)
	assert.Equal(t, errCategoryInvalid, cfgErr.Category)
	assert.Equal(t, "database.type", cfgErr.Field)
}

// TestNormalizeManagerDefaultsRootOnly pins that database.manager.* defaults
// reach the root section only; a named or tenant section carrying a manager
// block is rejected — pinned through Validate in validation_test.go.
func TestNormalizeManagerDefaultsRootOnly(t *testing.T) {
	cfg := singleTenantFixture()
	require.NoError(t, Validate(cfg))

	assert.Equal(t, defaultDatabaseManagerIdleTTL, cfg.Database.Manager.IdleTTL)
	assert.Equal(t, defaultDatabaseManagerCleanupInterval, cfg.Database.Manager.CleanupInterval)
	assert.Equal(t, defaultDatabaseManagerMaxSize, cfg.Database.Manager.MaxSize)
	assert.Zero(t, cfg.Databases["reporting"].Manager)
}

// TestCheckSingleTenantConflictAfterNormalize pins the conflict rule through
// the phase boundary: it now runs after root database and messaging
// normalization, so a complete root section alongside static tenants must
// still abort — normalization never adds identity to a section that had none.
func TestCheckSingleTenantConflictAfterNormalize(t *testing.T) {
	t.Run("root_database", func(t *testing.T) {
		cfg := staticMultitenantFixture()
		cfg.Database = createValidDatabaseConfig()

		err := Validate(cfg)
		require.Error(t, err)
		var cfgErr *ConfigError
		require.ErrorAs(t, err, &cfgErr)
		assert.Equal(t, fieldDatabase, cfgErr.Field)
		assert.Contains(t, err.Error(), "not allowed when static tenants are configured")
	})

	// A static source with no tenant map has no static tenants: the root
	// database stays legal, so the gate must be "at least one", not "any map".
	t.Run("no_tenant_map_permits_root_database", func(t *testing.T) {
		cfg := staticMultitenantFixture()
		cfg.Multitenant.Tenants = nil
		cfg.Database = createValidDatabaseConfig()

		require.NoError(t, Validate(cfg))
	})

	t.Run("root_messaging", func(t *testing.T) {
		cfg := staticMultitenantFixture()
		cfg.Messaging.Broker.URL = "amqp://guest:guest@localhost:5672/"

		err := Validate(cfg)
		require.Error(t, err)
		var cfgErr *ConfigError
		require.ErrorAs(t, err, &cfgErr)
		assert.Equal(t, fieldMessaging, cfgErr.Field)
		assert.Contains(t, err.Error(), "not allowed when static tenants are configured")
	})
}

// TestNormalizeModeDefaults pins whole-Config test 5: Multitenant.Enabled
// selects the manager/cache/messaging mode-dependent defaults through the full
// Validate entry point — single-tenant stamps the flat pool defaults,
// multi-tenant preserves zero so the manager builders scale to the tenant
// limit.
func TestNormalizeModeDefaults(t *testing.T) {
	t.Run("single_tenant", func(t *testing.T) {
		cfg := singleTenantFixture()
		cfg.Cache = CacheConfig{Enabled: true, Type: CacheTypeRedis, Redis: RedisConfig{Host: "localhost"}}

		require.NoError(t, Validate(cfg))

		assert.Equal(t, defaultCacheMaxSize, cfg.Cache.Manager.MaxSize)
		assert.Equal(t, defaultMaxPublishers, cfg.Messaging.Publisher.MaxCached)
		assert.Equal(t, defaultPublisherIdleTTL, cfg.Messaging.Publisher.IdleTTL)
		assert.Equal(t, defaultDatabaseManagerIdleTTL, cfg.Database.Manager.IdleTTL)
	})

	t.Run("multi_tenant", func(t *testing.T) {
		cfg := staticMultitenantFixture()
		cfg.Cache = CacheConfig{Enabled: true, Type: CacheTypeRedis, Redis: RedisConfig{Host: "localhost"}}

		require.NoError(t, Validate(cfg))

		assert.Zero(t, cfg.Cache.Manager.MaxSize, "preserved so the cache pool scales to the tenant limit")
		assert.Zero(t, cfg.Messaging.Publisher.MaxCached, "preserved so the publisher pool scales to the tenant limit")
		assert.Equal(t, defaultPublisherIdleTTLMultiTenant, cfg.Messaging.Publisher.IdleTTL)
		assert.Equal(t, defaultDatabaseManagerIdleTTLMultiTenant, cfg.Database.Manager.IdleTTL)
	})
}

// TestNormalizeDisabledCacheCarriesRedisDefaults pins decision 13: Redis
// defaults are filled unconditionally, even when the cache is disabled —
// closing the top-level gap where an enabled hand-built cache with a zero
// port/poolsize used to fail while koanf already gives 6379/10.
func TestNormalizeDisabledCacheCarriesRedisDefaults(t *testing.T) {
	t.Run("disabled_cache_carries_redis_defaults", func(t *testing.T) {
		cfg := createValidFullConfig() // cache disabled

		require.NoError(t, Validate(cfg))

		assert.Equal(t, defaultRedisPort, cfg.Cache.Redis.Port)
		assert.Equal(t, defaultRedisPoolSize, cfg.Cache.Redis.PoolSize)
		assert.Equal(t, defaultRedisDialTimeout, cfg.Cache.Redis.DialTimeout)
	})

	t.Run("enabled_hand_built_zero_port_passes", func(t *testing.T) {
		cfg := createValidFullConfig()
		cfg.Cache = CacheConfig{Enabled: true, Type: CacheTypeRedis, Redis: RedisConfig{Host: "localhost"}}

		require.NoError(t, Validate(cfg))
		assert.Equal(t, defaultRedisPort, cfg.Cache.Redis.Port)
	})
}

// TestCheckSeesNormalizedValues pins whole-Config test 7: check's cross-field
// rules run against normalize's filled defaults, not zero — messaging
// reconnect.maxdelay >= reconnect.delay compares the filled Delay, proving
// check ran after normalize.
func TestCheckSeesNormalizedValues(t *testing.T) {
	cfg := createValidFullConfig()
	cfg.Messaging.Reconnect.MaxDelay = time.Second // Delay left zero: normalize fills 5s

	err := Validate(cfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "messaging.reconnect.maxdelay")
	assert.Contains(t, err.Error(), "must be >= messaging.reconnect.delay")
}
