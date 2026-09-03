// envelope.go is the pure, liftable module of the #1308 prototype: tag scanner,
// keystore stand-in, Seal, Open, ledger stand-in and the error-code constants.
// It prints nothing and mutates no globals; scenarios.go drives it.
package main

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	jose "github.com/go-jose/go-jose/v4"
	"github.com/google/uuid"
)

// ---------------------------------------------------------------------------
// Wire constants (#1304)
// ---------------------------------------------------------------------------

const (
	TypSealedV1     = "vnd.gobricks.sealed.v1+json"
	CtyJSON         = "application/json"
	HeaderSealed    = "x-sealed" // unsigned ops header, never trusted by the opener
	HeaderSealedV1  = "v1"
	HeaderTenantID  = "x-tenant-id" // ADR-087 stamp, the carrier the shared-tenancy opener compares tid against
	HeaderOutboxEID = "x-outbox-event-id"
	TagName         = "seal"
)

// Allowlists mirror jose/algorithms.go.
var (
	allowedSigAlgs = []jose.SignatureAlgorithm{jose.PS256, jose.RS256}
	allowedKeyAlgs = []jose.KeyAlgorithm{jose.RSA_OAEP_256}
	allowedEncs    = []jose.ContentEncryption{jose.A256GCM}
)

// ---------------------------------------------------------------------------
// Kid grammar (#1306)
// ---------------------------------------------------------------------------

var (
	kidPattern        = regexp.MustCompile(`^[a-z0-9-]+$`)
	generationSuffix  = regexp.MustCompile(`-v[0-9]+$`)
	concretePattern   = regexp.MustCompile(`^([a-z0-9-]+)-v([0-9]+)$`)
	headerIDPattern   = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)
	dedupKeySeparator = ":" // deliberately OUTSIDE headerIDPattern: a header-sourced id can never spell a sealed dedup key
)

// ValidateLogicalKid enforces the logical-kid grammar: lowercase [a-z0-9-], and it
// must NOT end in -v<digits> so every concrete entry belongs to exactly one family.
func ValidateLogicalKid(kid string) error {
	if !kidPattern.MatchString(kid) {
		return fmt.Errorf("logical kid %q: must match %s", kid, kidPattern)
	}
	if generationSuffix.MatchString(kid) {
		return fmt.Errorf("logical kid %q: must not end in -v<digits> (that is a generation name)", kid)
	}
	return nil
}

// SplitConcrete returns (logical family, generation) for a concrete kid, or ok=false.
func SplitConcrete(kid string) (family, gen string, ok bool) {
	m := concretePattern.FindStringSubmatch(kid)
	if m == nil {
		return "", "", false
	}
	return m[1], "v" + m[2], true
}

// ---------------------------------------------------------------------------
// Tag scanner (#1305)
// ---------------------------------------------------------------------------

// SealSpec is the scanned declaration of a seal-tagged event type.
type SealSpec struct {
	SignLogical    string // two-kid identity, sign side
	EncryptLogical string // two-kid identity, encrypt side
	SubjectField   string // Go field name
	SubjectPath    string // json name of the subject == the sp manifest entry
}

// SealedPaths is the sp manifest (one path in v1).
func (s *SealSpec) SealedPaths() []string { return []string{s.SubjectPath} }

// ScanType scans T for the `seal` tag family.
func ScanType(t reflect.Type) (*SealSpec, error) {
	if t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct {
		return nil, fmt.Errorf("seal scan: %s is not a struct", t)
	}
	spec := &SealSpec{}
	sentinelSeen := false
	subjects := 0
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		tag, has := f.Tag.Lookup(TagName)
		if !has {
			continue
		}
		if tag == "subject" {
			subjects++
			if f.Anonymous {
				return nil, fmt.Errorf("seal scan: embedded field %s cannot be the subject", f.Name)
			}
			jsonName, skip := jsonNameOf(f)
			if skip {
				return nil, fmt.Errorf("seal scan: subject %s has json:\"-\" (no wire name to pin)", f.Name)
			}
			spec.SubjectField = f.Name
			spec.SubjectPath = jsonName
			continue
		}
		if sentinelSeen {
			return nil, fmt.Errorf("seal scan: sentinel declared twice (%s)", f.Name)
		}
		sentinelSeen = true
		if err := parseSentinel(spec, tag); err != nil {
			return nil, err
		}
	}
	switch {
	case !sentinelSeen && subjects > 0:
		return nil, errors.New("seal scan: subject declared without a sentinel `_ struct{} `seal:\"sign=…,encrypt=…\"``")
	case !sentinelSeen:
		return nil, errors.New("seal scan: type carries no seal tags")
	case subjects == 0:
		return nil, errors.New("seal scan: sentinel present but no field tagged seal:\"subject\"")
	case subjects > 1:
		return nil, fmt.Errorf("seal scan: %d subjects declared; v1 allows exactly one", subjects)
	}
	return spec, nil
}

func parseSentinel(spec *SealSpec, tag string) error {
	seen := map[string]bool{}
	for _, raw := range strings.Split(tag, ",") {
		kv := strings.SplitN(strings.TrimSpace(raw), "=", 2)
		if len(kv) != 2 || kv[1] == "" {
			return fmt.Errorf("seal scan: expected key=value, got %q", raw)
		}
		if seen[kv[0]] {
			return fmt.Errorf("seal scan: key %q given twice", kv[0])
		}
		seen[kv[0]] = true
		if err := ValidateLogicalKid(kv[1]); err != nil {
			return fmt.Errorf("seal scan: %w", err)
		}
		switch kv[0] {
		case "sign":
			spec.SignLogical = kv[1]
		case "encrypt":
			spec.EncryptLogical = kv[1]
		default:
			return fmt.Errorf("seal scan: unknown key %q", kv[0])
		}
	}
	if spec.SignLogical == "" || spec.EncryptLogical == "" {
		return errors.New("seal scan: sentinel needs both sign= and encrypt=")
	}
	return nil
}

func jsonNameOf(f reflect.StructField) (name string, skip bool) {
	tag := f.Tag.Get("json")
	if tag == "-" {
		return "", true
	}
	if name, _, _ = strings.Cut(tag, ","); name != "" {
		return name, false
	}
	return f.Name, false
}

// ---------------------------------------------------------------------------
// Keystore stand-in + activation (#1306)
// ---------------------------------------------------------------------------

// KeyEntry is one concrete generation entry: private and/or public material.
type KeyEntry struct {
	Private *rsa.PrivateKey
	Public  *rsa.PublicKey
}

// Keystore is name-addressed: entries are `<logical>-v<N>`. The accept set IS the keystore.
type Keystore struct {
	entries map[string]*KeyEntry
}

func NewKeystore() *Keystore { return &Keystore{entries: map[string]*KeyEntry{}} }

// GenerateKeyPair mints a fresh 2048-bit RSA pair (startup-time in the prototype).
func GenerateKeyPair() *rsa.PrivateKey {
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		panic(err)
	}
	return k
}

// ProvisionPrivate stores a private (and its public) under a concrete name.
func (ks *Keystore) ProvisionPrivate(name string, k *rsa.PrivateKey) *Keystore {
	ks.mustConcrete(name)
	ks.entries[name] = &KeyEntry{Private: k, Public: &k.PublicKey}
	return ks
}

// ProvisionPublic stores a public-only entry (the peer side).
func (ks *Keystore) ProvisionPublic(name string, pub *rsa.PublicKey) *Keystore {
	ks.mustConcrete(name)
	ks.entries[name] = &KeyEntry{Public: pub}
	return ks
}

// Remove drops an entry (accept set shrinks).
func (ks *Keystore) Remove(name string) *Keystore { delete(ks.entries, name); return ks }

func (ks *Keystore) mustConcrete(name string) {
	if _, _, ok := SplitConcrete(name); !ok {
		panic(fmt.Sprintf("keystore entry %q is not a concrete generation name (<logical>-v<N>)", name))
	}
}

func (ks *Keystore) Private(name string) (*rsa.PrivateKey, bool) {
	e, ok := ks.entries[name]
	if !ok || e.Private == nil {
		return nil, false
	}
	return e.Private, true
}

func (ks *Keystore) Public(name string) (*rsa.PublicKey, bool) {
	e, ok := ks.entries[name]
	if !ok || e.Public == nil {
		return nil, false
	}
	return e.Public, true
}

// Generations lists the provisioned concrete names of one logical family, sorted.
func (ks *Keystore) Generations(logical string) []string {
	var out []string
	for name := range ks.entries {
		if fam, _, ok := SplitConcrete(name); ok && fam == logical {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

// Describe renders the accept set without leaking material: name + roles held.
func (ks *Keystore) Describe() map[string]string {
	out := map[string]string{}
	for name, e := range ks.entries {
		roles := []string{}
		if e.Private != nil {
			roles = append(roles, "private")
		}
		if e.Public != nil {
			roles = append(roles, "public")
		}
		out[name] = strings.Join(roles, "+")
	}
	return out
}

// Activation is the producer's explicit selector: logical -> generation ("v2").
type Activation map[string]string

// ResolveActive picks the concrete generation a producer seals under.
// One provisioned generation auto-activates; several with no selector is a startup
// error; a selector naming an unprovisioned generation is a startup error.
func ResolveActive(ks *Keystore, logical string, act Activation) (string, error) {
	gens := ks.Generations(logical)
	if len(gens) == 0 {
		return "", fmt.Errorf("startup: no generation of %q provisioned", logical)
	}
	if want, ok := act[logical]; ok {
		name := logical + "-" + want
		for _, g := range gens {
			if g == name {
				return name, nil
			}
		}
		return "", fmt.Errorf("startup: activation names %s but it is not provisioned (have %v)", name, gens)
	}
	if len(gens) == 1 {
		return gens[0], nil
	}
	return "", fmt.Errorf("startup: %d generations of %q provisioned (%v) and no activation selector — refusing to guess", len(gens), logical, gens)
}

// ---------------------------------------------------------------------------
// Producer: Seal (#1304 encrypt-subset-then-sign-whole)
// ---------------------------------------------------------------------------

// Frame is the AMQP delivery stand-in: body + headers.
type Frame struct {
	Body    []byte
	Headers map[string]string
}

// Producer is the typed-door stand-in for one event type.
type Producer struct {
	Keystore   *Keystore
	Activation Activation
	EventType  string
	Tenant     string // ADR-087 stamp; "" when none resolves
	Now        func() time.Time
}

// SealTrace captures every intermediate the walkthrough wants to show.
type SealTrace struct {
	PlaintextDoc []byte
	SealedDoc    []byte // the exact signed bytes
	JWE          string
	JWEHeader    map[string]any
	JWSHeader    map[string]any
	SignKid      string
	EncKid       string
}

// Startup resolves the producer's two roles (sign→private, encrypt→public) and the
// active generations — the ResolvePolicy-parity fail-fast.
func (p *Producer) Startup(spec *SealSpec) (signKid, encKid string, err error) {
	if signKid, err = ResolveActive(p.Keystore, spec.SignLogical, p.Activation); err != nil {
		return "", "", err
	}
	if _, ok := p.Keystore.Private(signKid); !ok {
		return "", "", fmt.Errorf("startup: %s holds no private key (producer signs)", signKid)
	}
	if encKid, err = ResolveActive(p.Keystore, spec.EncryptLogical, p.Activation); err != nil {
		return "", "", err
	}
	if _, ok := p.Keystore.Public(encKid); !ok {
		return "", "", fmt.Errorf("startup: %s holds no public key (producer encrypts)", encKid)
	}
	return signKid, encKid, nil
}

// Seal runs once per event; a redelivery is the same bytes.
func (p *Producer) Seal(evt any) (Frame, *SealTrace, error) {
	spec, err := ScanType(reflect.TypeOf(evt))
	if err != nil {
		return Frame{}, nil, err
	}
	signKid, encKid, err := p.Startup(spec)
	if err != nil {
		return Frame{}, nil, err
	}
	signPriv, _ := p.Keystore.Private(signKid)
	encPub, _ := p.Keystore.Public(encKid)

	tr := &SealTrace{SignKid: signKid, EncKid: encKid}
	tr.PlaintextDoc, err = json.Marshal(evt)
	if err != nil {
		return Frame{}, nil, err
	}
	var doc map[string]json.RawMessage
	if err = json.Unmarshal(tr.PlaintextDoc, &doc); err != nil {
		return Frame{}, nil, err
	}
	subject, ok := doc[spec.SubjectPath]
	if !ok {
		subject = json.RawMessage("null") // always seal, nil included
	}

	// Inner JWE over the subject subtree. iss binds authorship to the outer kid.
	tr.JWEHeader = map[string]any{"alg": string(jose.RSA_OAEP_256), "enc": string(jose.A256GCM), "kid": encKid, "cty": CtyJSON, "iss": signKid}
	encOpts := (&jose.EncrypterOptions{}).WithContentType(CtyJSON).WithHeader("iss", signKid)
	enc, err := jose.NewEncrypter(jose.A256GCM, jose.Recipient{Algorithm: jose.RSA_OAEP_256, Key: encPub, KeyID: encKid}, encOpts)
	if err != nil {
		return Frame{}, nil, err
	}
	jweObj, err := enc.Encrypt(subject)
	if err != nil {
		return Frame{}, nil, err
	}
	if tr.JWE, err = jweObj.CompactSerialize(); err != nil {
		return Frame{}, nil, err
	}

	// Replace the subject in place; serialize ONCE; sign those exact bytes.
	doc[spec.SubjectPath], _ = json.Marshal(tr.JWE)
	if tr.SealedDoc, err = json.Marshal(doc); err != nil {
		return Frame{}, nil, err
	}

	now := time.Now()
	if p.Now != nil {
		now = p.Now()
	}
	tr.JWSHeader = map[string]any{
		"alg": string(jose.PS256), "typ": TypSealedV1, "cty": CtyJSON, "kid": signKid,
		"sp": spec.SealedPaths(), "jti": uuid.NewString(), "iat": now.Unix(), "etyp": p.EventType,
	}
	if p.Tenant != "" {
		tr.JWSHeader["tid"] = p.Tenant
	}
	sigOpts := (&jose.SignerOptions{}).WithType(TypSealedV1).WithContentType(CtyJSON)
	for _, k := range []string{"kid", "sp", "jti", "iat", "etyp", "tid"} {
		if v, ok := tr.JWSHeader[k]; ok {
			sigOpts = sigOpts.WithHeader(jose.HeaderKey(k), v)
		}
	}
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.PS256, Key: signPriv}, sigOpts)
	if err != nil {
		return Frame{}, nil, err
	}
	jwsObj, err := signer.Sign(tr.SealedDoc)
	if err != nil {
		return Frame{}, nil, err
	}
	compact, err := jwsObj.CompactSerialize()
	if err != nil {
		return Frame{}, nil, err
	}

	headers := map[string]string{HeaderSealed: HeaderSealedV1}
	if p.Tenant != "" {
		headers[HeaderTenantID] = p.Tenant
	}
	return Frame{Body: []byte(compact), Headers: headers}, tr, nil
}

// ---------------------------------------------------------------------------
// Consumer: Open (#1304-#1307 opener rule set, in order)
// ---------------------------------------------------------------------------

// Code is an opener error code; each rule owns a distinct one.
type Code string

const (
	CodeNotSealed                Code = "NOT_SEALED"
	CodeAlgNotAllowed            Code = "SEAL_ALG_NOT_ALLOWED"
	CodeKidFamilyMismatch        Code = "SEAL_KID_FAMILY_MISMATCH"
	CodeKidUnknownGen            Code = "SEAL_KID_UNKNOWN_GENERATION"
	CodeSignatureInvalid         Code = "SEAL_SIGNATURE_INVALID"
	CodeEventTypeMismatch        Code = "SEAL_EVENT_TYPE_MISMATCH"
	CodeTenantMismatch           Code = "SEAL_TENANT_MISMATCH"
	CodeManifestMismatch         Code = "SEAL_MANIFEST_MISMATCH"
	CodeAuthorshipMismatch       Code = "SEAL_AUTHORSHIP_MISMATCH"
	CodeDecryptFailed            Code = "SEAL_DECRYPT_FAILED"
	CodePayloadUndecodable       Code = "SEAL_PAYLOAD_UNDECODABLE"
	CodeHeaderIDInvalid          Code = "HEADER_ID_INVALID"
	VerdictProcessed                  = "processed"
	VerdictDuplicate                  = "duplicate — skipped"
	DispositionPoison                 = "POISON (nack, no requeue → DLQ)"
	DispositionRecoverable            = "RECOVERABLE (provisioning gap; same DLQ path, distinct code)"
	DispositionPlaintextAccepted      = "PLAINTEXT ACCEPTED under Accept-unsealed (WARN)"
)

// OpenError names the rule that fired and its code.
type OpenError struct {
	Rule        int
	Code        Code
	Detail      string
	Disposition string
}

func (e *OpenError) Error() string {
	return fmt.Sprintf("rule %d → %s: %s [%s]", e.Rule, e.Code, e.Detail, e.Disposition)
}

func poison(rule int, code Code, detail string) *OpenError {
	return &OpenError{Rule: rule, Code: code, Detail: detail, Disposition: DispositionPoison}
}

// Tenancy of the consuming deployment.
type Tenancy int

const (
	TenancySingle Tenancy = iota
	TenancyShared
	TenancyPerTenant
)

func (t Tenancy) String() string {
	return [...]string{"single-tenant", "shared", "per-tenant"}[t]
}

// Consumer is the typed-door stand-in for one declared event type.
type Consumer struct {
	Name           string
	Keystore       *Keystore
	EventType      string
	Tenancy        Tenancy
	ContextTenant  string // per-tenant tenancy: the tenant the delivery pipeline resolved
	AcceptUnsealed bool
	// DisableFamilyPin is a PROTOTYPE-ONLY debug knob for the S4 defense-in-depth step.
	DisableFamilyPin bool
}

// SealedEnvelope is what the WithMeta door exposes after a successful open.
type SealedEnvelope struct {
	JTI     string `json:"jti"`
	IAT     int64  `json:"iat"`
	ETyp    string `json:"etyp"`
	TID     string `json:"tid,omitempty"`
	SignKid string `json:"signKid"`
	EncKid  string `json:"encKid"`
}

// DedupKey composes `<logical sign family>:<jti>`. The ":" separator sits OUTSIDE the
// header-id grammar ^[A-Za-z0-9_-]{1,128}$, so no header-sourced id on an unsealed
// sibling queue can ever spell this key and pre-insert it into a shared ledger.
func (e SealedEnvelope) DedupKey() string {
	fam, _, _ := SplitConcrete(e.SignKid)
	return fam + dedupKeySeparator + e.JTI
}

// OpenTrace captures the intermediates of one open.
type OpenTrace struct {
	JWSHeader map[string]any
	SealedDoc []byte
	JWEHeader map[string]any
	OpenedDoc []byte
	Unsealed  bool
	Warn      string
}

// Open applies the rule set in order; the first failing rule wins.
func (c *Consumer) Open(frame Frame, out any) (*SealedEnvelope, *OpenTrace, *OpenError) {
	spec, err := ScanType(reflect.TypeOf(out))
	if err != nil {
		return nil, nil, poison(0, CodePayloadUndecodable, err.Error())
	}
	tr := &OpenTrace{}
	body := string(frame.Body)

	// Rule 1 — structural sniff.
	hdr, shaped := sniffJWS(body)
	if !shaped {
		if c.AcceptUnsealed {
			tr.Unsealed = true
			tr.Warn = "WARN accept-unsealed: body is not JWS-shaped; opened as plaintext — authenticity OFF for this consumer"
			if err := json.Unmarshal(frame.Body, out); err != nil {
				return nil, tr, poison(1, CodePayloadUndecodable, err.Error())
			}
			tr.OpenedDoc = frame.Body
			return nil, tr, nil
		}
		return nil, tr, poison(1, CodeNotSealed, "body is not a compact JWS")
	}
	tr.JWSHeader = hdr
	if typ, _ := hdr["typ"].(string); typ != TypSealedV1 {
		return nil, tr, poison(1, CodeNotSealed, fmt.Sprintf("typ %q is not %q (JWS-shaped, so poison — never plaintext fallback)", typ, TypSealedV1))
	}

	// Rule 2 — outer alg allowlist.
	alg, _ := hdr["alg"].(string)
	if !containsSig(alg) {
		return nil, tr, poison(2, CodeAlgNotAllowed, "outer alg "+alg)
	}

	// Rule 3 — family pin by grammar.
	kid, _ := hdr["kid"].(string)
	fam, _, ok := SplitConcrete(kid)
	if !c.DisableFamilyPin && (!ok || fam != spec.SignLogical) {
		return nil, tr, poison(3, CodeKidFamilyMismatch, fmt.Sprintf("wire kid %q is not a generation of declared sign family %q", kid, spec.SignLogical))
	}

	// Rule 4 — generation must resolve to a PUBLIC key locally.
	pub, ok := c.Keystore.Public(kid)
	if !ok {
		return nil, tr, &OpenError{Rule: 4, Code: CodeKidUnknownGen, Disposition: DispositionRecoverable,
			Detail: fmt.Sprintf("generation %q not provisioned in consumer keystore (accept set: %v)", kid, c.Keystore.Generations(spec.SignLogical))}
	}

	// Rule 5 — signature.
	jws, err := jose.ParseSigned(body, allowedSigAlgs)
	if err != nil {
		return nil, tr, poison(5, CodeSignatureInvalid, "parse: "+err.Error())
	}
	sealedDoc, err := jws.Verify(pub)
	if err != nil {
		return nil, tr, poison(5, CodeSignatureInvalid, "signature does not verify under "+kid)
	}
	tr.SealedDoc = sealedDoc

	// Rule 6 — etyp pin.
	if etyp, _ := hdr["etyp"].(string); etyp != c.EventType {
		return nil, tr, poison(6, CodeEventTypeMismatch, fmt.Sprintf("etyp present (len %d) ≠ declared EventType %q", len(etyp), c.EventType))
	}

	// Rule 7 — tid.
	tid, _ := hdr["tid"].(string)
	switch c.Tenancy {
	case TenancyShared:
		if carrier := frame.Headers[HeaderTenantID]; tid != carrier {
			return nil, tr, poison(7, CodeTenantMismatch, fmt.Sprintf("signed tid %q ≠ %s carrier %q", tid, HeaderTenantID, carrier))
		}
	case TenancyPerTenant:
		if tid != "" && tid != c.ContextTenant {
			return nil, tr, poison(7, CodeTenantMismatch, fmt.Sprintf("signed tid %q ≠ context tenant %q", tid, c.ContextTenant))
		}
	case TenancySingle:
		// not specified by #1306/#1307 — see DX findings; prototype ignores tid here
	}

	// Rule 8 — sp manifest.
	if !equalStrings(anySlice(hdr["sp"]), spec.SealedPaths()) {
		return nil, tr, poison(8, CodeManifestMismatch, fmt.Sprintf("sp %v ≠ declared %v", hdr["sp"], spec.SealedPaths()))
	}

	// Rule 9 — payload doc, inner JWE checks, decrypt.
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(sealedDoc, &doc); err != nil {
		return nil, tr, poison(9, CodePayloadUndecodable, "payload doc: "+err.Error())
	}
	var compact string
	if err := json.Unmarshal(doc[spec.SubjectPath], &compact); err != nil {
		return nil, tr, poison(9, CodePayloadUndecodable, "subject value is not a compact JWE string")
	}
	jweHdr, ok := decodeSegmentHeader(compact, 5)
	if !ok {
		return nil, tr, poison(9, CodePayloadUndecodable, "subject value is not a compact JWE")
	}
	tr.JWEHeader = jweHdr
	if a, _ := jweHdr["alg"].(string); a != string(jose.RSA_OAEP_256) {
		return nil, tr, poison(9, CodeAlgNotAllowed, "inner alg "+a)
	}
	if e, _ := jweHdr["enc"].(string); e != string(jose.A256GCM) {
		return nil, tr, poison(9, CodeAlgNotAllowed, "inner enc "+e)
	}
	iss, _ := jweHdr["iss"].(string)
	if iss != kid {
		return nil, tr, poison(9, CodeAuthorshipMismatch, fmt.Sprintf("inner iss %q ≠ outer kid %q (strip-and-re-sign)", iss, kid))
	}
	encKid, _ := jweHdr["kid"].(string)
	encFam, _, ok := SplitConcrete(encKid)
	if !ok || encFam != spec.EncryptLogical {
		return nil, tr, poison(9, CodeKidFamilyMismatch, fmt.Sprintf("inner kid %q is not a generation of declared encrypt family %q", encKid, spec.EncryptLogical))
	}
	priv, ok := c.Keystore.Private(encKid)
	if !ok {
		return nil, tr, &OpenError{Rule: 9, Code: CodeKidUnknownGen, Disposition: DispositionRecoverable,
			Detail: fmt.Sprintf("inner kid %q not provisioned as PRIVATE in consumer keystore", encKid)}
	}
	jwe, err := jose.ParseEncrypted(compact, allowedKeyAlgs, allowedEncs)
	if err != nil {
		return nil, tr, poison(9, CodePayloadUndecodable, "JWE parse: "+err.Error())
	}
	plaintext, err := jwe.Decrypt(priv)
	if err != nil {
		return nil, tr, poison(9, CodeDecryptFailed, "sub-check decrypt: "+encKid+" private does not open this JWE")
	}

	// Rule 10 — splice and decode.
	doc[spec.SubjectPath] = plaintext
	opened, err := json.Marshal(doc)
	if err != nil {
		return nil, tr, poison(10, CodePayloadUndecodable, err.Error())
	}
	tr.OpenedDoc = opened
	if err := json.Unmarshal(opened, out); err != nil {
		return nil, tr, poison(10, CodePayloadUndecodable, err.Error())
	}

	// Rule 11 — envelope.
	env := &SealedEnvelope{JTI: str(hdr["jti"]), IAT: num(hdr["iat"]), ETyp: str(hdr["etyp"]), TID: tid, SignKid: kid, EncKid: encKid}
	if !headerIDPattern.MatchString(env.JTI) {
		return nil, tr, poison(11, CodeHeaderIDInvalid, "jti fails identifier grammar")
	}
	return env, tr, nil
}

// sniffJWS: 3 dot-separated base64url segments with a JSON protected header.
func sniffJWS(body string) (map[string]any, bool) {
	return decodeSegmentHeader(body, 3)
}

func decodeSegmentHeader(compact string, segments int) (map[string]any, bool) {
	parts := strings.Split(compact, ".")
	if len(parts) != segments {
		return nil, false
	}
	for _, p := range parts {
		if _, err := base64.RawURLEncoding.DecodeString(p); err != nil {
			return nil, false
		}
	}
	raw, _ := base64.RawURLEncoding.DecodeString(parts[0])
	var hdr map[string]any
	if err := json.Unmarshal(raw, &hdr); err != nil {
		return nil, false
	}
	return hdr, true
}

func containsSig(alg string) bool {
	for _, a := range allowedSigAlgs {
		if string(a) == alg {
			return true
		}
	}
	return false
}

func anySlice(v any) []string {
	arr, _ := v.([]any)
	out := make([]string, 0, len(arr))
	for _, e := range arr {
		out = append(out, str(e))
	}
	return out
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func str(v any) string { s, _ := v.(string); return s }

func num(v any) int64 {
	switch n := v.(type) {
	case float64:
		return int64(n)
	case json.Number:
		i, _ := n.Int64()
		return i
	case int64:
		return n
	}
	return 0
}

// ---------------------------------------------------------------------------
// Ledger stand-in (#1307)
// ---------------------------------------------------------------------------

// Ledger is an in-memory inbox stand-in keyed by dedup key.
type Ledger struct {
	Name      string
	seen      map[string]bool
	DedupHits int
}

func NewLedger(name string) *Ledger { return &Ledger{Name: name, seen: map[string]bool{}} }

// ProcessOnce records the key; the second arrival is a duplicate.
func (l *Ledger) ProcessOnce(key string) string {
	if l.seen[key] {
		l.DedupHits++
		return VerdictDuplicate
	}
	l.seen[key] = true
	return VerdictProcessed
}

// ProcessOnceHeaderID is the UNSEALED path: the id comes from a header and must pass the
// header-id grammar before it can touch the ledger.
func (l *Ledger) ProcessOnceHeaderID(id string) (string, error) {
	if !headerIDPattern.MatchString(id) {
		return "", fmt.Errorf("%s: header id %q rejected by grammar %s", CodeHeaderIDInvalid, id, headerIDPattern)
	}
	return l.ProcessOnce(id), nil
}

// ---------------------------------------------------------------------------
// Attack helpers (used by scenarios; kept here because they manipulate the wire form)
// ---------------------------------------------------------------------------

// TamperPayload rewrites the JWS payload segment through fn, keeping header+signature.
func TamperPayload(body []byte, fn func(doc map[string]json.RawMessage)) []byte {
	parts := strings.Split(string(body), ".")
	raw, _ := base64.RawURLEncoding.DecodeString(parts[1])
	var doc map[string]json.RawMessage
	_ = json.Unmarshal(raw, &doc)
	fn(doc)
	edited, _ := json.Marshal(doc)
	parts[1] = base64.RawURLEncoding.EncodeToString(edited)
	return []byte(strings.Join(parts, "."))
}

// ResignPayload keeps the verified/extracted payload doc (with the original inner JWE)
// and signs it under another key: the strip-and-re-sign attack.
func ResignPayload(sealedDoc []byte, origHeader map[string]any, kid string, priv *rsa.PrivateKey) []byte {
	return ResignPayloadWithTyp(sealedDoc, origHeader, TypSealedV1, kid, priv)
}

// ResignPayloadWithTyp is ResignPayload with an arbitrary typ (S7's wrong-typ probe).
func ResignPayloadWithTyp(sealedDoc []byte, origHeader map[string]any, typ, kid string, priv *rsa.PrivateKey) []byte {
	opts := (&jose.SignerOptions{}).WithType(jose.ContentType(typ)).WithContentType(CtyJSON)
	for k, v := range origHeader {
		switch k {
		case "alg", "typ", "cty", "kid":
			continue
		}
		opts = opts.WithHeader(jose.HeaderKey(k), v)
	}
	opts = opts.WithHeader("kid", kid)
	signer, _ := jose.NewSigner(jose.SigningKey{Algorithm: jose.PS256, Key: priv}, opts)
	obj, _ := signer.Sign(sealedDoc)
	s, _ := obj.CompactSerialize()
	return []byte(s)
}

// JWSPayloadDoc returns the (unverified) payload segment of a compact JWS, for display.
func JWSPayloadDoc(body []byte) []byte {
	parts := strings.Split(string(body), ".")
	if len(parts) != 3 {
		return nil
	}
	raw, _ := base64.RawURLEncoding.DecodeString(parts[1])
	return raw
}

// Sizes reports plaintext vs wire vs JWE-only byte counts.
type Sizes struct {
	Plaintext, Wire, JWEOnly int
	Ratio                    string
}

func MeasureSizes(tr *SealTrace, frame Frame) Sizes {
	return Sizes{
		Plaintext: len(tr.PlaintextDoc), Wire: len(frame.Body), JWEOnly: len(tr.JWE),
		Ratio: strconv.FormatFloat(float64(len(frame.Body))/float64(len(tr.PlaintextDoc)), 'f', 2, 64) + "x",
	}
}
