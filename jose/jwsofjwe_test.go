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

func TestSealJWSofJWESignsTheCompactJWEVerbatim(t *testing.T) {
	f := newJWSofJWEFixture(t)
	payload := []byte(`{"pan":"card-fixture-0000"}`)

	before := time.Now().Unix()
	compact, err := Seal(payload, f.outbound, f.resolver)
	after := time.Now().Unix()
	require.NoError(t, err)

	require.Len(t, strings.Split(compact, "."), 3)
	jws, err := jose.ParseSigned(compact, []jose.SignatureAlgorithm{jose.PS256})
	require.NoError(t, err)
	inner, err := jws.Verify(&f.priv.PublicKey)
	require.NoError(t, err)
	require.Len(t, strings.Split(string(inner), "."), 5, "the JWS payload is the compact JWE itself")
	assert.Equal(t, payload, decryptWithGoJose(t, string(inner), f.priv, jose.A256GCM))

	hdr := peekHeader(t, compact)
	assert.Equal(t, "PS256", hdr.Alg)
	assert.Equal(t, "peer-key", hdr.Kid)
	assert.Equal(t, "JOSE", hdr.Typ)
	assert.Equal(t, "JWE", hdr.Cty)
	iat, err := hdr.ExtraInt64("iat")
	require.NoError(t, err)
	assert.GreaterOrEqual(t, iat, before, "outer iat is epoch seconds")
	assert.LessOrEqual(t, iat, after, "outer iat is epoch seconds")
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
	assert.Equal(t, "our-key", hdr.JWE.Kid)
	assert.Equal(t, "RSA-OAEP-256", hdr.JWE.Alg)
	assert.Equal(t, "A256GCM", hdr.JWE.Enc)
	assert.Equal(t, "JOSE", hdr.JWE.Typ)
	assert.Empty(t, hdr.JWE.Cty)
	assert.Equal(t, headerIAT(t, innerJWE(t, compact, f.priv)), hdr.JWE.IATMillis)
}

// resignOuter re-signs a sealed body's inner JWE under the given outer header options,
// with the adapter directly rather than Seal, so each case varies one outer property.
func resignOuter(t *testing.T, f *jwsOfJWEFixture, compact string, opts *cryptoadapter.SignOptions) string {
	t.Helper()
	out, err := cryptoadapter.Sign([]byte(innerJWE(t, compact, f.priv)), f.priv, opts)
	require.NoError(t, err)
	return out
}

func TestOpenJWSofJWERefusals(t *testing.T) {
	f := newJWSofJWEFixture(t)
	sealed, err := Seal([]byte(`{"a":1}`), f.outbound, f.resolver)
	require.NoError(t, err)
	outer := func(mutate func(o *cryptoadapter.SignOptions)) string {
		o := &cryptoadapter.SignOptions{Kid: "peer-key", SigAlg: jose.PS256, Cty: "JWE", Typ: "JOSE"}
		mutate(o)
		return resignOuter(t, f, sealed, o)
	}
	sig := strings.LastIndexByte(sealed, '.') + 10
	flip := byte('A')
	if sealed[sig] == 'A' {
		flip = 'B'
	}
	tampered := sealed[:sig] + string(flip) + sealed[sig+1:]

	tests := []struct {
		name     string
		body     string
		wantCode string
	}{
		{"tampered_signature", tampered, codeSignatureInvalid},
		{"wrong_signing_kid", outer(func(o *cryptoadapter.SignOptions) { o.Kid = "rogue-key" }), codeKidUnknown},
		{"disallowed_signature_algorithm", outer(func(o *cryptoadapter.SignOptions) { o.SigAlg = jose.RS256 }), codeAlgorithmDisallowed},
		{"outer_without_cty", outer(func(o *cryptoadapter.SignOptions) { o.Cty = "" }), codeCtyRejected},
		{"outer_with_other_cty", outer(func(o *cryptoadapter.SignOptions) { o.Cty = "JWS" }), codeCtyRejected},
		{"jwe_outer_body", innerJWE(t, sealed, f.priv), "JOSE_OUTER_NOT_JWS"},
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
	stale := resignOuter(t, f, sealed, &cryptoadapter.SignOptions{
		Kid: "peer-key", SigAlg: jose.PS256, Cty: "JWE", Typ: "JOSE", Extra: map[string]any{"iat": 1},
	})

	plaintext, _, hdr, err := Open(stale, f.inbound, f.resolver)
	require.NoError(t, err)
	assert.JSONEq(t, `{"a":1}`, string(plaintext))
	assert.Equal(t, int64(1), hdr.JWE.IATMillis)
}
