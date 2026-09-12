package auth

import (
	"fmt"
	"maps"
	"net/url"
	"slices"
	"strings"

	"github.com/gaborage/go-bricks/config"
)

// Accepted signature algorithms. The set is closed: RSA-only, and matched
// case-sensitively so a misspelled operator value fails startup instead of
// silently widening the allowlist. They are the config package's spellings, so
// the allowlist config load enforces is the one the verifier enforces.
const (
	AlgRS256 = config.AlgorithmRS256
	AlgPS256 = config.AlgorithmPS256
)

// maxLeeway caps auth.jwt.leeway. Leeway absorbs clock skew between the issuer
// and this service and nothing else, so it is bounded: an unbounded leeway makes
// every expired credential verify indefinitely. The bound is the config
// package's, so the load-time check and this one cannot drift apart.
const maxLeeway = config.MaxAuthLeeway

// Config-error field prefixes, kept section-qualified so a startup failure names
// the YAML key the operator must fix.
const (
	fieldPrefix     = "auth.jwt."
	jwksFieldPrefix = fieldPrefix + "jwks."
)

// Config is the bearer-credential verification configuration: the framework's
// auth.jwt section, taken as a package-local type so the verifier's validation
// and its typed *ConfigError live next to the code that enforces them.
//
// A module converts the framework's section at its seam:
//
//	cfg := auth.Config(deps.Config.Auth.JWT)
//
// config.Validate has already rejected what is wrong for every deployment
// (see config.checkAuth); Validate below adds what is true only once a
// verifier is actually built.
type Config config.AuthJWTConfig

// Validate performs fail-fast validation of the verifier's configuration:
// issuer, audience, algorithms, leeway and typ. It deliberately leaves the
// auth.jwt.jwks.* group alone — a verifier built over a pinned PublicKeyResolver
// fetches nothing — so the JWKS-backed resolver validates that group itself.
// Every failure is a *ConfigError naming the offending auth.jwt.* key.
func (c *Config) Validate() error {
	checks := []func() *ConfigError{
		c.validateIssuer,
		c.validateAudience,
		c.validateAlgorithms,
		c.validateLeeway,
		c.validateTyp,
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

func (c *Config) validateAlgorithms() *ConfigError {
	field := fieldPrefix + "algorithms"
	if len(c.Algorithms) == 0 {
		return NewConfigError(field, "at least one algorithm is required", nil)
	}
	for _, alg := range c.Algorithms {
		if _, ok := signatureAlgorithms[alg]; !ok {
			return NewConfigError(field, fmt.Sprintf("unsupported algorithm %q: allowed values are %s", alg, strings.Join(supportedAlgorithms(), " and ")), nil)
		}
	}
	return nil
}

// supportedAlgorithms lists the accepted algorithm names in a stable order, for
// the operator-facing error message. signatureAlgorithms stays the single owner
// of the set.
func supportedAlgorithms() []string {
	return slices.Sorted(maps.Keys(signatureAlgorithms))
}

func (c *Config) validateLeeway() *ConfigError {
	if c.Leeway < 0 {
		return NewConfigError(fieldPrefix+"leeway", "leeway must not be negative", nil)
	}
	if c.Leeway > maxLeeway {
		return NewConfigError(fieldPrefix+"leeway", fmt.Sprintf("leeway must not exceed %v", maxLeeway), nil)
	}
	return nil
}

func (c *Config) validateTyp() *ConfigError {
	for _, typ := range c.Typ {
		if strings.TrimSpace(typ) == "" {
			return NewConfigError(fieldPrefix+"typ", "typ entries must not be empty", nil)
		}
	}
	return nil
}

// validateJWKSSource validates the auth.jwt.jwks.* group plus the endpoint it
// fetches from. It is separate from Validate because it is the JWKS-backed key
// resolver's precondition, not the verifier's: a pinned PublicKeyResolver makes no
// network call, so requiring an https endpoint of it would layer one
// component's configuration onto another's.
func (c *Config) validateJWKSSource() *ConfigError {
	if err := c.validateJWKSURI(); err != nil {
		return err
	}
	if c.JWKS.StaleCeiling < c.JWKS.TTL {
		return NewConfigError(jwksFieldPrefix+"staleceiling", "stale ceiling must be greater than or equal to ttl", nil)
	}
	if c.JWKS.MinRefreshInterval <= 0 {
		return NewConfigError(jwksFieldPrefix+"minrefreshinterval", "min refresh interval must be positive", nil)
	}
	if c.JWKS.MaxBodyBytes <= 0 {
		return NewConfigError(jwksFieldPrefix+"maxbodybytes", "max body bytes must be positive", nil)
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
	if parsed.Hostname() == "" {
		return NewConfigError(field, "jwks uri must include a hostname", nil)
	}
	return nil
}
