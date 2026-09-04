package messaging

import (
	"errors"
	"fmt"
	"reflect"
	"sync"

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

// SealTagName is the struct-tag key of the sealing family. Spelled here rather than
// imported from jose/sealed: the probe must not pull the codec into every build, and
// jose must not import messaging. messaging/sealed pins the two spellings together.
const SealTagName = "seal"

// IsSealTagged reports whether t (pointers unwrapped) is a struct that carries a `seal`
// tag anywhere the typed publish door would refuse to publish as plaintext: on its own
// fields or the members an untagged embedded struct promotes (the depth
// jose/sealed.ScanType inspects), OR misplaced on a named nested field or a tagged embed
// (which DeclareTypedPublisher refuses outright). It is a probe, not a scan: the codec
// judges the declaration; the probe must say yes wherever the codec would speak or the
// door would fail closed. It is the one detector every lane guard shares — the typed
// publish door, the streams typed declarations (which refuse a sealed T in v1) and the
// outbox door (which refuses a sealed struct payload) — so a struct is "sealed" in exactly
// one way.
//
// The outbox door asks on every Publish, so the answer is memoized per Go type: the
// set of payload types a process publishes is small and fixed, and a type's tags
// never change.
func IsSealTagged(t reflect.Type) bool {
	t = derefType(t)
	if t == nil || t.Kind() != reflect.Struct {
		return false
	}
	if v, ok := sealTagCache.Load(t); ok {
		tagged, _ := v.(bool)
		return tagged
	}
	v := hasSealTagIn(t, nil) || misplacedSealTag(t) != ""
	sealTagCache.Store(t, v)
	return v
}

// sealTagCache memoizes IsSealTagged per struct type: keyed by the reflect.Type
// itself (comparable, unique per named type), never by shape, so two types with
// identical fields but different tags never alias.
var sealTagCache sync.Map

// derefType strips every pointer level from t; nil stays nil.
func derefType(t reflect.Type) reflect.Type {
	for t != nil && t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	return t
}

// hasSealTagIn walks t's own fields and recurses into untagged embedded structs.
// seen guards against embedding cycles and is allocated only once recursion
// starts, so the common flat struct costs no allocation.
func hasSealTagIn(t reflect.Type, seen map[reflect.Type]bool) bool {
	t = derefType(t)
	if t == nil || t.Kind() != reflect.Struct || seen[t] {
		return false
	}
	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)
		if _, ok := field.Tag.Lookup(SealTagName); ok {
			return true
		}
		if field.Anonymous && field.Tag.Get("json") == "" {
			if seen == nil {
				seen = map[reflect.Type]bool{}
			}
			seen[t] = true
			if hasSealTagIn(field.Type, seen) {
				return true
			}
		}
	}
	return false
}

// misplacedSealTag reports the first `seal` tag that sits where neither the probe nor
// jose/sealed.ScanType looks — on or under a NAMED nested struct field, or inside a tagged
// embed — as a dotted field path, or "" when none. Such a tag would otherwise ship in
// plaintext silently, so DeclareTypedPublisher refuses the declaration.
func misplacedSealTag(t reflect.Type) string {
	return misplacedSealTagIn(t, "", true, map[sealTagVisit]bool{})
}

// sealTagVisit keys the cycle guard by type AND position: the same struct can be
// reached first as an untagged embed (supported) and again as a named field
// (unsupported), and the second visit must still judge its tags.
type sealTagVisit struct {
	typ       reflect.Type
	supported bool
}

func misplacedSealTagIn(t reflect.Type, path string, supported bool, seen map[sealTagVisit]bool) string {
	for t != nil && t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	visit := sealTagVisit{typ: t, supported: supported}
	if t == nil || t.Kind() != reflect.Struct || seen[visit] {
		return ""
	}
	seen[visit] = true
	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)
		fieldPath := field.Name
		if path != "" {
			fieldPath = path + "." + field.Name
		}
		_, tagged := field.Tag.Lookup(SealTagName)
		if tagged && !supported {
			return fieldPath
		}
		promoted := supported && field.Anonymous && field.Tag.Get("json") == ""
		if found := misplacedSealTagIn(field.Type, fieldPath, promoted, seen); found != "" {
			return found
		}
	}
	return ""
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
