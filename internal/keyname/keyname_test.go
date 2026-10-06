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

func TestValidName(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want bool
	}{
		{name: "one_segment", in: "webhook-signing", want: true},
		{name: "dotted", in: "tokens.our", want: true},
		{name: "mixed", in: "a.b-c.d", want: true},
		{name: "reserved_word_alone", in: "secret", want: true},
		{name: "reserved_word_after_dot", in: "webhook.secret", want: true},
		{name: "empty", in: "", want: false},
		{name: "leading_dot", in: ".x", want: false},
		{name: "trailing_dot", in: "x.", want: false},
		{name: "double_dot", in: "x..y", want: false},
		{name: "uppercase", in: "Tokens.our", want: false},
		{name: "underscore", in: "tokens_our", want: false},
		{name: "trailing_newline", in: "tokens.our\n", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, ValidName(tt.in))
		})
	}
}

func TestReservedAfterDot(t *testing.T) {
	tests := []struct {
		name     string
		in       string
		wantWord string
		wantBad  bool
	}{
		{name: "after_dot", in: "webhook.secret", wantWord: "secret", wantBad: true},
		{name: "deep", in: "a.b.pkcs12.c", wantWord: "pkcs12", wantBad: true},
		{name: "first_of_two", in: "a.public.private", wantWord: "public", wantBad: true},
		{name: "first_segment", in: "secret.x", wantBad: false},
		{name: "one_segment", in: "private", wantBad: false},
		{name: "near_miss", in: "webhook.secrets", wantBad: false},
		{name: "hyphen_joined", in: "webhook-secret", wantBad: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			word, bad := ReservedAfterDot(tt.in)
			assert.Equal(t, tt.wantBad, bad)
			assert.Equal(t, tt.wantWord, word)
		})
	}
}

func TestValidEntryName(t *testing.T) {
	assert.True(t, ValidEntryName("tokens.our"))
	assert.True(t, ValidEntryName("secret"), "a first segment is never a field")
	assert.False(t, ValidEntryName("webhook.secret"))
	assert.False(t, ValidEntryName("tokens_our"))
}

func TestFirstFoldClashAndDottedPrefix(t *testing.T) {
	earlier, later, found := FirstFoldClash([]string{"a", "a-b.c", "a.b-c", "tokens-our", "tokens.our"})
	assert.True(t, found)
	assert.Equal(t, "a-b.c", earlier)
	assert.Equal(t, "a.b-c", later)
	_, _, found = FirstFoldClash([]string{"tokens-our", "tokens.ours"})
	assert.False(t, found)

	prefix, name, found := FirstDottedPrefix([]string{"a", "b", "b.c", "b.c.d"})
	assert.True(t, found)
	assert.Equal(t, "b", prefix)
	assert.Equal(t, "b.c", name)
	prefix, name, found = FirstDottedPrefix([]string{"x.y", "x"})
	assert.True(t, found, "order does not hide a prefix")
	assert.Equal(t, "x", prefix)
	assert.Equal(t, "x.y", name)
	_, _, found = FirstDottedPrefix([]string{"tokens", "tokens-our", "token.s"})
	assert.False(t, found)
}

// TestFirstFoldedPrefix: a pair nests when one fold is a dotted prefix of the
// other's and either name contains '.'. Two names without '.' are exempt, so
// families that boot today (payments-sign beside payments-sign-eu) stay valid.
func TestFirstFoldedPrefix(t *testing.T) {
	tests := []struct {
		name       string
		names      []string
		wantPrefix string
		wantName   string
	}{
		{name: "hyphen_beside_dotted", names: []string{"payments-sign", "payments.sign.eu"}, wantPrefix: "payments-sign", wantName: "payments.sign.eu"},
		{name: "dotted_beside_hyphen", names: []string{"payments-sign-eu", "payments.sign"}, wantPrefix: "payments.sign", wantName: "payments-sign-eu"},
		{name: "raw_prefix", names: []string{"payments", "payments.sign"}, wantPrefix: "payments", wantName: "payments.sign"},
		{name: "order_does_not_hide_it", names: []string{"x.y.z", "x-y"}, wantPrefix: "x-y", wantName: "x.y.z"},
		{name: "first_pair_in_order", names: []string{"a-b", "a.b.c", "d", "d.e"}, wantPrefix: "a-b", wantName: "a.b.c"},
		{name: "hyphen_only_pair_is_exempt", names: []string{"payments-sign", "payments-sign-eu"}},
		{name: "no_nesting", names: []string{"payments.sign", "payments.signer", "tokens-our"}},
		{name: "equal_folds_do_not_nest", names: []string{"tokens-our", "tokens.our"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			prefix, name, found := FirstFoldedPrefix(tt.names)
			assert.Equal(t, tt.wantPrefix != "", found)
			assert.Equal(t, tt.wantPrefix, prefix)
			assert.Equal(t, tt.wantName, name)
		})
	}
}

func TestFoldAndDottedPrefix(t *testing.T) {
	assert.Equal(t, "tokens.our", Fold("tokens-our"))
	assert.Equal(t, Fold("a-b.c"), Fold("a.b-c"))
	assert.Equal(t, "tokens.our", Fold("tokens.our"))

	assert.True(t, IsDottedPrefix("tokens", "tokens.our"))
	assert.True(t, IsDottedPrefix("a.b", "a.b.c"))
	assert.False(t, IsDottedPrefix("token", "tokens.our"))
	assert.False(t, IsDottedPrefix("tokens", "tokens-our"))
	assert.False(t, IsDottedPrefix("tokens", "tokens"))
}
