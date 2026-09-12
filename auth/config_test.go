package auth

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/config"
)

const (
	testIssuer   = "https://issuer.example.com"
	testAudience = "api://orders"
	testJWKSURI  = "https://issuer.example.com/.well-known/jwks.json"
)

func validConfig() Config {
	return Config{
		Issuer:                 testIssuer,
		Audience:               []string{testAudience},
		JWKSURI:                testJWKSURI,
		Algorithms:             []string{AlgRS256, AlgPS256},
		Leeway:                 30 * time.Second,
		JWKSTTL:                15 * time.Minute,
		JWKSStaleCeiling:       time.Hour,
		JWKSMinRefreshInterval: 30 * time.Second,
		JWKSMaxBodyBytes:       1048576,
	}
}

func TestConfigValidateAcceptsAFullyPopulatedConfig(t *testing.T) {
	cfg := validConfig()

	assert.NoError(t, cfg.Validate())
}

func TestConfigValidateAcceptsAZeroLeewayAndAnEqualStaleCeiling(t *testing.T) {
	cfg := validConfig()
	cfg.Leeway = 0
	cfg.JWKSStaleCeiling = cfg.JWKSTTL

	assert.NoError(t, cfg.Validate())
}

func TestConfigValidateAcceptsASingleAlgorithm(t *testing.T) {
	for _, alg := range []string{AlgRS256, AlgPS256} {
		cfg := validConfig()
		cfg.Algorithms = []string{alg}

		assert.NoError(t, cfg.Validate(), alg)
	}
}

func TestConfigValidateRejectsInvalidConfigs(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*Config)
		field   string
		message string
	}{
		{
			name:    "empty_issuer",
			mutate:  func(c *Config) { c.Issuer = "" },
			field:   "auth.jwt.issuer",
			message: "issuer is required",
		},
		{
			name:    "nil_audience",
			mutate:  func(c *Config) { c.Audience = nil },
			field:   "auth.jwt.audience",
			message: "at least one audience is required",
		},
		{
			name:    "empty_audience",
			mutate:  func(c *Config) { c.Audience = []string{} },
			field:   "auth.jwt.audience",
			message: "at least one audience is required",
		},
		{
			name:    "blank_audience_entry",
			mutate:  func(c *Config) { c.Audience = []string{testAudience, "  "} },
			field:   "auth.jwt.audience",
			message: "audience entries must not be empty",
		},
		{
			name:    "empty_jwks_uri",
			mutate:  func(c *Config) { c.JWKSURI = "" },
			field:   "auth.jwt.jwksuri",
			message: "jwks uri is required",
		},
		{
			name:    "plain_http_jwks_uri",
			mutate:  func(c *Config) { c.JWKSURI = "http://issuer.example.com/jwks.json" },
			field:   "auth.jwt.jwksuri",
			message: "jwks uri must use the https scheme",
		},
		{
			name:    "scheme_less_jwks_uri",
			mutate:  func(c *Config) { c.JWKSURI = "issuer.example.com/jwks.json" },
			field:   "auth.jwt.jwksuri",
			message: "jwks uri must use the https scheme",
		},
		{
			name:    "unparsable_jwks_uri",
			mutate:  func(c *Config) { c.JWKSURI = "https://issuer.example.com/%zz" },
			field:   "auth.jwt.jwksuri",
			message: "jwks uri is not a valid url",
		},
		{
			name:    "empty_algorithms",
			mutate:  func(c *Config) { c.Algorithms = nil },
			field:   "auth.jwt.algorithms",
			message: "at least one algorithm is required",
		},
		{
			name:    "unsupported_algorithm",
			mutate:  func(c *Config) { c.Algorithms = []string{AlgRS256, "HS256"} },
			field:   "auth.jwt.algorithms",
			message: "unsupported algorithm",
		},
		{
			name:    "lowercase_algorithm",
			mutate:  func(c *Config) { c.Algorithms = []string{"rs256"} },
			field:   "auth.jwt.algorithms",
			message: "unsupported algorithm",
		},
		{
			name:    "negative_leeway",
			mutate:  func(c *Config) { c.Leeway = -time.Second },
			field:   "auth.jwt.leeway",
			message: "leeway must not be negative",
		},
		{
			name:    "stale_ceiling_below_ttl",
			mutate:  func(c *Config) { c.JWKSStaleCeiling = c.JWKSTTL - time.Second },
			field:   "auth.jwt.jwks.staleceiling",
			message: "stale ceiling must be greater than or equal to ttl",
		},
		{
			name:    "zero_min_refresh_interval",
			mutate:  func(c *Config) { c.JWKSMinRefreshInterval = 0 },
			field:   "auth.jwt.jwks.minrefreshinterval",
			message: "min refresh interval must be positive",
		},
		{
			name:    "negative_min_refresh_interval",
			mutate:  func(c *Config) { c.JWKSMinRefreshInterval = -time.Second },
			field:   "auth.jwt.jwks.minrefreshinterval",
			message: "min refresh interval must be positive",
		},
		{
			name:    "zero_max_body_bytes",
			mutate:  func(c *Config) { c.JWKSMaxBodyBytes = 0 },
			field:   "auth.jwt.jwks.maxbodybytes",
			message: "max body bytes must be positive",
		},
		{
			name:    "negative_max_body_bytes",
			mutate:  func(c *Config) { c.JWKSMaxBodyBytes = -1 },
			field:   "auth.jwt.jwks.maxbodybytes",
			message: "max body bytes must be positive",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := validConfig()
			tt.mutate(&cfg)

			err := cfg.Validate()

			require.Error(t, err)
			var cerr *ConfigError
			require.ErrorAs(t, err, &cerr)
			assert.Equal(t, tt.field, cerr.Field)
			assert.Contains(t, cerr.Message, tt.message)
		})
	}
}

func setRequiredAuthEnv(t *testing.T) {
	t.Helper()
	t.Setenv("AUTH_JWT_ISSUER", testIssuer)
	t.Setenv("AUTH_JWT_AUDIENCE", testAudience)
	t.Setenv("AUTH_JWT_JWKSURI", testJWKSURI)
}

func TestConfigInjectionAppliesDocumentedDefaults(t *testing.T) {
	setRequiredAuthEnv(t)
	src, err := config.Load()
	require.NoError(t, err)

	var cfg Config
	require.NoError(t, src.InjectInto(&cfg))

	assert.Equal(t, testIssuer, cfg.Issuer)
	assert.Equal(t, []string{testAudience}, cfg.Audience)
	assert.Equal(t, testJWKSURI, cfg.JWKSURI)
	assert.Equal(t, []string{AlgRS256, AlgPS256}, cfg.Algorithms)
	assert.Equal(t, 30*time.Second, cfg.Leeway)
	assert.Nil(t, cfg.Typ)
	assert.Equal(t, 15*time.Minute, cfg.JWKSTTL)
	assert.Equal(t, time.Hour, cfg.JWKSStaleCeiling)
	assert.Equal(t, 30*time.Second, cfg.JWKSMinRefreshInterval)
	assert.Equal(t, int64(1048576), cfg.JWKSMaxBodyBytes)
	assert.False(t, cfg.TelemetryEndUserID)
	assert.NoError(t, cfg.Validate())
}

func TestConfigInjectionRequiresIssuerAudienceAndJWKSURI(t *testing.T) {
	for _, missing := range []string{"AUTH_JWT_ISSUER", "AUTH_JWT_AUDIENCE", "AUTH_JWT_JWKSURI"} {
		t.Run(missing, func(t *testing.T) {
			setRequiredAuthEnv(t)
			t.Setenv(missing, "")

			src, err := config.Load()
			require.NoError(t, err)

			var cfg Config
			err = src.InjectInto(&cfg)

			require.Error(t, err)
			assert.Contains(t, err.Error(), "auth.jwt.")
		})
	}
}

// TestConfigInjectionSplitsACommaSeparatedStringList pins the []string tag semantics for
// the env/default path: config.InjectInto splits on "," and trims each element, dropping
// the empties — so "a, b ,,c" yields exactly three entries.
func TestConfigInjectionSplitsACommaSeparatedStringList(t *testing.T) {
	setRequiredAuthEnv(t)
	t.Setenv("AUTH_JWT_AUDIENCE", "api://orders, api://billing ,,api://ledger")
	t.Setenv("AUTH_JWT_TYP", "at+jwt,JWT")

	src, err := config.Load()
	require.NoError(t, err)

	var cfg Config
	require.NoError(t, src.InjectInto(&cfg))

	assert.Equal(t, []string{"api://orders", "api://billing", "api://ledger"}, cfg.Audience)
	assert.Equal(t, []string{"at+jwt", "JWT"}, cfg.Typ)
}

// TestConfigInjectionReadsAYAMLSequenceStringList pins the other half of the []string
// semantics: a native YAML sequence arrives as []any and is converted element-wise, so a
// list written across YAML lines needs no comma encoding.
func TestConfigInjectionReadsAYAMLSequenceStringList(t *testing.T) {
	dir := t.TempDir()
	yaml := "auth:\n" +
		"  jwt:\n" +
		"    issuer: " + testIssuer + "\n" +
		"    jwksuri: " + testJWKSURI + "\n" +
		"    audience:\n" +
		"      - api://orders\n" +
		"      - api://billing\n" +
		"    algorithms:\n" +
		"      - PS256\n" +
		"    jwks:\n" +
		"      ttl: 5m\n" +
		"      maxbodybytes: 65536\n" +
		"    telemetry:\n" +
		"      enduserid: true\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(yaml), 0o600))
	t.Chdir(dir)

	src, err := config.Load()
	require.NoError(t, err)

	var cfg Config
	require.NoError(t, src.InjectInto(&cfg))

	assert.Equal(t, []string{"api://orders", "api://billing"}, cfg.Audience)
	assert.Equal(t, []string{AlgPS256}, cfg.Algorithms)
	assert.Equal(t, 5*time.Minute, cfg.JWKSTTL)
	assert.Equal(t, int64(65536), cfg.JWKSMaxBodyBytes)
	assert.True(t, cfg.TelemetryEndUserID)
	assert.NoError(t, cfg.Validate())
}

func TestConfigValidateReturnsATypedConfigError(t *testing.T) {
	cfg := validConfig()
	cfg.Issuer = ""

	err := cfg.Validate()

	var cerr *ConfigError
	require.ErrorAs(t, err, &cerr)
	assert.Contains(t, err.Error(), "auth.jwt.issuer")
}
