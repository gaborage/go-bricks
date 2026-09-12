package auth

import (
	"context"
	"crypto/rsa"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	jose "github.com/go-jose/go-jose/v4"

	"github.com/gaborage/go-bricks/logger"
)

// Registered claim names read during verification.
const (
	claimIssuer    = "iss"
	claimAudience  = "aud"
	claimSubject   = "sub"
	claimExpiresAt = "exp"
	claimNotBefore = "nbf"
	claimIssuedAt  = "iat"
)

// Verifier verifies compact JWS bearer credentials against a KeySource and the
// configured issuer, audience, algorithm allowlist and clock leeway.
//
// A Verifier is safe for concurrent use: it is immutable after construction and
// delegates key lookup to the KeySource, which carries its own concurrency
// contract.
//
// SECURITY: no method logs, renders or records the credential string, the
// signature bytes or the "sub" claim. A rejection is reported by class only.
type Verifier struct {
	cfg     Config
	log     logger.Logger
	src     KeySource
	allowed []jose.SignatureAlgorithm

	// now is the clock used for the exp/nbf/iat comparisons. Tests in this
	// package replace it directly; there is deliberately no exported option.
	now func() time.Time
}

// NewVerifierWithKeySource builds a verifier over an explicitly supplied key
// source, for consumers that pin issuer keys out of band rather than fetching
// JWKS.
//
// cfg is validated up front and its *ConfigError is returned unchanged, so a
// misconfigured service fails startup instead of booting with a widened
// allowlist. A nil src is rejected for the same reason.
//
// A nil log is tolerated: the DEBUG rejection logging simply no-ops, which keeps
// a verifier constructible in a test without wiring a logger. Verification
// behavior is identical either way.
//
//nolint:gocritic // hugeParam: Config is the injected value type; a pointer here would invite post-construction mutation.
func NewVerifierWithKeySource(cfg Config, log logger.Logger, src KeySource) (*Verifier, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if src == nil {
		return nil, NewConfigError(fieldPrefix+"keysource", "key source is required", nil)
	}
	allowed, err := allowedAlgorithms(cfg.Algorithms)
	if err != nil {
		return nil, err
	}
	return &Verifier{
		cfg:     cfg,
		log:     log,
		src:     src,
		allowed: allowed,
		now:     time.Now,
	}, nil
}

// allowedAlgorithms maps the configured algorithm names onto go-jose values. The
// allowlist is handed to the parser, so an unlisted "alg" dies before any key is
// ever looked up.
func allowedAlgorithms(names []string) ([]jose.SignatureAlgorithm, error) {
	allowed := make([]jose.SignatureAlgorithm, 0, len(names))
	for _, name := range names {
		switch name {
		case AlgRS256:
			allowed = append(allowed, jose.RS256)
		case AlgPS256:
			allowed = append(allowed, jose.PS256)
		default:
			return nil, NewConfigError(fieldPrefix+"algorithms", fmt.Sprintf("unsupported algorithm %q", name), nil)
		}
	}
	return allowed, nil
}

// Close releases the verifier's resources. This key-source-backed verifier owns
// none, so it is a no-op returning nil and is safe to call any number of times.
// It exists so consumers can call it unconditionally from a module Shutdown,
// alongside the JWKS-backed verifier whose refresher does need stopping.
func (v *Verifier) Close() error {
	return nil
}

// Verify checks credential end to end and returns the identity it asserts.
//
// An empty or whitespace-only credential returns ErrMissingCredential, which is
// deliberately outside the ErrInvalidCredential chain so a caller can answer
// "no credential presented" differently from "credential rejected". Every rule
// failure returns a *VerificationError, for which errors.Is(err,
// ErrInvalidCredential) holds. An unusable key set returns ErrKeySetUnavailable,
// which is neither.
//
// The returned Principal's Subject is empty when the credential carries no "sub"
// claim: the claim is not required by RFC 7519, and a credential may assert an
// audience-scoped identity without one.
func (v *Verifier) Verify(ctx context.Context, credential string) (Principal, error) {
	if strings.TrimSpace(credential) == "" {
		return Principal{}, ErrMissingCredential
	}

	parsed, err := jose.ParseSigned(credential, v.allowed)
	if err != nil {
		return Principal{}, v.reject(classifyParseError(err), err)
	}
	if len(parsed.Signatures) != 1 {
		return Principal{}, v.reject(ClassMalformed, fmt.Errorf("expected exactly one signature, got %d", len(parsed.Signatures)))
	}

	header := parsed.Signatures[0].Protected
	if header.KeyID == "" {
		return Principal{}, v.reject(ClassKidMissing, errors.New("protected header carries no kid"))
	}
	if typErr := v.checkType(&header); typErr != nil {
		return Principal{}, typErr
	}

	key, err := v.resolveKey(ctx, header.KeyID)
	if err != nil {
		return Principal{}, err
	}

	payload, err := parsed.Verify(key)
	if err != nil {
		return Principal{}, v.reject(ClassSignature, err)
	}

	return v.principalFromPayload(payload)
}

// classifyParseError separates "this is not a JWS the parser accepts" from "the
// alg is not on the allowlist"; go-jose reports the latter as a typed error.
func classifyParseError(err error) string {
	var algErr *jose.ErrUnexpectedSignatureAlgorithm
	if errors.As(err, &algErr) {
		return ClassAlgorithm
	}
	return ClassMalformed
}

// checkType enforces cfg.Typ against the protected "typ" header, compared
// case-insensitively per RFC 7515 section 4.1.9. An empty cfg.Typ skips the check.
func (v *Verifier) checkType(header *jose.Header) error {
	if len(v.cfg.Typ) == 0 {
		return nil
	}
	typ, _ := header.ExtraHeaders[jose.HeaderType].(string)
	for _, want := range v.cfg.Typ {
		if strings.EqualFold(typ, want) {
			return nil
		}
	}
	return v.reject(ClassType, errors.New("protected header typ is not accepted"))
}

// resolveKey looks the signing key up and maps the source's failure modes onto
// the two distinct outcomes a caller must tell apart: an unknown kid is a caller
// fault (401), an unusable key set is a server fault (503).
//
// SECURITY: the returned unavailable error wraps the sentinel only. Wrapping the
// source's own error would let a source reclassify a 503 into a 401; the cause is
// logged at DEBUG instead.
func (v *Verifier) resolveKey(ctx context.Context, kid string) (*rsa.PublicKey, error) {
	key, err := v.src.PublicKey(ctx, kid)
	if err != nil {
		if errors.Is(err, ErrKidUnknown) {
			return nil, v.reject(ClassKidUnknown, err)
		}
		v.debug("key_set_unavailable")
		return nil, fmt.Errorf("auth: issuer key lookup failed: %w", ErrKeySetUnavailable)
	}
	if key == nil {
		v.debug("key_set_unavailable")
		return nil, fmt.Errorf("auth: issuer key lookup failed: %w", ErrKeySetUnavailable)
	}
	return key, nil
}

// principalFromPayload validates the registered claims of an already
// signature-verified payload and builds the Principal. It is never called with
// an unverified payload.
func (v *Verifier) principalFromPayload(payload []byte) (Principal, error) {
	var claims map[string]any
	if err := json.Unmarshal(payload, &claims); err != nil {
		return Principal{}, v.reject(ClassMalformed, err)
	}

	if issuer, ok := claims[claimIssuer].(string); !ok || issuer != v.cfg.Issuer {
		return Principal{}, v.reject(ClassIssuer, errors.New("iss does not match the configured issuer"))
	}

	audience, err := normalizeAudience(claims[claimAudience])
	if err != nil {
		return Principal{}, v.reject(ClassMalformed, err)
	}
	if !intersects(audience, v.cfg.Audience) {
		return Principal{}, v.reject(ClassAudience, errors.New("aud does not include a configured audience"))
	}

	expiresAt, issuedAt, err := v.validateTimeClaims(claims)
	if err != nil {
		return Principal{}, err
	}

	subject, ok := stringClaim(claims, claimSubject)
	if !ok {
		return Principal{}, v.reject(ClassMalformed, errors.New("sub is present but not a string"))
	}

	return Principal{
		Subject:   subject,
		Issuer:    v.cfg.Issuer,
		Audience:  audience,
		ExpiresAt: expiresAt,
		IssuedAt:  issuedAt,
		Claims:    claims,
	}, nil
}

// validateTimeClaims enforces exp (required), nbf and iat (both optional)
// against the verifier's clock, widened by the configured leeway. It returns the
// decoded exp and iat; iat is the zero time when the claim is absent.
func (v *Verifier) validateTimeClaims(claims map[string]any) (expiresAt, issuedAt time.Time, err error) {
	expiresAt, present, err := numericDate(claims, claimExpiresAt)
	if err != nil {
		return time.Time{}, time.Time{}, v.reject(ClassMalformed, err)
	}
	if !present {
		return time.Time{}, time.Time{}, v.reject(ClassMissingExpiry, errors.New("exp is absent"))
	}

	now := v.now()
	if expiresAt.Before(now.Add(-v.cfg.Leeway)) {
		return time.Time{}, time.Time{}, v.reject(ClassExpired, errors.New("exp is in the past"))
	}

	notBefore, present, err := numericDate(claims, claimNotBefore)
	if err != nil {
		return time.Time{}, time.Time{}, v.reject(ClassMalformed, err)
	}
	if present && notBefore.After(now.Add(v.cfg.Leeway)) {
		return time.Time{}, time.Time{}, v.reject(ClassNotYetValid, errors.New("nbf is in the future"))
	}

	issuedAt, present, err = numericDate(claims, claimIssuedAt)
	if err != nil {
		return time.Time{}, time.Time{}, v.reject(ClassMalformed, err)
	}
	if present && issuedAt.After(now.Add(v.cfg.Leeway)) {
		return time.Time{}, time.Time{}, v.reject(ClassIssuedInFuture, errors.New("iat is in the future"))
	}
	if !present {
		issuedAt = time.Time{}
	}
	return expiresAt, issuedAt, nil
}

// stringClaim returns an optional string claim. An absent claim yields ("", true);
// a present non-string yields ("", false).
func stringClaim(claims map[string]any, name string) (value string, ok bool) {
	raw, present := claims[name]
	if !present {
		return "", true
	}
	value, ok = raw.(string)
	return value, ok
}

// normalizeAudience accepts the two shapes RFC 7519 allows for "aud": a single
// string, or an array of strings. Anything else is malformed.
func normalizeAudience(raw any) ([]string, error) {
	switch value := raw.(type) {
	case nil:
		return nil, nil
	case string:
		return []string{value}, nil
	case []any:
		out := make([]string, 0, len(value))
		for _, entry := range value {
			str, ok := entry.(string)
			if !ok {
				return nil, errors.New("aud array carries a non-string entry")
			}
			out = append(out, str)
		}
		return out, nil
	default:
		return nil, errors.New("aud is neither a string nor an array of strings")
	}
}

// intersects reports whether presented and configured share at least one value.
func intersects(presented, configured []string) bool {
	for _, candidate := range presented {
		for _, want := range configured {
			if candidate == want {
				return true
			}
		}
	}
	return false
}

// numericDate decodes an optional NumericDate claim. JSON numbers decode to
// float64, so a fractional value keeps its sub-second part. A present
// non-numeric value is an error, never a panic.
func numericDate(claims map[string]any, name string) (value time.Time, present bool, err error) {
	raw, ok := claims[name]
	if !ok || raw == nil {
		return time.Time{}, false, nil
	}
	seconds, ok := raw.(float64)
	if !ok {
		return time.Time{}, true, fmt.Errorf("%s is not a numeric date", name)
	}
	whole, frac := int64(seconds), seconds-float64(int64(seconds))
	return time.Unix(whole, int64(frac*float64(time.Second))), true, nil
}

// reject builds the verification error and emits the DEBUG breadcrumb.
//
// SECURITY: only the class reaches the log. cause is carried on the error for
// framework inspection and is never rendered by VerificationError.Error().
func (v *Verifier) reject(class string, cause error) error {
	v.debug(class)
	return NewVerificationError(class, cause)
}

// debug emits the class-only rejection breadcrumb, no-oping when no logger was
// supplied.
func (v *Verifier) debug(class string) {
	if v.log == nil {
		return
	}
	v.log.Debug().Str("class", class).Msg("auth: credential rejected")
}
