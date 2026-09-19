package redis

import (
	"reflect"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/cache"
	"github.com/gaborage/go-bricks/config"
)

// settableCacheKeys walks config.CacheConfig's koanf tags into the dotted key set an
// operator can actually set under the cache section. It is the oracle for every field
// this package addresses a ConfigError to: app.qualifyRedisClientError re-heads that
// field under "cache.", so a field absent here names a key nobody can write.
//
// The walk starts at the cache section rather than its redis sub-block on purpose: the
// load-timeout bound is copied from cache.loadtimeout, one level above redis, and a
// redis-only walk would either miss it or reject it wrongly.
func settableCacheKeys() map[string]struct{} {
	keys := make(map[string]struct{})

	var walk func(rt reflect.Type, prefix string)
	walk = func(rt reflect.Type, prefix string) {
		for i := range rt.NumField() {
			f := rt.Field(i)
			tag := f.Tag.Get("koanf")
			if tag == "" {
				continue
			}
			key := prefix + tag
			keys[key] = struct{}{}

			ft := f.Type
			for ft.Kind() == reflect.Pointer {
				ft = ft.Elem()
			}
			if ft.Kind() == reflect.Struct {
				walk(ft, key+".")
			}
		}
	}
	walk(reflect.TypeOf(config.CacheConfig{}), "")

	return keys
}

// TestSettableCacheKeysOracle pins the oracle itself before anything asserts against it:
// an empty or redis-only key set would make every field assertion below pass vacuously.
func TestSettableCacheKeysOracle(t *testing.T) {
	keys := settableCacheKeys()

	assert.Contains(t, keys, "redis.poolsize")
	assert.Contains(t, keys, "redis.tls.cafile")
	assert.Contains(t, keys, "loadtimeout", "the load-timeout key sits on the cache section, not under redis")
	assert.NotContains(t, keys, "redis.pool_size")
	assert.NotContains(t, keys, "redis.loadtimeout")
}

// TestRedisConfigErrorFieldsAreSettableKeys proves every validation failure this package
// raises is addressed to a key an operator can set, by reflection over the operator-facing
// config structs rather than a list of literals — so a field added tomorrow with a
// mismatched error spelling fails here.
func TestRedisConfigErrorFieldsAreSettableKeys(t *testing.T) {
	valid := func() Config {
		return Config{Host: "localhost", Port: 6379, PoolSize: 10}
	}

	tests := []struct {
		name   string
		mutate func(c *Config)
	}{
		{name: "missing_host", mutate: func(c *Config) { c.Host = "" }},
		{name: "invalid_port", mutate: func(c *Config) { c.Port = 70000 }},
		{name: "unknown_mode", mutate: func(c *Config) { c.Mode = "sentinel" }},
		{name: "cluster_with_database", mutate: func(c *Config) { c.Mode = ModeCluster; c.Database = 1 }},
		{name: "whitespace_username", mutate: func(c *Config) { c.Username = " " }},
		{name: "username_without_password", mutate: func(c *Config) { c.Username = "app" }},
		{name: "database_out_of_range", mutate: func(c *Config) { c.Database = 16 }},
		{name: "invalid_pool_size", mutate: func(c *Config) { c.PoolSize = 0 }},
		{name: "negative_dial_timeout", mutate: func(c *Config) { c.DialTimeout = -time.Second }},
		{name: "read_timeout_below_minus_one", mutate: func(c *Config) { c.ReadTimeout = -2 }},
		{name: "write_timeout_below_minus_one", mutate: func(c *Config) { c.WriteTimeout = -2 }},
		{name: "negative_load_timeout", mutate: func(c *Config) { c.LoadTimeout = -time.Millisecond }},
		{name: "staged_tls_material_while_disabled", mutate: func(c *Config) { c.TLS.CAFile = "/etc/ca.pem" }},
	}

	keys := settableCacheKeys()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := valid()
			tt.mutate(&cfg)

			err := cfg.Validate()
			require.Error(t, err)

			var cfgErr *cache.ConfigError
			require.ErrorAs(t, err, &cfgErr)
			assert.Contains(t, keys, cfgErr.Field,
				"error field %q qualifies to cache.%s, which no operator can set", cfgErr.Field, cfgErr.Field)
		})
	}
}

// TestRedisConfigCarriesNoInjectionTags pins that the transport config stays out of the
// consumer-facing injector's tag family: nothing ever hands this struct to
// config.Config.InjectInto, so a tag here documents a key that does not exist.
func TestRedisConfigCarriesNoInjectionTags(t *testing.T) {
	for _, rt := range []reflect.Type{reflect.TypeOf(Config{}), reflect.TypeOf(TLSConfig{})} {
		t.Run(rt.Name(), func(t *testing.T) {
			for i := range rt.NumField() {
				f := rt.Field(i)
				for _, tag := range []string{"config", "required", "default"} {
					_, ok := f.Tag.Lookup(tag)
					assert.False(t, ok, "%s.%s carries a dead %s: tag", rt.Name(), f.Name, tag)
				}
			}
		})
	}
}
