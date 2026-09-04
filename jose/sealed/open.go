package sealed

import (
	"crypto/rsa"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	jose "github.com/go-jose/go-jose/v4"

	bricksjose "github.com/gaborage/go-bricks/jose"
	"github.com/gaborage/go-bricks/jose/internal/cryptoadapter"
)

// Opener sentinels. Every Open failure is an *OpenError whose embedded *bricksjose.Error carries
// one of these as Sentinel, so errors.Is picks the disposition class and Code names the rule.
var (
	// ErrNotSealed is the structural sentinel: the body is not a compact JWS carrying TypV1.
	ErrNotSealed = errors.New("sealed: body is not a sealed message")
	// ErrKidUnknownGeneration is the recoverable class: the wire kid is a well-formed
	// Generation of the declared family that this consumer has not provisioned yet.
	ErrKidUnknownGeneration = errors.New("sealed: kid generation not provisioned")
	// ErrOpenFailed covers every other opener rule: poison, never recoverable by provisioning.
	ErrOpenFailed = errors.New("sealed: open failed")
)

// Opener wire-protocol codes, one per rule (SEAL_TAG_* and the sealer's codes live in errors.go).
const (
	CodeNotSealed            = "NOT_SEALED"
	CodeAlgNotAllowed        = "SEAL_ALG_NOT_ALLOWED"
	CodeCtyInvalid           = "SEAL_CTY_INVALID"
	CodeCritPresent          = "SEAL_CRIT_PRESENT"
	CodeKidUnknownGeneration = "SEAL_KID_UNKNOWN_GENERATION"
	CodeSignatureInvalid     = "SEAL_SIGNATURE_INVALID"
	CodeHeaderSlotInvalid    = "SEAL_HEADER_SLOT_INVALID"
	CodeEventTypeMismatch    = "SEAL_EVENT_TYPE_MISMATCH"
	CodeTenantMismatch       = "SEAL_TENANT_MISMATCH"
	CodeManifestMismatch     = "SEAL_MANIFEST_MISMATCH"
	CodePayloadUndecodable   = "SEAL_PAYLOAD_UNDECODABLE"
	CodeAuthorshipMismatch   = "SEAL_AUTHORSHIP_MISMATCH"
	CodeDecryptFailed        = "SEAL_DECRYPT_FAILED"
)

// Detail keys an OpenError may carry. Values are presence, length and layer only — never
// a header's value (#1307: a signed value is not a log-safe value).
const (
	DetailLayer   = "layer"   // "jwe" when an inner-JWE check reused an outer code
	DetailSlot    = "slot"    // the failing authenticated slot (jti, iat, etyp, sp, tid)
	DetailPresent = "present" // "true"/"false"
	DetailLen     = "len"     // byte length of a string slot or member count of an array slot

	layerJWE = "jwe"
)

// headerIDPattern is the header-id grammar (G6): every signed jti and every header-sourced
// id must match it before reaching a ledger. dedupKeySeparator is deliberately outside it.
var headerIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

const dedupKeySeparator = ":"

var (
	openSigAlgs  = []jose.SignatureAlgorithm{jose.PS256, jose.RS256}
	openKeyAlgs  = []jose.KeyAlgorithm{keyAlg}
	openContents = []jose.ContentEncryption{enc}
)

// TenantExpectation is the tenancy-agnostic `tid` rule the caller passes: the mapping from
// shared / per-tenant / disabled tenancy to these two fields belongs to messaging/sealed.
type TenantExpectation struct {
	// Required makes an absent signed tid poison.
	Required bool
	// Expected, when non-empty, must equal a present tid. An empty Expected only checks
	// presence (per Required) and surfaces whatever tid the wire carries.
	Expected string
}

// OpenOptions carries what Open needs beyond the body and its Spec.
type OpenOptions struct {
	// EventType is the consumer declaration's EventType; the signed etyp must equal it.
	EventType string
	// Tenant is the tid rule for this delivery.
	Tenant TenantExpectation
	// Keys resolves the two wire kids per message: sign PUBLIC to verify, encrypt PRIVATE to decrypt.
	Keys bricksjose.KeyResolver
}

// Envelope is what a verified, decrypted message proves about itself. IssuedAt is the
// signed seal time, informational only — nothing here compared it to a clock.
type Envelope struct {
	JTI        string
	IssuedAt   time.Time
	EventType  string
	TenantID   string
	SignKid    string
	SignFamily string
	EncKid     string
}

// DedupKey is the ledger key: the Logical sign family (never the concrete Generation, so a
// rotation does not re-open the replay window) joined to the signed jti by a separator
// that headerIDPattern excludes, so no header-sourced id can spell it.
func (e *Envelope) DedupKey() string {
	return e.SignFamily + dedupKeySeparator + e.JTI
}

// OpenError is the opener's typed error: the *bricksjose.Error every jose failure is (errors.As
// reaches it through Unwrap, errors.Is reaches the Sentinel through it) plus the rule that
// fired and its presence/length details.
type OpenError struct {
	// Err is the *bricksjose.Error: Sentinel, Code, Message, Kid and Cause.
	Err *bricksjose.Error
	// Rule is the 1-based rule number that fired. It is a rendering aid for logs and
	// reports only: callers MUST match on Err.Code or the sentinels, never on Rule, whose
	// numbering may be renumbered when the rule set grows.
	Rule int
	// Details carries presence, length and layer facts only.
	Details map[string]string
}

func (e *OpenError) Error() string {
	if e == nil || e.Err == nil {
		return "<nil>"
	}
	msg := e.Err.Error()
	if len(e.Details) == 0 {
		return msg
	}
	keys := make([]string, 0, len(e.Details))
	for k := range e.Details {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = k + "=" + e.Details[k]
	}
	return msg + " [" + strings.Join(parts, " ") + "]"
}

// Unwrap exposes the embedded *bricksjose.Error, so errors.As(err, &joseErr) works and errors.Is
// continues to the Sentinel.
func (e *OpenError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

// Open verifies and decrypts one sealed body into out, a pointer to spec.Type. It applies
// the v1 rule set in order; the first failing rule wins and names itself through the code:
//
//	 1 compact JWS with typ TypV1                       NOT_SEALED
//	 2 alg ∈ {PS256,RS256}, cty json, no crit           SEAL_ALG_NOT_ALLOWED / SEAL_CTY_INVALID / SEAL_CRIT_PRESENT
//	 3 kid is a Generation of the sign family           SEAL_KID_FAMILY_MISMATCH
//	 4 kid resolves to a PUBLIC key                     SEAL_KID_UNKNOWN_GENERATION (recoverable)
//	 5 signature over the exact payload bytes           SEAL_SIGNATURE_INVALID
//	 6 jti / iat / etyp / sp present and well-formed    SEAL_HEADER_SLOT_INVALID
//	 7 etyp == declared EventType                       SEAL_EVENT_TYPE_MISMATCH
//	 8 tid vs TenantExpectation                         SEAL_TENANT_MISMATCH
//	 9 sp == declared sealed set                        SEAL_MANIFEST_MISMATCH
//	10 payload object, Subject is a JWE, inner header,  SEAL_PAYLOAD_UNDECODABLE / outer codes with layer=jwe /
//	   iss == kid, encrypt family, PRIVATE key, decrypt SEAL_AUTHORSHIP_MISMATCH / SEAL_DECRYPT_FAILED
//	11 splice the plaintext back, unmarshal into out    SEAL_PAYLOAD_UNDECODABLE
//	12 Envelope
//
// Rules 1–4 run on the peeked, still unauthenticated protected header, before any
// signature parsing; nothing in rules 1–9 touches the inner JWE. Keys resolve per message
// through opts.Keys. No clock is read: iat is surfaced, never judged.
func Open(body []byte, spec *Spec, opts *OpenOptions, out any) (*Envelope, error) {
	if err := checkOpenArgs(spec, opts, out); err != nil {
		return nil, err
	}
	compact := string(body)

	// Rules 1–4 on the unauthenticated peek; rule 5 authenticates the header.
	signKid, signFamily, signKey, err := peekOuter(compact, spec, opts.Keys)
	if err != nil {
		return nil, err
	}
	payload, hdr, err := cryptoadapter.Verify(compact, signKey, &cryptoadapter.VerifyOptions{ExpectedKid: signKid, AllowedSigAlgs: openSigAlgs})
	if err != nil {
		return nil, openError(5, ErrOpenFailed, CodeSignatureInvalid, "signature does not verify under the wire kid", nil)
	}

	// Rule 6 — authenticated slots (G7); rules 7–9 — the pins against the declaration.
	slots, err := checkSlots(&hdr)
	if err != nil {
		return nil, err
	}
	if pinErr := checkPins(slots, spec, opts); pinErr != nil {
		return nil, pinErr
	}

	// Rule 10 — the payload document, the inner JWE's header, authorship, encrypt family, decrypt.
	span, err := locateSubject(payload, spec.SubjectPath)
	if err != nil {
		return nil, openCause(10, CodePayloadUndecodable, fmt.Sprintf("cannot pin subject member %q", spec.SubjectPath), err)
	}
	var innerCompact string
	if unquoteErr := json.Unmarshal(span.value, &innerCompact); unquoteErr != nil || !isCompactJOSE(innerCompact) {
		return nil, openError(10, ErrOpenFailed, CodePayloadUndecodable, "subject member is not a compact JWE string", nil)
	}
	plaintext, encKid, err := openSubject(innerCompact, hdr.Kid, spec, opts.Keys)
	if err != nil {
		return nil, err
	}

	// Rule 11 — the plaintext goes back over the JWE's byte span and the document decodes as
	// T into a fresh value: out is written only once every rule has passed, so a refused
	// message leaves the caller's value untouched.
	opened := reflect.New(spec.Type)
	if err := json.Unmarshal(spliceRaw(payload, span, plaintext), opened.Interface()); err != nil {
		return nil, openCause(11, CodePayloadUndecodable, "opened document does not decode into the event type", fmt.Errorf("%T", err))
	}
	reflect.ValueOf(out).Elem().Set(opened.Elem())

	// Rule 12 — the envelope. Every slot was validated by rule 6.
	return &Envelope{
		JTI:        slots.jti,
		IssuedAt:   time.Unix(slots.issuedAt, 0).UTC(),
		EventType:  slots.eventType,
		TenantID:   slots.tenantID,
		SignKid:    hdr.Kid,
		SignFamily: signFamily,
		EncKid:     encKid,
	}, nil
}

// peekOuter runs rules 1–4 on the peeked, still unauthenticated protected header: the
// structural check and typ, G5 policy, the sign-family pin, and the PUBLIC key for the
// Generation. It returns the wire kid, its family and the key rule 5 verifies with.
func peekOuter(compact string, spec *Spec, keys bricksjose.KeyResolver) (kid, family string, key *rsa.PublicKey, err error) {
	// Rule 1 — structural: exactly three segments whose first is a JSON object, typ = v1.
	if strings.Count(compact, ".") != 2 {
		return "", "", nil, openError(1, ErrNotSealed, CodeNotSealed, "body is not a compact JWS", nil)
	}
	peek, peekErr := cryptoadapter.PeekProtectedHeader(compact)
	if peekErr != nil {
		return "", "", nil, openError(1, ErrNotSealed, CodeNotSealed, "protected header is not a JSON object", nil)
	}
	if peek.Typ != TypV1 {
		return "", "", nil, openError(1, ErrNotSealed, CodeNotSealed, fmt.Sprintf("typ is not %q", TypV1), nil)
	}

	// Rule 2 — outer header policy (G5).
	if err := checkHeaderPolicy(2, &peek, sigAlgAllowed(peek.Alg), ""); err != nil {
		return "", "", nil, err
	}

	// Rule 3 — family pin by grammar; rule 4 — the Generation must be provisioned as PUBLIC.
	family, _, ok := SplitGenerationKid(peek.Kid)
	if !ok || family != spec.SignLogical {
		return "", "", nil, familyError(3, peek.Kid, spec.SignLogical, tagKeySign, "")
	}
	key, keyErr := keys.PublicKey(peek.Kid)
	if keyErr != nil {
		return "", "", nil, unknownGenerationError(4, peek.Kid, tagKeySign, keyErr, "")
	}
	return peek.Kid, family, key, nil
}

// checkPins runs rules 7–9: the signed etyp, tid and sp against what the consumer declared.
func checkPins(slots *authenticatedSlots, spec *Spec, opts *OpenOptions) error {
	if slots.eventType != opts.EventType {
		return openError(7, ErrOpenFailed, CodeEventTypeMismatch, "signed etyp does not equal the declared EventType",
			map[string]string{DetailLen: strconv.Itoa(len(slots.eventType))})
	}
	if err := checkTenant(slots, opts.Tenant); err != nil {
		return err
	}
	if !slices.Equal(slots.sealedPaths, spec.SealedPaths()) {
		return openError(9, ErrOpenFailed, CodeManifestMismatch, "signed sp does not equal the declared sealed set",
			map[string]string{DetailLen: strconv.Itoa(len(slots.sealedPaths))})
	}
	return nil
}

// checkOpenArgs is the key-free pre-flight: wiring mistakes, reported with the sealer's
// SEAL_OPTIONS_INVALID / SEAL_TYPE_MISMATCH codes since they are the same class of error.
func checkOpenArgs(spec *Spec, opts *OpenOptions, out any) error {
	switch {
	case spec == nil || spec.Type == nil:
		return sealError(CodeOptionsInvalid, "Open requires a Spec from ScanType", nil)
	case opts == nil:
		return sealError(CodeOptionsInvalid, "Open requires OpenOptions", nil)
	case opts.Keys == nil:
		return sealError(CodeOptionsInvalid, "Open requires a KeyResolver", nil)
	case opts.EventType == "":
		return sealError(CodeOptionsInvalid, "Open requires a non-empty EventType", nil)
	}
	t := reflect.TypeOf(out)
	if t == nil || t.Kind() != reflect.Pointer || t.Elem() != spec.Type {
		return sealError(CodeTypeMismatch, fmt.Sprintf("out must be a *%v, got %v", spec.Type, t), nil)
	}
	return nil
}

// checkHeaderPolicy applies G5 to one layer's protected header: an allowed alg (the caller
// judged it against the layer's allowlist), cty application/json, no crit. layer is "" for
// the outer JWS and layerJWE for the inner JWE, where the same codes are reused.
func checkHeaderPolicy(rule int, hdr *cryptoadapter.Header, algAllowed bool, layer string) error {
	details := layerDetails(layer)
	if !algAllowed {
		return openError(rule, ErrOpenFailed, CodeAlgNotAllowed, "alg is not allowed", details)
	}
	if hdr.Cty != ContentTypeJSON {
		return openError(rule, ErrOpenFailed, CodeCtyInvalid, fmt.Sprintf("cty is not %q", ContentTypeJSON), details)
	}
	if _, present := hdr.Extra["crit"]; present {
		return openError(rule, ErrOpenFailed, CodeCritPresent, "crit is present", details)
	}
	return nil
}

func sigAlgAllowed(alg string) bool {
	return slices.Contains(openSigAlgs, jose.SignatureAlgorithm(alg))
}

// authenticatedSlots is what rule 6 extracted from the verified header for rules 7–12.
type authenticatedSlots struct {
	jti         string
	issuedAt    int64
	eventType   string
	sealedPaths []string
	tenantID    string
	tenantSet   bool
}

// checkSlots validates the four mandatory signed slots and the optional tid's shape. Every
// failure is SEAL_HEADER_SLOT_INVALID with the slot name, presence and length — never the value.
func checkSlots(hdr *cryptoadapter.Header) (*authenticatedSlots, error) {
	s := &authenticatedSlots{}
	var ok bool

	s.jti, ok = hdr.ExtraString(HeaderJTI)
	if !ok || !headerIDPattern.MatchString(s.jti) {
		return nil, slotError(HeaderJTI, hdr, len(s.jti))
	}
	iat, err := hdr.ExtraInt64(HeaderIssuedAt)
	if err != nil || iat < 0 {
		return nil, slotError(HeaderIssuedAt, hdr, 0)
	}
	s.issuedAt = iat
	s.eventType, ok = hdr.ExtraString(HeaderEventType)
	if !ok || s.eventType == "" {
		return nil, slotError(HeaderEventType, hdr, len(s.eventType))
	}
	s.sealedPaths, err = hdr.ExtraStringSlice(HeaderSealedPaths)
	if err != nil || len(s.sealedPaths) == 0 || slices.Contains(s.sealedPaths, "") {
		return nil, slotError(HeaderSealedPaths, hdr, len(s.sealedPaths))
	}
	if raw, present := hdr.Extra[HeaderTenantID]; present {
		s.tenantID, ok = raw.(string)
		if !ok {
			return nil, slotError(HeaderTenantID, hdr, 0)
		}
		s.tenantSet = true
	}
	return s, nil
}

func slotError(slot string, hdr *cryptoadapter.Header, length int) error {
	_, present := hdr.Extra[slot]
	return openError(6, ErrOpenFailed, CodeHeaderSlotInvalid, "authenticated slot is absent or malformed", map[string]string{
		DetailSlot:    slot,
		DetailPresent: strconv.FormatBool(present),
		DetailLen:     strconv.Itoa(length),
	})
}

// checkTenant is rule 8. Present-and-well-formed is rule 6's guarantee; this rule only
// decides required-and-absent and present-but-different.
func checkTenant(slots *authenticatedSlots, want TenantExpectation) error {
	details := map[string]string{
		DetailPresent: strconv.FormatBool(slots.tenantSet),
		DetailLen:     strconv.Itoa(len(slots.tenantID)),
	}
	if want.Required && !slots.tenantSet {
		return openError(8, ErrOpenFailed, CodeTenantMismatch, "signed tid is required and absent", details)
	}
	if slots.tenantSet && want.Expected != "" && slots.tenantID != want.Expected {
		return openError(8, ErrOpenFailed, CodeTenantMismatch, "signed tid does not equal the expected tenant", details)
	}
	return nil
}

// openSubject is the inner half of rule 10: the JWE's protected header under G5 with the
// outer codes and a layer=jwe detail, iss == the outer kid, the encrypt family pin, a
// PRIVATE key for the Generation, then the decrypt itself.
func openSubject(compact, outerKid string, spec *Spec, keys bricksjose.KeyResolver) (plaintext []byte, encKid string, err error) {
	inner, peekErr := cryptoadapter.PeekProtectedHeader(compact)
	if peekErr != nil || strings.Count(compact, ".") != 4 {
		return nil, "", openError(10, ErrOpenFailed, CodePayloadUndecodable, "subject member is not a compact JWE", nil)
	}
	algAllowed := inner.Alg == string(keyAlg) && inner.Enc == string(enc)
	if policyErr := checkHeaderPolicy(10, &inner, algAllowed, layerJWE); policyErr != nil {
		return nil, "", policyErr
	}
	if iss, _ := inner.ExtraString(HeaderIssuer); iss != outerKid {
		return nil, "", openError(10, ErrOpenFailed, CodeAuthorshipMismatch, "inner iss does not equal the outer kid", layerDetails(layerJWE))
	}
	encFamily, _, ok := SplitGenerationKid(inner.Kid)
	if !ok || encFamily != spec.EncryptLogical {
		return nil, "", familyError(10, inner.Kid, spec.EncryptLogical, tagKeyEncrypt, layerJWE)
	}
	encKey, keyErr := keys.PrivateKey(inner.Kid)
	if keyErr != nil {
		return nil, "", unknownGenerationError(10, inner.Kid, tagKeyEncrypt, keyErr, layerJWE)
	}
	plaintext, _, err = cryptoadapter.Decrypt(compact, encKey, &cryptoadapter.DecryptOptions{
		ExpectedKid: inner.Kid, AllowedKeyAlgs: openKeyAlgs, AllowedContentEnc: openContents,
	})
	if err != nil {
		return nil, "", openError(10, ErrOpenFailed, CodeDecryptFailed, "subject does not decrypt under the wire encrypt kid", layerDetails(layerJWE))
	}
	return plaintext, inner.Kid, nil
}

// familyError is the sealer's checkFamily verdict (same sentinel, code and wording) at
// open time, with the rule and layer attached.
func familyError(rule int, kid, logical, role, layer string) error {
	var je *bricksjose.Error
	errors.As(checkFamily(kid, logical, role), &je)
	return &OpenError{Err: je, Rule: rule, Details: layerDetails(layer)}
}

func unknownGenerationError(rule int, kid, role string, cause error, layer string) error {
	err := openError(rule, ErrKidUnknownGeneration, CodeKidUnknownGeneration,
		fmt.Sprintf("%s kid generation is not provisioned on this consumer", role), layerDetails(layer))
	err.Err.Kid, err.Err.Cause = kid, cause
	return err
}

func openError(rule int, sentinel error, code, msg string, details map[string]string) *OpenError {
	return &OpenError{
		Err:     &bricksjose.Error{Sentinel: sentinel, Code: code, Message: msg},
		Rule:    rule,
		Details: details,
	}
}

// openCause is openError with a by-type cause: the two document-shape failures whose
// decoder error could otherwise quote a byte of the Subject plaintext.
func openCause(rule int, code, msg string, cause error) error {
	err := openError(rule, ErrOpenFailed, code, msg, nil)
	err.Err.Cause = cause
	return err
}

// layerDetails is the details map for an inner-JWE reuse of an outer code; nil for the outer layer.
func layerDetails(layer string) map[string]string {
	if layer == "" {
		return nil
	}
	return map[string]string{DetailLayer: layer}
}
