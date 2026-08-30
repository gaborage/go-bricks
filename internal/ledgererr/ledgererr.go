// Package ledgererr bounds the diagnostic text a ledger writes to its error
// column. The outbox's relay and the inbox's hold both persist an error whose
// text is not theirs — err.Error() from a broker, a driver or a consumer's own
// handler — into a column both ledgers declare unbounded.
package ledgererr

import (
	"strings"
	"unicode"
)

// MaxBytes bounds the diagnostic text a ledger writes to its error column. Both
// ledgers declare that column unbounded (`error TEXT` on PostgreSQL,
// `error_msg CLOB` on Oracle), and the value written there
// is not ours: it is err.Error() from a broker or driver, which can carry
// server-supplied text of any length. A record that keeps failing rewrites the
// column every cycle, so an unbounded error is unbounded storage per retry, on
// the one table a service cannot drop.
//
// 1 KiB holds a broker error with its context and truncates only the pathological.
const MaxBytes = 1024

// TruncationMarker is appended in place of the bytes dropped, so a reader can
// tell a short error from a shortened one.
const TruncationMarker = "...[truncated]"

// Bound makes an arbitrary error string safe to store.
//
// Unlike an inbound trace identifier — where truncation silently forges
// correlation by mapping distinct upstream ids onto one, so a bad value is
// DISCARDED — this text is diagnostic and nothing keys on it. A truncated error
// still says what went wrong, while discarding one would throw away the only
// record of why a record is stuck. So truncation is the right answer here, and
// the marker keeps it honest.
//
// Three things happen, in order:
//   - Invalid UTF-8 is dropped. PostgreSQL rejects it outright, which would fail
//     the UPDATE and leave retry_count un-advanced — a record retrying forever
//     because the framework could not write down why it failed.
//   - Control bytes become spaces. This text is read back into logs and
//     dashboards, and a broker-supplied newline should not be able to forge a
//     log line there. Only control bytes: ToValidUTF8 has already removed every
//     invalid sequence, so a U+FFFD reaching here is one the sender actually
//     wrote, and substituting it would drop a character nothing is wrong with.
//   - The result is capped without leaving a half-encoded character in the
//     column.
func Bound(errMsg string) string {
	cleaned := strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, strings.ToValidUTF8(errMsg, ""))

	if len(cleaned) <= MaxBytes {
		return cleaned
	}

	// Slicing at a byte offset can land mid-rune; ToValidUTF8 drops the partial
	// tail, so the column never receives a half-encoded character. Doing it this
	// way rather than walking back to a rune start keeps the boundary out of the
	// code: there is no index to be off by one on.
	keep := MaxBytes - len(TruncationMarker)
	return strings.ToValidUTF8(cleaned[:keep], "") + TruncationMarker
}
