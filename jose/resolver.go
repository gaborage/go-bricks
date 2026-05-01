package jose

import (
	"crypto/rsa"
)

// KeyResolver abstracts key lookup so the jose package does not depend directly on
// app.KeyStore. This keeps the door open for future JWKS-URL backed resolvers without
// breaking callers, and makes testing trivial (NewTestResolver in jose/testing/).
//
// Public/private semantics map to JOSE roles:
//   - PrivateKey: used to decrypt inbound JWE and sign outbound JWS.
//   - PublicKey:  used to verify inbound JWS and encrypt outbound JWE.
//
// Implementations MUST return the registered ErrKidUnknown sentinel (wrapped in *Error)
// when a kid is not configured, so callers can distinguish unknown-key from other
// failures via errors.Is.
type KeyResolver interface {
	PrivateKey(kid string) (*rsa.PrivateKey, error)
	PublicKey(kid string) (*rsa.PublicKey, error)
}

// KeyStoreLike is the minimal subset of app.KeyStore that the resolver consumes.
// Defining it locally lets jose/ wrap any compatible store without importing app/,
// which would create an app → server → jose → app cycle once the server module wires
// the resolver via app/module_registry.go.
type KeyStoreLike interface {
	PrivateKey(name string) (*rsa.PrivateKey, error)
	PublicKey(name string) (*rsa.PublicKey, error)
}

// KeyStoreResolver adapts a KeyStoreLike (typically an app.KeyStore) to KeyResolver.
// It is the default resolver wired into the server when a keystore module is registered.
type KeyStoreResolver struct {
	ks KeyStoreLike
}

func NewKeyStoreResolver(ks KeyStoreLike) *KeyStoreResolver {
	return &KeyStoreResolver{ks: ks}
}

func (r *KeyStoreResolver) PrivateKey(kid string) (*rsa.PrivateKey, error) {
	if r == nil || r.ks == nil {
		return nil, &Error{
			Sentinel: ErrKeyResolution,
			Code:     codeKeystoreUnavailable,
			Message:  "no keystore configured; register a KeyStore module before declaring jose-tagged routes",
			Kid:      kid,
		}
	}
	pk, err := r.ks.PrivateKey(kid)
	if err != nil {
		return nil, &Error{
			Sentinel: ErrKidUnknown,
			Code:     codeKidUnknown,
			Message:  "private key not registered",
			Kid:      kid,
			Cause:    err,
		}
	}
	return pk, nil
}

func (r *KeyStoreResolver) PublicKey(kid string) (*rsa.PublicKey, error) {
	if r == nil || r.ks == nil {
		return nil, &Error{
			Sentinel: ErrKeyResolution,
			Code:     codeKeystoreUnavailable,
			Message:  "no keystore configured; register a KeyStore module before declaring jose-tagged routes",
			Kid:      kid,
		}
	}
	pk, err := r.ks.PublicKey(kid)
	if err != nil {
		return nil, &Error{
			Sentinel: ErrKidUnknown,
			Code:     codeKidUnknown,
			Message:  "public key not registered",
			Kid:      kid,
			Cause:    err,
		}
	}
	return pk, nil
}

// ResolvePolicy verifies that every kid named in the policy resolves to a key of the
// correct role via the resolver. Called once at registration time per route — failures
// must fail process startup (Fail Fast principle).
func ResolvePolicy(r KeyResolver, p *Policy) error {
	if p == nil {
		return nil
	}
	switch p.Direction {
	case DirectionInbound:
		if _, err := r.PrivateKey(p.DecryptKid); err != nil {
			return err
		}
		if _, err := r.PublicKey(p.VerifyKid); err != nil {
			return err
		}
	case DirectionOutbound:
		if _, err := r.PrivateKey(p.SignKid); err != nil {
			return err
		}
		if _, err := r.PublicKey(p.EncryptKid); err != nil {
			return err
		}
	}
	return nil
}
