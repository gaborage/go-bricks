package sealed_test

import (
	"encoding/json"
	"errors"
	"reflect"
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
			assert.ErrorIs(t, err, sealed.ErrTagInvalid)
			var jerr *bricksjose.Error
			require.True(t, errors.As(err, &jerr))
			assert.Equal(t, tc.code, jerr.Code)
			assert.Equal(t, tc.kid, jerr.Kid)
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
			assert.ErrorIs(t, err, sealed.ErrSealFailed)
			var jerr *bricksjose.Error
			require.True(t, errors.As(err, &jerr))
			assert.Equal(t, sealed.CodeDocumentInvalid, jerr.Code)
			// The refusal names the declared subject path and nothing the document carries.
			assert.Contains(t, jerr.Message, `"`+docSubjectPath+`"`)
			assert.NotContains(t, err.Error(), "Card")
			assert.NotContains(t, err.Error(), "CARD")
			assert.NotContains(t, err.Error(), testPAN)
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
			require.True(t, errors.As(err, &jerr))
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
	require.True(t, errors.As(err, &sealErr))
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
	require.True(t, errors.As(err, &jerr))
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
