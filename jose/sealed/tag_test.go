package sealed_test

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/jose"
	"github.com/gaborage/go-bricks/jose/sealed"
)

type cardData struct {
	PAN string `json:"pan"`
	Exp string `json:"exp"`
}

// paymentAuthorized is the canonical fixture: the Subject sits between two clear members so
// the wire-order assertion can tell a splice from a rebuild.
type paymentAuthorized struct {
	_       struct{}  `seal:"sign=svc-payments-sign,encrypt=acme-core-enc"`
	OrderID string    `json:"orderId"`
	Card    *cardData `json:"card" seal:"subject"`
	Amount  int64     `json:"amount"`
}

type plainEvent struct {
	ID string `json:"id"`
}

func TestScanTypeValidDeclaration(t *testing.T) {
	spec, err := sealed.ScanType(reflect.TypeOf(paymentAuthorized{}))
	require.NoError(t, err)
	require.NotNil(t, spec)
	assert.Equal(t, reflect.TypeOf(paymentAuthorized{}), spec.Type)
	assert.Equal(t, "svc-payments-sign", spec.SignLogical)
	assert.Equal(t, "acme-core-enc", spec.EncryptLogical)
	assert.Equal(t, "Card", spec.SubjectField)
	assert.Equal(t, "card", spec.SubjectPath)
	assert.Equal(t, []string{"card"}, spec.SealedPaths())
}

func TestScanTypeUnwrapsPointersAndUsesFieldNameWithoutJSONTag(t *testing.T) {
	type evt struct {
		_    struct{} `seal:"encrypt=enc-fam, sign=sign-fam"`
		Card cardData `seal:"subject"`
	}
	spec, err := sealed.ScanType(reflect.TypeOf((**evt)(nil)))
	require.NoError(t, err)
	assert.Equal(t, reflect.TypeOf(evt{}), spec.Type)
	assert.Equal(t, "sign-fam", spec.SignLogical)
	assert.Equal(t, "enc-fam", spec.EncryptLogical)
	assert.Equal(t, "Card", spec.SubjectPath, "json name defaults to the Go field name")
}

func TestScanTypeReturnsNilForUntaggedTypes(t *testing.T) {
	for name, typ := range map[string]reflect.Type{
		"nil_type":     nil,
		"plain_struct": reflect.TypeOf(plainEvent{}),
		"non_struct":   reflect.TypeOf("string"),
		"jose_tagged": reflect.TypeOf(struct {
			_    struct{} `jose:"sign=a,encrypt=b"`
			Body string   `json:"body"`
		}{}),
	} {
		t.Run(name, func(t *testing.T) {
			spec, err := sealed.ScanType(typ)
			assert.NoError(t, err)
			assert.Nil(t, spec)
		})
	}
}

func TestScanTypeScanErrors(t *testing.T) {
	type sub struct{ V string }
	type embeddedSubject struct {
		_   struct{} `seal:"sign=s,encrypt=e"`
		sub `seal:"subject"`
	}
	cases := []struct {
		name string
		typ  reflect.Type
		code string
		msg  string
	}{
		{name: "zero_subjects", typ: reflect.TypeOf(struct {
			_  struct{} `seal:"sign=s,encrypt=e"`
			ID string   `json:"id"`
		}{}), code: sealed.CodeTagSubjectMissing, msg: "no field is tagged"},
		{name: "two_subjects", typ: reflect.TypeOf(struct {
			_ struct{} `seal:"sign=s,encrypt=e"`
			A string   `json:"a" seal:"subject"`
			B string   `json:"b" seal:"subject"`
		}{}), code: sealed.CodeTagSubjectMultiple, msg: "2 fields tagged"},
		{name: "subject_without_sentinel", typ: reflect.TypeOf(struct {
			A string `json:"a" seal:"subject"`
		}{}), code: sealed.CodeTagSentinelMissing, msg: "without a sentinel"},
		{name: "subject_json_skip", typ: reflect.TypeOf(struct {
			_ struct{} `seal:"sign=s,encrypt=e"`
			A string   `json:"-" seal:"subject"`
		}{}), code: sealed.CodeTagSubjectInvalid, msg: `json:"-"`},
		{name: "subject_embedded", typ: reflect.TypeOf(embeddedSubject{}), code: sealed.CodeTagSubjectInvalid, msg: "embedded"},
		{name: "subject_unexported", typ: reflect.TypeOf(struct {
			_ struct{} `seal:"sign=s,encrypt=e"`
			a string   `seal:"subject"`
		}{}), code: sealed.CodeTagSubjectInvalid, msg: "unexported"},
		{name: "subject_omitempty", typ: reflect.TypeOf(struct {
			_ struct{} `seal:"sign=s,encrypt=e"`
			A *sub     `json:"a,omitempty" seal:"subject"`
		}{}), code: sealed.CodeTagSubjectInvalid, msg: `"omitempty"`},
		{name: "subject_omitzero", typ: reflect.TypeOf(struct {
			_ struct{} `seal:"sign=s,encrypt=e"`
			A sub      `json:"a,omitzero" seal:"subject"`
		}{}), code: sealed.CodeTagSubjectInvalid, msg: `"omitzero"`},
		{name: "sibling_case_folds_before_subject", typ: reflect.TypeOf(struct {
			_   struct{} `seal:"sign=s,encrypt=e"`
			PAN string   `json:"PAN"`
			Pan string   `json:"pan" seal:"subject"`
		}{}), code: sealed.CodeTagSubjectInvalid, msg: "case-folds"},
		{name: "sibling_case_folds_after_subject", typ: reflect.TypeOf(struct {
			_   struct{} `seal:"sign=s,encrypt=e"`
			Pan string   `json:"pan" seal:"subject"`
			PAN string   `json:"PAN"`
		}{}), code: sealed.CodeTagSubjectInvalid, msg: "case-folds"},
		{name: "sibling_case_folds_by_field_name", typ: reflect.TypeOf(struct {
			_    struct{} `seal:"sign=s,encrypt=e"`
			Card string   `seal:"subject"`
			CARD string
		}{}), code: sealed.CodeTagSubjectInvalid, msg: "case-folds"},
		{name: "kid_bad_alphabet", typ: reflect.TypeOf(struct {
			_ struct{} `seal:"sign=svc.sign,encrypt=e"`
			A string   `json:"a" seal:"subject"`
		}{}), code: sealed.CodeTagKidInvalid, msg: "must match"},
		{name: "kid_too_long", typ: reflect.TypeOf(struct {
			_ struct{} `seal:"sign=s,encrypt=eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"`
			A string   `json:"a" seal:"subject"`
		}{}), code: sealed.CodeTagKidInvalid, msg: "exceeds 64"},
		{name: "kid_ends_in_generation", typ: reflect.TypeOf(struct {
			_ struct{} `seal:"sign=svc-sign-v2,encrypt=e"`
			A string   `json:"a" seal:"subject"`
		}{}), code: sealed.CodeTagKidInvalid, msg: "generation name"},
		{name: "sentinel_empty", typ: reflect.TypeOf(struct {
			_ struct{} `seal:""`
			A string   `json:"a" seal:"subject"`
		}{}), code: sealed.CodeTagInvalid, msg: "is empty"},
		{name: "sentinel_missing_encrypt", typ: reflect.TypeOf(struct {
			_ struct{} `seal:"sign=s"`
			A string   `json:"a" seal:"subject"`
		}{}), code: sealed.CodeTagInvalid, msg: "both sign= and encrypt="},
		{name: "sentinel_missing_sign", typ: reflect.TypeOf(struct {
			_ struct{} `seal:"encrypt=e"`
			A string   `json:"a" seal:"subject"`
		}{}), code: sealed.CodeTagInvalid, msg: "both sign= and encrypt="},
		{name: "sentinel_unknown_key", typ: reflect.TypeOf(struct {
			_ struct{} `seal:"sign=s,encrypt=e,verify=v"`
			A string   `json:"a" seal:"subject"`
		}{}), code: sealed.CodeTagInvalid, msg: `unknown seal sentinel key "verify"`},
		{name: "sentinel_duplicate_key", typ: reflect.TypeOf(struct {
			_ struct{} `seal:"sign=s,sign=t,encrypt=e"`
			A string   `json:"a" seal:"subject"`
		}{}), code: sealed.CodeTagInvalid, msg: "more than once"},
		{name: "sentinel_malformed_pair", typ: reflect.TypeOf(struct {
			_ struct{} `seal:"sign,encrypt=e"`
			A string   `json:"a" seal:"subject"`
		}{}), code: sealed.CodeTagInvalid, msg: "expected key=value"},
		{name: "sentinel_empty_value", typ: reflect.TypeOf(struct {
			_ struct{} `seal:"sign=,encrypt=e"`
			A string   `json:"a" seal:"subject"`
		}{}), code: sealed.CodeTagInvalid, msg: "expected key=value"},
		{name: "sentinel_twice", typ: reflect.TypeOf(struct {
			_ struct{} `seal:"sign=s,encrypt=e"`
			X struct{} `seal:"sign=s,encrypt=e"`
			A string   `json:"a" seal:"subject"`
		}{}), code: sealed.CodeTagInvalid, msg: "declared twice"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			spec, err := sealed.ScanType(tc.typ)
			assert.Nil(t, spec)
			require.Error(t, err)
			assert.ErrorIs(t, err, sealed.ErrTagInvalid)
			var jerr *jose.Error
			require.True(t, errors.As(err, &jerr))
			assert.Equal(t, tc.code, jerr.Code)
			assert.Contains(t, jerr.Message, tc.msg)
			assert.False(t, strings.Contains(jerr.Message, "%!"), "message must be fully formatted")
		})
	}
}

func TestScanTypeIgnoresSiblingsThatCannotReachTheWire(t *testing.T) {
	type embedded struct{ Inner string }
	spec, err := sealed.ScanType(reflect.TypeOf(struct {
		_        struct{}  `seal:"sign=s,encrypt=e"`
		Skipped  string    `json:"-"`
		Other    string    `json:"CARD_"`
		embedded           // embedded: promoted members, no member of its own
		Card     *cardData `json:"card" seal:"subject"`
	}{}))
	require.NoError(t, err)
	assert.Equal(t, "card", spec.SubjectPath)
}

func TestScanTypeKidInvalidCarriesTheKid(t *testing.T) {
	_, err := sealed.ScanType(reflect.TypeOf(struct {
		_ struct{} `seal:"sign=s,encrypt=bad.kid"`
		A string   `json:"a" seal:"subject"`
	}{}))
	var jerr *jose.Error
	require.True(t, errors.As(err, &jerr))
	assert.Equal(t, "bad.kid", jerr.Kid)
	assert.Contains(t, jerr.Message, "for encrypt")
}
