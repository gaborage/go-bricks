package logger

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestResolvePretty(t *testing.T) {
	tests := []struct {
		name           string
		format         string
		legacyPretty   bool
		otlpLogsActive bool
		isTerminal     bool
		want           bool
	}{
		{
			name:           "legacy_pretty_true_wins_over_format_json",
			format:         FormatJSON,
			legacyPretty:   true,
			otlpLogsActive: false,
			isTerminal:     false,
			want:           true,
		},
		{
			name:           "legacy_pretty_true_wins_over_obs_active",
			format:         FormatAuto,
			legacyPretty:   true,
			otlpLogsActive: true,
			isTerminal:     true,
			want:           true,
		},
		{
			name:           "format_console_always_pretty",
			format:         FormatConsole,
			legacyPretty:   false,
			otlpLogsActive: false,
			isTerminal:     false,
			want:           true,
		},
		{
			name:           "format_pretty_alias_is_console",
			format:         "pretty",
			legacyPretty:   false,
			otlpLogsActive: false,
			isTerminal:     false,
			want:           true,
		},
		{
			name:           "format_console_case_insensitive",
			format:         "CONSOLE",
			legacyPretty:   false,
			otlpLogsActive: false,
			isTerminal:     false,
			want:           true,
		},
		{
			name:           "format_json_always_structured",
			format:         FormatJSON,
			legacyPretty:   false,
			otlpLogsActive: false,
			isTerminal:     true,
			want:           false,
		},
		{
			name:           "format_structured_alias_is_json",
			format:         "structured",
			legacyPretty:   false,
			otlpLogsActive: false,
			isTerminal:     true,
			want:           false,
		},
		{
			name:           "auto_tty_no_otlp_pretty",
			format:         FormatAuto,
			legacyPretty:   false,
			otlpLogsActive: false,
			isTerminal:     true,
			want:           true,
		},
		{
			name:           "auto_tty_with_otlp_json",
			format:         FormatAuto,
			legacyPretty:   false,
			otlpLogsActive: true,
			isTerminal:     true,
			want:           false,
		},
		{
			name:           "auto_no_tty_no_otlp_json",
			format:         FormatAuto,
			legacyPretty:   false,
			otlpLogsActive: false,
			isTerminal:     false,
			want:           false,
		},
		{
			name:           "empty_format_treated_as_auto_tty",
			format:         "",
			legacyPretty:   false,
			otlpLogsActive: false,
			isTerminal:     true,
			want:           true,
		},
		{
			name:           "whitespace_format_treated_as_auto",
			format:         "   ",
			legacyPretty:   false,
			otlpLogsActive: false,
			isTerminal:     true,
			want:           true,
		},
		{
			name:           "unknown_format_falls_back_to_auto",
			format:         "yaml",
			legacyPretty:   false,
			otlpLogsActive: false,
			isTerminal:     true,
			want:           true,
		},
		{
			name:           "unknown_format_no_tty_is_json",
			format:         "yaml",
			legacyPretty:   false,
			otlpLogsActive: false,
			isTerminal:     false,
			want:           false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ResolvePretty(tc.format, tc.legacyPretty, tc.otlpLogsActive, tc.isTerminal)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestStdoutIsTerminalDoesNotPanic(t *testing.T) {
	// Under `go test`, stdout is usually not a TTY (it's a pipe to the test
	// runner). We don't care about the exact return value — we only verify
	// the function is safe to call and returns a bool without crashing.
	assert.NotPanics(t, func() {
		_ = StdoutIsTerminal()
	})
}
