package keyname

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// These tables pin the grammar config, keystore, jose and jose/sealed judge
// names by: the rows for one-segment names are verdicts the four copies gave
// before the grammar moved here, and ADR-144 adds the dotted rows.

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
		{"kid_interior_dot", ValidKid, "a.b", true},
		{"kid_dotted_mixed", ValidKid, "Svc_x.pay-ments.v1", true},
		{"kid_leading_dot", ValidKid, ".x", false},
		{"kid_trailing_dot", ValidKid, "x.", false},
		{"kid_double_dot", ValidKid, "x..y", false},
		{"kid_lone_dot", ValidKid, ".", false},
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

		{"name_one_segment", ValidName, "webhook-signing", true},
		{"name_dotted", ValidName, "tokens.our", true},
		{"name_mixed", ValidName, "a.b-c.d", true},
		{"name_reserved_word_alone", ValidName, "secret", true},
		{"name_reserved_word_after_dot", ValidName, "webhook.secret", true},
		{"name_empty", ValidName, "", false},
		{"name_leading_dot", ValidName, ".x", false},
		{"name_trailing_dot", ValidName, "x.", false},
		{"name_double_dot", ValidName, "x..y", false},
		{"name_uppercase", ValidName, "Tokens.our", false},
		{"name_underscore", ValidName, "tokens_our", false},
		{"name_trailing_newline", ValidName, "tokens.our\n", false},
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
		{name: "dotted", in: "svc.payments", want: LogicalOK},
		{name: "dotted_v_letters", in: "svc.vault", want: LogicalOK},
		{name: "dotted_v_without_digits", in: "svc.v", want: LogicalOK},
		{name: "one_segment_v1", in: "v1", want: LogicalOK},
		{name: "empty", in: "", want: LogicalNotKid},
		{name: "double_dot", in: "svc..payments", want: LogicalNotKid},
		{name: "over_cap", in: strings.Repeat("a", MaxLogicalLen+1), want: LogicalTooLong},
		{name: "marker", in: "svc-sign-v3", want: LogicalEndsInMarker},
		{name: "marker_leading_zero", in: "svc-sign-v01", want: LogicalEndsInMarker},
		{name: "marker_zero", in: "svc-sign-v0", want: LogicalEndsInMarker},
		{name: "segment_marker", in: "svc.sign.v3", want: LogicalEndsInMarker},
		{name: "segment_marker_undotted_family", in: "audit.v1", want: LogicalEndsInMarker},
		{name: "segment_marker_leading_zero", in: "svc.v01", want: LogicalEndsInMarker},
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
		{name: "family_bad_alphabet", in: "svc_x!-v1", wantLogical: "svc_x!", wantVersion: "v1", want: Malformed},
		// ADR-144: the family fixes the marker.
		{name: "dotted_family", in: "payments.sign.v1", wantLogical: "payments.sign", wantVersion: "v1", want: Generation},
		{name: "dotted_family_multi_digit", in: "a.b.c.v207", wantLogical: "a.b.c", wantVersion: "v207", want: Generation},
		{name: "hyphen_in_dotted_family", in: "svc.payments-sign.v2", wantLogical: "svc.payments-sign", wantVersion: "v2", want: Generation},
		{name: "dotted_family_hyphen_marker", in: "payments.sign-v1", wantLogical: "payments.sign", wantVersion: "v1", want: Malformed},
		{name: "undotted_family_segment_marker", in: "audit.v1", wantLogical: "audit", wantVersion: "v1", want: Malformed},
		{name: "hyphen_family_segment_marker", in: "payments-sign.v1", wantLogical: "payments-sign", wantVersion: "v1", want: Malformed},
		{name: "segment_zero_version", in: "x.y.v0", wantLogical: "x.y", wantVersion: "v0", want: Malformed},
		{name: "segment_leading_zero_version", in: "x.y.v01", wantLogical: "x.y", wantVersion: "v01", want: Malformed},
		{name: "segment_family_marked", in: "x.v1.v2", wantLogical: "x.v1", wantVersion: "v2", want: Malformed},
		{name: "hyphen_family_marked_segment", in: "x-v1.v2", wantLogical: "x-v1", wantVersion: "v2", want: Malformed},
		{name: "dotted_family_marked_hyphen", in: "a.b-v1-v2", wantLogical: "a.b-v1", wantVersion: "v2", want: Malformed},
		{name: "segment_empty_family", in: ".v1", wantLogical: "", wantVersion: "v1", want: Malformed},
		{name: "dotted_ordinary", in: "tokens.our", want: Ordinary},
		{name: "one_segment_v1", in: "v1", want: Ordinary},
		{name: "segment_v_letters", in: "x.vault", want: Ordinary},
		{name: "segment_uppercase_v", in: "x.y.V2", want: Ordinary},
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
	assert.Equal(t, "payments.sign.v2", GenerationName("payments.sign", "v2"))
	assert.Equal(t, "svc.payments-sign.v2", GenerationName("svc.payments-sign", "v2"))

	// Composing and splitting are inverses over every well-formed Generation.
	for _, family := range []string{"payments-sign", "payments.sign"} {
		logical, version, form := SplitGeneration(GenerationName(family, "v12"))
		assert.Equal(t, Generation, form, family)
		assert.Equal(t, family, logical)
		assert.Equal(t, "v12", version)
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

// checkSplit asserts the split invariants for one name: a Generation composes
// back from its parts with a Logical family and a canonical version, and is no
// Logical kid itself; an Ordinary name returns no parts.
func checkSplit(t *testing.T, name string) {
	t.Helper()
	logical, version, form := SplitGeneration(name)
	switch form {
	case Generation:
		if got := GenerationName(logical, version); got != name {
			t.Fatalf("GenerationName(%q, %q) = %q, want %q", logical, version, got, name)
		}
		if CheckLogical(logical) != LogicalOK || !ValidVersion(version) {
			t.Fatalf("Generation %q has family %q, version %q outside the grammar", name, logical, version)
		}
		if fault := CheckLogical(name); fault != LogicalEndsInMarker && fault != LogicalTooLong {
			t.Fatalf("Generation %q passes as a Logical kid", name)
		}
	case Ordinary:
		if logical != "" || version != "" {
			t.Fatalf("Ordinary %q returned parts %q, %q", name, logical, version)
		}
	case Malformed:
	}
}

// FuzzSplitGeneration pins the round trip: every Generation is composed back
// from its parts, and every family the split returns is a Logical kid.
func FuzzSplitGeneration(f *testing.F) {
	for _, seed := range []string{
		"payments-sign-v1", "payments.sign.v1", "payments.sign-v1", "audit.v1",
		"x.v1.v2", "x-v1-v2", "tokens.our", "v1", ".v1", "-v1", "a.b.c.v207",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, name string) {
		if Fold(Fold(name)) != Fold(name) {
			t.Fatalf("Fold is not idempotent on %q", name)
		}
		checkSplit(t, name)
	})
}
