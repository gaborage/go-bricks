package logger

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// consoleLine emits one message through a real console-mode logger and returns the
// captured output. os.Stdout is redirected BEFORE construction because the ConsoleWriter
// captures it then.
func consoleLine(t *testing.T, msg string) string {
	t.Helper()
	return captureStdout(t, func() {
		New("info", true).Info().Msg(msg)
	})
}

func TestConsoleQuotesMessageWithControlBytes(t *testing.T) {
	out := consoleLine(t, "GET /test\n[FORGED] level=info msg=owned\x00tail completed")
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	require.Len(t, lines, 1, "one event must render as one line, got: %q", out)
	assert.Contains(t, lines[0], `\n[FORGED]`)
	assert.Contains(t, lines[0], `\x00tail`)
	assert.NotContains(t, lines[0], "\x00")
}

func TestConsoleLeavesOrdinaryMessagesUnquoted(t *testing.T) {
	cases := map[string]string{
		"spaces":              "GET /api/users completed in 123ms with status 2xx",
		"printable_utf8":      "héllo wörld — ünïcode ✓",
		"quote_and_backslash": `path "a\b"`,
	}
	for name, msg := range cases {
		t.Run(name, func(t *testing.T) {
			out := consoleLine(t, msg)
			assert.Contains(t, out, msg)
			assert.NotContains(t, out, `"`+strings.ReplaceAll(msg, `"`, `\"`)+`"`, "message must not be Go-quoted")
		})
	}
}

func TestHasControlBytesBoundaries(t *testing.T) {
	// A/B pairs on both edges of the predicate: the mutation gate flips < and ==.
	cases := map[string]struct {
		in   string
		want bool
	}{
		"0x1f_unit_separator": {"a\x1fb", true},
		"0x20_space":          {"a b", false},
		"0x7e_tilde":          {"a~b", false},
		"0x7f_del":            {"a\x7fb", true},
		"0x00_nul":            {"a\x00b", true},
		"0x0a_newline":        {"a\nb", true},
		"0x80_high_bit":       {"a\x80b", false},
		"empty":               {"", false},
		"utf8_multibyte":      {"aéb", false},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, tc.want, hasControlBytes(tc.in))
		})
	}
}

func TestJSONModeUnchangedForControlBytes(t *testing.T) {
	out := captureStdout(t, func() {
		New("info", false).Info().Msg("a\nb")
	})
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	require.Len(t, lines, 1)
	assert.Contains(t, lines[0], `"message":"a\nb"`)
}
