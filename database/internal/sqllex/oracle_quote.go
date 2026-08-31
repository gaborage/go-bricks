package sqllex

import "strings"

// This file owns the RENDERING half of the identifier story the same way
// identifier.go owns the grammar half: one implementation of quoting,
// escaping, and qualified-name splitting, shared by the query builder and the
// columns package. A second copy of an injection-boundary rule is the defect
// ADR-082 exists to stop; the columns package carried exactly such a copy of
// the quoting rule (without the escaping and dotted-name hardenings) until the
// logic moved here.

// QuoteOracleIdentifier applies Oracle's quoting rules to an identifier:
// qualified names are split on separator dots and each segment quoted on its
// own (#1151), an already-quoted segment passes through unchanged, and a
// segment that is a reserved word or falls outside the bare-identifier
// alphabet is wrapped with its interior quotes doubled (#1104).
func QuoteOracleIdentifier(column string) string {
	trimmed := strings.TrimSpace(column)
	if trimmed == "" {
		return trimmed
	}

	// Fast path: a bare segment (no dot, no quote) cannot split and cannot
	// already be quoted, so the parser never needs to run for it — the
	// overwhelming case at every door.
	if strings.IndexByte(trimmed, '.') < 0 && strings.IndexByte(trimmed, '"') < 0 {
		if IsOracleReservedWord(trimmed) || oracleNeedsQuoting(trimmed) {
			return QuoteIdentifierLiteral(trimmed)
		}
		return trimmed
	}

	if IsQuotedIdentifier(trimmed) {
		return trimmed
	}

	if segments := SplitIdentifierSegments(trimmed); len(segments) > 1 {
		for i, segment := range segments {
			segments[i] = QuoteOracleIdentifier(segment)
		}
		return strings.Join(segments, ".")
	}

	// A single segment still carrying a quote or dot: not well-formed quoted
	// (that returned above), so escape it whole if it needs quoting.
	// oracleNeedsQuoting allows only [A-Za-z0-9_$#], so such a segment always
	// fails it and reaches the escaping branch — a quote can never end the
	// identifier early.
	if IsOracleReservedWord(trimmed) || oracleNeedsQuoting(trimmed) {
		return QuoteIdentifierLiteral(trimmed)
	}

	return trimmed
}

func oracleNeedsQuoting(identifier string) bool {
	if identifier == "" {
		return false
	}

	first := identifier[0]
	if first >= '0' && first <= '9' {
		return true
	}

	for i := 0; i < len(identifier); i++ {
		if !isValidIdentifierChar(identifier[i]) {
			return true
		}
	}

	return false
}

// QuoteIdentifierLiteral wraps text in quotes with every interior quote doubled,
// which is how Oracle and PostgreSQL both spell a quote inside a name. A quote
// left undoubled ends the identifier early, so the remainder is parsed as SQL.
//
// Collapsing precedes doubling because a key arrives in escaped form: `a""b`
// already denotes the one-quote name `a"b`, the reading the upsert door applies
// to it. Doubling blind would rename that column. Collapsing first makes the
// pass idempotent — an already-escaped key survives unchanged, and a lone quote
// is the only thing that gains a partner.
func QuoteIdentifierLiteral(text string) string {
	if !strings.ContainsRune(text, '"') {
		// The overwhelming case, and the hot one: one scan, one allocation, the
		// same cost this had before it learned to escape.
		return `"` + text + `"`
	}
	collapsed := strings.ReplaceAll(text, `""`, `"`)
	return `"` + strings.ReplaceAll(collapsed, `"`, `""`) + `"`
}

// IsQuotedIdentifier reports whether text is ALREADY a well-formed quoted
// identifier: wrapped in quotes with every interior quote doubled. Re-quoting
// one of these would change the name it denotes, which is why the renderers pass
// it through. Wrapping alone does not qualify — `role" = 'admin', "name` is
// wrapped and is two SQL tokens.
func IsQuotedIdentifier(text string) bool {
	return IsQuotedString(text) && !HasUnescapedQuote(text[1:len(text)-1])
}

// IsQuotedString reports whether a string is fully enclosed in double quotes.
func IsQuotedString(s string) bool {
	return len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"'
}

// HasUnescapedQuote reports whether text carries a quote that is not part of a
// doubled "" escape. It reads renderings only: the builder's upsert door judges
// the KEY with its stricter keyCarriesInteriorQuote check — a rendering may
// legitimately carry a doubled quote, a caller's identifier argument may not.
func HasUnescapedQuote(text string) bool {
	return strings.Contains(strings.ReplaceAll(text, `""`, ""), `"`)
}

// SplitIdentifierSegments splits a qualified identifier on the dots that
// SEPARATE segments, leaving a dot inside a quoted segment where it belongs:
// `"my.col"` is one column, not two (#1151). Both identifier renderers compose
// it the same way, so the fallback rule lives here once rather than at each.
//
// A string parseQualifiedIdentifier rejects — unbalanced quotes — comes back as
// a single segment. Rendering fewer segments than the caller wrote would be the
// silent variant; one whole (escaped) identifier is not.
func SplitIdentifierSegments(identifier string) []string {
	if segments, ok := parseQualifiedIdentifier(identifier); ok {
		return segments
	}
	return []string{identifier}
}

// parseQualifiedIdentifier splits a qualified identifier into segments and validates them
// Handles quoted segments and ensures balanced quotes
func parseQualifiedIdentifier(name string) ([]string, bool) {
	parser := &identifierParser{
		input: name,
	}
	return parser.parse()
}

// identifierParser encapsulates the state and logic for parsing qualified identifiers
type identifierParser struct {
	input    string
	inQuotes bool
	segments []string
	current  strings.Builder
}

// parse processes the input string and returns the parsed segments
func (p *identifierParser) parse() ([]string, bool) {
	for i := 0; i < len(p.input); i++ {
		c := p.input[i]
		switch c {
		case '"':
			if p.handleQuote(i) {
				i++ // skip escaped quote partner
			}
		case '.':
			if !p.handleDot() {
				return nil, false
			}
		default:
			p.current.WriteByte(c)
		}
	}
	return p.finalize()
}

// handleQuote processes quote characters and escaped quotes
func (p *identifierParser) handleQuote(pos int) bool {
	// Handle escaped quotes: "" inside quoted string
	if p.inQuotes && pos+1 < len(p.input) && p.input[pos+1] == '"' {
		p.current.WriteByte('"')
		p.current.WriteByte('"')
		return true // indicate to skip next character
	}
	p.inQuotes = !p.inQuotes
	p.current.WriteByte('"')
	return false
}

// handleDot processes dot separators (only outside quotes)
func (p *identifierParser) handleDot() bool {
	if p.inQuotes {
		p.current.WriteByte('.')
		return true
	}
	s := strings.TrimSpace(p.current.String())
	if s == "" {
		return false
	}
	p.segments = append(p.segments, s)
	p.current.Reset()
	return true
}

// finalize completes parsing and validates the result
func (p *identifierParser) finalize() ([]string, bool) {
	s := strings.TrimSpace(p.current.String())
	if s == "" {
		return nil, false
	}
	p.segments = append(p.segments, s)
	// Reject unbalanced quotes
	if p.inQuotes {
		return nil, false
	}
	return p.segments, true
}

// ValidateSegment checks if a segment is valid (quoted or unquoted)
func ValidateSegment(segment string) bool {
	if segment[0] == '"' {
		return isValidQuotedSegment(segment)
	}
	return isValidIdentifierSegment(segment)
}

func isLetter(c byte) bool {
	return (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z')
}

func isValidIdentifierChar(c byte) bool {
	return isLetter(c) || (c >= '0' && c <= '9') || c == '_' || c == '$' || c == '#'
}

// isValidOracleIdentifierStart checks if a character can start an Oracle identifier
// Oracle identifiers must start with a letter (A-Z, a-z)
func isValidOracleIdentifierStart(c byte) bool {
	return isLetter(c)
}

// isValidIdentifierSegment validates a single unquoted identifier segment
// Must start with letter and contain only valid identifier characters
func isValidIdentifierSegment(segment string) bool {
	if segment == "" {
		return false
	}

	// Must start with a letter (fixes the bug with "1COUNT" etc.)
	if !isValidOracleIdentifierStart(segment[0]) {
		return false
	}

	// All characters must be valid identifier characters
	for i := 0; i < len(segment); i++ {
		if !isValidIdentifierChar(segment[i]) {
			return false
		}
	}

	return true
}

// isValidQuotedSegment validates a quoted identifier segment: properly
// quoted and non-empty inside the quotes.
func isValidQuotedSegment(segment string) bool {
	return IsQuotedString(segment) && segment[1:len(segment)-1] != ""
}
