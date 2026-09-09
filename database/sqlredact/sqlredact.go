// Package sqlredact scrubs credential literals out of SQL statement text before
// that text reaches a log field, a span attribute, or an error message.
//
// PostgreSQL's CREATE/ALTER ROLE ... PASSWORD '<literal>' and Oracle's
// CREATE/ALTER USER ... IDENTIFIED BY <password> take no bind parameter for the
// secret, so it never travels in args and the logger's SensitiveDataFilter,
// which masks by field NAME, cannot see it. Vendor scope is those two.
//
// The rule is deliberately blunt: find the first credential keyword followed by
// a value, keep everything up to and including that keyword, and DROP THE WHOLE
// REMAINDER. The credential always follows its keyword, so the dropped tail is
// where every hard case lives — dollar quoting, E'…' and U&'…' prefixes, doubled
// quotes, spaces inside the literal, Oracle's REPLACE <old> and BY VALUES
// <hash>, unterminated literals, and a decoy literal ending in the keyword. None
// can leak, because none is examined. Under-redaction would need the keyword to
// be absent, and then no vendor reads the statement as credential DDL either.
//
// The accepted cost is over-redaction: a keyword sitting in front of something
// quote-shaped costs the statement its tail. Trailing clauses after a credential
// are dropped by design — a truncated log line beats a leaked password.
package sqlredact

// redactedMarker replaces the credential value. It matches the convention the
// migration role provisioner has used in its own error summaries.
const redactedMarker = "[REDACTED]"

// Keywords are matched whole-word and ASCII case-insensitively.
const (
	kwPassword   = "password"
	kwIdentified = "identified"
	kwBy         = "by"
)

// Replacement tails appended after the preserved prefix, which ends at the
// keyword (at BY, for the Oracle form) exactly as the input spells it.
const (
	pgPasswordTail = " '" + redactedMarker + "'"
	oracleByTail   = " " + redactedMarker
)

// Statement returns sql truncated at its first credential clause, with the value
// replaced by a fixed marker.
//
// PASSWORD qualifies when the next non-whitespace byte begins a value: a quote,
// a dollar sign, or a letter introducing an E'…' / U&'…' constant. It does not
// qualify before `=`, `,`, `)`, `;`, end of input, or a bare word such as NULL
// or FROM, so DML naming a password column is untouched. IDENTIFIED qualifies
// when the next word is BY, which is what separates it from IDENTIFIED
// EXTERNALLY and IDENTIFIED GLOBALLY.
//
// When nothing qualifies the input string itself is returned, unallocated.
// Callers must scrub BEFORE truncating for length: a length cut can remove the
// very keyword this rule depends on.
func Statement(sql string) string {
	for i := 0; i < len(sql); i++ {
		if end, ok := wordAt(sql, i, kwPassword); ok {
			if startsValue(sql, skipSpace(sql, end)) {
				return sql[:end] + pgPasswordTail
			}
			i = end - 1
			continue
		}
		if end, ok := wordAt(sql, i, kwIdentified); ok {
			// The prefix runs through BY as the input spells it, so the
			// statement's own casing and spacing survive.
			if byEnd, isBy := wordAt(sql, skipSpace(sql, end), kwBy); isBy {
				return sql[:byEnd] + oracleByTail
			}
			i = end - 1
		}
	}
	return sql
}

// wordAt reports whether keyword occupies sql[i:] as a whole word, compared
// ASCII case-insensitively, and returns the offset just past it. keyword must be
// lowercase ASCII. The boundary check keeps password_hash from matching.
func wordAt(sql string, i int, keyword string) (end int, ok bool) {
	end = i + len(keyword)
	if end > len(sql) {
		return 0, false
	}
	if i > 0 && isWordByte(sql[i-1]) {
		return 0, false
	}
	if end < len(sql) && isWordByte(sql[end]) {
		return 0, false
	}
	for j := 0; j < len(keyword); j++ {
		if lowerASCII(sql[i+j]) != keyword[j] {
			return 0, false
		}
	}
	return end, true
}

// startsValue reports whether a credential value begins at i: a quoted or
// dollar-quoted constant, or a letter introducing a prefixed one (E'…', U&'…').
func startsValue(sql string, i int) bool {
	if i >= len(sql) {
		return false
	}
	switch sql[i] {
	case '\'', '"', '$':
		return true
	}
	if !isLetter(sql[i]) {
		return false
	}
	if i+1 < len(sql) && sql[i+1] == '\'' {
		return true
	}
	return i+2 < len(sql) && sql[i+1] == '&' && sql[i+2] == '\''
}

func skipSpace(sql string, i int) int {
	for i < len(sql) && isSpace(sql[i]) {
		i++
	}
	return i
}

func isWordByte(c byte) bool { return c == '_' || isLetter(c) || ('0' <= c && c <= '9') }

func isLetter(c byte) bool { return ('a' <= c && c <= 'z') || ('A' <= c && c <= 'Z') }

func lowerASCII(c byte) byte {
	if 'A' <= c && c <= 'Z' {
		return c + 'a' - 'A'
	}
	return c
}

func isSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\v' || c == '\f'
}
