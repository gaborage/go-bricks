package sealed

import (
	"context"
	"errors"

	"github.com/gaborage/go-bricks/jose"
	josesealed "github.com/gaborage/go-bricks/jose/sealed"
	"github.com/gaborage/go-bricks/keystore"
	"github.com/gaborage/go-bricks/messaging/internal/sealruntime"
)

var _ sealruntime.VerifierProvider = codec{}

// NewVerifier builds the producer's verification for stored sealed bytes: wire kids resolve as
// PUBLIC keys by entry name, with no activation filter, so every provisioned generation of both
// families is tagged as seal material here, where the app's role log still sees it.
func (codec) NewVerifier(sp sealruntime.Spec, eventType string, rt *sealruntime.Runtime) (sealruntime.Verifier, error) {
	s, ok := sp.(spec)
	if !ok || s.inner == nil {
		return nil, errors.New("messaging/sealed: spec was not produced by this codec")
	}
	if rt == nil || rt.KeyStore == nil {
		return nil, sealruntime.ErrKeyStoreMissing
	}
	families, ok := rt.KeyStore.(keystore.FamilyEnumerator)
	if !ok {
		return nil, ErrKeyStoreNoFamilies
	}
	if eventType == "" {
		return nil, errors.New("messaging/sealed: a sealed publisher needs a non-empty EventType (the signed etyp is pinned to it)")
	}
	for _, logical := range []string{s.inner.SignLogical, s.inner.EncryptLogical} {
		for _, gen := range families.Generations(logical) {
			if gen.Role != keystore.RoleSecret {
				recordSealRole(rt.KeyStore, gen.Kid())
			}
		}
	}
	return &verifier{spec: s.inner, eventType: eventType, keys: jose.NewKeyStoreResolver(rt.KeyStore)}, nil
}

// verifier is bound to one publisher declaration; immutable, shared by every goroutine and tenant.
type verifier struct {
	spec      *josesealed.Spec
	eventType string
	keys      jose.KeyResolver
}

// Verify runs jose/sealed.Verify with no tid rule: the door judges the tid against the tenant it will stamp.
func (v *verifier) Verify(_ context.Context, body []byte) (sealruntime.Envelope, error) {
	env, err := josesealed.Verify(body, v.spec, &josesealed.OpenOptions{EventType: v.eventType, Keys: v.keys})
	if err != nil {
		return sealruntime.Envelope{}, refuse(err)
	}
	return sealruntime.Envelope(*env), nil
}
