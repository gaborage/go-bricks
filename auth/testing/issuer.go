// Package testing provides an in-memory fake JWT issuer for exercising the auth
// package's bearer/JWT verifier without a real identity provider.
//
// The primary type is Issuer, which holds one or more RSA key pairs and mints
// compact JWS credentials — valid ones plus every rejection shape a verifier must
// refuse (expired, wrong audience, wrong issuer, unknown kid, alg=none, ES256, …).
// Feed Issuer.PublicKeys() to a static key resolver and the verifier will accept
// exactly what this issuer signs.
//
// Minting never returns an error: a fake that forces error plumbing at every call
// site reads worse than one that panics on the impossible. Key generation failure
// and a payload that cannot be JSON-encoded (a channel in Claims.Extra, say) panic
// with a clear message; nothing else can fail.
//
// Example usage:
//
//	iss := NewIssuer().WithIssuerURL("https://idp.test/").WithAudience("payments-api")
//	token := iss.Mint(Claims{Subject: "user-42", Extra: map[string]any{"scope": "read"}})
//	expired := iss.MintExpired()
package testing

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	jose "github.com/go-jose/go-jose/v4"
)

// Defaults applied by NewIssuer when a builder does not override them.
const (
	DefaultIssuerURL = "https://issuer.test/"
	DefaultAudience  = "go-bricks-test"
	DefaultKeyID     = "test-key-1"
	DefaultSubject   = "test-subject"
	DefaultLifetime  = time.Hour
)

// Values used by the negative-case minting helpers. They are exported so a test can
// assert that a rejection names the offending value.
const (
	UnknownKeyID  = "unknown-kid"
	WrongAudience = "wrong-audience"
	WrongIssuer   = "https://attacker.test/"
)

// rsaKeyBits is the modulus size of every RSA key the issuer generates.
const rsaKeyBits = 2048

// Algorithm names the JWS signature algorithm written to the protected header.
type Algorithm string

// Algorithms the issuer can mint. AlgNone and AlgES256 exist only to prove a
// verifier refuses them: the framework's allowlist is {RS256, PS256}.
const (
	AlgRS256 Algorithm = "RS256"
	AlgPS256 Algorithm = "PS256"
	AlgES256 Algorithm = "ES256"
	AlgNone  Algorithm = "none"
)

// Claims describes the JWT payload to mint. Zero fields take issuer defaults:
// Subject becomes DefaultSubject, Issuer the issuer URL, Audience the default
// audience, IssuedAt the current time, and ExpiresAt one DefaultLifetime ahead.
// NotBefore is omitted when zero.
//
// A single-element Audience is encoded as a JSON string and a multi-element one as
// an array, matching what real issuers emit.
type Claims struct {
	Subject    string
	Issuer     string
	Audience   []string
	IssuedAt   time.Time
	NotBefore  time.Time
	ExpiresAt  time.Time
	OmitExpiry bool
	// Extra is merged into the payload before the standard claims, so a standard
	// claim always wins over a same-named extra.
	Extra map[string]any
}

// MintOptions is the flexible minting entry point's input: the payload plus the
// header and signing choices.
type MintOptions struct {
	Claims
	// Algorithm defaults to AlgRS256.
	Algorithm Algorithm
	// KeyID overrides the header kid; empty means the active signing kid.
	KeyID string
	// OmitKeyID drops the kid header entirely. It wins over KeyID.
	OmitKeyID bool
	// Type overrides the typ header, which defaults to "JWT".
	Type string
	// SignKey signs with a caller-supplied key instead of the issuer's — an
	// *rsa.PrivateKey or an *ecdsa.PrivateKey. Used to mint a well-formed
	// credential the verifier cannot validate.
	SignKey any
}

// Issuer is an in-memory JWT issuer holding RSA key pairs under stable key IDs.
// It is not safe for concurrent rotation, but minting from several goroutines is
// fine once configuration has settled.
type Issuer struct {
	issuerURL string
	audience  string
	keys      map[string]*rsa.PrivateKey
	activeKID string
	// foreign is a key pair that is never exposed through PublicKeys, so anything
	// signed with it is a valid-looking credential the verifier must reject. Only a
	// handful of helpers need it, so it is generated on first use — an RSA-2048
	// keygen costs tens of milliseconds and most issuers never mint these shapes.
	foreign     *rsa.PrivateKey
	foreignOnce sync.Once
	// ecKey backs the ES256 rejection shape and is likewise generated on first use.
	ecKey     *ecdsa.PrivateKey
	ecKeyOnce sync.Once
	now       func() time.Time
}

// NewIssuer returns an issuer with one RSA-2048 key pair under DefaultKeyID.
// The unexposed key pair behind MintUnknownKeyID/MintBadSignature and the ECDSA
// key behind MintES256 are generated lazily on first use. It panics if key
// generation fails.
func NewIssuer() *Issuer {
	return &Issuer{
		issuerURL: DefaultIssuerURL,
		audience:  DefaultAudience,
		keys:      map[string]*rsa.PrivateKey{DefaultKeyID: generateRSAKey()},
		activeKID: DefaultKeyID,
		now:       time.Now,
	}
}

// foreignKey returns the key pair PublicKeys never exposes, generating it once.
func (i *Issuer) foreignKey() *rsa.PrivateKey {
	i.foreignOnce.Do(func() { i.foreign = generateRSAKey() })
	return i.foreign
}

// ecdsaKey returns the P-256 key used for ES256 credentials, generating it once.
func (i *Issuer) ecdsaKey() *ecdsa.PrivateKey {
	i.ecKeyOnce.Do(func() {
		key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			panic(fmt.Sprintf("auth/testing: generate ECDSA key: %v", err))
		}
		i.ecKey = key
	})
	return i.ecKey
}

func generateRSAKey() *rsa.PrivateKey {
	key, err := rsa.GenerateKey(rand.Reader, rsaKeyBits)
	if err != nil {
		panic(fmt.Sprintf("auth/testing: generate RSA key: %v", err))
	}
	return key
}

// WithIssuerURL sets the `iss` claim minted by default.
func (i *Issuer) WithIssuerURL(url string) *Issuer {
	i.issuerURL = url
	return i
}

// WithAudience sets the `aud` claim minted by default.
func (i *Issuer) WithAudience(audience string) *Issuer {
	i.audience = audience
	return i
}

// WithClock replaces the time source used for the default iat/exp claims, making
// minted lifetimes deterministic.
func (i *Issuer) WithClock(now func() time.Time) *Issuer {
	i.now = now
	return i
}

// Rotate adds a fresh RSA key pair under kid and makes it the active signing key.
// Previously issued credentials stay verifiable because the old public key remains
// in PublicKeys.
//
// An already-registered kid panics: overwriting a key would silently break every
// credential minted under it, which is the opposite of what this helper promises
// and would make a rotation test assert the wrong thing.
func (i *Issuer) Rotate(kid string) *Issuer {
	if _, exists := i.keys[kid]; exists {
		panic(fmt.Sprintf("auth/testing: kid %q is already registered; Rotate must not overwrite a key credentials were minted under", kid))
	}
	i.keys[kid] = generateRSAKey()
	i.activeKID = kid
	return i
}

// IssuerURL returns the configured `iss` value.
func (i *Issuer) IssuerURL() string { return i.issuerURL }

// Audience returns the configured default `aud` value.
func (i *Issuer) Audience() string { return i.audience }

// ActiveKeyID returns the kid credentials are currently signed under.
func (i *Issuer) ActiveKeyID() string { return i.activeKID }

// PublicKeys returns a copy of every public key the issuer will sign with, keyed by
// kid — ready to hand to a static key resolver.
func (i *Issuer) PublicKeys() map[string]*rsa.PublicKey {
	out := make(map[string]*rsa.PublicKey, len(i.keys))
	for kid, key := range i.keys {
		out[kid] = &key.PublicKey
	}
	return out
}

// PublicKey returns the public key registered under kid, or nil.
func (i *Issuer) PublicKey(kid string) *rsa.PublicKey {
	key, ok := i.keys[kid]
	if !ok {
		return nil
	}
	return &key.PublicKey
}

// Mint returns a compact JWS over claims, signed RS256 with the active key.
//
//nolint:gocritic // hugeParam: by-value keeps the call site a literal — iss.Mint(Claims{...})
func (i *Issuer) Mint(claims Claims) string {
	return i.MintWith(MintOptions{Claims: claims})
}

// MintWith is the flexible entry point every other minting helper delegates to.
//
//nolint:gocritic // hugeParam: by-value keeps the call site a literal — iss.MintWith(MintOptions{...})
func (i *Issuer) MintWith(opts MintOptions) string {
	payload := i.marshalPayload(&opts.Claims)
	header := i.buildHeader(&opts)

	if opts.Algorithm == AlgNone {
		return encodeSegment(mustMarshal(header)) + "." + encodeSegment(payload) + "."
	}
	return sign(payload, header, i.signingKey(&opts))
}

func (i *Issuer) buildHeader(opts *MintOptions) map[string]any {
	alg := opts.Algorithm
	if alg == "" {
		alg = AlgRS256
	}
	typ := opts.Type
	if typ == "" {
		typ = "JWT"
	}
	header := map[string]any{"alg": string(alg), "typ": typ}
	if !opts.OmitKeyID {
		kid := opts.KeyID
		if kid == "" {
			kid = i.activeKID
		}
		header["kid"] = kid
	}
	return header
}

func (i *Issuer) signingKey(opts *MintOptions) any {
	if opts.SignKey != nil {
		return opts.SignKey
	}
	if opts.Algorithm == AlgES256 {
		return i.ecdsaKey()
	}
	kid := opts.KeyID
	if kid == "" {
		kid = i.activeKID
	}
	key, ok := i.keys[kid]
	if !ok {
		panic(fmt.Sprintf("auth/testing: no signing key for kid %q; pass MintOptions.SignKey", kid))
	}
	return key
}

func (i *Issuer) marshalPayload(claims *Claims) []byte {
	payload := make(map[string]any)
	for k, v := range claims.Extra {
		payload[k] = v
	}

	now := i.now()
	payload["sub"] = orDefault(claims.Subject, DefaultSubject)
	payload["iss"] = orDefault(claims.Issuer, i.issuerURL)
	payload["aud"] = encodeAudience(claims.Audience, i.audience)

	iat := claims.IssuedAt
	if iat.IsZero() {
		iat = now
	}
	payload["iat"] = iat.Unix()

	if !claims.NotBefore.IsZero() {
		payload["nbf"] = claims.NotBefore.Unix()
	}

	if !claims.OmitExpiry {
		exp := claims.ExpiresAt
		if exp.IsZero() {
			exp = now.Add(DefaultLifetime)
		}
		payload["exp"] = exp.Unix()
	}

	return mustMarshal(payload)
}

func encodeAudience(audience []string, fallback string) any {
	switch len(audience) {
	case 0:
		return fallback
	case 1:
		return audience[0]
	default:
		return audience
	}
}

func orDefault(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

// sign produces a compact JWS carrying header verbatim as the protected header.
func sign(payload []byte, header map[string]any, key any) string {
	alg, _ := header["alg"].(string)
	signerOpts := &jose.SignerOptions{}
	for k, v := range header {
		if k == "alg" {
			continue
		}
		signerOpts = signerOpts.WithHeader(jose.HeaderKey(k), v)
	}
	signer, err := jose.NewSigner(jose.SigningKey{
		Algorithm: jose.SignatureAlgorithm(alg),
		Key:       key,
	}, signerOpts)
	if err != nil {
		panic(fmt.Sprintf("auth/testing: create %s signer: %v", alg, err))
	}
	obj, err := signer.Sign(payload)
	if err != nil {
		panic(fmt.Sprintf("auth/testing: sign: %v", err))
	}
	compact, err := obj.CompactSerialize()
	if err != nil {
		panic(fmt.Sprintf("auth/testing: serialize: %v", err))
	}
	return compact
}

func mustMarshal(v any) []byte {
	data, err := json.Marshal(v)
	if err != nil {
		panic(fmt.Sprintf("auth/testing: marshal: %v", err))
	}
	return data
}

func encodeSegment(raw []byte) string {
	return base64.RawURLEncoding.EncodeToString(raw)
}

// MintPS256 returns a valid credential signed with PS256 instead of RS256.
func (i *Issuer) MintPS256() string {
	return i.MintWith(MintOptions{Algorithm: AlgPS256})
}

// MintExpired returns a credential that expired half an hour ago, with an `iat`
// two hours in the past — MintExpiredWithin(time.Hour). The verifier caps leeway
// at five minutes, so half an hour is always past it.
func (i *Issuer) MintExpired() string {
	return i.MintExpiredWithin(time.Hour)
}

// MintExpiredWithin returns a credential that expired half of leeway ago, so a
// verifier configured with that leeway still accepts it and one without rejects it.
// Its `iat` is two leeways in the past, so the issued-in-future rule cannot fire first.
func (i *Issuer) MintExpiredWithin(leeway time.Duration) string {
	now := i.now()
	return i.MintWith(MintOptions{Claims: Claims{
		IssuedAt:  now.Add(-2 * leeway),
		ExpiresAt: now.Add(-leeway / 2),
	}})
}

// MintWrongAudience returns a credential addressed to WrongAudience.
func (i *Issuer) MintWrongAudience() string {
	return i.MintWith(MintOptions{Claims: Claims{Audience: []string{WrongAudience}}})
}

// MintWrongIssuer returns a credential whose `iss` is WrongIssuer.
func (i *Issuer) MintWrongIssuer() string {
	return i.MintWith(MintOptions{Claims: Claims{Issuer: WrongIssuer}})
}

// MintUnknownKeyID returns a credential whose kid header (UnknownKeyID) names a key
// the verifier will not have; it is signed with an unexposed key pair.
func (i *Issuer) MintUnknownKeyID() string {
	return i.MintWith(MintOptions{KeyID: UnknownKeyID, SignKey: i.foreignKey()})
}

// MintMissingKeyID returns a credential with no kid header.
func (i *Issuer) MintMissingKeyID() string {
	return i.MintWith(MintOptions{OmitKeyID: true})
}

// MintMissingExpiry returns a credential with no `exp` claim.
func (i *Issuer) MintMissingExpiry() string {
	return i.MintWith(MintOptions{Claims: Claims{OmitExpiry: true}})
}

// MintFutureNotBefore returns a credential whose `nbf` is an hour in the future.
// Its `exp` takes the default one DefaultLifetime ahead, so the window never
// opens — the helper exists to exercise the not-yet-valid rule, not to become
// valid later.
func (i *Issuer) MintFutureNotBefore() string {
	return i.MintWith(MintOptions{Claims: Claims{NotBefore: i.now().Add(time.Hour)}})
}

// MintFutureIssuedAt returns a credential whose `iat` is an hour in the future.
func (i *Issuer) MintFutureIssuedAt() string {
	return i.MintWith(MintOptions{Claims: Claims{IssuedAt: i.now().Add(time.Hour)}})
}

// MintAlgNone returns an unsigned credential with alg=none and an empty signature
// segment, hand-assembled because go-jose refuses to produce one.
func (i *Issuer) MintAlgNone() string {
	return i.MintWith(MintOptions{Algorithm: AlgNone})
}

// MintES256 returns a credential signed with an ECDSA P-256 key, proving the
// verifier refuses a non-RSA algorithm.
func (i *Issuer) MintES256() string {
	return i.MintWith(MintOptions{Algorithm: AlgES256})
}

// MintWithType returns a valid credential whose typ header is typ, e.g. "at+jwt".
func (i *Issuer) MintWithType(typ string) string {
	return i.MintWith(MintOptions{Type: typ})
}

// MintBadSignature returns a credential carrying the active kid but signed with a
// key pair the verifier does not hold.
func (i *Issuer) MintBadSignature() string {
	return i.MintWith(MintOptions{SignKey: i.foreignKey()})
}

// MintCorruptSignature returns a valid credential whose signature bytes have one
// bit flipped, so the wire shape stays well-formed but the signature does not
// verify. The segment is decoded, the low bit of a middle byte inverted and the
// result re-encoded: unconditional, so the corruption is guaranteed rather than
// dependent on what the signature happened to contain.
func (i *Issuer) MintCorruptSignature() string {
	compact := i.Mint(Claims{})
	idx := strings.LastIndex(compact, ".")
	raw, err := base64.RawURLEncoding.DecodeString(compact[idx+1:])
	if err != nil {
		panic(fmt.Sprintf("auth/testing: minted signature is not base64url: %v", err))
	}
	raw[len(raw)/2] ^= 0x01
	return compact[:idx+1] + base64.RawURLEncoding.EncodeToString(raw)
}
