package jose

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/jose/internal/cryptoadapter"
)

// Direct unit tests for mapDecryptError / mapVerifyError. The end-to-end roundtrip
// covers ErrParseEncrypted, ErrDecryptFailed, ErrParseSigned, and ErrVerifyFailed
// indirectly; the table tests below cover the remaining sentinels (ErrKidMissing,
// ErrKidMismatch, default) plus the full mapVerifyError surface.

func TestMapDecryptErrorAllArms(t *testing.T) {
	hdr := cryptoadapter.Header{Kid: "k1", Alg: "RSA-OAEP-256", Enc: "A256GCM"}
	tests := []struct {
		name     string
		in       error
		wantCode string
		wantStat int
	}{
		{name: "parse_failed", in: cryptoadapter.ErrParseEncrypted, wantCode: "JOSE_MALFORMED", wantStat: 400},
		{name: "kid_missing", in: cryptoadapter.ErrKidMissing, wantCode: "JOSE_KID_MISSING", wantStat: 401},
		{name: "kid_mismatch", in: cryptoadapter.ErrKidMismatch, wantCode: "JOSE_KID_UNKNOWN", wantStat: 401},
		{name: "decrypt_failed", in: cryptoadapter.ErrDecryptFailed, wantCode: "JOSE_DECRYPT_FAILED", wantStat: 401},
		{name: "default_unknown", in: errors.New("something else"), wantCode: "JOSE_DECRYPT_FAILED", wantStat: 401},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := mapDecryptError(tt.in, nil, &hdr)
			assert.Equal(t, tt.wantCode, got.Code)
			assert.Equal(t, tt.wantStat, got.Status)
		})
	}
}

func TestMapVerifyErrorAllArms(t *testing.T) {
	hdr := cryptoadapter.Header{Kid: "k1", Alg: "RS256"}
	tests := []struct {
		name     string
		in       error
		wantCode string
		wantStat int
	}{
		{name: "parse_failed", in: cryptoadapter.ErrParseSigned, wantCode: "JOSE_INNER_NOT_JWS", wantStat: 400},
		{name: "kid_missing", in: cryptoadapter.ErrKidMissing, wantCode: "JOSE_KID_MISSING", wantStat: 401},
		{name: "kid_mismatch", in: cryptoadapter.ErrKidMismatch, wantCode: "JOSE_KID_UNKNOWN", wantStat: 401},
		{name: "verify_failed", in: cryptoadapter.ErrVerifyFailed, wantCode: "JOSE_SIGNATURE_INVALID", wantStat: 401},
		{name: "default_unknown", in: errors.New("something else"), wantCode: "JOSE_SIGNATURE_INVALID", wantStat: 401},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := mapVerifyError(tt.in, nil, &hdr)
			assert.Equal(t, tt.wantCode, got.Code)
			assert.Equal(t, tt.wantStat, got.Status)
		})
	}
}

func TestParseClaimsNonObjectPayload(t *testing.T) {
	c := parseClaims([]byte("not-json-at-all"))
	require.NotNil(t, c)
	assert.Empty(t, c.Raw)
	assert.Empty(t, c.Issuer)
}

func TestParseClaimsAudAsString(t *testing.T) {
	c := parseClaims([]byte(`{"aud":"single-audience"}`))
	assert.Equal(t, []string{"single-audience"}, c.Audience)
}

// oversizedHeader pads a protected header past the adapter's bound. The bound itself is
// cryptoadapter's unexported maxPeekHeaderBytes, so this uses a value far above it rather
// than re-spelling the constant across the package boundary.
func oversizedHeader() map[string]any {
	return map[string]any{"pad": strings.Repeat("a", 1<<20)}
}

// The header bound applies to every compact Open parses, at both layers and in every mode.
// go-jose enforces no bound of its own, so without it these tokens open normally.
func TestOpenRefusesAnOversizedProtectedHeader(t *testing.T) {
	f := newTestFixture(t)
	ourPub, err := f.resolver.PublicKey("our-key")
	require.NoError(t, err)
	peerPriv, err := f.resolver.PrivateKey("peer-key")
	require.NoError(t, err)

	bigJWE, err := cryptoadapter.Encrypt([]byte(`{"a":1}`), ourPub, &cryptoadapter.EncryptOptions{
		Kid: "our-key", KeyAlg: DefaultKeyAlg, Enc: DefaultEnc, Cty: ctyNestedJWS, Extra: oversizedHeader(),
	})
	require.NoError(t, err)

	bigJWS, err := cryptoadapter.Sign([]byte(`{"a":1}`), peerPriv, &cryptoadapter.SignOptions{
		Kid: "peer-key", SigAlg: DefaultSigAlg, Cty: DefaultCty, Extra: oversizedHeader(),
	})
	require.NoError(t, err)
	// A normal-sized JWE carrying the oversized inner JWS: the outer layer passes the bound,
	// the inner one must not.
	nestedOverInner, err := cryptoadapter.Encrypt([]byte(bigJWS), ourPub, &cryptoadapter.EncryptOptions{
		Kid: "our-key", KeyAlg: DefaultKeyAlg, Enc: DefaultEnc, Cty: ctyNestedJWS,
	})
	require.NoError(t, err)

	bare := bareInbound()
	bare.DecryptKid = "our-key"
	bigBareJWE, err := cryptoadapter.Encrypt([]byte(`{"a":1}`), ourPub, &cryptoadapter.EncryptOptions{
		Kid: "our-key", KeyAlg: DefaultKeyAlg, Enc: bare.Enc, Extra: oversizedHeader(),
	})
	require.NoError(t, err)

	tests := []struct {
		name     string
		compact  string
		policy   *Policy
		wantCode string
	}{
		{"nested_outer_jwe_header", bigJWE, f.inbound, codeMalformed},
		{"nested_inner_jws_header", nestedOverInner, f.inbound, "JOSE_INNER_NOT_JWS"},
		{"bare_jwe_header", bigBareJWE, bare, codeMalformed},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			plaintext, _, _, openErr := Open(tt.compact, tt.policy, f.resolver)
			assert.Nil(t, plaintext)
			requireJOSEErrorCode(t, openErr, tt.wantCode)
		})
	}
}

// jsonJWS and jsonJWE re-serialize a compact token's untouched segments as the flattened JSON
// serialization, so the serialization is the token's only defect.
func jsonJWS(compact string) string {
	s := strings.Split(compact, ".")
	return fmt.Sprintf(`{"protected":%q,"payload":%q,"signature":%q}`, s[0], s[1], s[2])
}

func jsonJWE(compact string) string {
	s := strings.Split(compact, ".")
	return fmt.Sprintf(`{"protected":%q,"encrypted_key":%q,"iv":%q,"ciphertext":%q,"tag":%q}`, s[0], s[1], s[2], s[3], s[4])
}

// The documented wire format is compact: a JSON-serialized layer gets the code a malformed
// compact one does.
func TestOpenRefusesJSONSerialization(t *testing.T) {
	f := newTestFixture(t)
	nested, err := Seal([]byte(`{"a":1}`), f.outbound, f.resolver)
	require.NoError(t, err)

	ourPub, err := f.resolver.PublicKey("our-key")
	require.NoError(t, err)
	peerPriv, err := f.resolver.PrivateKey("peer-key")
	require.NoError(t, err)
	innerJWS, err := cryptoadapter.Sign([]byte(`{"a":1}`), peerPriv, &cryptoadapter.SignOptions{
		Kid: "peer-key", SigAlg: DefaultSigAlg, Cty: DefaultCty,
	})
	require.NoError(t, err)
	nestedOverJSONInner, err := cryptoadapter.Encrypt([]byte(jsonJWS(innerJWS)), ourPub, &cryptoadapter.EncryptOptions{
		Kid: "our-key", KeyAlg: DefaultKeyAlg, Enc: DefaultEnc, Cty: ctyNestedJWS,
	})
	require.NoError(t, err)

	bareOut, bareIn := bareOutbound(), bareInbound()
	bareOut.EncryptKid = "our-key"
	bare, err := Seal([]byte(`{"a":1}`), bareOut, f.resolver)
	require.NoError(t, err)

	tests := []struct {
		name     string
		compact  string
		policy   *Policy
		wantCode string
	}{
		{"nested_outer_jwe", jsonJWE(nested), f.inbound, codeMalformed},
		{"nested_inner_jws", nestedOverJSONInner, f.inbound, codeInnerNotJWS},
		{"bare_jwe", jsonJWE(bare), bareIn, codeMalformed},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			plaintext, _, _, openErr := Open(tt.compact, tt.policy, f.resolver)
			assert.Nil(t, plaintext)
			requireJOSEErrorCode(t, openErr, tt.wantCode)
		})
	}
}
