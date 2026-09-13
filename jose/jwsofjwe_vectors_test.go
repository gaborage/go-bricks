package jose

import (
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"os"
	"strings"
	"testing"

	jose "github.com/go-jose/go-jose/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---- published JWS-of-JWE vectors ----
//
// The tokens in testdata/jwsofjwe_vectors.json are produced by go-jose DIRECTLY (see
// buildJWSofJWEVector), never by Seal, so they are an oracle independent of the code under
// test. They share testdata/keys.json with the bare vectors. Regenerate both with:
// go test ./jose -update

const (
	jwsVecFile = "testdata/jwsofjwe_vectors.json"
	vecIATSecs = vecIATMs / 1000
)

// jwsVectorNote travels inside the fixture file so its provenance is readable in place.
const jwsVectorNote = "Generated test material for the jose JWS-of-JWE vectors (go test ./jose -update): " +
	"tokens signed and encrypted under the disposable keys in keys.json. Never provisioned anywhere; nothing to rotate."

// jwsOfJWEVector is one published token plus the verdict the opener must reach.
type jwsOfJWEVector struct {
	Name      string `json:"name"`
	Code      string `json:"code"` // "" for a vector that must open
	Plaintext string `json:"plaintext,omitempty"`
	Compact   string `json:"compact"`
}

type jwsOfJWEVectorFile struct {
	Note    string           `json:"note"`
	Vectors []jwsOfJWEVector `json:"vectors"`
}

// buildJWSofJWEVector encrypts then signs with go-jose directly, so headers Seal would
// never write (a missing cty, RS256, a rogue signer) can be produced.
func buildJWSofJWEVector(t *testing.T, name string, keys map[string]*rsa.PrivateKey) string {
	t.Helper()
	encOpts := (&jose.EncrypterOptions{
		ExtraHeaders: map[jose.HeaderKey]any{jose.HeaderKey("iat"): vecIATMs},
	}).WithType("JOSE")
	encrypter, err := jose.NewEncrypter(jose.A256GCM,
		jose.Recipient{Algorithm: jose.RSA_OAEP_256, Key: &keys[vecEncKid].PublicKey, KeyID: vecEncKid}, encOpts)
	require.NoError(t, err)
	obj, err := encrypter.Encrypt([]byte(vecPlain))
	require.NoError(t, err)
	inner, err := obj.CompactSerialize()
	require.NoError(t, err)

	if name == "jwe_outer_body" {
		return inner
	}
	if name == "tampered_inner_ciphertext" {
		inner = tamperSegment(t, inner, 3)
	}

	signKid, sigAlg := vecEncKid, jose.PS256
	switch name {
	case "rogue_signing_kid":
		signKid = vecRogueKid
	case "outer_rs256":
		sigAlg = jose.RS256
	}

	signOpts := (&jose.SignerOptions{
		ExtraHeaders: map[jose.HeaderKey]any{jose.HeaderKey("iat"): vecIATSecs},
	}).WithType("JOSE")
	if name != "outer_kid_missing" {
		signOpts = signOpts.WithHeader(jose.HeaderKey("kid"), signKid)
	}
	switch name {
	case "outer_cty_missing":
	case "outer_cty_jws":
		signOpts = signOpts.WithContentType("JWS")
	default:
		signOpts = signOpts.WithContentType("JWE")
	}
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: sigAlg, Key: keys[signKid]}, signOpts)
	require.NoError(t, err)
	signed, err := signer.Sign([]byte(inner))
	require.NoError(t, err)
	compact, err := signed.CompactSerialize()
	require.NoError(t, err)
	return compact
}

// tamperSegment flips one base64url character of the given compact segment.
func tamperSegment(t *testing.T, compact string, segment int) string {
	t.Helper()
	parts := strings.Split(compact, ".")
	require.Greater(t, len(parts), segment)
	s := parts[segment]
	require.NotEmpty(t, s)
	flip := byte('A')
	if s[0] == 'A' {
		flip = 'B'
	}
	parts[segment] = string(flip) + s[1:]
	return strings.Join(parts, ".")
}

func regenerateJWSofJWEVectors(t *testing.T, keys map[string]*rsa.PrivateKey) []jwsOfJWEVector {
	t.Helper()
	vectors := []jwsOfJWEVector{
		{Name: "jwsofjwe_ps256", Plaintext: vecPlain},
		{Name: "outer_cty_missing", Code: codeCtyRejected},
		{Name: "outer_cty_jws", Code: codeCtyRejected},
		{Name: "outer_kid_missing", Code: codeKidMissing},
		{Name: "outer_rs256", Code: codeAlgorithmDisallowed},
		{Name: "rogue_signing_kid", Code: codeKidUnknown},
		{Name: "tampered_inner_ciphertext", Code: codeDecryptFailed},
		{Name: "jwe_outer_body", Code: codeOuterNotJWS},
	}
	for i := range vectors {
		vectors[i].Compact = buildJWSofJWEVector(t, vectors[i].Name, keys)
	}
	raw, err := json.MarshalIndent(jwsOfJWEVectorFile{Note: jwsVectorNote, Vectors: vectors}, "", "  ")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(jwsVecFile, append(raw, '\n'), 0o600))
	return vectors
}

// assertPSSSalt32 verifies the outer signature with an exact 32-byte salt length, the
// value nimbus-jose-jwt and go-jose both produce. go-jose's own verify auto-detects the
// salt, so only this check can fail if the salt length ever moves.
func assertPSSSalt32(t *testing.T, compact string, pub *rsa.PublicKey) {
	t.Helper()
	parts := strings.Split(compact, ".")
	require.Len(t, parts, 3)
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	require.NoError(t, err)
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	assert.NoError(t, rsa.VerifyPSS(pub, crypto.SHA256, digest[:], sig,
		&rsa.PSSOptions{SaltLength: 32, Hash: crypto.SHA256}))
}

func TestOpenJWSofJWEVectors(t *testing.T) {
	keys := loadVectorKeys(t)
	var file jwsOfJWEVectorFile
	if *updateVectors {
		file.Vectors = regenerateJWSofJWEVectors(t, keys)
	} else {
		raw, err := os.ReadFile(jwsVecFile)
		require.NoError(t, err)
		require.NoError(t, json.Unmarshal(raw, &file))
	}
	require.NotEmpty(t, file.Vectors)

	resolver := &fixtureResolver{
		priv: map[string]*rsa.PrivateKey{vecEncKid: keys[vecEncKid]},
		pub:  map[string]*rsa.PublicKey{vecEncKid: &keys[vecEncKid].PublicKey},
	}
	inbound := &Policy{
		Direction: DirectionInbound, Mode: SealModeJWSofJWE,
		DecryptKid: vecEncKid, VerifyKid: vecEncKid,
		SigAlg: jose.PS256, KeyAlg: DefaultKeyAlg, Enc: jose.A256GCM,
	}
	require.NoError(t, inbound.Validate())

	for _, v := range file.Vectors {
		t.Run(v.Name, func(t *testing.T) {
			plaintext, claims, hdr, err := Open(v.Compact, inbound, resolver)
			if v.Code != "" {
				require.Error(t, err)
				requireJOSEErrorCode(t, err, v.Code)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, v.Plaintext, string(plaintext))
			require.NotNil(t, claims)
			assert.Equal(t, "cardholder-9", claims.Subject)
			assert.Equal(t, Header{Kid: vecEncKid, Alg: "PS256", Cty: "JWE", Typ: "JOSE"}, hdr.JWS)
			assert.Equal(t, Header{
				Kid: vecEncKid, Alg: "RSA-OAEP-256", Enc: "A256GCM", Typ: "JOSE", IATMillis: vecIATMs,
			}, hdr.JWE)
			assertPSSSalt32(t, v.Compact, &keys[vecEncKid].PublicKey)
		})
	}
}
