package jose

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"strings"
	"sync"
	"testing"
	"time"

	jose "github.com/go-jose/go-jose/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/jose/internal/cryptoadapter"
)

// jwsOfJWEFixture mirrors newTestFixture's symmetric kid namespace: Seal signs as
// "peer-key" and encrypts to "our-key", so Open verifies "peer-key" and decrypts "our-key".
// The two kids hold DIFFERENT key pairs on purpose: with one pair behind both, a regression
// that swapped the signing and encryption lookups would still round-trip.
type jwsOfJWEFixture struct {
	signPriv *rsa.PrivateKey // "peer-key": signs the outer JWS, verifies it
	encPriv  *rsa.PrivateKey // "our-key": receives the inner JWE, decrypts it
	resolver *fixtureResolver
	outbound *Policy
	inbound  *Policy
}

// signingKeys is the outer-JWS pair, distinct from bareKeys, which serves the inner JWE.
// Generated once: the tests only read it.
var signingKeys = sync.OnceValues(func() (*rsa.PrivateKey, *rsa.PublicKey) {
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		panic(err)
	}
	return priv, &priv.PublicKey
})

func newJWSofJWEFixture(t *testing.T) *jwsOfJWEFixture {
	t.Helper()
	encPriv, encPub := bareKeys()
	signPriv, signPub := signingKeys()
	outbound, inbound := jwsOfJWEOutbound(), jwsOfJWEInbound()
	outbound.SignKid, outbound.EncryptKid = "peer-key", "our-key"
	outbound.SigAlg, inbound.SigAlg = jose.PS256, jose.PS256
	return &jwsOfJWEFixture{
		signPriv: signPriv,
		encPriv:  encPriv,
		resolver: &fixtureResolver{
			priv: map[string]*rsa.PrivateKey{"our-key": encPriv, "peer-key": signPriv},
			pub:  map[string]*rsa.PublicKey{"our-key": encPub, "peer-key": signPub},
		},
		outbound: outbound,
		inbound:  inbound,
	}
}

// innerJWE verifies the outer JWS with go-jose directly and returns its payload, the
// compact inner JWE.
func innerJWE(t *testing.T, compact string, signKey *rsa.PrivateKey) string {
	t.Helper()
	jws, err := jose.ParseSigned(compact, []jose.SignatureAlgorithm{jose.PS256})
	require.NoError(t, err)
	inner, err := jws.Verify(&signKey.PublicKey)
	require.NoError(t, err)
	return string(inner)
}

// validOuter is the outer JWS header a JWS-of-JWE Open accepts for the fixture's inbound
// policy; each refusal case varies one field of it.
func validOuter() *cryptoadapter.SignOptions {
	return &cryptoadapter.SignOptions{Kid: "peer-key", SigAlg: jose.PS256, Cty: "JWE", Typ: "JOSE"}
}

// resignOuter re-signs a sealed body's inner JWE under opts with the adapter directly,
// not Seal.
func resignOuter(t *testing.T, key *rsa.PrivateKey, compact string, opts *cryptoadapter.SignOptions) string {
	t.Helper()
	out, err := cryptoadapter.Sign([]byte(innerJWE(t, compact, key)), key, opts)
	require.NoError(t, err)
	return out
}

func TestSealJWSofJWESignsTheCompactJWEVerbatim(t *testing.T) {
	f := newJWSofJWEFixture(t)
	payload := []byte(`{"pan":"card-fixture-0000"}`)

	before := time.Now().Unix()
	compact, err := Seal(payload, f.outbound, f.resolver)
	after := time.Now().Unix()
	require.NoError(t, err)

	require.Len(t, strings.Split(compact, "."), 3)
	inner := innerJWE(t, compact, f.signPriv)
	require.Len(t, strings.Split(inner, "."), 5, "the JWS payload is the compact JWE itself")
	assert.Equal(t, payload, decryptWithGoJose(t, inner, f.encPriv, jose.A256GCM))

	hdr := peekHeader(t, compact)
	assert.Equal(t, "PS256", hdr.Alg)
	assert.Equal(t, "peer-key", hdr.Kid)
	assert.Equal(t, "JOSE", hdr.Typ)
	assert.Equal(t, "JWE", hdr.Cty)
	iat := headerIAT(t, compact)
	assert.GreaterOrEqual(t, iat, before, "outer iat is epoch seconds")
	assert.LessOrEqual(t, iat, after, "outer iat is epoch seconds")
}

func TestSealJWSofJWEInnerJWECarriesPolicyHeadersButNoCty(t *testing.T) {
	f := newJWSofJWEFixture(t)
	f.outbound.Typ = "JOSE"
	f.outbound.IATMillis = true
	f.outbound.Cty = DefaultCty // httpclient's WithJOSE fills Cty for every mode

	compact, err := Seal([]byte(`{}`), f.outbound, f.resolver)
	require.NoError(t, err)
	assert.Less(t, headerIAT(t, compact), int64(100_000_000_000), "outer iat stays seconds under IATMillis")
	assert.Equal(t, "JWE", peekHeader(t, compact).Cty)

	inner := innerJWE(t, compact, f.signPriv)
	hdr := peekHeader(t, inner)
	assert.Equal(t, "RSA-OAEP-256", hdr.Alg)
	assert.Equal(t, "A256GCM", hdr.Enc)
	assert.Equal(t, "our-key", hdr.Kid)
	assert.Equal(t, "JOSE", hdr.Typ)
	assert.Empty(t, hdr.Cty, "the inner JWE carries no cty")
	assert.Greater(t, headerIAT(t, inner), int64(1_600_000_000_000), "inner iat is epoch milliseconds")
}

// The outer JWS header is fixed by the mode: Policy.Typ addresses the inner JWE only.
func TestSealJWSofJWEOuterTypIgnoresPolicyTyp(t *testing.T) {
	f := newJWSofJWEFixture(t)
	f.outbound.Typ = "vnd.x"

	compact, err := Seal([]byte(`{}`), f.outbound, f.resolver)
	require.NoError(t, err)
	assert.Equal(t, "JOSE", peekHeader(t, compact).Typ)
	assert.Equal(t, "vnd.x", peekHeader(t, innerJWE(t, compact, f.signPriv)).Typ)
}

func TestOpenJWSofJWERoundTripReportsBothLayers(t *testing.T) {
	f := newJWSofJWEFixture(t)
	f.outbound.Typ = "JOSE"
	f.outbound.IATMillis = true
	payload := []byte(`{"pan":"card-fixture-0000","iat":1700000000}`)
	compact, err := Seal(payload, f.outbound, f.resolver)
	require.NoError(t, err)

	plaintext, claims, hdr, err := Open(compact, f.inbound, f.resolver)
	require.NoError(t, err)
	assert.Equal(t, payload, plaintext)
	assert.Equal(t, time.Unix(1700000000, 0).UTC(), claims.IssuedAt)

	assert.Equal(t, Header{Kid: "peer-key", Alg: "PS256", Cty: "JWE", Typ: "JOSE"}, hdr.JWS,
		"the outer iat is seconds, so it is never reported as IATMillis")
	assert.Equal(t, Header{
		Kid: "our-key", Alg: "RSA-OAEP-256", Enc: "A256GCM", Typ: "JOSE",
		IATMillis: headerIAT(t, innerJWE(t, compact, f.signPriv)),
	}, hdr.JWE)
}

// nilSigningKeyResolver hands back no signing key and no error — the shape of a buggy
// KeyResolver, and the only way to drive Seal past key resolution into a sign failure.
type nilSigningKeyResolver struct{ *fixtureResolver }

func (*nilSigningKeyResolver) PrivateKey(string) (*rsa.PrivateKey, error) { return nil, nil }

// nilEncryptKeyResolver is its encrypt-side twin, reaching the encrypt-failure arm.
type nilEncryptKeyResolver struct{ *fixtureResolver }

func (*nilEncryptKeyResolver) PublicKey(string) (*rsa.PublicKey, error) { return nil, nil }

// Seal resolves the signing key, encrypts, then signs; each step's failure must surface
// with the step's own code rather than a generic one.
func TestSealJWSofJWEPropagatesResolverAndSignFailures(t *testing.T) {
	f := newJWSofJWEFixture(t)

	tests := []struct {
		name     string
		resolver KeyResolver
		wantCode string
	}{
		{"sign_kid_unresolvable", &fixtureResolver{pub: f.resolver.pub}, codeKidUnknown},
		{"encrypt_kid_unresolvable", &fixtureResolver{priv: f.resolver.priv}, codeKidUnknown},
		{"signing_key_missing_without_error", &nilSigningKeyResolver{f.resolver}, codeOutboundFailed},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			compact, sealErr := Seal([]byte(`{"a":1}`), f.outbound, tt.resolver)
			assert.Empty(t, compact)
			requireJOSEErrorCode(t, sealErr, tt.wantCode)
		})
	}
}

// Open resolves the verify key before it parses anything, so an unknown VerifyKid is the
// resolver's error, not a signature failure.
func TestOpenJWSofJWEPropagatesVerifyKidFailure(t *testing.T) {
	f := newJWSofJWEFixture(t)
	sealed, err := Seal([]byte(`{"a":1}`), f.outbound, f.resolver)
	require.NoError(t, err)

	plaintext, _, _, err := Open(sealed, f.inbound, &fixtureResolver{priv: f.resolver.priv})
	assert.Nil(t, plaintext)
	requireJOSEErrorCode(t, err, codeKidUnknown)
}

// The two roles must not be interchangeable. Signing the outer JWS with the ENCRYPTION key,
// or encrypting the inner JWE to the SIGNING key, has to fail — otherwise a regression that
// swapped the two lookups would still round-trip through every other test here.
func TestOpenJWSofJWERefusesSwappedRoleKeys(t *testing.T) {
	f := newJWSofJWEFixture(t)
	sealed, err := Seal([]byte(`{"a":1}`), f.outbound, f.resolver)
	require.NoError(t, err)

	// Outer JWS signed with the encryption key, under the right kid. Built in two steps
	// because resignOuter verifies with the same key it re-signs with.
	inner := innerJWE(t, sealed, f.signPriv)
	wrongSigner, err := cryptoadapter.Sign([]byte(inner), f.encPriv, validOuter())
	require.NoError(t, err)

	plaintext, _, _, err := Open(wrongSigner, f.inbound, f.resolver)
	assert.Nil(t, plaintext)
	requireJOSEErrorCode(t, err, codeSignatureInvalid)

	// Inner JWE encrypted to the signing key, then signed correctly.
	signPub := &f.signPriv.PublicKey
	strayInner, err := cryptoadapter.Encrypt([]byte(`{"a":1}`), signPub, &cryptoadapter.EncryptOptions{
		Kid: "our-key", KeyAlg: DefaultKeyAlg, Enc: jose.A256GCM,
	})
	require.NoError(t, err)
	strayOuter, err := cryptoadapter.Sign([]byte(strayInner), f.signPriv, validOuter())
	require.NoError(t, err)

	plaintext, _, _, err = Open(strayOuter, f.inbound, f.resolver)
	assert.Nil(t, plaintext)
	requireJOSEErrorCode(t, err, codeDecryptFailed)
}

func TestOpenJWSofJWERefusals(t *testing.T) {
	f := newJWSofJWEFixture(t)
	sealed, err := Seal([]byte(`{"a":1}`), f.outbound, f.resolver)
	require.NoError(t, err)
	outer := func(mutate func(o *cryptoadapter.SignOptions)) string {
		o := validOuter()
		mutate(o)
		return resignOuter(t, f.signPriv, sealed, o)
	}
	// A byte inside the signature segment, clear of its final character's padding bits.
	sigByte := strings.LastIndexByte(sealed, '.') + 10
	flip := byte('A')
	if sealed[sigByte] == 'A' {
		flip = 'B'
	}
	tampered := sealed[:sigByte] + string(flip) + sealed[sigByte+1:]

	tests := []struct {
		name     string
		body     string
		wantCode string
	}{
		{"tampered_signature", tampered, codeSignatureInvalid},
		{"wrong_signing_kid", outer(func(o *cryptoadapter.SignOptions) { o.Kid = "rogue-key" }), codeKidUnknown},
		{"missing_signing_kid", outer(func(o *cryptoadapter.SignOptions) { o.Kid = "" }), codeKidMissing},
		{"unparseable_three_segment_body", "a.b.c", codeOuterNotJWS},
		// A valid protected header passes the peek; go-jose still refuses the bad segments.
		{"unparseable_signed_segments", sealed[:strings.IndexByte(sealed, '.')] + ".!!.!!", codeOuterNotJWS},
		{"disallowed_signature_algorithm", outer(func(o *cryptoadapter.SignOptions) { o.SigAlg = jose.RS256 }), codeAlgorithmDisallowed},
		{"outer_without_cty", outer(func(o *cryptoadapter.SignOptions) { o.Cty = "" }), codeCtyRejected},
		{"outer_with_other_cty", outer(func(o *cryptoadapter.SignOptions) { o.Cty = "JWS" }), codeCtyRejected},
		{"jwe_outer_body", innerJWE(t, sealed, f.signPriv), codeOuterNotJWS},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			plaintext, _, _, err := Open(tt.body, f.inbound, f.resolver)
			assert.Nil(t, plaintext)
			requireJOSEErrorCode(t, err, tt.wantCode)
		})
	}
}

func TestOpenJWSofJWENeverJudgesIAT(t *testing.T) {
	f := newJWSofJWEFixture(t)
	f.outbound.ProtectedHeaders = map[string]any{"iat": 1}
	sealed, err := Seal([]byte(`{"a":1}`), f.outbound, f.resolver)
	require.NoError(t, err)
	stale := validOuter()
	stale.Extra = map[string]any{"iat": 1}

	plaintext, _, hdr, err := Open(resignOuter(t, f.signPriv, sealed, stale), f.inbound, f.resolver)
	require.NoError(t, err)
	assert.JSONEq(t, `{"a":1}`, string(plaintext))
	assert.Equal(t, int64(1), hdr.JWE.IATMillis)
}

// ---- SealModeJWSofJWEMinimal: kid-less inbound peers ----

var minimalPayload = []byte(`{"pan":"card-fixture-0000"}`)

// jwsOfJWEMinimalFixture keeps the two roles on different pairs: signingKeys verifies the
// outer JWS ("peer-key") and bareKeys decrypts the inner JWE ("our-key"). Its inbound policy
// is the default posture, AcceptMissingKid on.
type jwsOfJWEMinimalFixture struct {
	signPriv *rsa.PrivateKey
	encPub   *rsa.PublicKey
	resolver *fixtureResolver
	inbound  *Policy
}

func newJWSofJWEMinimalFixture(t *testing.T) *jwsOfJWEMinimalFixture {
	t.Helper()
	encPriv, encPub := bareKeys()
	signPriv, signPub := signingKeys()
	inbound := jwsOfJWEMinimalInbound()
	inbound.AcceptMissingKid = true
	return &jwsOfJWEMinimalFixture{
		signPriv: signPriv,
		encPub:   encPub,
		resolver: &fixtureResolver{
			priv: map[string]*rsa.PrivateKey{"our-key": encPriv},
			pub:  map[string]*rsa.PublicKey{"peer-key": signPub},
		},
		inbound: inbound,
	}
}

// withAcceptMissingKid returns a copy of the fixture's inbound policy under the given setting.
func (f *jwsOfJWEMinimalFixture) withAcceptMissingKid(accept bool) *Policy {
	p := *f.inbound
	p.AcceptMissingKid = accept
	return &p
}

// mintMinimalInner encrypts with go-jose directly, never Seal. Nil opts and no recipient key
// id give exactly {"alg":"RSA-OAEP-256","enc":"A256GCM"}.
func mintMinimalInner(t *testing.T, pub *rsa.PublicKey, keyAlg jose.KeyAlgorithm, enc jose.ContentEncryption, opts *jose.EncrypterOptions) string {
	t.Helper()
	encrypter, err := jose.NewEncrypter(enc, jose.Recipient{Algorithm: keyAlg, Key: pub}, opts)
	require.NoError(t, err)
	obj, err := encrypter.Encrypt(minimalPayload)
	require.NoError(t, err)
	compact, err := obj.CompactSerialize()
	require.NoError(t, err)
	return compact
}

// mintDetachedOuter signs jwe with go-jose directly and returns the detached form
// (header..signature) the profile sends. It also checks that spliceDetached rebuilds
// go-jose's own attached form, byte for byte.
func mintDetachedOuter(t *testing.T, key *rsa.PrivateKey, alg jose.SignatureAlgorithm, jwe string, opts *jose.SignerOptions) string {
	t.Helper()
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: alg, Key: key}, opts)
	require.NoError(t, err)
	obj, err := signer.Sign([]byte(jwe))
	require.NoError(t, err)
	detached, err := obj.DetachedCompactSerialize()
	require.NoError(t, err)
	attached, err := obj.CompactSerialize()
	require.NoError(t, err)
	require.Equal(t, attached, spliceDetached(t, detached, jwe))
	return detached
}

// spliceDetached rebuilds the attached compact JWS Open takes from a detached one and the JWE
// compact string it signed: header.base64url(jwe).signature (RFC 7515 Appendix F). Every
// splicing test goes through it.
func spliceDetached(t *testing.T, detached, jwe string) string {
	t.Helper()
	header, rest, ok := strings.Cut(detached, ".")
	require.True(t, ok)
	payload, signature, ok := strings.Cut(rest, ".")
	require.True(t, ok)
	require.Empty(t, payload)
	return header + "." + base64.RawURLEncoding.EncodeToString([]byte(jwe)) + "." + signature
}

// vector mints the default vector, varied only by the given header options, and splices it.
func (f *jwsOfJWEMinimalFixture) vector(t *testing.T, inner *jose.EncrypterOptions, outer *jose.SignerOptions) string {
	t.Helper()
	jwe := mintMinimalInner(t, f.encPub, jose.RSA_OAEP_256, jose.A256GCM, inner)
	return spliceDetached(t, mintDetachedOuter(t, f.signPriv, jose.RS512, jwe, outer), jwe)
}

func outerKid(v any) *jose.SignerOptions    { return (&jose.SignerOptions{}).WithHeader("kid", v) }
func innerKid(v any) *jose.EncrypterOptions { return (&jose.EncrypterOptions{}).WithHeader("kid", v) }

func outerCty(cty string) *jose.SignerOptions {
	return (&jose.SignerOptions{}).WithContentType(jose.ContentType(cty))
}

func innerCty(cty string) *jose.EncrypterOptions {
	return (&jose.EncrypterOptions{}).WithContentType(jose.ContentType(cty))
}

// requireJOSEErrorStatus asserts both the code and the HTTP status of a refusal.
func requireJOSEErrorStatus(t *testing.T, err error, wantCode string, wantStatus int) {
	t.Helper()
	var jerr *Error
	require.ErrorAs(t, err, &jerr)
	assert.Equal(t, wantCode, jerr.Code)
	assert.Equal(t, wantStatus, jerr.Status)
}

// requireOpen opens body under p and asserts the payload, or the refusal when wantCode is set.
func (f *jwsOfJWEMinimalFixture) requireOpen(t *testing.T, body string, p *Policy, wantCode string, wantStatus int) {
	t.Helper()
	plaintext, _, _, err := Open(body, p, f.resolver)
	if wantCode == "" {
		require.NoError(t, err)
		assert.Equal(t, minimalPayload, plaintext)
		return
	}
	assert.Nil(t, plaintext)
	requireJOSEErrorStatus(t, err, wantCode, wantStatus)
}

func TestOpenJWSofJWEMinimalOpensKidlessDetachedVector(t *testing.T) {
	f := newJWSofJWEMinimalFixture(t)
	jwe := mintMinimalInner(t, f.encPub, jose.RSA_OAEP_256, jose.A256GCM, nil)
	detached := mintDetachedOuter(t, f.signPriv, jose.RS512, jwe, nil)
	_, outerHasKid := headerParams(t, detached)["kid"]
	_, innerHasKid := headerParams(t, jwe)["kid"]
	require.False(t, outerHasKid)
	require.False(t, innerHasKid)

	plaintext, _, hdr, err := Open(spliceDetached(t, detached, jwe), f.inbound, f.resolver)
	require.NoError(t, err)
	assert.Equal(t, minimalPayload, plaintext)
	assert.Equal(t, "RS512", hdr.JWS.Alg)
	assert.Empty(t, hdr.JWS.Kid)
	assert.Zero(t, hdr.JWS.IATMillis)
	assert.Empty(t, hdr.JWE.Kid)
	assert.Equal(t, "A256GCM", hdr.JWE.Enc)
}

func TestOpenJWSofJWEMinimalKidRules(t *testing.T) {
	f := newJWSofJWEMinimalFixture(t)
	tests := []struct {
		name       string
		inner      *jose.EncrypterOptions
		outer      *jose.SignerOptions
		accept     bool
		wantCode   string
		wantStatus int
	}{
		{"outer_matching_inner_absent_flag_on", nil, outerKid("peer-key"), true, "", 0},
		{"inner_matching_outer_absent_flag_on", innerKid("our-key"), nil, true, "", 0},
		{"both_matching_flag_on", innerKid("our-key"), outerKid("peer-key"), true, "", 0},
		{"both_absent_flag_off", nil, nil, false, codeKidMissing, 401},
		{"outer_matching_inner_absent_flag_off", nil, outerKid("peer-key"), false, codeKidMissing, 401},
		{"inner_matching_outer_absent_flag_off", innerKid("our-key"), nil, false, codeKidMissing, 401},
		{"outer_wrong_flag_on", nil, outerKid("rogue-key"), true, codeKidUnknown, 401},
		{"inner_wrong_flag_on", innerKid("rogue-key"), nil, true, codeKidUnknown, 401},
		{"outer_present_empty_flag_on", nil, outerKid(""), true, codeKidUnknown, 401},
		{"outer_present_null_flag_on", nil, outerKid(nil), true, codeKidUnknown, 401},
		{"outer_present_empty_flag_off", nil, outerKid(""), false, codeKidMissing, 401},
		{"outer_present_null_flag_off", nil, outerKid(nil), false, codeKidMissing, 401},
		{"inner_present_empty_flag_on", innerKid(""), nil, true, codeKidUnknown, 401},
		{"inner_present_null_flag_on", innerKid(nil), nil, true, codeKidUnknown, 401},
		{"outer_non_string_kid_flag_on", nil, outerKid(42), true, codeOuterNotJWS, 400},
		{"outer_non_string_kid_flag_off", nil, outerKid(42), false, codeOuterNotJWS, 400},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f.requireOpen(t, f.vector(t, tt.inner, tt.outer), f.withAcceptMissingKid(tt.accept), tt.wantCode, tt.wantStatus)
		})
	}
}

func TestOpenJWSofJWEMinimalOuterCty(t *testing.T) {
	f := newJWSofJWEMinimalFixture(t)
	tests := []struct {
		name     string
		outer    *jose.SignerOptions
		wantCode string
	}{
		{"outer_cty_jwe", outerCty("JWE"), ""},
		{"outer_cty_absent", nil, ""},
		{"outer_cty_jws", outerCty("JWS"), codeCtyRejected},
		{"outer_cty_application_json", outerCty("application/json"), codeCtyRejected},
		{"outer_cty_present_empty", outerCty(""), codeCtyRejected},
		{"outer_cty_present_null", (&jose.SignerOptions{}).WithHeader(jose.HeaderContentType, nil), codeCtyRejected},
		{"outer_cty_non_string", (&jose.SignerOptions{}).WithHeader(jose.HeaderContentType, 42), codeCtyRejected},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f.requireOpen(t, f.vector(t, nil, tt.outer), f.inbound, tt.wantCode, 400)
		})
	}
}

// The present-cty rows above test a written member: each option set puts cty on the wire.
func TestOpenJWSofJWEMinimalOuterCtyRowsWriteTheMember(t *testing.T) {
	f := newJWSofJWEMinimalFixture(t)
	jwe := mintMinimalInner(t, f.encPub, jose.RSA_OAEP_256, jose.A256GCM, nil)
	for _, v := range []any{"", nil, 42} {
		detached := mintDetachedOuter(t, f.signPriv, jose.RS512, jwe, (&jose.SignerOptions{}).WithHeader(jose.HeaderContentType, v))
		_, present := headerParams(t, detached)["cty"]
		assert.True(t, present, "cty %v", v)
	}
}

// SealModeJWSofJWE still requires an outer cty of exactly JWE: a present empty one is refused
// as an absent one is.
func TestOpenJWSofJWERefusesAPresentEmptyOuterCty(t *testing.T) {
	f := newJWSofJWEMinimalFixture(t)
	p := jwsOfJWEInbound()
	p.SigAlg = jose.PS256
	jwe := mintMinimalInner(t, f.encPub, jose.RSA_OAEP_256, jose.A256GCM, innerKid("our-key"))
	detached := mintDetachedOuter(t, f.signPriv, jose.PS256, jwe, outerKid("peer-key").WithContentType(""))
	requireJOSEErrorStatus(t, openErr(spliceDetached(t, detached, jwe), p, f.resolver), codeCtyRejected, 400)
}

// The outer cty presence rule leaves the inner JWE alone: a present empty inner cty still
// passes bare mode's permissive rule.
func TestOpenJWSofJWEMinimalInnerCtyRuleUnchanged(t *testing.T) {
	f := newJWSofJWEMinimalFixture(t)
	jwe := mintMinimalInner(t, f.encPub, jose.RSA_OAEP_256, jose.A256GCM, innerCty(""))
	_, present := headerParams(t, jwe)["cty"]
	require.True(t, present)
	f.requireOpen(t, spliceDetached(t, mintDetachedOuter(t, f.signPriv, jose.RS512, jwe, nil), jwe), f.inbound, "", 0)
}

func TestOpenJWSofJWEMinimalRefusals(t *testing.T) {
	f := newJWSofJWEMinimalFixture(t)
	jwe := mintMinimalInner(t, f.encPub, jose.RSA_OAEP_256, jose.A256GCM, nil)
	detached := mintDetachedOuter(t, f.signPriv, jose.RS512, jwe, nil)
	tamperedJWE := tamperSegment(t, jwe, jweCiphertextSegment)
	signedInner := func(keyAlg jose.KeyAlgorithm, enc jose.ContentEncryption, opts *jose.EncrypterOptions) string {
		inner := mintMinimalInner(t, f.encPub, keyAlg, enc, opts)
		return spliceDetached(t, mintDetachedOuter(t, f.signPriv, jose.RS512, inner, nil), inner)
	}

	tests := []struct {
		name       string
		body       string
		accept     bool
		wantCode   string
		wantStatus int
	}{
		{"tampered_jwe_string", spliceDetached(t, detached, tamperedJWE), true, codeSignatureInvalid, 401},
		{"unspliced_detached", detached, true, codeSignatureInvalid, 401},
		{"five_segment_body_flag_on", jwe, true, codeOuterNotJWS, 400},
		{"five_segment_body_flag_off", jwe, false, codeOuterNotJWS, 400},
		{"inner_a256cbc_hs512", signedInner(jose.RSA_OAEP_256, jose.A256CBC_HS512, nil), true, codeMalformed, 400},
		{"inner_a128gcm", signedInner(jose.RSA_OAEP_256, jose.A128GCM, nil), true, codeMalformed, 400},
		{"inner_cty_jws", f.vector(t, innerCty("JWS"), nil), true, codeCtyRejected, 400},
		{"inner_rsa_oaep_sha1", signedInner(jose.RSA_OAEP, jose.A256GCM, nil), true, codeMalformed, 400},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f.requireOpen(t, tt.body, f.withAcceptMissingKid(tt.accept), tt.wantCode, tt.wantStatus)
		})
	}
}

// The outer alg pin runs before any key lookup, so refusingResolver fails the test if a key
// is ever resolved.
func TestOpenJWSofJWEMinimalAlgPinResolvesNoKey(t *testing.T) {
	f := newJWSofJWEMinimalFixture(t)
	jwe := mintMinimalInner(t, f.encPub, jose.RSA_OAEP_256, jose.A256GCM, nil)
	for _, alg := range []jose.SignatureAlgorithm{jose.PS256, jose.RS256} {
		body := spliceDetached(t, mintDetachedOuter(t, f.signPriv, alg, jwe, nil), jwe)
		for _, accept := range []bool{true, false} {
			requireJOSEErrorStatus(t, openErr(body, f.withAcceptMissingKid(accept), refusingResolver{t}), codeAlgorithmDisallowed, 400)
		}
	}
}
