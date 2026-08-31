package ledgererr

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
)

// TestBoundMakesArbitraryTextSafeToStore is the helper's own table, moved here
// with it from the outbox relay. The relay keeps its own test that the value
// REACHING the ledger is bounded, which is the property that matters there.
func TestBoundMakesArbitraryTextSafeToStore(t *testing.T) {
	tests := []struct {
		name      string
		in        string
		wantExact string
		truncated bool
	}{
		{name: "short_text_passes_through", in: "broker rejected", wantExact: "broker rejected"},
		{name: "empty_stays_empty", in: "", wantExact: ""},
		// The column is read back into logs and dashboards, so a broker-supplied
		// newline must not be able to forge a line there.
		{
			name:      "control_bytes_become_spaces",
			in:        "publish failed\r\nlevel=error msg=\"forged\"\x00",
			wantExact: "publish failed  level=error msg=\"forged\" ",
		},
		// PostgreSQL rejects invalid UTF-8 outright: the UPDATE would fail and
		// retry_count would never advance, retrying forever over text nobody reads.
		{name: "invalid_utf8_is_dropped", in: "err: " + string([]byte{0xff, 0xfe}) + " tail", wantExact: "err:  tail"},
		// The pair that matters: invalid BYTES are dropped by ToValidUTF8 above,
		// so by the time the mapping runs, a U+FFFD is a character the sender
		// actually wrote. Substituting it would silently discard real content,
		// and the two cases together pin the seam between the two behaviors.
		{
			name:      "a_genuine_replacement_character_survives",
			in:        "broker said \ufffd here",
			wantExact: "broker said \ufffd here",
		},
		{name: "at_the_cap_is_untouched", in: strings.Repeat("a", MaxBytes), wantExact: strings.Repeat("a", MaxBytes)},
		{name: "one_over_the_cap_truncates", in: strings.Repeat("a", MaxBytes+1), truncated: true},
		{name: "pathological_10kb_truncates", in: strings.Repeat("broker unreachable; ", 512), truncated: true},
		{name: "multibyte_truncates_on_a_rune_boundary", in: strings.Repeat("日", 5000), truncated: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Bound(tt.in)

			assert.LessOrEqual(t, len(got), MaxBytes, "the ledger never receives more than the cap")
			assert.True(t, utf8.ValidString(got), "invalid UTF-8 would be rejected by PostgreSQL and fail the UPDATE")
			assert.NotContains(t, got, "\x00")
			assert.NotContains(t, got, "\n")
			assert.NotContains(t, got, "\r")

			if tt.truncated {
				assert.True(t, strings.HasSuffix(got, TruncationMarker),
					"a shortened error says so, or a reader cannot tell it from a short one")
			} else {
				assert.Equal(t, tt.wantExact, got)
			}
		})
	}
}
