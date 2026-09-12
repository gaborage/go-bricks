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
		Issuer:     testIssuer,
		Audience:   []string{testAudience},
		JWKSURI:    testJWKSURI,
		Algorithms: []string{AlgRS256, AlgPS256},
		Leeway:     30 * time.Second,
		JWKS: config.AuthJWKSConfig{
			TTL:                15 * time.Minute,
			StaleCeiling:       time.Hour,
			MinRefreshInterval: 30 * time.Second,
			MaxBodyBytes:       1048576,
		},
	}
}

func TestConfigValidateAcceptsAFullyPopulatedConfig(t *testing.T) {
	cfg := validConfig()

	assert.NoError(t, cfg.Validate())
}

func TestConfigValidateAcceptsAZeroLeeway(t *testing.T) {
	cfg := validConfig()
	cfg.Leeway = 0

	assert.NoError(t, cfg.Validate())
}

// TestConfigValidateAcceptsTheLeewayCeiling pins the boundary: the cap itself is
// honored, only a value above it is rejected.
func TestConfigValidateAcceptsTheLeewayCeiling(t *testing.T) {
	cfg := validConfig()
	cfg.Leeway = maxLeeway

	assert.Equal(t, 5*time.Minute, maxLeeway)
	assert.NoError(t, cfg.Validate())
}

// TestConfigValidateIgnoresTheJWKSGroup pins the split: a verifier built over a
// pinned resolver makes no network call, so neither the endpoint nor the
// fetch tuning is its precondition.
func TestConfigValidateIgnoresTheJWKSGroup(t *testing.T) {
	cfg := validConfig()
	cfg.JWKSURI = ""
	cfg.JWKS = config.AuthJWKSConfig{}

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
			// An unbounded leeway makes every expired credential verify
			// indefinitely: 876000h once accepted a credential expired a century ago.
			name:    "leeway_past_the_ceiling",
			mutate:  func(c *Config) { c.Leeway = maxLeeway + time.Second },
			field:   "auth.jwt.leeway",
			message: "leeway must not exceed",
		},
		{
			name:    "absurd_leeway",
			mutate:  func(c *Config) { c.Leeway = 876000 * time.Hour },
			field:   "auth.jwt.leeway",
			message: "leeway must not exceed",
		},
		{
			name:    "blank_typ_entry",
			mutate:  func(c *Config) { c.Typ = []string{"at+jwt", " "} },
			field:   "auth.jwt.typ",
			message: "typ entries must not be empty",
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

func TestConfigValidateJWKSSourceAcceptsAPopulatedGroup(t *testing.T) {
	cfg := validConfig()

	assert.Nil(t, cfg.validateJWKSSource())
}

func TestConfigValidateJWKSSourceAcceptsAnEqualStaleCeiling(t *testing.T) {
	cfg := validConfig()
	cfg.JWKS.StaleCeiling = cfg.JWKS.TTL

	assert.Nil(t, cfg.validateJWKSSource())
}

func TestConfigValidateJWKSSourceRejectsInvalidGroups(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*Config)
		field   string
		message string
	}{
		{
			name:    "empty_jwks_uri",
			mutate:  func(c *Config) { c.JWKSURI = "" },
			field:   "auth.jwt.jwksuri",
			message: "jwks uri is required",
		},
		{
			name:    "blank_jwks_uri",
			mutate:  func(c *Config) { c.JWKSURI = "   " },
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
			name:    "host_less_jwks_uri",
			mutate:  func(c *Config) { c.JWKSURI = "https:///jwks.json" },
			field:   "auth.jwt.jwksuri",
			message: "jwks uri must include a hostname",
		},
		{
			name:    "unparsable_jwks_uri",
			mutate:  func(c *Config) { c.JWKSURI = "https://issuer.example.com/%zz" },
			field:   "auth.jwt.jwksuri",
			message: "jwks uri is not a valid url",
		},
		{
			name:    "stale_ceiling_below_ttl",
			mutate:  func(c *Config) { c.JWKS.StaleCeiling = c.JWKS.TTL - time.Second },
			field:   "auth.jwt.jwks.staleceiling",
			message: "stale ceiling must be greater than or equal to ttl",
		},
		{
			name:    "zero_min_refresh_interval",
			mutate:  func(c *Config) { c.JWKS.MinRefreshInterval = 0 },
			field:   "auth.jwt.jwks.minrefreshinterval",
			message: "min refresh interval must be positive",
		},
		{
			name:    "negative_min_refresh_interval",
			mutate:  func(c *Config) { c.JWKS.MinRefreshInterval = -time.Second },
			field:   "auth.jwt.jwks.minrefreshinterval",
			message: "min refresh interval must be positive",
		},
		{
			name:    "zero_max_body_bytes",
			mutate:  func(c *Config) { c.JWKS.MaxBodyBytes = 0 },
			field:   "auth.jwt.jwks.maxbodybytes",
			message: "max body bytes must be positive",
		},
		{
			name:    "negative_max_body_bytes",
			mutate:  func(c *Config) { c.JWKS.MaxBodyBytes = -1 },
			field:   "auth.jwt.jwks.maxbodybytes",
			message: "max body bytes must be positive",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := validConfig()
			tt.mutate(&cfg)

			err := cfg.validateJWKSSource()

			require.NotNil(t, err)
			assert.Equal(t, tt.field, err.Field)
			assert.Contains(t, err.Message, tt.message)
		})
	}
}

// writeConfigYAML writes a config.yaml into a temp directory and makes it the
// working directory, so config.Load reads exactly this tree.
func writeConfigYAML(t *testing.T, body string) {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(body), 0o600))
	t.Chdir(dir)
}

// TestAuthSectionAppliesDocumentedDefaults pins the framework defaults an
// operator inherits without writing a single auth key, and the fact that the
// section is optional: a service that never configures auth still loads.
func TestAuthSectionAppliesDocumentedDefaults(t *testing.T) {
	writeConfigYAML(t, "app:\n  name: defaults-service\n")

	loaded, err := config.Load()
	require.NoError(t, err)

	cfg := Config(loaded.Auth.JWT)
	assert.Empty(t, cfg.Issuer)
	assert.Empty(t, cfg.Audience)
	assert.Empty(t, cfg.JWKSURI)
	assert.Equal(t, []string{AlgRS256, AlgPS256}, cfg.Algorithms)
	assert.Equal(t, 30*time.Second, cfg.Leeway)
	assert.Nil(t, cfg.Typ)
	assert.Equal(t, 15*time.Minute, cfg.JWKS.TTL)
	assert.Equal(t, time.Hour, cfg.JWKS.StaleCeiling)
	assert.Equal(t, 30*time.Second, cfg.JWKS.MinRefreshInterval)
	assert.Equal(t, int64(1048576), cfg.JWKS.MaxBodyBytes)
	assert.False(t, cfg.Telemetry.EndUserID)
}

// TestAuthSectionReadsAYAMLSequenceStringList pins how koanf decodes the
// []string keys from a native YAML sequence: element-wise, entries verbatim.
func TestAuthSectionReadsAYAMLSequenceStringList(t *testing.T) {
	writeConfigYAML(t, "auth:\n"+
		"  jwt:\n"+
		"    issuer: "+testIssuer+"\n"+
		"    jwksuri: "+testJWKSURI+"\n"+
		"    audience:\n"+
		"      - api://orders\n"+
		"      - api://billing\n"+
		"    algorithms:\n"+
		"      - PS256\n"+
		"    typ:\n"+
		"      - at+jwt\n"+
		"      - JWT\n"+
		"    jwks:\n"+
		"      ttl: 5m\n"+
		"      maxbodybytes: 65536\n"+
		"    telemetry:\n"+
		"      enduserid: true\n")

	loaded, err := config.Load()
	require.NoError(t, err)

	cfg := Config(loaded.Auth.JWT)
	assert.Equal(t, []string{"api://orders", "api://billing"}, cfg.Audience)
	assert.Equal(t, []string{AlgPS256}, cfg.Algorithms)
	assert.Equal(t, []string{"at+jwt", "JWT"}, cfg.Typ)
	assert.Equal(t, 5*time.Minute, cfg.JWKS.TTL)
	assert.Equal(t, int64(65536), cfg.JWKS.MaxBodyBytes)
	assert.True(t, cfg.Telemetry.EndUserID)
	assert.Nil(t, cfg.validateJWKSSource())
	assert.NoError(t, cfg.Validate())
}

// TestAuthSectionSplitsACommaSeparatedEnvStringList pins the other half: a
// single environment variable expresses a list by comma, with each element
// trimmed and the empty ones dropped — so "a, b ,,c" yields three entries.
func TestAuthSectionSplitsACommaSeparatedEnvStringList(t *testing.T) {
	writeConfigYAML(t, "app:\n  name: env-list-service\n")
	t.Setenv("AUTH_JWT_ISSUER", testIssuer)
	t.Setenv("AUTH_JWT_AUDIENCE", "api://orders, api://billing ,,api://ledger")
	t.Setenv("AUTH_JWT_TYP", "at+jwt,JWT")

	loaded, err := config.Load()
	require.NoError(t, err)

	cfg := Config(loaded.Auth.JWT)
	assert.Equal(t, []string{"api://orders", "api://billing", "api://ledger"}, cfg.Audience)
	assert.Equal(t, []string{"at+jwt", "JWT"}, cfg.Typ)
	assert.NoError(t, cfg.Validate())
}

// TestAuthSectionFailsConfigLoadOnAnUnsupportedAlgorithm pins the load-time
// seam: a value that is wrong for every deployment is refused by config.Load,
// not deferred to the verifier.
func TestAuthSectionFailsConfigLoadOnAnUnsupportedAlgorithm(t *testing.T) {
	writeConfigYAML(t, "auth:\n  jwt:\n    algorithms:\n      - HS256\n")

	_, err := config.Load()

	require.Error(t, err)
	assert.Contains(t, err.Error(), "auth.jwt.algorithms")
}

// TestAuthSectionLoadsWithoutAJWKSURI pins the behavior a pinned-key
// deployment depends on: no auth.jwt.jwksuri is a valid configuration.
func TestAuthSectionLoadsWithoutAJWKSURI(t *testing.T) {
	writeConfigYAML(t, "auth:\n"+
		"  jwt:\n"+
		"    issuer: "+testIssuer+"\n"+
		"    audience:\n"+
		"      - "+testAudience+"\n")

	loaded, err := config.Load()
	require.NoError(t, err)

	cfg := Config(loaded.Auth.JWT)
	assert.Empty(t, cfg.JWKSURI)
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
