package sealed

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"time"

	"github.com/google/uuid"

	"github.com/gaborage/go-bricks/jose"
	"github.com/gaborage/go-bricks/jose/internal/cryptoadapter"
)

// Wire constants of the sealed-message envelope v1 (ADR-097).
const (
	// TypV1 is the outer JWS `typ`: the authoritative, tamper-evident version marker and the
	// only thing that announces a sealed message — there is no AMQP header for it.
	TypV1 = "vnd.gobricks.sealed.v1+json"
	// ContentTypeJSON is the `cty` both layers must carry.
	ContentTypeJSON = jose.DefaultCty

	// Protected-header params the sealer writes beyond alg/kid/typ/cty.
	HeaderSealedPaths = "sp"   // outer: the signed manifest of sealed member names
	HeaderJTI         = "jti"  // outer: fresh UUID minted here, the signed half of the Dedup key
	HeaderIssuedAt    = "iat"  // outer: seal time, unix seconds, informational
	HeaderEventType   = "etyp" // outer: the declared EventType, enforced by the opener
	HeaderTenantID    = "tid"  // outer: the ADR-087 tenant stamp, signed
	HeaderIssuer      = "iss"  // inner: the outer kid, binding authorship to the ciphertext

	// Algorithms are fixed in v1; the opener accepts RS256 as well, the sealer emits PS256.
	// Named as untyped strings / parent defaults so this file never imports go-jose under a
	// second alias; the cryptoadapter option types pin them to go-jose's algorithm types.
	sigAlg = "PS256"
	keyAlg = jose.DefaultKeyAlg
	enc    = jose.DefaultEnc
)

// Options carries what Seal needs beyond the event and its Spec. The concrete kids are the
// producer's ACTIVE Generations of the two Logical kids the Spec names — resolving
// Activation is the caller's job (messaging/sealed), keeping this package keystore-agnostic.
type Options struct {
	// SignKid is the concrete sign Generation (`<Spec.SignLogical>-v<N>`); its PRIVATE key signs.
	SignKid string
	// EncryptKid is the concrete encrypt Generation (`<Spec.EncryptLogical>-v<N>`); its PUBLIC key wraps the CEK.
	EncryptKid string
	// EventType is the publisher declaration's EventType, written as `etyp`. Required.
	EventType string
	// TenantID is the ADR-087 tenant stamp, written as `tid` when non-empty and omitted otherwise.
	TenantID string
	// Keys resolves the two concrete kids per call; registration-time resolution is a check, never a cache.
	Keys jose.KeyResolver
	// Now supplies `iat`; nil means time.Now. It exists for deterministic tests, not for callers to backdate.
	Now func() time.Time
}

// Seal turns one event into its sealed wire bytes: marshal evt, encrypt the Subject member
// as a compact JWE (RSA-OAEP-256 + A256GCM, `iss` = SignKid), splice it in place of the
// plaintext, and sign the whole document as a compact JWS (PS256, TypV1, signed `sp`,
// `jti`, `iat`, `etyp`, `tid`). Seal runs once per call: a fresh `jti` is minted here and
// nothing the caller passes can choose it. The result is the exact body to publish or
// persist — a redelivery is the same bytes.
//
// evt must be a value or pointer of spec.Type. Failures are *jose.Error values: a key the
// resolver cannot supply propagates the resolver's own error; everything else carries one
// of this package's sentinels and codes.
func Seal(evt any, spec *Spec, opts *Options) ([]byte, error) {
	if err := opts.Validate(spec); err != nil {
		return nil, err
	}
	if t := unwrapPointer(reflect.TypeOf(evt)); t != spec.Type {
		return nil, sealError(CodeTypeMismatch, fmt.Sprintf("event type %v does not match the scanned %v", t, spec.Type), nil)
	}
	signKey, err := opts.Keys.PrivateKey(opts.SignKid)
	if err != nil {
		return nil, err
	}
	encKey, err := opts.Keys.PublicKey(opts.EncryptKid)
	if err != nil {
		return nil, err
	}

	plain, err := json.Marshal(evt)
	if err != nil {
		// SECURITY: encoding/json embeds value bytes in some marshal errors (an invalid
		// json.Number literal, a MarshalJSON syntax error); the Subject may be among them,
		// so the cause is reported by type only (ADR-081 class).
		return nil, sealError(CodeSealFailed, "failed to marshal event", marshalErrorType(err))
	}
	span, err := locateSubject(plain, spec.SubjectPath)
	if err != nil {
		return nil, sealError(CodeDocumentInvalid, fmt.Sprintf("cannot pin subject member %q", spec.SubjectPath), err)
	}

	subjectJWE, err := cryptoadapter.Encrypt(span.value, encKey, &cryptoadapter.EncryptOptions{
		Kid:    opts.EncryptKid,
		KeyAlg: keyAlg,
		Enc:    enc,
		Cty:    ContentTypeJSON,
		Extra:  map[string]any{HeaderIssuer: opts.SignKid},
	})
	if err != nil {
		return nil, sealError(CodeSealFailed, "failed to encrypt subject", err)
	}
	doc, err := splice(plain, span, subjectJWE)
	if err != nil {
		return nil, sealError(CodeSealFailed, "failed to splice subject", err)
	}

	compact, err := cryptoadapter.Sign(doc, signKey, &cryptoadapter.SignOptions{
		Kid:    opts.SignKid,
		SigAlg: sigAlg,
		Cty:    ContentTypeJSON,
		Typ:    TypV1,
		Extra:  outerExtra(spec, opts),
	})
	if err != nil {
		return nil, sealError(CodeSealFailed, "failed to sign sealed document", err)
	}
	return []byte(compact), nil
}

// outerExtra builds the signed slots: sp, jti (minted here), iat, etyp and tid when a tenant resolved.
func outerExtra(spec *Spec, opts *Options) map[string]any {
	now := time.Now
	if opts.Now != nil {
		now = opts.Now
	}
	extra := map[string]any{
		HeaderSealedPaths: spec.SealedPaths(),
		HeaderJTI:         uuid.NewString(),
		HeaderIssuedAt:    now().Unix(),
		HeaderEventType:   opts.EventType,
	}
	if opts.TenantID != "" {
		extra[HeaderTenantID] = opts.TenantID
	}
	return extra
}

// Bounds on the caller-supplied signed slots: etyp mirrors the AMQP shortstr limit the
// EventType already lives under; tid mirrors the multitenant tenant-id cap. An oversize
// value would still seal, but could push the protected header past the opener's peek cap.
const (
	MaxEventTypeLen = 255
	MaxTenantIDLen  = 64
)

// marshalErrorType renders a marshal failure by type only. A *json.MarshalerError names
// the offending Go type and the class of its inner error; nothing carries value bytes.
func marshalErrorType(err error) error {
	var me *json.MarshalerError
	if errors.As(err, &me) {
		return fmt.Errorf("%T on %v: %T", me, me.Type, me.Err)
	}
	return fmt.Errorf("%T", err)
}

// Validate is the key-free pre-flight Seal runs first: the Spec and Options are complete
// and both concrete kids are Generations of the Spec's Logical kids. A producer can call it
// at declaration time to fail startup before any event or key material is involved.
func (o *Options) Validate(spec *Spec) error {
	switch {
	case spec == nil || spec.Type == nil:
		return sealError(CodeOptionsInvalid, "Seal requires a Spec from ScanType", nil)
	case o == nil:
		return sealError(CodeOptionsInvalid, "Seal requires Options", nil)
	case o.Keys == nil:
		return sealError(CodeOptionsInvalid, "Seal requires a KeyResolver", nil)
	case o.EventType == "":
		return sealError(CodeOptionsInvalid, "Seal requires a non-empty EventType", nil)
	case len(o.EventType) > MaxEventTypeLen:
		return sealError(CodeOptionsInvalid, fmt.Sprintf("EventType exceeds %d bytes (length %d)", MaxEventTypeLen, len(o.EventType)), nil)
	case len(o.TenantID) > MaxTenantIDLen:
		return sealError(CodeOptionsInvalid, fmt.Sprintf("TenantID exceeds %d bytes (length %d)", MaxTenantIDLen, len(o.TenantID)), nil)
	}
	if err := checkFamily(o.SignKid, spec.SignLogical, tagKeySign); err != nil {
		return err
	}
	return checkFamily(o.EncryptKid, spec.EncryptLogical, tagKeyEncrypt)
}

// checkFamily pins a concrete kid to the declared Logical kid before any key is touched.
func checkFamily(kid, logical, role string) error {
	family, _, ok := SplitGenerationKid(kid)
	if !ok || family != logical {
		return &jose.Error{
			Sentinel: ErrKidFamilyMismatch,
			Code:     CodeKidFamilyMismatch,
			Message:  fmt.Sprintf("%s kid is not a generation of the declared logical kid %q", role, logical),
			Kid:      kid,
		}
	}
	return nil
}

func sealError(code, msg string, cause error) *jose.Error {
	return &jose.Error{Sentinel: ErrSealFailed, Code: code, Message: msg, Cause: cause}
}
