package auth

import (
	"context"
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
	// Aliasing contract: Claims is READ-ONLY. It is shared by every reader of the
	// context — the map is never copied on the way in or out — so mutating it
	// corrupts every other reader's view of the same request identity. Copy the
	// value out before modifying it.
	Claims map[string]any
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
