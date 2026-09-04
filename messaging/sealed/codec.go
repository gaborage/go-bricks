package sealed

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"time"

	"github.com/gaborage/go-bricks/jose"
	josesealed "github.com/gaborage/go-bricks/jose/sealed"
	"github.com/gaborage/go-bricks/keystore"
	"github.com/gaborage/go-bricks/messaging"
	"github.com/gaborage/go-bricks/messaging/internal/sealruntime"
	"github.com/gaborage/go-bricks/messaging/internal/tenantstamp"
)

// ErrKeyStoreNoFamilies fires when the registered key store cannot enumerate generation
// families — sealing needs the keystore module's store, not an arbitrary KeyStore.
var ErrKeyStoreNoFamilies = errors.New("messaging/sealed: the registered key store does not enumerate generation families; sealing needs the keystore module's store")

// ErrRoleMismatch fires at startup when an active generation lacks the role its side needs.
var ErrRoleMismatch = errors.New("messaging/sealed: active generation lacks the role its role in sealing requires")

// codec adapts jose/sealed to the messaging seam.
type codec struct{}

var _ messaging.SealCodec = codec{}

// spec wraps the scanned declaration; messaging sees only the two Logical kids.
type spec struct{ inner *josesealed.Spec }

func (s spec) SignLogical() string    { return s.inner.SignLogical }
func (s spec) EncryptLogical() string { return s.inner.EncryptLogical }

// ScanType scans t for the seal tag family; (nil, nil) for a plain type.
func (codec) ScanType(t reflect.Type) (sealruntime.Spec, error) {
	inner, err := josesealed.ScanType(t)
	if err != nil {
		return nil, err
	}
	if inner == nil {
		return nil, nil
	}
	return spec{inner: inner}, nil
}

// NewSealer is the producer's startup fail-fast: resolve the ACTIVE generation of each
// Logical kid (keystore.ActiveGeneration — one provisioned auto-activates, several need
// the messaging.seal.active selector), require the sign generation to hold a PRIVATE key
// and the encrypt generation a PUBLIC one, and pre-flight the sealer's options without
// touching key material. Keys themselves resolve per call through jose.KeyResolver.
func (codec) NewSealer(sp sealruntime.Spec, eventType string, rt *sealruntime.Runtime) (sealruntime.Sealer, error) {
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
	signGen, err := activeWithRole(families, rt.Active, s.inner.SignLogical, keystore.RolePrivate, "sign")
	if err != nil {
		return nil, err
	}
	encGen, err := activeWithRole(families, rt.Active, s.inner.EncryptLogical, keystore.RolePublicOnly, "encrypt")
	if err != nil {
		return nil, err
	}
	template := josesealed.Options{
		SignKid:    signGen.Kid(),
		EncryptKid: encGen.Kid(),
		EventType:  eventType,
		Keys:       jose.NewKeyStoreResolver(rt.KeyStore),
	}
	if err := template.Validate(s.inner); err != nil {
		return nil, err
	}
	return &sealer{spec: s.inner, template: template}, nil
}

// activeWithRole resolves the active generation of logical and checks it holds the role
// the side needs: the producer signs with a PRIVATE key and encrypts to a PUBLIC one (a
// private entry also serves as public, so RolePrivate satisfies the encrypt side too).
func activeWithRole(families keystore.FamilyEnumerator, active map[string]string, logical string, need keystore.Role, side string) (keystore.Generation, error) {
	gen, err := keystore.ActiveGeneration(families, active, logical)
	if err != nil {
		return keystore.Generation{}, fmt.Errorf("messaging/sealed: %s family: %w", side, err)
	}
	switch {
	case gen.Role == keystore.RoleSecret:
		return keystore.Generation{}, fmt.Errorf("%w: %s generation %s holds a symmetric secret, not an RSA key", ErrRoleMismatch, side, gen.Kid())
	case need == keystore.RolePrivate && gen.Role != keystore.RolePrivate:
		return keystore.Generation{}, fmt.Errorf("%w: %s generation %s holds no private key (the producer signs with it)", ErrRoleMismatch, side, gen.Kid())
	}
	return gen, nil
}

// sealer is bound to one declaration: its Spec, the active concrete kids and the
// EventType. It is immutable and shared by every goroutine and tenant.
type sealer struct {
	spec     *josesealed.Spec
	template josesealed.Options
}

// Seal mirrors the ADR-087 tenant stamp into the signed tid — the tenant the context
// carries, absent when none resolves — and runs jose/sealed.Seal once. The stamping
// wrapper later writes the same tenant onto the AMQP header from the same context, so the
// signed tid and the carrier agree by construction.
func (s *sealer) Seal(ctx context.Context, evt any) ([]byte, error) {
	start := time.Now()
	opts := s.template
	tenant, err := tenantstamp.Resolve(ctx, "")
	if err != nil {
		return nil, err
	}
	opts.TenantID = tenant
	wire, err := josesealed.Seal(evt, s.spec, &opts)
	sealruntime.Instruments().RecordOperation(ctx, sealruntime.OpSeal, time.Since(start))
	return wire, err
}
