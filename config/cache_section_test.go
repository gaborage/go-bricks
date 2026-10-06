package config

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	gbtesting "github.com/gaborage/go-bricks/testing"
)

// writeTestCAFile mints a self-signed certificate and writes it as a PEM file,
// the shape cache.redis.tls.cafile carries.
func writeTestCAFile(t *testing.T) string {
	t.Helper()
	certPEM, _ := gbtesting.SelfSignedCertKeyPEM(t)
	path := filepath.Join(t.TempDir(), "ca.pem")
	require.NoError(t, os.WriteFile(path, certPEM, 0o600))
	return path
}

// writeTestClientPair mints a certificate/key pair and writes both as PEM files,
// the shape cache.redis.tls.certfile/keyfile carry.
func writeTestClientPair(t *testing.T) (certPath, keyPath string) {
	t.Helper()
	certPEM, keyPEM := gbtesting.SelfSignedCertKeyPEM(t)
	dir := t.TempDir()
	certPath = filepath.Join(dir, "client.pem")
	keyPath = filepath.Join(dir, "client.key")
	require.NoError(t, os.WriteFile(certPath, certPEM, 0o600))
	require.NoError(t, os.WriteFile(keyPath, keyPEM, 0o600))
	return certPath, keyPath
}

func TestValidateCacheDisabled(t *testing.T) {
	cfg := CacheConfig{Enabled: false}
	err := checkCache(&cfg)
	assert.NoError(t, err)
}

// TestNormalizeCacheLeavesDisabledManagerBlockAlone pins the first link of the chain
// wiki/cache.md documents: a disabled cache's manager values are neither defaulted nor
// judged here, so a negative one reaches CreateCacheManager as written (ADR-054).
func TestNormalizeCacheLeavesDisabledManagerBlockAlone(t *testing.T) {
	manager := CacheManagerConfig{MaxSize: -1, IdleTTL: -time.Minute, CleanupInterval: -time.Minute}
	cfg := CacheConfig{Manager: manager}

	require.NoError(t, normalizeCache(&cfg, false))
	assert.Equal(t, manager, cfg.Manager)
}

func TestValidateCacheSuccess(t *testing.T) {
	cfg := CacheConfig{
		Enabled: true,
		Type:    "redis",
		Redis: RedisConfig{
			Host:            "localhost",
			Port:            6379,
			Password:        "secret",
			Database:        0,
			PoolSize:        10,
			DialTimeout:     5 * time.Second,
			ReadTimeout:     3 * time.Second,
			WriteTimeout:    3 * time.Second,
			MaxRetries:      3,
			MinRetryBackoff: 8 * time.Millisecond,
			MaxRetryBackoff: 512 * time.Millisecond,
		},
	}

	err := checkCache(&cfg)
	assert.NoError(t, err)
}

func TestValidateCacheTypeFailures(t *testing.T) {
	tests := []struct {
		name          string
		cacheType     string
		expectedError string
	}{
		{
			name:          "invalid_type",
			cacheType:     "memcached",
			expectedError: cacheTypeField,
		},
		{
			name:          "empty_type",
			cacheType:     "",
			expectedError: cacheTypeField,
		},
		{
			name:          "uppercase_type",
			cacheType:     "REDIS",
			expectedError: cacheTypeField,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := CacheConfig{
				Enabled: true,
				Type:    tt.cacheType,
			}

			err := checkCache(&cfg)
			require.ErrorContains(t, err, tt.expectedError)
		})
	}
}

func TestValidateRedisCacheFailures(t *testing.T) {
	tests := []struct {
		name          string
		redis         RedisConfig
		expectedError string
	}{
		{
			name: "missing_host",
			redis: RedisConfig{
				Host: "",
				Port: 6379,
			},
			expectedError: "cache.redis.host",
		},
		{
			name: "invalid_port_zero",
			redis: RedisConfig{
				Host: "localhost",
				Port: 0,
			},
			expectedError: redisPortField,
		},
		{
			name: "invalid_port_negative",
			redis: RedisConfig{
				Host: "localhost",
				Port: -1,
			},
			expectedError: redisPortField,
		},
		{
			name: "invalid_port_too_high",
			redis: RedisConfig{
				Host: "localhost",
				Port: 99999,
			},
			expectedError: redisPortField,
		},
		{
			name: "invalid_database_negative",
			redis: RedisConfig{
				Host:     "localhost",
				Port:     6379,
				Database: -1,
			},
			expectedError: "cache.redis.database",
		},
		{
			name: "invalid_database_too_high",
			redis: RedisConfig{
				Host:     "localhost",
				Port:     6379,
				Database: 16,
			},
			expectedError: "cache.redis.database",
		},
		{
			name: "invalid_pool_size_zero",
			redis: RedisConfig{
				Host:     "localhost",
				Port:     6379,
				PoolSize: 0,
			},
			expectedError: "cache.redis.poolsize",
		},
		{
			name: "invalid_pool_size_negative",
			redis: RedisConfig{
				Host:     "localhost",
				Port:     6379,
				PoolSize: -1,
			},
			expectedError: "cache.redis.poolsize",
		},
		{
			name: "invalid_dial_timeout_negative",
			redis: RedisConfig{
				Host:        "localhost",
				Port:        6379,
				PoolSize:    10,
				DialTimeout: -1 * time.Second,
			},
			expectedError: "cache.redis.dialtimeout",
		},
		{
			name: "invalid_read_timeout_too_negative",
			redis: RedisConfig{
				Host:        "localhost",
				Port:        6379,
				PoolSize:    10,
				ReadTimeout: -2 * time.Second,
			},
			expectedError: "cache.redis.readtimeout",
		},
		{
			name: "invalid_write_timeout_too_negative",
			redis: RedisConfig{
				Host:         "localhost",
				Port:         6379,
				PoolSize:     10,
				WriteTimeout: -2 * time.Second,
			},
			expectedError: "cache.redis.writetimeout",
		},
		{
			name: "tls_material_staged_while_disabled",
			redis: RedisConfig{
				Host:     "localhost",
				Port:     6379,
				PoolSize: 10,
				TLS:      RedisTLSConfig{Enabled: false, CAFile: "/etc/ssl/ca.pem"},
			},
			expectedError: "cache.redis.tls.enabled",
		},
		{
			name: "tls_ca_from_both_sources",
			redis: RedisConfig{
				Host:     "localhost",
				Port:     6379,
				PoolSize: 10,
				TLS:      RedisTLSConfig{Enabled: true, CAFile: "/etc/ssl/ca.pem", CAValue: "cGVt"},
			},
			expectedError: "cache.redis.tls.cafile",
		},
		{
			name: "tls_cert_without_key",
			redis: RedisConfig{
				Host:     "localhost",
				Port:     6379,
				PoolSize: 10,
				TLS:      RedisTLSConfig{Enabled: true, CertFile: "/etc/ssl/client.pem"},
			},
			expectedError: "cache.redis.tls.keyfile",
		},
		{
			name: "tls_key_without_cert",
			redis: RedisConfig{
				Host:     "localhost",
				Port:     6379,
				PoolSize: 10,
				TLS:      RedisTLSConfig{Enabled: true, KeyValue: "cGVt"},
			},
			expectedError: "cache.redis.tls.certfile",
		},
		{
			name: "tls_minversion_below_floor",
			redis: RedisConfig{
				Host:     "localhost",
				Port:     6379,
				PoolSize: 10,
				TLS:      RedisTLSConfig{Enabled: true, MinVersion: "1.1"},
			},
			expectedError: "cache.redis.tls.minversion",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := CacheConfig{
				Enabled: true,
				Type:    "redis",
				Redis:   tt.redis,
			}

			err := checkCache(&cfg)
			require.ErrorContains(t, err, tt.expectedError)
		})
	}
}

func TestValidateRedisCacheEdgeCases(t *testing.T) {
	// Validation now loads the material, so the full-material case needs a real
	// bundle and a real pair rather than placeholder paths.
	caPEM, _ := gbtesting.SelfSignedCertKeyPEM(t)
	clientCert, clientKey := writeTestClientPair(t)

	tests := []struct {
		name  string
		redis RedisConfig
		valid bool
	}{
		{
			name: "read_timeout_disabled",
			redis: RedisConfig{
				Host:        "localhost",
				Port:        6379,
				PoolSize:    10,
				ReadTimeout: -1,
			},
			valid: true,
		},
		{
			name: "write_timeout_disabled",
			redis: RedisConfig{
				Host:         "localhost",
				Port:         6379,
				PoolSize:     10,
				WriteTimeout: -1,
			},
			valid: true,
		},
		{
			name: "dial_timeout_zero",
			redis: RedisConfig{
				Host:        "localhost",
				Port:        6379,
				PoolSize:    10,
				DialTimeout: 0,
			},
			valid: true,
		},
		{
			name: "database_max_valid",
			redis: RedisConfig{
				Host:     "localhost",
				Port:     6379,
				PoolSize: 10,
				Database: 15,
			},
			valid: true,
		},
		{
			// The upper bound is inclusive: 65535 is a port, not one past the end.
			name: "port_max_valid",
			redis: RedisConfig{
				Host:     "localhost",
				Port:     65535,
				PoolSize: 10,
			},
			valid: true,
		},
		{
			// An enabled block with no material at all verifies against the
			// system roots — the common managed-Redis shape.
			name: "tls_enabled_without_material",
			redis: RedisConfig{
				Host:     "localhost",
				Port:     6379,
				PoolSize: 10,
				TLS:      RedisTLSConfig{Enabled: true},
			},
			valid: true,
		},
		{
			name: "tls_minversion_12",
			redis: RedisConfig{
				Host:     "localhost",
				Port:     6379,
				PoolSize: 10,
				TLS:      RedisTLSConfig{Enabled: true, MinVersion: "1.2"},
			},
			valid: true,
		},
		{
			name: "tls_minversion_13_with_full_material",
			redis: RedisConfig{
				Host:     "localhost",
				Port:     6379,
				PoolSize: 10,
				TLS: RedisTLSConfig{
					Enabled:    true,
					CAValue:    base64.StdEncoding.EncodeToString(caPEM),
					CertFile:   clientCert,
					KeyFile:    clientKey,
					ServerName: "redis.internal",
					MinVersion: "1.3",
				},
			},
			valid: true,
		},
		{
			name: "tls_disabled_and_empty",
			redis: RedisConfig{
				Host:     "localhost",
				Port:     6379,
				PoolSize: 10,
				TLS:      RedisTLSConfig{},
			},
			valid: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := CacheConfig{
				Enabled: true,
				Type:    "redis",
				Redis:   tt.redis,
			}

			err := checkCache(&cfg)
			if tt.valid {
				assert.NoError(t, err)
			} else {
				assert.Error(t, err)
			}
		})
	}
}

func TestNormalizeCacheLoadTimeout(t *testing.T) {
	tests := []struct {
		name    string
		enabled bool
		given   time.Duration
		want    time.Duration
		wantErr bool
	}{
		{name: "absent_takes_the_default", enabled: true, given: 0, want: 500 * time.Millisecond},
		{name: "explicit_zero_takes_the_default", enabled: false, given: 0, want: 500 * time.Millisecond},
		{name: "operator_value_is_kept", enabled: true, given: 250 * time.Millisecond, want: 250 * time.Millisecond},
		{name: "disabled_cache_is_normalized_too", enabled: false, given: 3 * time.Second, want: 3 * time.Second},
		{name: "negative_is_rejected", enabled: true, given: -time.Millisecond, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &CacheConfig{Enabled: tt.enabled, Type: CacheTypeRedis, LoadTimeout: tt.given}
			cfg.Redis.Host = "localhost"
			err := normalizeCache(cfg, false)
			if tt.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), "cache.loadtimeout")
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, cfg.LoadTimeout)
		})
	}
}

// TestLoadRedisTLSFromYAML pins the koanf key path for the new block: the
// struct tags must spell cache.redis.tls.* or an operator's YAML lands nowhere.
func TestLoadRedisTLSFromYAML(t *testing.T) {
	clearEnvironmentVariables()
	defer clearEnvironmentVariables()

	dir := t.TempDir()
	// The cafile must resolve: validation loads the material, so a placeholder
	// path would fail the Load this test is about.
	caFile := writeTestCAFile(t)
	yamlBody := "cache:\n" +
		"  enabled: true\n" +
		"  redis:\n" +
		"    host: localhost\n" +
		"    tls:\n" +
		"      enabled: true\n" +
		"      cafile: " + caFile + "\n" +
		"      servername: redis.internal\n" +
		"      minversion: \"1.3\"\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, testConfigFileYAML), []byte(yamlBody), 0o600))
	t.Chdir(dir)

	cfg, err := Load()
	require.NoError(t, err)
	assert.True(t, cfg.Cache.Redis.TLS.Enabled)
	assert.Equal(t, caFile, cfg.Cache.Redis.TLS.CAFile)
	assert.Equal(t, "redis.internal", cfg.Cache.Redis.TLS.ServerName)
	assert.Equal(t, "1.3", cfg.Cache.Redis.TLS.MinVersion)
}

// TestValidateRedisTLSLoadsMaterial pins that the structural pass is not the
// whole check: an enabled block naming a file that is not there fails config
// validation, addressed to cache.redis.tls, rather than booting green and
// failing on a tenant's first request.
func TestValidateRedisTLSLoadsMaterial(t *testing.T) {
	cfg := CacheConfig{
		Enabled: true,
		Type:    CacheTypeRedis,
		Redis: RedisConfig{
			Host:     "localhost",
			Port:     6379,
			PoolSize: 10,
			TLS:      RedisTLSConfig{Enabled: true, CAFile: filepath.Join(t.TempDir(), "absent-ca.pem")},
		},
	}

	err := checkCache(&cfg)

	require.Error(t, err)
	var cfgErr *ConfigError
	require.ErrorAs(t, err, &cfgErr)
	assert.Equal(t, "cache.redis.tls", cfgErr.Field)
	assert.Contains(t, err.Error(), "absent-ca.pem")
}

// TestValidateRedisTLSAcceptsLoadableMaterial is the other half: a cafile that
// really is a PEM bundle passes, so the load is a check and not a blanket
// rejection of file-sourced material.
func TestValidateRedisTLSAcceptsLoadableMaterial(t *testing.T) {
	cfg := CacheConfig{
		Enabled: true,
		Type:    CacheTypeRedis,
		Redis: RedisConfig{
			Host:     "localhost",
			Port:     6379,
			PoolSize: 10,
			TLS:      RedisTLSConfig{Enabled: true, CAFile: writeTestCAFile(t), MinVersion: "1.3"},
		},
	}

	assert.NoError(t, checkCache(&cfg))
}

// TestValidateCacheRedisUsername pins cache.redis.username as an identity that
// cannot stand alone: go-redis builds the AUTH clause only when the password is
// non-empty, so a name with an empty password sends no AUTH at all and the
// connection silently runs as whatever identity the endpoint hands an
// unauthenticated client. That pair is refused naming both keys. A password
// alone selects the implicit "default" user and stays valid, neither key set
// stays valid, and a whitespace-only name — an AUTH argument no ACL rule can
// match — is refused ahead of the coupling rule.
func TestValidateCacheRedisUsername(t *testing.T) {
	tests := []struct {
		name      string
		username  string
		password  string
		wantField string
		wantMsg   string
	}{
		{name: "named_user_with_password", username: "svc", password: "pw"},
		{name: "absent_username_with_password", password: "pw"},
		{name: "neither_username_nor_password"},
		{
			name:      "named_user_without_password",
			username:  "svc",
			wantField: "cache.redis.username",
			wantMsg:   "cache.redis.password",
		},
		{
			name:      "whitespace_only_username_with_password",
			username:  " \t ",
			password:  "pw",
			wantField: "cache.redis.username",
			wantMsg:   "whitespace-only",
		},
		{
			name:      "whitespace_only_username_without_password",
			username:  " \t ",
			wantField: "cache.redis.username",
			wantMsg:   "whitespace-only",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := CacheConfig{
				Enabled: true,
				Type:    CacheTypeRedis,
				Redis: RedisConfig{
					Host:     "localhost",
					Port:     6379,
					PoolSize: 10,
					Username: tt.username,
					Password: tt.password,
				},
			}

			err := checkCache(&cfg)
			if tt.wantField == "" {
				require.NoError(t, err)
				return
			}

			var cfgErr *ConfigError
			require.ErrorAs(t, err, &cfgErr)
			assert.Equal(t, tt.wantField, cfgErr.Field)
			assert.Contains(t, cfgErr.Message, tt.wantMsg,
				"the message must name the rule that fired, not just the field both rules share")
		})
	}
}

// TestValidateCacheRedisMode pins cache.redis.mode as a closed enum. An
// unrecognized value is refused rather than treated as the default, because the
// default dials a single node and a cluster-protocol endpoint answers MOVED to
// the first key — a typo would otherwise surface as a runtime cache failure
// instead of a startup one. The error carries the allowed pair so the operator
// does not have to find the list. The enum is case-sensitive AND untrimmed: the
// env provider hands the value through verbatim and no decode hook trims a
// scalar, so a padded value fails startup rather than degrading to standalone.
func TestValidateCacheRedisMode(t *testing.T) {
	tests := []struct {
		name      string
		mode      string
		wantField string
	}{
		{name: "empty_is_standalone"},
		{name: "standalone_is_accepted", mode: "standalone"},
		{name: "cluster_is_accepted", mode: "cluster"},
		{name: "unknown_mode_is_rejected", mode: "sentinel", wantField: "cache.redis.mode"},
		{name: "capitalised_name_is_rejected", mode: "Cluster", wantField: "cache.redis.mode"},
		{name: "padded_name_is_rejected", mode: " cluster", wantField: "cache.redis.mode"},
		{name: "trailing_newline_is_rejected", mode: "cluster\n", wantField: "cache.redis.mode"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := CacheConfig{
				Enabled: true,
				Type:    CacheTypeRedis,
				Redis: RedisConfig{
					Host:     "localhost",
					Port:     6379,
					PoolSize: 10,
					Mode:     tt.mode,
				},
			}

			err := checkCache(&cfg)
			if tt.wantField == "" {
				require.NoError(t, err)
				return
			}

			var cfgErr *ConfigError
			require.ErrorAs(t, err, &cfgErr)
			assert.Equal(t, tt.wantField, cfgErr.Field)
			assert.Contains(t, cfgErr.Action, "standalone")
			assert.Contains(t, cfgErr.Action, "cluster",
				"the operator must be given the whole allowed list, not just the half it missed")
		})
	}
}

// TestValidateCacheRedisClusterRejectsNonZeroDatabase pins the coupling the
// cluster protocol forces: go-redis drops the selected database on the way to
// the cluster client, so accepting the pair would move a deployment's whole
// keyspace to database 0 on the mode flip alone. The error is addressed to the
// database key and names the mode, because either key could be the one the
// operator meant to change. The rule runs ahead of the 0-15 range check, because
// under cluster that range does not apply at all: an out-of-range database under
// cluster is reported as the coupling, which the message assertion below
// distinguishes from the range error.
func TestValidateCacheRedisClusterRejectsNonZeroDatabase(t *testing.T) {
	tests := []struct {
		name      string
		mode      string
		database  int
		wantField string
	}{
		{name: "standalone_keeps_a_selected_database", mode: "standalone", database: 3},
		{name: "cluster_on_database_zero_is_accepted", mode: "cluster"},
		{name: "cluster_with_a_selected_database_is_rejected", mode: "cluster", database: 3, wantField: "cache.redis.database"},
		{name: "cluster_with_the_last_database_is_rejected", mode: "cluster", database: 15, wantField: "cache.redis.database"},
		{name: "cluster_outranks_the_range_check", mode: "cluster", database: 99, wantField: "cache.redis.database"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := CacheConfig{
				Enabled: true,
				Type:    CacheTypeRedis,
				Redis: RedisConfig{
					Host:     "localhost",
					Port:     6379,
					PoolSize: 10,
					Mode:     tt.mode,
					Database: tt.database,
				},
			}

			err := checkCache(&cfg)
			if tt.wantField == "" {
				require.NoError(t, err)
				return
			}

			var cfgErr *ConfigError
			require.ErrorAs(t, err, &cfgErr)
			assert.Equal(t, tt.wantField, cfgErr.Field)
			assert.Contains(t, cfgErr.Message, "cache.redis.mode",
				"the message must name the other key, so the operator knows which of the two to change")
		})
	}
}

// TestNormalizeCacheRedisModeDefaultsToStandalone pins the fill itself. It
// matters beyond the root section, where koanf would also write the default:
// applyRedisDefaults is the only place a per-tenant cache gets one, so without
// the fill here a tenant's mode would stay empty and no reader could tell an
// absent value from a chosen one.
func TestNormalizeCacheRedisModeDefaultsToStandalone(t *testing.T) {
	tests := []struct {
		name  string
		given string
		want  string
	}{
		{name: "absent_takes_the_default", given: "", want: "standalone"},
		{name: "cluster_is_kept", given: "cluster", want: "cluster"},
		{name: "an_unknown_value_is_left_for_validation", given: "sentinel", want: "sentinel"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &CacheConfig{Enabled: true, Type: CacheTypeRedis}
			cfg.Redis.Host = "localhost"
			cfg.Redis.Mode = tt.given

			require.NoError(t, normalizeCache(cfg, false))
			assert.Equal(t, tt.want, cfg.Redis.Mode)
		})
	}
}

// TestValidateCacheRedisKeyPrefix pins the namespace grammar at the config door.
// An absent key is the app.name default and an explicit empty string is the
// documented opt-out, so both pass; every value that would make the prefix match
// keys it does not own — whitespace, a glob metacharacter, a cluster hash tag, a
// dangling separator — is refused at startup rather than written to Redis.
func TestValidateCacheRedisKeyPrefix(t *testing.T) {
	tests := []struct {
		name      string
		keyPrefix *string
		wantField string
	}{
		{name: "absent_takes_the_app_name_default"},
		{name: "explicit_empty_is_the_opt_out", keyPrefix: new("")},
		{name: "plain_prefix", keyPrefix: new("orders")},
		{name: "whitespace_is_rejected", keyPrefix: new("bad name"), wantField: "cache.redis.keyprefix"},
		{name: "glob_is_rejected", keyPrefix: new("orders*"), wantField: "cache.redis.keyprefix"},
		{name: "hash_tag_is_rejected", keyPrefix: new("{orders}"), wantField: "cache.redis.keyprefix"},
		{name: "glob_escape_is_rejected", keyPrefix: new(`orders\`), wantField: "cache.redis.keyprefix"},
		{name: "trailing_separator_is_rejected", keyPrefix: new("orders:"), wantField: "cache.redis.keyprefix"},
		{name: "inner_separator_is_rejected", keyPrefix: new("orders:v2"), wantField: "cache.redis.keyprefix"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := CacheConfig{
				Enabled: true,
				Type:    CacheTypeRedis,
				Redis: RedisConfig{
					Host:      "localhost",
					Port:      6379,
					PoolSize:  10,
					KeyPrefix: tt.keyPrefix,
				},
			}

			err := checkCache(&cfg)
			if tt.wantField == "" {
				require.NoError(t, err)
				return
			}

			var cfgErr *ConfigError
			require.ErrorAs(t, err, &cfgErr)
			assert.Equal(t, tt.wantField, cfgErr.Field)
		})
	}
}

// TestValidateCacheKeyPrefixDefaultNeedsAValidAppName pins the one cross-section
// rule the default creates: with no cache.redis.keyprefix, app.name IS the cache
// namespace, so a name that cannot be one fails startup instead of failing at the
// first cache access. The rule binds wherever the default applies — the root cache
// and a per-tenant cache alike — and never where an explicit prefix (including the
// empty opt-out) has displaced it.
func TestValidateCacheKeyPrefixDefaultNeedsAValidAppName(t *testing.T) {
	tests := []struct {
		name      string
		appName   string
		enabled   bool
		keyPrefix *string
		wantErr   bool
	}{
		{name: "namespaceable_app_name", appName: "orders", enabled: true},
		{name: "unnamespaceable_app_name_with_cache", appName: "bad name", enabled: true, wantErr: true},
		// A name that is one SEGMENT is the same rule: an app.name carrying ':' would
		// become a two-segment default namespace, which is the ambiguity the prefix
		// grammar removes, so it fails here and asks for an explicit keyprefix.
		{name: "app_name_carrying_a_separator", appName: "orders:v2", enabled: true, wantErr: true},
		{name: "unnamespaceable_app_name_with_explicit_prefix", appName: "bad name", enabled: true, keyPrefix: new("ok")},
		{name: "unnamespaceable_app_name_with_the_opt_out", appName: "bad name", enabled: true, keyPrefix: new("")},
		{name: "unnamespaceable_app_name_without_cache", appName: "bad name"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &Config{
				App:    createValidAppConfig(),
				Server: createValidServerConfig(),
				Log:    createValidLogConfig(),
				Cache: CacheConfig{
					Enabled: tt.enabled,
					Type:    CacheTypeRedis,
					Redis:   RedisConfig{Host: "localhost", KeyPrefix: tt.keyPrefix},
				},
			}
			cfg.App.Name = tt.appName

			err := Validate(cfg)
			if !tt.wantErr {
				require.NoError(t, err)
				return
			}

			var cfgErr *ConfigError
			require.ErrorAs(t, err, &cfgErr)
			assert.Equal(t, "app.name", cfgErr.Field, "the fault is the name, not the cache key that was never set")
			assert.Contains(t, cfgErr.Action, "cache.redis.keyprefix", "the operator must be offered the override as the fix")
		})
	}
}

// TestLoadRedisKeyPrefixTriState proves the loader keeps the three arms apart, which is
// what the *string is for: an absent key stays nil so the app.name default can apply, an
// explicit empty string arrives as a non-nil empty value (the opt-out), and a value
// arrives as itself — from YAML and from the environment alike.
func TestLoadRedisKeyPrefixTriState(t *testing.T) {
	load := func(t *testing.T, redisLines string, env map[string]string) *Config {
		t.Helper()
		cfg, err := loadDeliveredEmptyFixture(t, "cache:\n  enabled: true\n  redis:\n    host: localhost\n"+redisLines, env)
		require.NoError(t, err)
		return cfg
	}

	t.Run("absent_stays_nil", func(t *testing.T) {
		cfg := load(t, "", nil)

		assert.Nil(t, cfg.Cache.Redis.KeyPrefix, "an absent key must stay absent, or app.name can never apply")
	})

	t.Run("explicit_empty_is_delivered", func(t *testing.T) {
		cfg := load(t, "    keyprefix: \"\"\n", nil)

		require.NotNil(t, cfg.Cache.Redis.KeyPrefix, "the opt-out must be distinguishable from an absent key")
		assert.Empty(t, *cfg.Cache.Redis.KeyPrefix)
	})

	t.Run("value_is_delivered", func(t *testing.T) {
		cfg := load(t, "    keyprefix: orders\n", nil)

		require.NotNil(t, cfg.Cache.Redis.KeyPrefix)
		assert.Equal(t, "orders", *cfg.Cache.Redis.KeyPrefix)
	})

	t.Run("environment_overrides_yaml", func(t *testing.T) {
		cfg := load(t, "    keyprefix: orders\n", map[string]string{"CACHE_REDIS_KEYPREFIX": "from-env"})

		require.NotNil(t, cfg.Cache.Redis.KeyPrefix)
		assert.Equal(t, "from-env", *cfg.Cache.Redis.KeyPrefix)
	})
}

func TestResolveCacheSectionForKeyFillsPortForUnvalidatedTenant(t *testing.T) {
	section := &CacheConfig{Enabled: true, Redis: RedisConfig{Host: "redis.acme.internal"}}

	got, err := ResolveCacheSectionForKey(section, "acme")

	require.NoError(t, err)
	assert.Equal(t, 6379, got.Redis.Port)
	assert.Equal(t, 0, section.Redis.Port)
}

// resolvedCacheSectionDefaults is the section the door hands back for an enabled section that
// named only its host: every literal the Normalization step fills.
func resolvedCacheSectionDefaults() CacheConfig {
	return CacheConfig{
		Enabled: true,
		Type:    "redis",
		Redis: RedisConfig{
			Host:            "redis.acme.internal",
			Mode:            "standalone",
			Port:            6379,
			PoolSize:        10,
			DialTimeout:     5 * time.Second,
			ReadTimeout:     3 * time.Second,
			WriteTimeout:    3 * time.Second,
			MaxRetries:      3,
			MinRetryBackoff: 8 * time.Millisecond,
			MaxRetryBackoff: 512 * time.Millisecond,
		},
		LoadTimeout: 500 * time.Millisecond,
	}
}

func TestResolveCacheSectionForKeyNormalizes(t *testing.T) {
	hostOnly := func() *CacheConfig {
		return &CacheConfig{Enabled: true, Redis: RedisConfig{Host: "redis.acme.internal"}}
	}
	validated := func() *CacheConfig {
		section := resolvedCacheSectionDefaults()
		return &section
	}
	cluster := func() *CacheConfig {
		return &CacheConfig{Enabled: true, Redis: RedisConfig{Host: "redis.acme.internal", Mode: "cluster"}}
	}
	clusterWant := resolvedCacheSectionDefaults()
	clusterWant.Redis.Mode = "cluster"

	tests := []struct {
		name    string
		key     string
		section func() *CacheConfig
		want    CacheConfig
	}{
		{name: "root_unvalidated", key: "", section: hostOnly, want: resolvedCacheSectionDefaults()},
		{name: "tenant_unvalidated", key: "acme", section: hostOnly, want: resolvedCacheSectionDefaults()},
		{name: "root_validated_is_a_no_op", key: "", section: validated, want: resolvedCacheSectionDefaults()},
		{name: "tenant_validated_is_a_no_op", key: "acme", section: validated, want: resolvedCacheSectionDefaults()},
		{name: "tenant_cluster_gets_retries", key: "acme", section: cluster, want: clusterWant},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ResolveCacheSectionForKey(tt.section(), tt.key)

			require.NoError(t, err)
			assert.Equal(t, tt.want, *got)
		})
	}
}

func TestResolveCacheSectionForKeyRefuses(t *testing.T) {
	tests := []struct {
		name       string
		key        string
		section    *CacheConfig
		wantField  string
		wantCat    string
		wantMsg    string
		wantAction string
	}{
		{
			name:      "tenant_nil",
			key:       "acme",
			section:   nil,
			wantField: "multitenant.tenants.acme.cache",
			wantCat:   "invalid",
			wantMsg:   "configuration is nil for key 'acme'",
		},
		{
			name:      "root_nil",
			key:       "",
			section:   nil,
			wantField: "cache",
			wantCat:   "invalid",
			wantMsg:   "configuration is nil for key ''",
		},
		{
			name:      "tenant_disabled",
			key:       "acme",
			section:   &CacheConfig{Type: "redis"},
			wantField: "multitenant.tenants.acme.cache",
			wantCat:   "not_configured",
			wantMsg:   "(optional)",
			wantAction: "to enable: set MULTITENANT_TENANTS_ACME_CACHE_ENABLED env var or add " +
				"multitenant.tenants.acme.cache.enabled to config.yaml",
		},
		{
			name:       "root_disabled",
			key:        "",
			section:    &CacheConfig{Type: "redis"},
			wantField:  "cache",
			wantCat:    "not_configured",
			wantMsg:    "(optional)",
			wantAction: "to enable: set CACHE_ENABLED env var or add cache.enabled to config.yaml",
		},
		{
			name:       "tenant_unsupported_type",
			key:        "acme",
			section:    &CacheConfig{Enabled: true, Type: "memcached", Redis: RedisConfig{Host: "redis.acme.internal"}},
			wantField:  "multitenant.tenants.acme.cache.type",
			wantCat:    "invalid",
			wantMsg:    "'memcached' is not supported",
			wantAction: "must be one of: redis",
		},
		{
			name:      "tenant_empty_host",
			key:       "acme",
			section:   &CacheConfig{Enabled: true},
			wantField: "multitenant.tenants.acme.cache.redis.host",
			wantCat:   "missing",
			wantMsg:   "required",
			wantAction: "set MULTITENANT_TENANTS_ACME_CACHE_REDIS_HOST env var or add " +
				"multitenant.tenants.acme.cache.redis.host to config.yaml",
		},
		{
			name:       "root_empty_host",
			key:        "",
			section:    &CacheConfig{Enabled: true},
			wantField:  "cache.redis.host",
			wantCat:    "missing",
			wantMsg:    "required",
			wantAction: "set CACHE_REDIS_HOST env var or add cache.redis.host to config.yaml",
		},
		{
			name:       "tenant_port_out_of_range",
			key:        "acme",
			section:    &CacheConfig{Enabled: true, Redis: RedisConfig{Host: "redis.acme.internal", Port: 99999}},
			wantField:  "multitenant.tenants.acme.cache.redis.port",
			wantCat:    "invalid",
			wantMsg:    "invalid value: 99999",
			wantAction: "must be one of: 1-65535",
		},
		{
			name:      "tenant_negative_loadtimeout",
			key:       "acme",
			section:   &CacheConfig{Enabled: true, Redis: RedisConfig{Host: "redis.acme.internal"}, LoadTimeout: -time.Second},
			wantField: "multitenant.tenants.acme.cache.loadtimeout",
			wantCat:   "invalid",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ResolveCacheSectionForKey(tt.section, tt.key)

			assert.Nil(t, got)
			var cfgErr *ConfigError
			require.ErrorAs(t, err, &cfgErr)
			assert.Equal(t, tt.wantField, cfgErr.Field)
			assert.Equal(t, tt.wantCat, cfgErr.Category)
			if tt.wantMsg != "" {
				assert.Equal(t, tt.wantMsg, cfgErr.Message)
			}
			assert.Equal(t, tt.wantAction, cfgErr.Action)
		})
	}
}

func TestResolveCacheSectionForKeyCarriesExplicitKeyPrefixUnchecked(t *testing.T) {
	for _, prefix := range []string{"orders:v2", ""} {
		t.Run("prefix_"+prefix, func(t *testing.T) {
			section := &CacheConfig{Enabled: true, Redis: RedisConfig{Host: "redis.acme.internal", KeyPrefix: &prefix}}

			got, err := ResolveCacheSectionForKey(section, "acme")

			require.NoError(t, err)
			require.NotNil(t, got.Redis.KeyPrefix)
			assert.Equal(t, prefix, *got.Redis.KeyPrefix)
		})
	}
}

func TestResolveCacheSectionForKeySharesNoMemoryWithItsInput(t *testing.T) {
	prefix := "orders"
	section := &CacheConfig{Enabled: true, Redis: RedisConfig{Host: "redis.acme.internal", KeyPrefix: &prefix}}

	got, err := ResolveCacheSectionForKey(section, "acme")
	require.NoError(t, err)

	prefix = "billing"
	section.Redis.Host = "redis.billing.internal"

	assert.Equal(t, "orders", *got.Redis.KeyPrefix)
	assert.Equal(t, "redis.acme.internal", got.Redis.Host)
}

// TestResolveCacheSectionForKeyLeavesTLSFileReadToTheDial pins the one startup rule the door
// skips besides the keyprefix grammar: it judges the TLS block's shape but reads no file.
func TestResolveCacheSectionForKeyLeavesTLSFileReadToTheDial(t *testing.T) {
	section := &CacheConfig{Enabled: true, Redis: RedisConfig{
		Host: "redis.acme.internal",
		TLS:  RedisTLSConfig{Enabled: true, CAFile: filepath.Join(t.TempDir(), "missing-ca.pem")},
	}}

	_, err := ResolveCacheSectionForKey(section, "acme")

	assert.NoError(t, err)
}

func TestResolveCacheSectionForKeyChecksTLSShape(t *testing.T) {
	section := &CacheConfig{Enabled: true, Redis: RedisConfig{
		Host: "redis.acme.internal",
		TLS:  RedisTLSConfig{Enabled: true, CAFile: "/etc/ca.pem", CAValue: "inline"},
	}}

	_, err := ResolveCacheSectionForKey(section, "acme")

	var cfgErr *ConfigError
	require.ErrorAs(t, err, &cfgErr)
	assert.Equal(t, "multitenant.tenants.acme.cache.redis.tls.cafile", cfgErr.Field)
}

// TestNormalizeCacheFillsEmptyRootType pins the startup half of the shared Normalization
// step: a hand-built root with no type is filled with redis instead of refused.
func TestNormalizeCacheFillsEmptyRootType(t *testing.T) {
	cfg := CacheConfig{Enabled: true, Redis: RedisConfig{Host: "redis.internal"}}

	require.NoError(t, normalizeCache(&cfg, false))
	require.NoError(t, checkCache(&cfg))
	assert.Equal(t, "redis", cfg.Type)
}
