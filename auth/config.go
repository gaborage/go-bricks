package auth

import (
	"fmt"
	"net/url"
	"strings"
	"time"
)

// Accepted signature algorithms. The set is closed: RSA-only, and matched
// case-sensitively so a misspelled operator value fails startup instead of
// silently widening the allowlist.
const (
	AlgRS256 = "RS256"
	AlgPS256 = "PS256"
)

// Config-error field prefixes, kept section-qualified so a startup failure names
// the YAML key the operator must fix.
const (
	fieldPrefix     = "auth.jwt."
	jwksFieldPrefix = fieldPrefix + "jwks."
)

// Config is the bearer-credential verification configuration, injected from the
// auth.jwt.* section with deps.Config.InjectInto(&cfg).
//
// The keys are nested (auth.jwt.jwks.ttl, auth.jwt.telemetry.enduserid) but the
// struct is flat: config.InjectInto resolves each field against the full key
// path in its tag and does not descend into nested structs.
type Config struct {
	// Issuer is matched exactly against the credential's "iss" claim.
	Issuer string `config:"auth.jwt.issuer" required:"true"`

	// Audience lists the accepted "aud" values; a credential must carry one of them.
	Audience []string `config:"auth.jwt.audience" required:"true"`

	// JWKSURI is the issuer's key set endpoint. HTTPS only.
	JWKSURI string `config:"auth.jwt.jwksuri" required:"true"`

	// Algorithms is the accepted "alg" allowlist, a subset of {RS256, PS256}.
	Algorithms []string `config:"auth.jwt.algorithms" default:"RS256,PS256"`

	// Leeway absorbs clock skew on the exp/nbf/iat comparisons.
	Leeway time.Duration `config:"auth.jwt.leeway" default:"30s"`

	// Typ, when set, constrains the JOSE header "typ": it must match one entry,
	// compared case-insensitively per RFC 7515. Empty means no typ check.
	Typ []string `config:"auth.jwt.typ"`

	// JWKSTTL is how long a fetched key set is served without refreshing.
	JWKSTTL time.Duration `config:"auth.jwt.jwks.ttl" default:"15m"`

	// JWKSStaleCeiling is how long a stale key set may still be served when the
	// issuer is unreachable. Past it, verification fails with ErrKeySetUnavailable.
	JWKSStaleCeiling time.Duration `config:"auth.jwt.jwks.staleceiling" default:"1h"`

	// JWKSMinRefreshInterval floors the spacing between key set fetches, so an
	// unknown kid cannot be used to hammer the issuer.
	JWKSMinRefreshInterval time.Duration `config:"auth.jwt.jwks.minrefreshinterval" default:"30s"`

	// JWKSMaxBodyBytes caps the key set response body read from the issuer.
	JWKSMaxBodyBytes int64 `config:"auth.jwt.jwks.maxbodybytes" default:"1048576"`

	// TelemetryEndUserID enables the enduser.id span attribute. It is off by
	// default because it records the subject of every verified credential.
	TelemetryEndUserID bool `config:"auth.jwt.telemetry.enduserid" default:"false"`
}

// Validate performs fail-fast validation of the auth configuration.
// Every failure is a *ConfigError naming the offending auth.jwt.* key.
func (c *Config) Validate() error {
	return c.validate()
}

// validate runs the checks in configuration order so the first reported failure
// is the earliest key an operator would read.
func (c *Config) validate() error {
	checks := []func() *ConfigError{
		c.validateIssuer,
		c.validateAudience,
		c.validateJWKSURI,
		c.validateAlgorithms,
		c.validateLeeway,
		c.validateJWKS,
	}
	for _, check := range checks {
		if err := check(); err != nil {
			return err
		}
	}
	return nil
}

func (c *Config) validateIssuer() *ConfigError {
	if strings.TrimSpace(c.Issuer) == "" {
		return NewConfigError(fieldPrefix+"issuer", "issuer is required", nil)
	}
	return nil
}

func (c *Config) validateAudience() *ConfigError {
	field := fieldPrefix + "audience"
	if len(c.Audience) == 0 {
		return NewConfigError(field, "at least one audience is required", nil)
	}
	for _, aud := range c.Audience {
		if strings.TrimSpace(aud) == "" {
			return NewConfigError(field, "audience entries must not be empty", nil)
		}
	}
	return nil
}

func (c *Config) validateJWKSURI() *ConfigError {
	field := fieldPrefix + "jwksuri"
	if strings.TrimSpace(c.JWKSURI) == "" {
		return NewConfigError(field, "jwks uri is required", nil)
	}
	parsed, err := url.Parse(c.JWKSURI)
	if err != nil {
		return NewConfigError(field, "jwks uri is not a valid url", err)
	}
	if parsed.Scheme != "https" {
		return NewConfigError(field, "jwks uri must use the https scheme", nil)
	}
	return nil
}

func (c *Config) validateAlgorithms() *ConfigError {
	field := fieldPrefix + "algorithms"
	if len(c.Algorithms) == 0 {
		return NewConfigError(field, "at least one algorithm is required", nil)
	}
	for _, alg := range c.Algorithms {
		if alg != AlgRS256 && alg != AlgPS256 {
			return NewConfigError(field, fmt.Sprintf("unsupported algorithm %q: allowed values are %s and %s", alg, AlgRS256, AlgPS256), nil)
		}
	}
	return nil
}

func (c *Config) validateLeeway() *ConfigError {
	if c.Leeway < 0 {
		return NewConfigError(fieldPrefix+"leeway", "leeway must not be negative", nil)
	}
	return nil
}

func (c *Config) validateJWKS() *ConfigError {
	if c.JWKSStaleCeiling < c.JWKSTTL {
		return NewConfigError(jwksFieldPrefix+"staleceiling", "stale ceiling must be greater than or equal to ttl", nil)
	}
	if c.JWKSMinRefreshInterval <= 0 {
		return NewConfigError(jwksFieldPrefix+"minrefreshinterval", "min refresh interval must be positive", nil)
	}
	if c.JWKSMaxBodyBytes <= 0 {
		return NewConfigError(jwksFieldPrefix+"maxbodybytes", "max body bytes must be positive", nil)
	}
	return nil
}
