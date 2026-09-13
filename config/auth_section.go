package config

import (
	"fmt"
	"net/url"
	"strings"
)

// checkAuth rejects an auth section that is wrong for EVERY deployment. It
// deliberately does not require auth.jwt.issuer, auth.jwt.audience or
// auth.jwt.jwksuri: a service that builds no verifier configures none of them,
// and a verifier built over a statically pinned resolver (auth.PublicKeyResolver)
// never fetches a key set. Those conditional rules live with the consumer, in
// auth.Config.Validate and auth's JWKS-source check.
//
// What is left holds unconditionally: every key checked here either carries a
// framework default (so a non-default value is an operator's) or is inert when
// empty. A value that contradicts itself — an algorithm outside the closed set,
// a negative duration, a stale ceiling below the TTL it bounds, a plaintext
// JWKS endpoint — is wrong whether or not a verifier is ever built.
func checkAuth(cfg *AuthConfig) error {
	jwt := &cfg.JWT

	if err := checkNonBlankEntries(fieldAuthAudience, jwt.Audience); err != nil {
		return err
	}

	if err := checkAuthAlgorithms(jwt.Algorithms); err != nil {
		return err
	}

	if jwt.Leeway < 0 {
		return NewValidationError(fieldAuthLeeway, errMustBeNonNegative)
	}
	if jwt.Leeway > MaxAuthLeeway {
		return NewValidationError(fieldAuthLeeway, fmt.Sprintf("must not exceed %v", MaxAuthLeeway))
	}

	if err := checkNonBlankEntries(fieldAuthTyp, jwt.Typ); err != nil {
		return err
	}

	return checkAuthJWKS(&jwt.JWKS, jwt.JWKSURI)
}

// checkNonBlankEntries rejects a list whose entries are present but blank. An
// absent list is not this rule's business: it means the control is unused.
func checkNonBlankEntries(field string, entries []string) error {
	for _, entry := range entries {
		if strings.TrimSpace(entry) == "" {
			return NewValidationError(field, "entries must not be empty")
		}
	}
	return nil
}

// checkAuthAlgorithms rejects a value outside the closed RSA set. An EMPTY list
// is left to the verifier: it is the shape a deployment without auth never
// reaches, because the default fills both entries.
func checkAuthAlgorithms(algorithms []string) error {
	for _, alg := range algorithms {
		if alg != AlgorithmRS256 && alg != AlgorithmPS256 {
			return &ConfigError{
				Category: errCategoryInvalid,
				Field:    fieldAuthAlgorithms,
				Message:  fmt.Sprintf("unsupported algorithm %q", alg),
				Action:   fmt.Sprintf("use %s, %s, or both", AlgorithmRS256, AlgorithmPS256),
			}
		}
	}
	return nil
}

// checkAuthJWKS rejects a key-set stanza that cannot be honored. The URI is
// optional (pinned-key deployments set none) but, when set, must be an https
// endpoint: the key set is the verifier's trust anchor, so plaintext transport
// hands signature verification to the network.
// A configured endpoint additionally makes the refresh floor and the body cap
// live: zero would mean "refresh without limit" and "read a body without limit",
// which no key-set fetch can honor. Both are therefore required to be positive
// exactly when a URI is set — a pinned-key deployment fetches nothing, and the
// framework defaults fill both in for everyone else.
// checkAuthJWKSURI rejects a key-set endpoint that is not a parsable https URL
// with a hostname: "https:///jwks.json" carries the right scheme and no host to
// fetch from, so the scheme check alone would let it through.
func checkAuthJWKSURI(uri string) error {
	parsed, err := url.Parse(uri)
	if err != nil {
		return NewValidationError(fieldAuthJWKSURI, "is not a valid url")
	}
	if parsed.Scheme != "https" {
		return NewValidationError(fieldAuthJWKSURI, "must use the https scheme")
	}
	if parsed.Hostname() == "" {
		return NewValidationError(fieldAuthJWKSURI, "must include a hostname")
	}
	return nil
}

func checkAuthJWKS(cfg *AuthJWKSConfig, uri string) error {
	if strings.TrimSpace(uri) != "" {
		if err := checkAuthJWKSURI(uri); err != nil {
			return err
		}
		if cfg.MinRefreshInterval <= 0 {
			return NewValidationError(fieldAuthJWKSMinRefresh, errMustBePositive)
		}
		if cfg.MaxBodyBytes <= 0 {
			return NewValidationError(fieldAuthJWKSMaxBodyBytes, errMustBePositive)
		}
	}

	nonNegative := []struct {
		field string
		value int64
	}{
		{fieldAuthJWKSTTL, int64(cfg.TTL)},
		{fieldAuthJWKSStaleCeiling, int64(cfg.StaleCeiling)},
		{fieldAuthJWKSMinRefresh, int64(cfg.MinRefreshInterval)},
		{fieldAuthJWKSMaxBodyBytes, cfg.MaxBodyBytes},
	}
	for _, k := range nonNegative {
		if k.value < 0 {
			return NewValidationError(k.field, errMustBeNonNegative)
		}
	}

	if cfg.StaleCeiling < cfg.TTL {
		return &ConfigError{
			Category: errCategoryInvalid,
			Field:    fieldAuthJWKSStaleCeiling,
			Message:  fmt.Sprintf("must be greater than or equal to %s (%v)", fieldAuthJWKSTTL, cfg.TTL),
			Action:   fmt.Sprintf("raise %s or lower %s", fieldAuthJWKSStaleCeiling, fieldAuthJWKSTTL),
		}
	}

	return nil
}
