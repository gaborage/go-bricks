package keyname

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// These tables pin the grammar as config, keystore, jose and jose/sealed spelled
// it before it moved here: every row is a verdict one of those copies gave.

// TestPredicates pins the yes/no grammars in one table: the predicate is a column, so
// each row reads as the verdict that predicate gives one input.
func TestPredicates(t *testing.T) {
	tests := []struct {
		name  string
		valid func(string) bool
		in    string
		want  bool
	}{
		{"section_lowercase", ValidSectionName, "reportdb", true},
		{"section_digits_and_hyphen", ValidSectionName, "report-db-2", true},
		{"section_single_hyphen", ValidSectionName, "-", true},
		{"section_empty", ValidSectionName, "", false},
		{"section_underscore", ValidSectionName, "report_db", false},
		{"section_uppercase", ValidSectionName, "ReportDB", false},
		{"section_dot", ValidSectionName, "report.db", false},
		{"section_trailing_newline", ValidSectionName, "reportdb\n", false},

		{"kid_mixed_alphabet", ValidKid, "Svc_payments-sign-v1", true},
		{"kid_single_char", ValidKid, "a", true},
		{"kid_empty", ValidKid, "", false},
		{"kid_dot", ValidKid, "a.b", false},
		{"kid_colon", ValidKid, "family:jti", false},
		{"kid_space", ValidKid, "sign v1", false},
		{"kid_unicode", ValidKid, "kéy", false},
		{"kid_trailing_newline", ValidKid, "k\n", false},

		{"version_v1", ValidVersion, "v1", true},
		{"version_v10", ValidVersion, "v10", true},
		{"version_zero", ValidVersion, "v0", false},
		{"version_leading_zero", ValidVersion, "v01", false},
		{"version_no_digits", ValidVersion, "v", false},
		{"version_no_v", ValidVersion, "1", false},
		{"version_uppercase_v", ValidVersion, "V1", false},
		{"version_hyphen_kept", ValidVersion, "-v1", false},
		{"version_trailing_newline", ValidVersion, "v1\n", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, tt.valid(tt.in))
		})
	}
}

func TestCheckLogical(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want LogicalFault
	}{
		{name: "hyphenated", in: "svc-payments-sign", want: LogicalOK},
		{name: "underscore_and_upper", in: "Svc_Payments", want: LogicalOK},
		{name: "at_cap", in: strings.Repeat("a", MaxLogicalLen), want: LogicalOK},
		{name: "v_without_digits", in: "svc-v", want: LogicalOK},
		{name: "digits_without_hyphen", in: "svcv3", want: LogicalOK},
		{name: "dash_v_letters", in: "svc-vault", want: LogicalOK},
		{name: "empty", in: "", want: LogicalNotKid},
		{name: "dot", in: "svc.payments", want: LogicalNotKid},
		{name: "over_cap", in: strings.Repeat("a", MaxLogicalLen+1), want: LogicalTooLong},
		{name: "marker", in: "svc-sign-v3", want: LogicalEndsInMarker},
		{name: "marker_leading_zero", in: "svc-sign-v01", want: LogicalEndsInMarker},
		{name: "marker_zero", in: "svc-sign-v0", want: LogicalEndsInMarker},
		// Order: the alphabet is judged before the length, the length before the marker.
		{name: "alphabet_before_length", in: strings.Repeat("a", MaxLogicalLen) + ".", want: LogicalNotKid},
		{name: "length_before_marker", in: strings.Repeat("a", MaxLogicalLen-2) + "-v1", want: LogicalTooLong},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, CheckLogical(tt.in))
		})
	}
}

func TestSplitGeneration(t *testing.T) {
	tests := []struct {
		name        string
		in          string
		wantLogical string
		wantVersion string
		want        Form
	}{
		{name: "payments_sign_v1", in: "payments-sign-v1", wantLogical: "payments-sign", wantVersion: "v1", want: Generation},
		{name: "svc_payments_sign_v2", in: "svc-payments-sign-v2", wantLogical: "svc-payments-sign", wantVersion: "v2", want: Generation},
		{name: "multi_digit", in: "k-v207", wantLogical: "k", wantVersion: "v207", want: Generation},
		{name: "family_at_cap", in: strings.Repeat("a", MaxLogicalLen) + "-v1", wantLogical: strings.Repeat("a", MaxLogicalLen), wantVersion: "v1", want: Generation},
		// The keystore never parses the version, so no integer bound applies here.
		{name: "long_version", in: "svc-v99999999999999999999", wantLogical: "svc", wantVersion: "v99999999999999999999", want: Generation},
		{name: "zero_version", in: "x-v0", wantLogical: "x", wantVersion: "v0", want: Malformed},
		{name: "leading_zero_version", in: "x-v01", wantLogical: "x", wantVersion: "v01", want: Malformed},
		{name: "last_marker_wins", in: "x-v1-v2", wantLogical: "x-v1", wantVersion: "v2", want: Malformed},
		{name: "empty_family", in: "-v1", wantLogical: "", wantVersion: "v1", want: Malformed},
		{name: "family_over_cap", in: strings.Repeat("a", MaxLogicalLen+1) + "-v1", wantLogical: strings.Repeat("a", MaxLogicalLen+1), wantVersion: "v1", want: Malformed},
		{name: "family_bad_alphabet", in: "svc.x-v1", wantLogical: "svc.x", wantVersion: "v1", want: Malformed},
		{name: "ordinary", in: "signing", want: Ordinary},
		{name: "v_without_digits", in: "svc-v", want: Ordinary},
		{name: "digits_without_v", in: "svc-2", want: Ordinary},
		{name: "uppercase_v", in: "svc-V2", want: Ordinary},
		{name: "marker_not_last", in: "x-v1-a", want: Ordinary},
		{name: "trailing_newline", in: "x-v1\n", want: Ordinary},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			logical, version, form := SplitGeneration(tt.in)
			assert.Equal(t, tt.want, form)
			assert.Equal(t, tt.wantLogical, logical)
			assert.Equal(t, tt.wantVersion, version)
		})
	}
}

func TestGenerationName(t *testing.T) {
	assert.Equal(t, "svc-payments-sign-v2", GenerationName("svc-payments-sign", "v2"))

	// Composing and splitting are inverses over every well-formed Generation.
	logical, version, form := SplitGeneration(GenerationName("payments-sign", "v12"))
	assert.Equal(t, Generation, form)
	assert.Equal(t, "payments-sign", logical)
	assert.Equal(t, "v12", version)
}
