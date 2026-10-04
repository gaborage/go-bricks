package sealed

import (
	"context"
	"errors"

	"github.com/gaborage/go-bricks/internal/sealruntime"
	"github.com/gaborage/go-bricks/jose"
	josesealed "github.com/gaborage/go-bricks/jose/sealed"
	"github.com/gaborage/go-bricks/keystore"
)

var _ sealruntime.VerifierProvider = codec{}

// NewVerifier builds the producer's verification for stored sealed bytes: wire kids resolve as
// PUBLIC keys by entry name, with no activation filter, so every provisioned RSA generation of
// both families (never a secret) is tagged as seal material here, where the app's role log still sees it.
// A verified body is then admitted only under a sign generation the key set indexes with its private key.
func (codec) NewVerifier(sp sealruntime.Spec, eventType string, rt *sealruntime.Runtime) (sealruntime.Verifier, error) {
	inner, families, err := bind(sp, rt)
	if err != nil {
		return nil, err
	}
	if eventType == "" {
		return nil, errors.New("messaging/sealed: a sealed publisher needs a non-empty EventType (the signed etyp is pinned to it)")
	}
	for _, logical := range []string{inner.SignLogical, inner.EncryptLogical} {
		for _, gen := range families.Generations(logical) {
			if gen.Role != keystore.RoleSecret {
				recordSealRole(rt.KeyStore, gen.Kid())
			}
		}
	}
	return &verifier{spec: inner, eventType: eventType, keys: jose.NewKeyStoreResolver(rt.KeyStore), families: families}, nil
}

// verifier is bound to one publisher declaration; immutable, shared by every goroutine and tenant.
type verifier struct {
	spec      *josesealed.Spec
	eventType string
	keys      jose.KeyResolver
	families  keystore.FamilyEnumerator
}

// notSignableMessage is static: the refusal carries no wire value beyond the kid.
const notSignableMessage = "sign kid generation is held without its private key; this producer could not have signed it"

// Verify runs jose/sealed.Verify with no tid rule (the door judges the tid against the tenant it will
// stamp), then refuses a sign generation this producer could not have signed under.
func (v *verifier) Verify(_ context.Context, body []byte) (sealruntime.Envelope, error) {
	env, err := josesealed.Verify(body, v.spec, &josesealed.OpenOptions{EventType: v.eventType, Keys: v.keys})
	if err != nil {
		return sealruntime.Envelope{}, refuse(err)
	}
	if !v.canSign(env.SignKid) {
		return sealruntime.Envelope{}, refuse(&josesealed.OpenError{
			Err: &jose.Error{
				Sentinel: josesealed.ErrKidUnknownGeneration, Code: josesealed.CodeKidUnknownGeneration,
				Message: notSignableMessage, Kid: env.SignKid,
			},
			Rule: 4,
		})
	}
	return sealruntime.Envelope(*env), nil
}

// canSign reports whether the key set indexes kid's sign generation with its private key; it reads
// the role only and resolves no private key.
func (v *verifier) canSign(kid string) bool {
	for _, gen := range v.families.Generations(v.spec.SignLogical) {
		if gen.Kid() == kid {
			return gen.Role == keystore.RolePrivate
		}
	}
	return false
}
