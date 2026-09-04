package messaging

import (
	"errors"
	"fmt"
	"reflect"

	"github.com/gaborage/go-bricks/messaging/internal/sealruntime"
)

// Payload sealing is opt-in at the build graph (ADR-097, on the ADR-091 pattern): the
// typed publish door only probes T for the `seal` tag family; the codec that scans and
// seals lives in messaging/sealed and registers itself from init. The app configures the
// runtime facts — key store, Activation selector, tenancy, meter — before declarations
// are collected, so a seal-tagged declaration fails startup, never a publish.

// ErrSealingNotLinked is the startup error for a seal-tagged type declared in a build
// that never imported messaging/sealed.
var ErrSealingNotLinked = sealruntime.ErrNotLinked

// ErrNotSealTagged is returned by Publisher.Seal for a type that carries no seal tags.
var ErrNotSealTagged = errors.New("messaging: Seal on a type that carries no seal tags; a plain event goes to the outbox as a struct payload")

// Aliases of the seam types, so messaging/sealed and the app name them from here.
type (
	// SealCodec is what messaging/sealed registers.
	SealCodec = sealruntime.Codec
	// SealRuntime is what the app configures at bootstrap.
	SealRuntime = sealruntime.Runtime
	// SealTenancy is the tenancy fact the opener's tid rule reads.
	SealTenancy = sealruntime.Tenancy
	// SealKeyStore is the app.KeyStore subset sealing needs.
	SealKeyStore = sealruntime.KeyStore
	// Sealer turns one event into its sealed wire bytes.
	Sealer = sealruntime.Sealer
)

const (
	SealTenancyDisabled  = sealruntime.TenancyDisabled
	SealTenancyShared    = sealruntime.TenancyShared
	SealTenancyPerTenant = sealruntime.TenancyPerTenant
)

// RegisterSealCodec installs the sealing codec. A blank import of messaging/sealed does
// this from init; a second registration panics.
func RegisterSealCodec(c SealCodec) { sealruntime.Register(c) }

// ConfigureSealing records the runtime facts sealing needs. The app calls it once before
// collecting declarations; a seal-tagged declaration collected before it fails Validate.
func ConfigureSealing(rt *SealRuntime) { sealruntime.Configure(rt) }

// SealingRuntime returns the facts ConfigureSealing recorded, or nil before it ran.
func SealingRuntime() *SealRuntime { return sealruntime.Configured() }

// sealTagName is the struct-tag key of the sealing family. Spelled here rather than
// imported: the probe must not pull the codec into every build.
const sealTagName = "seal"

// hasSealTag reports whether t (pointers unwrapped) is a struct carrying a `seal` tag at
// the depth jose/sealed.ScanType inspects: its own fields, plus the members an untagged
// embedded struct promotes (which ScanType refuses). It is a probe, not a scan: the codec
// judges the declaration, and the probe must say yes wherever the codec would speak.
func hasSealTag(t reflect.Type) bool {
	return hasSealTagIn(t, map[reflect.Type]bool{})
}

func hasSealTagIn(t reflect.Type, seen map[reflect.Type]bool) bool {
	for t != nil && t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t == nil || t.Kind() != reflect.Struct || seen[t] {
		return false
	}
	seen[t] = true
	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)
		if _, ok := field.Tag.Lookup(sealTagName); ok {
			return true
		}
		if field.Anonymous && field.Tag.Get("json") == "" && hasSealTagIn(field.Type, seen) {
			return true
		}
	}
	return false
}

// newSealer builds the sealer for a seal-tagged declaration, or reports why it cannot:
// codec not linked, runtime not configured, no key store, a refused declaration, or a
// producer that cannot resolve its Activation. Every error is recorded on the
// Declarations and surfaces from Validate as a startup failure.
func newSealer(t reflect.Type, eventType string) (Sealer, error) {
	codec := sealruntime.Registered()
	if codec == nil {
		return nil, fmt.Errorf("%w (event type %q, Go type %v)", ErrSealingNotLinked, eventType, t)
	}
	rt := sealruntime.Configured()
	if rt == nil {
		return nil, fmt.Errorf("%w (event type %q)", sealruntime.ErrNotConfigured, eventType)
	}
	if rt.KeyStore == nil {
		return nil, fmt.Errorf("%w (event type %q)", sealruntime.ErrKeyStoreMissing, eventType)
	}
	spec, err := codec.ScanType(t)
	if err != nil {
		return nil, fmt.Errorf("messaging: seal declaration of %v (event type %q): %w", t, eventType, err)
	}
	if spec == nil {
		return nil, fmt.Errorf("messaging: %v carries seal tags the codec did not recognize (event type %q)", t, eventType)
	}
	sealer, err := codec.NewSealer(spec, eventType, rt)
	if err != nil {
		return nil, fmt.Errorf("messaging: sealing producer for %v (event type %q): %w", t, eventType, err)
	}
	return sealer, nil
}
