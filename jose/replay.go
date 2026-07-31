package jose

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"
)

var (
	// ErrClaimsMissing is returned when CheckJTIReplay is handed nil claims,
	// which means the route was not JOSE-protected. Map it to 500 — it is a
	// wiring error, not a peer error.
	ErrClaimsMissing = errors.New("jose: no verified claims")

	// ErrJTIMissing is returned when the verified claim set carries no jti, so
	// no replay check is possible. Map it to 401 — the peer omitted a claim
	// the deployment requires, which is a peer error, not a server fault.
	ErrJTIMissing = errors.New("jose: verified claims carry no jti")

	// ErrIssuerMissing is returned when CheckJTIReplay is handed claims with no
	// iss, and also when CheckJTIReplayInNamespace is handed an empty
	// namespace. iss is optional per RFC 7519, and every iss-less token would
	// otherwise share one replay namespace, letting two partners' jtis collide.
	ErrIssuerMissing = errors.New("jose: claims carry no iss — use CheckJTIReplayInNamespace with an explicit namespace")

	// ErrReplayRecorderMissing is returned when CheckJTIReplay is handed a nil
	// recorder. Map it to 500 — like ErrClaimsMissing it is a wiring error, not
	// anything the peer did.
	ErrReplayRecorderMissing = errors.New("jose: replay recorder is nil")

	// ErrReplayWindowInvalid is returned when the window argument is not
	// positive. A zero window would store a jti with no expiry.
	ErrReplayWindowInvalid = errors.New("jose: replay window must be positive")
)

// ReplayRecorder is the minimal atomic set-if-absent surface CheckJTIReplay needs.
// cache.Cache satisfies it structurally, so jose does not import cache (the
// package deliberately carries no go-bricks dependencies). Mirrors the
// KeyStoreLike pattern in resolver.go.
type ReplayRecorder interface {
	// GetOrSet must be atomic set-if-absent: wasSet is true only when THIS
	// call inserted a key that was absent, and false when the key already
	// existed. CheckJTIReplay's replay detection is exactly this bit, so an
	// implementation that returns true unconditionally silently disables it.
	GetOrSet(ctx context.Context, key string, value []byte, ttl time.Duration) (storedValue []byte, wasSet bool, err error)
}

// replayMarker is the stored value. Its content is never read — only the key's
// presence matters — but GetOrSet requires a non-empty value.
var replayMarker = []byte{1}

// CheckJTIReplay records the claim set's jti in recorder and reports whether
// this is the first time it has been seen. fresh=false means the jti was
// already recorded: a replay, which the caller should reject.
//
// window is how long a jti is remembered, measured from the first sighting. It
// MUST be at least as long as the caller's own acceptance horizon — the point
// past which the caller's timing checks would reject the token anyway.
//
// The TTL is deliberately NOT derived from the token's own exp. Doing so looks
// tighter but forgets the jti at exp, while a caller that allows clock skew
// still accepts the token until exp+skew: that difference is a replay window.
// A caller whose freshness check bounds acceptance at iat+window is safe here,
// because the key outlives that bound.
//
// CheckJTIReplay does NOT enforce exp, nbf, or iat freshness — skew tolerances
// are partner-specific and remain the application's. Callers should run their
// timing checks first and only reach here with a token they otherwise accept.
//
// claims.Issuer must be non-empty: iss is optional per RFC 7519, and every
// iss-less token would otherwise land in the same "unknown issuer" replay
// namespace, letting two partners' jtis (short counters, per-day ordinals)
// collide and reject each other's valid requests. Token profiles that omit
// iss must use CheckJTIReplayInNamespace with an explicit namespace instead.
//
// Atomicity is the recorder's: with a SET-NX backend, exactly one of N
// concurrent requests carrying the same jti observes fresh=true.
func CheckJTIReplay(ctx context.Context, recorder ReplayRecorder, claims *Claims, window time.Duration) (fresh bool, err error) {
	if claims == nil {
		return false, ErrClaimsMissing
	}
	if recorder == nil {
		return false, ErrReplayRecorderMissing
	}
	if claims.JTI == "" {
		return false, ErrJTIMissing
	}
	if claims.Issuer == "" {
		return false, ErrIssuerMissing
	}
	if window <= 0 {
		return false, ErrReplayWindowInvalid
	}

	_, wasSet, err := recorder.GetOrSet(ctx, replayKeyIn(claims.Issuer, claims.JTI), replayMarker, window)
	if err != nil {
		return false, fmt.Errorf("jose: replay recorder: %w", err)
	}
	return wasSet, nil
}

// CheckJTIReplayInNamespace is CheckJTIReplay for token profiles that omit
// iss. namespace names the trust domain the caller has authenticated by
// other means — the policy's VerifyKid is the natural choice, since the
// middleware bound the signature to it. Empty namespaces are rejected.
func CheckJTIReplayInNamespace(ctx context.Context, recorder ReplayRecorder, namespace string, claims *Claims, window time.Duration) (fresh bool, err error) {
	if claims == nil {
		return false, ErrClaimsMissing
	}
	if recorder == nil {
		return false, ErrReplayRecorderMissing
	}
	if claims.JTI == "" {
		return false, ErrJTIMissing
	}
	if namespace == "" {
		return false, ErrIssuerMissing
	}
	if window <= 0 {
		return false, ErrReplayWindowInvalid
	}

	_, wasSet, err := recorder.GetOrSet(ctx, replayKeyIn(namespace, claims.JTI), replayMarker, window)
	if err != nil {
		return false, fmt.Errorf("jose: replay recorder: %w", err)
	}
	return wasSet, nil
}

// replayKeyIn namespaces the jti by ns (the issuer, or an explicit namespace
// for iss-less token profiles). Two partners can legitimately mint the same
// jti value, and a collision would reject a valid request. The length prefix
// keeps the namespace boundary unambiguous, so no namespace string can alias
// another namespace by embedding the separator.
//
// Per-tenant separation holds only when each tenant's CacheConfig resolves a
// distinct Redis instance or logical DB — the Redis client does no key
// prefixing, so tenants sharing one address+DB share this keyspace too.
func replayKeyIn(ns, jti string) string {
	return "jose:jti:" + strconv.Itoa(len(ns)) + ":" + ns + ":" + jti
}
