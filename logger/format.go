package logger

import (
	"os"
	"strings"

	"golang.org/x/term"
)

// Log format constants used by config.OutputConfig.Format.
// "pretty" and "structured" are accepted aliases for "console" and "json".
const (
	FormatAuto    = "auto"
	FormatConsole = "console"
	FormatJSON    = "json"
)

// ResolvePretty decides whether the logger should run in pretty (console) mode.
//
// legacyPretty=true wins over format because users opting in via the older
// log.pretty boolean expect their preference to stick. Combined with
// otlpLogsActive=true it will panic at startup via WithOTelProvider — that
// fail-fast is intentional: silently muting the log shipping pipeline would
// be a worse outcome than a clear startup error.
func ResolvePretty(format string, legacyPretty, otlpLogsActive, isTerminal bool) bool {
	if legacyPretty {
		return true
	}

	switch strings.ToLower(strings.TrimSpace(format)) {
	case FormatConsole, "pretty":
		return true
	case FormatJSON, "structured":
		return false
	default:
		return isTerminal && !otlpLogsActive
	}
}

// StdoutIsTerminal reports whether os.Stdout is attached to a terminal.
func StdoutIsTerminal() bool {
	return term.IsTerminal(int(os.Stdout.Fd()))
}
