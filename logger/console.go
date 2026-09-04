package logger

import (
	"strconv"

	"github.com/rs/zerolog"
)

// quoteControlMessage is the ConsoleWriter.FormatPrepare hook. The console writer renders
// the event message verbatim, so a request-derived message carrying a newline forges log
// lines. A message with any control byte is Go-quoted so the event stays on one line;
// every other message renders exactly as before. JSON mode never reaches this hook.
func quoteControlMessage(evt map[string]any) error {
	msg, ok := evt[zerolog.MessageFieldName].(string)
	if ok && hasControlBytes(msg) {
		evt[zerolog.MessageFieldName] = strconv.Quote(msg)
	}
	return nil
}

// hasControlBytes reports whether s contains a C0 control byte (below 0x20) or DEL (0x7f).
// Spaces and printable UTF-8 are deliberately not control bytes: zerolog's field predicate
// would quote most messages and escape nothing useful.
func hasControlBytes(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < 0x20 || s[i] == 0x7f {
			return true
		}
	}
	return false
}
