package config

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	testAuthJWKSURI = "https://issuer.example.com/.well-known/jwks.json"
)

// defaultedAuth is the section as config.Load hands it over when an operator
// writes no auth key at all: the framework defaults, no identity.
func defaultedAuth() *AuthConfig {
	return &AuthConfig{
		JWT: AuthJWTConfig{
			Algorithms: []string{AlgorithmRS256, AlgorithmPS256},
			Leeway:     30 * time.Second,
			JWKS: AuthJWKSConfig{
				TTL:                15 * time.Minute,
				StaleCeiling:       time.Hour,
				MinRefreshInterval: 30 * time.Second,
				MaxBodyBytes:       defaultAuthJWKSMaxBodyBytes,
			},
		},
	}
}

// TestCheckAuthAcceptsAnUnconfiguredSection pins the rule that makes the
// section optional: a service that builds no verifier has no issuer, no
// audience and no JWKS endpoint, and still loads.
func TestCheckAuthAcceptsAnUnconfiguredSection(t *testing.T) {
	assert.NoError(t, checkAuth(&AuthConfig{}))
	assert.NoError(t, checkAuth(defaultedAuth()))
}

func TestCheckAuthAcceptsAFullyConfiguredSection(t *testing.T) {
	cfg := defaultedAuth()
	cfg.JWT.Issuer = "https://issuer.example.com"
	cfg.JWT.Audience = []string{"api://orders"}
	cfg.JWT.JWKSURI = testAuthJWKSURI
	cfg.JWT.Typ = []string{"at+jwt"}

	assert.NoError(t, checkAuth(cfg))
}

// TestCheckAuthAcceptsPinnedKeysWithoutAJWKSURI is the reason the JWKS group is
// not required here: the pinned-key deployment configures no endpoint.
func TestCheckAuthAcceptsPinnedKeysWithoutAJWKSURI(t *testing.T) {
	cfg := defaultedAuth()
	cfg.JWT.Issuer = "https://issuer.example.com"
	cfg.JWT.Audience = []string{"api://orders"}

	assert.NoError(t, checkAuth(cfg))
}

func TestCheckAuthRejectsInvalidSections(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*AuthConfig)
		field   string
		message string
	}{
		{
			name:    "blank_audience_entry",
			mutate:  func(c *AuthConfig) { c.JWT.Audience = []string{"api://orders", " "} },
			field:   fieldAuthAudience,
			message: "entries must not be empty",
		},
		{
			name:    "unsupported_algorithm",
			mutate:  func(c *AuthConfig) { c.JWT.Algorithms = []string{AlgorithmRS256, "HS256"} },
			field:   fieldAuthAlgorithms,
			message: `unsupported algorithm "HS256"`,
		},
		{
			name:    "lowercase_algorithm",
			mutate:  func(c *AuthConfig) { c.JWT.Algorithms = []string{"rs256"} },
			field:   fieldAuthAlgorithms,
			message: `unsupported algorithm "rs256"`,
		},
		{
			name:    "negative_leeway",
			mutate:  func(c *AuthConfig) { c.JWT.Leeway = -time.Second },
			field:   fieldAuthLeeway,
			message: errMustBeNonNegative,
		},
		{
			// Leeway absorbs clock skew only. Unbounded, it makes every expired
			// credential verify indefinitely.
			name:    "leeway_past_the_ceiling",
			mutate:  func(c *AuthConfig) { c.JWT.Leeway = MaxAuthLeeway + time.Second },
			field:   fieldAuthLeeway,
			message: "must not exceed",
		},
		{
			name:    "absurd_leeway",
			mutate:  func(c *AuthConfig) { c.JWT.Leeway = 876000 * time.Hour },
			field:   fieldAuthLeeway,
			message: "must not exceed",
		},
		{
			name: "zero_min_refresh_interval_with_a_jwks_uri",
			mutate: func(c *AuthConfig) {
				c.JWT.JWKSURI = testAuthJWKSURI
				c.JWT.JWKS.MinRefreshInterval = 0
			},
			field:   fieldAuthJWKSMinRefresh,
			message: errMustBePositive,
		},
		{
			name: "zero_max_body_bytes_with_a_jwks_uri",
			mutate: func(c *AuthConfig) {
				c.JWT.JWKSURI = testAuthJWKSURI
				c.JWT.JWKS.MaxBodyBytes = 0
			},
			field:   fieldAuthJWKSMaxBodyBytes,
			message: errMustBePositive,
		},
		{
			name:    "blank_typ_entry",
			mutate:  func(c *AuthConfig) { c.JWT.Typ = []string{""} },
			field:   fieldAuthTyp,
			message: "entries must not be empty",
		},
		{
			name:    "plain_http_jwks_uri",
			mutate:  func(c *AuthConfig) { c.JWT.JWKSURI = "http://issuer.example.com/jwks.json" },
			field:   fieldAuthJWKSURI,
			message: "must use the https scheme",
		},
		{
			name:    "scheme_less_jwks_uri",
			mutate:  func(c *AuthConfig) { c.JWT.JWKSURI = "issuer.example.com/jwks.json" },
			field:   fieldAuthJWKSURI,
			message: "must use the https scheme",
		},
		{
			name:    "host_less_jwks_uri",
			mutate:  func(c *AuthConfig) { c.JWT.JWKSURI = "https:///jwks.json" },
			field:   fieldAuthJWKSURI,
			message: "must include a hostname",
		},
		{
			name:    "unparsable_jwks_uri",
			mutate:  func(c *AuthConfig) { c.JWT.JWKSURI = "https://issuer.example.com/%zz" },
			field:   fieldAuthJWKSURI,
			message: "is not a valid url",
		},
		{
			name:    "negative_jwks_ttl",
			mutate:  func(c *AuthConfig) { c.JWT.JWKS.TTL = -time.Second },
			field:   fieldAuthJWKSTTL,
			message: errMustBeNonNegative,
		},
		{
			name: "negative_stale_ceiling",
			mutate: func(c *AuthConfig) {
				c.JWT.JWKS.TTL = 0
				c.JWT.JWKS.StaleCeiling = -time.Second
			},
			field:   fieldAuthJWKSStaleCeiling,
			message: errMustBeNonNegative,
		},
		{
			name:    "negative_min_refresh_interval",
			mutate:  func(c *AuthConfig) { c.JWT.JWKS.MinRefreshInterval = -time.Second },
			field:   fieldAuthJWKSMinRefresh,
			message: errMustBeNonNegative,
		},
		{
			name:    "negative_max_body_bytes",
			mutate:  func(c *AuthConfig) { c.JWT.JWKS.MaxBodyBytes = -1 },
			field:   fieldAuthJWKSMaxBodyBytes,
			message: errMustBeNonNegative,
		},
		{
			name:    "stale_ceiling_below_ttl",
			mutate:  func(c *AuthConfig) { c.JWT.JWKS.StaleCeiling = c.JWT.JWKS.TTL - time.Second },
			field:   fieldAuthJWKSStaleCeiling,
			message: "must be greater than or equal to auth.jwt.jwks.ttl",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := defaultedAuth()
			tt.mutate(cfg)

			err := checkAuth(cfg)

			require.Error(t, err)
			var cerr *ConfigError
			require.ErrorAs(t, err, &cerr)
			assert.Equal(t, tt.field, cerr.Field)
			assert.Contains(t, cerr.Message, tt.message)
			assert.Equal(t, errCategoryInvalid, cerr.Category)
		})
	}
}

// TestCheckAuthAcceptsAnEqualStaleCeiling pins the boundary of the one
// cross-key rule: equal is honored, only below is a contradiction.
func TestCheckAuthAcceptsAnEqualStaleCeiling(t *testing.T) {
	cfg := defaultedAuth()
	cfg.JWT.JWKS.StaleCeiling = cfg.JWT.JWKS.TTL

	assert.NoError(t, checkAuth(cfg))
}

// TestValidateRunsCheckAuth pins that the section is reached from the load-time
// entry point, not only from its own function.
func TestValidateRunsCheckAuth(t *testing.T) {
	cfg := createValidFullConfig()
	cfg.Auth = *defaultedAuth()
	cfg.Auth.JWT.Algorithms = []string{"HS256"}

	err := Validate(cfg)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "auth config")
	assert.Contains(t, err.Error(), fieldAuthAlgorithms)
}

// TestCheckAuthAcceptsTheLeewayCeiling pins the boundary: the cap itself loads,
// only a value above it is rejected.
func TestCheckAuthAcceptsTheLeewayCeiling(t *testing.T) {
	cfg := defaultedAuth()
	cfg.JWT.Leeway = MaxAuthLeeway

	assert.Equal(t, 5*time.Minute, MaxAuthLeeway)
	assert.NoError(t, checkAuth(cfg))
}

// TestCheckAuthLeavesTheFetchTuningToAPinnedKeyDeployment pins the scope of the
// positivity rule: the refresh floor and the body cap are only live once an
// endpoint is configured, so a pinned-key deployment that never fetches is not
// forced to carry them.
func TestCheckAuthLeavesTheFetchTuningToAPinnedKeyDeployment(t *testing.T) {
	cfg := defaultedAuth()
	cfg.JWT.JWKS.MinRefreshInterval = 0
	cfg.JWT.JWKS.MaxBodyBytes = 0

	assert.NoError(t, checkAuth(cfg))
}
