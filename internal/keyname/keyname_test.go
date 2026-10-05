package keyname

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// These tables pin the grammar as config, keystore, jose and jose/sealed spelled
// it before it moved here: every row is a verdict one of those copies gave.

func TestValidSectionName(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want bool
	}{
		{name: "lowercase", in: "reportdb", want: true},
		{name: "digits_and_hyphen", in: "report-db-2", want: true},
		{name: "single_hyphen", in: "-", want: true},
		{name: "empty", in: "", want: false},
		{name: "underscore", in: "report_db", want: false},
		{name: "uppercase", in: "ReportDB", want: false},
		{name: "dot", in: "report.db", want: false},
		{name: "trailing_newline", in: "reportdb\n", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, ValidSectionName(tt.in))
		})
	}
}

func TestValidKid(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want bool
	}{
		{name: "mixed_alphabet", in: "Svc_payments-sign-v1", want: true},
		{name: "single_char", in: "a", want: true},
		{name: "empty", in: "", want: false},
		{name: "dot", in: "a.b", want: false},
		{name: "colon", in: "family:jti", want: false},
		{name: "space", in: "sign v1", want: false},
		{name: "unicode", in: "kéy", want: false},
		{name: "trailing_newline", in: "k\n", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, ValidKid(tt.in))
		})
	}
}

func TestValidVersion(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want bool
	}{
		{name: "v1", in: "v1", want: true},
		{name: "v10", in: "v10", want: true},
		{name: "zero", in: "v0", want: false},
		{name: "leading_zero", in: "v01", want: false},
		{name: "no_digits", in: "v", want: false},
		{name: "no_v", in: "1", want: false},
		{name: "uppercase_v", in: "V1", want: false},
		{name: "hyphen_kept", in: "-v1", want: false},
		{name: "trailing_newline", in: "v1\n", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, ValidVersion(tt.in))
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
