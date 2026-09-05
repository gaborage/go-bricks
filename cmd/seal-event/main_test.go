package main

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/jose/sealed"
	jositest "github.com/gaborage/go-bricks/jose/testing"
)

// The kid namespace the fixtures use: Logical families as a consumer would tag them, and
// the concrete Generations the CLI is handed. Splitting one from the other in the constants
// is the point — the CLI is given the Generation and must derive the family itself.
const (
	signFamily = "svc-payments-sign"
	encFamily  = "aud-core-encrypt"
	signKid    = signFamily + "-v1"
	encKid     = encFamily + "-v1"

	testEventType = "payment.authorized"
	testTenant    = "t1"
	subjectMember = "card"
)

// docJSON is the fixture document: two clear members and one Subject object.
const docJSON = `{"order_id":"o-1","amount":100,"card":{"pan":"4111111111111111","expiry":"12/30"}}`

// cardData is the Subject's shape; paymentAuthorized is the consumer-side declaration the
// round-trip opens against. The CLI never sees either — it seals a raw document — so these
// types are exactly the consumer half of the contract the CLI must satisfy.
type cardData struct {
	PAN    string `json:"pan"`
	Expiry string `json:"expiry"`
}

type paymentAuthorized struct {
	_       struct{} `seal:"sign=svc-payments-sign,encrypt=aud-core-encrypt"`
	OrderID string   `json:"order_id"`
	Amount  int      `json:"amount"`
	Card    cardData `json:"card" seal:"subject"`
}

// runCLI invokes run() with in-memory streams, so every case goes through the same
// entry point the real binary does.
func runCLI(args []string, stdin []byte) (stdout, stderr string, code int) {
	var outBuf, errBuf bytes.Buffer
	code = run(args, bytes.NewReader(stdin), &outBuf, &errBuf)
	return outBuf.String(), errBuf.String(), code
}

// derPKCS8Private renders a private key in the PKCS#8 DER form the -sign-key flags take.
func derPKCS8Private(t *testing.T, priv *rsa.PrivateKey) []byte {
	t.Helper()
	der, err := x509.MarshalPKCS8PrivateKey(priv)
	require.NoError(t, err)
	return der
}

// derPKIXPublic renders a public key in the PKIX DER form the -encrypt-key flags take.
func derPKIXPublic(t *testing.T, pub *rsa.PublicKey) []byte {
	t.Helper()
	der, err := x509.MarshalPKIXPublicKey(pub)
	require.NoError(t, err)
	return der
}

// writeFile drops data into dir under name and returns the path, for the file-source flags.
func writeFile(t *testing.T, dir, name string, data []byte) string {
	t.Helper()
	path := filepath.Join(dir, name)
	require.NoError(t, os.WriteFile(path, data, 0o600))
	return path
}

// cliFixture holds the producer-role material the CLI flags expect (sign PRIVATE, encrypt
// PUBLIC) plus the consumer's mirror halves used to open what the CLI emitted.
type cliFixture struct {
	signPub  *rsa.PublicKey
	signPriv *rsa.PrivateKey
	encPriv  *rsa.PrivateKey
	signPath string
	encPath  string
	dir      string
}

// sharedKeys mints the two RSA pairs once for the whole package. RSA-2048 generation is
// ~37ms and the suite builds a fixture per subtest, so per-test pairs cost ~2s of the ~3s
// run for no isolation gain: no test mutates a key, and what each test does need to itself
// — its key FILES and its temp dir — still comes fresh from t.TempDir() below.
var sharedKeys = sync.OnceValue(func() *keyPairs {
	signPriv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		panic("seal-event test: generate sign key: " + err.Error())
	}
	encPriv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		panic("seal-event test: generate encrypt key: " + err.Error())
	}
	return &keyPairs{signPriv: signPriv, encPriv: encPriv}
})

type keyPairs struct {
	signPriv *rsa.PrivateKey
	encPriv  *rsa.PrivateKey
}

// newCLIFixture gives one subtest the shared keys plus its own temp dir and key files.
func newCLIFixture(t *testing.T) *cliFixture {
	t.Helper()
	keys := sharedKeys()
	dir := t.TempDir()
	return &cliFixture{
		signPub:  &keys.signPriv.PublicKey,
		signPriv: keys.signPriv,
		encPriv:  keys.encPriv,
		signPath: writeFile(t, dir, "sign.der", derPKCS8Private(t, keys.signPriv)),
		encPath:  writeFile(t, dir, "enc.pub.der", derPKIXPublic(t, &keys.encPriv.PublicKey)),
		dir:      dir,
	}
}

// baseArgs is the minimal valid flag set: file key sources, both concrete kids, the
// Subject member and the event type. Cases append to it, or override one slot through
// withFlag.
func (fx *cliFixture) baseArgs() []string {
	return []string{
		"-sign-key-file", fx.signPath,
		"-encrypt-key-file", fx.encPath,
		"-sign-kid", signKid,
		"-encrypt-kid", encKid,
		"-subject", subjectMember,
		"-event-type", testEventType,
	}
}

// withFlag returns a copy of args with flagName's value replaced, addressing the flag by
// NAME rather than by offset: baseArgs can then be reordered or extended without silently
// repointing a case at a different flag than its name and assertion claim. A flag that is
// not there is a bug in the case, not a scenario, so it fails the test loudly.
func withFlag(t *testing.T, args []string, flagName, value string) []string {
	t.Helper()
	out := append([]string(nil), args...)
	for i := 0; i+1 < len(out); i += 2 {
		if out[i] == flagName {
			out[i+1] = value
			return out
		}
	}
	t.Fatalf("flag %s is not part of baseArgs", flagName)
	return nil
}

// consumerSpec scans the consumer declaration once per test.
func consumerSpec(t *testing.T) *sealed.Spec {
	t.Helper()
	spec, err := sealed.ScanType(reflect.TypeOf(paymentAuthorized{}))
	require.NoError(t, err)
	require.NotNil(t, spec)
	return spec
}

// consumerResolver holds the INVERSE of the CLI's own material under the concrete kids:
// the sign PUBLIC half (to verify) and the encrypt PRIVATE half (to decrypt).
func (fx *cliFixture) consumerResolver() map[string]any {
	return map[string]any{
		signKid: fx.signPub,
		encKid:  fx.encPriv,
	}
}

// openBody runs the real opener over the CLI's stdout. The declared EventType is always
// testEventType: it is the CONSUMER's declaration, fixed by the seal tag it belongs with —
// the etyp cases vary the CLI's -event-type flag instead, which is the half that can drift.
func (fx *cliFixture) openBody(t *testing.T, stdout string, tenant sealed.TenantExpectation) (*paymentAuthorized, *sealed.Envelope, error) {
	t.Helper()
	var got paymentAuthorized
	env, err := sealed.Open([]byte(strings.TrimSpace(stdout)), consumerSpec(t), &sealed.OpenOptions{
		EventType: testEventType,
		Tenant:    tenant,
		Keys:      jositest.NewTestResolver(fx.consumerResolver()),
	}, &got)
	return &got, env, err
}

// openCode extracts the wire-protocol code from an Open failure, so a case names the rule
// that fired rather than merely asserting "some error".
func openCode(t *testing.T, err error) string {
	t.Helper()
	require.Error(t, err)
	var oe *sealed.OpenError
	require.True(t, errors.As(err, &oe), "not an *sealed.OpenError: %v", err)
	return oe.Err.Code
}

// TestSealEventRoundTrip is the load-bearing test: a body the CLI emits opens through the
// production sealed.Open path against the consumer's own tag declaration.
func TestSealEventRoundTrip(t *testing.T) {
	t.Run("with_tenant_id", func(t *testing.T) {
		fx := newCLIFixture(t)

		stdout, stderr, code := runCLI(append(fx.baseArgs(), "-tenant-id", testTenant), []byte(docJSON))
		require.Equal(t, 0, code, "stderr: %s", stderr)

		got, env, err := fx.openBody(t, stdout, sealed.TenantExpectation{Required: true, Expected: testTenant})
		require.NoError(t, err)

		assert.Equal(t, "o-1", got.OrderID)
		assert.Equal(t, 100, got.Amount)
		assert.Equal(t, cardData{PAN: "4111111111111111", Expiry: "12/30"}, got.Card)

		assert.Equal(t, testEventType, env.EventType)
		assert.Equal(t, testTenant, env.TenantID)
		assert.Equal(t, signKid, env.SignKid)
		assert.Equal(t, encKid, env.EncKid)
		assert.Equal(t, signFamily, env.SignFamily)
	})

	t.Run("without_tenant_id", func(t *testing.T) {
		fx := newCLIFixture(t)

		stdout, stderr, code := runCLI(fx.baseArgs(), []byte(docJSON))
		require.Equal(t, 0, code, "stderr: %s", stderr)

		// Absent -tenant-id means no signed tid at all: an opener that does not demand one
		// sees an empty TenantID, and one that does refuses the very same body.
		_, env, err := fx.openBody(t, stdout, sealed.TenantExpectation{})
		require.NoError(t, err)
		assert.Empty(t, env.TenantID)

		_, _, err = fx.openBody(t, stdout, sealed.TenantExpectation{Required: true})
		assert.Equal(t, sealed.CodeTenantMismatch, openCode(t, err))
	})

	t.Run("stdin_and_file_are_equivalent", func(t *testing.T) {
		fx := newCLIFixture(t)
		docPath := writeFile(t, fx.dir, "event.json", []byte(docJSON))

		fromStdin, stderr, code := runCLI(fx.baseArgs(), []byte(docJSON))
		require.Equal(t, 0, code, "stderr: %s", stderr)
		fromFile, stderr, code := runCLI(append(fx.baseArgs(), docPath), nil)
		require.Equal(t, 0, code, "stderr: %s", stderr)

		stdinEvt, _, err := fx.openBody(t, fromStdin, sealed.TenantExpectation{})
		require.NoError(t, err)
		fileEvt, _, err := fx.openBody(t, fromFile, sealed.TenantExpectation{})
		require.NoError(t, err)
		assert.Equal(t, stdinEvt, fileEvt)
	})

	t.Run("explicit_stdin_dash", func(t *testing.T) {
		fx := newCLIFixture(t)

		stdout, stderr, code := runCLI(append(fx.baseArgs(), "-"), []byte(docJSON))
		require.Equal(t, 0, code, "stderr: %s", stderr)
		_, _, err := fx.openBody(t, stdout, sealed.TenantExpectation{})
		require.NoError(t, err)
	})

	t.Run("base64_value_key_sources", func(t *testing.T) {
		fx := newCLIFixture(t)

		stdout, stderr, code := runCLI([]string{
			"-sign-key-value", base64.StdEncoding.EncodeToString(derPKCS8Private(t, fx.signPriv)),
			"-encrypt-key-value", base64.StdEncoding.EncodeToString(derPKIXPublic(t, &fx.encPriv.PublicKey)),
			"-sign-kid", signKid,
			"-encrypt-kid", encKid,
			"-subject", subjectMember,
			"-event-type", testEventType,
		}, []byte(docJSON))
		require.Equal(t, 0, code, "stderr: %s", stderr)

		got, _, err := fx.openBody(t, stdout, sealed.TenantExpectation{})
		require.NoError(t, err)
		assert.Equal(t, "4111111111111111", got.Card.PAN)
	})
}

// TestSealEventFreshJTI pins that each invocation mints its own jti — the dedup QA recipe
// is publishing ONE body twice, never sealing twice.
func TestSealEventFreshJTI(t *testing.T) {
	fx := newCLIFixture(t)

	first, stderr, code := runCLI(fx.baseArgs(), []byte(docJSON))
	require.Equal(t, 0, code, "stderr: %s", stderr)
	second, stderr, code := runCLI(fx.baseArgs(), []byte(docJSON))
	require.Equal(t, 0, code, "stderr: %s", stderr)

	_, firstEnv, err := fx.openBody(t, first, sealed.TenantExpectation{})
	require.NoError(t, err)
	_, secondEnv, err := fx.openBody(t, second, sealed.TenantExpectation{})
	require.NoError(t, err)

	assert.NotEqual(t, firstEnv.JTI, secondEnv.JTI)
	assert.NotEqual(t, firstEnv.DedupKey(), secondEnv.DedupKey())
}

// TestSealEventKidBinding pins the two halves of the kid contract: what the CLI writes into
// the signed header is what the consumer's family pin and etyp rule judge, and a kid that is
// not a Generation at all never reaches a key.
func TestSealEventKidBinding(t *testing.T) {
	t.Run("foreign_sign_family_fails_open", func(t *testing.T) {
		fx := newCLIFixture(t)

		// A well-formed Generation of a DIFFERENT family: the CLI derives "rogue-sign" and
		// seals happily; the consumer's spec declares svc-payments-sign, so the family pin
		// (rule 3) fires before any key is resolved.
		args := withFlag(t, append(fx.baseArgs(), "-tenant-id", testTenant), "-sign-kid", "rogue-sign-v1")
		stdout, stderr, code := runCLI(args, []byte(docJSON))
		require.Equal(t, 0, code, "stderr: %s", stderr)

		_, _, err := fx.openBody(t, stdout, sealed.TenantExpectation{})
		assert.Equal(t, sealed.CodeKidFamilyMismatch, openCode(t, err))
	})

	t.Run("wrong_event_type_fails_open", func(t *testing.T) {
		fx := newCLIFixture(t)

		args := withFlag(t, fx.baseArgs(), "-event-type", "payment.declined")
		stdout, stderr, code := runCLI(args, []byte(docJSON))
		require.Equal(t, 0, code, "stderr: %s", stderr)

		_, _, err := fx.openBody(t, stdout, sealed.TenantExpectation{})
		assert.Equal(t, sealed.CodeEventTypeMismatch, openCode(t, err))
	})

	t.Run("non_generation_kids_rejected", func(t *testing.T) {
		cases := []struct {
			name string
			flag string
			kid  string
		}{
			{name: "bare_logical_sign_kid", flag: "-sign-kid", kid: signFamily},
			{name: "zero_generation_sign_kid", flag: "-sign-kid", kid: "x-v0"},
			{name: "bare_logical_encrypt_kid", flag: "-encrypt-kid", kid: encFamily},
			{name: "zero_generation_encrypt_kid", flag: "-encrypt-kid", kid: "x-v0"},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				fx := newCLIFixture(t)
				args := withFlag(t, fx.baseArgs(), tc.flag, tc.kid)

				stdout, stderr, code := runCLI(args, []byte(docJSON))
				require.Equal(t, 1, code)
				assert.Empty(t, stdout)
				assert.Contains(t, stderr, tc.flag)
			})
		}
	})
}

// TestSealEventRejections covers the flag-validation and document-validation error paths.
// Each case asserts the exit code AND a distinguishing stderr fragment, so a mutation that
// collapses two failures into one is not silently green.
func TestSealEventRejections(t *testing.T) {
	type tableCase struct {
		name       string
		build      func(t *testing.T, fx *cliFixture) []string
		stdin      string
		wantCode   int
		wantStderr string
	}

	table := []tableCase{
		{
			name: "both_sign_key_sources",
			build: func(t *testing.T, fx *cliFixture) []string {
				// Both sources carry VALID material for the SAME key, so without the
				// exactly-one-of check the file source wins and the seal succeeds.
				val := base64.StdEncoding.EncodeToString(derPKCS8Private(t, fx.signPriv))
				return append(fx.baseArgs(), "-sign-key-value", val)
			},
			stdin:      docJSON,
			wantCode:   1,
			wantStderr: "-sign-key-value",
		},
		{
			name: "neither_sign_key_source",
			build: func(t *testing.T, fx *cliFixture) []string {
				return withFlag(t, fx.baseArgs(), "-sign-key-file", "")
			},
			stdin:      docJSON,
			wantCode:   1,
			wantStderr: "-sign-key-file",
		},
		{
			name: "both_encrypt_key_sources",
			build: func(t *testing.T, fx *cliFixture) []string {
				val := base64.StdEncoding.EncodeToString(derPKIXPublic(t, &fx.encPriv.PublicKey))
				return append(fx.baseArgs(), "-encrypt-key-value", val)
			},
			stdin:      docJSON,
			wantCode:   1,
			wantStderr: "-encrypt-key-value",
		},
		{
			name: "neither_encrypt_key_source",
			build: func(t *testing.T, fx *cliFixture) []string {
				return withFlag(t, fx.baseArgs(), "-encrypt-key-file", "")
			},
			stdin:      docJSON,
			wantCode:   1,
			wantStderr: "-encrypt-key-file",
		},
		{
			name: "missing_sign_kid",
			build: func(t *testing.T, fx *cliFixture) []string {
				return withFlag(t, fx.baseArgs(), "-sign-kid", "")
			},
			stdin:      docJSON,
			wantCode:   1,
			wantStderr: "-sign-kid",
		},
		{
			name: "missing_encrypt_kid",
			build: func(t *testing.T, fx *cliFixture) []string {
				return withFlag(t, fx.baseArgs(), "-encrypt-kid", "")
			},
			stdin:      docJSON,
			wantCode:   1,
			wantStderr: "-encrypt-kid",
		},
		{
			name: "missing_subject",
			build: func(t *testing.T, fx *cliFixture) []string {
				return withFlag(t, fx.baseArgs(), "-subject", "")
			},
			stdin:      docJSON,
			wantCode:   1,
			wantStderr: "-subject",
		},
		{
			name: "missing_event_type",
			build: func(t *testing.T, fx *cliFixture) []string {
				return withFlag(t, fx.baseArgs(), "-event-type", "")
			},
			stdin:      docJSON,
			wantCode:   1,
			wantStderr: "-event-type",
		},
		{
			name:  "subject_absent_from_document",
			build: func(_ *testing.T, fx *cliFixture) []string { return fx.baseArgs() },
			stdin: `{"order_id":"o-1","amount":100}`,
			// The Subject member must exist to be sealed; the document door refuses the
			// whole document rather than emitting an event with nothing sealed.
			wantCode:   1,
			wantStderr: sealed.CodeDocumentInvalid,
		},
		{
			name:  "subject_case_fold_twin_present",
			build: func(_ *testing.T, fx *cliFixture) []string { return fx.baseArgs() },
			// "Card" case-folds to the Subject "card": encoding/json matches members
			// case-insensitively, so a consumer could read the clear twin.
			stdin:      `{"order_id":"o-1","card":{"pan":"4111111111111111"},"Card":"plaintext"}`,
			wantCode:   1,
			wantStderr: sealed.CodeDocumentInvalid,
		},
		{
			name:       "non_object_document",
			build:      func(_ *testing.T, fx *cliFixture) []string { return fx.baseArgs() },
			stdin:      `[{"card":{"pan":"4111111111111111"}}]`,
			wantCode:   1,
			wantStderr: sealed.CodeDocumentInvalid,
		},
		{
			name:       "trailing_content_after_document",
			build:      func(_ *testing.T, fx *cliFixture) []string { return fx.baseArgs() },
			stdin:      docJSON + `{"second":true}`,
			wantCode:   1,
			wantStderr: sealed.CodeDocumentInvalid,
		},
		{
			name: "unreadable_payload_file",
			build: func(_ *testing.T, fx *cliFixture) []string {
				return append(fx.baseArgs(), filepath.Join(fx.dir, "does-not-exist.json"))
			},
			wantCode:   1,
			wantStderr: "read payload file",
		},
		{
			name: "garbage_sign_key_der",
			build: func(t *testing.T, fx *cliFixture) []string {
				garbage := writeFile(t, fx.dir, "garbage.der", []byte{0x00, 0x01, 0x02})
				return withFlag(t, fx.baseArgs(), "-sign-key-file", garbage)
			},
			stdin:      docJSON,
			wantCode:   1,
			wantStderr: "sign key",
		},
		{
			name: "garbage_encrypt_key_der",
			build: func(t *testing.T, fx *cliFixture) []string {
				garbage := writeFile(t, fx.dir, "garbage.pub.der", []byte{0x00, 0x01, 0x02})
				return withFlag(t, fx.baseArgs(), "-encrypt-key-file", garbage)
			},
			stdin:      docJSON,
			wantCode:   1,
			wantStderr: "encrypt key",
		},
		{
			name: "two_positional_arguments",
			build: func(_ *testing.T, fx *cliFixture) []string {
				return append(fx.baseArgs(), "a.json", "b.json")
			},
			wantCode:   2,
			wantStderr: "at most one",
		},
		{
			name:     "help_exits_zero",
			build:    func(_ *testing.T, _ *cliFixture) []string { return []string{"-h"} },
			wantCode: 0,
		},
		{
			name:     "unknown_flag_exits_two",
			build:    func(_ *testing.T, fx *cliFixture) []string { return append(fx.baseArgs(), "-nope") },
			wantCode: 2,
		},
	}

	for _, tc := range table {
		t.Run(tc.name, func(t *testing.T) {
			fx := newCLIFixture(t)
			stdout, stderr, code := runCLI(tc.build(t, fx), []byte(tc.stdin))
			require.Equal(t, tc.wantCode, code, "stderr: %s", stderr)
			if tc.wantCode != 0 {
				assert.Empty(t, stdout)
			}
			if tc.wantStderr != "" {
				assert.Contains(t, stderr, tc.wantStderr)
			}
		})
	}
}
