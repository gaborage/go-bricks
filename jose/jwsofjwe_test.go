package jose

import (
	"crypto/rsa"
	"strings"
	"testing"
	"time"

	jose "github.com/go-jose/go-jose/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/jose/internal/cryptoadapter"
)

// jwsOfJWEFixture mirrors newTestFixture's symmetric kid namespace: Seal signs as
// "peer-key" and encrypts to "our-key", so Open verifies "peer-key" and decrypts "our-key".
// One shared key pair backs both kids.
type jwsOfJWEFixture struct {
	priv     *rsa.PrivateKey
	resolver *fixtureResolver
	outbound *Policy
	inbound  *Policy
}

func newJWSofJWEFixture(t *testing.T) *jwsOfJWEFixture {
	t.Helper()
	priv, pub := bareKeys()
	outbound, inbound := jwsOfJWEOutbound(), jwsOfJWEInbound()
	outbound.SignKid, outbound.EncryptKid = "peer-key", "our-key"
	outbound.SigAlg, inbound.SigAlg = jose.PS256, jose.PS256
	return &jwsOfJWEFixture{
		priv: priv,
		resolver: &fixtureResolver{
			priv: map[string]*rsa.PrivateKey{"our-key": priv, "peer-key": priv},
			pub:  map[string]*rsa.PublicKey{"our-key": pub, "peer-key": pub},
		},
		outbound: outbound,
		inbound:  inbound,
	}
}

// innerJWE verifies the outer JWS with go-jose directly and returns its payload, the
// compact inner JWE.
func innerJWE(t *testing.T, compact string, key *rsa.PrivateKey) string {
	t.Helper()
	jws, err := jose.ParseSigned(compact, []jose.SignatureAlgorithm{jose.PS256})
	require.NoError(t, err)
	inner, err := jws.Verify(&key.PublicKey)
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
	inner := innerJWE(t, compact, f.priv)
	require.Len(t, strings.Split(inner, "."), 5, "the JWS payload is the compact JWE itself")
	assert.Equal(t, payload, decryptWithGoJose(t, inner, f.priv, jose.A256GCM))

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

	inner := innerJWE(t, compact, f.priv)
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
	assert.Equal(t, "vnd.x", peekHeader(t, innerJWE(t, compact, f.priv)).Typ)
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
		IATMillis: headerIAT(t, innerJWE(t, compact, f.priv)),
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

func TestOpenJWSofJWERefusals(t *testing.T) {
	f := newJWSofJWEFixture(t)
	sealed, err := Seal([]byte(`{"a":1}`), f.outbound, f.resolver)
	require.NoError(t, err)
	outer := func(mutate func(o *cryptoadapter.SignOptions)) string {
		o := validOuter()
		mutate(o)
		return resignOuter(t, f.priv, sealed, o)
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
		{"jwe_outer_body", innerJWE(t, sealed, f.priv), codeOuterNotJWS},
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

	plaintext, _, hdr, err := Open(resignOuter(t, f.priv, sealed, stale), f.inbound, f.resolver)
	require.NoError(t, err)
	assert.JSONEq(t, `{"a":1}`, string(plaintext))
	assert.Equal(t, int64(1), hdr.JWE.IATMillis)
}
