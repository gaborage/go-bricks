package jose

import "context"

// ctxKey is an unexported type to prevent cross-package collisions on context.Value lookups.
type ctxKey int

const (
	keyInboundVerified ctxKey = iota
	keyClaims
	keyPolicy
)

// WithInboundVerified marks the context as having passed JOSE inbound decryption + signature
// verification. The presence of this marker (not its value) gates outbound encryption per
// the security invariant: a response is JOSE-encrypted iff this is set AND the route has
// an outbound policy.
func WithInboundVerified(ctx context.Context) context.Context {
	return context.WithValue(ctx, keyInboundVerified, true)
}

// IsInboundVerified reports whether the context was marked verified by a successful
// inbound JOSE decode.
func IsInboundVerified(ctx context.Context) bool {
	v, _ := ctx.Value(keyInboundVerified).(bool)
	return v
}

// WithClaims attaches the verified claim set extracted from the inbound JWS payload.
// Applications retrieve it via ClaimsFromContext to enforce iat/exp/jti policies.
func WithClaims(ctx context.Context, c *Claims) context.Context {
	return context.WithValue(ctx, keyClaims, c)
}

// ClaimsFromContext returns the verified Claims attached by the inbound middleware,
// or nil if no JOSE verification ran for this request.
func ClaimsFromContext(ctx context.Context) *Claims {
	c, _ := ctx.Value(keyClaims).(*Claims)
	return c
}

// WithPolicy attaches the given outbound Policy to the context for later retrieval
// via PolicyFromContext. The current server wiring threads the outbound policy as an
// explicit parameter and stores it on the route descriptor rather than on the context,
// so this helper has no callers today; it remains as a context-key accessor pair.
func WithPolicy(ctx context.Context, p *Policy) context.Context {
	return context.WithValue(ctx, keyPolicy, p)
}

// PolicyFromContext returns the outbound Policy attached via WithPolicy, or nil if
// none was attached (the default in the current wiring).
func PolicyFromContext(ctx context.Context) *Policy {
	p, _ := ctx.Value(keyPolicy).(*Policy)
	return p
}
