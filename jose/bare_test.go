package jose

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"os"
	"sync"
	"testing"

	jose "github.com/go-jose/go-jose/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/jose/internal/cryptoadapter"
)

// bareFixture holds one key pair plus the matching bare-mode policies. The kid namespace
// matches newTestFixture's so the two fixtures read alike.
type bareFixture struct {
	ourPriv  *rsa.PrivateKey
	resolver *fixtureResolver
	outbound *Policy
	inbound  *Policy
}

// bareKeys is the one key pair every test in this file shares. Keys are read-only here, so
// generating a fresh 2048-bit pair per test would only cost wall-clock time.
var bareKeys = sync.OnceValues(func() (*rsa.PrivateKey, *rsa.PublicKey) {
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		panic(err)
	}
	return priv, &priv.PublicKey
})

func newBareFixture(t *testing.T) *bareFixture {
	t.Helper()
	ourPriv, ourPub := bareKeys()
	outbound, inbound := bareOutbound(), bareInbound()
	outbound.EncryptKid = "our-key"
	inbound.DecryptKid = "our-key"
	return &bareFixture{
		ourPriv: ourPriv,
		resolver: &fixtureResolver{
			priv: map[string]*rsa.PrivateKey{"our-key": ourPriv},
			pub:  map[string]*rsa.PublicKey{"our-key": ourPub},
		},
		outbound: outbound,
		inbound:  inbound,
	}
}

// peekHeader reads the protected header straight off the wire, so the assertions see what
// was serialized rather than anything the package kept.
func peekHeader(t *testing.T, compact string) cryptoadapter.Header {
	t.Helper()
	hdr, err := cryptoadapter.PeekProtectedHeader(compact)
	require.NoError(t, err)
	return hdr
}

// decryptWithGoJose decrypts a compact JWE with go-jose directly — an oracle independent
// of the package's own Open path.
func decryptWithGoJose(t *testing.T, compact string, key *rsa.PrivateKey, enc jose.ContentEncryption) []byte {
	t.Helper()
	obj, err := jose.ParseEncrypted(compact,
		[]jose.KeyAlgorithm{jose.RSA_OAEP_256},
		[]jose.ContentEncryption{enc})
	require.NoError(t, err)
	plaintext, err := obj.Decrypt(key)
	require.NoError(t, err)
	return plaintext
}

// headerIAT reads the wire iat as an integer.
func headerIAT(t *testing.T, compact string) int64 {
	t.Helper()
	hdr := peekHeader(t, compact)
	iat, err := hdr.ExtraInt64("iat")
	require.NoError(t, err)
	return iat
}

func TestSealBareJWEWritesProtectedHeaders(t *testing.T) {
	f := newBareFixture(t)
	f.outbound.Typ = "JOSE"
	f.outbound.ProtectedHeaders = map[string]any{"iss": "acme-payments"}
	f.outbound.IATMillis = true
	payload := []byte(`{"pan":"4111111111111111"}`)

	compact, err := Seal(payload, f.outbound, f.resolver)
	require.NoError(t, err)

	hdr := peekHeader(t, compact)
	assert.Equal(t, "RSA-OAEP-256", hdr.Alg)
	assert.Equal(t, "A128GCM", hdr.Enc)
	assert.Equal(t, "our-key", hdr.Kid)
	assert.Equal(t, "JOSE", hdr.Typ)
	iss, ok := hdr.ExtraString("iss")
	assert.True(t, ok)
	assert.Equal(t, "acme-payments", iss)
	assert.Empty(t, hdr.Cty, "an unset policy Cty must leave cty off the wire")

	// Milliseconds, not seconds: 2001-09-09 in seconds is 1e9, in milliseconds 1e12.
	iat := headerIAT(t, compact)
	assert.Greater(t, iat, int64(1_600_000_000_000))
	assert.Less(t, iat, int64(100_000_000_000_000))

	// No inner JWS: the ciphertext holds the caller's bytes verbatim.
	assert.Equal(t, payload, decryptWithGoJose(t, compact, f.ourPriv, jose.A128GCM))
}

func TestSealBareJWEWritesCtyWhenPolicySetsIt(t *testing.T) {
	f := newBareFixture(t)
	f.outbound.Cty = DefaultCty

	compact, err := Seal([]byte(`{}`), f.outbound, f.resolver)
	require.NoError(t, err)
	assert.Equal(t, DefaultCty, peekHeader(t, compact).Cty)
}

func TestSealBareJWEOmitsIATWhenNotStamping(t *testing.T) {
	f := newBareFixture(t)

	compact, err := Seal([]byte(`{}`), f.outbound, f.resolver)
	require.NoError(t, err)
	hdr := peekHeader(t, compact)
	assert.Empty(t, hdr.Typ)
	_, err = hdr.ExtraInt64("iat")
	assert.ErrorIs(t, err, cryptoadapter.ErrExtraAbsent)
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
	assert.Equal(t, headerIAT(t, compact), hdr.JWE.IATMillis)

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
	f.outbound.Enc = jose.A256GCM
	f.inbound.Enc = jose.A256GCM
	payload := []byte(`{"amount":1250}`)

	compact, err := Seal(payload, f.outbound, f.resolver)
	require.NoError(t, err)
	assert.Equal(t, "A256GCM", peekHeader(t, compact).Enc)

	plaintext, _, _, err := Open(compact, f.inbound, f.resolver)
	require.NoError(t, err)
	assert.Equal(t, payload, plaintext)
}

func TestSealBareJWEStampsTheClockSeamAtEachCall(t *testing.T) {
	f := newBareFixture(t)
	f.outbound.IATMillis = true

	original := nowMillis
	t.Cleanup(func() { nowMillis = original })
	stamps := []int64{1_700_000_000_123, 1_700_000_042_456}
	call := 0
	nowMillis = func() int64 {
		v := stamps[call]
		call++
		return v
	}

	first, err := Seal([]byte(`{}`), f.outbound, f.resolver)
	require.NoError(t, err)
	second, err := Seal([]byte(`{}`), f.outbound, f.resolver)
	require.NoError(t, err)

	assert.Equal(t, stamps[0], headerIAT(t, first))
	assert.Equal(t, stamps[1], headerIAT(t, second))

	_, _, hdr, err := Open(second, f.inbound, f.resolver)
	require.NoError(t, err)
	assert.Equal(t, stamps[1], hdr.JWE.IATMillis)
}

func TestSealBareJWEDoesNotMutatePolicyHeaders(t *testing.T) {
	f := newBareFixture(t)
	f.outbound.IATMillis = true
	f.outbound.ProtectedHeaders = map[string]any{"iss": "acme"}

	_, err := Seal([]byte(`{}`), f.outbound, f.resolver)
	require.NoError(t, err)
	assert.Equal(t, map[string]any{"iss": "acme"}, f.outbound.ProtectedHeaders)
}

func TestOpenBareJWECtyRule(t *testing.T) {
	tests := []struct {
		name      string
		sealCty   string
		policyCty string
		wantCode  string
	}{
		{"agreeing_cty", DefaultCty, DefaultCty, ""},
		{"peer_omits_cty", "", DefaultCty, ""},
		{"policy_omits_cty", "text/csv", "", ""},
		{"disagreeing_cty", "text/csv", DefaultCty, "JOSE_CTY_REJECTED"},
	}
	f := newBareFixture(t)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f.outbound.Cty = tt.sealCty
			compact, err := Seal([]byte(`{}`), f.outbound, f.resolver)
			require.NoError(t, err)

			f.inbound.Cty = tt.policyCty
			_, _, _, err = Open(compact, f.inbound, f.resolver)
			if tt.wantCode == "" {
				require.NoError(t, err)
				return
			}
			require.ErrorIs(t, err, ErrCtyRejected)
			requireJOSEErrorCode(t, err, tt.wantCode)
		})
	}
}

// ---- published bare-JWE vectors ----
//
// The tokens in testdata/bare_vectors.json are produced by go-jose DIRECTLY (see
// buildVectorToken), never by Seal, so they are an oracle independent of the code under
// test. Regenerate with: go test ./jose -update

var updateVectors = flag.Bool("update", false, "regenerate the published bare-JWE vectors")

const (
	vecEncKid   = "mle-enc-v1"
	vecRogueKid = "rogue"
	vecIATMs    = int64(1_800_000_000_123)
	vecIssuer   = "acme-payments"
	vecPlain    = `{"pan":"4111111111111111","sub":"cardholder-9"}`
	vecKeysFile = "testdata/keys.json"
	vecFile     = "testdata/bare_vectors.json"
)

// vectorNote travels inside both fixture files so their provenance is readable in place.
const vectorNote = "Generated test material for the jose bare-JWE vectors (go test ./jose -update): " +
	"disposable RSA keys and the tokens encrypted under them. Never provisioned anywhere; nothing to rotate."

type bareKeysFile struct {
	Note string            `json:"note"`
	Keys map[string]string `json:"keys"` // kid -> base64 PKCS#1 DER
}

type bareVectorFile struct {
	Note    string       `json:"note"`
	Vectors []bareVector `json:"vectors"`
}

// bareVector is one published token plus the verdict the opener must reach.
type bareVector struct {
	Name      string `json:"name"`
	Policy    string `json:"policy"` // "bare" or "nested"
	Code      string `json:"code"`   // "" for a vector that must open
	Enc       string `json:"enc"`
	Typ       string `json:"typ,omitempty"`
	IATMillis int64  `json:"iatMillis,omitempty"`
	Plaintext string `json:"plaintext,omitempty"`
	Token     string `json:"token"`
}

func loadVectorKeys(t *testing.T) map[string]*rsa.PrivateKey {
	t.Helper()
	file := bareKeysFile{Note: vectorNote, Keys: map[string]string{}}
	raw, err := os.ReadFile(vecKeysFile)
	if errors.Is(err, os.ErrNotExist) && *updateVectors {
		for _, kid := range []string{vecEncKid, vecRogueKid} {
			priv, _ := generateKeyPair(t)
			file.Keys[kid] = base64.StdEncoding.EncodeToString(x509.MarshalPKCS1PrivateKey(priv))
		}
		raw, err = json.MarshalIndent(file, "", "  ")
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(vecKeysFile, append(raw, '\n'), 0o600))
	}
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(raw, &file))

	keys := map[string]*rsa.PrivateKey{}
	for kid, b64 := range file.Keys {
		der, err := base64.StdEncoding.DecodeString(b64)
		require.NoError(t, err)
		keys[kid], err = x509.ParsePKCS1PrivateKey(der)
		require.NoError(t, err)
	}
	return keys
}

// buildVectorToken encrypts with go-jose directly, so headers Seal would never write
// (a rogue kid, a CBC content encryption) can be produced.
func buildVectorToken(t *testing.T, v *bareVector, pub *rsa.PublicKey, kid string) string {
	t.Helper()
	extra := map[jose.HeaderKey]any{}
	if v.IATMillis != 0 {
		extra[jose.HeaderKey("iat")] = v.IATMillis
		extra[jose.HeaderKey("iss")] = vecIssuer
	}
	opts := &jose.EncrypterOptions{ExtraHeaders: extra}
	if v.Typ != "" {
		opts = opts.WithType(jose.ContentType(v.Typ))
	}
	encrypter, err := jose.NewEncrypter(jose.ContentEncryption(v.Enc),
		jose.Recipient{Algorithm: jose.RSA_OAEP_256, Key: pub, KeyID: kid}, opts)
	require.NoError(t, err)
	obj, err := encrypter.Encrypt([]byte(v.Plaintext))
	require.NoError(t, err)
	compact, err := obj.CompactSerialize()
	require.NoError(t, err)
	return compact
}

func regenerateVectors(t *testing.T, keys map[string]*rsa.PrivateKey) []bareVector {
	t.Helper()
	vectors := []bareVector{
		{
			Name: "bare_a128gcm_with_typ_and_iat", Policy: "bare", Enc: "A128GCM",
			Typ: "JOSE", IATMillis: vecIATMs, Plaintext: vecPlain,
		},
		{Name: "bare_a256gcm", Policy: "bare", Enc: "A256GCM", Plaintext: vecPlain},
		{Name: "wrong_kid", Policy: "bare", Enc: "A128GCM", Code: codeKidUnknown, Plaintext: vecPlain},
		{
			Name: "disallowed_enc_a128cbc_hs256", Policy: "bare", Enc: "A128CBC-HS256",
			Code: codeMalformed, Plaintext: vecPlain,
		},
		{
			Name: "bare_token_on_nested_policy", Policy: "nested", Enc: "A128GCM",
			Code: codeMalformed, Plaintext: vecPlain,
		},
	}
	for i := range vectors {
		kid := vecEncKid
		if vectors[i].Name == "wrong_kid" {
			kid = vecRogueKid
		}
		vectors[i].Token = buildVectorToken(t, &vectors[i], &keys[kid].PublicKey, kid)
	}
	raw, err := json.MarshalIndent(bareVectorFile{Note: vectorNote, Vectors: vectors}, "", "  ")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(vecFile, append(raw, '\n'), 0o600))
	return vectors
}

func TestOpenBareJWEVectors(t *testing.T) {
	keys := loadVectorKeys(t)
	var file bareVectorFile
	if *updateVectors {
		file.Vectors = regenerateVectors(t, keys)
	} else {
		raw, err := os.ReadFile(vecFile)
		require.NoError(t, err)
		require.NoError(t, json.Unmarshal(raw, &file))
	}
	require.NotEmpty(t, file.Vectors)

	resolver := &fixtureResolver{
		priv: map[string]*rsa.PrivateKey{vecEncKid: keys[vecEncKid]},
		pub:  map[string]*rsa.PublicKey{vecRogueKid: &keys[vecRogueKid].PublicKey},
	}
	bare := &Policy{
		Direction: DirectionInbound, Mode: SealModeBareJWE,
		DecryptKid: vecEncKid, KeyAlg: DefaultKeyAlg, Enc: jose.A128GCM,
	}
	nested := &Policy{
		Direction: DirectionInbound, DecryptKid: vecEncKid, VerifyKid: vecRogueKid,
		SigAlg: DefaultSigAlg, KeyAlg: DefaultKeyAlg, Enc: DefaultEnc,
	}
	require.NoError(t, bare.Validate())
	require.NoError(t, nested.Validate())

	for _, v := range file.Vectors {
		t.Run(v.Name, func(t *testing.T) {
			p := bare
			if v.Policy == "nested" {
				p = nested
			}
			plaintext, claims, hdr, err := Open(v.Token, p, resolver)
			if v.Code != "" {
				require.Error(t, err)
				requireJOSEErrorCode(t, err, v.Code)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, v.Plaintext, string(plaintext))
			require.NotNil(t, claims)
			assert.Equal(t, "cardholder-9", claims.Subject)
			assert.Equal(t, v.Enc, hdr.JWE.Enc)
			assert.Equal(t, vecEncKid, hdr.JWE.Kid)
			assert.Equal(t, v.Typ, hdr.JWE.Typ)
			assert.Equal(t, v.IATMillis, hdr.JWE.IATMillis)
			assert.Equal(t, Header{}, hdr.JWS)
		})
	}
}

// TestOpenBareJWERejectsNestedTokenUnderDeclaredCty covers the misconfiguration where a
// bare policy is pointed at a peer that still sends the nested shape: the JWE decrypts,
// but its cty=JWS disagrees with the policy's, so the JWS never reaches the caller as if
// it were the plaintext.
func TestOpenBareJWERejectsNestedTokenUnderDeclaredCty(t *testing.T) {
	nestedFixture := newTestFixture(t)
	compact, err := Seal([]byte(`{"pan":"4111111111111111"}`), nestedFixture.outbound, nestedFixture.resolver)
	require.NoError(t, err)

	bare := &Policy{
		Direction: DirectionInbound, Mode: SealModeBareJWE,
		DecryptKid: "our-key", KeyAlg: DefaultKeyAlg, Enc: DefaultEnc, Cty: DefaultCty,
	}
	require.NoError(t, bare.Validate())

	_, _, hdr, err := Open(compact, bare, nestedFixture.resolver)
	require.ErrorIs(t, err, ErrCtyRejected)
	assert.Equal(t, "JWS", hdr.JWE.Cty)
}

// TestOpenBareJWERejectsNestedTokenWithoutDeclaredCty pins the fail-closed rule: a bare
// inbound policy refuses a JWE carrying cty=JWS even when the policy declares no Cty of
// its own, so the inner compact JWS can never surface as unverified plaintext.
func TestOpenBareJWERejectsNestedTokenWithoutDeclaredCty(t *testing.T) {
	nestedFixture := newTestFixture(t)
	compact, err := Seal([]byte(`{"pan":"4111111111111111"}`), nestedFixture.outbound, nestedFixture.resolver)
	require.NoError(t, err)

	bare := &Policy{
		Direction: DirectionInbound, Mode: SealModeBareJWE,
		DecryptKid: "our-key", KeyAlg: DefaultKeyAlg, Enc: DefaultEnc,
	}
	require.NoError(t, bare.Validate())

	plaintext, _, hdr, err := Open(compact, bare, nestedFixture.resolver)
	require.Error(t, err)
	require.ErrorIs(t, err, ErrCtyRejected)
	assert.Nil(t, plaintext)
	assert.Equal(t, "JWS", hdr.JWE.Cty)
}
