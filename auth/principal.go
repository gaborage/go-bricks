package auth

import (
	"context"
	"fmt"
	"time"
)

// Principal is the identity a verified credential asserts. It is attached to the
// request context by the auth middleware and read back by handlers.
//
// A Principal is identification, not authorization: its presence means the
// credential verified against the configured issuer, nothing more.
type Principal struct {
	// Subject is the "sub" claim. It MUST NOT be logged or recorded.
	Subject string

	// Issuer is the "iss" claim, already matched against the configured issuer.
	Issuer string

	// Audience is the "aud" claim, already matched against the configured audience.
	Audience []string

	// ExpiresAt is the "exp" claim. A credential without one never verifies.
	ExpiresAt time.Time

	// IssuedAt is the "iat" claim; the zero time when the claim was absent.
	IssuedAt time.Time

	// Claims is the raw decoded payload.
	//
	// Aliasing contract: Claims is READ-ONLY. The map is never copied on the way
	// in or out, so every reader of this request's context holds the same map: a
	// delete(p.Claims, …) or a write in one handler, middleware or helper
	// corrupts what every other reader sees. When one of those readers is a
	// background goroutine on a context.WithoutCancel(ctx) — the framework's own
	// pattern for work that outlives the request — the breach is a data RACE, not
	// merely a logic bug. Copy any value out before modifying it.
	//
	// No defensive copy is made on purpose. json.Unmarshal yields nested []any
	// and map[string]any for precisely the claims most likely to be mutated
	// ("scope", "realm_access.roles"), so a shallow copy would cost an allocation
	// on every request while advertising a safety it does not provide for those
	// nested values.
	Claims map[string]any
}

// String renders the Principal without its subject or any claim value.
//
// SECURITY: this is the elision seam. A Principal travels on the request
// context, so it lands in a downstream log.Info().Interface("principal", p) or
// fmt.Errorf("%v", p) by accident; the logger's SensitiveDataFilter matches
// field NAMES and cannot help, because "Subject" is not a sensitive name and a
// claim key is attacker-chosen. Rendering therefore drops Subject and Claims and
// keeps only the already-public issuer, audience and expiry.
//
// It covers every verb that consults fmt.Stringer — %v, %s, %q, %+v — for both
// Principal and *Principal, since the receiver is a value. It does NOT cover
// %#v, which prints the Go-syntax representation by design and bypasses
// Stringer, nor a struct-walking encoder such as json.Marshal or the logger's
// reflective filter. Those render the fields directly and must not be pointed at
// a Principal.
func (p Principal) String() string {
	return fmt.Sprintf("auth.Principal{issuer: %q, audience: %q, expiresAt: %s, subject: <elided>, claims: <elided:%d>}",
		p.Issuer, p.Audience, p.ExpiresAt.Format(time.RFC3339), len(p.Claims))
}

// Claim returns the raw claim stored under name, and whether it was present.
// A present claim whose value is null returns (nil, true).
//
//nolint:gocritic // hugeParam: Principal is a value type by contract — it travels on context.Value.
func (p Principal) Claim(name string) (value any, ok bool) {
	value, ok = p.Claims[name]
	return value, ok
}

// ctxKey is an unexported type to prevent cross-package collisions on context.Value lookups.
type ctxKey int

const keyPrincipal ctxKey = iota

// withPrincipal attaches the verified identity to ctx.
//
//nolint:gocritic // hugeParam: the context stores the Principal by value; a pointer would only add an alias.
func withPrincipal(ctx context.Context, p Principal) context.Context {
	return context.WithValue(ctx, keyPrincipal, p)
}

// PrincipalFromContext returns the identity the auth middleware verified for this
// request, and whether one was attached. Absence (ok == false) means the request
// skipped the middleware or carried no credential.
func PrincipalFromContext(ctx context.Context) (p Principal, ok bool) {
	p, ok = ctx.Value(keyPrincipal).(Principal)
	return p, ok
}
