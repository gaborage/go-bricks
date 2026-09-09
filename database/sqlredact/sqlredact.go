// Package sqlredact scrubs credential literals out of SQL statement text before
// that text reaches a log field, a span attribute, or an error message.
//
// Some DDL grammars take no bind parameter for a secret: PostgreSQL's
// CREATE/ALTER ROLE ... PASSWORD '<literal>' and Oracle's CREATE/ALTER USER ...
// IDENTIFIED BY <password> both embed the credential in the statement itself, so
// the value never travels in args and the logger's SensitiveDataFilter — which
// masks by field NAME — cannot see it. The scrub here works by SHAPE instead:
// it recognizes the two credential-bearing clauses and replaces only the value.
// It is deliberately not a general string-literal strip, so the statement stays
// diagnosable and non-credential SQL is returned untouched.
//
// Recognition is done by a single-pass scanner rather than a regular expression.
// The scanner is a literal/comment skipper, NOT a SQL tokenizer: it recognizes
// exactly a single-quoted literal with ” doubling, an E'…' constant with
// backslash escapes, a U&'…' constant, a "double quoted" identifier with ""
// doubling, a $tag$…$tag$ body (empty tag included, nesting not handled), a --
// line comment and a /* block comment */. Nothing else. Everything outside those
// is plain text to it.
//
// Known limits, all of which fail toward over-redaction: a body that only
// EXECUTEs credential DDL as a string is not reached (the string is data at
// every level), a secret embedded in a connection string rather than a
// credential clause (CREATE SUBSCRIPTION ... CONNECTION 'password=...') is an
// arbitrary literal and out of scope, and the scanner assumes standard_conforming_strings is on, so
// with the non-default setting a backslash-terminated literal can end early. It
// fails closed everywhere it loses state — an unterminated literal, identifier,
// comment or dollar block that still names a credential keyword redacts to the
// end of the input rather than silently ending the scan.
//
// A pattern cannot track quote state, so a decoy literal ending in the keyword
// ("SELECT 'PASSWORD '; ALTER ROLE r PASSWORD 'secret'") mis-anchors it and the
// output reads as redacted while the secret survives verbatim — the worst
// possible failure for a scrub. The scanner also reaches shapes no single RE2
// pattern can: dollar-quoted constants, whose closing tag would need a
// backreference, and a comment sitting between the keyword and its value.
package sqlredact

import "strings"

// redactedMarker replaces the credential value. It matches the convention the
// migration role provisioner has used in its own error summaries.
const redactedMarker = "[REDACTED]"

// quotedMarker is the PostgreSQL form: the whole string constant is replaced,
// so the marker carries its own quotes and the clause stays well-formed.
const quotedMarker = "'" + redactedMarker + "'"

// Keywords every scrubbable clause must contain, used as the cheap pre-filter
// in front of the scanner. Lowercase ASCII, as containsFold requires.
const (
	kwPassword   = "password"
	kwIdentified = "identified"
	kwBy         = "by"
	kwValues     = "values"
	kwReplace    = "replace"
	kwWith       = "with"
)

// redaction marks a byte range of the input to be replaced by marker.
type redaction struct {
	start  int
	end    int
	marker string
}

// Statement returns sql with every credential literal replaced by a fixed marker.
//
// It handles PostgreSQL `PASSWORD <string constant>` in all of that grammar's
// spellings — plain, doubled-quote escaped, E'…' and U&'…' prefixed, and
// dollar-quoted — and Oracle `IDENTIFIED BY <token>`, including its
// `REPLACE <old password>` and `BY VALUES <verifier>` forms. Keywords match
// case-insensitively and tolerate any separator, newlines and SQL comments
// included. Only a keyword found OUTSIDE a string literal, quoted identifier or
// comment counts.
//
// Anything else — `PASSWORD NULL`, `PASSWORD $1`, `IDENTIFIED EXTERNALLY`,
// `IDENTIFIED GLOBALLY`, ordinary DML with quoted values, non-SQL input — is
// returned byte for byte, as the input string itself. Callers must scrub BEFORE
// truncating: a cut inside the literal hides the closing quote, and the value
// then reads as an unterminated constant rather than a credential.
func Statement(sql string) string {
	// Cheap reject for the overwhelmingly common query that carries no credential
	// clause at all, so the scanner never walks it. The window is a fixed-length
	// ASCII fold, marginally tighter than the scanner's own Unicode-folding word
	// compare: a keyword spelled with a rune that case-folds to ASCII would be
	// rejected here. No server folds a keyword that way, so the gap is not
	// reachable in SQL either engine accepts.
	if !containsFold(sql, kwPassword) && !containsFold(sql, kwIdentified) {
		return sql
	}
	spans := credentialSpans(sql)
	if len(spans) == 0 {
		return sql
	}
	return apply(sql, spans)
}

// maxBodyDepth bounds recursion into dollar-quoted bodies. Two levels covers a
// DO block nesting one function body; the cap exists so pathological input
// cannot drive the stack.
const maxBodyDepth = 4

// credentialSpans scans sql once and returns the byte ranges holding credential
// values, in ascending order. Text inside a string constant, a quoted identifier
// or a comment is skipped wholesale, so a keyword appearing there is inert — the
// exception being a dollar-quoted body, which in DO and CREATE FUNCTION is
// executable code rather than data, so the scan recurses into it.
func credentialSpans(sql string) []redaction {
	return scan(sql, 0)
}

func scan(sql string, depth int) []redaction {
	var spans []redaction
	for i := 0; i < len(sql); {
		found, next, stop := skipRegion(sql, i, depth)
		spans = append(spans, found...)
		if stop {
			return spans
		}
		if next > i {
			i = next
			continue
		}
		word, wordEnd := readWord(sql, i)
		if word == "" {
			i++
			continue
		}
		clause, end := clauseAt(sql, word, wordEnd)
		if len(clause) > 0 {
			spans = append(spans, clause...)
			i = end
			continue
		}
		i = wordEnd
	}
	return spans
}

// skipRegion consumes the non-keyword region at i, if there is one: a
// dollar-quoted body (scanned recursively, since DO and CREATE FUNCTION bodies
// are code), or a literal, identifier or comment (skipped whole). next == i
// means i is plain text the caller should read as a word. stop reports that the
// scanner lost quote state and has already emitted its fail-closed span.
func skipRegion(sql string, i, depth int) (spans []redaction, next int, stop bool) {
	if start, end, ok := dollarQuotedBody(sql, i); ok {
		return nestedSpans(sql[start:end], start, depth), end + dollarBodyTagLen(sql, i), false
	}
	end, skipped, unterminated := skipOpaque(sql, i)
	switch {
	case !skipped:
		return nil, i, false
	case !unterminated:
		return nil, end, false
	}
	if span, ok := failClosed(sql, i); ok {
		return []redaction{span}, len(sql), true
	}
	return nil, len(sql), true
}

// nestedSpans scans a dollar-quoted body and rebases its spans onto the
// enclosing statement. Beyond maxBodyDepth the body is left alone.
func nestedSpans(body string, offset, depth int) []redaction {
	if depth >= maxBodyDepth {
		// Fail closed at the cap: dropping the body would make nesting a way to
		// hide a credential from the scrub.
		span, ok := failClosed(body, 0)
		if !ok {
			return nil
		}
		span.start += offset
		span.end += offset
		return []redaction{span}
	}
	inner := scan(body, depth+1)
	for i := range inner {
		inner[i].start += offset
		inner[i].end += offset
	}
	return inner
}

// failClosed returns a span covering sql[i:] when a credential keyword still
// appears there, so a scanner that has lost quote state over-redacts instead of
// giving up.
func failClosed(sql string, i int) (redaction, bool) {
	rest := sql[i:]
	if !containsFold(rest, kwPassword) && !containsFold(rest, kwIdentified) {
		return redaction{}, false
	}
	return redaction{start: i, end: len(sql), marker: redactedMarker}, true
}

// clauseAt dispatches on a keyword read at the top level and returns the
// redactions its clause contributes plus the offset to resume scanning from.
// A nil result means word did not open a credential clause.
func clauseAt(sql, word string, after int) (spans []redaction, end int) {
	switch {
	case strings.EqualFold(word, kwPassword):
		if span, ok := pgPasswordSpan(sql, after); ok {
			return []redaction{span}, span.end
		}
	case strings.EqualFold(word, kwIdentified):
		return oracleIdentifiedSpans(sql, after)
	}
	return nil, after
}

// pgPasswordSpan reads the value of a PostgreSQL PASSWORD clause starting at the
// first byte after the keyword. It reports a span only for a string constant:
// `PASSWORD NULL` carries no secret and `PASSWORD $1` binds one as a parameter,
// so both are left alone. An unterminated constant spans to the end of the input
// — the value is still a credential even when the statement is malformed.
func pgPasswordSpan(sql string, i int) (redaction, bool) {
	start, truncated := skipSeparators(sql, i)
	if truncated {
		return redaction{start: skipSpace(sql, i), end: len(sql), marker: quotedMarker}, true
	}
	end, ok := readStringConstant(sql, start)
	if !ok {
		if !startsStringConstant(sql, start) {
			return redaction{}, false
		}
		end = len(sql)
	}
	return redaction{start: start, end: end, marker: quotedMarker}, true
}

// oracleIdentifiedSpans reads an Oracle IDENTIFIED clause starting at the first
// byte after the keyword. IDENTIFIED EXTERNALLY and IDENTIFIED GLOBALLY carry no
// password and yield nothing. The BY form yields the password token, the
// verifier after BY VALUES, and the old password after a REPLACE — that last one
// is a credential the caller is retiring, not a harmless echo.
func oracleIdentifiedSpans(sql string, i int) (spans []redaction, end int) {
	start, truncated := skipSeparators(sql, i)
	if truncated {
		return []redaction{{start: skipSpace(sql, i), end: len(sql), marker: redactedMarker}}, len(sql)
	}
	word, after := readWord(sql, start)
	if !strings.EqualFold(word, kwBy) {
		return nil, after
	}

	valueStart, truncated := skipSeparators(sql, after)
	if truncated {
		return []redaction{{start: skipSpace(sql, after), end: len(sql), marker: redactedMarker}}, len(sql)
	}
	word, afterWord := readWord(sql, valueStart)
	if strings.EqualFold(word, kwValues) {
		valueStart, truncated = skipSeparators(sql, afterWord)
		if truncated {
			return []redaction{{start: skipSpace(sql, afterWord), end: len(sql), marker: redactedMarker}}, len(sql)
		}
	}

	if _, wordEnd := readWord(sql, valueStart); introducesCredential(sql, valueStart, wordEnd) {
		// Let the scan resume so the PASSWORD clause redacts the literal itself.
		return nil, after
	}

	span, ok := valueSpan(sql, valueStart)
	if !ok {
		return nil, after
	}
	spans, end = append(spans, span), span.end

	// An ALTER USER ... IDENTIFIED BY new REPLACE old carries two passwords.
	next, _ := skipSeparators(sql, end)
	word, afterWord = readWord(sql, next)
	if !strings.EqualFold(word, kwReplace) {
		return spans, end
	}
	oldStart, oldTruncated := skipSeparators(sql, afterWord)
	if oldTruncated {
		return append(spans, redaction{start: skipSpace(sql, afterWord), end: len(sql), marker: redactedMarker}), len(sql)
	}
	if old, okOld := valueSpan(sql, oldStart); okOld {
		spans, end = append(spans, old), old.end
	}
	return spans, end
}

// valueSpan reads one Oracle password token: a string constant, a quoted
// identifier, or a bare run up to the next separator or statement terminator.
func valueSpan(sql string, i int) (redaction, bool) {
	if end, ok := readStringConstant(sql, i); ok {
		return redaction{start: i, end: end, marker: redactedMarker}, true
	}
	if end, ok := readQuotedIdentifier(sql, i); ok {
		return redaction{start: i, end: end, marker: redactedMarker}, true
	}
	// Fail closed: a value that opens a quote and never closes it must not fall
	// through to the bare-token reader, which would stop at the next space and
	// leave the rest of the credential in clear.
	if startsStringConstant(sql, i) || (i < len(sql) && sql[i] == '"') {
		return redaction{start: i, end: len(sql), marker: redactedMarker}, true
	}
	end := i
	for end < len(sql) && !isSpace(sql[end]) && sql[end] != ';' {
		if sql[end] == '\'' || sql[end] == '"' {
			// A quote inside a bare token means the value is not the shape we
			// think it is and the scanner's quote state is now wrong. Fail closed.
			return redaction{start: i, end: len(sql), marker: redactedMarker}, true
		}
		end++
	}
	if end == i {
		return redaction{}, false
	}
	return redaction{start: i, end: end, marker: redactedMarker}, true
}

// introducesCredential reports whether the token at sql[start:end] is a keyword
// carrying the real credential after it rather than being one itself. MySQL
// spells a hash as IDENTIFIED BY PASSWORD '<hash>'; consuming the keyword as the
// value would leave the hash in clear behind a marker that reads as a successful
// scrub. It requires a string constant to actually follow, because `password`
// and `with` are themselves legal Oracle passwords — declining those outright
// abandoned the clause and shipped the REPLACE credential too.
func introducesCredential(sql string, start, end int) bool {
	token := sql[start:end]
	if !strings.EqualFold(token, kwPassword) && !strings.EqualFold(token, kwWith) {
		return false
	}
	next, truncated := skipSeparators(sql, end)
	if truncated {
		return false
	}
	_, ok := readStringConstant(sql, next)
	return ok
}

// apply rewrites sql with each span replaced by its marker. Spans arrive in
// ascending order and never overlap, so one pass suffices.
func apply(sql string, spans []redaction) string {
	var b strings.Builder
	b.Grow(len(sql))
	last := 0
	for _, s := range spans {
		b.WriteString(sql[last:s.start])
		b.WriteString(s.marker)
		last = s.end
	}
	b.WriteString(sql[last:])
	return b.String()
}

// skipOpaque advances past a string constant, quoted identifier or comment
// beginning at i. unterminated reports that one opened and never closed, which
// leaves the scanner unable to tell code from data for the rest of the input.
func skipOpaque(s string, i int) (next int, skipped, unterminated bool) {
	if end, closed, ok := skipComment(s, i); ok {
		return end, true, !closed
	}
	if end, ok := readStringConstant(s, i); ok {
		return end, true, false
	}
	if end, ok := readQuotedIdentifier(s, i); ok {
		return end, true, false
	}
	if startsStringConstant(s, i) || s[i] == '"' {
		return len(s), true, true
	}
	return i, false, false
}

// skipSeparators advances past whitespace and comments, which SQL allows
// anywhere between a keyword and its value. truncated reports that a block
// comment never closed, so the value the caller was reaching for is gone and
// there is nothing left to distinguish from it.
func skipSeparators(s string, i int) (next int, truncated bool) {
	for i < len(s) {
		if isSpace(s[i]) {
			i++
			continue
		}
		end, closed, ok := skipComment(s, i)
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
// A line comment ends at CR as well as LF, matching the server's non_newline
// class — scanning only for LF would let a CR-terminated comment swallow the
// statement behind it. An unterminated block comment reports closed == false;
// a line comment running to the end of input is normal and closed.
func skipComment(s string, i int) (next int, closed, ok bool) {
	switch {
	case strings.HasPrefix(s[i:], "--"):
		if end := strings.IndexAny(s[i:], "\n\r"); end >= 0 {
			return i + end, true, true
		}
		return len(s), true, true
	case strings.HasPrefix(s[i:], "/*"):
		if end := strings.Index(s[i+2:], "*/"); end >= 0 {
			return i + 2 + end + 2, true, true
		}
		return len(s), false, true
	}
	return i, false, false
}

// readWord reads a run of identifier characters at i, returning it and the
// offset after it. It returns "" when i is not on an identifier character.
func readWord(s string, i int) (word string, next int) {
	end := i
	for end < len(s) && isWordByte(s[end]) {
		end++
	}
	return s[i:end], end
}

// readQuotedIdentifier reads a "double quoted" identifier, in which "" is an
// embedded quote.
func readQuotedIdentifier(s string, i int) (next int, ok bool) {
	if i >= len(s) || s[i] != '"' {
		return i, false
	}
	for j := i + 1; j < len(s); j++ {
		if s[j] != '"' {
			continue
		}
		if j+1 < len(s) && s[j+1] == '"' {
			j++
			continue
		}
		return j + 1, true
	}
	return i, false
}

// readStringConstant reads a SQL string constant at i in any of PostgreSQL's
// spellings: plain '…' with ” doubling, a prefixed constant (E'…' with
// backslash escapes, and the N/B/X/U& forms without them), or a dollar-quoted
// $tag$…$tag$ body. It reports false when i does not begin one, or when the
// constant never closes.
func readStringConstant(s string, i int) (next int, ok bool) {
	if i >= len(s) {
		return i, false
	}
	if s[i] == '$' {
		return readDollarQuoted(s, i)
	}
	prefix := stringPrefixLen(s, i)
	if prefix < 0 {
		return i, false
	}
	escapes := prefix == 1 && (s[i] == 'E' || s[i] == 'e')
	for j := i + prefix + 1; j < len(s); j++ {
		switch {
		case escapes && s[j] == '\\':
			j++
		case s[j] == '\'':
			if j+1 < len(s) && s[j+1] == '\'' {
				j++
				continue
			}
			return j + 1, true
		}
	}
	return i, false
}

// stringPrefixLen returns the length of the constant prefix at i (0 for a bare
// '…', 1 for E/N/B/X, 2 for U&) or -1 when no string constant starts there.
func stringPrefixLen(s string, i int) int {
	if s[i] == '\'' {
		return 0
	}
	if i+2 < len(s) && (s[i] == 'U' || s[i] == 'u') && s[i+1] == '&' && s[i+2] == '\'' {
		return 2
	}
	if i+1 < len(s) && strings.IndexByte("EeNnBbXx", s[i]) >= 0 && s[i+1] == '\'' {
		return 1
	}
	return -1
}

// startsStringConstant reports whether a string constant begins at i, whether or
// not it closes. Callers use it to tell an unterminated constant from text that
// is not a constant at all.
func startsStringConstant(s string, i int) bool {
	if i >= len(s) {
		return false
	}
	if s[i] == '$' {
		_, tagged := dollarTag(s, i)
		return tagged
	}
	return stringPrefixLen(s, i) >= 0
}

// readDollarQuoted reads a $tag$…$tag$ constant at i. The tag may be empty ($$).
func readDollarQuoted(s string, i int) (next int, ok bool) {
	tag, tagged := dollarTag(s, i)
	if !tagged {
		return i, false
	}
	body := i + len(tag)
	if end := strings.Index(s[body:], tag); end >= 0 {
		return body + end + len(tag), true
	}
	return i, false
}

// dollarTag returns the $tag$ delimiter at i. A tag body may not start with a
// digit, which is what separates $tag$ from the $1 bind-parameter placeholder,
// and a $ directly after an identifier byte is part of that identifier (legal in
// PostgreSQL, pervasive in Oracle's v$ views) rather than an opening delimiter.
func dollarTag(s string, i int) (tag string, ok bool) {
	if i >= len(s) || s[i] != '$' {
		return "", false
	}
	if i > 0 && isWordByte(s[i-1]) {
		return "", false
	}
	j := i + 1
	for j < len(s) && isWordByte(s[j]) {
		if isDigit(s[j]) && j == i+1 {
			return "", false
		}
		j++
	}
	if j >= len(s) || s[j] != '$' {
		return "", false
	}
	return s[i : j+1], true
}

// dollarQuotedBody returns the bounds of the body inside a $tag$…$tag$ constant
// at i, excluding both delimiters.
func dollarQuotedBody(s string, i int) (start, end int, ok bool) {
	tag, tagged := dollarTag(s, i)
	if !tagged {
		return 0, 0, false
	}
	start = i + len(tag)
	offset := strings.Index(s[start:], tag)
	if offset < 0 {
		return 0, 0, false
	}
	return start, start + offset, true
}

// dollarBodyTagLen returns the length of the closing delimiter of the
// dollar-quoted constant at i, so the caller can resume past it.
func dollarBodyTagLen(s string, i int) int {
	tag, tagged := dollarTag(s, i)
	if !tagged {
		return 0
	}
	return len(tag)
}

// containsFold reports whether s contains needle, comparing ASCII letters
// case-insensitively and without allocating. needle must be lowercase ASCII.
func containsFold(s, needle string) bool {
	for i := 0; i+len(needle) <= len(s); i++ {
		if strings.EqualFold(s[i:i+len(needle)], needle) {
			return true
		}
	}
	return false
}

// skipSpace advances past whitespace only, leaving comments in place. The
// fail-closed spans use it so a redaction starts at the offending construct
// rather than swallowing the whitespace that separates it from the keyword.
func skipSpace(s string, i int) int {
	for i < len(s) && isSpace(s[i]) {
		i++
	}
	return i
}

// isWordByte reports whether c can appear inside an identifier or a dollar-quote
// tag. Bytes >= 0x80 count: the server's ident_cont and dolq_cont classes both
// include \200-\377, so a tag such as $tag$ with a non-ASCII letter is real and
// must not be mistaken for loose text.
func isWordByte(c byte) bool {
	return c == '_' || c >= 0x80 || isDigit(c) || ('a' <= c && c <= 'z') || ('A' <= c && c <= 'Z')
}

func isDigit(c byte) bool { return '0' <= c && c <= '9' }

func isSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\v' || c == '\f'
}
