// Package sqlredact scrubs credential literals out of SQL statement text before
// that text reaches a log field, a span attribute, or an error message.
//
// PostgreSQL's CREATE/ALTER ROLE ... PASSWORD '<literal>' and Oracle's
// CREATE/ALTER USER ... IDENTIFIED BY <password> take no bind parameter for the
// secret, so it never travels in args and the logger's SensitiveDataFilter,
// which masks by field NAME, cannot see it. Vendor scope is those two.
//
// The rule never examines the credential: find the first credential keyword
// followed by a value, keep everything up to and including that keyword, and
// drop the whole remainder. Since the value always follows its keyword, every
// shape a matcher would have to understand — dollar quoting, prefixed constants,
// doubled quotes, Oracle's trailing REPLACE and BY VALUES, unterminated literals
// — is in the discarded tail. Over-redaction is the accepted cost: a keyword in
// front of something quote-shaped costs the statement its tail, and a delimiter
// opened before the keyword, such as the `(` of an OPTIONS list, is left
// unbalanced by design.
//
// What is NOT scrubbed: a password inside a connection-string literal, as in
// CREATE SUBSCRIPTION ... CONNECTION 'host=h password=x' or a dblink argument.
// Qualifying on `=` would truncate ordinary DML such as
// UPDATE users SET password = crypt($1, gen_salt('bf')).
package sqlredact

import "strings"

const redactedMarker = "[REDACTED]"

const (
	kwPassword   = "password"
	kwIdentified = "identified"
	kwBy         = "by"
)

// Tails appended after the preserved prefix, which ends at the keyword — at BY,
// for a complete Oracle clause — exactly as the input spells it.
const (
	pgPasswordTail = " '" + redactedMarker + "'"
	redactedTail   = " " + redactedMarker
)

// Statement returns sql truncated at its first credential clause, with the value
// replaced by a fixed marker.
//
// PASSWORD qualifies when the next byte past any whitespace or comment begins a
// value: a single quote, a dollar sign, or a letter introducing an E'…' / U&'…'
// constant. It does not qualify before `=`, `,`, `)`, `;`, end of input, or a
// bare word such as NULL or FROM, so DML naming a password column is untouched.
// IDENTIFIED qualifies when the next word is BY. Either keyword also qualifies
// when the gap behind it runs into an unterminated comment, since the value can
// no longer be located: this text reaches the log on the driver-error path,
// where malformed statements are exactly what arrives.
//
// When nothing qualifies the input string itself is returned, unallocated.
// Callers must scrub BEFORE truncating for length: a length cut can remove the
// very keyword this rule depends on.
func Statement(sql string) string {
	for i := range len(sql) {
		if end, ok := wordAt(sql, i, kwPassword); ok {
			gap, truncated := skipGap(sql, end)
			if truncated || startsValue(sql, gap) {
				return sql[:end] + pgPasswordTail
			}
			continue
		}
		if end, ok := wordAt(sql, i, kwIdentified); ok {
			gap, truncated := skipGap(sql, end)
			if truncated {
				return sql[:end] + redactedTail
			}
			if byEnd, isBy := wordAt(sql, gap, kwBy); isBy {
				return sql[:byEnd] + redactedTail
			}
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

// startsValue reports whether a PASSWORD value begins at i. PostgreSQL takes a
// single-quoted string constant, plain or dollar-quoted or prefixed; a
// double-quoted token is an identifier, never a password.
func startsValue(sql string, i int) bool {
	if i >= len(sql) {
		return false
	}
	switch sql[i] {
	case '\'', '$':
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

// skipGap advances past the whitespace and comments a lexer allows between two
// tokens. truncated reports that the gap ran to the end of input inside an
// unterminated block comment, leaving the value unlocatable.
func skipGap(sql string, i int) (next int, truncated bool) {
	for i < len(sql) {
		if isSpace(sql[i]) {
			i++
			continue
		}
		end, closed, ok := skipComment(sql, i)
		if !ok {
			return i, false
		}
		if !closed {
			return end, true
		}
		i = end
	}
	return i, false
}

// skipComment advances past a -- line comment or a /* block comment */ at i.
// A line comment ends at CR as well as LF, and block comments nest, both
// matching PostgreSQL. A line comment running to end of input is closed; an
// unterminated block comment is not.
func skipComment(sql string, i int) (next int, closed, ok bool) {
	switch {
	case strings.HasPrefix(sql[i:], "--"):
		// Search past the leading "--" so an empty comment (a bare "--\n") reports
		// the newline at offset 0 rather than looking unterminated.
		if end := strings.IndexAny(sql[i+2:], "\n\r"); end >= 0 {
			return i + 2 + end, true, true
		}
		return len(sql), true, true
	case strings.HasPrefix(sql[i:], "/*"):
		depth, j := 1, i+2
		for j+1 < len(sql) {
			switch {
			case sql[j] == '/' && sql[j+1] == '*':
				depth++
				j += 2
			case sql[j] == '*' && sql[j+1] == '/':
				depth--
				j += 2
				if depth == 0 {
					return j, true, true
				}
			default:
				j++
			}
		}
		return len(sql), false, true
	}
	return i, false, false
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
