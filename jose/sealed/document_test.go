package sealed_test

import (
	"bytes"
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	bricksjose "github.com/gaborage/go-bricks/jose"
	"github.com/gaborage/go-bricks/jose/sealed"
	jositest "github.com/gaborage/go-bricks/jose/testing"
)

const (
	docSignLogical = "svc-payments-sign"
	docEncLogical  = "acme-core-enc"
	docSubjectPath = "card"
	// cardPlaintext is the Subject value of every document fixture, spelled once so the
	// byte-identity assertion can locate it in the input.
	cardPlaintext = `{"pan":"` + testPAN + `","exp":"12/29"}`
)

func documentSpec(t *testing.T) *sealed.Spec {
	t.Helper()
	spec, err := sealed.NewDocumentSpec(docSignLogical, docEncLogical, docSubjectPath)
	require.NoError(t, err)
	return spec
}

// sampleDocument is the caller's own serialization: members in a different order than the
// struct's, padded with whitespace encoding/json would never emit.
func sampleDocument() []byte {
	return []byte("{\n  \"amount\" : 1250,\n\t\"" + docSubjectPath + "\": " + cardPlaintext + ",\n  \"orderId\":\"ord-1\"\n}")
}

// prettyDocument is the shape a CLI reads from a file: pretty-printed, with the Subject as
// the FIRST member. The decoder's peek skips the whitespace between the brace and the key,
// so the first-member separator fixup must not key off that offset.
func prettyDocument() []byte {
	return []byte("{\n  \"" + docSubjectPath + "\": " + cardPlaintext + ",\n  \"amount\": 1250\n}")
}

func TestNewDocumentSpecBuildsATypelessSpec(t *testing.T) {
	spec := documentSpec(t)
	assert.Nil(t, spec.Type, "a document spec describes no Go type")
	assert.Equal(t, docSignLogical, spec.SignLogical)
	assert.Equal(t, docEncLogical, spec.EncryptLogical)
	assert.Empty(t, spec.SubjectField, "there is no Go field behind the subject")
	assert.Equal(t, docSubjectPath, spec.SubjectPath)
	assert.Equal(t, []string{docSubjectPath}, spec.SealedPaths())
}

func TestNewDocumentSpecRejectsInvalidArguments(t *testing.T) {
	cases := []struct {
		name    string
		sign    string
		encrypt string
		path    string
		code    string
		kid     string
	}{
		{name: "sign_kid_breaks_the_grammar", sign: "svc payments", encrypt: docEncLogical, path: docSubjectPath, code: sealed.CodeTagKidInvalid, kid: "svc payments"},
		{name: "encrypt_kid_breaks_the_grammar", sign: docSignLogical, encrypt: "acme/core", path: docSubjectPath, code: sealed.CodeTagKidInvalid, kid: "acme/core"},
		{name: "sign_kid_is_a_generation", sign: docSignLogical + "-v2", encrypt: docEncLogical, path: docSubjectPath, code: sealed.CodeTagKidInvalid, kid: docSignLogical + "-v2"},
		{name: "subject_path_empty", sign: docSignLogical, encrypt: docEncLogical, path: "", code: sealed.CodeTagSubjectMissing},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			spec, err := sealed.NewDocumentSpec(tc.sign, tc.encrypt, tc.path)
			assert.Nil(t, spec)
			var jerr *bricksjose.Error
			require.ErrorAs(t, err, &jerr)
			assert.Equal(t, tc.code, jerr.Code)
			assert.Equal(t, tc.kid, jerr.Kid)
			// Sentinel last: it is require, and a wrong sentinel must not abort
			// before the code and kid above, which are independent properties.
			require.ErrorIs(t, err, sealed.ErrTagInvalid)
		})
	}
}

// TestSealDocumentRoundTripsThroughOpen seals bytes no Go type produced and opens them with
// the scanned Spec of the type they describe: one envelope, both doors.
func TestSealDocumentRoundTripsThroughOpen(t *testing.T) {
	k := testKeys(t)
	consumer := jositest.NewTestResolver(map[string]any{signKid: &k.signPriv.PublicKey, encKid: k.encPriv})
	opts := testOptions(t)
	opts.TenantID = "tenant-a"

	wire, err := sealed.SealDocument(sampleDocument(), documentSpec(t), opts)
	require.NoError(t, err)

	var evt paymentAuthorized
	env, err := sealed.Open(wire, testSpec(t), &sealed.OpenOptions{
		EventType: eventType,
		Keys:      consumer,
		Tenant:    sealed.TenantExpectation{Required: true, Expected: "tenant-a"},
	}, &evt)
	require.NoError(t, err)
	assert.Equal(t, sampleEvent(), evt, "the opened event equals the one the document describes")
	assert.Equal(t, signKid, env.SignKid)
	assert.Equal(t, encKid, env.EncKid)
	assert.Equal(t, eventType, env.EventType)
	assert.Equal(t, "tenant-a", env.TenantID)
	assert.NotEmpty(t, env.JTI)
}

// TestSealDocumentPreservesCallerBytes is the property that makes the door worth having: the
// signed payload is the input document with the Subject value swapped and nothing else.
func TestSealDocumentPreservesCallerBytes(t *testing.T) {
	k := testKeys(t)
	doc := sampleDocument()

	wire, err := sealed.SealDocument(doc, documentSpec(t), testOptions(t))
	require.NoError(t, err)

	payload, _, innerJWE := openWire(t, wire, &k.signPriv.PublicKey)
	quoted, err := json.Marshal(innerJWE)
	require.NoError(t, err)
	want := strings.Replace(string(doc), cardPlaintext, string(quoted), 1)
	assert.Equal(t, want, string(payload), "member order, whitespace and every clear byte are the caller's")
	assert.Equal(t, []string{"amount", docSubjectPath, "orderId"}, topLevelKeys(t, payload), "wire order is the document's, not the struct's")
	assert.NotContains(t, string(payload), testPAN)
}

func TestSealDocumentAcceptsAScannedSpec(t *testing.T) {
	k := testKeys(t)
	// encoding/json's own bytes for the same event, sealed through the document door.
	doc, err := json.Marshal(sampleEvent())
	require.NoError(t, err)

	wire, err := sealed.SealDocument(doc, testSpec(t), testOptions(t))
	require.NoError(t, err)
	payload, _, _ := openWire(t, wire, &k.signPriv.PublicKey)
	assert.Equal(t, []string{"orderId", docSubjectPath, "amount"}, topLevelKeys(t, payload))
}

func TestSealDocumentRejectsInvalidDocuments(t *testing.T) {
	cases := []struct {
		name string
		doc  string
	}{
		{name: "case_fold_twin_of_the_subject", doc: `{"Card":{"pan":"other"},"card":` + cardPlaintext + `}`},
		{name: "upper_case_twin_after_the_subject", doc: `{"card":` + cardPlaintext + `,"CARD":1}`},
		{name: "subject_absent", doc: `{"orderId":"ord-1","amount":1250}`},
		{name: "subject_duplicated", doc: `{"card":` + cardPlaintext + `,"card":{"pan":"other"}}`},
		{name: "not_an_object_array", doc: `[]`},
		{name: "not_an_object_empty", doc: ``},
		{name: "trailing_content", doc: `{"card":` + cardPlaintext + `} {"card":1}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			wire, err := sealed.SealDocument([]byte(tc.doc), documentSpec(t), testOptions(t))
			assert.Nil(t, wire)
			var jerr *bricksjose.Error
			require.ErrorAs(t, err, &jerr)
			assert.Equal(t, sealed.CodeDocumentInvalid, jerr.Code)
			// The refusal names the declared subject path and nothing the document carries.
			assert.Contains(t, jerr.Message, `"`+docSubjectPath+`"`)
			assert.NotContains(t, err.Error(), "Card")
			assert.NotContains(t, err.Error(), "CARD")
			assert.NotContains(t, err.Error(), testPAN)
			// Sentinel last: a wrong sentinel must not abort before the PAN and
			// case-fold-twin leak checks above, which are what this test guards.
			require.ErrorIs(t, err, sealed.ErrSealFailed)
		})
	}
}

func TestSealDocumentRejectsInvalidOptions(t *testing.T) {
	spec := documentSpec(t)
	wrongFamily := testOptions(t)
	wrongFamily.SignKid = "svc-orders-sign-v1"
	noEventType := testOptions(t)
	noEventType.EventType = ""

	cases := []struct {
		name string
		spec *sealed.Spec
		opts *sealed.Options
		code string
	}{
		{name: "sign_kid_of_another_family", spec: spec, opts: wrongFamily, code: sealed.CodeKidFamilyMismatch},
		{name: "empty_event_type", spec: spec, opts: noEventType, code: sealed.CodeOptionsInvalid},
		{name: "nil_spec", spec: nil, opts: testOptions(t), code: sealed.CodeOptionsInvalid},
		{name: "nil_options", spec: spec, opts: nil, code: sealed.CodeOptionsInvalid},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			wire, err := sealed.SealDocument(sampleDocument(), tc.spec, tc.opts)
			assert.Nil(t, wire)
			var jerr *bricksjose.Error
			require.ErrorAs(t, err, &jerr)
			assert.Equal(t, tc.code, jerr.Code)
		})
	}
}

// TestTypedDoorsRefuseADocumentSpec pins the one-way relation: a document Spec seals through
// SealDocument only, because neither Seal nor Open can pin a Go type without spec.Type.
func TestTypedDoorsRefuseADocumentSpec(t *testing.T) {
	k := testKeys(t)
	spec := documentSpec(t)

	_, err := sealed.Seal(sampleEvent(), spec, testOptions(t))
	var sealErr *bricksjose.Error
	require.ErrorAs(t, err, &sealErr)
	assert.Equal(t, sealed.CodeOptionsInvalid, sealErr.Code)
	assert.Contains(t, sealErr.Message, "ScanType")

	wire, err := sealed.SealDocument(sampleDocument(), spec, testOptions(t))
	require.NoError(t, err)
	consumer := jositest.NewTestResolver(map[string]any{signKid: &k.signPriv.PublicKey, encKid: k.encPriv})
	var evt paymentAuthorized
	_, err = sealed.Open(wire, spec, &sealed.OpenOptions{EventType: eventType, Keys: consumer}, &evt)
	var openErr *sealed.OpenError
	require.ErrorAs(t, err, &openErr)
	assert.Zero(t, openErr.Rule, "pre-flight, no rule fired")
	var jerr *bricksjose.Error
	require.ErrorAs(t, err, &jerr)
	assert.Equal(t, sealed.CodeOptionsInvalid, jerr.Code)
	assert.Contains(t, jerr.Message, "ScanType")
}

// TestDocumentSpecIsNotAScannedSpec guards the reverse direction: ScanType keeps producing a
// Spec whose Type is set, so the two shapes never converge.
func TestDocumentSpecIsNotAScannedSpec(t *testing.T) {
	scanned := testSpec(t)
	require.Equal(t, reflect.TypeOf(paymentAuthorized{}), scanned.Type)
	assert.Equal(t, "Card", scanned.SubjectField)
	assert.Nil(t, documentSpec(t).Type)
}

// TestOpenDocumentOpensWhatSealDocumentProduced is link 1's core acceptance criterion:
// OpenDocument runs the identical rule chain as Open (same Envelope for the same body), but
// hands back the document with the Subject member ABSENT — not a redaction placeholder,
// since a caller with no Go type (the CLI) decides what belongs there — and the subject
// plaintext separately. A document Spec, which Open refuses, is exactly what this door wants.
func TestOpenDocumentOpensWhatSealDocumentProduced(t *testing.T) {
	k := testKeys(t)
	consumer := jositest.NewTestResolver(map[string]any{signKid: &k.signPriv.PublicKey, encKid: k.encPriv})
	opts := testOptions(t)
	opts.TenantID = "tenant-a"
	wire, err := sealed.SealDocument(sampleDocument(), documentSpec(t), opts)
	require.NoError(t, err)

	openOpts := &sealed.OpenOptions{
		EventType: eventType, Keys: consumer,
		Tenant: sealed.TenantExpectation{Required: true, Expected: "tenant-a"},
	}

	var evt paymentAuthorized
	wantEnv, err := sealed.Open(wire, testSpec(t), openOpts, &evt)
	require.NoError(t, err)

	opened, err := sealed.OpenDocument(wire, documentSpec(t), openOpts)
	require.NoError(t, err)
	assert.Equal(t, wantEnv, opened.Envelope, "the same Envelope Open returns for the same body")

	assert.True(t, json.Valid(opened.Document), "the document handed back is JSON")
	keys := topLevelKeys(t, opened.Document)
	assert.NotContains(t, keys, docSubjectPath, "the subject member is absent, not redacted")
	assert.Equal(t, []string{"amount", "orderId"}, keys, "the other members keep their wire order")
	assert.JSONEq(t, cardPlaintext, string(opened.Subject), "the subject plaintext is returned apart from the document")
}

// TestOpenDocumentOpensAPrettyPrintedDocument is the end-to-end regression for the
// first-member fixup: the Subject is the first member of a pretty-printed document, so a
// fixup keyed off the brace offset leaves `{\n  ,` behind — invalid JSON a CLI would print.
func TestOpenDocumentOpensAPrettyPrintedDocument(t *testing.T) {
	k := testKeys(t)
	consumer := jositest.NewTestResolver(map[string]any{signKid: &k.signPriv.PublicKey, encKid: k.encPriv})
	wire, err := sealed.SealDocument(prettyDocument(), documentSpec(t), testOptions(t))
	require.NoError(t, err)

	opened, err := sealed.OpenDocument(wire, documentSpec(t), &sealed.OpenOptions{EventType: eventType, Keys: consumer})
	require.NoError(t, err)
	assert.True(t, json.Valid(opened.Document), "the document a CLI prints must be JSON")
	assert.Equal(t, []byte("{\n  \"amount\": 1250\n}"), opened.Document, "every clear byte but the subject member is the caller's")
	assert.JSONEq(t, cardPlaintext, string(opened.Subject))
}

// TestOpenDocumentSubjectAtRestoresTheOriginalDocument pins the splice contract SubjectAt
// exists for: a caller that puts the member back at that offset — its key and separator,
// which traveled with the value, plus Subject as the value — gets the pre-seal document back
// byte for byte. A caller splicing a redaction placeholder instead lands in the same place.
// The offset is checked independently against where the member sits in the original, so an
// off-by-one cannot pass. The pretty case is the first-member fixup's end-to-end regression.
func TestOpenDocumentSubjectAtRestoresTheOriginalDocument(t *testing.T) {
	k := testKeys(t)
	consumer := jositest.NewTestResolver(map[string]any{signKid: &k.signPriv.PublicKey, encKid: k.encPriv})

	cases := []struct {
		name string
		doc  []byte
		// before and after bracket Subject: together they spell the member text removeMember
		// took out of the document, separator included.
		before, after string
	}{
		{
			name:   "first_member_compact",
			doc:    []byte(`{"` + docSubjectPath + `":` + cardPlaintext + `,"amount":1250}`),
			before: `"` + docSubjectPath + `":`,
			after:  ",",
		},
		{
			name:   "middle_member",
			doc:    sampleDocument(),
			before: ",\n\t\"" + docSubjectPath + "\": ",
		},
		{
			name:   "pretty_printed_first_member",
			doc:    prettyDocument(),
			before: "\"" + docSubjectPath + "\": ",
			after:  ",\n  ",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			wire, err := sealed.SealDocument(tc.doc, documentSpec(t), testOptions(t))
			require.NoError(t, err)

			opened, err := sealed.OpenDocument(wire, documentSpec(t), &sealed.OpenOptions{EventType: eventType, Keys: consumer})
			require.NoError(t, err)

			member := tc.before + string(opened.Subject) + tc.after
			require.Equal(t, 1, strings.Count(string(tc.doc), member), "the member text must locate one spot in the original")
			assert.Equal(t, bytes.Index(tc.doc, []byte(member)), opened.SubjectAt,
				"SubjectAt is where the member sat, not where it would append")

			restored := slices.Concat(opened.Document[:opened.SubjectAt], []byte(member), opened.Document[opened.SubjectAt:])
			assert.Equal(t, tc.doc, restored, "splicing the member back at SubjectAt reproduces the original byte for byte")
		})
	}
}

// TestOpenDocumentRejectsWiringMistakes mirrors TestOpenRejectsWiringMistakes for the
// type-free door: the same pre-flight, reported with Rule 0, and named after the door the
// caller actually called so a wiring mistake is not attributed to Open.
func TestOpenDocumentRejectsWiringMistakes(t *testing.T) {
	k := testKeys(t)
	consumer := jositest.NewTestResolver(map[string]any{signKid: &k.signPriv.PublicKey, encKid: k.encPriv})
	wire, err := sealed.SealDocument(sampleDocument(), documentSpec(t), testOptions(t))
	require.NoError(t, err)
	spec := documentSpec(t)

	cases := []struct {
		name string
		spec *sealed.Spec
		opts *sealed.OpenOptions
	}{
		{name: "nil_spec", spec: nil, opts: &sealed.OpenOptions{EventType: eventType, Keys: consumer}},
		{name: "nil_opts", spec: spec, opts: nil},
		{name: "nil_keys", spec: spec, opts: &sealed.OpenOptions{EventType: eventType}},
		{name: "empty_event_type", spec: spec, opts: &sealed.OpenOptions{Keys: consumer}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			opened, err := sealed.OpenDocument(wire, tc.spec, tc.opts)
			assert.Nil(t, opened)

			var oe *sealed.OpenError
			require.ErrorAs(t, err, &oe, "every OpenDocument failure is an *OpenError")
			assert.Zero(t, oe.Rule, "pre-flight, no rule fired")
			var je *bricksjose.Error
			require.ErrorAs(t, err, &je)
			assert.Equal(t, sealed.CodeOptionsInvalid, je.Code)
			assert.True(t, strings.HasPrefix(je.Message, "OpenDocument requires "), "the message names the door the caller called: %q", je.Message)
			assert.ErrorIs(t, err, sealed.ErrSealFailed)
		})
	}
}

// TestOpenDocumentRefusesASubjectPlaintextThatIsNotValidJSON is rule 11's shape-free floor:
// the type-free door cannot decode into spec.Type, but it still refuses a plaintext that
// would splice into a document no caller could use. A CLI must never print garbage.
func TestOpenDocumentRefusesASubjectPlaintextThatIsNotValidJSON(t *testing.T) {
	k := loadVectorKeys(t)
	loadVectors(t, k) // primes the shared positive Subject JWE

	cases := []struct {
		name      string
		plaintext string
	}{
		{name: "subject_plaintext_is_not_json", plaintext: "not json at all"},
		{name: "subject_plaintext_is_truncated_json", plaintext: `{"pan":`},
		// The injection the floor exists to block: spliced into the document this reads as a
		// well-formed object, so Open's rule 11 accepts it and the sibling member rides along.
		{name: "subject_plaintext_injects_a_sibling_member", plaintext: `1,"injected":2`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := k.build(t, mutation{inner: func(o *innerOpts) { o.plaintext = []byte(tc.plaintext) }})

			opened, err := sealed.OpenDocument([]byte(body), documentSpec(t), vectorOptions(k, nil))
			assert.Nil(t, opened)

			var oe *sealed.OpenError
			require.ErrorAs(t, err, &oe)
			assert.Equal(t, sealed.CodePayloadUndecodable, oe.Err.Code)
			assert.Equal(t, 11, oe.Rule)
			assert.Equal(t, "subject plaintext is not a valid JSON value", oe.Err.Message,
				"the message names what the floor judges: the plaintext, not the document")
			assert.NotContains(t, err.Error(), tc.plaintext, "the refusal carries no plaintext byte")
			require.ErrorIs(t, err, sealed.ErrOpenFailed)
		})
	}
}

// TestOpenDocumentRefusalsMatchOpen table-drives every published negative vector through both
// doors: OpenDocument must refuse with the identical *OpenError (Code, Rule, Details) Open
// produces for the same body, since rules 1-10 are the shared openCore. Rule 11 is excluded:
// it is Open's own "decode into spec.Type" step (TestEveryCodeNamesItsRules already pins
// CodePayloadUndecodable to both {10, 11} as a deliberate two-rule code, not a drift), and
// OpenDocument has no type to decode into — see
// TestOpenDocumentAcceptsWhatOpenRefusesOnlyForItsType for that divergence.
func TestOpenDocumentRefusalsMatchOpen(t *testing.T) {
	k := loadVectorKeys(t)
	vf := loadVectors(t, k)
	require.NotEmpty(t, vf.Vectors)

	tested := 0
	for _, tc := range vf.Vectors {
		if tc.Rule == 11 {
			continue
		}
		tested++
		t.Run(tc.Name, func(t *testing.T) {
			opts := vectorOptions(k, tc.Tenant)

			var evt paymentAuthorized
			_, wantErr := sealed.Open([]byte(tc.Body), testSpec(t), opts, &evt)
			require.Error(t, wantErr)
			var wantOE *sealed.OpenError
			require.ErrorAs(t, wantErr, &wantOE)

			opened, gotErr := sealed.OpenDocument([]byte(tc.Body), documentSpec(t), opts)
			require.Error(t, gotErr)
			assert.Nil(t, opened)

			var gotOE *sealed.OpenError
			require.ErrorAs(t, gotErr, &gotOE)
			assert.Equal(t, wantOE.Err.Code, gotOE.Err.Code)
			assert.Equal(t, wantOE.Rule, gotOE.Rule)
			assert.Equal(t, wantOE.Details, gotOE.Details)
		})
	}
	assert.Positive(t, tested, "the rule-11 exclusion must not empty the table")
}

// TestOpenDocumentAcceptsWhatOpenRefusesOnlyForItsType is the one deliberate divergence from
// TestOpenDocumentRefusalsMatchOpen: the "opened_document_wrong_shape" vector's Subject
// plaintext is a JSON string where paymentAuthorized.Card expects an object, so Open refuses
// at rule 11 (SEAL_PAYLOAD_UNDECODABLE) decoding into spec.Type. OpenDocument never decodes
// into a type — it splices the plaintext out and hands it back as bytes — so the same body
// opens cleanly through the document door.
func TestOpenDocumentAcceptsWhatOpenRefusesOnlyForItsType(t *testing.T) {
	k := loadVectorKeys(t)
	vf := loadVectors(t, k)

	var tc vector
	for _, v := range vf.Vectors {
		if v.Name == "opened_document_wrong_shape" {
			tc = v
		}
	}
	require.Equal(t, "opened_document_wrong_shape", tc.Name, "fixture vector must exist")
	opts := vectorOptions(k, tc.Tenant)

	var evt paymentAuthorized
	_, err := sealed.Open([]byte(tc.Body), testSpec(t), opts, &evt)
	var oe *sealed.OpenError
	require.ErrorAs(t, err, &oe)
	require.Equal(t, 11, oe.Rule, "fixture vector must still fail Open at rule 11")

	opened, err := sealed.OpenDocument([]byte(tc.Body), documentSpec(t), opts)
	require.NoError(t, err)
	assert.NotNil(t, opened.Envelope)
	assert.NotContains(t, topLevelKeys(t, opened.Document), docSubjectPath)
	assert.JSONEq(t, `"not-an-object"`, string(opened.Subject))
}
