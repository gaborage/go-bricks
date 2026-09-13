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

// jwsOfJWEVector is one published token plus the verdict the opener must reach. The build
// knobs are `json:"-"`: they describe how -update produces the token, so they live in the Go
// table only and never reach the fixture file.
type jwsOfJWEVector struct {
	Name      string `json:"name"`
	Code      string `json:"code"` // "" for a vector that must open
	Plaintext string `json:"plaintext,omitempty"`
	Compact   string `json:"compact"`

	SignKid     string                  `json:"-"`
	SigAlg      jose.SignatureAlgorithm `json:"-"`
	OuterCty    string                  `json:"-"` // "" writes no cty header
	OmitKid     bool                    `json:"-"`
	TamperInner bool                    `json:"-"`
	InnerOnly   bool                    `json:"-"` // publish the bare inner JWE, unsigned
}

type jwsOfJWEVectorFile struct {
	Note    string           `json:"note"`
	Vectors []jwsOfJWEVector `json:"vectors"`
}

// buildJWSofJWEVector encrypts then signs with go-jose directly, so headers Seal would
// never write (a missing cty, RS256, a rogue signer) can be produced.
func buildJWSofJWEVector(t *testing.T, v *jwsOfJWEVector, keys map[string]*rsa.PrivateKey) string {
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

	if v.InnerOnly {
		return inner
	}
	if v.TamperInner {
		inner = tamperSegment(t, inner, 3)
	}

	signOpts := (&jose.SignerOptions{
		ExtraHeaders: map[jose.HeaderKey]any{jose.HeaderKey("iat"): vecIATSecs},
	}).WithType("JOSE")
	if !v.OmitKid {
		signOpts = signOpts.WithHeader(jose.HeaderKey("kid"), v.SignKid)
	}
	if v.OuterCty != "" {
		signOpts = signOpts.WithContentType(jose.ContentType(v.OuterCty))
	}
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: v.SigAlg, Key: keys[v.SignKid]}, signOpts)
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
	valid := jwsOfJWEVector{SignKid: vecEncKid, SigAlg: jose.PS256, OuterCty: ctyJWE}
	vector := func(name, code string, mutate func(v *jwsOfJWEVector)) jwsOfJWEVector {
		v := valid
		v.Name, v.Code = name, code
		mutate(&v)
		return v
	}
	vectors := []jwsOfJWEVector{
		vector("jwsofjwe_ps256", "", func(v *jwsOfJWEVector) { v.Plaintext = vecPlain }),
		vector("outer_cty_missing", codeCtyRejected, func(v *jwsOfJWEVector) { v.OuterCty = "" }),
		vector("outer_cty_jws", codeCtyRejected, func(v *jwsOfJWEVector) { v.OuterCty = ctyNestedJWS }),
		vector("outer_kid_missing", codeKidMissing, func(v *jwsOfJWEVector) { v.OmitKid = true }),
		vector("outer_rs256", codeAlgorithmDisallowed, func(v *jwsOfJWEVector) { v.SigAlg = jose.RS256 }),
		vector("rogue_signing_kid", codeKidUnknown, func(v *jwsOfJWEVector) { v.SignKid = vecRogueKid }),
		vector("tampered_inner_ciphertext", codeDecryptFailed, func(v *jwsOfJWEVector) { v.TamperInner = true }),
		vector("jwe_outer_body", codeOuterNotJWS, func(v *jwsOfJWEVector) { v.InnerOnly = true }),
	}
	for i := range vectors {
		vectors[i].Compact = buildJWSofJWEVector(t, &vectors[i], keys)
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
