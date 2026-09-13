package main

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/internal/sealcli"
	"github.com/gaborage/go-bricks/jose/sealed"
	jositest "github.com/gaborage/go-bricks/jose/testing"
)

// The kid namespace the fixtures use, split the same way seal-event's tests split it: the
// CLI is handed the concrete Generation and must derive the Logical family itself.
const (
	signFamily = "svc-payments-sign"
	encFamily  = "aud-core-encrypt"
	signKid    = signFamily + "-v1"
	encKid     = encFamily + "-v1"

	testEventType = "payment.authorized"
	testTenant    = "t1"
	subjectMember = "card"

	// subjectMarker is the byte string the redaction tests hunt for. It is deliberately
	// unlike any envelope or flag value, so finding it anywhere is proof of a leak.
	subjectMarker = "PAN-LEAK-CANARY-4111111111111111"
	wantRedacted  = `"<redacted>"`
)

// docJSON is the fixture document: two clear members and one Subject object carrying the
// canary. Member order is the producer's and survives the seal byte for byte.
const docJSON = `{"order_id":"o-1","amount":100,"card":{"pan":"` + subjectMarker + `","expiry":"12/30"}}`

// runCLI invokes run() with in-memory streams, so every case goes through the same entry
// point the real binary does.
func runCLI(args []string, stdin []byte) (stdout, stderr string, code int) {
	var outBuf, errBuf bytes.Buffer
	code = run(args, bytes.NewReader(stdin), &outBuf, &errBuf)
	return outBuf.String(), errBuf.String(), code
}

// derPKCS8Private renders a private key in the PKCS#8 DER form the -encrypt-key flags take.
func derPKCS8Private(t *testing.T, priv *rsa.PrivateKey) []byte {
	t.Helper()
	der, err := x509.MarshalPKCS8PrivateKey(priv)
	require.NoError(t, err)
	return der
}

// derPKIXPublic renders a public key in the PKIX DER form the -sign-key flags take.
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

type keyPairs struct {
	signPriv *rsa.PrivateKey
	encPriv  *rsa.PrivateKey
}

// sharedKeys mints the two RSA pairs once for the whole package: no test mutates a key, and
// what each test does need to itself — its key FILES and its temp dir — still comes fresh.
var sharedKeys = sync.OnceValue(func() *keyPairs {
	signPriv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		panic("open-event test: generate sign key: " + err.Error())
	}
	encPriv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		panic("open-event test: generate encrypt key: " + err.Error())
	}
	return &keyPairs{signPriv: signPriv, encPriv: encPriv}
})

// cliFixture holds the CONSUMER-role material the CLI flags expect (sign PUBLIC, encrypt
// PRIVATE) plus the producer's mirror halves used to mint the bodies it is fed.
type cliFixture struct {
	signPriv *rsa.PrivateKey
	encPriv  *rsa.PrivateKey
	signPath string
	encPath  string
	dir      string
}

func newCLIFixture(t *testing.T) *cliFixture {
	t.Helper()
	keys := sharedKeys()
	dir := t.TempDir()
	return &cliFixture{
		signPriv: keys.signPriv,
		encPriv:  keys.encPriv,
		signPath: writeFile(t, dir, "sign.pub.der", derPKIXPublic(t, &keys.signPriv.PublicKey)),
		encPath:  writeFile(t, dir, "enc.der", derPKCS8Private(t, keys.encPriv)),
		dir:      dir,
	}
}

// producerResolver holds the INVERSE of the CLI's own material: the sign PRIVATE half (to
// sign) and the encrypt PUBLIC half (to encrypt).
func (fx *cliFixture) producerResolver() map[string]any {
	return map[string]any{
		signKid: fx.signPriv,
		encKid:  &fx.encPriv.PublicKey,
	}
}

// sealOptions is the producer half of one fixture body; cases vary a field to make the CLI
// refuse the result.
type sealOptions struct {
	doc        string
	signKid    string
	encKid     string
	eventType  string
	tenantID   string
	subjectKey string
}

func (fx *cliFixture) defaultSealOptions() *sealOptions {
	return &sealOptions{
		doc:        docJSON,
		signKid:    signKid,
		encKid:     encKid,
		eventType:  testEventType,
		tenantID:   testTenant,
		subjectKey: subjectMember,
	}
}

// seal mints one sealed body through the same production path seal-event runs.
func (fx *cliFixture) seal(t *testing.T, o *sealOptions) []byte {
	t.Helper()
	signLogical, _, ok := sealed.SplitGenerationKid(o.signKid)
	require.True(t, ok, "fixture sign kid must be a generation")
	encLogical, _, ok := sealed.SplitGenerationKid(o.encKid)
	require.True(t, ok, "fixture encrypt kid must be a generation")

	spec, err := sealed.NewDocumentSpec(signLogical, encLogical, o.subjectKey)
	require.NoError(t, err)

	body, err := sealed.SealDocument([]byte(o.doc), spec, &sealed.Options{
		SignKid:    o.signKid,
		EncryptKid: o.encKid,
		EventType:  o.eventType,
		TenantID:   o.tenantID,
		Keys:       jositest.NewTestResolver(fx.producerResolver()),
	})
	require.NoError(t, err)
	return body
}

// baseArgs is the minimal valid flag set: file key sources, both concrete kids, the Subject
// member, the event type and the tenancy the fixture bodies are sealed under.
func (fx *cliFixture) baseArgs() []string {
	return []string{
		"-sign-key-file", fx.signPath,
		"-encrypt-key-file", fx.encPath,
		"-sign-kid", signKid,
		"-encrypt-kid", encKid,
		"-subject", subjectMember,
		"-event-type", testEventType,
		"-tenancy", "shared",
		"-tenant-id", testTenant,
	}
}

// withFlag returns a copy of args with flagName's value replaced, addressing the flag by
// NAME rather than by offset. A flag that is not there is a bug in the case.
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

// TestOpenEventDefaultOutput is the load-bearing case: a body the production sealer minted
// opens, the envelope is reported field for field, and the Subject member is rendered
// redacted — shape visible, plaintext nowhere.
func TestOpenEventDefaultOutput(t *testing.T) {
	fx := newCLIFixture(t)
	body := fx.seal(t, fx.defaultSealOptions())

	stdout, stderr, code := runCLI(fx.baseArgs(), body)
	require.Equal(t, 0, code, "stderr: %s", stderr)
	assert.Empty(t, stderr, "a clean open says nothing on stderr")

	for _, want := range []string{
		"JTI:", "IssuedAt:",
		"EventType:  " + testEventType,
		"TenantID:   " + testTenant,
		"SignKid:    " + signKid,
		"SignFamily: " + signFamily,
		"EncKid:     " + encKid,
	} {
		assert.Contains(t, stdout, want)
	}

	assert.Contains(t, stdout, `"card":`+wantRedacted, "the subject member keeps its place, redacted")
	assert.Contains(t, stdout, `"order_id":"o-1"`, "clear members travel verbatim")
	assert.Contains(t, stdout, `"amount":100`)
}

// TestOpenEventNeverLeaksSubjectByDefault is the redaction guard, asserted on BOTH streams:
// a canary that reaches either one is a leak wherever the render put it.
func TestOpenEventNeverLeaksSubjectByDefault(t *testing.T) {
	fx := newCLIFixture(t)
	body := fx.seal(t, fx.defaultSealOptions())

	t.Run("text", func(t *testing.T) {
		stdout, stderr, code := runCLI(fx.baseArgs(), body)
		require.Equal(t, 0, code, "stderr: %s", stderr)
		assert.NotContains(t, stdout, subjectMarker, "subject plaintext on stdout")
		assert.NotContains(t, stderr, subjectMarker, "subject plaintext on stderr")
		// A length hint is a leak too: the placeholder is fixed-width by construction.
		assert.Contains(t, stdout, wantRedacted)
	})

	t.Run("json", func(t *testing.T) {
		stdout, stderr, code := runCLI(append(fx.baseArgs(), "-json"), body)
		require.Equal(t, 0, code, "stderr: %s", stderr)
		assert.NotContains(t, stdout, subjectMarker, "subject plaintext on stdout")
		assert.NotContains(t, stderr, subjectMarker, "subject plaintext on stderr")
		// The payload is judged DECODED: the escaped and unescaped spellings of the
		// placeholder are the same JSON value, and only the value is the contract.
		assert.Equal(t, redactedText, subjectValue(t, decodeSuccess(t, stdout).Document))
	})
}

// TestOpenEventDocumentStaysValidJSON pins that the redacted splice lands back in a
// document a downstream tool can parse, in every position the member can occupy.
func TestOpenEventDocumentStaysValidJSON(t *testing.T) {
	cases := []struct{ name, doc string }{
		{"first_member", `{"card":{"pan":"` + subjectMarker + `"},"amount":100}`},
		{"middle_member", docJSON},
		{"last_member", `{"amount":100,"card":{"pan":"` + subjectMarker + `"}}`},
		{"only_member", `{"card":{"pan":"` + subjectMarker + `"}}`},
		{"pretty_printed", "{\n  \"amount\": 100,\n  \"card\": {\"pan\": \"" + subjectMarker + "\"}\n}"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fx := newCLIFixture(t)
			o := fx.defaultSealOptions()
			o.doc = tc.doc
			body := fx.seal(t, o)

			stdout, stderr, code := runCLI(append(fx.baseArgs(), "-json"), body)
			require.Equal(t, 0, code, "stderr: %s", stderr)

			doc := decodeSuccess(t, stdout).Document
			assert.NotContains(t, string(doc), subjectMarker)
			assert.Equal(t, redactedText, subjectValue(t, doc))
		})
	}
}

// TestOpenEventPrintSubject pins the fixture-only escape hatch: the plaintext appears in
// the member's own place and stderr carries exactly one warning line.
func TestOpenEventPrintSubject(t *testing.T) {
	fx := newCLIFixture(t)
	body := fx.seal(t, fx.defaultSealOptions())

	stdout, stderr, code := runCLI(append(fx.baseArgs(), "-print-subject"), body)
	require.Equal(t, 0, code, "stderr: %s", stderr)

	assert.Contains(t, stdout, subjectMarker)
	assert.Contains(t, stdout, `"card":{"pan":"`+subjectMarker+`"`)
	assert.NotContains(t, stdout, wantRedacted)

	lines := strings.Split(strings.TrimSuffix(stderr, "\n"), "\n")
	require.Len(t, lines, 1, "stderr must carry exactly one warning line, got %q", stderr)
	assert.Contains(t, lines[0], "-print-subject")
}

// stdin and a positional file must be interchangeable, as on seal-event.
func TestOpenEventReadsFileOrStdin(t *testing.T) {
	fx := newCLIFixture(t)
	body := fx.seal(t, fx.defaultSealOptions())
	bodyPath := writeFile(t, fx.dir, "body.txt", body)

	fromStdin, stderr, code := runCLI(fx.baseArgs(), body)
	require.Equal(t, 0, code, "stderr: %s", stderr)
	fromFile, stderr, code := runCLI(append(fx.baseArgs(), bodyPath), nil)
	require.Equal(t, 0, code, "stderr: %s", stderr)
	assert.Equal(t, fromStdin, fromFile)

	// A trailing newline is what a shell redirect leaves behind; it must not defeat the open.
	withNewline, stderr, code := runCLI(fx.baseArgs(), append(append([]byte(nil), body...), '\n'))
	require.Equal(t, 0, code, "stderr: %s", stderr)
	assert.Equal(t, fromStdin, withNewline)
}

// base64 key sources must reach the same resolver the file sources do.
func TestOpenEventBase64KeySources(t *testing.T) {
	fx := newCLIFixture(t)
	body := fx.seal(t, fx.defaultSealOptions())

	stdout, stderr, code := runCLI([]string{
		"-sign-key-value", base64.StdEncoding.EncodeToString(derPKIXPublic(t, &fx.signPriv.PublicKey)),
		"-encrypt-key-value", base64.StdEncoding.EncodeToString(derPKCS8Private(t, fx.encPriv)),
		"-sign-kid", signKid,
		"-encrypt-kid", encKid,
		"-subject", subjectMember,
		"-event-type", testEventType,
	}, body)
	require.Equal(t, 0, code, "stderr: %s", stderr)
	assert.Contains(t, stdout, `"card":`+wantRedacted)
}

// TestOpenEventHelpNamesItsLimits pins the two facts -h must not leave an operator to
// discover by experiment: that the tenancy default opts OUT of the tid rule rather than
// enforcing one, and that the input is size-capped.
func TestOpenEventHelpNamesItsLimits(t *testing.T) {
	_, stderr, code := runCLI([]string{"-h"}, nil)
	require.Equal(t, exitOK, code)
	assert.Contains(t, stderr, "disabled: tenant stamp not judged")
	assert.Contains(t, stderr, strconv.FormatInt(sealcli.MaxPayloadBytes, 10)+" bytes")
	// Both halves of the coupling are stated where each flag is documented: the flag name
	// alone would print regardless, so each assertion pins the sentence, not the spelling.
	assert.Contains(t, stderr, "-tenant-id is then refused")
	assert.Contains(t, stderr, "-tenancy disabled")
}

// TestOpenEventRefusesOversizedBody pins that this binary's payload cap reaches the door as a
// tool error, before any key material is asked to open something that cannot be a body.
func TestOpenEventRefusesOversizedBody(t *testing.T) {
	fx := newCLIFixture(t)
	oversized := bytes.Repeat([]byte("a"), int(sealcli.MaxPayloadBytes)+1)

	stdout, stderr, code := runCLI(fx.baseArgs(), oversized)
	require.Equal(t, exitToolError, code, "stderr: %s", stderr)
	assert.Empty(t, stdout)
	assert.Contains(t, stderr, "exceeds the size limit")
}

// jsonSuccess is the documented -json success shape. Rule is deliberately absent from the
// struct AND asserted absent from the raw payload below.
type jsonSuccess struct {
	Envelope struct {
		JTI        string `json:"jti"`
		IssuedAt   string `json:"issuedAt"`
		EventType  string `json:"eventType"`
		TenantID   string `json:"tenantId"`
		SignKid    string `json:"signKid"`
		SignFamily string `json:"signFamily"`
		EncKid     string `json:"encKid"`
	} `json:"envelope"`
	Document json.RawMessage `json:"document"`
}

// refusalPayload is the documented -json refusal shape.
type refusalPayload struct {
	Code    string            `json:"code"`
	Details map[string]string `json:"details"`
}

func decodeSuccess(t *testing.T, stdout string) *jsonSuccess {
	t.Helper()
	var got jsonSuccess
	require.NoError(t, json.Unmarshal([]byte(stdout), &got), "stdout is not JSON: %s", stdout)
	assertNoRuleKey(t, stdout)
	return &got
}

// assertNoRuleKey walks the payload generically: Rule's numbering is documented unstable,
// so it must not appear under any spelling at any level a caller could key on.
func assertNoRuleKey(t *testing.T, payload string) {
	t.Helper()
	var generic map[string]any
	require.NoError(t, json.Unmarshal([]byte(payload), &generic))
	for _, k := range []string{"rule", "Rule"} {
		assert.NotContains(t, generic, k, "Rule must be omitted from every output")
		if nested, ok := generic["envelope"].(map[string]any); ok {
			assert.NotContains(t, nested, k)
		}
	}
}

// redactedText is the DECODED placeholder value — what any JSON consumer reads back,
// whichever spelling the encoder chose on the wire.
const redactedText = "<redacted>"

// subjectValue decodes a rendered document and returns its subject member, so a case
// asserts the VALUE a consumer sees rather than the bytes the encoder happened to emit.
func subjectValue(t *testing.T, doc json.RawMessage) any {
	t.Helper()
	var members map[string]any
	require.NoError(t, json.Unmarshal(doc, &members), "document is not a JSON object: %s", doc)
	value, ok := members[subjectMember]
	require.True(t, ok, "subject member %q absent from %s", subjectMember, doc)
	return value
}

// TestOpenEventJSONSuccess pins the success payload's shape and that the document member
// is embedded as JSON, not as a quoted string a caller would have to decode twice.
func TestOpenEventJSONSuccess(t *testing.T) {
	fx := newCLIFixture(t)
	body := fx.seal(t, fx.defaultSealOptions())

	stdout, stderr, code := runCLI(append(fx.baseArgs(), "-json"), body)
	require.Equal(t, 0, code, "stderr: %s", stderr)
	assert.Empty(t, stderr)

	got := decodeSuccess(t, stdout)
	assert.NotEmpty(t, got.Envelope.JTI)
	assert.NotEmpty(t, got.Envelope.IssuedAt)
	assert.Equal(t, testEventType, got.Envelope.EventType)
	assert.Equal(t, testTenant, got.Envelope.TenantID)
	assert.Equal(t, signKid, got.Envelope.SignKid)
	assert.Equal(t, signFamily, got.Envelope.SignFamily)
	assert.Equal(t, encKid, got.Envelope.EncKid)

	assert.JSONEq(t, `{"order_id":"o-1","amount":100,"card":"<redacted>"}`, string(got.Document))

	// HTML escaping stays ON: a subject that ever carried <script> must not reach a
	// browser-backed DLQ viewer unescaped. Every decoder reads back the same value.
	assert.NotContains(t, stdout, wantRedacted, "the raw payload must carry the escaped spelling")
	assert.Contains(t, stdout, `\u003credacted\u003e`)
}

// TestOpenEventJSONPrintSubject pins that the escape hatch reaches the JSON path too, and
// that the warning still goes to stderr so stdout stays machine-readable.
func TestOpenEventJSONPrintSubject(t *testing.T) {
	fx := newCLIFixture(t)
	body := fx.seal(t, fx.defaultSealOptions())

	stdout, stderr, code := runCLI(append(fx.baseArgs(), "-json", "-print-subject"), body)
	require.Equal(t, 0, code, "stderr: %s", stderr)

	got := decodeSuccess(t, stdout)
	assert.JSONEq(t,
		`{"order_id":"o-1","amount":100,"card":{"pan":"`+subjectMarker+`","expiry":"12/30"}}`,
		string(got.Document))
	assert.Contains(t, stderr, "-print-subject")
}

// tamper flips one byte of the body's payload segment, leaving a well-formed compact JWS
// whose signature no longer covers what it carries.
func tamper(t *testing.T, body []byte) []byte {
	t.Helper()
	segs := strings.Split(string(body), ".")
	require.Len(t, segs, 3, "a sealed body is a three-segment compact JWS")
	payload := []byte(segs[1])
	last := len(payload) - 1
	if payload[last] == 'A' {
		payload[last] = 'B'
	} else {
		payload[last] = 'A'
	}
	segs[1] = string(payload)
	return []byte(strings.Join(segs, "."))
}

// refusalCase is one message the CLI must refuse: the body is minted with seal, the flags
// are bent with bend, and the wire code names the rule that must fire.
type refusalCase struct {
	name     string
	seal     func(o *sealOptions)
	bend     func(t *testing.T, fx *cliFixture, args []string) []string
	tamper   bool
	wantCode string
}

var refusalCases = []refusalCase{
	{
		name:     "tampered_body",
		tamper:   true,
		wantCode: sealed.CodeSignatureInvalid,
	},
	{
		name: "wrong_sign_kid_family",
		bend: func(t *testing.T, _ *cliFixture, a []string) []string {
			return withFlag(t, a, "-sign-kid", "rogue-sign-v1")
		},
		wantCode: sealed.CodeKidFamilyMismatch,
	},
	{
		name: "wrong_sign_kid_generation",
		bend: func(t *testing.T, _ *cliFixture, a []string) []string {
			return withFlag(t, a, "-sign-kid", signFamily+"-v2")
		},
		wantCode: sealed.CodeKidUnknownGeneration,
	},
	{
		name: "wrong_encrypt_kid_family",
		bend: func(t *testing.T, _ *cliFixture, a []string) []string {
			return withFlag(t, a, "-encrypt-kid", "rogue-encrypt-v1")
		},
		wantCode: sealed.CodeKidFamilyMismatch,
	},
	{
		name: "wrong_encrypt_kid_generation",
		bend: func(t *testing.T, _ *cliFixture, a []string) []string {
			return withFlag(t, a, "-encrypt-kid", encFamily+"-v2")
		},
		wantCode: sealed.CodeKidUnknownGeneration,
	},
	{
		name: "wrong_event_type",
		bend: func(t *testing.T, _ *cliFixture, a []string) []string {
			return withFlag(t, a, "-event-type", "payment.declined")
		},
		wantCode: sealed.CodeEventTypeMismatch,
	},
	{
		name:     "shared_tenancy_wrong_tenant",
		bend:     func(t *testing.T, _ *cliFixture, a []string) []string { return withFlag(t, a, "-tenant-id", "t2") },
		wantCode: sealed.CodeTenantMismatch,
	},
	{
		name:     "shared_tenancy_absent_tenant",
		seal:     func(o *sealOptions) { o.tenantID = "" },
		bend:     func(t *testing.T, _ *cliFixture, a []string) []string { return withFlag(t, a, "-tenant-id", "") },
		wantCode: sealed.CodeTenantMismatch,
	},
	{
		name: "optional_tenancy_wrong_tenant",
		bend: func(t *testing.T, _ *cliFixture, a []string) []string {
			return withFlag(t, withFlag(t, a, "-tenancy", "optional"), "-tenant-id", "t2")
		},
		wantCode: sealed.CodeTenantMismatch,
	},
	{
		name: "per_tenant_tenancy_wrong_tenant",
		bend: func(t *testing.T, _ *cliFixture, a []string) []string {
			return withFlag(t, withFlag(t, a, "-tenancy", "per-tenant"), "-tenant-id", "t2")
		},
		wantCode: sealed.CodeTenantMismatch,
	},
	{
		name:     "wrong_subject_member",
		bend:     func(t *testing.T, _ *cliFixture, a []string) []string { return withFlag(t, a, "-subject", "order_id") },
		wantCode: sealed.CodeManifestMismatch,
	},
	{
		name:     "not_sealed_at_all",
		bend:     func(_ *testing.T, _ *cliFixture, a []string) []string { return a },
		wantCode: sealed.CodeNotSealed,
	},
}

// bodyFor builds the body one refusal case is fed.
func bodyFor(t *testing.T, fx *cliFixture, tc *refusalCase) []byte {
	t.Helper()
	if tc.name == "not_sealed_at_all" {
		return []byte(docJSON)
	}
	o := fx.defaultSealOptions()
	if tc.seal != nil {
		tc.seal(o)
	}
	body := fx.seal(t, o)
	if tc.tamper {
		body = tamper(t, body)
	}
	return body
}

// TestOpenEventRefusals pins that every rule the consume door would fire reaches the shell
// as exit 3 plus the SAME wire code, on stderr in text mode and on stdout under -json.
func TestOpenEventRefusals(t *testing.T) {
	for _, tc := range refusalCases {
		t.Run(tc.name, func(t *testing.T) {
			fx := newCLIFixture(t)
			body := bodyFor(t, fx, &tc)
			args := fx.baseArgs()
			if tc.bend != nil {
				args = tc.bend(t, fx, args)
			}

			t.Run("text", func(t *testing.T) {
				stdout, stderr, code := runCLI(args, body)
				require.Equal(t, exitRefused, code, "stdout: %s stderr: %s", stdout, stderr)
				assert.Empty(t, stdout, "a refusal writes nothing to stdout in text mode")
				assert.Contains(t, stderr, tc.wantCode)
				assert.NotContains(t, stderr, subjectMarker)
			})

			t.Run("json", func(t *testing.T) {
				stdout, stderr, code := runCLI(append(args, "-json"), body)
				require.Equal(t, exitRefused, code, "stderr: %s", stderr)
				assert.Empty(t, stderr, "under -json the refusal is the stdout payload")

				var got refusalPayload
				require.NoError(t, json.Unmarshal([]byte(stdout), &got), "stdout is not JSON: %s", stdout)
				assert.Equal(t, tc.wantCode, got.Code)
				assertNoRuleKey(t, stdout)
				assertSafeDetails(t, got.Details)
				assert.NotContains(t, stdout, subjectMarker)
			})
		})
	}
}

// assertSafeDetails pins the detail vocabulary: presence, length and layer only. A key
// outside it is a new fact the refusal path started disclosing.
func assertSafeDetails(t *testing.T, details map[string]string) {
	t.Helper()
	require.NotNil(t, details, "details must be an object, never null")
	allowed := map[string]bool{
		sealed.DetailLayer:   true,
		sealed.DetailSlot:    true,
		sealed.DetailPresent: true,
		sealed.DetailLen:     true,
	}
	for k, v := range details {
		assert.True(t, allowed[k], "detail key %q is outside the presence/length/layer vocabulary", k)
		assert.NotContains(t, v, subjectMarker)
	}
}

// TestOpenEventToolErrors covers exit 1: the invocation itself could not run. Every case
// asserts a distinguishing stderr fragment, so collapsing two failures into one is not
// silently green.
func TestOpenEventToolErrors(t *testing.T) {
	cases := []struct {
		name       string
		build      func(t *testing.T, fx *cliFixture) []string
		wantStderr string
	}{
		{
			name: "missing_sign_key_file",
			build: func(t *testing.T, fx *cliFixture) []string {
				return withFlag(t, fx.baseArgs(), "-sign-key-file", filepath.Join(fx.dir, "absent.der"))
			},
			wantStderr: "sign key",
		},
		{
			name: "malformed_sign_key_der",
			build: func(t *testing.T, fx *cliFixture) []string {
				garbage := writeFile(t, fx.dir, "garbage.pub.der", []byte{0x00, 0x01, 0x02})
				return withFlag(t, fx.baseArgs(), "-sign-key-file", garbage)
			},
			wantStderr: "sign key",
		},
		{
			name: "malformed_encrypt_key_der",
			build: func(t *testing.T, fx *cliFixture) []string {
				garbage := writeFile(t, fx.dir, "garbage.der", []byte{0x00, 0x01, 0x02})
				return withFlag(t, fx.baseArgs(), "-encrypt-key-file", garbage)
			},
			wantStderr: "encrypt key",
		},
		{
			name: "unreadable_body_file",
			build: func(_ *testing.T, fx *cliFixture) []string {
				return append(fx.baseArgs(), filepath.Join(fx.dir, "does-not-exist.txt"))
			},
			wantStderr: "read payload file",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fx := newCLIFixture(t)
			body := fx.seal(t, fx.defaultSealOptions())
			stdout, stderr, code := runCLI(tc.build(t, fx), body)
			require.Equal(t, exitToolError, code, "stderr: %s", stderr)
			assert.Empty(t, stdout)
			assert.Contains(t, stderr, tc.wantStderr)
		})
	}
}

// TestOpenEventUsageErrors covers exit 2: the flags themselves are wrong. None of these
// may reach a key read, which every nonexistent path in the required-flag cases proves.
func TestOpenEventUsageErrors(t *testing.T) {
	cases := []struct {
		name       string
		build      func(t *testing.T, fx *cliFixture) []string
		wantCode   int
		wantStderr string
	}{
		{"missing_sign_kid", bendFlag("-sign-kid", ""), exitUsage, "-sign-kid is required"},
		{"missing_encrypt_kid", bendFlag("-encrypt-kid", ""), exitUsage, "-encrypt-kid is required"},
		{"missing_subject", bendFlag("-subject", ""), exitUsage, "-subject is required"},
		{"missing_event_type", bendFlag("-event-type", ""), exitUsage, "-event-type is required"},
		{"neither_sign_key_source", bendFlag("-sign-key-file", ""), exitUsage, "-sign-key-file"},
		{"neither_encrypt_key_source", bendFlag("-encrypt-key-file", ""), exitUsage, "-encrypt-key-file"},
		{"unknown_tenancy", bendFlag("-tenancy", "per_tenant"), exitUsage, "-tenancy"},
		// Fail-open on the one flag an operator reaches for is the defect: a -tenant-id
		// nobody judges must be refused, never silently ignored.
		{"tenant_id_under_disabled_tenancy", bendFlag("-tenancy", "disabled"), exitUsage, "-tenant-id"},
		{"non_generation_sign_kid", bendFlag("-sign-kid", signFamily), exitUsage, "-sign-kid"},
		{"non_generation_encrypt_kid", bendFlag("-encrypt-kid", "x-v0"), exitUsage, "-encrypt-kid"},
		{
			name: "both_sign_key_sources",
			build: func(t *testing.T, fx *cliFixture) []string {
				val := base64.StdEncoding.EncodeToString(derPKIXPublic(t, &fx.signPriv.PublicKey))
				return append(fx.baseArgs(), "-sign-key-value", val)
			},
			wantCode:   exitUsage,
			wantStderr: "-sign-key-value",
		},
		{
			name: "two_positional_arguments",
			build: func(_ *testing.T, fx *cliFixture) []string {
				return append(fx.baseArgs(), "a.txt", "b.txt")
			},
			wantCode:   exitUsage,
			wantStderr: "at most one",
		},
		{
			name:     "unknown_flag",
			build:    func(_ *testing.T, fx *cliFixture) []string { return append(fx.baseArgs(), "-nope") },
			wantCode: exitUsage,
		},
		{
			name:     "help_exits_zero",
			build:    func(_ *testing.T, _ *cliFixture) []string { return []string{"-h"} },
			wantCode: exitOK,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fx := newCLIFixture(t)
			stdout, stderr, code := runCLI(tc.build(t, fx), nil)
			require.Equal(t, tc.wantCode, code, "stderr: %s", stderr)
			assert.Empty(t, stdout, "a usage failure prints nothing to stdout, -json or not")
			if tc.wantStderr != "" {
				assert.Contains(t, stderr, tc.wantStderr)
			}
		})
	}
}

// bendFlag builds the common case shape: baseArgs with one flag's value replaced.
func bendFlag(name, value string) func(t *testing.T, fx *cliFixture) []string {
	return func(t *testing.T, fx *cliFixture) []string {
		return withFlag(t, fx.baseArgs(), name, value)
	}
}

// buildBinary compiles one cmd/ binary into dir and returns its path, so the round trip
// exercises the real executables rather than an in-process call that only resembles them.
func buildBinary(t *testing.T, dir, name string) string {
	t.Helper()
	out := filepath.Join(dir, name)
	if runtime.GOOS == "windows" {
		out += ".exe"
	}
	cmd := exec.CommandContext(t.Context(), "go", "build", "-o", out, "github.com/gaborage/go-bricks/cmd/"+name)
	combined, err := cmd.CombinedOutput()
	require.NoError(t, err, "go build %s: %s", name, combined)
	return out
}

// runBinary executes a built binary and reports its streams and exit status.
func runBinary(t *testing.T, bin string, args []string, stdin []byte) (stdout, stderr string, code int) {
	t.Helper()
	var outBuf, errBuf bytes.Buffer
	cmd := exec.CommandContext(t.Context(), bin, args...)
	cmd.Stdin = bytes.NewReader(stdin)
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	err := cmd.Run()
	var exitErr *exec.ExitError
	switch {
	case err == nil:
	case errors.As(err, &exitErr):
		code = exitErr.ExitCode()
	default:
		require.NoError(t, err, "running %s", bin)
	}
	return outBuf.String(), errBuf.String(), code
}

// TestSealEventToOpenEventRoundTrip drives both BINARIES against one fixture key pair: the
// producer holds the sign PRIVATE and encrypt PUBLIC halves, the opener their mirror
// images, and what one mints is what the other opens.
func TestSealEventToOpenEventRoundTrip(t *testing.T) {
	fx := newCLIFixture(t)
	sealBin := buildBinary(t, fx.dir, "seal-event")
	openBin := buildBinary(t, fx.dir, "open-event")

	// The producer's own halves, written next to the consumer's in the same temp dir.
	signPrivPath := writeFile(t, fx.dir, "sign.der", derPKCS8Private(t, fx.signPriv))
	encPubPath := writeFile(t, fx.dir, "enc.pub.der", derPKIXPublic(t, &fx.encPriv.PublicKey))

	body, stderr, code := runBinary(t, sealBin, []string{
		"-sign-key-file", signPrivPath,
		"-encrypt-key-file", encPubPath,
		"-sign-kid", signKid,
		"-encrypt-kid", encKid,
		"-subject", subjectMember,
		"-event-type", testEventType,
		"-tenant-id", testTenant,
	}, []byte(docJSON))
	require.Equal(t, 0, code, "seal-event stderr: %s", stderr)
	require.NotEmpty(t, body)

	openArgs := fx.baseArgs()
	stdout, stderr, code := runBinary(t, openBin, openArgs, []byte(body))
	require.Equal(t, exitOK, code, "open-event stderr: %s", stderr)
	assert.Contains(t, stdout, "SignFamily: "+signFamily)
	assert.Contains(t, stdout, `"card":`+wantRedacted)
	assert.NotContains(t, stdout, subjectMarker)
	assert.NotContains(t, stderr, subjectMarker)

	// The escape hatch recovers what the producer sealed, byte for byte.
	stdout, stderr, code = runBinary(t, openBin, append(openArgs, "-print-subject"), []byte(body))
	require.Equal(t, exitOK, code, "open-event stderr: %s", stderr)
	assert.Contains(t, stdout, `"card":{"pan":"`+subjectMarker+`","expiry":"12/30"}`)
	assert.Contains(t, stderr, "-print-subject")
}
