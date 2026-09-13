package main

import (
	"bytes"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	gojose "github.com/go-jose/go-jose/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	jose "github.com/gaborage/go-bricks/jose"
	jositest "github.com/gaborage/go-bricks/jose/testing"
)

// runCLI invokes run() with an in-memory stdin/stdout/stderr, for assertions
// without touching the real process streams.
func runCLI(args []string, stdin []byte) (stdout, stderr string, code int) {
	var outBuf, errBuf bytes.Buffer
	code = run(args, bytes.NewReader(stdin), &outBuf, &errBuf)
	return outBuf.String(), errBuf.String(), code
}

func derPKCS8Private(t *testing.T, priv *rsa.PrivateKey) []byte {
	t.Helper()
	der, err := x509.MarshalPKCS8PrivateKey(priv)
	require.NoError(t, err)
	return der
}

func derPKIXPublic(t *testing.T, pub *rsa.PublicKey) []byte {
	t.Helper()
	der, err := x509.MarshalPKIXPublicKey(pub)
	require.NoError(t, err)
	return der
}

func writeFile(t *testing.T, dir, name string, data []byte) string {
	t.Helper()
	path := filepath.Join(dir, name)
	require.NoError(t, os.WriteFile(path, data, 0o600))
	return path
}

// Fixed kid strings for TestSealPayloadRoundTrip's subtests — kid BINDING
// itself is pinned separately by TestSealPayloadKidBinding.
const (
	testSignKid = "sig-kid"
	testEncKid  = "enc-kid"
)

// openErr discards the success-path return values from jose.Open and returns
// only the error, for this file's one negative-case assertion.
func openErr(compact string, p *jose.Policy, r jose.KeyResolver) error {
	plaintext, claims, hdr, err := jose.Open(compact, p, r)
	_ = plaintext
	_ = claims
	_ = hdr
	return err
}

// cliFixture is the shared per-test key setup: one signing pair and one
// encryption pair, with their DER halves written where the CLI flags expect
// them (signing PRIVATE key file, encryption PUBLIC key file).
type cliFixture struct {
	signPriv *rsa.PrivateKey
	signPub  *rsa.PublicKey
	encPriv  *rsa.PrivateKey
	signPath string
	encPath  string
	dir      string
}

func newCLIFixture(t *testing.T) *cliFixture {
	t.Helper()
	signPriv, signPub := jositest.GenerateTestKeyPair(t)
	encPriv, encPub := jositest.GenerateTestKeyPair(t)
	dir := t.TempDir()
	return &cliFixture{
		signPriv: signPriv,
		signPub:  signPub,
		encPriv:  encPriv,
		signPath: writeFile(t, dir, "sign.der", derPKCS8Private(t, signPriv)),
		encPath:  writeFile(t, dir, "enc.pub.der", derPKIXPublic(t, encPub)),
		dir:      dir,
	}
}

// TestSealPayloadRoundTrip is the load-bearing test: it seals via the CLI's
// run() and recovers the plaintext through the real jose.Open path, proving
// a CLI-sealed token is one the middleware accepts.
func TestSealPayloadRoundTrip(t *testing.T) {
	t.Run("file_sources_stdin_payload", func(t *testing.T) {
		fx := newCLIFixture(t)

		payload := []byte(`{"hello":"world"}`)
		stdout, stderr, code := runCLI([]string{
			"-sign-key-file", fx.signPath,
			"-encrypt-key-file", fx.encPath,
			"-sign-kid", testSignKid,
			"-encrypt-kid", testEncKid,
		}, payload)
		require.Equal(t, 0, code, "stderr: %s", stderr)

		assertRoundTrip(t, stdout, payload, fx.signPub, fx.encPriv, jose.DefaultSigAlg)
	})

	t.Run("base64_value_sources", func(t *testing.T) {
		fx := newCLIFixture(t)
		signVal := base64.StdEncoding.EncodeToString(derPKCS8Private(t, fx.signPriv))
		encVal := base64.StdEncoding.EncodeToString(derPKIXPublic(t, &fx.encPriv.PublicKey))

		payload := []byte(`{"a":1}`)
		stdout, stderr, code := runCLI([]string{
			"-sign-key-value", signVal,
			"-encrypt-key-value", encVal,
			"-sign-kid", testSignKid,
			"-encrypt-kid", testEncKid,
		}, payload)
		require.Equal(t, 0, code, "stderr: %s", stderr)

		assertRoundTrip(t, stdout, payload, fx.signPub, fx.encPriv, jose.DefaultSigAlg)
	})

	t.Run("payload_from_file", func(t *testing.T) {
		fx := newCLIFixture(t)
		payload := []byte(`{"positional":true}`)
		payloadPath := writeFile(t, fx.dir, "payload.json", payload)

		stdout, stderr, code := runCLI([]string{
			"-sign-key-file", fx.signPath,
			"-encrypt-key-file", fx.encPath,
			"-sign-kid", testSignKid,
			"-encrypt-kid", testEncKid,
			payloadPath,
		}, nil)
		require.Equal(t, 0, code, "stderr: %s", stderr)

		assertRoundTrip(t, stdout, payload, fx.signPub, fx.encPriv, jose.DefaultSigAlg)
	})

	t.Run("ps256_sig_alg", func(t *testing.T) {
		fx := newCLIFixture(t)

		payload := []byte(`{"alg":"ps256"}`)
		stdout, stderr, code := runCLI([]string{
			"-sign-key-file", fx.signPath,
			"-encrypt-key-file", fx.encPath,
			"-sign-kid", testSignKid,
			"-encrypt-kid", testEncKid,
			"-sig-alg", "PS256",
		}, payload)
		require.Equal(t, 0, code, "stderr: %s", stderr)

		assertRoundTrip(t, stdout, payload, fx.signPub, fx.encPriv, gojose.PS256)
	})
}

// assertRoundTrip opens a CLI-sealed compact token through the real jose.Open
// path. The mirror resolver holds the INVERSE material of the CLI's own key
// wiring: the encryption PRIVATE half (to decrypt what the CLI encrypted to
// the public half) and the signing PUBLIC half (to verify what the CLI
// signed with the private half).
func assertRoundTrip(t *testing.T, stdout string, wantPayload []byte, signPub *rsa.PublicKey, encPriv *rsa.PrivateKey, sigAlg gojose.SignatureAlgorithm) {
	t.Helper()
	compact := strings.TrimSpace(stdout)
	mirror := &jose.Policy{
		Direction:  jose.DirectionInbound,
		DecryptKid: testEncKid,  // JWE was encrypted TO this kid
		VerifyKid:  testSignKid, // JWS was signed BY this kid
		SigAlg:     sigAlg, KeyAlg: jose.DefaultKeyAlg,
		Enc: jose.DefaultEnc, Cty: jose.DefaultCty,
	}
	resolver := jositest.NewTestResolver(map[string]any{
		testEncKid:  encPriv, // *rsa.PrivateKey — registers private+public
		testSignKid: signPub, // *rsa.PublicKey  — public only
	})
	plaintext, _ := jositest.OpenForTest(t, compact, mirror, resolver)
	assert.Equal(t, wantPayload, plaintext)
}

// TestSealPayloadKidBinding pins that the emitted token's kid HEADERS bind at
// BOTH layers, distinguishing header-mismatch from resolver-miss: in every
// case the resolver holds the CORRECT keys under the policy's kid names, so
// key resolution succeeds and the failure can only come from comparing the
// token's own header kid.
func TestSealPayloadKidBinding(t *testing.T) {
	fx := newCLIFixture(t)

	payload := []byte(`{"k":"v"}`)
	stdout, stderr, code := runCLI([]string{
		"-sign-key-file", fx.signPath,
		"-encrypt-key-file", fx.encPath,
		"-sign-kid", "sigA",
		"-encrypt-kid", "encB",
	}, payload)
	require.Equal(t, 0, code, "stderr: %s", stderr)
	compact := strings.TrimSpace(stdout)

	assertKidMismatch := func(t *testing.T, mirror *jose.Policy, resolver jose.KeyResolver, wantTokenKid string) {
		t.Helper()
		err := openErr(compact, mirror, resolver)
		require.Error(t, err)
		require.ErrorIs(t, err, jose.ErrKidUnknown)
		var joseErr *jose.Error
		require.ErrorAs(t, err, &joseErr)
		// The TOKEN's header kid, not the policy's — proves the token was
		// parsed and its header compared, not merely that resolution against
		// the policy kid failed.
		assert.Equal(t, wantTokenKid, joseErr.Kid)
	}

	t.Run("outer_jwe_kid", func(t *testing.T) {
		mirror := &jose.Policy{
			Direction:  jose.DirectionInbound,
			DecryptKid: "encD",
			VerifyKid:  "sigC",
			SigAlg:     jose.DefaultSigAlg, KeyAlg: jose.DefaultKeyAlg,
			Enc: jose.DefaultEnc, Cty: jose.DefaultCty,
		}
		resolver := jositest.NewTestResolver(map[string]any{
			"encD": fx.encPriv,
			"sigC": fx.signPub,
		})
		assertKidMismatch(t, mirror, resolver, "encB")
	})

	t.Run("inner_jws_kid", func(t *testing.T) {
		// Correct JWE kid (decryption succeeds), wrong JWS kid — the failure
		// must come from the INNER signature layer and carry the token's JWS
		// header kid.
		mirror := &jose.Policy{
			Direction:  jose.DirectionInbound,
			DecryptKid: "encB",
			VerifyKid:  "sigC",
			SigAlg:     jose.DefaultSigAlg, KeyAlg: jose.DefaultKeyAlg,
			Enc: jose.DefaultEnc, Cty: jose.DefaultCty,
		}
		resolver := jositest.NewTestResolver(map[string]any{
			"encB": fx.encPriv,
			"sigC": fx.signPub,
		})
		assertKidMismatch(t, mirror, resolver, "sigA")
	})
}

// TestSealPayloadRejections exercises the CLI's error paths, each asserting
// both the exit code and a distinguishing stderr fragment.
func TestSealPayloadRejections(t *testing.T) {
	t.Run("both_file_and_value_set", func(t *testing.T) {
		fx := newCLIFixture(t)
		signVal := base64.StdEncoding.EncodeToString(derPKCS8Private(t, fx.signPriv))

		// Both sides carry VALID material for the SAME key: with the
		// exactly-one-of check deleted, file wins and sealing SUCCEEDS,
		// flipping this test's expected exit 1 to 0.
		_, stderr, code := runCLI([]string{
			"-sign-key-file", fx.signPath,
			"-sign-key-value", signVal,
			"-encrypt-key-file", fx.encPath,
			"-sign-kid", "sig-kid",
			"-encrypt-kid", "enc-kid",
		}, []byte(`{}`))
		require.Equal(t, 1, code)
		assert.Contains(t, stderr, "-sign-key-file")
		assert.Contains(t, stderr, "-sign-key-value")
	})

	t.Run("missing_sign_kid", func(t *testing.T) {
		fx := newCLIFixture(t)

		_, stderr, code := runCLI([]string{
			"-sign-key-file", fx.signPath,
			"-encrypt-key-file", fx.encPath,
			"-encrypt-kid", "enc-kid",
		}, []byte(`{}`))
		require.Equal(t, 1, code)
		assert.Contains(t, stderr, "-sign-kid")
	})

	t.Run("disallowed_sig_alg", func(t *testing.T) {
		fx := newCLIFixture(t)

		// Assert the CODE, not just the exit status: without Policy.Validate,
		// go-jose's own ES256-vs-RSA-key failure surfaces as
		// JOSE_OUTBOUND_FAILED — a different code — so a bare exit-code
		// assertion would survive that mutation.
		_, stderr, code := runCLI([]string{
			"-sign-key-file", fx.signPath,
			"-encrypt-key-file", fx.encPath,
			"-sign-kid", "sig-kid",
			"-encrypt-kid", "enc-kid",
			"-sig-alg", "ES256",
		}, []byte(`{}`))
		require.Equal(t, 1, code)
		assert.Contains(t, stderr, "JOSE_ALGORITHM_DISALLOWED")
	})

	t.Run("bad_payload_file", func(t *testing.T) {
		fx := newCLIFixture(t)

		_, stderr, code := runCLI([]string{
			"-sign-key-file", fx.signPath,
			"-encrypt-key-file", fx.encPath,
			"-sign-kid", "sig-kid",
			"-encrypt-kid", "enc-kid",
			filepath.Join(fx.dir, "does-not-exist.json"),
		}, nil)
		require.Equal(t, 1, code)
		assert.Contains(t, stderr, "read payload file")
	})

	t.Run("garbage_key_der", func(t *testing.T) {
		fx := newCLIFixture(t)
		garbagePath := writeFile(t, fx.dir, "garbage.der", []byte{0x00, 0x01, 0x02})

		_, stderr, code := runCLI([]string{
			"-sign-key-file", garbagePath,
			"-encrypt-key-file", fx.encPath,
			"-sign-kid", "sig-kid",
			"-encrypt-kid", "enc-kid",
		}, []byte(`{}`))
		require.Equal(t, 1, code)
		assert.Contains(t, stderr, "sign key")
	})

	t.Run("help_exits_zero", func(t *testing.T) {
		_, _, code := runCLI([]string{"-h"}, nil)
		require.Equal(t, 0, code)
	})

	t.Run("extra_positional_args", func(t *testing.T) {
		_, stderr, code := runCLI([]string{"a.json", "b.json"}, nil)
		require.Equal(t, 2, code)
		assert.Contains(t, stderr, "at most one payload-file")
	})

	t.Run("unknown_mode", func(t *testing.T) {
		fx := newCLIFixture(t)
		_, stderr, code := runCLI([]string{"-mode", "flat", "-encrypt-key-file", fx.encPath, "-encrypt-kid", testEncKid}, []byte(`{}`))
		require.Equal(t, 1, code)
		assert.Contains(t, stderr, `-mode "flat"`)
	})
}

// bareArgs are the minimal bare-mode flags: an encryption key and kid, nothing signed.
func bareArgs(fx *cliFixture, extra ...string) []string {
	return append([]string{
		"-mode", "bare",
		"-encrypt-key-file", fx.encPath,
		"-encrypt-kid", testEncKid,
	}, extra...)
}

// nestedArgs are the minimal default-mode flags: both key sources and both kids.
func nestedArgs(fx *cliFixture, extra ...string) []string {
	return append([]string{
		"-sign-key-file", fx.signPath,
		"-encrypt-key-file", fx.encPath,
		"-sign-kid", testSignKid,
		"-encrypt-kid", testEncKid,
	}, extra...)
}

// openBare opens a CLI-sealed bare JWE through jose.Open with the inverse
// material: the encryption PRIVATE half under the kid the CLI encrypted to.
func openBare(t *testing.T, compact string, encPriv *rsa.PrivateKey, enc gojose.ContentEncryption) ([]byte, jose.OpenHeader) {
	t.Helper()
	mirror := &jose.Policy{
		Mode:       jose.SealModeBareJWE,
		Direction:  jose.DirectionInbound,
		DecryptKid: testEncKid,
		KeyAlg:     jose.DefaultKeyAlg,
		Enc:        enc,
		Cty:        jose.DefaultCty,
	}
	resolver := jositest.NewTestResolver(map[string]any{testEncKid: encPriv})
	plaintext, _, hdr, err := jose.Open(compact, mirror, resolver)
	require.NoError(t, err)
	return plaintext, hdr
}

// protectedHeader decodes the first compact segment into its JSON members.
func protectedHeader(t *testing.T, compact string) map[string]any {
	t.Helper()
	seg, _, _ := strings.Cut(compact, ".")
	raw, err := base64.RawURLEncoding.DecodeString(seg)
	require.NoError(t, err)
	var hdr map[string]any
	require.NoError(t, json.Unmarshal(raw, &hdr))
	return hdr
}

// TestSealPayloadBareMode pins that -mode bare emits a single five-segment JWE
// whose plaintext IS the payload: opening it as a bare JWE succeeds, which a
// nested token (cty=JWS) never does.
func TestSealPayloadBareMode(t *testing.T) {
	fx := newCLIFixture(t)
	payload := []byte(`{"bare":true}`)

	stdout, stderr, code := runCLI(bareArgs(fx), payload)
	require.Equal(t, 0, code, "stderr: %s", stderr)

	compact := strings.TrimSuffix(stdout, "\n")
	assert.Len(t, strings.Split(compact, "."), 5)
	plaintext, hdr := openBare(t, compact, fx.encPriv, jose.DefaultEnc)
	assert.Equal(t, payload, plaintext)
	assert.Equal(t, testEncKid, hdr.JWE.Kid)
	assert.Equal(t, jose.Header{}, hdr.JWS, "bare mode has no inner JWS")
}

// TestSealPayloadBareRefusesSigningMaterial pins that every signing input is a
// hard, named error under -mode bare. Each case is otherwise sealable, so
// deleting its check flips the exit code or loses the flag name from stderr.
func TestSealPayloadBareRefusesSigningMaterial(t *testing.T) {
	fx := newCLIFixture(t)
	signVal := base64.StdEncoding.EncodeToString(derPKCS8Private(t, fx.signPriv))

	cases := []struct {
		name  string
		extra []string
		flag  string
	}{
		{"sign_key_file", []string{"-sign-key-file", fx.signPath}, "-sign-key-file"},
		{"sign_key_value", []string{"-sign-key-value", signVal}, "-sign-key-value"},
		{"sign_kid", []string{"-sign-kid", testSignKid}, "-sign-kid"},
		{"sig_alg", []string{"-sig-alg", "PS256"}, "-sig-alg"},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			stdout, stderr, code := runCLI(bareArgs(fx, tt.extra...), []byte(`{}`))
			require.Equal(t, 1, code, "stdout: %s", stdout)
			assert.Empty(t, stdout)
			assert.Contains(t, stderr, tt.flag+" is not accepted with -mode bare")
		})
	}
}

// TestSealPayloadEnc pins -enc against each mode's allowlist: A128GCM seals
// under bare, and nested refuses it naming the nested allowed set.
func TestSealPayloadEnc(t *testing.T) {
	fx := newCLIFixture(t)

	t.Run("a128gcm_under_bare", func(t *testing.T) {
		payload := []byte(`{"enc":"a128"}`)

		stdout, stderr, code := runCLI(bareArgs(fx, "-enc", "A128GCM"), payload)
		require.Equal(t, 0, code, "stderr: %s", stderr)

		compact := strings.TrimSuffix(stdout, "\n")
		assert.Equal(t, "A128GCM", protectedHeader(t, compact)["enc"])
		plaintext, _ := openBare(t, compact, fx.encPriv, gojose.A128GCM)
		assert.Equal(t, payload, plaintext)
	})

	t.Run("a128gcm_under_nested", func(t *testing.T) {
		stdout, stderr, code := runCLI(nestedArgs(fx, "-enc", "A128GCM"), []byte(`{}`))
		require.Equal(t, 1, code)
		assert.Empty(t, stdout)
		assert.Contains(t, stderr, `-enc "A128GCM" is not allowed with -mode nested (allowed: [A256GCM])`)
	})
}

// TestSealPayloadDefaultModeShape guards the default invocation: with no
// -mode it still emits the nested token, whose outer protected header holds
// exactly alg, enc, kid and cty=JWS and nothing bare mode adds.
func TestSealPayloadDefaultModeShape(t *testing.T) {
	fx := newCLIFixture(t)
	stdout, stderr, code := runCLI(nestedArgs(fx), []byte(`{}`))
	require.Equal(t, 0, code, "stderr: %s", stderr)
	require.True(t, strings.HasSuffix(stdout, "\n"))

	assert.Equal(t, map[string]any{
		"alg": "RSA-OAEP-256",
		"enc": "A256GCM",
		"kid": testEncKid,
		"cty": "JWS",
	}, protectedHeader(t, strings.TrimSuffix(stdout, "\n")))
}

// TestSealPayloadHelp pins that -h documents every mode and bare-outbound flag.
func TestSealPayloadHelp(t *testing.T) {
	_, stderr, code := runCLI([]string{"-h"}, nil)
	require.Equal(t, 0, code)
	for _, flagName := range []string{"-mode", "-enc"} {
		assert.Regexp(t, `(?m)^  `+flagName+`( |$)`, stderr, "help omits %s", flagName)
	}
	assert.Contains(t, stderr, "nested allows [A256GCM], bare allows [A128GCM A256GCM]")
	assert.Contains(t, stderr, "nested default RS256")
	assert.Regexp(t, `(?m)^  -sign-kid string\n\s+.*required with -mode nested; refused with -mode bare`, stderr)
	assert.Regexp(t, `(?m)^  -sign-key-file string\n\s+.*used to sign the outbound JWS \(nested mode only\)`, stderr)
	assert.NotRegexp(t, `(?m)^  -sign-kid string\n\s+.*\(required\)$`, stderr)
}
