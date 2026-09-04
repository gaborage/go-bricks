package sealed

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/gaborage/go-bricks/jose"
	josesealed "github.com/gaborage/go-bricks/jose/sealed"
	"github.com/gaborage/go-bricks/keystore"
	"github.com/gaborage/go-bricks/messaging/internal/sealruntime"
)

// ErrFamilyUnprovisioned fires at consumer startup when a Logical kid the
// declaration names has no provisioned generation on this side: nothing could
// verify or decrypt, so the consumer never starts.
var ErrFamilyUnprovisioned = errors.New("messaging/sealed: family has no provisioned generation on this consumer")

// ErrGenerationUnresolvable fires at consumer startup when a provisioned generation
// is indexed under the right role but the key store cannot hand out its material in
// that role — a consumer that started anyway would refuse every delivery under that kid.
var ErrGenerationUnresolvable = errors.New("messaging/sealed: provisioned generation does not resolve in the consumer's role")

var _ sealruntime.OpenerProvider = codec{}

// NewOpener is the consumer's startup fail-fast (#1306 "ResolvePolicy-parity", ADR-097):
// the sign family has at least one provisioned generation and every generation holds
// an RSA key (public is enough to verify); the encrypt family has at least one
// provisioned generation and every generation holds a PRIVATE key (the consumer
// decrypts with it); every generation actually resolves through the resolver in that
// role, as jose.ResolvePolicy resolves a route's kids at registration; the
// declaration's EventType is non-empty. The accept set IS the local keystore, so this
// checks provisioning, never activation — the wire kid is still resolved per message,
// and the startup resolution is a check, never a cache.
func (codec) NewOpener(sp sealruntime.Spec, eventType string, rt *sealruntime.Runtime) (sealruntime.Opener, error) {
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
		return nil, errors.New("messaging/sealed: a sealed consumer needs a non-empty EventType (the signed etyp is pinned to it)")
	}
	keys := jose.NewKeyStoreResolver(rt.KeyStore)
	if err := provisionedWithRole(families, keys, s.inner.SignLogical, keystore.RolePublicOnly, "sign"); err != nil {
		return nil, err
	}
	if err := provisionedWithRole(families, keys, s.inner.EncryptLogical, keystore.RolePrivate, "encrypt"); err != nil {
		return nil, err
	}
	return &opener{spec: s.inner, eventType: eventType, keys: keys}, nil
}

// provisionedWithRole checks a family's provisioned generations for the consumer's
// inherited role: every generation is RSA material, when need is RolePrivate every
// generation holds the private key (a private entry also serves as public), and each
// generation resolves through keys in that role — the index says what an entry
// SHOULD hold, the resolver proves it does.
func provisionedWithRole(families keystore.FamilyEnumerator, keys jose.KeyResolver, logical string, need keystore.Role, side string) error {
	gens := families.Generations(logical)
	if len(gens) == 0 {
		return fmt.Errorf("%w: %s family %q (expected a keystore.keys entry named %s-v<N>)", ErrFamilyUnprovisioned, side, logical, logical)
	}
	for _, gen := range gens {
		switch {
		case gen.Role == keystore.RoleSecret:
			return fmt.Errorf("%w: %s generation %s holds a symmetric secret, not an RSA key", ErrRoleMismatch, side, gen.Kid())
		case need == keystore.RolePrivate && gen.Role != keystore.RolePrivate:
			return fmt.Errorf("%w: %s generation %s holds no private key (the consumer decrypts with it)", ErrRoleMismatch, side, gen.Kid())
		}
		if err := resolveInRole(keys, gen.Kid(), need); err != nil {
			return fmt.Errorf("%w: %s generation %s: %w", ErrGenerationUnresolvable, side, gen.Kid(), err)
		}
	}
	return nil
}

// resolveInRole asks the resolver for the material the consumer will use: the private
// key to decrypt an encrypt generation; the public key to verify a sign generation,
// which a private entry also serves (its public half is derivable), so a private-only
// store is accepted for the sign side.
func resolveInRole(keys jose.KeyResolver, kid string, need keystore.Role) error {
	if need == keystore.RolePrivate {
		_, err := keys.PrivateKey(kid)
		return err
	}
	if _, err := keys.PublicKey(kid); err == nil {
		return nil
	}
	_, err := keys.PrivateKey(kid)
	return err
}

// opener is bound to one consumer declaration: its Spec, its EventType and the
// resolver the wire kids resolve through per message. Immutable, shared by every
// worker and tenant.
type opener struct {
	spec      *josesealed.Spec
	eventType string
	keys      jose.KeyResolver
}

// Open runs jose/sealed.Open under the caller's tid rule and reports the outcome on
// the seal instruments: every open records its duration, a refused one also counts
// under its code. A refusal is an *sealruntime.OpenRefusedError whose text is the
// code and the presence/length details only; the opener's own error stays in the
// chain, so errors.Is against jose/sealed's sentinels keeps working downstream.
func (o *opener) Open(ctx context.Context, body []byte, want sealruntime.TenantRule, out any) (sealruntime.Envelope, error) {
	start := time.Now()
	env, err := josesealed.Open(body, o.spec, &josesealed.OpenOptions{
		EventType: o.eventType,
		Tenant:    josesealed.TenantExpectation(want),
		Keys:      o.keys,
	}, out)
	metrics := sealruntime.Instruments()
	metrics.RecordOperation(ctx, sealruntime.OpOpen, time.Since(start))
	if err != nil {
		refused := refuse(err)
		metrics.RecordOpenFailure(ctx, refused.Code)
		return sealruntime.Envelope{}, refused
	}
	return sealruntime.Envelope(*env), nil
}

// refuse maps an opener error onto the seam's error: the code and details of the
// *jose/sealed.OpenError when there is one (every rule and pre-flight failure), the
// bare *jose.Error code otherwise; Recoverable marks the unknown-generation class.
func refuse(err error) *sealruntime.OpenRefusedError {
	refused := &sealruntime.OpenRefusedError{
		Cause:       err,
		Recoverable: errors.Is(err, josesealed.ErrKidUnknownGeneration),
	}
	var oe *josesealed.OpenError
	var je *jose.Error
	switch {
	case errors.As(err, &oe):
		refused.Code, refused.Details = oe.Err.Code, oe.Details
	case errors.As(err, &je):
		refused.Code = je.Code
	default:
		refused.Code = "SEAL_OPEN_FAILED"
	}
	return refused
}
