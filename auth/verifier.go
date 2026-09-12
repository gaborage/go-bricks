package auth

import (
	"context"
	"crypto/rsa"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"reflect"
	"slices"
	"strings"
	"time"

	jose "github.com/go-jose/go-jose/v4"

	"github.com/gaborage/go-bricks/logger"
)

// maxCredentialBytes bounds the credential handed to the parser. It is a DoS
// bound, not a spec limit: base64 decoding and JSON parsing a megabyte-sized
// input before rejecting it is work an attacker gets for free. 64 KiB is far
// above any realistic bearer credential.
const maxCredentialBytes = 65536

// maxNumericDate bounds a NumericDate claim, in seconds. int64(float64) is
// implementation-defined once the value leaves int64 range — it saturates on
// arm64 and wraps on amd64 — so an out-of-range "nbf" or "iat" would be
// platform-dependent rather than rejected. 1e15 seconds is ~31 million years,
// past any date a credential can mean and inside float64's exact-integer range.
const maxNumericDate = 1e15

// Registered claim names read during verification.
const (
	claimIssuer    = "iss"
	claimAudience  = "aud"
	claimSubject   = "sub"
	claimExpiresAt = "exp"
	claimNotBefore = "nbf"
	claimIssuedAt  = "iat"
)

// Verifier verifies compact JWS bearer credentials against a PublicKeyResolver
// and the configured issuer, audience, algorithm allowlist and clock leeway.
//
// A Verifier is safe for concurrent use: it is immutable after construction and
// delegates key lookup to the PublicKeyResolver, which carries its own
// concurrency contract.
//
// SECURITY: no method logs, renders or records the credential string, the
// signature bytes or the "sub" claim. A rejection is reported by class only.
type Verifier struct {
	cfg      Config
	log      logger.Logger
	resolver PublicKeyResolver
	allowed  []jose.SignatureAlgorithm

	// now is the clock used for the exp/nbf/iat comparisons. Tests in this
	// package replace it directly; there is deliberately no exported option.
	now func() time.Time
}

// NewVerifierWithResolver builds a verifier over an explicitly supplied
// PublicKeyResolver, for consumers that pin issuer keys out of band rather than
// fetching JWKS.
//
// cfg is validated up front and its *ConfigError is returned unchanged, so a
// misconfigured service fails startup instead of booting with a widened
// allowlist. A nil resolver is rejected for the same reason, and so is a non-nil
// interface holding a nil pointer.
//
// Ownership: resolver stays the CALLER's. The returned verifier never closes
// it, so a resolver with resources of its own must be shut down by whoever
// built it.
//
// A nil log is tolerated: the DEBUG rejection logging simply no-ops, which keeps
// a verifier constructible in a test without wiring a logger. Verification
// behavior is identical either way.
//
// cfg is taken by value, which copies only the HEADERS of its slice fields, so
// the stored configuration clones Audience, Algorithms and Typ. Without that, a
// caller writing to the slice it passed in would change what a live verifier
// accepts — and race a concurrent Verify — contradicting the type's immutability
// contract.
//
//nolint:gocritic // hugeParam: Config is the injected value type; a pointer here would invite post-construction mutation.
func NewVerifierWithResolver(cfg Config, log logger.Logger, resolver PublicKeyResolver) (*Verifier, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if isNilResolver(resolver) {
		return nil, NewConfigError(fieldPrefix+"resolver", "public key resolver is required", nil)
	}
	cfg.Audience = slices.Clone(cfg.Audience)
	cfg.Algorithms = slices.Clone(cfg.Algorithms)
	cfg.Typ = slices.Clone(cfg.Typ)
	return &Verifier{
		cfg:      cfg,
		log:      log,
		resolver: resolver,
		allowed:  allowedAlgorithms(cfg.Algorithms),
		now:      time.Now,
	}, nil
}

// isNilResolver reports whether resolver is unusable: a nil interface, or a
// non-nil interface holding a nil pointer (or other nil-able kind). A caller
// that stores a *StaticKeyResolver in a struct field and forgets to assign it
// produces the second case, which would otherwise panic inside PublicKey on the
// request path instead of at construction.
func isNilResolver(resolver PublicKeyResolver) bool {
	if resolver == nil {
		return true
	}
	v := reflect.ValueOf(resolver)
	switch v.Kind() {
	case reflect.Pointer, reflect.Map, reflect.Chan, reflect.Func, reflect.Slice, reflect.UnsafePointer:
		return v.IsNil()
	default:
		return false
	}
}

// signatureAlgorithms is the single owner of the closed algorithm allowlist: it
// maps each accepted configuration spelling onto its go-jose value. Config
// validation and the parser allowlist both read it, so a third algorithm is one
// entry here rather than two edits that must agree.
var signatureAlgorithms = map[string]jose.SignatureAlgorithm{
	AlgRS256: jose.RS256,
	AlgPS256: jose.PS256,
}

// allowedAlgorithms maps the configured algorithm names onto go-jose values. The
// allowlist is handed to the parser, so an unlisted "alg" dies before any key is
// ever looked up.
//
// It cannot fail: every caller runs Config.Validate first, and validateAlgorithms
// rejects any name signatureAlgorithms does not own. Skipping an unknown name
// rather than erroring keeps the residual failure closed — the allowlist can
// only ever narrow, never widen.
func allowedAlgorithms(names []string) []jose.SignatureAlgorithm {
	allowed := make([]jose.SignatureAlgorithm, 0, len(names))
	for _, name := range names {
		if alg, ok := signatureAlgorithms[name]; ok {
			allowed = append(allowed, alg)
		}
	}
	return allowed
}

// Close releases the resources the verifier itself CONSTRUCTED, and only those.
// A PublicKeyResolver handed in through NewVerifierWithResolver belongs to the
// caller and is never closed here; a verifier that builds its own refreshing
// JWKS resolver owns it and must stop it. This resolver-backed verifier
// constructed nothing, so Close is a no-op returning nil, safe to call any
// number of times.
// It exists so consumers can call it unconditionally from a module Shutdown.
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
	if len(credential) > maxCredentialBytes {
		return Principal{}, v.reject(ClassMalformed, errors.New("credential exceeds the maximum accepted length"))
	}

	// ParseSignedCompact, never ParseSigned: the latter dispatches to the JSON
	// serialization for an input starting with "{", which would admit an
	// attacker-controlled unprotected header and an unbounded signatures array
	// into the crypto layer. This package accepts the compact form only, so
	// "alg" can come from nowhere but the protected header.
	parsed, err := jose.ParseSignedCompact(credential, v.allowed)
	if err != nil {
		return Principal{}, v.reject(classifyParseError(err), err)
	}
	// Belt and braces behind the compact parser, which yields exactly one signature.
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
func classifyParseError(err error) Class {
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
	if slices.ContainsFunc(v.cfg.Typ, func(want string) bool { return strings.EqualFold(typ, want) }) {
		return nil
	}
	return v.reject(ClassType, errors.New("protected header typ is not accepted"))
}

// resolveKey looks the signing key up and maps the resolver's failure modes onto
// the two distinct outcomes a caller must tell apart: an unknown kid is a caller
// fault (401), an unusable key set is a server fault (503).
//
// SECURITY: the returned unavailable error wraps the sentinel only. Wrapping the
// resolver's own error would let a resolver reclassify a 503 into a 401. The cause is
// dropped rather than logged: a PublicKeyResolver is consumer-supplied and may return
// anything, so only the class reaches the log.
func (v *Verifier) resolveKey(ctx context.Context, kid string) (*rsa.PublicKey, error) {
	key, err := v.resolver.PublicKey(ctx, kid)
	if err != nil && errors.Is(err, ErrKidUnknown) {
		return nil, v.reject(ClassKidUnknown, err)
	}
	// A nil key with a nil error violates the PublicKeyResolver contract; treating it as
	// an unusable key set keeps the failure closed instead of reaching Verify.
	if err != nil || key == nil {
		v.debug(ClassKeySetUnavailable)
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

	subject, wellTyped := optionalStringClaim(claims, claimSubject)
	if !wellTyped {
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
	return expiresAt, issuedAt, nil
}

// optionalStringClaim reads a claim that may be absent but, when present, must be
// a string. wellTyped is false only for a present value of the wrong type; an
// absent claim yields ("", true). The bool reports TYPE, not presence — unlike
// Principal.Claim, whose bool reports presence.
func optionalStringClaim(claims map[string]any, name string) (value string, wellTyped bool) {
	raw, present := claims[name]
	if !present {
		return "", true
	}
	value, wellTyped = raw.(string)
	return value, wellTyped
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
	return slices.ContainsFunc(presented, func(candidate string) bool {
		return slices.Contains(configured, candidate)
	})
}

// numericDate decodes an optional NumericDate claim. JSON numbers decode to
// float64, so a fractional value keeps its sub-second part. A present
// non-numeric value is an error, never a panic, and so is one outside
// maxNumericDate: the int64 conversion below is implementation-defined past
// that range, which would make the exp/nbf/iat comparisons platform-dependent.
func numericDate(claims map[string]any, name string) (value time.Time, present bool, err error) {
	raw, ok := claims[name]
	if !ok || raw == nil {
		return time.Time{}, false, nil
	}
	seconds, ok := raw.(float64)
	if !ok {
		return time.Time{}, true, fmt.Errorf("%s is not a numeric date", name)
	}
	if math.IsNaN(seconds) || math.Abs(seconds) > maxNumericDate {
		return time.Time{}, true, fmt.Errorf("%s is out of the accepted numeric date range", name)
	}
	whole, frac := int64(seconds), seconds-float64(int64(seconds))
	return time.Unix(whole, int64(frac*float64(time.Second))), true, nil
}

// reject builds the verification error and emits the DEBUG breadcrumb.
//
// SECURITY: only the class reaches the log. cause is carried on the error for
// framework inspection and is never rendered by VerificationError.Error().
func (v *Verifier) reject(class Class, cause error) error {
	v.debug(class)
	return NewVerificationError(class, cause)
}

// debug emits the class-only rejection breadcrumb, no-oping when no logger was
// supplied.
func (v *Verifier) debug(class Class) {
	if v.log == nil {
		return
	}
	v.log.Debug().Str("class", string(class)).Msg("auth: credential rejected")
}
