package main

import (
	"bytes"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"errors"
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
		require.True(t, errors.Is(err, jose.ErrKidUnknown))
		var joseErr *jose.Error
		require.True(t, errors.As(err, &joseErr))
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
}
