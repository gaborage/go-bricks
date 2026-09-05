package sealed

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/jose"
)

// TestSentinelsAreDistinctAndNamespaced: the three published sentinels are
// separate identities under errors.Is and carry the package prefix.
func TestSentinelsAreDistinctAndNamespaced(t *testing.T) {
	sentinels := []error{ErrTagInvalid, ErrKidFamilyMismatch, ErrSealFailed}
	for i, a := range sentinels {
		assert.True(t, strings.HasPrefix(a.Error(), "sealed: "), a.Error())
		for j, b := range sentinels {
			assert.Equal(t, i == j, errors.Is(a, b), "%v vs %v", a, b)
		}
	}
}

// TestWireCodesArePinned pins the published wire-protocol code strings: consumers
// match on them, so a respelling is a breaking change.
func TestWireCodesArePinned(t *testing.T) {
	want := map[string]string{
		"CodeTagInvalid":         "SEAL_TAG_INVALID",
		"CodeTagKidInvalid":      "SEAL_TAG_KID_INVALID",
		"CodeTagSentinelMissing": "SEAL_TAG_SENTINEL_MISSING",
		"CodeTagSubjectMissing":  "SEAL_TAG_SUBJECT_MISSING",
		"CodeTagSubjectMultiple": "SEAL_TAG_SUBJECT_MULTIPLE",
		"CodeTagSubjectInvalid":  "SEAL_TAG_SUBJECT_INVALID",
		"CodeKidFamilyMismatch":  "SEAL_KID_FAMILY_MISMATCH",
		"CodeOptionsInvalid":     "SEAL_OPTIONS_INVALID",
		"CodeTypeMismatch":       "SEAL_TYPE_MISMATCH",
		"CodeDocumentInvalid":    "SEAL_DOCUMENT_INVALID",
		"CodeSealFailed":         "SEAL_FAILED",
	}
	got := map[string]string{
		"CodeTagInvalid":         CodeTagInvalid,
		"CodeTagKidInvalid":      CodeTagKidInvalid,
		"CodeTagSentinelMissing": CodeTagSentinelMissing,
		"CodeTagSubjectMissing":  CodeTagSubjectMissing,
		"CodeTagSubjectMultiple": CodeTagSubjectMultiple,
		"CodeTagSubjectInvalid":  CodeTagSubjectInvalid,
		"CodeKidFamilyMismatch":  CodeKidFamilyMismatch,
		"CodeOptionsInvalid":     CodeOptionsInvalid,
		"CodeTypeMismatch":       CodeTypeMismatch,
		"CodeDocumentInvalid":    CodeDocumentInvalid,
		"CodeSealFailed":         CodeSealFailed,
	}
	assert.Equal(t, want, got)

	seen := map[string]bool{}
	for name, code := range got {
		assert.True(t, strings.HasPrefix(code, "SEAL_"), name)
		assert.False(t, seen[code], "duplicate code %s", code)
		seen[code] = true
	}
}

// TestScanErrorCarriesSentinelAndCode: a refused declaration surfaces as a
// *jose.Error that errors.Is the scan sentinel and names the rule by code.
func TestScanErrorCarriesSentinelAndCode(t *testing.T) {
	_, err := ScanType(reflect.TypeOf(struct {
		Card string `json:"card" seal:"subject"`
	}{}))
	require.Error(t, err)
	require.ErrorIs(t, err, ErrTagInvalid)
	require.NotErrorIs(t, err, ErrSealFailed)
	var je *jose.Error
	require.ErrorAs(t, err, &je)
	assert.Equal(t, CodeTagSentinelMissing, je.Code)
}
