package jose

import (
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	joselib "github.com/go-jose/go-jose/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// bareFixture holds one key pair plus the matching bare-mode policies. The kid namespace
// matches newTestFixture's so the two fixtures read alike.
type bareFixture struct {
	ourPriv  *rsa.PrivateKey
	resolver *fixtureResolver
	outbound *Policy
	inbound  *Policy
}

func newBareFixture(t *testing.T) *bareFixture {
	t.Helper()
	ourPriv, ourPub := generateKeyPair(t)
	return &bareFixture{
		ourPriv: ourPriv,
		resolver: &fixtureResolver{
			priv: map[string]*rsa.PrivateKey{"our-key": ourPriv},
			pub:  map[string]*rsa.PublicKey{"our-key": ourPub},
		},
		outbound: &Policy{
			Direction:  DirectionOutbound,
			Mode:       SealModeBareJWE,
			EncryptKid: "our-key",
			KeyAlg:     DefaultKeyAlg,
			Enc:        joselib.A128GCM,
		},
		inbound: &Policy{
			Direction:  DirectionInbound,
			Mode:       SealModeBareJWE,
			DecryptKid: "our-key",
			KeyAlg:     DefaultKeyAlg,
			Enc:        joselib.A128GCM,
		},
	}
}

// protectedHeaderOf decodes segment 0 of a compact serialization directly, so the
// assertions read the wire bytes rather than anything the package computed.
func protectedHeaderOf(t *testing.T, compact string) map[string]any {
	t.Helper()
	seg, _, ok := strings.Cut(compact, ".")
	require.True(t, ok)
	raw, err := base64.RawURLEncoding.DecodeString(seg)
	require.NoError(t, err)
	var hdr map[string]any
	require.NoError(t, json.Unmarshal(raw, &hdr))
	return hdr
}

// decryptWithGoJose decrypts a compact JWE with go-jose directly — an oracle independent
// of the package's own Open path.
func decryptWithGoJose(t *testing.T, compact string, key *rsa.PrivateKey, enc joselib.ContentEncryption) []byte {
	t.Helper()
	obj, err := joselib.ParseEncrypted(compact,
		[]joselib.KeyAlgorithm{joselib.RSA_OAEP_256},
		[]joselib.ContentEncryption{enc})
	require.NoError(t, err)
	plaintext, err := obj.Decrypt(key)
	require.NoError(t, err)
	return plaintext
}

func TestSealBareJWEWritesProtectedHeaders(t *testing.T) {
	f := newBareFixture(t)
	f.outbound.Typ = "JOSE"
	f.outbound.ProtectedHeaders = map[string]any{"iss": "acme-payments"}
	f.outbound.IATMillis = true
	payload := []byte(`{"pan":"4111111111111111"}`)

	compact, err := Seal(payload, f.outbound, f.resolver)
	require.NoError(t, err)

	hdr := protectedHeaderOf(t, compact)
	assert.Equal(t, "RSA-OAEP-256", hdr["alg"])
	assert.Equal(t, "A128GCM", hdr["enc"])
	assert.Equal(t, "our-key", hdr["kid"])
	assert.Equal(t, "JOSE", hdr["typ"])
	assert.Equal(t, "acme-payments", hdr["iss"])
	assert.NotContains(t, hdr, "cty", "an unset policy Cty must leave cty off the wire")

	iat, ok := hdr["iat"].(float64)
	require.True(t, ok, "iat must be a JSON number, got %T", hdr["iat"])
	// Milliseconds, not seconds: 2001-09-09 in seconds is 1e9, in milliseconds 1e12.
	assert.Greater(t, int64(iat), int64(1_600_000_000_000))
	assert.Less(t, int64(iat), int64(100_000_000_000_000))

	// No inner JWS: the ciphertext holds the caller's bytes verbatim.
	assert.Equal(t, payload, decryptWithGoJose(t, compact, f.ourPriv, joselib.A128GCM))
}

func TestSealBareJWEWritesCtyWhenPolicySetsIt(t *testing.T) {
	f := newBareFixture(t)
	f.outbound.Cty = DefaultCty

	compact, err := Seal([]byte(`{}`), f.outbound, f.resolver)
	require.NoError(t, err)
	assert.Equal(t, DefaultCty, protectedHeaderOf(t, compact)["cty"])
}

func TestSealBareJWEOmitsIATWhenNotStamping(t *testing.T) {
	f := newBareFixture(t)

	compact, err := Seal([]byte(`{}`), f.outbound, f.resolver)
	require.NoError(t, err)
	hdr := protectedHeaderOf(t, compact)
	assert.NotContains(t, hdr, "iat")
	assert.NotContains(t, hdr, "typ")
}

func TestSealBareJWERejectsInvalidPolicy(t *testing.T) {
	f := newBareFixture(t)
	f.outbound.SignKid = "our-key" // never legal in bare mode

	_, err := Seal([]byte(`{}`), f.outbound, f.resolver)
	require.ErrorIs(t, err, ErrPolicyMismatch)
	requireJOSEErrorCode(t, err, codePolicyDirectionMismatch)
}

func TestOpenBareJWERoundTrip(t *testing.T) {
	f := newBareFixture(t)
	f.outbound.Typ = "JOSE"
	f.outbound.IATMillis = true
	payload := []byte(`{"pan":"4111111111111111","sub":"cardholder-9"}`)

	compact, err := Seal(payload, f.outbound, f.resolver)
	require.NoError(t, err)

	plaintext, claims, hdr, err := Open(compact, f.inbound, f.resolver)
	require.NoError(t, err)
	assert.Equal(t, payload, plaintext)
	require.NotNil(t, claims)
	assert.Equal(t, "cardholder-9", claims.Subject)

	assert.Equal(t, "our-key", hdr.JWE.Kid)
	assert.Equal(t, "RSA-OAEP-256", hdr.JWE.Alg)
	assert.Equal(t, "A128GCM", hdr.JWE.Enc)
	assert.Equal(t, "JOSE", hdr.JWE.Typ)
	assert.Empty(t, hdr.JWE.Cty)
	// iat is reported as written, in milliseconds; jose never judges its freshness.
	wireIAT, ok := protectedHeaderOf(t, compact)["iat"].(float64)
	require.True(t, ok)
	assert.Equal(t, int64(wireIAT), hdr.JWE.IATMillis)

	// No inner JWS layer exists, so its header stays zero.
	assert.Equal(t, Header{}, hdr.JWS)
}

func TestOpenBareJWEWithoutIATReportsZero(t *testing.T) {
	f := newBareFixture(t)

	compact, err := Seal([]byte(`{}`), f.outbound, f.resolver)
	require.NoError(t, err)
	_, _, hdr, err := Open(compact, f.inbound, f.resolver)
	require.NoError(t, err)
	assert.Zero(t, hdr.JWE.IATMillis)
	assert.Empty(t, hdr.JWE.Typ)
}

func TestOpenBareJWERoundTripA256GCM(t *testing.T) {
	f := newBareFixture(t)
	f.outbound.Enc = joselib.A256GCM
	f.inbound.Enc = joselib.A256GCM
	payload := []byte(`{"amount":1250}`)

	compact, err := Seal(payload, f.outbound, f.resolver)
	require.NoError(t, err)
	assert.Equal(t, "A256GCM", protectedHeaderOf(t, compact)["enc"])

	plaintext, _, _, err := Open(compact, f.inbound, f.resolver)
	require.NoError(t, err)
	assert.Equal(t, payload, plaintext)
}
